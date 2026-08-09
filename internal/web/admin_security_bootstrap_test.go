package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type adminBootstrapFixture struct {
	dir           string
	passwordPath  string
	bootstrapPath string
}

func newAdminBootstrapFixture(t *testing.T, bootstrap string) adminBootstrapFixture {
	t.Helper()
	previous := sharedSettings
	t.Cleanup(func() { sharedSettings = previous })
	dir := t.TempDir()
	fixture := adminBootstrapFixture{
		dir:           dir,
		passwordPath:  filepath.Join(dir, "data", "admin-password"),
		bootstrapPath: filepath.Join(dir, "secret", "bootstrap"),
	}
	if bootstrap != "" {
		if err := os.MkdirAll(filepath.Dir(fixture.bootstrapPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.bootstrapPath, []byte(bootstrap+"\n"), 0400); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("M365_DATA_DIR", "")
	t.Setenv("M365_SETTINGS_FILE", filepath.Join(dir, "settings.json"))
	t.Setenv("M365_TOKEN_CACHE", filepath.Join(dir, "accounts.json"))
	t.Setenv("M365_SESSION_CACHE", filepath.Join(dir, "sessions.json"))
	t.Setenv("M365_ADMIN_PASSWORD_FILE", fixture.passwordPath)
	t.Setenv("M365_ADMIN_PASSWORD_BOOTSTRAP_FILE", fixture.bootstrapPath)
	t.Setenv("M365_ADMIN_PASSWORD", "")
	t.Setenv("M365_API_KEYS", filepath.Join(dir, "api-keys.json"))
	t.Setenv("M365_DEBUG_LOG", filepath.Join(dir, "debug.jsonl"))
	t.Setenv(adminAllowedHostsEnv, "")
	t.Setenv(adminTrustedProxiesEnv, "")
	return fixture
}

func (f adminBootstrapFixture) newServer(t *testing.T) *Server {
	t.Helper()
	sharedSettings = nil
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func responseText(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestAdminPasswordPathPrefersExplicitFileOverDataDir(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit", "admin-password")
	t.Setenv("M365_ADMIN_PASSWORD_FILE", explicit)
	t.Setenv("M365_DATA_DIR", filepath.Join(dir, "data"))
	if got := adminPasswordPath(); got != explicit {
		t.Fatalf("adminPasswordPath()=%q want=%q", got, explicit)
	}
	if got := adminBootstrapConsumedPath(); got != explicit+".bootstrap-consumed" {
		t.Fatalf("adminBootstrapConsumedPath()=%q", got)
	}
}

func TestSaveAdminPasswordReplacesExistingCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-password")
	t.Setenv("M365_ADMIN_PASSWORD_FILE", path)
	if err := os.WriteFile(path, []byte("old-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveAdminPassword("new-password"); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "new-password\n" {
		t.Fatalf("stored=%q", stored)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%#o", info.Mode().Perm())
	}
}

func TestAdminCredentialPersistedReadErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "admin-password")
	if err := os.Mkdir(passwordPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365_ADMIN_PASSWORD_FILE", passwordPath)
	t.Setenv("M365_ADMIN_PASSWORD", "bootstrap-must-not-be-used")
	credential, err := loadAdminCredential()
	if err == nil {
		t.Fatalf("credential=%#v; expected persisted read error", credential)
	}
	if strings.Contains(err.Error(), "bootstrap-must-not-be-used") {
		t.Fatalf("error leaked bootstrap: %v", err)
	}
}

func TestAdminCredentialMarkerReadErrorFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-referential symlink fixture is not portable to Windows")
	}
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "admin-password")
	markerPath := passwordPath + ".bootstrap-consumed"
	if err := os.Symlink(filepath.Base(markerPath), markerPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("M365_ADMIN_PASSWORD_FILE", passwordPath)
	t.Setenv("M365_ADMIN_PASSWORD", "bootstrap-must-not-be-used")
	credential, err := loadAdminCredential()
	if err == nil {
		t.Fatalf("credential=%#v; expected marker read error", credential)
	}
	if strings.Contains(err.Error(), "bootstrap-must-not-be-used") {
		t.Fatalf("error leaked bootstrap: %v", err)
	}
}

func TestAdminCredentialMissingBootstrapIsUnavailable(t *testing.T) {
	fixture := newAdminBootstrapFixture(t, "")
	credential, err := loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Mode != adminCredentialUnavailable || credential.Password != "" {
		t.Fatalf("credential=%#v", credential)
	}

	s := fixture.newServer(t)
	ts, client := adminTestClient(t, s.Routes())
	response := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"admin123"}`)
	body := responseText(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(body, "admin123") {
		t.Fatalf("unavailable response leaked submitted credential: %s", body)
	}
}

func TestAdminCredentialUnavailableDoesNotDisableV1APIKeys(t *testing.T) {
	fixture := newAdminBootstrapFixture(t, "")
	s := fixture.newServer(t)
	_, key, err := s.apiKeys.create("unavailable-admin-test")
	if err != nil {
		t.Fatal(err)
	}
	response := serveAdminRequest(t, s.Routes(), http.MethodGet, "http://untrusted.example.test/v1/models", "198.51.100.20:5000", "", "", map[string]string{"Authorization": "Bearer " + key})
	body := responseText(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}

func TestAdminCredentialEmptyPersistedFileSuppressesBootstrap(t *testing.T) {
	fixture := newAdminBootstrapFixture(t, "explicit-bootstrap-secret")
	if err := os.MkdirAll(filepath.Dir(fixture.passwordPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.passwordPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	credential, err := loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Mode != adminCredentialUnavailable || credential.Password != "" {
		t.Fatalf("empty persisted file fell back to bootstrap: %#v", credential)
	}
}

func TestAdminCredentialInvalidBootstrapMountIsUnavailable(t *testing.T) {
	fixture := newAdminBootstrapFixture(t, "bootstrap-to-replace")
	if err := os.Remove(fixture.bootstrapPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.bootstrapPath, 0700); err != nil {
		t.Fatal(err)
	}
	credential, err := loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Mode != adminCredentialUnavailable || credential.Password != "" {
		t.Fatalf("invalid bootstrap mount activated credential: %#v", credential)
	}
}

func TestAdminBootstrapMarkerContainsNoSecretAndPreventsReload(t *testing.T) {
	const bootstrap = "sentinel-bootstrap-secret"
	fixture := newAdminBootstrapFixture(t, bootstrap)
	credential, err := loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Mode != adminCredentialBootstrap || credential.Password != bootstrap {
		t.Fatalf("credential=%#v", credential)
	}
	if err := markAdminBootstrapConsumed(); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(adminBootstrapConsumedPath())
	if err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Stat(adminBootstrapConsumedPath())
	if err != nil {
		t.Fatal(err)
	}
	if markerInfo.Mode().Perm() != 0600 {
		t.Fatalf("marker mode=%#o", markerInfo.Mode().Perm())
	}
	if strings.Contains(string(marker), bootstrap) {
		t.Fatalf("bootstrap marker leaked secret: %q", marker)
	}
	credential, err = loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Mode != adminCredentialUnavailable || credential.Password != "" {
		t.Fatalf("consumed credential reloaded: %#v", credential)
	}
	if _, err := os.Stat(fixture.passwordPath); !os.IsNotExist(err) {
		t.Fatalf("bootstrap consumption unexpectedly created persisted credential: %v", err)
	}
}

func TestConcurrentBootstrapSessionEstablishmentConsumesOnce(t *testing.T) {
	const bootstrap = "concurrent-bootstrap-secret"
	fixture := newAdminBootstrapFixture(t, bootstrap)
	s := fixture.newServer(t)
	now := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, token := range []string{"session-a", "session-b"} {
		token := token
		go func() {
			<-start
			_, err := s.establishAdminSession(bootstrap, adminCredentialBootstrap, token, now)
			results <- err
		}()
	}
	close(start)

	successes := 0
	consumed := 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errAdminBootstrapConsumed):
			consumed++
		default:
			t.Fatalf("unexpected establishment error: %v", err)
		}
	}
	s.mu.Lock()
	sessionCount := len(s.adminSessions)
	mode := s.adminCredentialMode
	s.mu.Unlock()
	if successes != 1 || consumed != 1 || sessionCount != 1 || mode != adminCredentialBootstrapConsumed {
		t.Fatalf("success=%d consumed=%d sessions=%d mode=%d", successes, consumed, sessionCount, mode)
	}
}

func TestAdminBootstrapMarkerFailureIssuesNoSession(t *testing.T) {
	const bootstrap = "marker-failure-bootstrap"
	fixture := newAdminBootstrapFixture(t, bootstrap)
	s := fixture.newServer(t)
	now := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)
	s.adminSessions["prior-session"] = adminSession{CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := os.WriteFile(filepath.Dir(fixture.passwordPath), []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	ts, client := adminTestClient(t, s.Routes())
	response := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"marker-failure-bootstrap"}`)
	body := responseText(t, response)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(body, bootstrap) {
		t.Fatalf("storage response leaked bootstrap: %s", body)
	}
	s.mu.Lock()
	_, priorExists := s.adminSessions["prior-session"]
	sessionCount := len(s.adminSessions)
	mode := s.adminCredentialMode
	s.mu.Unlock()
	if !priorExists || sessionCount != 1 || mode != adminCredentialBootstrap {
		t.Fatalf("prior=%v count=%d mode=%d", priorExists, sessionCount, mode)
	}
}

