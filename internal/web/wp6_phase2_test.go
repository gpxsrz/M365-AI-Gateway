package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func TestWP6SettingsDefaultAndRestartPersistence(t *testing.T) {
	fresh := defaultRuntimeSettings()
	if fresh.ChatMode != chatModePrivate || fresh.TextInputLimitUTF16 != defaultTextInputLimitUTF16 || fresh.ChatTimeoutSeconds != 120 {
		t.Fatalf("fresh settings=%#v", fresh)
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	store := &settingsStore{path: path, v: fresh}
	updated := fresh
	updated.ChatMode = chatModeNormal
	updated.TextInputLimitUTF16 = 262144
	if err := store.save(updated); err != nil {
		t.Fatal(err)
	}
	restarted := loadSettingsStore(path)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	if got := restarted.get(); got.ChatMode != chatModeNormal || got.TextInputLimitUTF16 != 262144 {
		t.Fatalf("restarted settings=%#v", got)
	}

	legacyPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(legacyPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := loadSettingsStore(legacyPath)
	if legacy.loadErr != nil || legacy.get().ChatMode != chatModePrivate || legacy.get().TextInputLimitUTF16 != defaultTextInputLimitUTF16 {
		t.Fatalf("legacy settings=%#v err=%v", legacy.get(), legacy.loadErr)
	}
}

func TestWP6SettingsRejectInvalidChatModeAndTextLimit(t *testing.T) {
	v := defaultRuntimeSettings()
	v.ChatMode = "unknown"
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted unknown chat mode")
	}
	v = defaultRuntimeSettings()
	v.TextInputLimitUTF16 = 0
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted zero UTF-16 text limit")
	}
	v = defaultRuntimeSettings()
	v.TextInputLimitUTF16 = maximumTextInputLimitUTF16 + 1
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted a text policy that cannot fit the finite request guard")
	}
}

func TestConfiguredChatHubClientUsesHotChatMode(t *testing.T) {
	store := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	client := newConfiguredChatHubClient(store)
	if client.PrivateMode == nil || !client.PrivateMode() {
		t.Fatal("fresh configured client is not private")
	}
	v := store.get()
	v.ChatMode = chatModeNormal
	if err := store.save(v); err != nil {
		t.Fatal(err)
	}
	if client.PrivateMode() {
		t.Fatal("normal mode did not apply to the next connection")
	}
	v.ChatMode = chatModePrivate
	if err := store.save(v); err != nil {
		t.Fatal(err)
	}
	if !client.PrivateMode() {
		t.Fatal("private mode did not reapply to the next connection")
	}
}

func TestUTF16CodeUnitsAndDefaultBoundary(t *testing.T) {
	for input, want := range map[string]int{"a": 1, "測": 1, "😀": 2, "a😀測": 4} {
		if got := utf16CodeUnits(input); got != want {
			t.Fatalf("utf16CodeUnits(%q)=%d want=%d", input, got, want)
		}
	}
	if err := validateCallerText([]oaiMsg{{Role: "user", Content: strings.Repeat("a", 128000)}}, 128000); err != nil {
		t.Fatal(err)
	}
	if err := validateCallerText([]oaiMsg{{Role: "user", Content: strings.Repeat("😀", 64000)}}, 128000); err != nil {
		t.Fatal(err)
	}
	if err := validateCallerText([]oaiMsg{{Role: "user", Content: strings.Repeat("a", 128001)}}, 128000); err == nil {
		t.Fatal("accepted 128001 UTF-16 units")
	}
}

func TestTextPolicyCountsCallerRolesButNotGeneratedOrBinaryContent(t *testing.T) {
	messages := []oaiMsg{
		{Role: "system", Content: strings.Repeat("s", 20)},
		{Role: "user", Content: strings.Repeat("u", 20)},
		{Role: "assistant", Content: strings.Repeat("a", 20)},
		{Role: "tool", Content: strings.Repeat("t", 20)},
	}
	if err := validateCallerText(messages, 79); err == nil {
		t.Fatal("did not count caller text across roles")
	}
	generatedAndBinary := []oaiMsg{
		{Role: "system", Content: strings.Repeat("generated", 20000), SidecarGenerated: true},
		{Role: "user", Content: []any{
			map[string]any{"type": "input_text", "text": strings.Repeat("x", 128000)},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", 2<<20)},
		}},
	}
	if err := validateCallerText(generatedAndBinary, 128000); err != nil {
		t.Fatal(err)
	}
	toolResult := []oaiMsg{{Role: "tool", Content: strings.Repeat("x", 128001)}}
	if err := validateCallerText(toolResult, 128000); err == nil {
		t.Fatal("full tool result was not checked before prompt compaction")
	}
}

