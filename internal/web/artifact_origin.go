package web

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const artifactPublicOriginEnv = "M365_PUBLIC_ORIGIN"

func configuredArtifactPublicOrigin() (string, error) {
	raw := strings.TrimSpace(os.Getenv(artifactPublicOriginEnv))
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%s must be an absolute origin", artifactPublicOriginEnv)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && artifactOriginHostIsLoopback(parsed.Hostname())) {
		return "", fmt.Errorf("%s must use HTTPS outside loopback", artifactPublicOriginEnv)
	}
	return parsed.String(), nil
}

func artifactOriginHostIsLoopback(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) artifactPublicOrigin(r *http.Request) (string, error) {
	if s.artifactOrigin != "" {
		return s.artifactOrigin, nil
	}
	info, err := s.adminSecurity.inspect(r)
	if err != nil || !info.localConsole {
		return "", fmt.Errorf("artifact download links require %s for remote access", artifactPublicOriginEnv)
	}
	return info.scheme + "://" + info.authority.String(), nil
}
