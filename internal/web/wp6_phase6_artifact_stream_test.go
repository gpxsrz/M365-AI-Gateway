package web

import (
	"context"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWP6ArtifactStreamGuardHoldsSameAndSplitURLWithUnicodePrefix(t *testing.T) {
	for _, prefix := range []string{"ordinary ", "KX", "ȺX"} {
		var buffer strings.Builder
		buffer.WriteString(prefix + "https://artifact.asyncgw.teams.microsoft.com/private")
		got := releaseArtifactSafePrefix(&buffer)
		if got != prefix {
			t.Fatalf("prefix %q released %q held=%q", prefix, got, buffer.String())
		}
		if !utf8.ValidString(got) || !strings.HasPrefix(buffer.String(), "https://") {
			t.Fatalf("invalid boundary released=%q held=%q", got, buffer.String())
		}
	}

	var split strings.Builder
	split.WriteString("safe KXhtt")
	if got := releaseArtifactSafePrefix(&split); got != "safe KX" || split.String() != "htt" {
		t.Fatalf("first split released=%q held=%q", got, split.String())
	}
	split.WriteString("ps://artifact.asyncgw.teams.microsoft.com/private")
	if got := releaseArtifactSafePrefix(&split); got != "" || !strings.HasPrefix(split.String(), "https://") {
		t.Fatalf("second split released=%q held=%q", got, split.String())
	}
}

func TestWP6ChatStreamMaterializesSameAndSplitArtifactURLBeforeTerminal(t *testing.T) {
	const upstream = "https://artifact.asyncgw.teams.microsoft.com/v1/objects/id/views/original/output.txt"
	for _, test := range []struct {
		name   string
		deltas []string
	}{
		{name: "same delta", deltas: []string{"ready " + upstream}},
		{name: "split delta", deltas: []string{"ready htt", "ps://artifact.asyncgw.teams.", "microsoft.com/v1/objects/id/views/original/output.txt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := artifactResultFixture(t, "ready "+upstream, []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "output.txt"}})
			chat := &wp1CandidateChat{result: result}
			for _, delta := range test.deltas {
				chat.events = append(chat.events, chathub.StreamEvent{Kind: "text", Text: delta})
			}
			server := newWP1CandidateServer(t, chat)
			store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server.artifacts = store
			server.artifactOrigin = "https://sidecar.example.test"
			server.artifactFetch = &artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return artifactResponse(http.StatusOK, "exact-output"), nil
				})},
				Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
			}
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, wp1ChatRequest("m365-auto", `,"stream":true`))
			body := recorder.Body.String()
			if strings.Contains(body, "asyncgw.teams.microsoft.com") || strings.Contains(body, "codeResultFileUrl") || strings.Contains(body, "blob:") {
				t.Fatalf("protected artifact leaked: %s", body)
			}
			if !strings.Contains(body, "[下載 output.txt](https://sidecar.example.test/v1/artifacts/") || !strings.Contains(body, `"finish_reason":"stop"`) || !strings.Contains(body, "data: [DONE]") {
				t.Fatalf("materialized success stream=%s", body)
			}
		})
	}
}

func TestWP6ChatStreamArtifactFetchFailureHasNoSuccessTerminal(t *testing.T) {
	const upstream = "https://artifact.asyncgw.teams.microsoft.com/v1/objects/id/views/original/fail.txt"
	result := artifactResultFixture(t, "ready "+upstream, []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "fail.txt"}})
	chat := &wp1CandidateChat{result: result, events: []chathub.StreamEvent{
		{Kind: "text", Text: "ready https://artifact.asyncgw.teams."},
		{Kind: "text", Text: "microsoft.com/v1/objects/id/views/original/fail.txt"},
	}}
	server := newWP1CandidateServer(t, chat)
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.artifacts = store
	server.artifactOrigin = "https://sidecar.example.test"
	server.artifactFetch = &artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return artifactResponse(http.StatusBadGateway, "private-error"), nil
		})},
		Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
	}
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, wp1ChatRequest("m365-auto", `,"stream":true`))
	body := recorder.Body.String()
	if !strings.Contains(body, `"code":"artifact_materialization_failed"`) || strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, "/v1/artifacts/") || strings.Contains(body, "asyncgw.teams.microsoft.com") || strings.Contains(body, "private-error") {
		t.Fatalf("unsafe artifact failure stream=%s", body)
	}
}
