package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenEndpointErrorOmitsProviderDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"provider-sensitive-detail"}`))
	}))
	t.Cleanup(server.Close)
	config := OAuthConfig{
		ClientID:          "profile-client",
		Authority:         server.URL + "/common",
		RedirectURI:       server.URL + "/callback",
		Scope:             "openid offline_access profile.read",
		AuthorizeEndpoint: server.URL + "/authorize",
		TokenEndpoint:     server.URL,
	}
	_, err := RefreshWithConfig(config, "refresh-original")
	var endpointError *TokenEndpointError
	if !errors.As(err, &endpointError) || endpointError.Status != http.StatusBadRequest || endpointError.Code != "invalid_grant" {
		t.Fatalf("unexpected token error: %#v", err)
	}
	if strings.Contains(err.Error(), "provider-sensitive-detail") {
		t.Fatalf("token error leaked provider description: %v", err)
	}
}

func TestTokenOperationsUsePinnedOAuthProfileConfig(t *testing.T) {
	var (
		mu    sync.Mutex
		forms []url.Values
	)
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"oid-profile","tid":"tid-profile"}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
			return
		}
		captured := make(url.Values, len(r.Form))
		for key, values := range r.Form {
			captured[key] = append([]string(nil), values...)
		}
		mu.Lock()
		forms = append(forms, captured)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "header." + claims + ".signature",
			"refresh_token": "refresh-next",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)

	config := OAuthConfig{
		ClientID:          "profile-client",
		Authority:         server.URL + "/common",
		RedirectURI:       server.URL + "/callback",
		Scope:             "openid offline_access profile.read",
		AuthorizeEndpoint: server.URL + "/authorize",
		TokenEndpoint:     server.URL,
	}
	if _, err := ExchangeCodeWithConfig(config, "authorization-code", "pkce-verifier", config.RedirectURI); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshWithConfig(config, "refresh-original"); err != nil {
		t.Fatal(err)
	}

	store, err := OpenStoreWithConfig(filepath.Join(t.TempDir(), "accounts.json"), config)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.Upsert(TokenSet{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-store",
		HomeOID:      "oid-store",
		TenantID:     "tid-store",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.EnsureValid(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ClientID != config.ClientID || refreshed.AccessToken == "expired-access" {
		t.Fatalf("refreshed account = %#v", refreshed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(forms) != 3 {
		t.Fatalf("token endpoint calls = %d, want 3", len(forms))
	}
	assertTokenForm := func(index int, grantType string) {
		t.Helper()
		form := forms[index]
		if form.Get("client_id") != config.ClientID || form.Get("scope") != config.Scope || form.Get("grant_type") != grantType {
			t.Fatalf("form[%d] = %v", index, form)
		}
	}
	assertTokenForm(0, "authorization_code")
	if forms[0].Get("code") != "authorization-code" || forms[0].Get("code_verifier") != "pkce-verifier" || forms[0].Get("redirect_uri") != config.RedirectURI {
		t.Fatalf("exchange form = %v", forms[0])
	}
	assertTokenForm(1, "refresh_token")
	if forms[1].Get("refresh_token") != "refresh-original" {
		t.Fatalf("direct refresh form = %v", forms[1])
	}
	assertTokenForm(2, "refresh_token")
	if forms[2].Get("refresh_token") != "refresh-store" {
		t.Fatalf("store refresh form = %v", forms[2])
	}
}

func TestStoreUpsertPersistenceFailurePreservesPreviousAccount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "accounts.json")
	store, err := OpenStoreWithConfig(path, testOAuthConfigForTokenServer("http://127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.Upsert(TokenSet{
		AccessToken:  "previous-access",
		RefreshToken: "previous-refresh",
		HomeOID:      "previous-oid",
		TenantID:     "previous-tid",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blocker, "accounts.json")
	if _, err := store.Upsert(TokenSet{
		AccessToken:  "replacement-access",
		RefreshToken: "replacement-refresh",
		HomeOID:      previous.ID,
		TenantID:     previous.TID,
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err == nil {
		t.Fatal("expected replacement persistence failure")
	}
	got, ok := store.Get(previous.ID)
	if !ok || got.AccessToken != previous.AccessToken || got.RefreshToken != previous.RefreshToken {
		t.Fatalf("failed replacement changed active in-memory credential: %#v", got)
	}
}

func testOAuthConfigForTokenServer(endpoint string) OAuthConfig {
	return OAuthConfig{
		ClientID:          "profile-client",
		Authority:         endpoint + "/common",
		RedirectURI:       endpoint + "/callback",
		Scope:             "openid offline_access profile.read",
		AuthorizeEndpoint: endpoint + "/authorize",
		TokenEndpoint:     endpoint,
	}
}

func expiredStoreForTokenServer(t *testing.T, endpoint string) (*Store, AccountToken, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.json")
	store, err := OpenStoreWithConfig(path, testOAuthConfigForTokenServer(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.Upsert(TokenSet{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-original",
		HomeOID:      "oid-store",
		TenantID:     "tid-store",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, account, path
}

func TestEnsureValidContextCancellationPreservesAccount(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	store, account, path := expiredStoreForTokenServer(t, server.URL)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.EnsureValidContext(ctx, account.ID)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not stop after caller cancellation")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("caller cancellation durably rewrote the token cache")
	}
	got, _ := store.Get(account.ID)
	if got.Status != "online" {
		t.Fatalf("caller cancellation changed account status to %q", got.Status)
	}
}

func TestEnsureValidTransientRefreshFailureDoesNotExpireAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"provider detail"}`))
	}))
	t.Cleanup(server.Close)
	store, account, path := expiredStoreForTokenServer(t, server.URL)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureValidContext(context.Background(), account.ID); err == nil {
		t.Fatal("expected transient refresh failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("transient refresh failure durably rewrote the token cache")
	}
	got, _ := store.Get(account.ID)
	if got.Status != "online" {
		t.Fatalf("transient refresh failure changed account status to %q", got.Status)
	}
}

func TestEnsureValidRetryableHTTPStatusNeverPersistsExpiredStatus(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"provider detail"}`))
			}))
			t.Cleanup(server.Close)
			store, account, path := expiredStoreForTokenServer(t, server.URL)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.EnsureValidContext(context.Background(), account.ID); err == nil {
				t.Fatal("expected retryable refresh failure")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("retryable HTTP failure durably rewrote the token cache")
			}
			got, _ := store.Get(account.ID)
			if got.Status != "online" {
				t.Fatalf("retryable HTTP status %d changed account status to %q", status, got.Status)
			}
		})
	}
}

func TestEnsureValidInvalidGrantPersistsExpiredStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"provider detail"}`))
	}))
	t.Cleanup(server.Close)
	store, account, _ := expiredStoreForTokenServer(t, server.URL)
	if _, err := store.EnsureValidContext(context.Background(), account.ID); err == nil {
		t.Fatal("expected invalid_grant refresh failure")
	}
	got, _ := store.Get(account.ID)
	if got.Status != "expired" {
		t.Fatalf("invalid_grant left account status=%q, want expired", got.Status)
	}
}
