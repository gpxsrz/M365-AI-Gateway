package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
