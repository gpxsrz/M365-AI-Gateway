package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"m365-native/internal/auth"
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

type lifecycleTokenEndpoint struct {
	server *httptest.Server
	mu     sync.Mutex
	forms  []url.Values
}

func newLifecycleTokenEndpoint(t *testing.T) *lifecycleTokenEndpoint {
	t.Helper()
	endpoint := &lifecycleTokenEndpoint{}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
			return
		}
		formCopy := make(url.Values, len(r.Form))
		for key, values := range r.Form {
			formCopy[key] = append([]string(nil), values...)
		}
		endpoint.mu.Lock()
		endpoint.forms = append(endpoint.forms, formCopy)
		endpoint.mu.Unlock()

		code := r.Form.Get("code")
		refresh := r.Form.Get("refresh_token")
		if refresh == "refresh-fail" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             "invalid_grant",
				"error_description": "offline refresh failure",
			})
			return
		}
		claims := map[string]any{
			"oid":                "oid-lifecycle",
			"tid":                "tid-lifecycle",
			"preferred_username": "person-lifecycle@example.test",
			"name":               "Lifecycle Account",
		}
		claimsRaw, _ := json.Marshal(claims)
		access := "header." + base64.RawURLEncoding.EncodeToString(claimsRaw) + ".signature"
		expiresIn := 3600
		if strings.HasPrefix(code, "expired-") {
			expiresIn = -60
		}
		refreshResult := firstNonEmpty(refresh, "refresh-"+code)
		switch code {
		case "expired-success":
			refreshResult = "refresh-success"
		case "expired-fail":
			refreshResult = "refresh-fail"
		}
		if refresh == "refresh-success" {
			refreshResult = "refresh-success-next"
		}
		response := map[string]any{
			"access_token":  access + "-" + firstNonEmpty(code, refresh, "unknown"),
			"refresh_token": refreshResult,
			"token_type":    "Bearer",
			"expires_in":    expiresIn,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func lifecycleOAuthConfig(endpoint string) auth.OAuthConfig {
	return auth.OAuthConfig{
		ClientID:          "lifecycle-client",
		Authority:         "https://login.example.test/common",
		RedirectURI:       "https://login.example.test/common/oauth2/nativeclient",
		Scope:             "openid profile offline_access lifecycle.read",
		AuthorizeEndpoint: "https://login.example.test/common/oauth2/v2.0/authorize",
		TokenEndpoint:     endpoint,
	}
}

func newOAuthLifecycleServer(t *testing.T, endpoint *lifecycleTokenEndpoint) (*Server, *auth.OAuthProfileManager, *auth.Store) {
	t.Helper()
	dir := t.TempDir()
	baseTokenPath := filepath.Join(dir, "accounts.json")
	manager, err := auth.OpenOAuthProfileManager(baseTokenPath, lifecycleOAuthConfig(endpoint.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	server := &Server{
		tokens:              activeStore,
		oauthProfiles:       manager,
		pkce:                map[string]pendingPKCE{},
		adminPassword:       "administrator-password",
		adminCredentialMode: adminCredentialPersisted,
		adminSessions: map[string]adminSession{
			"admin-session": {CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)},
		},
		clock: func() time.Time { return now },
	}
	return server, manager, activeStore
}

func lifecycleAdminRequest(t *testing.T, server *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	contentType := ""
	if body != "" {
		contentType = "application/json"
	}
	return lifecycleAdminRequestWithContentType(t, server, method, target, contentType, body)
}

func lifecycleAdminRequestWithContentType(t *testing.T, server *Server, method, target, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:41000"
	req.AddCookie(&http.Cookie{Name: "m365_admin_session", Value: "admin-session"})
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
	}
	rr := httptest.NewRecorder()
	server.Routes().ServeHTTP(rr, req)
	return rr
}

func decodeLifecycleJSON(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func callbackLifecycleState(t *testing.T, server *Server, state, code string) map[string]any {
	t.Helper()
	body := `{"state":` + mustJSON(state) + `,"code":` + mustJSON(code) + `}`
	recorder := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return decodeLifecycleJSON(t, recorder)
}

func startOAuthLifecycle(t *testing.T, server *Server, body string) map[string]any {
	t.Helper()
	recorder := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/start", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("OAuth start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	return decodeLifecycleJSON(t, recorder)
}

type oauthLifecycleObservation struct {
	State   string `json:"state"`
	Outcome string `json:"outcome"`
	Code    string `json:"code,omitempty"`
}

func TestOAuthLifecycleFirstLoginAndStagedReauthorization(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, _, activeStore := newOAuthLifecycleServer(t, endpoint)

	firstStart := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/start", "{}")
	if firstStart.Code != http.StatusOK {
		t.Fatalf("first start status=%d body=%s", firstStart.Code, firstStart.Body.String())
	}
	first := decodeLifecycleJSON(t, firstStart)
	if first["oauthProfileId"] != "legacy" || first["staged"] != false {
		t.Fatalf("first login target=%#v", first)
	}
	firstState, _ := first["state"].(string)
	callbackLifecycleState(t, server, firstState, "first-login-code")
	activeAccounts := testStoreAccounts(activeStore)
	if len(activeAccounts) != 1 || !strings.Contains(activeAccounts[0].AccessToken, "first-login-code") {
		t.Fatalf("first login active accounts=%#v", activeAccounts)
	}
	acceptedBefore, err := os.ReadFile(activeStore.Path())
	if err != nil {
		t.Fatal(err)
	}

	reauthStart := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/start", `{"stageActive":true}`)
	if reauthStart.Code != http.StatusOK {
		t.Fatalf("reauth start status=%d body=%s", reauthStart.Code, reauthStart.Body.String())
	}
	reauth := decodeLifecycleJSON(t, reauthStart)
	stagedID, _ := reauth["oauthProfileId"].(string)
	if !strings.HasPrefix(stagedID, "oauthp_") || reauth["staged"] != true {
		t.Fatalf("reauth target=%#v", reauth)
	}
	_, stagedBeforeCallback, err := server.oauthProfiles.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	if accounts := testStoreAccounts(stagedBeforeCallback); len(accounts) != 1 || !strings.Contains(accounts[0].AccessToken, "first-login-code") {
		t.Fatalf("staged active copy=%#v", accounts)
	}
	reauthState, _ := reauth["state"].(string)
	callback := callbackLifecycleState(t, server, reauthState, "reauth-code")
	if callback["oauthProfileId"] != stagedID || callback["staged"] != true {
		t.Fatalf("reauth callback=%#v", callback)
	}
	if got, err := os.ReadFile(activeStore.Path()); err != nil || string(got) != string(acceptedBefore) {
		t.Fatalf("reauth changed accepted active store: err=%v", err)
	}
	_, stagedAfterCallback, err := server.oauthProfiles.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	stagedAccounts := testStoreAccounts(stagedAfterCallback)
	if len(stagedAccounts) != 1 || !strings.Contains(stagedAccounts[0].AccessToken, "reauth-code") {
		t.Fatalf("reauth staged accounts=%#v", stagedAccounts)
	}
}

func TestOAuthLifecycleOrdinaryReloginUpdatesOnlyActiveCredential(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, _, activeStore := newOAuthLifecycleServer(t, endpoint)

	first := startOAuthLifecycle(t, server, "{}")
	firstState, _ := first["state"].(string)
	callbackLifecycleState(t, server, firstState, "first-login-code")

	relogin := startOAuthLifecycle(t, server, "{}")
	if relogin["oauthProfileId"] != "legacy" || relogin["staged"] != false {
		t.Fatalf("ordinary re-login target=%#v", relogin)
	}
	reloginState, _ := relogin["state"].(string)
	callbackLifecycleState(t, server, reloginState, "ordinary-relogin-code")

	accounts := testStoreAccounts(activeStore)
	if len(accounts) != 1 || !strings.Contains(accounts[0].AccessToken, "ordinary-relogin-code") {
		t.Fatalf("ordinary re-login active accounts=%#v", accounts)
	}
	status := testOAuthProfileStatus(t, activeStore.Path())
	if status.ActiveProfileID != "legacy" || status.PreviousProfileID != "" || len(status.Profiles) != 1 {
		t.Fatalf("ordinary re-login created or promoted a staged profile: %#v", status)
	}
}

func TestOAuthLifecycleRefreshRestartAndRemoval(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, manager, activeStore := newOAuthLifecycleServer(t, endpoint)
	observations := []oauthLifecycleObservation{}

	first := startOAuthLifecycle(t, server, "{}")
	firstState, _ := first["state"].(string)
	firstCallback := callbackLifecycleState(t, server, firstState, "accepted-login")
	if firstCallback["oauthProfileId"] != "legacy" {
		t.Fatalf("first login profile=%#v", firstCallback)
	}
	observations = append(observations, oauthLifecycleObservation{State: "first_login", Outcome: "success"})
	acceptedBefore, err := os.ReadFile(activeStore.Path())
	if err != nil {
		t.Fatal(err)
	}

	reauth := startOAuthLifecycle(t, server, `{"stageActive":true}`)
	stagedID, _ := reauth["oauthProfileId"].(string)
	reauthState, _ := reauth["state"].(string)
	callbackLifecycleState(t, server, reauthState, "expired-success")
	observations = append(observations, oauthLifecycleObservation{State: "reauth", Outcome: "success"})

	_, stagedStore, err := manager.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	stagedAccount, ok := stagedStore.First()
	if !ok {
		t.Fatal("staged account missing")
	}
	if _, err := stagedStore.EnsureValidContext(context.Background(), stagedAccount.ID); err != nil {
		t.Fatal(err)
	}
	refreshedAccounts := testStoreAccounts(stagedStore)
	if len(refreshedAccounts) != 1 || !strings.Contains(refreshedAccounts[0].AccessToken, "refresh-success") || refreshedAccounts[0].RefreshToken != "refresh-success-next" {
		t.Fatalf("refreshed staged accounts=%#v", refreshedAccounts)
	}
	if got, err := os.ReadFile(activeStore.Path()); err != nil || string(got) != string(acceptedBefore) {
		t.Fatalf("staged refresh changed active bytes: err=%v", err)
	}
	observations = append(observations, oauthLifecycleObservation{State: "refresh_success", Outcome: "success"})

	reopened, err := auth.OpenOAuthProfileManager(activeStore.Path(), lifecycleOAuthConfig(endpoint.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, restartedStore, err := reopened.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	if accounts := testStoreAccounts(restartedStore); len(accounts) != 1 || !strings.Contains(accounts[0].AccessToken, "refresh-success") {
		t.Fatalf("restart lost staged profile state=%#v", accounts)
	}
	observations = append(observations, oauthLifecycleObservation{State: "restart_persistence", Outcome: "success"})

	_, removedStore, err := reopened.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	removedAccount, ok := removedStore.First()
	if !ok || removedStore.Delete(removedAccount.ID) != nil {
		t.Fatal("staged account removal failed")
	}
	if accounts := testStoreAccounts(removedStore); len(accounts) != 0 {
		t.Fatalf("staged account removal did not persist: %#v", accounts)
	}
	if accounts := testStoreAccounts(activeStore); len(accounts) != 1 {
		t.Fatalf("staged account removal changed active store: %#v", accounts)
	}
	observations = append(observations, oauthLifecycleObservation{State: "account_removal", Outcome: "success"})

	postRemovalStart := startOAuthLifecycle(t, server, `{"profileId":`+mustJSON(stagedID)+`}`)
	postRemovalState, _ := postRemovalStart["state"].(string)
	postRemovalCallback := callbackLifecycleState(t, server, postRemovalState, "post-removal-reauth")
	if postRemovalCallback["oauthProfileId"] != stagedID || postRemovalCallback["staged"] != true {
		t.Fatalf("post-removal reauth callback=%#v", postRemovalCallback)
	}
	_, restoredStore, err := reopened.OpenStore(stagedID)
	if err != nil {
		t.Fatal(err)
	}
	if accounts := testStoreAccounts(restoredStore); len(accounts) != 1 || !strings.Contains(accounts[0].AccessToken, "post-removal-reauth") {
		t.Fatalf("post-removal reauth accounts=%#v", accounts)
	}
	observations = append(observations, oauthLifecycleObservation{State: "account_removal_reauth", Outcome: "success"})

	failedReauth := startOAuthLifecycle(t, server, `{"stageActive":true}`)
	failedID, _ := failedReauth["oauthProfileId"].(string)
	failedState, _ := failedReauth["state"].(string)
	callbackLifecycleState(t, server, failedState, "expired-fail")
	_, failedStore, err := manager.OpenStore(failedID)
	if err != nil {
		t.Fatal(err)
	}
	failedAccount, ok := failedStore.First()
	if !ok {
		t.Fatal("failed staged account missing")
	}
	if _, err := failedStore.EnsureValidContext(context.Background(), failedAccount.ID); err == nil {
		t.Fatal("refresh failure unexpectedly succeeded")
	}
	if err := manager.Discard(failedID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.OpenStore(failedID); !os.IsNotExist(err) {
		t.Fatalf("failed staged refresh profile was not discarded: %v", err)
	}
	if got, err := os.ReadFile(activeStore.Path()); err != nil || string(got) != string(acceptedBefore) {
		t.Fatalf("failed staged refresh changed active bytes: err=%v", err)
	}
	observations = append(observations, oauthLifecycleObservation{State: "refresh_failure", Outcome: "failed", Code: "token_refresh_error"})

	recovery := startOAuthLifecycle(t, server, `{"stageActive":true}`)
	recoveryID, _ := recovery["oauthProfileId"].(string)
	recoveryState, _ := recovery["state"].(string)
	recoveryCallback := callbackLifecycleState(t, server, recoveryState, "refresh-recovery-code")
	if recoveryCallback["oauthProfileId"] != recoveryID || recoveryCallback["staged"] != true {
		t.Fatalf("refresh recovery callback=%#v", recoveryCallback)
	}
	_, recoveryStore, err := manager.OpenStore(recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	if accounts := testStoreAccounts(recoveryStore); len(accounts) != 1 || !strings.Contains(accounts[0].AccessToken, "refresh-recovery-code") {
		t.Fatalf("refresh recovery staged accounts=%#v", accounts)
	}
	if got, err := os.ReadFile(activeStore.Path()); err != nil || string(got) != string(acceptedBefore) {
		t.Fatalf("refresh recovery changed active bytes: err=%v", err)
	}
	observations = append(observations, oauthLifecycleObservation{State: "refresh_recovery", Outcome: "success"})

	report, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"accepted-login", "expired-success", "expired-fail", "refresh-success", "post-removal-reauth", "refresh-recovery-code", "person-lifecycle@example.test", "oid-lifecycle", "tid-lifecycle"} {
		if strings.Contains(string(report), forbidden) {
			t.Fatalf("lifecycle report leaked %q: %s", forbidden, report)
		}
	}
	const expectedReport = `[{"state":"first_login","outcome":"success"},{"state":"reauth","outcome":"success"},{"state":"refresh_success","outcome":"success"},{"state":"restart_persistence","outcome":"success"},{"state":"account_removal","outcome":"success"},{"state":"account_removal_reauth","outcome":"success"},{"state":"refresh_failure","outcome":"failed","code":"token_refresh_error"},{"state":"refresh_recovery","outcome":"success"}]`
	if string(report) != expectedReport {
		t.Fatalf("lifecycle report=%s want=%s", report, expectedReport)
	}
}

func TestOAuthLifecycleCancelTimeoutMismatchAndReplayAreIndependentlyRecorded(t *testing.T) {
	observations := []oauthLifecycleObservation{}

	t.Run("cancel", func(t *testing.T) {
		endpoint := newLifecycleTokenEndpoint(t)
		server, manager, _ := newOAuthLifecycleServer(t, endpoint)
		start := startOAuthLifecycle(t, server, `{"stageActive":true}`)
		state, _ := start["state"].(string)
		stagedID, _ := start["oauthProfileId"].(string)
		body := `{"state":` + mustJSON(state) + `,"error":"access_denied"}`
		recorder := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
		if recorder.Code != http.StatusBadRequest || oauthErrorCode(t, recorder) != "oauth_authorization_cancelled" {
			t.Fatalf("cancel status=%d code=%q body=%s", recorder.Code, oauthErrorCode(t, recorder), recorder.Body.String())
		}
		status := lifecycleAdminRequest(t, server, http.MethodGet, "http://127.0.0.1/api/auth/status?state="+url.QueryEscape(state), "")
		if decodeLifecycleJSON(t, status)["status"] != "cancelled" {
			t.Fatalf("cancel status record=%s", status.Body.String())
		}
		if _, _, err := manager.OpenStore(stagedID); !os.IsNotExist(err) {
			t.Fatalf("cancelled staged profile was not discarded: %v", err)
		}
		observations = append(observations, oauthLifecycleObservation{State: "cancel", Outcome: "failed", Code: "oauth_authorization_cancelled"})
	})

	t.Run("timeout", func(t *testing.T) {
		endpoint := newLifecycleTokenEndpoint(t)
		server, manager, _ := newOAuthLifecycleServer(t, endpoint)
		start := startOAuthLifecycle(t, server, `{"stageActive":true}`)
		state, _ := start["state"].(string)
		stagedID, _ := start["oauthProfileId"].(string)
		server.clock = func() time.Time { return time.Date(2026, 8, 6, 13, 11, 0, 0, time.UTC) }
		body := `{"state":` + mustJSON(state) + `,"code":"late-code"}`
		recorder := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
		if recorder.Code != http.StatusGone || oauthErrorCode(t, recorder) != "oauth_state_expired" {
			t.Fatalf("timeout status=%d code=%q body=%s", recorder.Code, oauthErrorCode(t, recorder), recorder.Body.String())
		}
		if _, _, err := manager.OpenStore(stagedID); !os.IsNotExist(err) {
			t.Fatalf("expired staged profile was not discarded: %v", err)
		}
		observations = append(observations, oauthLifecycleObservation{State: "timeout", Outcome: "failed", Code: "oauth_state_expired"})
	})

	t.Run("mismatch", func(t *testing.T) {
		endpoint := newLifecycleTokenEndpoint(t)
		server, _, _ := newOAuthLifecycleServer(t, endpoint)
		body := `{"state":"unknown-state","code":"unknown-code"}`
		recorder := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
		if recorder.Code != http.StatusBadRequest || oauthErrorCode(t, recorder) != "oauth_state_mismatch" {
			t.Fatalf("mismatch status=%d code=%q body=%s", recorder.Code, oauthErrorCode(t, recorder), recorder.Body.String())
		}
		observations = append(observations, oauthLifecycleObservation{State: "mismatch", Outcome: "failed", Code: "oauth_state_mismatch"})
	})

	t.Run("replay", func(t *testing.T) {
		endpoint := newLifecycleTokenEndpoint(t)
		server, manager, _ := newOAuthLifecycleServer(t, endpoint)
		start := startOAuthLifecycle(t, server, `{"stageActive":true}`)
		state, _ := start["state"].(string)
		stagedID, _ := start["oauthProfileId"].(string)
		body := `{"state":` + mustJSON(state) + `,"code":"one-time-code"}`
		first := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
		if first.Code != http.StatusOK {
			t.Fatalf("replay setup status=%d body=%s", first.Code, first.Body.String())
		}
		replay := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", body)
		if replay.Code != http.StatusConflict || oauthErrorCode(t, replay) != "oauth_state_replayed" {
			t.Fatalf("replay status=%d code=%q body=%s", replay.Code, oauthErrorCode(t, replay), replay.Body.String())
		}
		if _, _, err := manager.OpenStore(stagedID); err != nil {
			t.Fatalf("replay incorrectly discarded successful staged profile: %v", err)
		}
		observations = append(observations, oauthLifecycleObservation{State: "replay", Outcome: "failed", Code: "oauth_state_replayed"})
	})

	report, err := json.Marshal(observations)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"late-code", "unknown-code", "one-time-code", "unknown-state", "refresh-", "person-lifecycle@example.test", "oid-lifecycle", "tid-lifecycle"} {
		if strings.Contains(string(report), forbidden) {
			t.Fatalf("failure lifecycle report leaked %q: %s", forbidden, report)
		}
	}
	const expectedReport = `[{"state":"cancel","outcome":"failed","code":"oauth_authorization_cancelled"},{"state":"timeout","outcome":"failed","code":"oauth_state_expired"},{"state":"mismatch","outcome":"failed","code":"oauth_state_mismatch"},{"state":"replay","outcome":"failed","code":"oauth_state_replayed"}]`
	if string(report) != expectedReport {
		t.Fatalf("failure lifecycle report=%s want=%s", report, expectedReport)
	}
}

func TestOAuthLifecyclePrunesAbandonedCandidatesButKeepsCompletedCandidates(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, manager, _ := newOAuthLifecycleServer(t, endpoint)

	abandoned := startOAuthLifecycle(t, server, `{"stageActive":true}`)
	abandonedID, _ := abandoned["oauthProfileId"].(string)
	completed := startOAuthLifecycle(t, server, `{"stageActive":true}`)
	completedID, _ := completed["oauthProfileId"].(string)
	completedState, _ := completed["state"].(string)
	callbackLifecycleState(t, server, completedState, "completed-candidate")

	retentionNow := time.Date(2026, 8, 6, 13, 31, 0, 0, time.UTC)
	server.clock = func() time.Time { return retentionNow }
	server.mu.Lock()
	session := server.adminSessions["admin-session"]
	session.LastSeenAt = retentionNow
	server.adminSessions["admin-session"] = session
	server.mu.Unlock()
	_ = startOAuthLifecycle(t, server, "{}")
	if _, _, err := manager.OpenStore(abandonedID); !os.IsNotExist(err) {
		t.Fatalf("abandoned staged profile was not pruned: %v", err)
	}
	if _, _, err := manager.OpenStore(completedID); err != nil {
		t.Fatalf("completed staged profile was incorrectly pruned: %v", err)
	}
}

func TestOAuthLifecycleStartBodyIsBoundedClosedAndExistingProfileSafe(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		body        string
		status      int
		code        string
	}{
		{name: "content type", contentType: "text/plain", body: `{}`, status: http.StatusUnsupportedMediaType, code: "oauth_start_content_type"},
		{name: "null body", contentType: "application/json", body: `null`, status: http.StatusBadRequest, code: "oauth_start_invalid_json"},
		{name: "unknown field", contentType: "application/json", body: `{"unknown":true}`, status: http.StatusBadRequest, code: "oauth_start_invalid_json"},
		{name: "OAuth override forbidden", contentType: "application/json", body: `{"oauth":{"client_id":"replacement-client"}}`, status: http.StatusBadRequest, code: "oauth_start_invalid_json"},
		{name: "target conflict", contentType: "application/json", body: `{"stageActive":true,"profileId":"legacy"}`, status: http.StatusBadRequest, code: "oauth_profile_target_conflict"},
		{name: "invalid profile", contentType: "application/json", body: `{"profileId":"oauthp_bad"}`, status: http.StatusBadRequest, code: "oauth_profile_target_invalid"},
		{name: "oversized", contentType: "application/json", body: `{"profileId":"` + strings.Repeat("x", 20<<10) + `"}`, status: http.StatusRequestEntityTooLarge, code: "oauth_start_body_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := newLifecycleTokenEndpoint(t)
			server, _, _ := newOAuthLifecycleServer(t, endpoint)
			recorder := lifecycleAdminRequestWithContentType(t, server, http.MethodPost, "http://127.0.0.1/api/auth/start", test.contentType, test.body)
			if recorder.Code != test.status || oauthErrorCode(t, recorder) != test.code {
				t.Fatalf("status=%d code=%q body=%s", recorder.Code, oauthErrorCode(t, recorder), recorder.Body.String())
			}
		})
	}

	endpoint := newLifecycleTokenEndpoint(t)
	server, manager, _ := newOAuthLifecycleServer(t, endpoint)
	existing, _, err := manager.Stage(lifecycleOAuthConfig(endpoint.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	start := startOAuthLifecycle(t, server, `{"profileId":`+mustJSON(existing.ProfileID)+`}`)
	if start["oauthProfileId"] != existing.ProfileID || start["staged"] != true {
		t.Fatalf("existing profile start=%#v", start)
	}
	state, _ := start["state"].(string)
	cancelBody := `{"state":` + mustJSON(state) + `,"error":"access_denied"}`
	cancel := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/callback", cancelBody)
	if cancel.Code != http.StatusBadRequest || oauthErrorCode(t, cancel) != "oauth_authorization_cancelled" {
		t.Fatalf("existing profile cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	if _, _, err := manager.OpenStore(existing.ProfileID); err != nil {
		t.Fatalf("reused existing profile was discarded: %v", err)
	}
}
