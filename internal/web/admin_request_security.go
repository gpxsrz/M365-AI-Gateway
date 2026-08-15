package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	adminAllowedHostsEnv   = "M365_ADMIN_ALLOWED_HOSTS"
	adminTrustedProxiesEnv = "M365_ADMIN_TRUSTED_PROXIES"

	adminSessionIdleTimeout     = 30 * time.Minute
	adminSessionAbsoluteTimeout = 24 * time.Hour
)

type adminSession struct {
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type adminAllowedHost struct {
	host string
	port string
}

func (a adminAllowedHost) String() string {
	if strings.Contains(a.host, ":") {
		if a.port == "" {
			return "[" + a.host + "]"
		}
		return net.JoinHostPort(a.host, a.port)
	}
	if a.port != "" {
		return net.JoinHostPort(a.host, a.port)
	}
	return a.host
}

type adminSecurityPolicy struct {
	allowedHosts   []adminAllowedHost
	trustedProxies []*net.IPNet
}

type adminRequestInfo struct {
	scheme       string
	authority    adminAllowedHost
	clientIP     net.IP
	directPeer   net.IP
	secure       bool
	localConsole bool
}

type adminRequestInfoContextKey struct{}

func loadAdminSecurityPolicy() (adminSecurityPolicy, error) {
	var policy adminSecurityPolicy
	for _, raw := range splitAdminEnvList(os.Getenv(adminAllowedHostsEnv)) {
		host, err := parseAdminAuthority(raw)
		if err != nil {
			return adminSecurityPolicy{}, fmt.Errorf("%s: %w", adminAllowedHostsEnv, err)
		}
		policy.allowedHosts = append(policy.allowedHosts, host)
	}
	for _, raw := range splitAdminEnvList(os.Getenv(adminTrustedProxiesEnv)) {
		network, err := parseTrustedLoopbackProxy(raw)
		if err != nil {
			return adminSecurityPolicy{}, fmt.Errorf("%s: %w", adminTrustedProxiesEnv, err)
		}
		policy.trustedProxies = append(policy.trustedProxies, network)
	}
	return policy, nil
}

func splitAdminEnvList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func parseAdminAuthority(raw string) (adminAllowedHost, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n/\\?#@*%") || strings.Contains(raw, "://") {
		return adminAllowedHost{}, fmt.Errorf("管理 Host %q 無效", raw)
	}

	var host, port string
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		host = ip.String()
	} else if h, p, err := net.SplitHostPort(raw); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	} else if strings.Count(raw, ":") > 1 {
		return adminAllowedHost{}, fmt.Errorf("管理 Host %q 無效", raw)
	} else {
		host = raw
	}

	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	if host == "" {
		return adminAllowedHost{}, fmt.Errorf("管理 Host %q 無效", raw)
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return adminAllowedHost{}, fmt.Errorf("管理 Host %q 的連接埠無效", raw)
		}
		port = strconv.Itoa(n)
	}
	return adminAllowedHost{host: host, port: port}, nil
}

func parseTrustedLoopbackProxy(raw string) (*net.IPNet, error) {
	if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("受信任 Proxy %q 不是 loopback 位址", raw)
		}
		bits := 128
		if ip.To4() != nil {
			ip = ip.To4()
			bits = 32
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
	}
	ip, network, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("受信任 Proxy %q 無效", raw)
	}
	network.IP = ip.Mask(network.Mask)
	ones, bits := network.Mask.Size()
	if bits == 32 {
		start := network.IP.To4()
		end := append(net.IP(nil), start...)
		for i := range end {
			end[i] |= ^network.Mask[i]
		}
		if !start.IsLoopback() || !end.IsLoopback() {
			return nil, fmt.Errorf("受信任 Proxy 範圍 %q 未完全位於 loopback", raw)
		}
		return network, nil
	}
	if bits != 128 || ones != 128 || !network.IP.IsLoopback() {
		return nil, fmt.Errorf("受信任 Proxy 範圍 %q 必須是單一 loopback IPv6 位址", raw)
	}
	return network, nil
}

func (p adminSecurityPolicy) inspect(r *http.Request) (adminRequestInfo, error) {
	peer := parseRemoteIP(r.RemoteAddr)
	if peer == nil {
		return adminRequestInfo{}, fmt.Errorf("請求來源位址無效")
	}

	info := adminRequestInfo{directPeer: peer, clientIP: peer}
	if p.isTrustedProxy(peer) {
		forwardedProto, err := oneForwardedHeader(r.Header, "X-Forwarded-Proto")
		if err != nil || !strings.EqualFold(forwardedProto, "https") {
			return adminRequestInfo{}, fmt.Errorf("受信任 Proxy 必須提供單一 HTTPS forwarded protocol")
		}
		forwardedHost, err := oneForwardedHeader(r.Header, "X-Forwarded-Host")
		if err != nil {
			return adminRequestInfo{}, fmt.Errorf("受信任 Proxy 必須提供單一 forwarded Host")
		}
		authority, err := parseAdminAuthority(forwardedHost)
		if err != nil {
			return adminRequestInfo{}, err
		}
		forwarded := rightMostForwardedIP(strings.Join(r.Header.Values("X-Forwarded-For"), ","))
		if forwarded == nil {
			return adminRequestInfo{}, fmt.Errorf("受信任 Proxy 必須提供有效的 forwarded client 位址")
		}
		info.scheme = "https"
		info.authority = authority
		info.secure = true
		info.clientIP = forwarded
	} else {
		authority, err := parseAdminAuthority(r.Host)
		if err != nil {
			return adminRequestInfo{}, err
		}
		info.authority = authority
		if r.TLS != nil {
			info.scheme = "https"
			info.secure = true
		} else {
			info.scheme = "http"
		}
	}

	info.localConsole = info.directPeer.IsLoopback() && info.clientIP.IsLoopback() && info.authority.isLoopback()
	if info.localConsole {
		return info, nil
	}
	if !info.secure {
		return adminRequestInfo{}, fmt.Errorf("非 loopback 管理介面必須使用 HTTPS")
	}
	if !p.hostAllowed(info.authority) {
		return adminRequestInfo{}, fmt.Errorf("管理 Host 不在允許清單中")
	}
	return info, nil
}

