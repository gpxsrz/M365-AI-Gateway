package web

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newAdminSecurityServer(t *testing.T, password string) *Server {
	t.Helper()
	return newAdminSecurityServerWithPolicy(t, password, "", "")
}

func newAdminSecurityServerWithPolicy(t *testing.T, password, allowedHosts, trustedProxies string) *Server {
	t.Helper()
	return newAdminSecurityServerWithCredential(t, password, "", allowedHosts, trustedProxies)
}

func newAdminBootstrapServer(t *testing.T, password string) *Server {
	t.Helper()
	return newAdminBootstrapServerWithPolicy(t, password, "", "")
}

func newAdminBootstrapServerWithPolicy(t *testing.T, password, allowedHosts, trustedProxies string) *Server {
	t.Helper()
	return newAdminSecurityServerWithCredential(t, "", password, allowedHosts, trustedProxies)
}

func newAdminSecurityServerWithCredential(t *testing.T, persistedPassword, bootstrapPassword, allowedHosts, trustedProxies string) *Server {
	t.Helper()
	previous := sharedSettings
	sharedSettings = nil
	t.Cleanup(func() { sharedSettings = previous })
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "admin-password")
	if persistedPassword != "" {
		if err := os.WriteFile(passwordPath, []byte(persistedPassword+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("M365_DATA_DIR", "")
	t.Setenv("M365_SETTINGS_FILE", filepath.Join(dir, "settings.json"))
	t.Setenv("M365_TOKEN_CACHE", filepath.Join(dir, "accounts.json"))
	t.Setenv("M365_SESSION_CACHE", filepath.Join(dir, "sessions.json"))
	t.Setenv("M365_ADMIN_PASSWORD_FILE", passwordPath)
	t.Setenv("M365_ADMIN_PASSWORD_BOOTSTRAP_FILE", "")
	t.Setenv("M365_API_KEYS", filepath.Join(dir, "api-keys.json"))
	t.Setenv("M365_DEBUG_LOG", filepath.Join(dir, "debug.jsonl"))
	t.Setenv(adminAllowedHostsEnv, allowedHosts)
	t.Setenv(adminTrustedProxiesEnv, trustedProxies)
	t.Setenv("M365_ADMIN_PASSWORD", bootstrapPassword)
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type adminTestServer struct {
	URL string
}

type localHandlerTransport struct {
	handler http.Handler
	proxy   bool
}

func (t localHandlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	if cloned.Host == "" {
		cloned.Host = cloned.URL.Host
	}
	cloned.RemoteAddr = "127.0.0.1:1"
	if t.proxy {
		forwardedHost := cloned.Host
		cloned.Host = "127.0.0.1"
		cloned.TLS = nil
		cloned.Header.Set("X-Forwarded-For", "198.51.100.20")
		cloned.Header.Set("X-Forwarded-Host", forwardedHost)
		cloned.Header.Set("X-Forwarded-Proto", "https")
	} else if strings.EqualFold(cloned.URL.Scheme, "https") {
		cloned.TLS = &tls.ConnectionState{}
	}
	rr := httptest.NewRecorder()
	t.handler.ServeHTTP(rr, cloned)
	resp := rr.Result()
	resp.Request = req
	return resp, nil
}

func adminTestClient(t *testing.T, h http.Handler) (*adminTestServer, *http.Client) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: localHandlerTransport{handler: h}}
	c.Jar = jar
	return &adminTestServer{URL: "https://127.0.0.1"}, c
}

func adminProxyClient(t *testing.T, h http.Handler) (*adminTestServer, *http.Client) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: localHandlerTransport{handler: h, proxy: true}}
	c.Jar = jar
	return &adminTestServer{URL: "https://127.0.0.1"}, c
}

func requestOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func postJSON(t *testing.T, c *http.Client, url, body string) *http.Response {
	return postJSONWithHostOrigin(t, c, url, "", requestOrigin(url), body)
}