func TestTextPolicyRejectsEveryPublicTextAdapterBeforeChatHub(t *testing.T) {
	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	cases := []struct {
		name string
		call func(*Server, *httptest.ResponseRecorder)
	}{
		{"legacy", func(s *Server, rr *httptest.ResponseRecorder) {
			s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"gpt-5.6-sol","message":`+mustJSON(over)+`}`)))
		}},
		{"legacy_stream", func(s *Server, rr *httptest.ResponseRecorder) {
			s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"gpt-5.6-sol","message":`+mustJSON(over)+`}`)))
		}},
		{"chat_completions", func(s *Server, rr *httptest.ResponseRecorder) {
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":`+mustJSON(over)+`}]}`)))
		}},
		{"responses", func(s *Server, rr *httptest.ResponseRecorder) {
			s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":`+mustJSON(over)+`}`)))
		}},
		{"anthropic", func(s *Server, rr *httptest.ResponseRecorder) {
			s.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":`+mustJSON(over)+`}]}`)))
		}},
		{"images", func(s *Server, rr *httptest.ResponseRecorder) {
			s.imageGenerations(rr, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-5.6-sol","prompt":`+mustJSON(over)+`}`)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "ok", ConversationID: "conversation", SessionID: "session", RequestID: "request", Images: []string{"https://example.test/image.png"}}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			tc.call(server, recorder)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "128000") || !strings.Contains(strings.ToLower(recorder.Body.String()), "utf-16") {
				t.Fatalf("unclear policy error: %s", recorder.Body.String())
			}
			if len(chat.requests) != 0 {
				t.Fatalf("ChatHub called %d times", len(chat.requests))
			}
		})
	}
}

func TestRaisedTextPolicyAppliesImmediately(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "ok", ConversationID: "conversation", SessionID: "session", RequestID: "request"}}
	server := newWP1CandidateServer(t, chat)
	server.settings.path = filepath.Join(t.TempDir(), "settings.json")
	exact := strings.Repeat("x", 128000)
	exactBody := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":` + mustJSON(exact) + `}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exactBody)))
	if recorder.Code != http.StatusOK || len(chat.requests) != 1 {
		t.Fatalf("exact status=%d calls=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
	}
	text := strings.Repeat("x", 128001)
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":` + mustJSON(text) + `}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest || len(chat.requests) != 1 {
		t.Fatalf("default status=%d calls=%d", recorder.Code, len(chat.requests))
	}
	v := server.settings.get()
	v.TextInputLimitUTF16 = 128001
	if err := server.settings.save(v); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if recorder.Code != http.StatusOK || len(chat.requests) != 2 {
		t.Fatalf("raised status=%d calls=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
	}
}

