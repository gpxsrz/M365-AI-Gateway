package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"
)

const EnvProxy = "M365_OUTBOUND_PROXY"

type Clients struct {
	HTTP      *http.Client
	WebSocket *websocket.Dialer
}

var (
	clientsMu sync.RWMutex
	clients   = directClients()
)

func directClients() *Clients {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &Clients{HTTP: &http.Client{Transport: t}, WebSocket: &websocket.Dialer{HandshakeTimeout: 20 * time.Second, ReadBufferSize: 1024 * 1024, WriteBufferSize: 64 * 1024}}
}
func ConfigureFromEnv() error {
	return Configure(os.Getenv(EnvProxy))
}
func Configure(raw string) error {
	c, e := New(raw)
	if e != nil {
		return e
	}
	clientsMu.Lock()
	clients = c
	clientsMu.Unlock()
	return nil
}
func HTTPClient() *http.Client {
	clientsMu.RLock()
	c := clients.HTTP
	clientsMu.RUnlock()
	return c
}

// PinnedHTTPSClient returns a one-host client whose TLS identity remains the
// original hostname even when the request URL contains a previously resolved
// IP address. The configured outbound proxy path is preserved.
func PinnedHTTPSClient(serverName string) *http.Client {
	clientsMu.RLock()
	configured := clients.HTTP
	clientsMu.RUnlock()
	return &http.Client{Transport: pinnedHTTPTransport(configured.Transport, serverName), Timeout: configured.Timeout}
}

func pinnedHTTPTransport(roundTripper http.RoundTripper, serverName string) http.RoundTripper {
	transport, ok := roundTripper.(*http.Transport)
	if !ok {
		return roundTripper
	}
	clone := transport.Clone()
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.ServerName = serverName
	return clone
}
func WebSocketDialer() *websocket.Dialer {
	clientsMu.RLock()
	c := clients.WebSocket
	clientsMu.RUnlock()
	d := *c
	return &d
}
func ValidateProxyURL(raw string) error { _, e := New(raw); return e }
func New(raw string) (*Clients, error) {
	c := directClients()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return c, nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return nil, fmt.Errorf("outbound proxy must be a complete socks5://, http://, or https:// URL")
	}
	if u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return nil, fmt.Errorf("outbound proxy URL must not include a path, query, or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		c.HTTP.Transport.(*http.Transport).Proxy = http.ProxyURL(u)
		c.WebSocket.Proxy = http.ProxyURL(u)
	case "https":
		// Do not use Transport.Proxy here: Go's standard transport performs its
		// own proxy TLS handshake and bypasses our IP-certificate compatibility.
		transport := c.HTTP.Transport.(*http.Transport)
		transport.Proxy = nil
		transport.DialContext = httpsProxyDialer{proxyURL: u}.DialContext
		c.WebSocket.NetDialContext = httpsProxyDialer{proxyURL: u}.DialContext
	case "socks5":
		var a *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			a = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, e := proxy.SOCKS5("tcp", u.Host, a, proxy.Direct)
		if e != nil {
			return nil, fmt.Errorf("configure SOCKS5 proxy: %w", e)
		}
		contextDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("configure SOCKS5 proxy: context-aware dialer is unavailable")
		}
		c.HTTP.Transport.(*http.Transport).DialContext = contextDialer.DialContext
		c.WebSocket.NetDialContext = contextDialer.DialContext
	default:
		return nil, fmt.Errorf("outbound proxy scheme %q is unsupported; use socks5, http, or https", u.Scheme)
	}
	return c, nil
}

type httpsProxyDialer struct{ proxyURL *url.URL }

func (d httpsProxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("HTTPS proxy only supports tcp, got %q", network)
	}
	a := d.proxyURL.Host
	if d.proxyURL.Port() == "" {
		a = net.JoinHostPort(d.proxyURL.Hostname(), "443")
	}
	raw, e := (&net.Dialer{}).DialContext(ctx, network, a)
	if e != nil {
		return nil, e
	}
	// Proxy endpoints commonly present a certificate for their hostname while users
	// configure an IP address. This option affects only the TLS hop to the proxy;
	// target-site certificate verification remains enabled.
	insecureProxyTLS := os.Getenv("M365_PROXY_INSECURE_TLS") == "1" || os.Getenv("M365_PROXY_INSECURE_TLS") == "true" || net.ParseIP(d.proxyURL.Hostname()) != nil
	conn := tls.Client(raw, &tls.Config{ServerName: d.proxyURL.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecureProxyTLS}) // #nosec G402 -- explicitly scoped to configured proxy TLS

	if e = conn.HandshakeContext(ctx); e != nil {
		raw.Close()
		return nil, e
	}
	q := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if d.proxyURL.User != nil {
		pw, _ := d.proxyURL.User.Password()
		q.SetBasicAuth(d.proxyURL.User.Username(), pw)
	}
	if e = q.Write(conn); e != nil {
		conn.Close()
		return nil, e
	}
	rd := bufio.NewReader(conn)
	resp, e := http.ReadResponse(rd, q)
	if e != nil {
		conn.Close()
		return nil, e
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("HTTPS proxy CONNECT %s: %s", address, resp.Status)
	}
	return &bufferedConn{Conn: conn, reader: rd}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
