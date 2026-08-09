package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWP6ArtifactMaterializationAndBrowserDownloadSurviveRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.csv?private=sentinel"
	want := []byte("alpha,中文,😀\n")
	server := &Server{
		artifacts:      store,
		artifactOrigin: "https://sidecar.example.test",
		artifactFetch: &artifactFetchClient{
			HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != upstream || request.Header.Get("Authorization") != "Bearer ic3-token" {
					t.Fatalf("unexpected artifact request")
				}
				return artifactResponse(http.StatusOK, string(want)), nil
			})},
			Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
		},
	}
	result := artifactResultFixture(t, "原回答", []artifactFixture{{ReferenceID: "turn1file1", URL: upstream, Filename: "報告.csv"}})
	request := httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil)
	markdown, err := server.materializeArtifacts(context.Background(), request, &result)
	if err != nil {
		t.Fatal(err)
	}
	if markdown == "" || !strings.Contains(result.Text, "[下載 報告.csv](https://sidecar.example.test/v1/artifacts/") || chathub.ContainsProtectedArtifactReference(result.Text) {
		t.Fatalf("caller text=%q", result.Text)
	}
	token := artifactTokenFromMarkdown(t, result.Text)

	get := httptest.NewRecorder()
	server.adminMiddleware(http.HandlerFunc(server.artifactContent)).ServeHTTP(get, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+token+artifactRouteSuffix, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Header().Get("Content-Disposition"), "attachment") || get.Header().Get("Cache-Control") != "private, no-store" || string(get.Body.Bytes()) != string(want) {
		t.Fatalf("download status=%d headers=%v body=%q", get.Code, get.Header(), get.Body.Bytes())
	}
	if get.Header().Get("Content-Disposition") == "" || !strings.Contains(strings.ToLower(get.Header().Get("Content-Disposition")), "utf-8") {
		t.Fatalf("UTF-8 filename missing: %q", get.Header().Get("Content-Disposition"))
	}
	head := httptest.NewRecorder()
	server.artifactContent(head, httptest.NewRequest(http.MethodHead, artifactRoutePrefix+token+artifactRouteSuffix, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD status=%d length=%q body=%d", head.Code, head.Header().Get("Content-Length"), head.Body.Len())
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := openArtifactStore(root, artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedServer := &Server{artifacts: restarted}
	afterRestart := httptest.NewRecorder()
	restartedServer.artifactContent(afterRestart, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+token+artifactRouteSuffix, nil))
	if afterRestart.Code != http.StatusOK || string(afterRestart.Body.Bytes()) != string(want) {
		t.Fatalf("restart download status=%d body=%q", afterRestart.Code, afterRestart.Body.Bytes())
	}
}

func TestWP6ArtifactMaterializationCorrelatesTwoOutputsToDistinctExactDownloads(t *testing.T) {
	const (
		firstURL  = "https://us.asyncgw.teams.microsoft.com/v1/objects/one/views/original/first.csv"
		secondURL = "https://us.asyncgw.teams.microsoft.com/v1/objects/two/views/original/second.bin"
	)
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		artifacts:      store,
		artifactOrigin: "https://sidecar.example.test",
		artifactFetch: &artifactFetchClient{
			HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.String() {
				case firstURL:
					return artifactResponse(http.StatusOK, "first-bytes"), nil
				case secondURL:
					return artifactResponse(http.StatusOK, "second-bytes"), nil
				default:
					t.Fatalf("unexpected artifact URL")
					return nil, errors.New("unexpected artifact URL")
				}
			})},
			Token: func(context.Context, string) (string, error) { return "token", nil },
		},
	}
	result := artifactResultFixture(t, "outputs ready", []artifactFixture{
		{ReferenceID: "one", URL: firstURL, Filename: "first.csv"},
		{ReferenceID: "two", URL: secondURL, Filename: "second.bin"},
	})
	if _, err := server.materializeArtifacts(context.Background(), httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil), &result); err != nil {
		t.Fatal(err)
	}
	tokens := artifactTokensFromMarkdown(t, result.Text)
	if len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Fatalf("distinct artifact token count=%d collision=%t", len(tokens), len(tokens) == 2 && tokens[0] == tokens[1])
	}
	for index, want := range []struct {
		filename, body, sha256 string
	}{
		{filename: "first.csv", body: "first-bytes", sha256: "e811e1ebb5f584ba17b364d7bac66bad0de3e0e223757e48c386de0e31ac63db"},
		{filename: "second.bin", body: "second-bytes", sha256: "d7b0717202604f4941983807ee1dbed5cab2458921145e952cf6230aa400da46"},
	} {
		recorder := httptest.NewRecorder()
		server.artifactContent(recorder, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+tokens[index]+artifactRouteSuffix, nil))
		digest := sha256.Sum256(recorder.Body.Bytes())
		if recorder.Code != http.StatusOK || recorder.Body.String() != want.body || hex.EncodeToString(digest[:]) != want.sha256 || !strings.Contains(recorder.Header().Get("Content-Disposition"), want.filename) {
			t.Fatalf("artifact %d status=%d filename=%q body=%q sha256=%x", index, recorder.Code, recorder.Header().Get("Content-Disposition"), recorder.Body.String(), digest)
		}
	}
}

