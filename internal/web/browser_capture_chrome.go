package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"m365-native/internal/auth"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	browserCDPStartupTimeout = 30 * time.Second
	browserCDPCommandTimeout = 5 * time.Second
)

type devToolsTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type browserCDPEvent struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Error  *struct {
		Code int    `json:"code"`
		Text string `json:"message"`
	} `json:"error"`
	Result struct {
		ErrorText string `json:"errorText"`
	} `json:"result"`
	Params struct {
		Request struct {
			URL string `json:"url"`
		} `json:"request"`
		URL string `json:"url"`
	} `json:"params"`
}

func runBrowserPKCECapture(ctx context.Context, request browserPKCECaptureRequest) (browserPKCECapturedAuthorization, error) {
	if taskSpace := strings.TrimSpace(os.Getenv("M365_EGO_BROWSER_TASK_SPACE")); taskSpace != "" {
		return runEgoBrowserPKCECapture(ctx, request, taskSpace)
	}
	return runCDPLaunchBrowserPKCECapture(ctx, request)
}

func runCDPLaunchBrowserPKCECapture(ctx context.Context, request browserPKCECaptureRequest) (browserPKCECapturedAuthorization, error) {
	if strings.TrimSpace(request.AuthorizationURL) == "" || strings.TrimSpace(request.RedirectURI) == "" || strings.TrimSpace(request.State) == "" || strings.TrimSpace(request.ProfileDir) == "" {
		return browserPKCECapturedAuthorization{}, errors.New("browser PKCE capture request is incomplete")
	}
	if err := validateBrowserAuthorizationURL(request.AuthorizationURL); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if err := validateBrowserRedirectURI(request.RedirectURI); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if err := os.MkdirAll(request.ProfileDir, 0o700); err != nil {
		return browserPKCECapturedAuthorization{}, fmt.Errorf("create browser profile: %w", err)
	}
	if err := os.Chmod(request.ProfileDir, 0o700); err != nil {
		return browserPKCECapturedAuthorization{}, fmt.Errorf("secure browser profile: %w", err)
	}

	browserPath, err := resolveCDPBrowserExecutable()
	if err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	activePortPath := filepath.Join(request.ProfileDir, "DevToolsActivePort")
	_ = os.Remove(activePortPath)

	cmd := exec.CommandContext(ctx, browserPath,
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=0",
		"--remote-allow-origins=*",
		"--user-data-dir="+request.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-session-crashed-bubble",
		"--disable-blink-features=AutomationControlled",
		"--new-window",
		"about:blank",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return browserPKCECapturedAuthorization{}, fmt.Errorf("start browser: %w", err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()

	port, browserWebSocketURL, err := waitForDevToolsActivePort(ctx, activePortPath, processDone)
	if err != nil {
		stopCDPBrowser("", cmd.Process, processDone)
		return browserPKCECapturedAuthorization{}, err
	}
	defer func() {
		stopCDPBrowser(browserWebSocketURL, cmd.Process, processDone)
		_ = os.Remove(activePortPath)
	}()

	targetWebSocketURL, err := waitForDevToolsPage(ctx, port, processDone)
	if err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, targetWebSocketURL, nil)
	if err != nil {
		return browserPKCECapturedAuthorization{}, fmt.Errorf("connect browser devtools: %w", err)
	}
	defer conn.Close()

	if err := writeCDPCommandAndWait(ctx, conn, 1, "Network.enable", map[string]any{}); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if err := writeCDPCommandAndWait(ctx, conn, 2, "Page.enable", map[string]any{}); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if err := writeCDPCommandAndWait(ctx, conn, 3, "Page.addScriptToEvaluateOnNewDocument", map[string]any{
		"source": `Object.defineProperty(navigator,"webdriver",{get:()=>undefined});`,
	}); err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	earlyCapture, capturedEarly, err := writeCDPNavigateAndWait(ctx, conn, 4, request)
	if err != nil {
		return browserPKCECapturedAuthorization{}, err
	}
	if capturedEarly {
		return earlyCapture, nil
	}

	messages := make(chan []byte)
	readErrors := make(chan error, 1)
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				readErrors <- err
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return browserPKCECapturedAuthorization{}, ctx.Err()
		case err := <-readErrors:
			return browserPKCECapturedAuthorization{}, fmt.Errorf("read browser devtools event: %w", err)
		case message := <-messages:
			if captured, ok := captureBrowserPKCEAuthorizationFromCDPMessage(message, request); ok {
				return captured, nil
			}
		}
	}
}

