package outbound

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestDirectConnectivityUsesNoProxyForHTTPOrWebSocket(t *testing.T) {
	clients, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := clients.HTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTP transport=%T", clients.HTTP.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("direct HTTP unexpectedly configured a proxy")
	}
	if clients.WebSocket.Proxy != nil || clients.WebSocket.NetDialContext != nil {
		t.Fatal("direct WebSocket unexpectedly configured a proxy dial path")
	}
}

func TestSingleHTTPProxyIsSharedByHTTPAndWebSocket(t *testing.T) {
	const raw = "http://proxy.example:8080"
	clients, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := clients.HTTP.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil || clients.WebSocket.Proxy == nil {
		t.Fatalf("single HTTP proxy was not configured for both HTTP and WebSocket: transport=%T wsProxyConfigured=%t", clients.HTTP.Transport, clients.WebSocket.Proxy != nil)
	}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "substrate.office.com"}}
	for name, resolver := range map[string]func(*http.Request) (*url.URL, error){"http": transport.Proxy, "websocket": clients.WebSocket.Proxy} {
		got, err := resolver(request)
		if err != nil || got.String() != raw {
			t.Fatalf("%s proxy=%v err=%v want=%s", name, got, err, raw)
		}
	}
}

func TestSingleSOCKS5AndHTTPSProxyConfigureBothTransports(t *testing.T) {
	for _, raw := range []string{"socks5://127.0.0.1:1080", "https://proxy.example:8443"} {
		t.Run(raw, func(t *testing.T) {
			clients, err := New(raw)
			if err != nil {
				t.Fatal(err)
			}
			transport, ok := clients.HTTP.Transport.(*http.Transport)
			if !ok || transport.DialContext == nil || clients.WebSocket.NetDialContext == nil {
				t.Fatalf("single proxy %s did not configure both HTTP and WebSocket dial paths", raw)
			}
		})
	}
}

func TestSOCKS5CancellationClosesUnderlyingHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted <- conn
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()

	clients, err := New("socks5://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport := clients.HTTP.Transport.(*http.Transport)
	ctx, cancel := context.WithCancel(context.Background())
	dialDone := make(chan error, 1)
	go func() {
		conn, err := transport.DialContext(ctx, "tcp", "example.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		dialDone <- err
	}()

	var proxyConn net.Conn
	select {
	case proxyConn = <-accepted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("SOCKS5 proxy never accepted the underlying connection")
	}
	cancel()
	select {
	case err := <-dialDone:
		if err == nil {
			t.Fatal("SOCKS5 dial unexpectedly succeeded after cancellation")
		}
	case <-time.After(time.Second):
		_ = proxyConn.Close()
		t.Fatal("SOCKS5 DialContext did not return after cancellation")
	}
	select {
	case <-closed:
	case <-time.After(300 * time.Millisecond):
		_ = proxyConn.Close()
		t.Fatal("SOCKS5 cancellation returned to caller but left the underlying proxy connection blocked")
	}
}

func TestConfigureFromEnvIgnoresRemovedProxyPoolEnvironment(t *testing.T) {
	t.Setenv("M365_PROXY_POOL", "://malformed-pool-value")
	t.Setenv(EnvProxy, "")
	if err := ConfigureFromEnv(); err != nil {
		t.Fatalf("removed M365_PROXY_POOL still affected runtime: %v", err)
	}
	transport, ok := HTTPClient().Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("removed proxy-pool environment changed direct connectivity")
	}
}
