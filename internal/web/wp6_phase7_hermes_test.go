package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"m365-native/internal/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	hermesV020Commit         = "3c27eb6234bf91b8ceee9e9071591b31e9b148cb"
	hermesFailureOutputLimit = 8 << 10
)

func hermesFailureOutput(output []byte, secret string) string {
	sanitized := string(output)
	if secret != "" {
		sanitized = strings.ReplaceAll(sanitized, secret, "[REDACTED]")
	}
	return boundedUTF8Preview(sanitized, hermesFailureOutputLimit)
}

func hermesChildEnv(home, source, origin, apiKey string) []string {
	return []string{
		"HOME=" + home,
		"HERMES_HOME=" + home,
		"PYTHONPATH=" + source,
		"PYTHONUTF8=1",
		"WP6_MCP_ENDPOINT=" + origin + "/v1/mcp",
		"WP6_MCP_LEGACY_ENDPOINT=" + origin + "/v1/mcp/sse",
		"WP6_MCP_API_KEY=" + apiKey,
	}
}

type mcpHTTPObservation struct {
	method          string
	path            string
	hasAuth         bool
	hasSession      bool
	sessionID       string
	protocolVersion string
	rpcMethod       string
}

func TestWP6HermesFailureOutputRedactsSecretBeforeBounding(t *testing.T) {
	const secret = "m365-secret-api-key"
	if got := hermesFailureOutput([]byte("before "+secret+" after"), secret); got != "before [REDACTED] after" {
		t.Fatalf("failure output redaction=%q", got)
	}
	raw := strings.Repeat("A", hermesFailureOutputLimit-4) + secret + strings.Repeat("B", hermesFailureOutputLimit) + secret

	got := hermesFailureOutput([]byte(raw), secret)
	if strings.Contains(got, secret) {
		t.Fatalf("failure output leaked API key: %q", got)
	}
	if len(got) > hermesFailureOutputLimit {
		t.Fatalf("failure output bytes=%d want <=%d", len(got), hermesFailureOutputLimit)
	}
}

func TestWP6HermesChildEnvIsIsolated(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "PARENT-CREDENTIAL-SENTINEL")
	want := []string{
		"HOME=/isolated/home",
		"HERMES_HOME=/isolated/home",
		"PYTHONPATH=/pinned/hermes",
		"PYTHONUTF8=1",
		"WP6_MCP_ENDPOINT=http://127.0.0.1:1234/v1/mcp",
		"WP6_MCP_LEGACY_ENDPOINT=http://127.0.0.1:1234/v1/mcp/sse",
		"WP6_MCP_API_KEY=generated-test-key",
	}
	got := hermesChildEnv("/isolated/home", "/pinned/hermes", "http://127.0.0.1:1234", "generated-test-key")
	if !slices.Equal(got, want) {
		t.Fatalf("Hermes child env=%q want=%q", got, want)
	}
	if strings.Contains(strings.Join(got, "\n"), "PARENT-CREDENTIAL-SENTINEL") {
		t.Fatal("Hermes child env inherited a parent credential")
	}
}