func writeCDPCommand(conn *websocket.Conn, id int, method string, params map[string]any) error {
	if err := conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("send browser devtools command %s: %w", method, err)
	}
	return nil
}

func writeCDPCommandAndWait(ctx context.Context, conn *websocket.Conn, id int, method string, params map[string]any) error {
	deadline := time.Now().Add(browserCDPCommandTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("bound browser devtools command %s: %w", method, err)
	}
	defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck
	if err := writeCDPCommand(conn, id, method, params); err != nil {
		return err
	}
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("confirm browser devtools command %s: %w", method, err)
		}
		done, failed := cdpCommandAcknowledged(message, id)
		if !done {
			continue
		}
		if failed {
			return fmt.Errorf("browser devtools command %s failed", method)
		}
		return nil
	}
}

func writeCDPNavigateAndWait(ctx context.Context, conn *websocket.Conn, id int, request browserPKCECaptureRequest) (browserPKCECapturedAuthorization, bool, error) {
	deadline := time.Now().Add(browserCDPCommandTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return browserPKCECapturedAuthorization{}, false, fmt.Errorf("bound browser devtools command Page.navigate: %w", err)
	}
	defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck
	if err := writeCDPCommand(conn, id, "Page.navigate", map[string]any{"url": request.AuthorizationURL}); err != nil {
		return browserPKCECapturedAuthorization{}, false, err
	}
	var captured browserPKCECapturedAuthorization
	capturedEarly := false
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			return browserPKCECapturedAuthorization{}, false, fmt.Errorf("confirm browser devtools command Page.navigate: %w", err)
		}
		if candidate, ok := captureBrowserPKCEAuthorizationFromCDPMessage(message, request); ok {
			captured = candidate
			capturedEarly = true
		}
		done, failed := cdpCommandAcknowledged(message, id)
		if !done {
			continue
		}
		if failed {
			return browserPKCECapturedAuthorization{}, false, errors.New("browser devtools command Page.navigate failed")
		}
		return captured, capturedEarly, nil
	}
}

func cdpCommandAcknowledged(message []byte, expectedID int) (bool, bool) {
	var response browserCDPEvent
	if err := json.Unmarshal(message, &response); err != nil || response.ID != expectedID {
		return false, false
	}
	return true, response.Error != nil || response.Result.ErrorText != ""
}

func captureBrowserPKCEAuthorizationFromCDPMessage(message []byte, request browserPKCECaptureRequest) (browserPKCECapturedAuthorization, bool) {
	var event browserCDPEvent
	if err := json.Unmarshal(message, &event); err != nil {
		return browserPKCECapturedAuthorization{}, false
	}
	candidate := ""
	switch event.Method {
	case "Network.requestWillBeSent":
		candidate = event.Params.Request.URL
	case "Page.frameRequestedNavigation":
		candidate = event.Params.URL
	}
	return captureBrowserPKCEAuthorization(candidate, request.RedirectURI, request.State)
}

func captureBrowserPKCEAuthorization(candidate, redirectURI, expectedState string) (browserPKCECapturedAuthorization, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || !oauthRedirectURLMatches(candidate, redirectURI) {
		return browserPKCECapturedAuthorization{}, false
	}
	input, err := oauthInputFromURL(candidate)
	if err != nil {
		return browserPKCECapturedAuthorization{}, false
	}
	input = trimOAuthCallbackInput(input)
	if input.State != expectedState || validateOAuthCallbackMaterial(input) != nil {
		return browserPKCECapturedAuthorization{}, false
	}
	return browserPKCECapturedAuthorization{Code: input.Code, State: input.State, Error: input.Error}, true
}

