package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m365-native/internal/auth"
)

func TestBrowserPKCECallbackMatchesRequiresExactRedirectStateAndMaterial(t *testing.T) {
	const (
		redirect = "https://login.microsoftonline.com/common/oauth2/nativeclient"
		state    = "expected-state"
	)
	cases := []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "authorization code", candidate: redirect + "?code=one-time-code&state=" + state, want: true},
		{name: "provider error", candidate: redirect + "?error=access_denied&state=" + state, want: true},
		{name: "wrong state", candidate: redirect + "?code=one-time-code&state=other", want: false},
		{name: "wrong host", candidate: "https://example.test/common/oauth2/nativeclient?code=one-time-code&state=" + state, want: false},
		{name: "wrong path", candidate: "https://login.microsoftonline.com/common/wrongplace?code=one-time-code&state=" + state, want: false},
		{name: "missing material", candidate: redirect + "?state=" + state, want: false},
		{name: "duplicate state", candidate: redirect + "?code=one-time-code&state=" + state + "&state=other", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured, got := captureBrowserPKCEAuthorization(tc.candidate, redirect, state)
			if got != tc.want {
				t.Fatalf("captureBrowserPKCEAuthorization()=%v want %v", got, tc.want)
			}
			if got && captured.State != state {
				t.Fatalf("captured state=%q want %q", captured.State, state)
			}
			if got && strings.Contains(strings.Join([]string{captured.Code, captured.State, captured.Error}, "\n"), redirect) {
				t.Fatal("capture result retained the full callback URL")
			}
		})
	}
}

func TestBrowserPKCECaptureRecognizesTransientCDPEvents(t *testing.T) {
	request := browserPKCECaptureRequest{
		RedirectURI: auth.DefaultRedirectURI,
		State:       "expected-state",
	}
	cases := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "network request", message: `{"method":"Network.requestWillBeSent","params":{"request":{"url":"https://login.microsoftonline.com/common/oauth2/nativeclient?code=one-time-code&state=expected-state"}}}`, want: true},
		{name: "frame navigation", message: `{"method":"Page.frameRequestedNavigation","params":{"url":"https://login.microsoftonline.com/common/oauth2/nativeclient?error=access_denied&state=expected-state"}}`, want: true},
		{name: "unrelated request", message: `{"method":"Network.requestWillBeSent","params":{"request":{"url":"https://login.microsoftonline.com/common/oauth2/wrongplace"}}}`},
		{name: "command response", message: `{"id":4,"result":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured, got := captureBrowserPKCEAuthorizationFromCDPMessage([]byte(tc.message), request)
			if got != tc.want {
				t.Fatalf("captureBrowserPKCEAuthorizationFromCDPMessage()=%v want %v", got, tc.want)
			}
			if got && captured.State != request.State {
				t.Fatalf("captured state=%q want %q", captured.State, request.State)
			}
		})
	}
}

func TestCDPCommandAcknowledgedRequiresMatchingSuccessfulResponse(t *testing.T) {
	cases := []struct {
		name       string
		message    string
		wantDone   bool
		wantFailed bool
	}{
		{name: "success", message: `{"id":4,"result":{"frameId":"one"}}`, wantDone: true},
		{name: "provider error", message: `{"id":4,"error":{"code":-32000,"message":"navigation failed"}}`, wantDone: true, wantFailed: true},
		{name: "navigation error", message: `{"id":4,"result":{"errorText":"net::ERR_FAILED"}}`, wantDone: true, wantFailed: true},
		{name: "different command", message: `{"id":3,"result":{}}`},
		{name: "event", message: `{"method":"Network.requestWillBeSent","params":{}}`},
		{name: "invalid", message: `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, failed := cdpCommandAcknowledged([]byte(tc.message), 4)
			if done != tc.wantDone || failed != tc.wantFailed {
				t.Fatalf("cdpCommandAcknowledged()=(%v,%v) want (%v,%v)", done, failed, tc.wantDone, tc.wantFailed)
			}
		})
	}
}

