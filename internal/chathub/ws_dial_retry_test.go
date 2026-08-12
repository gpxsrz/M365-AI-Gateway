package chathub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newOneFailureThenSuccessWebSocketServer(t *testing.T, firstStatus int, onFirst func()) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	var payloadWrites atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 && firstStatus != http.StatusSwitchingProtocols {
			if onFirst != nil {
				onFirst()
			}
			if firstStatus == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "37")
			}
			http.Error(w, "synthetic WebSocket upgrade failure", firstStatus)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("read SignalR handshake: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{}`+rs)); err != nil {
			t.Errorf("write SignalR handshake: %v", err)
			return
		}
		if _, payload, err := conn.ReadMessage(); err != nil {
			t.Errorf("read chat payload: %v", err)
			return
		} else if !strings.Contains(string(payload), "retry canary") {
			t.Errorf("unexpected chat payload: %s", payload)
			return
		}
		payloadWrites.Add(1)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":3}`+rs))
	}))
	return server, &attempts, &payloadWrites
}

func webSocketRetryTestClient(serverURL string, dialHook func(context.Context) (net.Conn, error)) *Client {
	address := strings.TrimPrefix(serverURL, "https://")
	client := NewClient()
	client.Dialer = &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if dialHook != nil {
				if conn, err := dialHook(ctx); conn != nil || err != nil {
					return conn, err
				}
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", address)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // Test server certificate only.
	}
	return client
}

func retryTestRequest() Request {
	return Request{Text: "retry canary", ConversationID: "conversation", SessionID: "session"}
}

func retryTestAccount() Account {
	return Account{AccessToken: "token", OID: "oid", TID: "tid"}
}

func TestChatRetriesTransientWebSocketUpgradeBeforeSendingPayload(t *testing.T) {
	server, attempts, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, http.StatusInternalServerError, nil)
	defer server.Close()
	client := webSocketRetryTestClient(server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat(ctx, retryTestAccount(), retryTestRequest())
	if err != nil {
		t.Fatalf("chat failed instead of retrying pre-send WebSocket upgrade: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("dial attempts=%d want=2", got)
	}
	if got := payloadWrites.Load(); got != 1 {
		t.Fatalf("chat payload writes=%d want=1", got)
	}
}

func TestChatRetriesOtherTransientWebSocketUpgradeStatuses(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, attempts, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, status, nil)
			defer server.Close()
			client := webSocketRetryTestClient(server.URL, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if _, err := client.Chat(ctx, retryTestAccount(), retryTestRequest()); err != nil {
				t.Fatalf("HTTP %d upgrade was not retried: %v", status, err)
			}
			if attempts.Load() != 2 || payloadWrites.Load() != 1 {
				t.Fatalf("HTTP %d attempts=%d payloads=%d want 2/1", status, attempts.Load(), payloadWrites.Load())
			}
		})
	}
}

func TestChatRetriesTransientNetworkDialFailureBeforeSendingPayload(t *testing.T) {
	server, _, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, http.StatusSwitchingProtocols, nil)
	defer server.Close()
	var dialCalls atomic.Int32
	client := webSocketRetryTestClient(server.URL, func(context.Context) (net.Conn, error) {
		if dialCalls.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNRESET}
		}
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Chat(ctx, retryTestAccount(), retryTestRequest()); err != nil {
		t.Fatalf("transient network dial failure was not retried: %v", err)
	}
	if dialCalls.Load() != 2 || payloadWrites.Load() != 1 {
		t.Fatalf("dial calls=%d payloads=%d want 2/1", dialCalls.Load(), payloadWrites.Load())
	}
}

func TestChatDoesNotRetryRateLimitOrPermanentWebSocketUpgradeFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, attempts, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, status, nil)
			defer server.Close()
			client := webSocketRetryTestClient(server.URL, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err := client.Chat(ctx, retryTestAccount(), retryTestRequest())
			if err == nil {
				t.Fatalf("HTTP %d upgrade unexpectedly retried and succeeded", status)
			}
			if attempts.Load() != 1 || payloadWrites.Load() != 0 {
				t.Fatalf("HTTP %d attempts=%d payloads=%d want 1/0", status, attempts.Load(), payloadWrites.Load())
			}
			if status == http.StatusTooManyRequests {
				var rateLimit *RateLimitError
				if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != "37" {
					t.Fatalf("429 mapping=%T %#v", err, rateLimit)
				}
			}
		})
	}
}

func TestChatDoesNotRetryPermanentTLSDialFailure(t *testing.T) {
	var dialCalls atomic.Int32
	client := NewClient()
	client.Dialer = &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, x509.HostnameError{Host: "example.invalid"}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Chat(ctx, retryTestAccount(), retryTestRequest()); err == nil {
		t.Fatal("permanent TLS/certificate dial failure unexpectedly succeeded")
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("permanent TLS/certificate dial attempts=%d want=1", got)
	}
}

func TestChatStopsDialRetryWhenCallerContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server, attempts, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, http.StatusInternalServerError, cancel)
	defer server.Close()
	client := webSocketRetryTestClient(server.URL, nil)

	_, err := client.Chat(ctx, retryTestAccount(), retryTestRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
	if attempts.Load() != 1 || payloadWrites.Load() != 0 {
		t.Fatalf("attempts=%d payloads=%d want 1/0", attempts.Load(), payloadWrites.Load())
	}
}

func TestChatStopsDialRetryWhenCallerDeadlineExpiresDuringBackoff(t *testing.T) {
	server, attempts, payloadWrites := newOneFailureThenSuccessWebSocketServer(t, http.StatusInternalServerError, nil)
	defer server.Close()
	client := webSocketRetryTestClient(server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Chat(ctx, retryTestAccount(), retryTestRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v want context.DeadlineExceeded", err)
	}
	if attempts.Load() != 1 || payloadWrites.Load() != 0 {
		t.Fatalf("attempts=%d payloads=%d want 1/0", attempts.Load(), payloadWrites.Load())
	}
}

func TestChatDoesNotReplayAfterWebSocketUpgradeSucceeds(t *testing.T) {
	var attempts atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage() // SignalR handshake from the client.
		// Close before sending the SignalR handshake response. The WebSocket was
		// established, so pre-send dial retry must not replay the request.
	}))
	defer server.Close()
	client := webSocketRetryTestClient(server.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat(ctx, retryTestAccount(), retryTestRequest())
	if err == nil || !strings.Contains(err.Error(), "handshake recv") {
		t.Fatalf("error=%v want handshake recv failure", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("post-upgrade failure dial attempts=%d want=1", got)
	}
}
