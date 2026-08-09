package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testResourceScope = "https://ic3.teams.office.com/.default openid profile offline_access"

func TestResourceAccessTokenCachesByScopeAndUsesActiveRefreshToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
			return
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q", got)
		}
		if got := r.Form.Get("client_id"); got != "resource-client" {
			t.Errorf("client_id = %q", got)
		}
		if got := r.Form.Get("scope"); got != testResourceScope {
			t.Errorf("scope = %q", got)
		}
		if got := r.Form.Get("refresh_token"); got != "refresh-main" {
			t.Errorf("refresh_token = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "secondary-access",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)

	store := newResourceTokenTestStore(t, server.URL, "refresh-main")
	for range 2 {
		token, err := store.ResourceAccessToken(context.Background(), testResourceScope)
		if err != nil {
			t.Fatal(err)
		}
		if token != "secondary-access" {
			t.Fatalf("resource access token = %q", token)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
}

func TestResourceAccessTokenReacquiresExpiredEntry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "secondary-" + string(rune('0'+call)),
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)

	store := newResourceTokenTestStore(t, server.URL, "refresh-main")
	first, err := store.ResourceAccessToken(context.Background(), testResourceScope)
	if err != nil {
		t.Fatal(err)
	}
	store.resourceMu.Lock()
	entry := store.resourceTokens[testResourceScope]
	entry.ExpiresAt = time.Now().Add(-time.Minute)
	store.resourceTokens[testResourceScope] = entry
	store.resourceMu.Unlock()
	second, err := store.ResourceAccessToken(context.Background(), testResourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || calls.Load() != 2 {
		t.Fatalf("tokens = %q, %q; endpoint calls = %d", first, second, calls.Load())
	}
}

func TestResourceAccessTokenCoalescesConcurrentRefresh(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "secondary-access",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)

	store := newResourceTokenTestStore(t, server.URL, "refresh-main")
	const callers = 24
	results := make(chan string, callers)
	errors := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := store.ResourceAccessToken(context.Background(), testResourceScope)
			results <- token
			errors <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for token := range results {
		if token != "secondary-access" {
			t.Fatalf("resource access token = %q", token)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
}

func TestResourceAccessTokenPersistsOnlyRotatedRefreshToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "secondary-access",
			"refresh_token": "refresh-rotated",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "accounts.json")
	store := openResourceTokenTestStore(t, path, server.URL, "refresh-main")
	before, _ := store.First()
	if _, err := store.ResourceAccessToken(context.Background(), testResourceScope); err != nil {
		t.Fatal(err)
	}
	after, _ := store.First()
	if after.RefreshToken != "refresh-rotated" {
		t.Fatalf("refresh token was not rotated")
	}
	copyBefore := before
	copyAfter := after
	copyBefore.RefreshToken = ""
	copyAfter.RefreshToken = ""
	if copyBefore != copyAfter {
		t.Fatalf("secondary refresh changed main account fields:\nbefore=%#v\nafter=%#v", copyBefore, copyAfter)
	}

	reopened := openResourceTokenTestStore(t, path, server.URL, "")
	persisted, ok := reopened.First()
	if !ok || persisted.RefreshToken != "refresh-rotated" || persisted.AccessToken != before.AccessToken || !persisted.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("persisted account = %#v", persisted)
	}
	if _, err := reopened.ResourceAccessToken(context.Background(), testResourceScope); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("restart retained in-memory resource cache; calls = %d", got)
	}
}

func TestResourceAccessTokenErrorsDoNotLeakCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	config := resourceTokenTestConfig("http://127.0.0.1:1")
	store, err := OpenStoreWithConfig(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResourceAccessToken(context.Background(), testResourceScope); err == nil {
		t.Fatal("missing active account succeeded")
	}
	if _, err := store.Upsert(TokenSet{
		AccessToken: "main-access",
		HomeOID:     "oid-main",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResourceAccessToken(context.Background(), testResourceScope); err == nil {
		t.Fatal("missing refresh token succeeded")
	}

	const secret = "refresh-secret-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"prefix-refresh-secret-sentinel","error_description":"refresh-secret-sentinel"}`))
	}))
	t.Cleanup(server.Close)
	failing := newResourceTokenTestStore(t, server.URL, secret)
	_, err = failing.ResourceAccessToken(context.Background(), testResourceScope)
	if err == nil {
		t.Fatal("provider error succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("resource token error leaked credential: %v", err)
	}
}

func TestInvalidateResourceAccessTokenOnlyRemovesRejectedToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "secondary-" + string(rune('0'+call)),
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)

	store := newResourceTokenTestStore(t, server.URL, "refresh-main")
	first, err := store.ResourceAccessToken(context.Background(), testResourceScope)
	if err != nil {
		t.Fatal(err)
	}
	store.InvalidateResourceAccessToken(testResourceScope, "some-other-token")
	stillFirst, err := store.ResourceAccessToken(context.Background(), testResourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if stillFirst != first || calls.Load() != 1 {
		t.Fatalf("mismatched invalidation removed cache: token=%q calls=%d", stillFirst, calls.Load())
	}
	store.InvalidateResourceAccessToken(testResourceScope, first)
	second, err := store.ResourceAccessToken(context.Background(), testResourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if second == first || calls.Load() != 2 {
		t.Fatalf("matched invalidation did not reacquire: token=%q calls=%d", second, calls.Load())
	}
}

func TestResourceAccessTokenCacheIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "secondary-access",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)

	store := newResourceTokenTestStore(t, server.URL, "refresh-main")
	for i := 0; i < maxResourceTokenEntries+3; i++ {
		scope := testResourceScope + "/" + string(rune('a'+i))
		if _, err := store.ResourceAccessToken(context.Background(), scope); err != nil {
			t.Fatal(err)
		}
	}
	store.resourceMu.Lock()
	defer store.resourceMu.Unlock()
	if got := len(store.resourceTokens); got != maxResourceTokenEntries {
		t.Fatalf("resource token cache entries = %d, want %d", got, maxResourceTokenEntries)
	}
}

func newResourceTokenTestStore(t *testing.T, endpoint, refreshToken string) *Store {
	t.Helper()
	return openResourceTokenTestStore(t, filepath.Join(t.TempDir(), "accounts.json"), endpoint, refreshToken)
}

func openResourceTokenTestStore(t *testing.T, path, endpoint, refreshToken string) *Store {
	t.Helper()
	store, err := OpenStoreWithConfig(path, resourceTokenTestConfig(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	if refreshToken == "" {
		return store
	}
	if _, err := store.Upsert(TokenSet{
		AccessToken:  "main-access",
		RefreshToken: refreshToken,
		HomeOID:      "oid-main",
		TenantID:     "tid-main",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func resourceTokenTestConfig(endpoint string) OAuthConfig {
	return OAuthConfig{
		ClientID:          "resource-client",
		Authority:         endpoint + "/common",
		RedirectURI:       endpoint + "/callback",
		Scope:             "main-chat-scope",
		AuthorizeEndpoint: endpoint + "/authorize",
		TokenEndpoint:     endpoint,
	}
}
