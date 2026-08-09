package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type artifactRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn artifactRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func artifactResponse(status int, body string, headers ...http.Header) *http.Response {
	header := make(http.Header)
	if len(headers) > 0 {
		header = headers[0]
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

type observedArtifactBody struct {
	reads  int
	closed bool
	io.Reader
}

func (body *observedArtifactBody) Read(buffer []byte) (int, error) {
	body.reads++
	return body.Reader.Read(buffer)
}

func (body *observedArtifactBody) Close() error {
	body.closed = true
	return nil
}

func TestWP6ArtifactFetchReturnsBodyWithoutEagerBuffering(t *testing.T) {
	body := &observedArtifactBody{Reader: strings.NewReader("streamed")}
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: -1}, nil
		})},
		Token:    func(context.Context, string) (string, error) { return "token", nil },
		MaxBytes: 8,
	}
	result, err := fetcher.Fetch(context.Background(), "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.bin", "a.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	if body.reads != 0 {
		t.Fatalf("Fetch eagerly read response body %d times", body.reads)
	}
	if result.MaxBytes != 8 || result.ContentLength != -1 {
		t.Fatalf("stream metadata max=%d contentLength=%d", result.MaxBytes, result.ContentLength)
	}
}

func TestWP6ArtifactFetchUsesIC3HeadersAndPreservesExactBytes(t *testing.T) {
	want := []byte("START\x00\n\xe4\xb8\xad\xf0\x9f\x98\x80END")
	tokenCalls := 0
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet {
				t.Fatalf("method=%q", request.Method)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer ams-token" {
				t.Fatalf("authorization=%q", got)
			}
			if got := request.Header.Get("MS-IC3-Product"); got != "Copilot" {
				t.Fatalf("MS-IC3-Product=%q", got)
			}
			if got := request.Header.Get("X-MS-Client-Version"); got != "wp6-test" {
				t.Fatalf("X-MS-Client-Version=%q", got)
			}
			if got := request.Header.Get("Accept-Encoding"); got != "identity" {
				t.Fatalf("Accept-Encoding=%q", got)
			}
			return artifactResponse(http.StatusOK, string(want)), nil
		})},
		Token: func(_ context.Context, scope string) (string, error) {
			tokenCalls++
			if scope != artifactIC3Scope {
				t.Fatalf("scope=%q", scope)
			}
			return "ams-token", nil
		},
		ClientVersion: "wp6-test",
	}

	result, err := fetcher.Fetch(context.Background(), "https://us-prod.asyncgw.teams.microsoft.com/v1/objects/object-1/views/original/upstream.txt?sig=private", "../report.csv")
	if err != nil {
		t.Fatal(err)
	}
	if raw := readFetchedArtifact(t, result); string(raw) != string(want) {
		t.Fatalf("result bytes=%q", raw)
	}
	if result.Filename != "_report.csv" {
		t.Fatalf("filename=%q", result.Filename)
	}
	if tokenCalls != 1 {
		t.Fatalf("token calls=%d", tokenCalls)
	}
}

func TestWP6ArtifactFetchFallsBackToSafePathFilename(t *testing.T) {
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return artifactResponse(http.StatusOK, "ok"), nil
		})},
		Token: func(context.Context, string) (string, error) { return "token", nil },
	}
	result, err := fetcher.Fetch(context.Background(), "https://eu.asyncgw.teams.microsoft.com:443/v1/objects/id/content/original/%E5%A0%B1%E5%91%8A%3A2026.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	result.Body.Close()
	if result.Filename != "報告_2026.txt" {
		t.Fatalf("filename=%q", result.Filename)
	}
}

func TestWP6ArtifactFetchRetriesOneRejectedToken(t *testing.T) {
	tokens := []string{"rejected-token", "fresh-token"}
	tokenCalls := 0
	requestCalls := 0
	invalidated := []string{}
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requestCalls++
			if requestCalls == 1 {
				if request.Header.Get("Authorization") != "Bearer rejected-token" {
					t.Fatalf("first authorization=%q", request.Header.Get("Authorization"))
				}
				return artifactResponse(http.StatusUnauthorized, "do-not-log-this-body"), nil
			}
			if request.Header.Get("Authorization") != "Bearer fresh-token" {
				t.Fatalf("second authorization=%q", request.Header.Get("Authorization"))
			}
			return artifactResponse(http.StatusOK, "fresh-result"), nil
		})},
		Token: func(context.Context, string) (string, error) {
			token := tokens[tokenCalls]
			tokenCalls++
			return token, nil
		},
		Invalidate: func(scope, rejected string) {
			if scope != artifactIC3Scope {
				t.Fatalf("invalidation scope=%q", scope)
			}
			invalidated = append(invalidated, rejected)
		},
	}
	result, err := fetcher.Fetch(context.Background(), "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if raw := readFetchedArtifact(t, result); string(raw) != "fresh-result" || tokenCalls != 2 || requestCalls != 2 {
		t.Fatalf("result=%q token calls=%d request calls=%d", raw, tokenCalls, requestCalls)
	}
	if len(invalidated) != 1 || invalidated[0] != "rejected-token" {
		t.Fatalf("invalidated=%#v", invalidated)
	}
}

