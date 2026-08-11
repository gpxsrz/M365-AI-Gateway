package web

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIKeyStoreIgnoresPersistedLegacyPath(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current", "api-keys.json")
	legacy := filepath.Join(dir, "legacy", "api-keys.json")
	if err := os.MkdirAll(filepath.Dir(current), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(`{"Path":`+mustJSON(legacy)+`,"keys":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365_API_KEYS", current)
	store, err := openAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if store.Path != current {
		t.Fatalf("runtime path was replaced by persisted legacy path: %q", store.Path)
	}
	if _, _, err := store.create("regression"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy path was unexpectedly written: %v", err)
	}
	raw, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"Path"`) || strings.Contains(string(raw), legacy) {
		t.Fatalf("runtime path leaked back into persisted API-key JSON: %s", raw)
	}
}

func TestAPIKeyStoreRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-keys.json")
	if err := os.WriteFile(path, []byte(`{"keys":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365_API_KEYS", path)
	if _, err := openAPIKeys(); err == nil || !strings.Contains(err.Error(), "decode API key store") {
		t.Fatalf("malformed store error=%v", err)
	}
}

func TestAPIKeyStoreRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "api-keys.json")
	original := []byte(`{"keys":[]}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	t.Setenv("M365_API_KEYS", link)
	if _, err := openAPIKeys(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink store error=%v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(original) || info.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target was touched: mode=%o raw=%s", info.Mode().Perm(), raw)
	}
}

func TestAPIKeyLastUsedPersistenceIsRateLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-keys.json")
	store := &apiKeyStore{Path: path}
	record, raw, err := store.create("usage")
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	store.persist = func(_ string, _ []byte) error {
		writes.Add(1)
		return nil
	}
	store.lastUsagePersistAttempt = time.Now().Add(-apiKeyUsagePersistInterval)
	for i := 0; i < 100; i++ {
		owner, ok := store.authenticate(raw)
		if !ok || owner != record.ID {
			t.Fatalf("authenticate=%q,%v", owner, ok)
		}
	}
	if got := writes.Load(); got != 1 {
		t.Fatalf("100 successful requests caused %d LastUsedAt persistence writes, want 1", got)
	}
	if store.Keys[0].LastUsedAt == nil {
		t.Fatal("in-memory LastUsedAt was not refreshed")
	}
}
