package web

import (
	"context"
	"errors"
	"io"
	"m365-native/internal/outbound"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	artifactIC3Scope       = "https://ic3.teams.office.com/.default openid profile offline_access"
	defaultArtifactMaxSize = int64(512 << 20)
	maxArtifactNameBytes   = 240
)

var (
	errArtifactInvalidLocation          = errors.New("artifact fetch: invalid upstream location")
	errArtifactAuthorizationUnavailable = errors.New("artifact fetch: authorization unavailable")
	errArtifactAuthorizationRejected    = errors.New("artifact fetch: authorization rejected")
	errArtifactUpstreamRequest          = errors.New("artifact fetch: upstream request failed")
	errArtifactUpstreamStatus           = errors.New("artifact fetch: unexpected upstream status")
	errArtifactTooLarge                 = errors.New("artifact fetch: output exceeds size limit")
	errArtifactEmpty                    = errors.New("artifact fetch: output is empty")
	errArtifactRedirect                 = errors.New("artifact fetch: redirect forbidden")
)

type artifactFetchClient struct {
	HTTPClient    *http.Client
	Token         func(context.Context, string) (string, error)
	Invalidate    func(string, string)
	ClientVersion string
	MaxBytes      int64
}

type artifactFetchResult struct {
	Body          io.ReadCloser
	Filename      string
	MaxBytes      int64
	ContentLength int64
}

func (client artifactFetchClient) Fetch(ctx context.Context, rawURL, metadataName string) (artifactFetchResult, error) {
	location, err := parseArtifactLocation(rawURL)
	if err != nil {
		return artifactFetchResult{}, errArtifactInvalidLocation
	}
	if client.Token == nil {
		return artifactFetchResult{}, errArtifactAuthorizationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = outbound.HTTPClient()
	}
	requestClient := *httpClient
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errArtifactRedirect }
	version := strings.TrimSpace(client.ClientVersion)
	if version == "" {
		version = strings.TrimSpace(Version)
	}
	if version == "" {
		version = "m365-copilot2api"
	}

	for attempt := 0; attempt < 2; attempt++ {
		token, tokenErr := client.Token(ctx, artifactIC3Scope)
		if tokenErr != nil || !validArtifactBearerToken(token) {
			return artifactFetchResult{}, errArtifactAuthorizationUnavailable
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if requestErr != nil {
			return artifactFetchResult{}, errArtifactInvalidLocation
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("MS-IC3-Product", "Copilot")
		request.Header.Set("X-MS-Client-Version", version)
		request.Header.Set("Accept-Encoding", "identity")

		response, requestErr := requestClient.Do(request)
		if requestErr != nil {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			if errors.Is(requestErr, errArtifactRedirect) {
				return artifactFetchResult{}, errArtifactUpstreamStatus
			}
			return artifactFetchResult{}, errArtifactUpstreamRequest
		}
		if response == nil || response.Body == nil {
			return artifactFetchResult{}, errArtifactUpstreamRequest
		}
		if response.StatusCode == http.StatusUnauthorized {
			response.Body.Close()
			if client.Invalidate != nil {
				client.Invalidate(artifactIC3Scope, token)
			}
			if attempt == 0 && client.Invalidate != nil {
				continue
			}
			return artifactFetchResult{}, errArtifactAuthorizationRejected
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			response.Body.Close()
			return artifactFetchResult{}, errArtifactUpstreamStatus
		}

		limit := client.MaxBytes
		if limit <= 0 || limit > defaultArtifactMaxSize {
			limit = defaultArtifactMaxSize
		}
		if response.ContentLength > limit {
			response.Body.Close()
			return artifactFetchResult{}, errArtifactTooLarge
		}
		if response.ContentLength == 0 {
			response.Body.Close()
			return artifactFetchResult{}, errArtifactEmpty
		}
		return artifactFetchResult{
			Body:          response.Body,
			Filename:      safeArtifactFilename(metadataName, location),
			MaxBytes:      limit,
			ContentLength: response.ContentLength,
		}, nil
	}
	return artifactFetchResult{}, errArtifactAuthorizationRejected
}

func parseArtifactLocation(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, errArtifactInvalidLocation
	}
	location, err := url.Parse(raw)
	if err != nil || location.Scheme != "https" || location.Host == "" || location.User != nil || location.Fragment != "" {
		return nil, errArtifactInvalidLocation
	}
	hostname := strings.ToLower(location.Hostname())
	const suffix = ".asyncgw.teams.microsoft.com"
	if !strings.HasSuffix(hostname, suffix) || strings.TrimSuffix(hostname, suffix) == "" {
		return nil, errArtifactInvalidLocation
	}
	for _, label := range strings.Split(strings.TrimSuffix(hostname, suffix), ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") || strings.IndexFunc(label, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
		}) >= 0 {
			return nil, errArtifactInvalidLocation
		}
	}
	if port := location.Port(); port != "" && port != "443" {
		return nil, errArtifactInvalidLocation
	}
	escapedPath := strings.ToLower(location.EscapedPath())
	if strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return nil, errArtifactInvalidLocation
	}
	parts := strings.Split(strings.TrimPrefix(location.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "v1" || parts[1] != "objects" || parts[2] == "" ||
		(parts[3] != "content" && parts[3] != "views") || parts[4] != "original" {
		return nil, errArtifactInvalidLocation
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return nil, errArtifactInvalidLocation
		}
	}
	return location, nil
}

func validArtifactBearerToken(token string) bool {
	return token != "" && strings.TrimSpace(token) == token && strings.IndexFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) < 0
}

func safeArtifactFilename(metadataName string, location *url.URL) string {
	name := strings.TrimSpace(metadataName)
	if name == "" && location != nil {
		name = path.Base(location.Path)
	}
	var safe strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':':
			safe.WriteByte('_')
		case unicode.IsControl(r):
			safe.WriteByte('_')
		default:
			safe.WriteRune(r)
		}
	}
	name = strings.Trim(safe.String(), " .")
	if name == "" {
		name = "artifact"
	}
	if len(name) <= maxArtifactNameBytes {
		return name
	}
	extension := path.Ext(name)
	if len(extension) > 32 {
		extension = ""
	}
	base := strings.TrimSuffix(name, extension)
	budget := maxArtifactNameBytes - len(extension)
	for len(base) > budget {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	base = strings.TrimRight(base, " .")
	if base == "" {
		base = "artifact"
	}
	return base + extension
}