func TestWP6ArtifactFetchNeverRetriesMoreThanOnce(t *testing.T) {
	tokenCalls := 0
	requestCalls := 0
	invalidated := []string{}
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requestCalls++
			return artifactResponse(http.StatusUnauthorized, "private-upstream-body"), nil
		})},
		Token: func(context.Context, string) (string, error) {
			tokenCalls++
			return "token-" + string(rune('0'+tokenCalls)), nil
		},
		Invalidate: func(_ string, rejected string) { invalidated = append(invalidated, rejected) },
	}
	_, err := fetcher.Fetch(context.Background(), "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt", "")
	if !errors.Is(err, errArtifactAuthorizationRejected) {
		t.Fatalf("error=%v", err)
	}
	if tokenCalls != 2 || requestCalls != 2 || len(invalidated) != 2 {
		t.Fatalf("token calls=%d request calls=%d invalidated=%#v", tokenCalls, requestCalls, invalidated)
	}
}

func TestWP6ArtifactFetchRejectsInvalidLocationsBeforeAuth(t *testing.T) {
	locations := []string{
		"not a URL",
		" https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
		"http://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
		"https://asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
		"https://asyncgw.teams.microsoft.com.evil.test/v1/objects/id/views/original/a.txt",
		"https://bad..asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
		"https://user@us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
		"https://us.asyncgw.teams.microsoft.com:444/v1/objects/id/views/original/a.txt",
		"https://us.asyncgw.teams.microsoft.com/v2/objects/id/views/original/a.txt",
		"https://us.asyncgw.teams.microsoft.com/v1/objects/id/raw/a.txt",
		"https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/thumbnail/a.txt",
		"https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt#fragment",
	}
	for _, location := range locations {
		t.Run(location, func(t *testing.T) {
			tokenCalls := 0
			requestCalls := 0
			fetcher := artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					requestCalls++
					return artifactResponse(http.StatusOK, "unexpected"), nil
				})},
				Token: func(context.Context, string) (string, error) {
					tokenCalls++
					return "token", nil
				},
			}
			_, err := fetcher.Fetch(context.Background(), location, "a.txt")
			if !errors.Is(err, errArtifactInvalidLocation) || tokenCalls != 0 || requestCalls != 0 {
				t.Fatalf("error=%v token calls=%d request calls=%d", err, tokenCalls, requestCalls)
			}
		})
	}
}

func TestWP6ArtifactFetchForbidsRedirects(t *testing.T) {
	requestCalls := 0
	location := "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt"
	fetcher := artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requestCalls++
			header := make(http.Header)
			header.Set("Location", "https://evil.test/stolen")
			return artifactResponse(http.StatusFound, "redirect-body-secret", header), nil
		})},
		Token: func(context.Context, string) (string, error) { return "redirect-token", nil },
	}
	_, err := fetcher.Fetch(context.Background(), location, "")
	if !errors.Is(err, errArtifactUpstreamStatus) || requestCalls != 1 {
		t.Fatalf("error=%v requests=%d", err, requestCalls)
	}
	for _, secret := range []string{location, "evil.test", "redirect-body-secret", "redirect-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestWP6ArtifactFetchInclusiveLimitAndZeroRejection(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{name: "inclusive", body: "1234"},
		{name: "oversized", body: "12345", want: errArtifactTooLarge},
		{name: "zero", body: "", want: errArtifactEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			fetcher := artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return artifactResponse(http.StatusOK, test.body), nil
				})},
				Token:    func(context.Context, string) (string, error) { return "token", nil },
				MaxBytes: 4,
			}
			result, err := fetcher.Fetch(context.Background(), "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.bin", "")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			if test.want == nil {
				if raw := readFetchedArtifact(t, result); string(raw) != test.body {
					t.Fatalf("bytes=%q", raw)
				}
			}
		})
	}
}

func readFetchedArtifact(t *testing.T, result artifactFetchResult) []byte {
	t.Helper()
	defer result.Body.Close()
	raw, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWP6ArtifactFetchReturnsStableRedactedErrors(t *testing.T) {
	const location = "https://us.asyncgw.teams.microsoft.com/v1/objects/private/views/original/secret.txt?sig=url-secret"
	tests := []struct {
		name    string
		fetcher artifactFetchClient
		want    error
	}{
		{
			name: "token provider",
			fetcher: artifactFetchClient{Token: func(context.Context, string) (string, error) {
				return "", errors.New("provider-secret")
			}},
			want: errArtifactAuthorizationUnavailable,
		},
		{
			name: "network",
			fetcher: artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("network-secret")
				})},
				Token: func(context.Context, string) (string, error) { return "token-secret", nil },
			},
			want: errArtifactUpstreamRequest,
		},
		{
			name: "status and body",
			fetcher: artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return artifactResponse(http.StatusInternalServerError, "body-secret"), nil
				})},
				Token: func(context.Context, string) (string, error) { return "token-secret", nil },
			},
			want: errArtifactUpstreamStatus,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.fetcher.Fetch(context.Background(), location, "secret.txt")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			for _, secret := range []string{location, "url-secret", "provider-secret", "network-secret", "token-secret", "body-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}
