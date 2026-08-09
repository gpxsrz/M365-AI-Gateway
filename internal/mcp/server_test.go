package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testOwner = "api-key-owner"

func testProvider(t *testing.T) ToolProvider {
	t.Helper()
	return NewStaticToolProvider([]Tool{{
		Name:        "wp6_echo",
		Description: "Return the supplied marker.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"marker": map[string]any{"type": "string"}},
			"required":   []any{"marker"},
		},
	}}, func(_ context.Context, name string, args map[string]any) (CallResult, error) {
		if name != "wp6_echo" {
			return CallResult{}, fmt.Errorf("unknown tool")
		}
		marker, _ := args["marker"].(string)
		return CallResult{Content: []map[string]any{{"type": "text", "text": "WP6_ECHO:" + marker}}, StructuredData: map[string]any{"marker": marker}}, nil
	})
}

func rpcBody(id any, method string, params any) []byte {
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

func streamableRequest(t *testing.T, handler http.Handler, owner, sessionID, protocol string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set(SessionHeader, sessionID)
	}
	if protocol != "" {
		req.Header.Set(ProtocolHeader, protocol)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), testOwnerContextKey{}, owner)))
	return rec
}

type testOwnerContextKey struct{}

func TestWP6StreamableHTTPInitializeListCallAndDelete(t *testing.T) {
	server := NewServer(ServerOptions{Provider: testProvider(t)})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, _ := r.Context().Value(testOwnerContextKey{}).(string)
		server.ServeStreamableHTTP(w, r, owner)
	})

	initialize := streamableRequest(t, handler, testOwner, "", "", rpcBody("init-1", "initialize", map[string]any{
		"protocolVersion": LatestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}))
	if initialize.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", initialize.Code, initialize.Body.String())
	}
	sessionID := initialize.Header().Get(SessionHeader)
	if len(sessionID) < 32 || strings.Contains(initialize.Body.String(), sessionID) {
		t.Fatalf("session ID missing, weak, or leaked in body: header=%q body=%s", sessionID, initialize.Body.String())
	}
	var initializedResponse struct {
		ID     string `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initialize.Body.Bytes(), &initializedResponse); err != nil {
		t.Fatal(err)
	}
	if initializedResponse.ID != "init-1" || initializedResponse.Result.ProtocolVersion != LatestProtocolVersion {
		t.Fatalf("bad initialize response: %+v", initializedResponse)
	}

	initialized := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(nil, "notifications/initialized", map[string]any{}))
	if initialized.Code != http.StatusAccepted || initialized.Body.Len() != 0 {
		t.Fatalf("initialized status=%d body=%q", initialized.Code, initialized.Body.String())
	}

	listed := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(2, "tools/list", map[string]any{}))
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"name":"wp6_echo"`) {
		t.Fatalf("tools/list status=%d body=%s", listed.Code, listed.Body.String())
	}

	called := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("call-1", "tools/call", map[string]any{
		"name": "wp6_echo", "arguments": map[string]any{"marker": "中文😀END"},
	}))
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), "WP6_ECHO:中文😀END") || !strings.Contains(called.Body.String(), `"id":"call-1"`) {
		t.Fatalf("tools/call status=%d body=%s", called.Code, called.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "http://sidecar.test/v1/mcp", nil)
	deleteRequest.Header.Set(SessionHeader, sessionID)
	deleteRequest.Header.Set(ProtocolHeader, LatestProtocolVersion)
	deleteRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(deleteRecorder, deleteRequest, testOwner)
	if deleteRecorder.Code != http.StatusNoContent || server.SessionCount() != 0 {
		t.Fatalf("delete status=%d sessions=%d", deleteRecorder.Code, server.SessionCount())
	}
}