func TestWP6HermesV020MCPInterop(t *testing.T) {
	hermesSource := strings.TrimSpace(os.Getenv("WP6_HERMES_SOURCE"))
	hermesPython := strings.TrimSpace(os.Getenv("WP6_HERMES_PYTHON"))
	if hermesSource == "" || hermesPython == "" {
		t.Skip("set isolated pinned Hermes source and Python to run HERMES14")
	}
	identity, err := exec.Command("git", "-C", hermesSource, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(identity)) != hermesV020Commit {
		t.Fatalf("Hermes source is not pinned v0.20.0: sha=%q err=%v", strings.TrimSpace(string(identity)), err)
	}

	server := newAdminSecurityServer(t, "correct-password")
	t.Cleanup(func() { _ = server.Close() })
	_, apiKey, err := server.apiKeys.create("hermes-v020-mcp-acceptance")
	if err != nil {
		t.Fatal(err)
	}

	var observationMu sync.Mutex
	var observations []mcpHTTPObservation
	routes := server.Routes()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observation := mcpHTTPObservation{
			method:          r.Method,
			path:            r.URL.Path,
			hasAuth:         strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "),
			hasSession:      r.Header.Get(mcp.SessionHeader) != "",
			sessionID:       r.Header.Get(mcp.SessionHeader),
			protocolVersion: r.Header.Get(mcp.ProtocolHeader),
		}
		if r.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(body))
			var envelope struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &envelope)
			observation.rpcMethod = envelope.Method
		}
		observationMu.Lock()
		observations = append(observations, observation)
		observationMu.Unlock()
		routes.ServeHTTP(w, r)
	})
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	const marker = "HERMES14_MARKER_中文"
	script := `
import json
from tools.mcp_tool import register_mcp_servers, shutdown_mcp_servers
from tools.registry import registry

try:
    names = register_mcp_servers({
        "wp6_sidecar": {
            "url": __import__("os").environ["WP6_MCP_ENDPOINT"],
            "headers": {"Authorization": "Bearer " + __import__("os").environ["WP6_MCP_API_KEY"]},
            "skip_preflight": True,
            "connect_timeout": 5,
            "timeout": 5,
            "sampling": {"enabled": False},
            "elicitation": {"enabled": False},
            "tools": {"include": ["wp6_echo"], "resources": False, "prompts": False},
        },
        "wp6_legacy": {
            "url": __import__("os").environ["WP6_MCP_LEGACY_ENDPOINT"],
            "transport": "sse",
            "headers": {"Authorization": "Bearer " + __import__("os").environ["WP6_MCP_API_KEY"]},
            "connect_timeout": 5,
            "timeout": 5,
            "sampling": {"enabled": False},
            "elicitation": {"enabled": False},
            "tools": {"include": ["wp6_echo"], "resources": False, "prompts": False},
        }
    })
    expected = ["mcp__wp6_sidecar__wp6_echo", "mcp__wp6_legacy__wp6_echo"]
    if not all(name in names for name in expected):
        raise RuntimeError("expected MCP tools were not registered")
    print(registry.dispatch(expected[0], {"value": "HERMES14_MARKER_中文"}))
    print(registry.dispatch(expected[1], {"value": "HERMES14_LEGACY_中文"}))
finally:
    shutdown_mcp_servers()
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, hermesPython, "-c", script)
	command.Dir = hermesSource
	command.Env = hermesChildEnv(t.TempDir(), hermesSource, testServer.URL, apiKey)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned Hermes MCP round trip failed: %v\n%s", err, hermesFailureOutput(output, apiKey))
	}
	if !strings.Contains(string(output), `"result": "WP6_ECHO:`+marker+`"`) {
		t.Fatalf("pinned Hermes MCP result mismatch: %s", hermesFailureOutput(output, apiKey))
	}
	if !strings.Contains(string(output), `"result": "WP6_ECHO:HERMES14_LEGACY_中文"`) {
		t.Fatalf("pinned Hermes legacy SSE result mismatch: %s", hermesFailureOutput(output, apiKey))
	}

	observationMu.Lock()
	terminatedSessionID := ""
	for _, observation := range observations {
		if observation.path == "/v1/mcp" && observation.sessionID != "" {
			terminatedSessionID = observation.sessionID
		}
	}
	observationMu.Unlock()
	if terminatedSessionID == "" {
		t.Fatal("Hermes MCP session ID was not observed")
	}
	afterShutdown := wp6MCPRequest(t, routes, http.MethodPost, "/v1/mcp", apiKey, terminatedSessionID, mcp.LatestProtocolVersion, "", wp6MCPBody(99, "tools/list", nil))
	if afterShutdown.Code != http.StatusNotFound {
		t.Fatalf("Hermes MCP session survived shutdown: status=%d body=%s", afterShutdown.Code, afterShutdown.Body.String())
	}
	observationMu.Lock()
	defer observationMu.Unlock()
	wantRPC := map[string]bool{"initialize": false, "notifications/initialized": false, "tools/list": false, "tools/call": false}
	sawDelete := false
	sawLegacySSE := false
	legacyRPC := map[string]bool{"initialize": false, "notifications/initialized": false, "tools/list": false, "tools/call": false}
	for _, observation := range observations {
		if !observation.hasAuth {
			t.Fatalf("Hermes MCP request omitted authorization: %+v", observation)
		}
		if observation.path == "/v1/mcp" {
			if _, tracked := wantRPC[observation.rpcMethod]; tracked {
				wantRPC[observation.rpcMethod] = true
			}
		}
		if observation.path == "/v1/mcp/sse" && observation.method == http.MethodGet {
			sawLegacySSE = true
		}
		if observation.path == "/v1/mcp/message" {
			if _, tracked := legacyRPC[observation.rpcMethod]; tracked {
				legacyRPC[observation.rpcMethod] = true
			}
		}
		if observation.path == "/v1/mcp" && observation.rpcMethod != "initialize" && observation.rpcMethod != "" && (!observation.hasSession || observation.protocolVersion != mcp.LatestProtocolVersion) {
			t.Fatalf("post-initialize MCP headers missing: %+v", observation)
		}
		if observation.path == "/v1/mcp" && observation.method == http.MethodDelete {
			sawDelete = observation.hasSession && observation.protocolVersion == mcp.LatestProtocolVersion
		}
	}
	for method, seen := range wantRPC {
		if !seen {
			t.Fatalf("Hermes did not send %s; observations=%+v", method, observations)
		}
	}
	if !sawDelete {
		t.Fatalf("Hermes did not terminate its MCP session; observations=%+v", observations)
	}
	if !sawLegacySSE {
		t.Fatalf("Hermes did not open the legacy SSE route; observations=%+v", observations)
	}
	for method, seen := range legacyRPC {
		if !seen {
			t.Fatalf("Hermes legacy SSE did not send %s; observations=%+v", method, observations)
		}
	}
}
