package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"m365-native/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testOAuthState    = "state-issue-24"
	testOAuthVerifier = "verifier-issue-24"
	testOAuthCode     = "code-issue-24"
)

type oauthTokenStub struct {
	server   *httptest.Server
	calls    atomic.Int32
	code     string
	verifier string
	redirect string
}

func newOAuthTokenStub(t *testing.T) *oauthTokenStub {
	t.Helper()
	stub := &oauthTokenStub{}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"oid-issue-24","tid":"tid-issue-24","preferred_username":"person-issue-24@example.test","name":"Issue 24"}`))
	accessToken := "header." + claims + ".signature"
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		stub.code = r.Form.Get("code")
		stub.verifier = r.Form.Get("code_verifier")
		stub.redirect = r.Form.Get("redirect_uri")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": "refresh-issue-24",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func newPKCECallbackServer(t *testing.T, redirect string, now time.Time, tokenStub *oauthTokenStub) *Server {
	t.Helper()
	t.Setenv("M365_REDIRECT_URI", redirect)
	t.Setenv("M365_TOKEN_ENDPOINT", tokenStub.server.URL)
	dir := t.TempDir()
	store, err := auth.OpenStore(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		tokens: store,
		pkce: map[string]pendingPKCE{
			testOAuthState: {
				Verifier:    testOAuthVerifier,
				RedirectURI: redirect,
				Created:     now,
				Status:      "pending",
			},
		},
		adminPassword:       "administrator-password",
		adminCredentialMode: adminCredentialPersisted,
		adminSessions: map[string]adminSession{
			"admin-session": {
				CreatedAt:  now,
				LastSeenAt: now,
				ExpiresAt:  now.Add(time.Hour),
			},
		},
		clock: func() time.Time { return now },
	}
}

func serveOAuthCallback(t *testing.T, s *Server, method, target, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:43210"
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
	s.Routes().ServeHTTP(rr, req)
	return rr
}

func requireOAuthNoStoreHeaders(t *testing.T, header http.Header) {
	t.Helper()
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy=%q", got)
	}
}

func oauthErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	_, code, _ := openAIErrorDetails(rr.Body.Bytes())
	return code
}

func TestManualPKCEFallbackPOSTSuccessConsumesStateExactlyOnce(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "https://login.microsoftonline.com/common/oauth2/nativeclient"
	stub := newOAuthTokenStub(t)
	s := newPKCECallbackServer(t, redirect, now, stub)
	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousLogWriter)
	callbackURL := redirect + "?code=" + url.QueryEscape(testOAuthCode) + "&state=" + url.QueryEscape(testOAuthState)
	body := `{"callback_url":` + mustJSON(callbackURL) + `}`

	first := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	requireOAuthNoStoreHeaders(t, first.Header())
	if stub.calls.Load() != 1 || stub.code != testOAuthCode || stub.verifier != testOAuthVerifier || stub.redirect != redirect {
		t.Fatalf("token exchange calls=%d code=%q verifier=%q redirect=%q", stub.calls.Load(), stub.code, stub.verifier, stub.redirect)
	}
	response := first.Body.String()
	for _, forbidden := range []string{testOAuthCode, testOAuthVerifier, "refresh-issue-24", "person-issue-24@example.test", "oid-issue-24", "tid-issue-24"} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("callback response leaked %q: %s", forbidden, response)
		}
	}
	if !strings.Contains(response, `"status":"authenticated"`) || strings.Contains(response, "profileRef") {
		t.Fatalf("callback response has invalid single-account view: %s", response)
	}

	s.clock = func() time.Time { return now.Add(11 * time.Minute) }
	replay := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
	if replay.Code != http.StatusConflict || oauthErrorCode(t, replay) != "oauth_state_replayed" {
		t.Fatalf("replay status=%d code=%q body=%s", replay.Code, oauthErrorCode(t, replay), replay.Body.String())
	}
	requireOAuthNoStoreHeaders(t, replay.Header())
	if stub.calls.Load() != 1 {
		t.Fatalf("replay exchanged token again: calls=%d", stub.calls.Load())
	}
	for _, forbidden := range []string{testOAuthCode, testOAuthState, testOAuthVerifier, "refresh-issue-24", "person-issue-24@example.test", "oid-issue-24", "tid-issue-24"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("OAuth secret or identity %q entered service log: %s", forbidden, logs.String())
		}
	}
}

func TestManualPKCEFallbackRejectsInvalidTransportAndRedirect(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "https://login.microsoftonline.com/common/oauth2/nativeclient"
	tests := []struct {
		name        string
		target      string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{name: "query forbidden", target: "http://127.0.0.1/api/auth/callback?state=" + testOAuthState + "&code=" + testOAuthCode, contentType: "application/json", body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_query_forbidden"},
		{name: "content type", target: "http://127.0.0.1/api/auth/callback", contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType, wantCode: "oauth_callback_content_type"},
		{name: "bad json", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_invalid_json"},
		{name: "null body", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `null`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_invalid_json"},
		{name: "unknown field", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `{"state":"` + testOAuthState + `","code":"` + testOAuthCode + `","token":"forbidden"}`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_invalid_json"},
		{name: "scheme mismatch", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `{"callback_url":"http://login.microsoftonline.com/common/oauth2/nativeclient?state=` + testOAuthState + `&code=` + testOAuthCode + `"}`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_redirect_mismatch"},
		{name: "host mismatch", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `{"callback_url":"https://evil.example/common/oauth2/nativeclient?state=` + testOAuthState + `&code=` + testOAuthCode + `"}`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_redirect_mismatch"},
		{name: "path mismatch", target: "http://127.0.0.1/api/auth/callback", contentType: "application/json", body: `{"callback_url":"https://login.microsoftonline.com/common/oauth2/other?state=` + testOAuthState + `&code=` + testOAuthCode + `"}`, wantStatus: http.StatusBadRequest, wantCode: "oauth_callback_redirect_mismatch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newOAuthTokenStub(t)
			s := newPKCECallbackServer(t, redirect, now, stub)
			rr := serveOAuthCallback(t, s, http.MethodPost, tc.target, tc.contentType, tc.body)
			if rr.Code != tc.wantStatus || oauthErrorCode(t, rr) != tc.wantCode {
				t.Fatalf("status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
			}
			requireOAuthNoStoreHeaders(t, rr.Header())
			if stub.calls.Load() != 0 {
				t.Fatalf("invalid callback reached token endpoint: %d", stub.calls.Load())
			}
		})
	}

	stub := newOAuthTokenStub(t)
	s := newPKCECallbackServer(t, redirect, now, stub)
	oversized := `{"state":"` + testOAuthState + `","code":"` + strings.Repeat("x", 20<<10) + `"}`
	rr := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", oversized)
	if rr.Code != http.StatusRequestEntityTooLarge || oauthErrorCode(t, rr) != "oauth_callback_body_too_large" {
		t.Fatalf("oversized status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
	}
}

func TestPKCECallbackCancelTimeoutMismatchAndReplayAreStable(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "https://login.microsoftonline.com/common/oauth2/nativeclient"

	t.Run("cancel consumes state", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, redirect, now, stub)
		callbackURL := redirect + "?error=access_denied&error_description=cancelled&state=" + testOAuthState
		body := `{"callback_url":` + mustJSON(callbackURL) + `}`
		first := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if first.Code != http.StatusBadRequest || oauthErrorCode(t, first) != "oauth_authorization_cancelled" {
			t.Fatalf("cancel status=%d code=%q body=%s", first.Code, oauthErrorCode(t, first), first.Body.String())
		}
		replay := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if replay.Code != http.StatusConflict || oauthErrorCode(t, replay) != "oauth_state_replayed" {
			t.Fatalf("cancel replay status=%d code=%q body=%s", replay.Code, oauthErrorCode(t, replay), replay.Body.String())
		}
	})

	t.Run("expired", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, redirect, now.Add(-11*time.Minute), stub)
		s.clock = func() time.Time { return now }
		body := fmt.Sprintf(`{"state":%q,"code":%q}`, testOAuthState, testOAuthCode)
		rr := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if rr.Code != http.StatusGone || oauthErrorCode(t, rr) != "oauth_state_expired" {
			t.Fatalf("expired status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, redirect, now, stub)
		body := `{"state":"unknown-state","code":"` + testOAuthCode + `"}`
		rr := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if rr.Code != http.StatusBadRequest || oauthErrorCode(t, rr) != "oauth_state_mismatch" {
			t.Fatalf("mismatch status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
		}
	})
}

func TestConcurrentPKCECallbacksExchangeTokenExactlyOnce(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "https://login.microsoftonline.com/common/oauth2/nativeclient"
	stub := newOAuthTokenStub(t)
	s := newPKCECallbackServer(t, redirect, now, stub)
	body := `{"state":"` + testOAuthState + `","code":"` + testOAuthCode + `"}`
	handler := s.Routes()

	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/callback", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:43210"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1")
			req.AddCookie(&http.Cookie{Name: "m365_admin_session", Value: "admin-session"})
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			results <- rr
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, replays := 0, 0
	for rr := range results {
		switch {
		case rr.Code == http.StatusOK:
			successes++
		case rr.Code == http.StatusConflict && oauthErrorCode(t, rr) == "oauth_state_replayed":
			replays++
		default:
			t.Fatalf("unexpected concurrent callback status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	if successes != 1 || replays != 1 || stub.calls.Load() != 1 {
		t.Fatalf("successes=%d replays=%d token_calls=%d", successes, replays, stub.calls.Load())
	}
}

func TestPKCEProviderAndTokenEndpointErrorsUseStableCodes(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "https://login.microsoftonline.com/common/oauth2/nativeclient"

	t.Run("provider error", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, redirect, now, stub)
		body := `{"callback_url":"` + redirect + `?error=server_error&error_description=provider-sensitive-detail&state=` + testOAuthState + `"}`
		rr := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if rr.Code != http.StatusBadRequest || oauthErrorCode(t, rr) != "oauth_authorization_failed" {
			t.Fatalf("status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "provider-sensitive-detail") || stub.calls.Load() != 0 {
			t.Fatalf("provider error leaked detail or reached token endpoint: %s", rr.Body.String())
		}
	})

	t.Run("token endpoint error", func(t *testing.T) {
		stub := &oauthTokenStub{}
		stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stub.calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token-sensitive-detail"}`))
		}))
		t.Cleanup(stub.server.Close)
		s := newPKCECallbackServer(t, redirect, now, stub)
		body := `{"state":"` + testOAuthState + `","code":"` + testOAuthCode + `"}`
		rr := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if rr.Code != http.StatusBadGateway || oauthErrorCode(t, rr) != "oauth_token_exchange_failed" {
			t.Fatalf("status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "token-sensitive-detail") || stub.calls.Load() != 1 {
			t.Fatalf("token endpoint detail leaked or wrong call count: %s", rr.Body.String())
		}
		replay := serveOAuthCallback(t, s, http.MethodPost, "http://127.0.0.1/api/auth/callback", "application/json", body)
		if replay.Code != http.StatusConflict || oauthErrorCode(t, replay) != "oauth_state_replayed" {
			t.Fatalf("error replay status=%d code=%q body=%s", replay.Code, oauthErrorCode(t, replay), replay.Body.String())
		}
	})
}

