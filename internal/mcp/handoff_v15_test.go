package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHandoffV15OfficialSDKModernNegotiationAndToolRoundTrip(t *testing.T) {
	provider := toolProviderFuncs{
		list: func(context.Context) ([]Tool, error) {
			return []Tool{{
				Name:        "echo",
				Title:       "Echo",
				Description: "Return the supplied value.",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"value": map[string]any{"type": "string"}},
					"required":             []any{"value"},
					"additionalProperties": false,
				},
				OutputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"echo": map[string]any{"type": "string"}},
					"required":             []any{"echo"},
					"additionalProperties": false,
				},
				Annotations: map[string]any{"readOnlyHint": true},
				Meta:        map[string]any{"vendor": "preserved"},
			}}, nil
		},
		call: func(_ context.Context, name string, arguments map[string]any) (CallResult, error) {
			if name != "echo" {
				t.Fatalf("tool=%q", name)
			}
			value, _ := arguments["value"].(string)
			return CallResult{
				Content:        []map[string]any{{"type": "text", "text": "echo=" + value}},
				StructuredData: map[string]any{"echo": value},
				Meta:           map[string]any{"trace": "preserved"},
			}, nil
		},
	}
	server := NewServer(ServerOptions{Provider: provider})
	defer server.Close()

	var mu sync.Mutex
	var protocolHeaders []string
	var sessionHeaders []string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protocolHeaders = append(protocolHeaders, r.Header.Get(ProtocolHeader))
		sessionHeaders = append(sessionHeaders, r.Header.Get(SessionHeader))
		mu.Unlock()
		server.ServeStreamableHTTP(w, r, testOwner)
	}))
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "handoff-v15-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "echo" || listed.Tools[0].OutputSchema == nil {
		t.Fatalf("tools=%#v", listed.Tools)
	}
	if listed.Tools[0].Meta["vendor"] != "preserved" || listed.Tools[0].Annotations == nil || !listed.Tools[0].Annotations.ReadOnlyHint {
		t.Fatalf("tool metadata was not preserved: %#v", listed.Tools[0])
	}

	called, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "echo", Arguments: map[string]any{"value": "modern"}})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := called.StructuredContent.(map[string]any)
	if !ok || structured["echo"] != "modern" || called.Meta["trace"] != "preserved" || called.IsError {
		t.Fatalf("call result=%#v", called)
	}

	mu.Lock()
	defer mu.Unlock()
	seenModern := false
	for i, version := range protocolHeaders {
		if version != CurrentProtocolVersion {
			continue
		}
		seenModern = true
		if sessionHeaders[i] != "" {
			t.Fatalf("2026-07-28 request unexpectedly used legacy session header: %q", sessionHeaders[i])
		}
	}
	if !seenModern {
		t.Fatalf("official client never negotiated %s: headers=%#v", CurrentProtocolVersion, protocolHeaders)
	}
	if server.SessionCount() != 0 {
		t.Fatalf("modern official path leaked into custom legacy session core: %d", server.SessionCount())
	}
}

func TestHandoffV15LegacyMCPRemainsCompatibleBesideOfficialModernPath(t *testing.T) {
	server := NewServer(ServerOptions{Provider: NewStaticToolProvider([]Tool{{
		Name:        "legacy_echo",
		InputSchema: map[string]any{"type": "object"},
	}}, func(context.Context, string, map[string]any) (CallResult, error) {
		return CallResult{Content: []map[string]any{{"type": "text", "text": "legacy-ok"}}}, nil
	})})
	defer server.Close()
	handler, sessionID := initializedStreamableSession(t, server, testOwner)
	if sessionID == "" {
		t.Fatal("legacy 2025-11-25 session ID missing")
	}
	response := streamableRequest(t, handler, testOwner, sessionID, LatestProtocolVersion, rpcBody("legacy-call", "tools/call", map[string]any{
		"name": "legacy_echo", "arguments": map[string]any{},
	}))
	if response.Code != http.StatusOK || !containsJSONText(response.Body.String(), "legacy-ok") {
		t.Fatalf("legacy response=%d %s", response.Code, response.Body.String())
	}
}

func containsJSONText(body, text string) bool {
	return len(body) > 0 && len(text) > 0 && stringContains(body, text)
}

func stringContains(body, text string) bool {
	for i := 0; i+len(text) <= len(body); i++ {
		if body[i:i+len(text)] == text {
			return true
		}
	}
	return false
}