func postJSONWithHostOrigin(t *testing.T, c *http.Client, rawURL, host, origin, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if host != "" {
		req.Host = host
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func serveAdminRequest(t *testing.T, handler http.Handler, method, target, remoteAddr, origin, body string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remoteAddr
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Result()
}

func TestBootstrapPasswordForcesChangeAndRotatesSessions(t *testing.T) {
	const bootstrap = "one-time-bootstrap-password"
	s := newAdminBootstrapServer(t, bootstrap)
	ts, c := adminTestClient(t, s.Routes())

	r := postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"one-time-bootstrap-password"}`)
	if r.StatusCode != 200 {
		t.Fatalf("login=%d", r.StatusCode)
	}
	var login map[string]any
	_ = json.NewDecoder(r.Body).Decode(&login)
	r.Body.Close()
	if login["must_change_password"] != true {
		t.Fatalf("login=%#v", login)
	}

	r, _ = c.Get(ts.URL + "/api/account")
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("protected status=%d", r.StatusCode)
	}

	r = postJSON(t, c, ts.URL+"/api/admin/change-password", `{"current_password":"one-time-bootstrap-password","new_password":"a-new-password-123"}`)
	if r.StatusCode != 200 {
		t.Fatalf("change=%d", r.StatusCode)
	}
	r.Body.Close()

	r, _ = c.Get(ts.URL + "/api/account")
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session status=%d", r.StatusCode)
	}

	r = postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"a-new-password-123"}`)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("new login=%d", r.StatusCode)
	}
	r, _ = c.Get(ts.URL + "/api/account")
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("new session status=%d", r.StatusCode)
	}
}

func TestAdminLoginLocksAfterFiveFailures(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	ts, c := adminTestClient(t, s.Routes())
	for i := 0; i < 5; i++ {
		r := postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"wrong"}`)
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatalf("attempt %d=%d", i+1, r.StatusCode)
		}
	}
	r := postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	defer r.Body.Close()
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("locked=%d retry=%q", r.StatusCode, r.Header.Get("Retry-After"))
	}
}

func TestPersistedPasswordOverridesBootstrapEnv(t *testing.T) {
	path := t.TempDir() + "/admin-password"
	t.Setenv("M365_ADMIN_PASSWORD_FILE", path)
	t.Setenv("M365_ADMIN_PASSWORD", "old-bootstrap-password")
	if err := saveAdminPassword("persisted-new-password"); err != nil {
		t.Fatal(err)
	}
	credential, err := loadAdminCredential()
	if err != nil {
		t.Fatal(err)
	}
	if credential.Password != "persisted-new-password" || credential.Mode != adminCredentialPersisted {
		t.Fatalf("credential=%#v", credential)
	}
}

func TestExpiredLoginWindowResets(t *testing.T) {
	s := &Server{loginAttempts: map[string]loginAttempt{"x": {Failures: 4, WindowStart: time.Now().Add(-16 * time.Minute)}}}
	if ok, _ := s.loginAllowed("x", time.Now()); !ok {
		t.Fatal("expired window remained locked")
	}
}

func TestAdminRoutesRequireTrustedHTTPSAndMatchingOriginForNonLoopbackHosts(t *testing.T) {
	s := newAdminSecurityServerWithPolicy(t, "correct-password", "admin.example.test", "127.0.0.1/32")
	handler := s.Routes()

	direct := postJSONWithHostOrigin(t, &http.Client{Transport: localHandlerTransport{handler: handler}}, "http://127.0.0.1/api/admin/login", "admin.example.test", "http://admin.example.test", `{"password":"correct-password"}`)
	if direct.StatusCode == http.StatusOK {
		t.Fatalf("direct external HTTP login unexpectedly succeeded: %d", direct.StatusCode)
	}
	direct.Body.Close()

	proxy, client := adminProxyClient(t, handler)

	login := postJSONWithHostOrigin(t, client, proxy.URL+"/api/admin/login", "admin.example.test", "https://admin.example.test", `{"password":"correct-password"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("proxied login=%d body=%s", login.StatusCode, readResponseBody(t, login))
	}
	login.Body.Close()

	blocked := postJSONWithHostOrigin(t, client, proxy.URL+"/api/admin/change-password", "admin.example.test", "https://evil.example.test", `{"current_password":"correct-password","new_password":"replacement-password-123"}`)
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", blocked.StatusCode, readResponseBody(t, blocked))
	}
	blocked.Body.Close()

	allowed := postJSONWithHostOrigin(t, client, proxy.URL+"/api/admin/change-password", "admin.example.test", "https://admin.example.test", `{"current_password":"correct-password","new_password":"replacement-password-123"}`)
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("correct origin status=%d body=%s", allowed.StatusCode, readResponseBody(t, allowed))
	}
	allowed.Body.Close()
}