func TestResponsesHasNoHiddenReserializationTextLimit(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "ok", ConversationID: "conversation", SessionID: "session", RequestID: "request"}}
	server := newWP1CandidateServer(t, chat)
	server.settings.path = filepath.Join(t.TempDir(), "settings.json")
	v := server.settings.get()
	v.TextInputLimitUTF16 = 1_800_000
	if err := server.settings.save(v); err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("<", v.TextInputLimitUTF16)
	request := responsesRequest{Model: "gpt-5.6-sol", Input: input}
	o, err := request.openAI()
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(serialized) <= 10<<20 {
		t.Fatalf("fixture did not cross the former hidden limit: %d", len(serialized))
	}
	httpRequest := withCallerTextValidated(httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{}`))))
	_, raw, status, adapterErr := server.runOpenAIAdapter(httpRequest, o)
	if status != http.StatusOK || adapterErr != nil || len(chat.requests) != 1 {
		t.Fatalf("status=%d err=%v calls=%d body-prefix=%q", status, adapterErr, len(chat.requests), string(raw[:minIntForTest(len(raw), 300)]))
	}
}

func TestValidatedAdapterRequestCannotExpandPastFiniteSafetyGuard(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "must not run"}}
	server := newWP1CandidateServer(t, chat)
	limit, err := requestBodyLimit(server.settings.get().TextInputLimitUTF16)
	if err != nil {
		t.Fatal(err)
	}
	request := withCallerTextValidated(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	request.ContentLength = limit + 1
	recorder := httptest.NewRecorder()

	server.openaiChat(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge || len(chat.requests) != 0 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
	}
	if !strings.Contains(strings.ToLower(recorder.Body.String()), "sidecar") {
		t.Fatalf("resource error is not explicit: %s", recorder.Body.String())
	}
}

func TestRequestGuardIncludesThreeMaximumInlineAttachments(t *testing.T) {
	limit, err := requestBodyLimit(maximumTextInputLimitUTF16)
	if err != nil {
		t.Fatal(err)
	}
	required := 3*maxEncodedAttachmentBytes + int64(maximumTextInputLimitUTF16)*requestBytesPerUTF16 + requestBodyOverhead
	if limit < required {
		t.Fatalf("request guard=%d, need at least %d for accepted attachment/text envelope", limit, required)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	request.ContentLength = limit
	var value map[string]any
	if err := decodeBoundedJSON(httptest.NewRecorder(), request, limit, &value); err != nil {
		t.Fatalf("boundary Content-Length was rejected before JSON decode: %v", err)
	}
}

func TestRawRequestSafetyGuardReturnsExplicit413(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	called := false
	handler := server.debugMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	limit, err := requestBodyLimit(defaultTextInputLimitUTF16)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	request.ContentLength = limit + 1
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
	message := strings.ToLower(recorder.Body.String())
	if !strings.Contains(message, "sidecar") || strings.Contains(message, "microsoft") || strings.Contains(message, "context") {
		t.Fatalf("unsafe size error: %s", recorder.Body.String())
	}
}

func TestBoundedJSONReaderRejectsChunkedOversizeBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"value":"too large"}`))
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	var value map[string]any
	err := decodeBoundedJSON(recorder, request, 8, &value)
	if !isRequestBodyTooLarge(err) {
		t.Fatalf("error=%v, want request-too-large", err)
	}
}

func TestResponsesStreamAcceptsSSELineBeyondFormerTwoMiBLimit(t *testing.T) {
	want := strings.Repeat("x", 3<<20)
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: want, ConversationID: "conversation", SessionID: "session"}})
	recorder := httptest.NewRecorder()
	server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"m365-auto","stream":true,"input":"large response"}`)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "event: response.completed") || strings.Contains(recorder.Body.String(), "event: response.failed") {
		t.Fatalf("status=%d response did not complete; bytes=%d", recorder.Code, recorder.Body.Len())
	}
	if got := bytes.Count(recorder.Body.Bytes(), []byte("x")); got < len(want) {
		t.Fatalf("large streamed output was truncated: x count=%d want-at-least=%d", got, len(want))
	}
}

type gatedDebugBody struct {
	allowed *bool
	reader  *strings.Reader
}

func (body *gatedDebugBody) Read(p []byte) (int, error) {
	if !*body.allowed {
		return 0, errors.New("body read before handler")
	}
	return body.reader.Read(p)
}

func (*gatedDebugBody) Close() error { return nil }

func TestInactiveDebugMiddlewareDoesNotPreReadRequestBody(t *testing.T) {
	store := openDebugStoreWithPolicy(filepath.Join(t.TempDir(), "debug.json"), testDebugPolicy())
	server := &Server{debug: store, settings: &settingsStore{v: defaultRuntimeSettings()}}
	allowed := false
	want := `{"model":"m365-auto","messages":[{"role":"user","content":"safe"}]}`
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed = true
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != want {
			t.Fatalf("handler body=%q err=%v", got, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Body = &gatedDebugBody{allowed: &allowed, reader: strings.NewReader(want)}
	request.ContentLength = int64(len(want))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !allowed {
		t.Fatalf("status=%d handler called=%t", recorder.Code, allowed)
	}
}

func minIntForTest(a, b int) int {
	if a < b {
		return a
	}
	return b
}
