package chathub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandoffV15AllowedMessageTypesCoverStructuredCapabilities(t *testing.T) {
	payload := chatPayload("hello", "session", "conversation", "request", "magic", true, nil, nil, nil, 0, "")
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.Split(payload, rs)[0]), &frame); err != nil {
		t.Fatal(err)
	}
	arguments := frame["arguments"].([]any)[0].(map[string]any)
	rawTypes := arguments["allowedMessageTypes"].([]any)
	got := map[string]bool{}
	for _, raw := range rawTypes {
		got[raw.(string)] = true
	}
	for _, want := range []string{
		"Chat", "Suggestion", "Disengaged", "Progress", "EndOfRequest", "InternalLoaderMessage",
		"GeneratedCode", "GenerateContentQuery", "ReferencesListComplete", "RenderCardRequest",
		"SearchQuery", "InternalSearchQuery", "SemanticSerp", "AuthError",
	} {
		if !got[want] {
			t.Errorf("allowedMessageTypes missing %q: %#v", want, got)
		}
	}
}

func TestHandoffV15ToolChoiceNoneDoesNotExposeCallerToolsToChatHub(t *testing.T) {
	caller := Tool{Type: "function", Function: json.RawMessage(`{"name":"lookup","parameters":{"type":"object"}}`)}
	payload := chatPayload("hello", "session", "conversation", "request", "magic", true, nil, []Tool{caller}, "none", 1, "")
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.Split(payload, rs)[0]), &frame); err != nil {
		t.Fatal(err)
	}
	arguments := frame["arguments"].([]any)[0].(map[string]any)
	plugins := arguments["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("tool_choice=none exposed caller plugins: %#v", plugins)
	}
	plugin := plugins[0].(map[string]any)
	if plugin["Id"] != "BingWebSearch" || plugin["Source"] != "BuiltIn" {
		t.Fatalf("provider built-in changed under tool_choice=none: %#v", plugin)
	}
}

func TestHandoffV15SignalRCompletionStringErrorFails(t *testing.T) {
	err := runHandoffChatHubTerminalFixture(t, `{"type":3,"error":"HubException: deterministic failure"}`)
	if err == nil || !strings.Contains(err.Error(), "HubException: deterministic failure") {
		t.Fatalf("completion string error=%v", err)
	}
}

func TestHandoffV15SignalRClosePreservesRecoveryState(t *testing.T) {
	events := NormalizeEvents([]json.RawMessage{json.RawMessage(`{"type":7,"error":"server closing","allowReconnect":true}`)})
	if len(events) != 1 {
		t.Fatalf("events=%#v", events)
	}
	if events[0].Kind != "close" || events[0].ErrorText != "server closing" || events[0].AllowReconnect == nil || !*events[0].AllowReconnect {
		t.Fatalf("close event lost recovery state: %#v", events[0])
	}
	err := runHandoffChatHubTerminalFixture(t, `{"type":7,"error":"server closing","allowReconnect":true}`)
	var terminal *TerminalError
	if !errors.As(err, &terminal) || terminal.State.Kind != "close" || terminal.State.Error != "server closing" || terminal.State.AllowReconnect == nil || !*terminal.State.AllowReconnect {
		t.Fatalf("close terminal=%T %v", err, err)
	}
}

func TestHandoffV15StreamEventRawDoesNotReExposeProtectedSibling(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		if err = conn.WriteMessage(websocket.TextMessage, []byte(`{}`+rs)); err != nil {
			return
		}
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		update := `{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"Progress","contentType":"SearchResults","text":"safe progress"}],"artifact":{"codeResultImageUrl":"https://cdn.example.test/protected-output.png"}}]}` + rs
		if err = conn.WriteMessage(websocket.TextMessage, []byte(update)); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":3}`+rs))
	}))
	defer server.Close()

	address := strings.TrimPrefix(server.URL, "https://")
	client := NewClient()
	client.Dialer = &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test server certificate only.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []StreamEvent
	_, err := client.ChatWithEvents(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "fixture", ConversationID: "conversation", SessionID: "session"}, func(event StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "progress" || events[0].Text != "safe progress" {
		t.Fatalf("events=%#v", events)
	}
	if strings.Contains(string(events[0].Raw), "codeResultImageUrl") || strings.Contains(string(events[0].Raw), "protected-output.png") {
		t.Fatalf("protected parent sibling leaked through stream Raw: %s", events[0].Raw)
	}
}

func runHandoffChatHubTerminalFixture(t *testing.T, terminalFrame string) error {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		if err = conn.WriteMessage(websocket.TextMessage, []byte(`{}`+rs)); err != nil {
			return
		}
		if _, _, err = conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(terminalFrame+rs))
	}))
	defer server.Close()

	address := strings.TrimPrefix(server.URL, "https://")
	client := NewClient()
	client.Dialer = &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test server certificate only.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Chat(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "terminal fixture", ConversationID: "conversation", SessionID: "session"})
	return err
}