func TestRegisteredLoopbackGETCallbackRequiresExactHostAndPath(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	redirect := "http://127.0.0.1:4141/api/auth/callback"

	t.Run("success", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, redirect, now, stub)
		target := redirect + "?state=" + testOAuthState + "&code=" + testOAuthCode
		rr := serveOAuthCallback(t, s, http.MethodGet, target, "", "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "授權完成") {
			t.Fatalf("loopback success status=%d body=%s", rr.Code, rr.Body.String())
		}
		requireOAuthNoStoreHeaders(t, rr.Header())
	})

	for _, tc := range []struct {
		name       string
		configured string
		target     string
	}{
		{name: "host", configured: redirect, target: "http://localhost:4141/api/auth/callback?state=" + testOAuthState + "&code=" + testOAuthCode},
		{name: "port", configured: redirect, target: "http://127.0.0.1:4142/api/auth/callback?state=" + testOAuthState + "&code=" + testOAuthCode},
		{name: "path", configured: "http://127.0.0.1:4141/api/auth/other", target: "http://127.0.0.1:4141/api/auth/callback?state=" + testOAuthState + "&code=" + testOAuthCode},
		{name: "https", configured: redirect, target: "https://127.0.0.1:4141/api/auth/callback?state=" + testOAuthState + "&code=" + testOAuthCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newOAuthTokenStub(t)
			s := newPKCECallbackServer(t, tc.configured, now, stub)
			rr := serveOAuthCallback(t, s, http.MethodGet, tc.target, "", "")
			if rr.Code != http.StatusBadRequest || oauthErrorCode(t, rr) != "oauth_callback_redirect_mismatch" {
				t.Fatalf("status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
			}
			if stub.calls.Load() != 0 {
				t.Fatalf("mismatched loopback reached token endpoint")
			}
		})
	}

	t.Run("nativeclient GET forbidden", func(t *testing.T) {
		stub := newOAuthTokenStub(t)
		s := newPKCECallbackServer(t, "https://login.microsoftonline.com/common/oauth2/nativeclient", now, stub)
		rr := serveOAuthCallback(t, s, http.MethodGet, "http://127.0.0.1/api/auth/callback?state="+testOAuthState+"&code="+testOAuthCode, "", "")
		if rr.Code != http.StatusMethodNotAllowed || oauthErrorCode(t, rr) != "oauth_callback_loopback_required" {
			t.Fatalf("status=%d code=%q body=%s", rr.Code, oauthErrorCode(t, rr), rr.Body.String())
		}
	})
}

func TestManualFallbackUIUsesBoundedJSONPOSTAndIsNotPrimary(t *testing.T) {
	page := mustReadPKCEWebFile(t, "../../web/index.html")
	for _, required := range []string{
		`<details id="manualCallbackFallback" class="form-row" hidden>`,
		`function showManualCallbackFallback()`,
		`api('/api/auth/callback',{method:'POST',body:JSON.stringify(payload)})`,
		`callback_url`,
		`手動備援`,
		`不代表 Microsoft 登入失敗`,
		`完整 callback URL`,
		`授權碼有時效且只能使用一次`,
		`使用者取消`,
		`授權逾時`,
		`state 不符`,
		`權杖交換失敗`,
		`重新產生授權連結`,
	} {
		if !strings.Contains(page, required) {
			t.Errorf("management UI missing %q", required)
		}
	}
	for _, forbidden := range []string{"/api/auth/callback?", "u='/api/auth/callback?'"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("management UI still sends callback material in query: %q", forbidden)
		}
	}
}

func mustReadPKCEWebFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
