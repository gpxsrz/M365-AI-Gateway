package web

import (
	"net/http/httptest"
	"testing"
)

func TestWP6ArtifactPublicOriginPolicy(t *testing.T) {
	for _, test := range []struct {
		name, value, want string
		fail              bool
	}{
		{name: "https", value: "https://sidecar.example.test", want: "https://sidecar.example.test"},
		{name: "loopback_http", value: "http://127.0.0.1:4141/", want: "http://127.0.0.1:4141"},
		{name: "remote_http", value: "http://sidecar.example.test", fail: true},
		{name: "path", value: "https://sidecar.example.test/base", fail: true},
		{name: "userinfo", value: "https://user@sidecar.example.test", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(artifactPublicOriginEnv, test.value)
			got, err := configuredArtifactPublicOrigin()
			if test.fail {
				if err == nil {
					t.Fatalf("accepted origin %q", test.value)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("origin=%q err=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestWP6ArtifactOriginOnlyInfersLoopbackRequests(t *testing.T) {
	s := &Server{}
	local := httptest.NewRequest("GET", "http://127.0.0.1:4141/v1/chat/completions", nil)
	local.RemoteAddr = "127.0.0.1:50000"
	if got, err := s.artifactPublicOrigin(local); err != nil || got != "http://127.0.0.1:4141" {
		t.Fatalf("local origin=%q err=%v", got, err)
	}

	remote := httptest.NewRequest("GET", "https://sidecar.example.test/v1/chat/completions", nil)
	remote.RemoteAddr = "198.51.100.7:50000"
	if _, err := s.artifactPublicOrigin(remote); err == nil {
		t.Fatal("remote request host became artifact authority without configured public origin")
	}
}