func TestWaitForDevToolsActivePortReadsReadyFileAtContextBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "DevToolsActivePort")
	if err := os.WriteFile(path, []byte("59606\n/devtools/browser/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	port, browserWebSocketURL, err := waitForDevToolsActivePort(ctx, path, make(chan error))
	if err != nil {
		t.Fatalf("waitForDevToolsActivePort() error=%v", err)
	}
	if port != 59606 || browserWebSocketURL != "ws://127.0.0.1:59606/devtools/browser/test" {
		t.Fatalf("unexpected DevTools endpoint port=%d url=%q", port, browserWebSocketURL)
	}
}

func TestBrowserPKCECaptureAcceptsOnlyDefaultMicrosoftAuthorizeAndRedirect(t *testing.T) {
	if err := validateBrowserAuthorizationURL(auth.DefaultAuthority + "/oauth2/v2.0/authorize?client_id=test"); err != nil {
		t.Fatalf("default authorize URL rejected: %v", err)
	}
	if err := validateBrowserAuthorizationURL("https://example.test/common/oauth2/v2.0/authorize?client_id=test"); err == nil {
		t.Fatal("non-Microsoft authorize URL accepted")
	}
	if err := validateBrowserRedirectURI(auth.DefaultRedirectURI); err != nil {
		t.Fatalf("default redirect rejected: %v", err)
	}
	if err := validateBrowserRedirectURI(auth.DefaultRedirectURI + "?code=should-not-be-here"); err == nil {
		t.Fatal("redirect URI with query accepted")
	}
}

func TestEgoBrowserCaptureScriptUsesTaskSpaceNetworkCaptureAndNoOAuthLiterals(t *testing.T) {
	script := egoBrowserCaptureScript("/tmp/m365-ego-test.sock", "13")
	for _, required := range []string{
		"listTaskSpaces",
		"matches.length !== 1",
		"ownership !== \"agent\"",
		"useOrCreateTaskSpace(selected.id)",
		"createTab",
		"switchTab",
		"listTabs",
		"Network.enable",
		"Page.enable",
		"drainEvents",
		"Network.requestWillBeSent",
		"Page.frameRequestedNavigation",
		"socket.end",
		"tab.targetId === authTarget",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("ego-browser capture script missing %q", required)
		}
	}
	closeIndex := strings.Index(script, "await closeAuthTab()")
	finishIndex := strings.Index(script, "await finish(captured)")
	if closeIndex < 0 || finishIndex < 0 || closeIndex > finishIndex {
		t.Fatal("ego-browser capture must close its scratch tab before returning OAuth material")
	}
	for _, forbidden := range []string{
		"takeOverTaskSpace",
		"openOrReuseTab",
		"cliLog",
		"console.log",
		auth.DefaultClientID,
		auth.DefaultRedirectURI,
		"one-time-code",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("ego-browser capture script contains forbidden material %q", forbidden)
		}
	}
}

func TestEgoBrowserControlSocketIsPrivate(t *testing.T) {
	listener, path, err := listenEgoBrowserControlSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.RemoveAll(filepath.Dir(path))
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("control directory mode=%#o want 0700", dirInfo.Mode().Perm())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control socket mode=%#o want 0600", info.Mode().Perm())
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(path)), "m365-ego-") || filepath.Base(path) != "control.sock" {
		t.Fatalf("unexpected control socket path %q", path)
	}
}

func TestDefaultBrowserOAuthConfigPinsMicrosoftFirstPartyEndpoints(t *testing.T) {
	config := defaultClientOAuthConfig()
	if config.ClientID != auth.DefaultClientID || config.Authority != auth.DefaultAuthority || config.RedirectURI != auth.DefaultRedirectURI || config.Scope != auth.DefaultScope {
		t.Fatalf("default browser OAuth identity drifted: %#v", config)
	}
	if config.AuthorizeEndpoint != auth.DefaultAuthority+"/oauth2/v2.0/authorize" || config.TokenEndpoint != auth.DefaultAuthority+"/oauth2/v2.0/token" {
		t.Fatalf("default browser OAuth endpoints drifted: %#v", config)
	}
}