func TestWP6ArtifactHEADUsesMetadataWithoutReadingBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.PutReader("report.txt", strings.NewReader("good"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(onlyArtifactBlob(t, root), []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{artifacts: store}
	head := httptest.NewRecorder()
	server.artifactContent(head, httptest.NewRequest(http.MethodHead, artifactRoutePrefix+record.Token+artifactRouteSuffix, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "4" {
		t.Fatalf("HEAD status=%d length=%q body=%q", head.Code, head.Header().Get("Content-Length"), head.Body.String())
	}
	get := httptest.NewRecorder()
	server.artifactContent(get, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+record.Token+artifactRouteSuffix, nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("corrupt GET status=%d body=%q", get.Code, get.Body.String())
	}
}

func TestWP6ArtifactMaterializationStreamsUnknownLengthWithinConfiguredLimit(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/result.bin"
	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "inclusive", body: "12345678"},
		{name: "oversized", body: "123456789", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &observedArtifactBody{Reader: strings.NewReader(test.body)}
			store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{MaxBytes: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server := &Server{
				artifacts:      store,
				artifactOrigin: "https://sidecar.example.test",
				artifactFetch: &artifactFetchClient{
					HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, ContentLength: -1}, nil
					})},
					Token:    func(context.Context, string) (string, error) { return "token", nil },
					MaxBytes: 8,
				},
			}
			result := artifactResultFixture(t, "", []artifactFixture{{ReferenceID: "one", URL: upstream, Filename: "result.bin"}})
			_, err = server.materializeArtifacts(context.Background(), httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil), &result)
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v wantErr=%t", err, test.wantErr)
			}
			if !body.closed {
				t.Fatal("upstream body was not closed")
			}
			entries, readErr := os.ReadDir(filepath.Join(store.root, "blobs"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantBlobs := 1
			if test.wantErr {
				wantBlobs = 0
			}
			if len(entries) != wantBlobs {
				t.Fatalf("stored blobs=%d want=%d", len(entries), wantBlobs)
			}
		})
	}
}

func TestWP6ArtifactMaterializationAlwaysAddsCanonicalMarkdownLink(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.txt"
	for _, test := range []struct {
		name, original string
	}{
		{name: "plain URL", original: "download: " + upstream},
		{name: "inline code", original: "download: `" + upstream + "`"},
		{name: "existing canonical link", original: "[下載 report.txt](" + upstream + ")"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server := &Server{
				artifacts:      store,
				artifactOrigin: "https://sidecar.example.test",
				artifactFetch: &artifactFetchClient{
					HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
						return artifactResponse(http.StatusOK, "exact"), nil
					})},
					Token: func(context.Context, string) (string, error) { return "token", nil },
				},
			}
			result := artifactResultFixture(t, test.original, []artifactFixture{{ReferenceID: "one", URL: upstream, Filename: "report.txt"}})
			if _, err := server.materializeArtifacts(context.Background(), httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil), &result); err != nil {
				t.Fatal(err)
			}
			if strings.Count(result.Text, "[下載 report.txt](https://sidecar.example.test/v1/artifacts/") != 1 || strings.Contains(result.Text, upstream) {
				t.Fatalf("non-canonical artifact text=%q", result.Text)
			}
		})
	}
}

