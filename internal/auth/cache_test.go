package auth

import (
	"os"
	"path/filepath"
	"testing"

	"m365-native/internal/privatefile"
	"time"
)

func TestOpenStoreRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "tokens.json")
	original := []byte(`{"schema":"m365-oauth-token-cache/v1","accounts":[]}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := OpenStore(link); err == nil {
		t.Fatal("symlink token cache was accepted")
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

func TestAtomicWritePrivateFileRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "tokens.json")
	original := []byte("do-not-touch")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if err := privatefile.WriteAtomic(link, "token cache", ".m365-private-*", []byte("replacement")); err == nil {
		t.Fatal("atomic private write replaced a symlink path")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(original) {
		t.Fatalf("symlink target content changed: %s", raw)
	}
}

func TestDeletePersistenceFailurePreservesAccount(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.Upsert(TokenSet{
		AccessToken:  "a",
		RefreshToken: "r",
		HomeOID:      "oid-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blocker, "tokens.json")
	if err := store.Delete(account.ID); err == nil {
		t.Fatal("expected delete persistence failure")
	}
	got, ok := store.Get(account.ID)
	if !ok || got.AccessToken != account.AccessToken || got.RefreshToken != account.RefreshToken {
		t.Fatalf("failed delete removed active in-memory account: %#v", got)
	}
}

func TestUpsertAndList(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := store.Upsert(TokenSet{
		AccessToken:  "a",
		RefreshToken: "r",
		Email:        "a@example.com",
		DisplayName:  "A",
		HomeOID:      "oid-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Email != "a@example.com" {
		t.Fatalf("unexpected email: %s", acc.Email)
	}
	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 account, got %d", len(list))
	}
}
