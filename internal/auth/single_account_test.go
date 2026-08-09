package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenStoreKeepsOnlyNewestAccountAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(TokenSet{AccessToken: "first", HomeOID: "oid-first", TenantID: "tid", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(TokenSet{AccessToken: "second", HomeOID: "oid-second", TenantID: "tid", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 1 || got[0].ID != "oid-second" {
		t.Fatalf("active accounts = %#v", got)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 1 || got[0].ID != "oid-second" {
		t.Fatalf("restarted active accounts = %#v", got)
	}
}

func TestTokenStoreMigratesLegacyPoolToNewestAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	old := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	raw, err := json.Marshal(Cache{Schema: TokenCacheSchema, Accounts: []AccountToken{
		{ID: "older", OID: "older", AccessToken: "older-token", UpdatedAt: old},
		{ID: "newer", OID: "newer", AccessToken: "newer-token", UpdatedAt: newer},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.List(); len(got) != 1 || got[0].ID != "newer" {
		t.Fatalf("migrated active accounts = %#v", got)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 1 || got[0].ID != "newer" {
		t.Fatalf("persisted migrated accounts = %#v", got)
	}
}