func TestWP6ArtifactMaterializationRollsBackPartialBatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{MaxEntries: 1, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		artifacts:      store,
		artifactOrigin: "https://sidecar.example.test",
		artifactFetch: &artifactFetchClient{
			HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return artifactResponse(http.StatusOK, "bytes"), nil
			})},
			Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
		},
	}
	result := artifactResultFixture(t, "answer", []artifactFixture{
		{ReferenceID: "one", URL: "https://us.asyncgw.teams.microsoft.com/v1/objects/one/views/original/one.txt", Filename: "one.txt"},
		{ReferenceID: "two", URL: "https://us.asyncgw.teams.microsoft.com/v1/objects/two/views/original/two.txt", Filename: "two.txt"},
	})
	request := httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil)
	if _, err := server.materializeArtifacts(context.Background(), request, &result); !errors.Is(err, errArtifactMaterialization) {
		t.Fatalf("error=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial materialization left %d blobs", len(entries))
	}
}

func TestWP6ArtifactMaterializationFailsBeforeFetchWithoutTrustedOrigin(t *testing.T) {
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fetchCalls := 0
	server := &Server{
		artifacts: store,
		artifactFetch: &artifactFetchClient{
			HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
				fetchCalls++
				return artifactResponse(http.StatusOK, "secret"), nil
			})},
			Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
		},
	}
	result := artifactResultFixture(t, "answer", []artifactFixture{{ReferenceID: "one", URL: "https://us.asyncgw.teams.microsoft.com/v1/objects/one/views/original/one.txt"}})
	request := httptest.NewRequest(http.MethodPost, "https://untrusted.example.test/v1/chat/completions", nil)
	request.RemoteAddr = "198.51.100.8:50000"
	if _, err := server.materializeArtifacts(context.Background(), request, &result); !errors.Is(err, errArtifactMaterialization) || fetchCalls != 0 {
		t.Fatalf("error=%v fetch calls=%d", err, fetchCalls)
	}
}

func TestWP6ArtifactUnknownAndMalformedPathsDoNotDiscloseState(t *testing.T) {
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{artifacts: store}
	unknown := strings.Repeat("a", artifactTokenLength)
	rr := httptest.NewRecorder()
	server.adminMiddleware(http.HandlerFunc(server.artifactContent)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+unknown+artifactRouteSuffix, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown capability status=%d", rr.Code)
	}
	malformed := httptest.NewRecorder()
	server.adminMiddleware(http.HandlerFunc(server.artifactContent)).ServeHTTP(malformed, httptest.NewRequest(http.MethodGet, artifactRoutePrefix+"short"+artifactRouteSuffix, nil))
	if malformed.Code != http.StatusUnauthorized {
		t.Fatalf("malformed unauthenticated route status=%d", malformed.Code)
	}
}