func TestWP6StreamableHTTPLifecycleValidationAndBounds(t *testing.T) {
	var calls atomic.Int32
	provider := NewStaticToolProvider([]Tool{{Name: "slow", InputSchema: map[string]any{"type": "object"}}}, func(ctx context.Context, _ string, _ map[string]any) (CallResult, error) {
		calls.Add(1)
		<-ctx.Done()
		return CallResult{}, ctx.Err()
	})
	server := NewServer(ServerOptions{Provider: provider, MaxSessions: 1, MaxPendingPerSession: 1, CallTimeout: 25 * time.Millisecond})

	initialize := func(owner string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(1, "initialize", map[string]any{
			"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
		})))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		server.ServeStreamableHTTP(rec, req, owner)
		return rec
	}
	first := initialize("owner-a")
	if first.Code != http.StatusOK {
		t.Fatalf("first init: %d %s", first.Code, first.Body.String())
	}
	second := initialize("owner-b")
	if second.Code != http.StatusServiceUnavailable || server.SessionCount() != 1 {
		t.Fatalf("session bound status=%d sessions=%d", second.Code, server.SessionCount())
	}
	sessionID := first.Header().Get(SessionHeader)

	wrongOwner := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(2, "tools/list", nil)))
	wrongOwner.Header.Set("Content-Type", "application/json")
	wrongOwner.Header.Set("Accept", "application/json, text/event-stream")
	wrongOwner.Header.Set(SessionHeader, sessionID)
	wrongOwner.Header.Set(ProtocolHeader, LatestProtocolVersion)
	wrongOwnerRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(wrongOwnerRecorder, wrongOwner, "owner-b")
	if wrongOwnerRecorder.Code != http.StatusNotFound {
		t.Fatalf("owner isolation status=%d body=%s", wrongOwnerRecorder.Code, wrongOwnerRecorder.Body.String())
	}

	preInitialized := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(3, "tools/list", nil)))
	preInitialized.Header.Set("Content-Type", "application/json")
	preInitialized.Header.Set("Accept", "application/json, text/event-stream")
	preInitialized.Header.Set(SessionHeader, sessionID)
	preInitialized.Header.Set(ProtocolHeader, LatestProtocolVersion)
	preInitializedRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(preInitializedRecorder, preInitialized, "owner-a")
	if !strings.Contains(preInitializedRecorder.Body.String(), `"code":-32600`) {
		t.Fatalf("pre-initialized request was accepted: %s", preInitializedRecorder.Body.String())
	}

	note := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(nil, "notifications/initialized", nil)))
	note.Header.Set("Content-Type", "application/json")
	note.Header.Set("Accept", "application/json, text/event-stream")
	note.Header.Set(SessionHeader, sessionID)
	note.Header.Set(ProtocolHeader, LatestProtocolVersion)
	noteRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(noteRecorder, note, "owner-a")

	slow := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(4, "tools/call", map[string]any{"name": "slow", "arguments": map[string]any{}})))
	slow.Header.Set("Content-Type", "application/json")
	slow.Header.Set("Accept", "application/json, text/event-stream")
	slow.Header.Set(SessionHeader, sessionID)
	slow.Header.Set(ProtocolHeader, LatestProtocolVersion)
	slowRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(slowRecorder, slow, "owner-a")
	if !strings.Contains(slowRecorder.Body.String(), `"isError":true`) || !strings.Contains(slowRecorder.Body.String(), "timed out") || server.PendingCount() != 0 || calls.Load() != 1 {
		t.Fatalf("timeout result=%s pending=%d calls=%d", slowRecorder.Body.String(), server.PendingCount(), calls.Load())
	}
}

