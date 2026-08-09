package web

import "testing"

func TestWP6ArtifactCapabilityPathIsExactAndRedactable(t *testing.T) {
	valid := "/v1/artifacts/abcdefghijklmnopqrstuvwxyzABCDEFGH123456789/content"
	if token, ok := artifactCapabilityToken(valid); !ok || len(token) != artifactTokenLength {
		t.Fatalf("valid artifact path rejected: token length=%d ok=%v", len(token), ok)
	}
	for _, path := range []string{
		"/v1/artifacts/short/content",
		valid + "/extra",
		"/v1/artifacts/abcdefghijklmnopqrstuvwxyzABCDEFGH12345678!/content",
		"/v1/artifacts//content",
	} {
		if _, ok := artifactCapabilityToken(path); ok {
			t.Fatalf("malformed artifact path accepted: %q", path)
		}
	}
	if got := safeServiceLogPath(valid); got != "/v1/artifacts/{redacted}/content" {
		t.Fatalf("access-log path=%q", got)
	}
	if protocol, path := debugProtocolAndPath(valid); protocol != "artifact_download" || path != "/v1/artifacts/{redacted}/content" {
		t.Fatalf("debug identity=%q %q", protocol, path)
	}
}