func TestWP6ArtifactMarkdownCrossesEveryTextAdapter(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/result.txt"
	for _, test := range []struct {
		name string
		run  func(*Server) *httptest.ResponseRecorder
		want string
	}{
		{
			name: "legacy_nonstream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"m365-auto","message":"create file"}`)))
				return rr
			},
			want: `"status":"ok"`,
		},
		{
			name: "legacy_stream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"m365-auto","message":"create file"}`)))
				return rr
			},
			want: "event: done",
		},
		{
			name: "chat_nonstream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.openaiChat(rr, wp1ChatRequest("m365-auto", ""))
				return rr
			},
			want: `"finish_reason":"stop"`,
		},
		{
			name: "chat_stream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.openaiChat(rr, wp1ChatRequest("m365-auto", `,"stream":true`))
				return rr
			},
			want: "data: [DONE]",
		},
		{
			name: "responses_nonstream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m365-auto","input":"create file"}`)))
				return rr
			},
			want: `"status":"completed"`,
		},
		{
			name: "responses_stream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m365-auto","input":"create file","stream":true}`)))
				return rr
			},
			want: "event: response.completed",
		},
		{
			name: "anthropic_nonstream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				body := `{"model":"m365-auto","max_tokens":64,"messages":[{"role":"user","content":"create file"}]}`
				server.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
				return rr
			},
			want: `"type":"message"`,
		},
		{
			name: "anthropic_stream",
			run: func(server *Server) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				body := `{"model":"m365-auto","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"create file"}]}`
				server.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
				return rr
			},
			want: "event: message_stop",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: artifactResultFixture(t, "", []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "result.txt"}})}
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
			rr := test.run(server)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), test.want) || !strings.Contains(rr.Body.String(), "https://sidecar.example.test/v1/artifacts/") {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if chathub.ContainsProtectedArtifactReference(rr.Body.String()) {
				t.Fatalf("protected upstream artifact leaked: %s", rr.Body.String())
			}
		})
	}
}