func TestBrowserPKCECapturesTransientNativeclientCallbackIntoStagedDefaultClient(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, _, activeStore := newOAuthLifecycleServer(t, endpoint)
	testConfig := lifecycleOAuthConfig(endpoint.server.URL)
	server.browserPKCEConfig = func() auth.OAuthConfig { return testConfig }
	activeBefore, err := os.ReadFile(activeStore.Path())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	statusBefore := testOAuthProfileStatus(t, activeStore.Path())

	captured := make(chan browserPKCECaptureRequest, 1)
	release := make(chan struct{})
	server.browserPKCERun = func(ctx context.Context, request browserPKCECaptureRequest) (browserPKCECapturedAuthorization, error) {
		captured <- request
		select {
		case <-ctx.Done():
			return browserPKCECapturedAuthorization{}, ctx.Err()
		case <-release:
		}
		return browserPKCECapturedAuthorization{Code: "browser-success", State: request.State}, nil
	}

	start := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/browser/default/start", "{}")
	if start.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	startJSON := decodeLifecycleJSON(t, start)
	if startJSON["status"] != "browser_pkce_started" || startJSON["staged"] != true {
		t.Fatalf("unexpected start response: %#v", startJSON)
	}
	state, _ := startJSON["state"].(string)
	if state == "" {
		t.Fatal("missing browser PKCE state")
	}
	concurrent := lifecycleAdminRequest(t, server, http.MethodPost, "http://127.0.0.1/api/auth/browser/default/start", "{}")
	if concurrent.Code != http.StatusConflict || !strings.Contains(concurrent.Body.String(), "oauth_browser_session_active") {
		t.Fatalf("concurrent start status=%d body=%s", concurrent.Code, concurrent.Body.String())
	}
	for _, forbidden := range []string{"browser-success", "callback", "code=", "verifier"} {
		if strings.Contains(start.Body.String(), forbidden) {
			t.Fatalf("start response leaked %q: %s", forbidden, start.Body.String())
		}
	}

	request := <-captured
	if request.State != state || request.RedirectURI != testConfig.RedirectURI {
		t.Fatalf("unexpected browser capture request: %#v", request)
	}
	parsed, err := url.Parse(request.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("client_id") != testConfig.ClientID || parsed.Query().Get("state") != state {
		t.Fatalf("unexpected browser authorization URL")
	}
	if request.ProfileDir == "" {
		t.Fatal("missing persistent browser profile directory")
	}
	close(release)

	var status map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recorder := lifecycleAdminRequest(t, server, http.MethodGet, "http://127.0.0.1/api/auth/status?state="+url.QueryEscape(state), "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		status = decodeLifecycleJSON(t, recorder)
		if status["status"] == "authenticated" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status["status"] != "authenticated" || status["staged"] != true {
		t.Fatalf("browser PKCE did not authenticate: %#v", status)
	}
	account, ok := status["account"].(map[string]any)
	if !ok || account["status"] != "online" || account["profileRef"] != nil {
		t.Fatalf("invalid single-account result: %#v", status["account"])
	}

	statusAfter := testOAuthProfileStatus(t, activeStore.Path())
	if statusAfter.ActiveProfileID != statusBefore.ActiveProfileID || statusAfter.Generation != statusBefore.Generation {
		t.Fatalf("active profile changed: before=%#v after=%#v", statusBefore, statusAfter)
	}
	if len(statusAfter.Profiles) != len(statusBefore.Profiles)+1 {
		t.Fatalf("candidate profile count did not increase: before=%#v after=%#v", statusBefore, statusAfter)
	}
	activeAfter, err := os.ReadFile(activeStore.Path())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !bytes.Equal(activeBefore, activeAfter) {
		t.Fatal("browser PKCE changed active token store bytes")
	}
}

func TestBrowserPKCERouteRequiresAdminSession(t *testing.T) {
	endpoint := newLifecycleTokenEndpoint(t)
	server, _, _ := newOAuthLifecycleServer(t, endpoint)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/browser/default/start", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:41000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1")
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBrowserPKCEManagementUIUsesAutomaticCapture(t *testing.T) {
	raw, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, required := range []string{"預設第一方 Client 瀏覽器授權", "/api/auth/browser/default/start", "startBrowserCandidate()", "wrongplace 前自動擷取"} {
		if !strings.Contains(page, required) {
			t.Fatalf("management UI missing %q", required)
		}
	}
	if strings.Contains(page, "/api/auth/device/default/start") {
		t.Fatal("management UI contains the rejected default-client device-code path")
	}
	if strings.Contains(page, "重新登入已套用") {
		t.Fatal("management UI claims a ChatHub-only candidate validation was activated")
	}
	for _, required := range []string{"候選登入已保留", "尚未套用"} {
		if !strings.Contains(page, required) {
			t.Fatalf("management UI does not keep candidate validation separate from activation: missing %q", required)
		}
	}
}
