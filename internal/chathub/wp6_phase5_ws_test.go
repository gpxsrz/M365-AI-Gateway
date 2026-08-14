package chathub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wp6Phase5WSObservation struct {
	query   url.Values
	plugins []any
	err     error
}

type deadlineRecordingConn struct {
	net.Conn
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

func TestChatHubPreservesStreamAndShorterFinalEvidence(t *testing.T) {
	const streamed = `{"calls":[{"name":"execute_code","arguments":{"code":"print('BEGIN')\nprint('LOAD_BEARING_MIDDLE')\nprint('END')"}}],"answer":""}`
	const final = `{"calls":[`
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
		update, _ := json.Marshal(map[string]any{
			"type":   1,
			"target": "update",
			"arguments": []any{map[string]any{
				"messages": []any{map[string]any{"author": "bot", "text": streamed}},
			}},
		})
		completion, _ := json.Marshal(map[string]any{
			"type": 2,
			"item": map[string]any{"result": map[string]any{"message": final, "value": ""}},
		})
		for _, frame := range [][]byte{update, completion, []byte(`{"type":99,"future":"kept"}`), []byte(`{"type":3}`)} {
			if err = conn.WriteMessage(websocket.TextMessage, append(frame, []byte(rs)...)); err != nil {
				return
			}
		}
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
	result, err := client.Chat(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "route", ConversationID: "conversation", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != streamed || result.StreamedText != streamed || result.FinalText != final {
		t.Fatalf("text evidence mismatch: text=%q stream=%q final=%q", result.Text, result.StreamedText, result.FinalText)
	}
	if result.TextRelation != "final_prefix_of_stream" || result.TextSource != "stream" {
		t.Fatalf("relation=%q source=%q", result.TextRelation, result.TextSource)
	}
	if len(result.Events) != 4 {
		t.Fatalf("raw frame count=%d, want 4", len(result.Events))
	}
	for i, typ := range []string{`"type":1`, `"type":2`, `"type":99`, `"type":3`} {
		if !strings.Contains(string(result.Events[i]), typ) {
			t.Fatalf("raw frame %d=%s, want %s", i, result.Events[i], typ)
		}
	}
	if len(result.UnknownEvents) != 1 || result.UnknownEvents[0].Type != 99 {
		t.Fatalf("unknown events=%#v, want preserved type=99 frame", result.UnknownEvents)
	}
}

func (conn *deadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	conn.readDeadlines = append(conn.readDeadlines, deadline)
	return conn.Conn.SetReadDeadline(deadline)
}

func (conn *deadlineRecordingConn) SetWriteDeadline(deadline time.Time) error {
	conn.writeDeadlines = append(conn.writeDeadlines, deadline)
	return conn.Conn.SetWriteDeadline(deadline)
}

func TestWP6PrivateWebSocketAndNativeBingShareEveryConnection(t *testing.T) {
	observed := make(chan wp6Phase5WSObservation, 2)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			observed <- wp6Phase5WSObservation{err: err}
			return
		}
		defer conn.Close()
		if _, _, err = conn.ReadMessage(); err != nil {
			observed <- wp6Phase5WSObservation{err: err}
			return
		}
		if err = conn.WriteMessage(websocket.TextMessage, []byte(`{}`+rs)); err != nil {
			observed <- wp6Phase5WSObservation{err: err}
			return
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			observed <- wp6Phase5WSObservation{err: err}
			return
		}
		var frame map[string]any
		parts := strings.Split(string(payload), rs)
		if err = json.Unmarshal([]byte(parts[0]), &frame); err != nil {
			observed <- wp6Phase5WSObservation{err: err}
			return
		}
		arguments := frame["arguments"].([]any)[0].(map[string]any)
		plugins, _ := arguments["plugins"].([]any)
		observed <- wp6Phase5WSObservation{query: r.URL.Query(), plugins: plugins}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":3}`+rs))
	}))
	defer server.Close()

	address := strings.TrimPrefix(server.URL, "https://")
	client := NewClient()
	var tracedPrivateModes []bool
	client.Trace = func(meta map[string]any) {
		if meta["stage"] == "chathub_payload" {
			private, _ := meta["private_mode"].(bool)
			tracedPrivateModes = append(tracedPrivateModes, private)
		}
	}
	client.Dialer = &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test server certificate only.
	}
	account := Account{AccessToken: "test-token", OID: "test-oid", TID: "test-tid"}
	callerTool := Tool{Type: "function", Function: json.RawMessage(`{"name":"read_file","parameters":{"type":"object"}}`)}
	for _, request := range []Request{
		{Text: "Bing only", ConversationID: "conversation", SessionID: "session-1"},
		{Text: "Bing plus caller", ConversationID: "conversation", SessionID: "session-2", Tools: []Tool{callerTool}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.Chat(ctx, account, request)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		observation := <-observed
		if observation.err != nil {
			t.Fatal(observation.err)
		}
		if got := observation.query.Get("disableMemory"); got != "1" {
			t.Fatalf("new private WebSocket disableMemory=%q", got)
		}
		if len(observation.plugins) != len(request.Tools)+1 {
			t.Fatalf("plugins=%#v", observation.plugins)
		}
		bing := observation.plugins[0].(map[string]any)
		if bing["Id"] != "BingWebSearch" || bing["Source"] != "BuiltIn" {
			t.Fatalf("native Bing plugin=%#v", bing)
		}
	}
	if len(tracedPrivateModes) != 2 || !tracedPrivateModes[0] || !tracedPrivateModes[1] {
		t.Fatalf("private-mode traces=%v", tracedPrivateModes)
	}
}