func TestAdminBootstrapExistingMarkerFailsClosed(t *testing.T) {
	const bootstrap = "externally-consumed-bootstrap"
	fixture := newAdminBootstrapFixture(t, bootstrap)
	s := fixture.newServer(t)
	if err := os.MkdirAll(filepath.Dir(adminBootstrapConsumedPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminBootstrapConsumedPath(), []byte("m365-admin-bootstrap-consumed-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)
	s.adminSessions["prior-session"] = adminSession{CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	ts, client := adminTestClient(t, s.Routes())
	response := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"externally-consumed-bootstrap"}`)
	body := responseText(t, response)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	s.mu.Lock()
	password := s.adminPassword
	mode := s.adminCredentialMode
	sessionCount := len(s.adminSessions)
	s.mu.Unlock()
	if password != "" || mode != adminCredentialUnavailable || sessionCount != 0 {
		t.Fatalf("password_present=%v mode=%d sessions=%d", password != "", mode, sessionCount)
	}
}

func TestAdminBootstrapLoginIsSingleUseAndRotationPersists(t *testing.T) {
	const bootstrap = "sentinel-one-time-bootstrap"
	const persisted = "persisted-admin-password-123"
	fixture := newAdminBootstrapFixture(t, bootstrap)
	s := fixture.newServer(t)
	now := time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	s.adminSessions["prior-session"] = adminSession{CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	ts, client := adminTestClient(t, s.Routes())

	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"sentinel-one-time-bootstrap"}`)
	loginBody := responseText(t, login)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap login=%d body=%s", login.StatusCode, loginBody)
	}
	var loginResult map[string]any
	if err := json.Unmarshal([]byte(loginBody), &loginResult); err != nil {
		t.Fatal(err)
	}
	if loginResult["must_change_password"] != true {
		t.Fatalf("login result=%#v", loginResult)
	}
	if strings.Contains(loginBody, bootstrap) {
		t.Fatalf("login response leaked bootstrap: %s", loginBody)
	}

	s.mu.Lock()
	_, priorExists := s.adminSessions["prior-session"]
	sessionCount := len(s.adminSessions)
	mode := s.adminCredentialMode
	s.mu.Unlock()
	if priorExists || sessionCount != 1 || mode != adminCredentialBootstrapConsumed {
		t.Fatalf("prior=%v count=%d mode=%d", priorExists, sessionCount, mode)
	}

	secondClient := &http.Client{Transport: localHandlerTransport{handler: s.Routes()}}
	secondLogin := postJSON(t, secondClient, ts.URL+"/api/admin/login", `{"password":"sentinel-one-time-bootstrap"}`)
	secondBody := responseText(t, secondLogin)
	if secondLogin.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("bootstrap reuse=%d body=%s", secondLogin.StatusCode, secondBody)
	}
	if strings.Contains(secondBody, bootstrap) {
		t.Fatalf("reuse response leaked bootstrap: %s", secondBody)
	}

	protected, err := client.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	protectedBody := responseText(t, protected)
	if protected.StatusCode != http.StatusForbidden {
		t.Fatalf("protected=%d body=%s", protected.StatusCode, protectedBody)
	}

	samePassword := postJSON(t, client, ts.URL+"/api/admin/change-password", `{"current_password":"sentinel-one-time-bootstrap","new_password":"sentinel-one-time-bootstrap"}`)
	if body := responseText(t, samePassword); samePassword.StatusCode != http.StatusBadRequest {
		t.Fatalf("same password=%d body=%s", samePassword.StatusCode, body)
	}

	change := postJSON(t, client, ts.URL+"/api/admin/change-password", `{"current_password":"sentinel-one-time-bootstrap","new_password":"persisted-admin-password-123"}`)
	changeBody := responseText(t, change)
	if change.StatusCode != http.StatusOK {
		t.Fatalf("change=%d body=%s", change.StatusCode, changeBody)
	}
	if strings.Contains(changeBody, bootstrap) || strings.Contains(changeBody, persisted) {
		t.Fatalf("change response leaked credential: %s", changeBody)
	}
	if records := s.debug.list(); len(records) != 0 {
		t.Fatalf("management credential lifecycle entered debug evidence: %#v", records)
	}

	oldSession, err := client.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	if body := responseText(t, oldSession); oldSession.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session=%d body=%s", oldSession.StatusCode, body)
	}

	stored, err := os.ReadFile(fixture.passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != persisted+"\n" {
		t.Fatalf("persisted credential mismatch")
	}
	persistedInfo, err := os.Stat(fixture.passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	if persistedInfo.Mode().Perm() != 0600 {
		t.Fatalf("persisted credential mode=%#o", persistedInfo.Mode().Perm())
	}

	restarted := fixture.newServer(t)
	restartedServer, restartedClient := adminTestClient(t, restarted.Routes())
	bootstrapAfterRestart := postJSON(t, restartedClient, restartedServer.URL+"/api/admin/login", `{"password":"sentinel-one-time-bootstrap"}`)
	if body := responseText(t, bootstrapAfterRestart); bootstrapAfterRestart.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bootstrap after restart=%d body=%s", bootstrapAfterRestart.StatusCode, body)
	}
	persistedLogin := postJSON(t, restartedClient, restartedServer.URL+"/api/admin/login", `{"password":"persisted-admin-password-123"}`)
	persistedBody := responseText(t, persistedLogin)
	if persistedLogin.StatusCode != http.StatusOK {
		t.Fatalf("persisted login=%d body=%s", persistedLogin.StatusCode, persistedBody)
	}
	var persistedResult map[string]any
	if err := json.Unmarshal([]byte(persistedBody), &persistedResult); err != nil {
		t.Fatal(err)
	}
	if persistedResult["must_change_password"] != false {
		t.Fatalf("persisted result=%#v", persistedResult)
	}

	if err := os.Remove(fixture.passwordPath); err != nil {
		t.Fatal(err)
	}
	unavailable := fixture.newServer(t)
	unavailableServer, unavailableClient := adminTestClient(t, unavailable.Routes())
	reuseAfterDeletion := postJSON(t, unavailableClient, unavailableServer.URL+"/api/admin/login", `{"password":"sentinel-one-time-bootstrap"}`)
	if body := responseText(t, reuseAfterDeletion); reuseAfterDeletion.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("reuse after deletion=%d body=%s", reuseAfterDeletion.StatusCode, body)
	}
}
