package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAPIKeyAuthenticateReturnsRecordIDWithoutPersistingRawKey(t *testing.T) {
	path := t.TempDir() + "/api-keys.json"
	store := &apiKeyStore{Path: path}
	record, raw, err := store.create("checkpoint owner")
	if err != nil {
		t.Fatal(err)
	}

	owner, ok := store.authenticate(raw)
	if !ok || owner != record.ID {
		t.Fatalf("authenticate() = %q, %v; want %q, true", owner, ok, record.ID)
	}
	if store.Keys[0].LastUsedAt == nil {
		t.Fatal("successful authentication did not update LastUsedAt")
	}
	t.Setenv("M365_API_KEYS", path)
	reloaded := openAPIKeys()
	if len(reloaded.Keys) != 1 || reloaded.Keys[0].LastUsedAt == nil {
		t.Fatal("successful authentication did not persist LastUsedAt")
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), raw) {
		t.Fatal("persisted API-key state contains the raw key")
	}
}

func TestAPIKeyMiddlewareAttachesStableOwnerID(t *testing.T) {
	store := &apiKeyStore{Path: t.TempDir() + "/api-keys.json"}
	record, raw, err := store.create("middleware owner")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{apiKeys: store}
	got := ""
	handler := server.adminMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		got = apiKeyOwner(request)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || got != record.ID {
		t.Fatalf("status=%d owner=%q want=%q", recorder.Code, got, record.ID)
	}
}

func TestAPIKeyAuthenticateRejectsInvalidAndRevokedKeysWithoutOwnerID(t *testing.T) {
	store := &apiKeyStore{Path: t.TempDir() + "/api-keys.json"}
	record, raw, err := store.create("revoked owner")
	if err != nil {
		t.Fatal(err)
	}

	if owner, ok := store.authenticate("not-a-valid-key"); ok || owner != "" {
		t.Fatalf("invalid key authenticate() = %q, %v; want empty, false", owner, ok)
	}
	if revoked, err := store.revoke(record.ID); err != nil || !revoked {
		t.Fatalf("revoke() = %v, %v; want true, nil", revoked, err)
	}
	if owner, ok := store.authenticate(raw); ok || owner != "" {
		t.Fatalf("revoked key authenticate() = %q, %v; want empty, false", owner, ok)
	}
}

func TestAPIKeyOwnerContextRoundTripAndDefaultEmpty(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	if got := apiKeyOwner(request); got != "" {
		t.Fatalf("apiKeyOwner() without authentication = %q; want empty", got)
	}

	request = withAPIKeyOwner(request, "0123456789abcdef")
	if got := apiKeyOwner(request); got != "0123456789abcdef" {
		t.Fatalf("apiKeyOwner() = %q; want stable record ID", got)
	}
}