func TestWP6ChatHubBlockingReadStopsAtCallerContextDeadline(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
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
		started <- struct{}{}
		<-release
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Chat(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "wait", ConversationID: "conversation", SessionID: "session"})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Chat returned before reaching the blocking read: %v", err)
	case <-time.After(750 * time.Millisecond):
		close(release)
		released = true
		t.Fatal("test server did not reach the blocking read before the setup bound")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocking read error=%v, want caller deadline", err)
		}
	case <-time.After(1500 * time.Millisecond):
		close(release)
		released = true
		<-done
		t.Fatal("caller deadline did not interrupt blocking ChatHub read")
	}
	close(release)
	released = true
}

func TestWP6ChatHubUsesCallerDeadlineBeyondFiveMinutes(t *testing.T) {
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
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":3}`+rs))
	}))
	defer server.Close()

	address := strings.TrimPrefix(server.URL, "https://")
	client := NewClient()
	var recorded *deadlineRecordingConn
	client.Dialer = &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			connection, err := dialer.DialContext(ctx, "tcp", address)
			if err != nil {
				return nil, err
			}
			recorded = &deadlineRecordingConn{Conn: connection}
			return recorded, nil
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test server certificate only.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	if _, err := client.Chat(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "complete", ConversationID: "conversation", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	if recorded == nil || len(recorded.readDeadlines) == 0 || len(recorded.writeDeadlines) == 0 {
		t.Fatalf("caller deadline was not applied to the WebSocket: %#v", recorded)
	}
	containsDeadline := func(deadlines []time.Time) bool {
		for _, deadline := range deadlines {
			if deadline.Equal(wantDeadline) {
				return true
			}
		}
		return false
	}
	if !containsDeadline(recorded.readDeadlines) {
		t.Fatalf("read deadlines=%v want caller deadline=%v", recorded.readDeadlines, wantDeadline)
	}
	if !containsDeadline(recorded.writeDeadlines) {
		t.Fatalf("write deadlines=%v want caller deadline=%v", recorded.writeDeadlines, wantDeadline)
	}
	if time.Until(wantDeadline) <= 5*time.Minute {
		t.Fatalf("test caller deadline does not exercise the removed five-minute ceiling: %v", wantDeadline)
	}
}