func TestTrustedProxyRequiresForwardedClientAddress(t *testing.T) {
	for _, forwardedFor := range []string{"", "127.0.0.1, invalid"} {
		s := newAdminSecurityServerWithPolicy(t, "correct-password", "admin.example.test", "127.0.0.1/32")
		headers := map[string]string{
			"X-Forwarded-Host":  "admin.example.test",
			"X-Forwarded-Proto": "https",
		}
		if forwardedFor != "" {
			headers["X-Forwarded-For"] = forwardedFor
		}
		response := serveAdminRequest(t, s.Routes(), http.MethodPost, "http://127.0.0.1/api/admin/login", "127.0.0.1:5000", "https://admin.example.test", `{"password":"correct-password"}`, headers)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("forwardedFor=%q status=%d body=%s", forwardedFor, response.StatusCode, readResponseBody(t, response))
		}
		response.Body.Close()
	}
}

func TestTrustedProxyRejectsAmbiguousForwardedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(http.Header)
	}{
		{
			name: "duplicate-host",
			mutate: func(h http.Header) {
				h.Add("X-Forwarded-Host", "other.example.test")
			},
		},
		{
			name: "comma-host",
			mutate: func(h http.Header) {
				h.Set("X-Forwarded-Host", "admin.example.test, other.example.test")
			},
		},
		{
			name: "duplicate-proto",
			mutate: func(h http.Header) {
				h.Add("X-Forwarded-Proto", "http")
			},
		},
		{
			name: "comma-proto",
			mutate: func(h http.Header) {
				h.Set("X-Forwarded-Proto", "https, http")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAdminSecurityServerWithPolicy(t, "correct-password", "admin.example.test", "127.0.0.1/32")
			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/admin/login", strings.NewReader(`{"password":"correct-password"}`))
			req.RemoteAddr = "127.0.0.1:5000"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://admin.example.test")
			req.Header.Set("X-Forwarded-For", "198.51.100.20")
			req.Header.Set("X-Forwarded-Host", "admin.example.test")
			req.Header.Set("X-Forwarded-Proto", "https")
			tc.mutate(req.Header)
			rr := httptest.NewRecorder()
			s.Routes().ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAdminRoutesRejectRemoteHTTPAndUnknownHosts(t *testing.T) {
	s := newAdminSecurityServerWithPolicy(t, "correct-password", "admin.example.test", "")
	handler := s.Routes()

	remoteHTTP := serveAdminRequest(t, handler, http.MethodPost, "http://admin.example.test/api/admin/login", "198.51.100.20:5000", "http://admin.example.test", `{"password":"correct-password"}`, nil)
	if remoteHTTP.StatusCode != http.StatusForbidden {
		t.Fatalf("remote HTTP status=%d body=%s", remoteHTTP.StatusCode, readResponseBody(t, remoteHTTP))
	}
	remoteHTTP.Body.Close()

	pathEscape := serveAdminRequest(t, handler, http.MethodPost, "http://admin.example.test/v1/../api/admin/login", "198.51.100.20:5000", "http://admin.example.test", `{"password":"correct-password"}`, nil)
	if pathEscape.StatusCode != http.StatusForbidden {
		t.Fatalf("path escape status=%d body=%s", pathEscape.StatusCode, readResponseBody(t, pathEscape))
	}
	pathEscape.Body.Close()

	unknownHost := serveAdminRequest(t, handler, http.MethodPost, "https://unknown.example.test/api/admin/login", "198.51.100.20:5000", "https://unknown.example.test", `{"password":"correct-password"}`, nil)
	if unknownHost.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown host status=%d body=%s", unknownHost.StatusCode, readResponseBody(t, unknownHost))
	}
	unknownHost.Body.Close()

	accepted := serveAdminRequest(t, handler, http.MethodPost, "https://admin.example.test/api/admin/login", "198.51.100.20:5000", "https://admin.example.test", `{"password":"correct-password"}`, nil)
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("accepted TLS status=%d body=%s", accepted.StatusCode, readResponseBody(t, accepted))
	}
	cookies := accepted.Cookies()
	accepted.Body.Close()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("secure session cookie=%#v", cookies)
	}
}