func waitForDevToolsActivePort(ctx context.Context, path string, processDone <-chan error) (int, string, error) {
	startupCtx, cancel := context.WithTimeout(ctx, browserCDPStartupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-startupCtx.Done():
			if port, browserWebSocketURL, ok := readDevToolsActivePort(path); ok {
				return port, browserWebSocketURL, nil
			}
			return 0, "", fmt.Errorf("browser devtools startup: %w", startupCtx.Err())
		case err := <-processDone:
			if err == nil {
				err = errors.New("browser exited")
			}
			return 0, "", fmt.Errorf("browser exited before devtools became ready: %w", err)
		case <-ticker.C:
			if port, browserWebSocketURL, ok := readDevToolsActivePort(path); ok {
				return port, browserWebSocketURL, nil
			}
		}
	}
}

func readDevToolsActivePort(path string) (int, string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, "", false
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		return 0, "", false
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || port <= 0 || port > 65535 {
		return 0, "", false
	}
	webSocketPath := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(webSocketPath, "/") {
		return 0, "", false
	}
	return port, "ws://127.0.0.1:" + strconv.Itoa(port) + webSocketPath, true
}

func waitForDevToolsPage(ctx context.Context, port int, processDone <-chan error) (string, error) {
	startupCtx, cancel := context.WithTimeout(ctx, browserCDPStartupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port) + "/json/list"
	for {
		select {
		case <-startupCtx.Done():
			return "", fmt.Errorf("browser page target startup: %w", startupCtx.Err())
		case err := <-processDone:
			if err == nil {
				err = errors.New("browser exited")
			}
			return "", fmt.Errorf("browser exited before page target became ready: %w", err)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(startupCtx, http.MethodGet, endpoint, nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var targets []devToolsTarget
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&targets)
			resp.Body.Close()
			if decodeErr != nil || resp.StatusCode != http.StatusOK {
				continue
			}
			for _, target := range targets {
				if target.Type == "page" && target.WebSocketDebuggerURL != "" && (target.URL == "about:blank" || strings.HasPrefix(target.URL, "chrome://newtab")) {
					return target.WebSocketDebuggerURL, nil
				}
			}
			for _, target := range targets {
				if target.Type == "page" && target.WebSocketDebuggerURL != "" {
					return target.WebSocketDebuggerURL, nil
				}
			}
		}
	}
}

func stopCDPBrowser(browserWebSocketURL string, process *os.Process, processDone <-chan error) {
	if browserWebSocketURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if conn, _, err := websocket.DefaultDialer.DialContext(ctx, browserWebSocketURL, nil); err == nil {
			_ = writeCDPCommand(conn, 1, "Browser.close", map[string]any{})
			_ = conn.Close()
		}
		cancel()
	}
	select {
	case <-processDone:
		return
	case <-time.After(2 * time.Second):
	}
	if process != nil {
		_ = process.Kill()
	}
}

func resolveCDPBrowserExecutable() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("M365_BROWSER_PATH")); explicit != "" {
		if browserExecutableExists(explicit) {
			return explicit, nil
		}
		return "", errors.New("configured browser executable is unavailable")
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
			filepath.Join(home, "Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
			filepath.Join(home, "Applications/Brave Browser.app/Contents/MacOS/Brave Browser"),
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("PROGRAMFILES")
		programFilesX86 := os.Getenv("PROGRAMFILES(X86)")
		candidates = []string{
			filepath.Join(programFiles, "Google/Chrome/Application/chrome.exe"),
			filepath.Join(localAppData, "Google/Chrome/Application/chrome.exe"),
			filepath.Join(programFilesX86, "Microsoft/Edge/Application/msedge.exe"),
			filepath.Join(programFiles, "BraveSoftware/Brave-Browser/Application/brave.exe"),
		}
	}
	for _, candidate := range candidates {
		if browserExecutableExists(candidate) {
			return candidate, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "microsoft-edge", "brave-browser", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no CDP-capable browser was found")
}

func browserExecutableExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func validateBrowserAuthorizationURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid browser authorization URL")
	}
	expected, err := url.Parse(auth.DefaultAuthority + "/oauth2/v2.0/authorize")
	if err != nil || oauthURLIdentity(parsed) != oauthURLIdentity(expected) {
		return errors.New("browser authorization URL is not the supported Microsoft authorize endpoint")
	}
	return nil
}

func validateBrowserRedirectURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !oauthRedirectURLMatches(raw, auth.DefaultRedirectURI) {
		return errors.New("browser redirect URI is not the supported Microsoft nativeclient redirect")
	}
	return nil
}
