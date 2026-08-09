package web

import (
	"encoding/json"
	"m365-native/internal/auth"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerRestartOpensPromotedOAuthProfileWithPinnedClientConfig(t *testing.T) {
	previousSettings := sharedSettings
	sharedSettings = nil
	t.Cleanup(func() { sharedSettings = previousSettings })

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "accounts.json")
	legacyConfig := auth.OAuthConfig{
		ClientID:          "accepted-client",
		Authority:         "https://accepted.example.test/common",
		RedirectURI:       "https://accepted.example.test/oauth/callback",
		Scope:             "openid offline_access accepted.read",
		AuthorizeEndpoint: "https://accepted.example.test/common/oauth2/v2.0/authorize",
		TokenEndpoint:     "https://accepted.example.test/common/oauth2/v2.0/token",
	}
	candidateConfig := auth.OAuthConfig{
		ClientID:          "candidate-client",
		Authority:         "https://candidate.example.test/common",
		RedirectURI:       "https://candidate.example.test/oauth/callback",
		Scope:             "openid offline_access candidate.read",
		AuthorizeEndpoint: "https://candidate.example.test/common/oauth2/v2.0/authorize",
		TokenEndpoint:     "https://candidate.example.test/common/oauth2/v2.0/token",
	}
	manager, err := auth.OpenOAuthProfileManager(tokenPath, legacyConfig)
	if err != nil {
		t.Fatal(err)
	}
	candidate, candidateStore, err := manager.Stage(candidateConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := candidateStore.Upsert(auth.TokenSet{
		AccessToken:  "candidate-access-secret",
		RefreshToken: "candidate-refresh-secret",
		HomeOID:      "candidate-oid",
		TenantID:     "candidate-tid",
		Email:        "candidate@example.test",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	for _, step := range []auth.OAuthProfileValidationStep{
		auth.OAuthProfileValidationChatHub,
		auth.OAuthProfileValidationRefresh,
		auth.OAuthProfileValidationRestart,
		auth.OAuthProfileValidationRemoval,
	} {
		if _, err := manager.RecordValidation(candidate.ProfileID, step); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.Promote(candidate.ProfileID); err != nil {
		t.Fatal(err)
	}

	passwordPath := filepath.Join(dir, "admin-password")
	if err := os.WriteFile(passwordPath, []byte("administrator-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365_DATA_DIR", "")
	t.Setenv("M365_SETTINGS_FILE", filepath.Join(dir, "settings.json"))
	t.Setenv("M365_TOKEN_CACHE", tokenPath)
	t.Setenv("M365_SESSION_CACHE", filepath.Join(dir, "sessions.json"))
	t.Setenv("M365_ADMIN_PASSWORD_FILE", passwordPath)
	t.Setenv("M365_ADMIN_PASSWORD_BOOTSTRAP_FILE", "")
	t.Setenv("M365_ADMIN_PASSWORD", "")
	t.Setenv("M365_API_KEYS", filepath.Join(dir, "api-keys.json"))
	t.Setenv("M365_DEBUG_LOG", filepath.Join(dir, "debug.jsonl"))
	t.Setenv("M365_CLIENT_ID", "ignored-process-client")
	t.Setenv("M365_AUTHORITY", "https://ignored.example.test/common")
	t.Setenv("M365_REDIRECT_URI", "https://ignored.example.test/oauth/callback")
	t.Setenv("M365_SCOPE", "openid ignored.read")
	t.Setenv("M365_AUTHORIZE_ENDPOINT", "https://ignored.example.test/common/oauth2/v2.0/authorize")
	t.Setenv("M365_TOKEN_ENDPOINT", "https://ignored.example.test/common/oauth2/v2.0/token")

	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if got := server.tokens.Config(); got != candidateConfig {
		t.Fatalf("active OAuth config = %#v, want %#v", got, candidateConfig)
	}
	if got := server.tokens.Path(); got != candidateStore.Path() {
		t.Fatalf("active token path = %q, want %q", got, candidateStore.Path())
	}
	accounts := server.tokens.List()
	if len(accounts) != 1 || accounts[0].AccessToken != "candidate-access-secret" {
		t.Fatalf("active accounts = %#v", accounts)
	}

	rr := httptest.NewRecorder()
	server.health(rr, httptest.NewRequest("GET", "/api/health", nil))
	var health map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["clientId"] != candidateConfig.ClientID || health["scope"] != candidateConfig.Scope || health["tokenCache"] != candidateStore.Path() {
		t.Fatalf("health exposes wrong active profile metadata: %#v", health)
	}
}