func TestLocalConsoleAllowsHTTP(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: localHandlerTransport{handler: s.Routes()}, Jar: jar}

	login := postJSON(t, client, "http://127.0.0.1/api/admin/login", `{"password":"correct-password"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login=%d body=%s", login.StatusCode, readResponseBody(t, login))
	}
	cookies := login.Cookies()
	login.Body.Close()
	if len(cookies) != 1 || cookies[0].Secure {
		t.Fatalf("local HTTP cookie=%#v", cookies)
	}

	accounts, err := client.Get("http://127.0.0.1/api/account")
	if err != nil {
		t.Fatal(err)
	}
	accounts.Body.Close()
	if accounts.StatusCode != http.StatusOK {
		t.Fatalf("accounts=%d", accounts.StatusCode)
	}
}

func TestUntrustedForwardedHeadersDoNotAffectAdminIdentity(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	response := serveAdminRequest(t, s.Routes(), http.MethodPost, "http://127.0.0.1/api/admin/login", "127.0.0.1:5000", "http://127.0.0.1", `{"password":"wrong"}`, map[string]string{
		"X-Forwarded-For":   "198.51.100.99",
		"X-Forwarded-Host":  "admin.example.test",
		"X-Forwarded-Proto": "https",
	})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.StatusCode, readResponseBody(t, response))
	}
	response.Body.Close()

	s.mu.Lock()
	_, directRecorded := s.loginAttempts["127.0.0.1"]
	_, forwardedRecorded := s.loginAttempts["198.51.100.99"]
	s.mu.Unlock()
	if !directRecorded || forwardedRecorded {
		t.Fatalf("login attempts direct=%v forwarded=%v", directRecorded, forwardedRecorded)
	}
}

func TestUnsafeAdminRequestsRequireExactOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		want   int
	}{
		{name: "missing", origin: "", want: http.StatusForbidden},
		{name: "cross-site", origin: "https://evil.example.test", want: http.StatusForbidden},
		{name: "path-not-origin", origin: "https://127.0.0.1/", want: http.StatusForbidden},
		{name: "same-origin", origin: "https://127.0.0.1", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAdminSecurityServer(t, "correct-password")
			response := serveAdminRequest(t, s.Routes(), http.MethodPost, "https://127.0.0.1/api/admin/login", "127.0.0.1:5000", tc.origin, `{"password":"correct-password"}`, nil)
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.StatusCode, tc.want, readResponseBody(t, response))
			}
			response.Body.Close()
		})
	}
}

func TestUnsafeAdminRequestsRejectMalformedOrigins(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origins []string
	}{
		{name: "null", origins: []string{"null"}},
		{name: "query", origins: []string{"https://127.0.0.1?x=1"}},
		{name: "fragment", origins: []string{"https://127.0.0.1#x"}},
		{name: "unsupported-scheme", origins: []string{"ftp://127.0.0.1"}},
		{name: "comma-joined", origins: []string{"https://127.0.0.1, https://evil.example.test"}},
		{name: "duplicate", origins: []string{"https://127.0.0.1", "https://evil.example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAdminSecurityServer(t, "correct-password")
			req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/api/admin/login", strings.NewReader(`{"password":"correct-password"}`))
			req.RemoteAddr = "127.0.0.1:5000"
			req.Header.Set("Content-Type", "application/json")
			for _, origin := range tc.origins {
				req.Header.Add("Origin", origin)
			}
			rr := httptest.NewRecorder()
			s.Routes().ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAdminLogoutRequiresPostAndRevokesServerSession(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	ts, client := adminTestClient(t, s.Routes())

	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login=%d", login.StatusCode)
	}

	wrongMethod, err := client.Get(ts.URL + "/api/admin/logout")
	if err != nil {
		t.Fatal(err)
	}
	wrongMethod.Body.Close()
	if wrongMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET logout=%d", wrongMethod.StatusCode)
	}

	stillAuthenticated, err := client.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	stillAuthenticated.Body.Close()
	if stillAuthenticated.StatusCode != http.StatusOK {
		t.Fatalf("session after rejected GET=%d", stillAuthenticated.StatusCode)
	}

	logout := postJSON(t, client, ts.URL+"/api/admin/logout", `{}`)
	logout.Body.Close()
	if logout.StatusCode != http.StatusOK {
		t.Fatalf("POST logout=%d", logout.StatusCode)
	}

	revoked, err := client.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session after logout=%d", revoked.StatusCode)
	}
}

func TestAdminAuthEndpointsEnforceMethods(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	ts, client := adminTestClient(t, s.Routes())

	wrongLogin, err := client.Get(ts.URL + "/api/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	wrongLogin.Body.Close()
	if wrongLogin.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET login=%d", wrongLogin.StatusCode)
	}

	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login=%d", login.StatusCode)
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "pkce-start-get", method: http.MethodGet, path: "/api/auth/start"},
		{name: "pkce-status-post", method: http.MethodPost, path: "/api/auth/status?state=test"},
		{name: "pkce-callback-put", method: http.MethodPut, path: "/api/auth/callback"},
		{name: "pkce-callback-head", method: http.MethodHead, path: "/api/auth/callback?state=test&code=test"},
		{name: "admin-session-post", method: http.MethodPost, path: "/api/admin/session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if adminRequestNeedsOrigin(tc.method) {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", requestOrigin(ts.URL))
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d", response.StatusCode)
			}
		})
	}
}

func TestAdminWebUsesPostForStateChangingAuthActions(t *testing.T) {
	loginPage, err := os.ReadFile("../../web/login.html")
	if err != nil {
		t.Fatal(err)
	}
	indexPage, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}

	for name, check := range map[string]bool{
		"login helper POST":       strings.Contains(string(loginPage), "fetch(url,{method:'POST',credentials:'same-origin'"),
		"logout POST":             strings.Contains(string(indexPage), "fetch('/api/admin/logout',{method:'POST',credentials:'same-origin'})"),
		"PKCE start POST":         strings.Contains(string(indexPage), "api('/api/auth/start',options)"),
		"PKCE active reauth":      !strings.Contains(string(indexPage), "stageActive:true"),
		"PKCE callback JSON POST": strings.Contains(string(indexPage), "api('/api/auth/callback',{method:'POST',body:JSON.stringify(payload)})"),
		"PKCE callback no query":  !strings.Contains(string(indexPage), "/api/auth/callback?"),
		"no fixed login default":  !strings.Contains(string(loginPage), "admin123"),
		"one-time bootstrap copy": strings.Contains(string(loginPage), "一次性 bootstrap secret"),
	} {
		if !check {
			t.Fatalf("missing UI method contract: %s", name)
		}
	}
}

func TestNonLoopbackAdministrationRejectsBootstrapCredentials(t *testing.T) {
	s := newAdminBootstrapServerWithPolicy(t, "one-time-bootstrap-password", "admin.example.test", "")
	response := serveAdminRequest(t, s.Routes(), http.MethodPost, "https://admin.example.test/api/admin/login", "198.51.100.20:5000", "https://admin.example.test", `{"password":"one-time-bootstrap-password"}`, nil)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.StatusCode, readResponseBody(t, response))
	}
	response.Body.Close()
}

func TestV1APIKeyRoutesIgnoreAdminHostOriginPolicy(t *testing.T) {
	s := newAdminSecurityServerWithPolicy(t, "correct-password", "admin.example.test", "")
	_, key, err := s.apiKeys.create("route-policy-test")
	if err != nil {
		t.Fatal(err)
	}

	valid := serveAdminRequest(t, s.Routes(), http.MethodGet, "http://untrusted.example.test/v1/models", "198.51.100.20:5000", "https://evil.example.test", "", map[string]string{"Authorization": "Bearer " + key})
	if valid.StatusCode != http.StatusOK {
		t.Fatalf("valid API key status=%d body=%s", valid.StatusCode, readResponseBody(t, valid))
	}
	valid.Body.Close()

	xAPIKey := serveAdminRequest(t, s.Routes(), http.MethodGet, "http://untrusted.example.test/v1/models", "198.51.100.20:5000", "https://evil.example.test", "", map[string]string{"X-API-Key": key})
	if xAPIKey.StatusCode != http.StatusOK {
		t.Fatalf("X-API-Key status=%d body=%s", xAPIKey.StatusCode, readResponseBody(t, xAPIKey))
	}
	xAPIKey.Body.Close()

	invalid := serveAdminRequest(t, s.Routes(), http.MethodGet, "http://untrusted.example.test/v1/models", "198.51.100.20:5000", "", "", map[string]string{"Authorization": "Bearer invalid"})
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid API key status=%d body=%s", invalid.StatusCode, readResponseBody(t, invalid))
	}
	invalid.Body.Close()
}

func TestAdminSecurityPolicyRejectsNonLoopbackTrustedProxy(t *testing.T) {
	if _, err := parseTrustedLoopbackProxy("10.0.0.1"); err == nil {
		t.Fatal("non-loopback trusted proxy was accepted")
	}
	if _, err := parseTrustedLoopbackProxy("127.0.0.0/8"); err != nil {
		t.Fatalf("loopback CIDR rejected: %v", err)
	}
	if _, err := parseAdminAuthority("https://admin.example.test"); err == nil {
		t.Fatal("host allowlist accepted a URL")
	}
	if _, err := parseAdminAuthority("*.example.test"); err == nil {
		t.Fatal("host allowlist accepted a wildcard")
	}
}

func TestAdminSessionExpiresAfterIdleTimeoutAndAbsoluteDeadline(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	now := time.Date(2026, time.August, 6, 14, 0, 0, 0, time.UTC)
	s.clock = func() time.Time { return now }
	ts, c := adminTestClient(t, s.Routes())

	login := postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login=%d body=%s", login.StatusCode, readResponseBody(t, login))
	}
	login.Body.Close()

	now = now.Add(adminSessionIdleTimeout - time.Minute)
	active, err := c.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	active.Body.Close()
	if active.StatusCode != http.StatusOK {
		t.Fatalf("active session status=%d", active.StatusCode)
	}

	now = now.Add(adminSessionIdleTimeout - time.Minute)
	refreshed, err := c.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	refreshed.Body.Close()
	if refreshed.StatusCode != http.StatusOK {
		t.Fatalf("refreshed idle session status=%d", refreshed.StatusCode)
	}

	now = now.Add(adminSessionIdleTimeout + time.Second)
	idleExpired, err := c.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	idleExpired.Body.Close()
	if idleExpired.StatusCode != http.StatusUnauthorized {
		t.Fatalf("idle expiry status=%d", idleExpired.StatusCode)
	}

	login = postJSON(t, c, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("relogin=%d body=%s", login.StatusCode, readResponseBody(t, login))
	}
	login.Body.Close()

	var token string
	var absoluteDeadline time.Time
	s.mu.Lock()
	for key, session := range s.adminSessions {
		token = key
		absoluteDeadline = session.ExpiresAt
		session.LastSeenAt = session.ExpiresAt.Add(-time.Second)
		s.adminSessions[key] = session
	}
	s.mu.Unlock()
	if token == "" {
		t.Fatal("session token missing after relogin")
	}

	now = absoluteDeadline
	absoluteExpired, err := c.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	absoluteExpired.Body.Close()
	if absoluteExpired.StatusCode != http.StatusUnauthorized {
		t.Fatalf("absolute expiry status=%d", absoluteExpired.StatusCode)
	}
}

func readResponseBody(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
