package web

import (
	"bytes"
	"encoding/json"
	"m365-native/internal/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func wp6MCPRequest(t *testing.T, handler http.Handler, method, path, key, sessionID, protocol, origin string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "https://sidecar.test"+path, bytes.NewReader(payload))
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set(mcp.SessionHeader, sessionID)
	}
	if protocol != "" {
		request.Header.Set(mcp.ProtocolHeader, protocol)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func wp6MCPBody(id any, method string, params any) []byte {
	request := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		request["id"] = id
	}
	if params != nil {
		request["params"] = params
	}
	body, _ := json.Marshal(request)
	return body
}

func TestWP6ProductionMCPRouteAuthOwnerAndRoundTrip(t *testing.T) {
	server := newAdminSecurityServer(t, "correct-password")
	t.Cleanup(func() { _ = server.Close() })
	_, keyA, err := server.apiKeys.create("mcp-owner-a")
	if err != nil {
		t.Fatal(err)
	}
	keyBRecord, keyB, err := server.apiKeys.create("mcp-owner-b")
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Routes()
	initializeBody := wp6MCPBody(0, "initialize", map[string]any{
		"protocolVersion": mcp.LatestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "Hermes", "version": "0.20.0"},
	})

	unauthorized := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", "", "", "", "", initializeBody)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	badOrigin := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, "", "", "https://evil.test", initializeBody)
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin status=%d", badOrigin.Code)
	}
	server.adminSecurity.allowedHosts = []adminAllowedHost{{host: "sidecar.test"}}
	forgedRequest := httptest.NewRequest(http.MethodPost, "https://attacker.test/v1/mcp", bytes.NewReader(initializeBody))
	forgedRequest.Header.Set("Authorization", "Bearer "+keyA)
	forgedRequest.Header.Set("Content-Type", "application/json")
	forgedRequest.Header.Set("Accept", "application/json, text/event-stream")
	forgedRequest.Header.Set("Origin", "https://attacker.test")
	forgedHost := httptest.NewRecorder()
	handler.ServeHTTP(forgedHost, forgedRequest)
	if forgedHost.Code != http.StatusForbidden {
		t.Fatalf("forged Host/Origin status=%d", forgedHost.Code)
	}
	matchingOrigin := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, "", "", "https://sidecar.test", initializeBody)
	if matchingOrigin.Code != http.StatusOK {
		t.Fatalf("trusted Origin status=%d body=%s", matchingOrigin.Code, matchingOrigin.Body.String())
	}
	server.mcp.Close()
	server.mcp = server.newMCPRuntime()

	initialized := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, "", "", "", initializeBody)
	if initialized.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", initialized.Code, initialized.Body.String())
	}
	sessionID := initialized.Header().Get(mcp.SessionHeader)
	if sessionID == "" {
		t.Fatal("initialize omitted MCP session ID")
	}

	hijack := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyB, sessionID, mcp.LatestProtocolVersion, "", wp6MCPBody(1, "tools/list", nil))
	if hijack.Code != http.StatusNotFound {
		t.Fatalf("cross-key session hijack status=%d body=%s", hijack.Code, hijack.Body.String())
	}
	notification := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, sessionID, mcp.LatestProtocolVersion, "", wp6MCPBody(nil, "notifications/initialized", map[string]any{}))
	if notification.Code != http.StatusAccepted || notification.Body.Len() != 0 {
		t.Fatalf("initialized notification status=%d body=%q", notification.Code, notification.Body.String())
	}
	listed := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, sessionID, mcp.LatestProtocolVersion, "", wp6MCPBody(1, "tools/list", map[string]any{}))
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"readOnlyHint":true`) || !strings.Contains(listed.Body.String(), `"name":"wp6_echo"`) {
		t.Fatalf("tools/list status=%d body=%s", listed.Code, listed.Body.String())
	}
	called := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyA, sessionID, mcp.LatestProtocolVersion, "", wp6MCPBody("call", "tools/call", map[string]any{"name": "wp6_echo", "arguments": map[string]any{"value": "HERMES14_MARKER"}}))
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), "WP6_ECHO:HERMES14_MARKER") || !strings.Contains(called.Body.String(), `"structuredContent":{"value":"HERMES14_MARKER"}`) {
		t.Fatalf("tools/call status=%d body=%s", called.Code, called.Body.String())
	}

	legacyWithoutKey := wp6MCPRequest(t, handler, http.MethodGet, "/v1/mcp/sse", "", "", "", "", nil)
	if legacyWithoutKey.Code != http.StatusUnauthorized {
		t.Fatalf("legacy unauthenticated status=%d", legacyWithoutKey.Code)
	}
	legacyWrongMethod := wp6MCPRequest(t, handler, http.MethodHead, "/v1/mcp/sse", keyA, "", "", "", nil)
	if legacyWrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("legacy route not mounted/authenticated: status=%d", legacyWrongMethod.Code)
	}

	if revoked, err := server.apiKeys.revoke(keyBRecord.ID); err != nil || !revoked {
		t.Fatalf("revoke key: revoked=%v err=%v", revoked, err)
	}
	revoked := wp6MCPRequest(t, handler, http.MethodPost, "/v1/mcp", keyB, sessionID, mcp.LatestProtocolVersion, "", wp6MCPBody(2, "tools/list", nil))
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked API key status=%d", revoked.Code)
	}
	if protocol, path := debugProtocolAndPath("/v1/mcp/message"); protocol != "mcp" || path != "/v1/mcp/message" {
		t.Fatalf("debug route classification=%q %q", protocol, path)
	}
}