func TestWP6MCPOriginAndMessageLimit(t *testing.T) {
	server := NewServer(ServerOptions{Provider: testProvider(t), MaxMessageBytes: 256})
	badOrigin := httptest.NewRequest(http.MethodPost, "https://sidecar.test/v1/mcp", bytes.NewReader(rpcBody(1, "initialize", map[string]any{
		"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "x", "version": "1"},
	})))
	badOrigin.Header.Set("Origin", "https://evil.test")
	badOrigin.Header.Set("Content-Type", "application/json")
	badOrigin.Header.Set("Accept", "application/json, text/event-stream")
	badOriginRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(badOriginRecorder, badOrigin, testOwner)
	if badOriginRecorder.Code != http.StatusForbidden || server.SessionCount() != 0 {
		t.Fatalf("bad origin status=%d sessions=%d", badOriginRecorder.Code, server.SessionCount())
	}
	sameHostOrigin := badOrigin.Clone(badOrigin.Context())
	sameHostOrigin.Header = badOrigin.Header.Clone()
	sameHostOrigin.Header.Set("Origin", "https://sidecar.test")
	sameHostOriginRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(sameHostOriginRecorder, sameHostOrigin, testOwner)
	if sameHostOriginRecorder.Code != http.StatusForbidden || server.SessionCount() != 0 {
		t.Fatalf("default browser-origin policy status=%d sessions=%d", sameHostOriginRecorder.Code, server.SessionCount())
	}

	oversized := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", strings.NewReader(strings.Repeat("x", 257)))
	oversized.Header.Set("Content-Type", "application/json")
	oversized.Header.Set("Accept", "application/json, text/event-stream")
	oversizedRecorder := httptest.NewRecorder()
	server.ServeStreamableHTTP(oversizedRecorder, oversized, testOwner)
	if oversizedRecorder.Code != http.StatusRequestEntityTooLarge || server.SessionCount() != 0 {
		t.Fatalf("oversized status=%d body=%s sessions=%d", oversizedRecorder.Code, oversizedRecorder.Body.String(), server.SessionCount())
	}
}

func TestWP6LegacySSERoundTripAndDisconnectCleanup(t *testing.T) {
	server := NewServer(ServerOptions{Provider: testProvider(t)})
	handler := http.NewServeMux()
	handler.HandleFunc("/v1/mcp/sse", func(w http.ResponseWriter, r *http.Request) { server.ServeLegacySSE(w, r, testOwner) })
	handler.HandleFunc("/v1/mcp/message", func(w http.ResponseWriter, r *http.Request) { server.ServeLegacyMessage(w, r, testOwner) })
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/mcp/sse", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	var endpoint string
	for endpoint == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.IsAbs() || parsed.Query().Get("sessionId") == "" {
		t.Fatalf("unsafe legacy endpoint %q err=%v", endpoint, err)
	}

	post := func(payload []byte) {
		res, err := http.Post(ts.URL+endpoint, "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("legacy post=%d body=%s", res.StatusCode, body)
		}
	}
	post(rpcBody(1, "initialize", map[string]any{"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}))
	readLegacyMessage := func() string {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
	}
	if message := readLegacyMessage(); !strings.Contains(message, LatestProtocolVersion) {
		t.Fatalf("legacy initialize response=%s", message)
	}
	post(rpcBody(nil, "notifications/initialized", nil))
	post(rpcBody("list", "tools/list", nil))
	if message := readLegacyMessage(); !strings.Contains(message, `"name":"wp6_echo"`) || !strings.Contains(message, `"id":"list"`) {
		t.Fatalf("legacy list response=%s", message)
	}
	post(rpcBody(3, "tools/call", map[string]any{"name": "wp6_echo", "arguments": map[string]any{"marker": "LEGACY"}}))
	if message := readLegacyMessage(); !strings.Contains(message, "WP6_ECHO:LEGACY") {
		t.Fatalf("legacy call response=%s", message)
	}

	cancel()
	_ = response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for server.SessionCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.SessionCount() != 0 {
		t.Fatalf("legacy disconnect leaked %d session(s)", server.SessionCount())
	}
}

type toolProviderFuncs struct {
	list func(context.Context) ([]Tool, error)
	call func(context.Context, string, map[string]any) (CallResult, error)
}

func (p toolProviderFuncs) ListTools(ctx context.Context) ([]Tool, error) {
	return p.list(ctx)
}

func (p toolProviderFuncs) CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error) {
	return p.call(ctx, name, arguments)
}

func initializedStreamableSession(t *testing.T, server *Server, owner string) (http.Handler, string) {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.ServeStreamableHTTP(w, r, owner)
	})
	init := streamableRequest(t, handler, owner, "", "", rpcBody(0, "initialize", map[string]any{
		"protocolVersion": LatestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}))
	if init.Code != http.StatusOK {
		t.Fatalf("initialize=%d %s", init.Code, init.Body.String())
	}
	sessionID := init.Header().Get(SessionHeader)
	note := streamableRequest(t, handler, owner, sessionID, LatestProtocolVersion, rpcBody(nil, "notifications/initialized", map[string]any{}))
	if note.Code != http.StatusAccepted {
		t.Fatalf("initialized=%d %s", note.Code, note.Body.String())
	}
	return handler, sessionID
}

