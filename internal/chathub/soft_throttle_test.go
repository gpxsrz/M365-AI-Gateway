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

func chatHubSoftThrottleFixture(t *testing.T, item map[string]any) (Result, error) {
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
		completion, _ := json.Marshal(map[string]any{
			"type": 2,
			"item": item,
		})
		for _, frame := range [][]byte{completion, []byte(`{"type":3}`)} {
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
	return client.Chat(ctx, Account{AccessToken: "token", OID: "oid", TID: "tid"}, Request{Text: "fixture", ConversationID: "conversation", SessionID: "session"})
}

func TestChatHubThrottlingMetadataWithoutKnownNoticeDoesNotRateLimit(t *testing.T) {
	const reply = "STRUCTURED_METADATA_ONLY"
	result, err := chatHubSoftThrottleFixture(t, map[string]any{
		"messages": []any{map[string]any{
			"author":        "bot",
			"contentOrigin": "BotConnection",
			"messageType":   "",
			"offense":       "None",
			"text":          reply,
		}},
		"throttling": map[string]any{"signal": "fixture"},
		"result":     map[string]any{"message": reply, "value": ""},
	})
	if err != nil {
		t.Fatalf("quota metadata was misclassified as throttle: %T %v", err, err)
	}
	if result.Throttling == nil || result.Text != reply {
		t.Fatalf("quota metadata/result was not preserved: %#v", result)
	}
}

func TestChatHubNormalMeteringThrottlingMetadataDoesNotRateLimit(t *testing.T) {
	const reply = "NORMAL_META_OK"
	result, err := chatHubSoftThrottleFixture(t, map[string]any{
		"throttling": map[string]any{
			"maxNumUserMessagesInConversation":            30,
			"numLongDocSummaryUserMessagesInConversation": 0,
			"numUserMessagesInConversation":               1,
			"metering":                                    map[string]any{"enabled": true},
		},
		"result": map[string]any{"message": reply, "value": ""},
	})
	if err != nil {
		t.Fatalf("normal metering metadata was misclassified as throttle: %T %v", err, err)
	}
	if result.Text != reply {
		t.Fatalf("text=%q want=%q", result.Text, reply)
	}
}

func TestChatHubKnownSoftThrottleTextFallbackReturnsRateLimitError(t *testing.T) {
	for _, throttleText := range []string{
		"我們暫時無法回應這麼大量的要求。請稍後再試一次。",
		"我们暂时无法响应这么多请求。请稍后重试。",
	} {
		t.Run(throttleText, func(t *testing.T) {
			result, err := chatHubSoftThrottleFixture(t, map[string]any{
				"messages": []any{map[string]any{
					"author":        "bot",
					"contentOrigin": "BotConnection",
					"messageType":   "",
					"offense":       "None",
					"text":          throttleText,
				}},
				"result": map[string]any{"message": throttleText, "value": ""},
			})
			var rateLimit *RateLimitError
			if !errors.As(err, &rateLimit) {
				t.Fatalf("error=%T %v, want RateLimitError; result=%#v", err, err, result)
			}
		})
	}
}