func TestWP6BingAndCodeInterpreterArtifactProjectionCoexist(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/result.csv"
	structured, err := json.Marshal(map[string]any{"outputFiles": []any{map[string]any{
		"reference_id": "turn1file1", "codeResultFileUrl": upstream, "filename": "result.csv",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(map[string]any{
		"type": 1, "target": "update", "arguments": []any{map[string]any{"messages": []any{
			map[string]any{
				"messageType": "Progress", "contentType": "SearchResults", "text": "searching",
				"references": map[string]any{"bing-ref": map[string]any{
					"targetLink": "https://support.microsoft.com/topic", "isCitedInResponse": true,
					"displayData": map[string]any{"type": "Web", "content": "Microsoft Support"},
				}},
			},
			map[string]any{"messageType": "GeneratedCode", "contentOrigin": "CodeInterpreter", "text": string(structured)},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat := &wp1CandidateChat{result: chathub.Result{Text: "Grounded answer", Events: []json.RawMessage{frame}}}
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
			return artifactResponse(http.StatusOK, "exact-csv-bytes"), nil
		})},
		Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
	}

	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, wp1ChatRequest("m365-auto", ""))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"search_result_markers":1`) || !strings.Contains(body, "https://support.microsoft.com/topic") || !strings.Contains(body, "https://sidecar.example.test/v1/artifacts/") {
		t.Fatalf("status=%d body=%s", recorder.Code, body)
	}
	if chathub.ContainsProtectedArtifactReference(body) || strings.Contains(body, "turn1file1") {
		t.Fatalf("artifact metadata escaped or was misclassified: %s", body)
	}
}

func TestWP6ArtifactCapabilityAndBytesNeverEnterAccessOrDebugEvidence(t *testing.T) {
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Put("private.txt", []byte("PRIVATE-ARTIFACT-BYTES-SENTINEL"))
	if err != nil {
		t.Fatal(err)
	}
	debugStore := openDebugStoreWithPolicy(filepath.Join(t.TempDir(), "debug.json"), testDebugPolicy())
	server := &Server{artifacts: store, debug: debugStore, settings: &settingsStore{v: defaultRuntimeSettings()}}

	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(originalWriter)

	rr := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, artifactRoutePrefix+record.Token+artifactRouteSuffix, nil)
	request.RemoteAddr = "127.0.0.1:50000"
	server.Routes().ServeHTTP(rr, request)
	if rr.Code != http.StatusOK || rr.Body.String() != "PRIVATE-ARTIFACT-BYTES-SENTINEL" {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if strings.Contains(logs.String(), record.Token) || strings.Contains(logs.String(), "PRIVATE-ARTIFACT-BYTES-SENTINEL") || !strings.Contains(logs.String(), "/v1/artifacts/{redacted}/content") {
		t.Fatalf("unsafe access trace=%q", logs.String())
	}
	if records := debugStore.list(); len(records) != 0 {
		t.Fatalf("artifact request entered debug evidence: %#v", records)
	}
}

func TestWP6ArtifactFailureIsExplicitAndNeverReturnsSuccessTerminal(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/fail.txt"
	chat := &wp1CandidateChat{result: artifactResultFixture(t, "artifact ready", []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "fail.txt"}})}
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
			return artifactResponse(http.StatusInternalServerError, "PRIVATE-UPSTREAM-ERROR"), nil
		})},
		Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
	}

	nonstream := httptest.NewRecorder()
	server.openaiChat(nonstream, wp1ChatRequest("m365-auto", ""))
	if nonstream.Code != http.StatusBadGateway || !strings.Contains(nonstream.Body.String(), "artifact_materialization_failed") || strings.Contains(nonstream.Body.String(), "PRIVATE-UPSTREAM-ERROR") || strings.Contains(nonstream.Body.String(), upstream) {
		t.Fatalf("nonstream status=%d body=%s", nonstream.Code, nonstream.Body.String())
	}

	stream := httptest.NewRecorder()
	server.openaiChat(stream, wp1ChatRequest("m365-auto", `,"stream":true`))
	if !strings.Contains(stream.Body.String(), `"code":"artifact_materialization_failed"`) || strings.Contains(stream.Body.String(), `"finish_reason":"stop"`) || strings.Contains(stream.Body.String(), upstream) || strings.Contains(stream.Body.String(), "PRIVATE-UPSTREAM-ERROR") {
		t.Fatalf("stream status=%d body=%s", stream.Code, stream.Body.String())
	}
}

type artifactFixture struct {
	ReferenceID, URL, Filename string
}

func artifactResultFixture(t *testing.T, text string, files []artifactFixture) chathub.Result {
	t.Helper()
	output := make([]any, 0, len(files))
	for _, file := range files {
		output = append(output, map[string]any{"reference_id": file.ReferenceID, "codeResultFileUrl": file.URL, "filename": file.Filename})
	}
	structured, err := json.Marshal(map[string]any{"outputFiles": output})
	if err != nil {
		t.Fatal(err)
	}
	message := map[string]any{"messageType": "GeneratedCode", "contentOrigin": "CodeInterpreter", "text": string(structured)}
	frame, err := json.Marshal(map[string]any{
		"type": 1, "target": "update", "arguments": []any{map[string]any{"messages": []any{message}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return chathub.Result{Text: text, Events: []json.RawMessage{frame}}
}

func artifactTokenFromMarkdown(t *testing.T, text string) string {
	t.Helper()
	start := strings.Index(text, artifactRoutePrefix)
	if start < 0 {
		t.Fatalf("artifact path missing from %q", text)
	}
	start += len(artifactRoutePrefix)
	end := strings.Index(text[start:], artifactRouteSuffix)
	if end < 0 {
		t.Fatalf("artifact route suffix missing from %q", text)
	}
	token := text[start : start+end]
	if len(token) != artifactTokenLength {
		t.Fatalf("token length=%d", len(token))
	}
	return token
}

func artifactTokensFromMarkdown(t *testing.T, text string) []string {
	t.Helper()
	tokens := make([]string, 0)
	seen := make(map[string]struct{})
	for {
		start := strings.Index(text, artifactRoutePrefix)
		if start < 0 {
			break
		}
		start += len(artifactRoutePrefix)
		end := strings.Index(text[start:], artifactRouteSuffix)
		if end < 0 {
			t.Fatalf("artifact route suffix missing from %q", text)
		}
		token := text[start : start+end]
		if len(token) != artifactTokenLength {
			t.Fatalf("token length=%d", len(token))
		}
		if _, ok := seen[token]; !ok {
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
		text = text[start+end+len(artifactRouteSuffix):]
	}
	return tokens
}