func TestWP6ToolResultIsCompleteAndErrorsAreSanitized(t *testing.T) {
	large := "START" + strings.Repeat("中😀", 5000) + "MIDDLE" + strings.Repeat("z", 12000) + "END"
	provider := NewStaticToolProvider([]Tool{{Name: "large", InputSchema: map[string]any{"type": "object"}}, {Name: "fails", InputSchema: map[string]any{"type": "object"}}}, func(_ context.Context, name string, _ map[string]any) (CallResult, error) {
		if name == "fails" {
			return CallResult{}, errors.New("PRIVATE-PROVIDER-ERROR-SENTINEL")
		}
		return CallResult{Content: []map[string]any{{"type": "text", "text": large}}}, nil
	})
	server := NewServer(ServerOptions{Provider: provider})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)

	complete := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(1, "tools/call", map[string]any{"name": "large", "arguments": map[string]any{}}))
	for _, marker := range []string{"START", "MIDDLE", "END", "中😀"} {
		if !strings.Contains(complete.Body.String(), marker) {
			t.Fatalf("complete tool result lost %q; bytes=%d", marker, complete.Body.Len())
		}
	}
	if complete.Body.Len() < len(large) {
		t.Fatalf("complete tool result unexpectedly compacted: response=%d result=%d", complete.Body.Len(), len(large))
	}

	failed := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(2, "tools/call", map[string]any{"name": "fails", "arguments": map[string]any{}}))
	if !strings.Contains(failed.Body.String(), `"isError":true`) || strings.Contains(failed.Body.String(), "PRIVATE-PROVIDER-ERROR-SENTINEL") {
		t.Fatalf("provider error was not safely mapped: %s", failed.Body.String())
	}
	malformed := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(3, "tools/call", map[string]any{"name": "large", "arguments": "not-an-object"}))
	if !strings.Contains(malformed.Body.String(), `"code":-32602`) {
		t.Fatalf("malformed args=%s", malformed.Body.String())
	}
	unknownMethod := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(4, "resources/list", nil))
	if !strings.Contains(unknownMethod.Body.String(), `"code":-32601`) {
		t.Fatalf("unknown method=%s", unknownMethod.Body.String())
	}
}