func (p adminSecurityPolicy) isTrustedProxy(ip net.IP) bool {
	for _, network := range p.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (p adminSecurityPolicy) hostAllowed(candidate adminAllowedHost) bool {
	for _, allowed := range p.allowedHosts {
		if allowed.host != candidate.host {
			continue
		}
		if allowed.port == "" || allowed.port == candidate.port || (allowed.port == "443" && candidate.port == "") {
			return true
		}
	}
	return false
}

func (a adminAllowedHost) isLoopback() bool {
	if a.host == "localhost" {
		return true
	}
	ip := net.ParseIP(a.host)
	return ip != nil && ip.IsLoopback()
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddr), "[]")
	}
	return net.ParseIP(host)
}

func rightMostForwardedIP(raw string) net.IP {
	parts := strings.Split(raw, ",")
	return net.ParseIP(strings.TrimSpace(parts[len(parts)-1]))
}

func oneForwardedHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", fmt.Errorf("%s 必須恰好出現一次", name)
	}
	value := strings.TrimSpace(values[0])
	if value == "" || strings.Contains(value, ",") {
		return "", fmt.Errorf("%s 必須只包含一個值", name)
	}
	return value, nil
}

func (s *Server) adminRequestSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean(r.URL.Path)
		if cleanPath == hindsightWebhookPath || cleanPath == "/v1" || strings.HasPrefix(cleanPath, "/v1/") || hermesCompatibilityRequest(cleanPath) || memoryCompatibilityRequest(cleanPath) {
			next.ServeHTTP(w, r)
			return
		}

		info, err := s.adminSecurity.inspect(r)
		if err != nil {
			writeOpenAIError(w, http.StatusForbidden, "management_security_error", err.Error())
			return
		}
		if !info.localConsole {
			s.mu.Lock()
			mustChange := s.mustChangePassword
			s.mu.Unlock()
			if mustChange {
				writeOpenAIError(w, http.StatusServiceUnavailable, "management_security_error", "使用一次性 bootstrap secret 時，非 loopback 管理介面無法使用")
				return
			}
		}
		if adminRequestNeedsOrigin(r.Method) {
			if err := validateAdminOrigin(r.Header, info); err != nil {
				writeOpenAIError(w, http.StatusForbidden, "csrf_error", err.Error())
				return
			}
		}

		r = r.WithContext(context.WithValue(r.Context(), adminRequestInfoContextKey{}, info))
		next.ServeHTTP(w, r)
	})
}

func adminRequestNeedsOrigin(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func validateAdminOrigin(header http.Header, info adminRequestInfo) error {
	values := header.Values("Origin")
	if len(values) != 1 {
		return fmt.Errorf("必須提供一個 Origin header")
	}
	raw := strings.TrimSpace(values[0])
	if raw == "" || raw == "null" || strings.ContainsAny(raw, " \t\r\n,") {
		return fmt.Errorf("Origin 無效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("Origin 無效")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Origin scheme 無效")
	}
	authority, err := parseAdminAuthority(u.Host)
	if err != nil {
		return fmt.Errorf("Origin Host 無效")
	}
	if u.Scheme != info.scheme || originAuthorityKey(authority, u.Scheme) != originAuthorityKey(info.authority, info.scheme) {
		return fmt.Errorf("Origin 與管理 Host 不相符")
	}
	return nil
}

func originAuthorityKey(authority adminAllowedHost, scheme string) string {
	port := authority.port
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return authority.host + ":" + port
}

func adminRequestInfoFrom(r *http.Request) (adminRequestInfo, bool) {
	info, ok := r.Context().Value(adminRequestInfoContextKey{}).(adminRequestInfo)
	return info, ok
}

func secureAdminCookie(r *http.Request) bool {
	if info, ok := adminRequestInfoFrom(r); ok {
		return info.secure
	}
	return r.TLS != nil
}

func clientIP(r *http.Request) string {
	if info, ok := adminRequestInfoFrom(r); ok && info.clientIP != nil {
		return info.clientIP.String()
	}
	if ip := parseRemoteIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

func (s *Server) adminNow() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