func TestWP6CancellationPendingBoundAndTTL(t *testing.T) {
	started := make(chan struct{}, 2)
	provider := NewStaticToolProvider([]Tool{{Name: "block", InputSchema: map[string]any{"type": "object"}}}, func(ctx context.Context, _ string, _ map[string]any) (CallResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return CallResult{}, ctx.Err()
	})
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	server := NewServer(ServerOptions{Provider: provider, MaxPendingPerSession: 1, SessionTTL: time.Minute, CallTimeout: time.Minute, Now: func() time.Time { return now }})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("blocking", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	}()
	<-started
	bound := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("second", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	if !strings.Contains(bound.Body.String(), `"code":-32000`) {
		t.Fatalf("pending bound=%s", bound.Body.String())
	}
	cancelled := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"block\u0069ng","reason":"test"}}`))
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel notification=%d %s", cancelled.Code, cancelled.Body.String())
	}
	first := <-firstDone
	if first.Code != http.StatusAccepted || first.Body.Len() != 0 || server.PendingCount() != 0 {
		t.Fatalf("cancel result=%s pending=%d", first.Body.String(), server.PendingCount())
	}

	now = now.Add(time.Minute)
	if server.SessionCount() != 0 {
		t.Fatalf("expired session retained: %d", server.SessionCount())
	}
	expired := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(5, "tools/list", nil))
	if expired.Code != http.StatusNotFound {
		t.Fatalf("expired session status=%d body=%s", expired.Code, expired.Body.String())
	}
}

func TestWP6UncooperativeCallTimeoutReturnsAndBoundsProviderWorkers(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var calls atomic.Int32
	var running atomic.Int32
	provider := toolProviderFuncs{
		list: func(context.Context) ([]Tool, error) {
			return []Tool{{Name: "block", InputSchema: map[string]any{"type": "object"}}}, nil
		},
		call: func(context.Context, string, map[string]any) (CallResult, error) {
			call := calls.Add(1)
			running.Add(1)
			defer running.Add(-1)
			if call == 1 {
				<-release
				return CallResult{Content: []map[string]any{{"type": "text", "text": "LATE"}}}, nil
			}
			return CallResult{Content: []map[string]any{{"type": "text", "text": "FRESH"}}}, nil
		},
	}
	server := NewServer(ServerOptions{Provider: provider, MaxPendingPerSession: 1, CallTimeout: 20 * time.Millisecond})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("blocked", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	}()
	select {
	case response := <-done:
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "tool call timed out") {
			t.Fatalf("timeout response=%d %s", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("uncooperative provider kept the MCP handler blocked past its timeout")
	}
	if server.PendingCount() != 0 || calls.Load() != 1 || running.Load() != 1 {
		t.Fatalf("timeout leaked pending state or provider workers: pending=%d calls=%d running=%d", server.PendingCount(), calls.Load(), running.Load())
	}

	second := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("second", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	if second.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("provider worker bound failed: status=%d calls=%d body=%s", second.Code, calls.Load(), second.Body.String())
	}
	close(release)
	released = true
	deadline := time.Now().Add(time.Second)
	for running.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running.Load() != 0 {
		t.Fatal("released provider worker did not exit")
	}
	third := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("third", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	if third.Code != http.StatusOK || !strings.Contains(third.Body.String(), "FRESH") || strings.Contains(third.Body.String(), "LATE") {
		t.Fatalf("late provider result crossed request boundary: status=%d body=%s", third.Code, third.Body.String())
	}
}

func TestWP6UncooperativeListTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		var running atomic.Int32
		server := NewServer(ServerOptions{Provider: toolProviderFuncs{
			list: func(context.Context) ([]Tool, error) {
				running.Add(1)
				defer running.Add(-1)
				<-release
				return []Tool{}, nil
			},
			call: func(context.Context, string, map[string]any) (CallResult, error) { return CallResult{}, nil },
		}, MaxPendingPerSession: 1, CallTimeout: 20 * time.Millisecond})
		handler, sessionID := initializedStreamableSession(t, server, testOwner)
		response := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("list", "tools/list", map[string]any{}))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "tool registry timed out") {
			t.Fatalf("list timeout=%d %s", response.Code, response.Body.String())
		}
		if server.PendingCount() != 0 || running.Load() != 1 {
			t.Fatalf("list timeout state: pending=%d running=%d", server.PendingCount(), running.Load())
		}
	})

	t.Run("notification cancellation", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		started := make(chan struct{}, 1)
		var running atomic.Int32
		server := NewServer(ServerOptions{Provider: toolProviderFuncs{
			list: func(context.Context) ([]Tool, error) {
				running.Add(1)
				defer running.Add(-1)
				started <- struct{}{}
				<-release
				return []Tool{}, nil
			},
			call: func(context.Context, string, map[string]any) (CallResult, error) { return CallResult{}, nil },
		}, MaxPendingPerSession: 1, CallTimeout: time.Minute})
		handler, sessionID := initializedStreamableSession(t, server, testOwner)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("list", "tools/list", map[string]any{}))
		}()
		<-started
		cancelled := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(nil, "notifications/cancelled", map[string]any{"requestId": "list"}))
		if cancelled.Code != http.StatusAccepted {
			t.Fatalf("cancel notification=%d %s", cancelled.Code, cancelled.Body.String())
		}
		select {
		case response := <-done:
			if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
				t.Fatalf("cancelled list response=%d %q", response.Code, response.Body.String())
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatal("cancelled list handler did not return")
		}
		if server.PendingCount() != 0 || running.Load() != 1 {
			t.Fatalf("cancelled list state: pending=%d running=%d", server.PendingCount(), running.Load())
		}
	})
}

func TestWP6UncooperativeProviderDisconnectAndClose(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	var running atomic.Int32
	server := NewServer(ServerOptions{Provider: toolProviderFuncs{
		list: func(context.Context) ([]Tool, error) {
			return []Tool{{Name: "block", InputSchema: map[string]any{"type": "object"}}}, nil
		},
		call: func(context.Context, string, map[string]any) (CallResult, error) {
			calls.Add(1)
			running.Add(1)
			defer running.Add(-1)
			started <- struct{}{}
			<-release
			return CallResult{}, nil
		},
	}, MaxPendingPerSession: 1, CallTimeout: time.Minute})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("first", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	}()
	<-started

	deleteRequest := httptest.NewRequest(http.MethodDelete, "http://sidecar.test/v1/mcp", nil)
	deleteRequest.Header.Set(SessionHeader, sessionID)
	deleteRequest.Header.Set(ProtocolHeader, LatestProtocolVersion)
	deleted := httptest.NewRecorder()
	server.ServeStreamableHTTP(deleted, deleteRequest, testOwner)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("disconnect=%d %s", deleted.Code, deleted.Body.String())
	}
	select {
	case response := <-firstDone:
		if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
			t.Fatalf("disconnected handler=%d %q", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("disconnect did not release handler")
	}
	if server.SessionCount() != 0 || server.PendingCount() != 0 || running.Load() != 1 {
		t.Fatalf("disconnect state: sessions=%d pending=%d running=%d", server.SessionCount(), server.PendingCount(), running.Load())
	}

	_, secondSession := initializedStreamableSession(t, server, testOwner)
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondDone <- streamableRequest(t, handler, testOwner, secondSession, LatestProtocolVersion, rpcBody("second", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}}))
	}()
	deadline := time.Now().Add(250 * time.Millisecond)
	for server.PendingCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.PendingCount() != 1 || calls.Load() != 1 {
		t.Fatalf("second request did not wait inside global provider bound: pending=%d calls=%d", server.PendingCount(), calls.Load())
	}
	server.Close()
	server.Close()
	select {
	case response := <-secondDone:
		if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
			t.Fatalf("closed handler=%d %q", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("server close did not release waiting handler")
	}
	initialize := streamableRequest(t, handler, testOwner, "", "", rpcBody("closed", "initialize", map[string]any{
		"protocolVersion": LatestProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}))
	if initialize.Code != http.StatusServiceUnavailable || server.SessionCount() != 0 || server.PendingCount() != 0 {
		t.Fatalf("closed server accepted work: status=%d sessions=%d pending=%d body=%s", initialize.Code, server.SessionCount(), server.PendingCount(), initialize.Body.String())
	}
	close(release)
	released = true
	deadline = time.Now().Add(time.Second)
	for running.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running.Load() != 0 {
		t.Fatal("released provider worker did not exit after server Close")
	}
}

func TestWP6UncooperativeProviderRequestContextCancellation(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	started := make(chan struct{}, 1)
	var running atomic.Int32
	server := NewServer(ServerOptions{Provider: toolProviderFuncs{
		list: func(context.Context) ([]Tool, error) {
			return []Tool{{Name: "block", InputSchema: map[string]any{"type": "object"}}}, nil
		},
		call: func(context.Context, string, map[string]any) (CallResult, error) {
			running.Add(1)
			defer running.Add(-1)
			started <- struct{}{}
			<-release
			return CallResult{}, nil
		},
	}, MaxPendingPerSession: 1, CallTimeout: time.Minute})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)
	request := httptest.NewRequest(http.MethodPost, "http://sidecar.test/v1/mcp", bytes.NewReader(rpcBody("cancelled", "tools/call", map[string]any{"name": "block", "arguments": map[string]any{}})))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set(SessionHeader, sessionID)
	request.Header.Set(ProtocolHeader, LatestProtocolVersion)
	request = request.WithContext(context.WithValue(request.Context(), testOwnerContextKey{}, testOwner))
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
		if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
			t.Fatalf("cancelled request response=%d %q", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("request cancellation did not release handler")
	}
	if server.PendingCount() != 0 || running.Load() != 1 {
		t.Fatalf("request cancellation state: pending=%d running=%d", server.PendingCount(), running.Load())
	}
	close(release)
	released = true
	deadline := time.Now().Add(time.Second)
	for running.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if running.Load() != 0 {
		t.Fatal("released request-cancelled provider worker did not exit")
	}
}

func TestWP6StreamableGETIsRejectedWithoutLeakingStreams(t *testing.T) {
	server := NewServer(ServerOptions{Provider: testProvider(t)})
	_, sessionID := initializedStreamableSession(t, server, testOwner)
	for range 3 {
		request := httptest.NewRequest(http.MethodGet, "http://sidecar.test/v1/mcp", nil)
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set(SessionHeader, sessionID)
		request.Header.Set(ProtocolHeader, LatestProtocolVersion)
		response := httptest.NewRecorder()
		server.ServeStreamableHTTP(response, request, testOwner)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "HEAD, POST, DELETE" {
			t.Fatalf("GET status=%d allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
		}
	}
	if server.SessionCount() != 1 || server.PendingCount() != 0 {
		t.Fatalf("GET changed MCP state: sessions=%d pending=%d", server.SessionCount(), server.PendingCount())
	}
}

func TestWP6ToolSchemasArgumentsAndResultsAreValidated(t *testing.T) {
	var calls atomic.Int32
	tools := []Tool{{
		Name: "validate",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"value": map[string]any{"type": "string"}},
			"required":             []any{"value"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required":   []any{"value"},
		},
	}, {
		Name:        "empty",
		InputSchema: map[string]any{"type": "object"},
	}}
	provider := NewStaticToolProvider(tools, func(_ context.Context, name string, arguments map[string]any) (CallResult, error) {
		calls.Add(1)
		if name == "empty" {
			return CallResult{}, nil
		}
		value, _ := arguments["value"].(string)
		if value == "bad-output" {
			return CallResult{Content: []map[string]any{{"type": "text", "text": "bad"}}, StructuredData: map[string]any{"value": false}}, nil
		}
		if value == "bad-content" {
			return CallResult{Content: []map[string]any{{"type": "text", "text": 1}}, StructuredData: map[string]any{"value": value}}, nil
		}
		return CallResult{Content: []map[string]any{{"type": "text", "text": value}}, StructuredData: map[string]any{"value": value}}, nil
	})
	server := NewServer(ServerOptions{Provider: provider})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)

	for _, arguments := range []any{map[string]any{}, map[string]any{"value": 1}, map[string]any{"value": "ok", "extra": true}} {
		response := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(1, "tools/call", map[string]any{"name": "validate", "arguments": arguments}))
		if !strings.Contains(response.Body.String(), `"code":-32602`) {
			t.Fatalf("invalid arguments %#v accepted: %s", arguments, response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("provider called for schema-invalid arguments: %d", calls.Load())
	}

	badOutput := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(2, "tools/call", map[string]any{"name": "validate", "arguments": map[string]any{"value": "bad-output"}}))
	if !strings.Contains(badOutput.Body.String(), `"code":-32603`) || strings.Contains(badOutput.Body.String(), "bad-output") {
		t.Fatalf("invalid structured output not sanitized: %s", badOutput.Body.String())
	}
	badContent := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(3, "tools/call", map[string]any{"name": "validate", "arguments": map[string]any{"value": "bad-content"}}))
	if !strings.Contains(badContent.Body.String(), `"code":-32603`) || strings.Contains(badContent.Body.String(), "bad-content") {
		t.Fatalf("invalid content block not sanitized: %s", badContent.Body.String())
	}
	empty := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody(3, "tools/call", map[string]any{"name": "empty", "arguments": map[string]any{}}))
	if !strings.Contains(empty.Body.String(), `"content":[]`) {
		t.Fatalf("empty content was not encoded as an array: %s", empty.Body.String())
	}

	invalidRegistry := NewServer(ServerOptions{Provider: NewStaticToolProvider([]Tool{{
		Name:        "invalid",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "not-a-json-schema-type"}}},
	}}, func(context.Context, string, map[string]any) (CallResult, error) { return CallResult{}, nil })})
	invalidHandler, invalidSession := initializedStreamableSession(t, invalidRegistry, testOwner)
	listed := streamableRequest(t, invalidHandler, testOwner, invalidSession, LatestProtocolVersion, rpcBody(4, "tools/list", map[string]any{}))
	if !strings.Contains(listed.Body.String(), `"code":-32603`) {
		t.Fatalf("invalid registry schema accepted: %s", listed.Body.String())
	}
	if _, err := normalizeTools([]Tool{{
		Name:        "external_ref",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"$ref": "https://example.invalid/private-schema.json"}}},
	}}); err == nil {
		t.Fatal("external JSON Schema reference was accepted")
	}
}

func TestWP6ToolArgumentsPreserveLargeJSONIntegers(t *testing.T) {
	provider := NewStaticToolProvider([]Tool{{
		Name: "exact_integer",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"value": map[string]any{"type": "integer"}},
			"required":   []any{"value"},
		},
	}}, func(_ context.Context, _ string, arguments map[string]any) (CallResult, error) {
		value, ok := arguments["value"].(json.Number)
		if !ok {
			return CallResult{}, fmt.Errorf("large integer decoded as %T", arguments["value"])
		}
		return CallResult{Content: []map[string]any{{"type": "text", "text": value.String()}}}, nil
	})
	server := NewServer(ServerOptions{Provider: provider})
	handler, sessionID := initializedStreamableSession(t, server, testOwner)
	body := []byte(`{"jsonrpc":"2.0","id":"large","method":"tools/call","params":{"name":"exact_integer","arguments":{"value":9007199254740993}}}`)
	response := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, body)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"text":"9007199254740993"`) || strings.Contains(response.Body.String(), `"isError":true`) {
		t.Fatalf("large integer did not round trip exactly: %s", response.Body.String())
	}
}

func TestWP6ProtocolNegotiationAndConcurrentSessionCreation(t *testing.T) {
	server := NewServer(ServerOptions{Provider: testProvider(t), MaxSessions: 8})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.ServeStreamableHTTP(w, r, testOwner) })
	for _, test := range []struct {
		requested any
		want      string
		status    int
	}{
		{requested: "2024-11-05", want: "2024-11-05", status: http.StatusOK},
		{requested: "2099-01-01", want: LatestProtocolVersion, status: http.StatusOK},
		{requested: 20251125, status: http.StatusBadRequest},
	} {
		response := streamableRequest(t, handler, testOwner, "", "", rpcBody(1, "initialize", map[string]any{"protocolVersion": test.requested, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}))
		if response.Code != test.status || (test.want != "" && !strings.Contains(response.Body.String(), test.want)) {
			t.Fatalf("protocol %v status=%d body=%s", test.requested, response.Code, response.Body.String())
		}
	}
	missingID := streamableRequest(t, handler, testOwner, "", "", rpcBody(nil, "initialize", map[string]any{"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}))
	if missingID.Code != http.StatusBadRequest || !strings.Contains(missingID.Body.String(), `"code":-32600`) {
		t.Fatalf("initialize notification status=%d body=%s", missingID.Code, missingID.Body.String())
	}

	server = NewServer(ServerOptions{Provider: testProvider(t), MaxSessions: 4})
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.ServeStreamableHTTP(w, r, testOwner) })
	var wg sync.WaitGroup
	statuses := make(chan int, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- streamableRequest(t, handler, testOwner, "", "", rpcBody(1, "initialize", map[string]any{"protocolVersion": LatestProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})).Code
		}()
	}
	wg.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		if status == http.StatusOK {
			accepted++
		} else if status != http.StatusServiceUnavailable {
			t.Fatalf("unexpected concurrent initialize status=%d", status)
		}
	}
	if accepted != 4 || server.SessionCount() != 4 {
		t.Fatalf("session bound accepted=%d sessions=%d", accepted, server.SessionCount())
	}
}
