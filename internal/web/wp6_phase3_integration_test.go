package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type checkpointIntegrationChat struct {
	requests []chathub.Request
	results  []chathub.Result
	failures map[int]error
}

func (f *checkpointIntegrationChat) next(request chathub.Request) (chathub.Result, error) {
	f.requests = append(f.requests, request)
	if err := f.failures[len(f.requests)]; err != nil {
		return chathub.Result{}, err
	}
	if len(f.results) == 0 {
		return chathub.Result{}, fmt.Errorf("unexpected ChatHub request")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

func (f *checkpointIntegrationChat) Chat(_ context.Context, _ chathub.Account, request chathub.Request) (chathub.Result, error) {
	return f.next(request)
}

func (f *checkpointIntegrationChat) ChatWithDelta(_ context.Context, _ chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	result, err := f.next(request)
	if err == nil && emit != nil && result.Text != "" {
		err = emit(result.Text)
	}
	return result, err
}

func (f *checkpointIntegrationChat) ChatWithEvents(_ context.Context, _ chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result, err := f.next(request)
	if err == nil && emit != nil && result.Text != "" {
		err = emit(chathub.StreamEvent{Kind: "text", Text: result.Text})
	}
	return result, err
}

func phase3Request(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return withAPIKeyOwner(request, "phase3-owner")
}

func phase3Server(t *testing.T, chat *checkpointIntegrationChat, checkpointPath string) *Server {
	t.Helper()
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	store, err := openTransportCheckpointStore(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	server.checkpoints = store
	return server
}

func phase3OnlyCheckpointRecord(t *testing.T, server *Server) transportCheckpointRecord {
	t.Helper()
	server.checkpoints.mu.Lock()
	defer server.checkpoints.mu.Unlock()
	if len(server.checkpoints.records) != 1 {
		t.Fatalf("checkpoint records=%d, want 1", len(server.checkpoints.records))
	}
	for _, record := range server.checkpoints.records {
		return *record
	}
	t.Fatal("checkpoint record missing")
	return transportCheckpointRecord{}
}

func TestWP6LegacyCheckpointPreservesAssistantImageIdentity(t *testing.T) {
	const image = "data:image/png;base64,V1A2"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoints.json")
			chat := &checkpointIntegrationChat{results: []chathub.Result{{
				Text: "A1", Images: []string{image}, ConversationID: "conversation-image", SessionID: "session-image",
			}}}
			server := phase3Server(t, chat, path)
			recorder := httptest.NewRecorder()
			request := phase3Request(http.MethodPost, "/api/chat", `{"model":"gpt-5.6-sol","sessionKey":"image-session","message":"USER-IMAGE"}`)
			if stream {
				request.URL.Path = "/api/chat/stream"
				server.chatStream(recorder, request)
			} else {
				server.chatOnce(recorder, request)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}

			expected, err := canonicalCheckpointMessages([]oaiMsg{
				{Role: "user", Content: "USER-IMAGE"},
				assistantTextCheckpointMessage("A1", []string{image}),
			})
			if err != nil {
				t.Fatal(err)
			}
			record := phase3OnlyCheckpointRecord(t, server)
			if len(record.MessageDigests) != len(expected.digests) {
				t.Fatalf("message digests=%d want=%d", len(record.MessageDigests), len(expected.digests))
			}
			for index := range expected.digests {
				if record.MessageDigests[index] != expected.digests[index] {
					t.Fatalf("message digest %d does not preserve assistant image identity", index)
				}
			}
		})
	}
}

func TestWP6ToolCheckpointPreservesAssistantImages(t *testing.T) {
	const image = "data:image/png;base64,V1A2"
	calls := []detectedToolCall{{ID: "call-image", Type: "function", Name: "lookup", Arguments: []byte(`{"q":"image"}`)}}
	result := chathub.Result{Text: "tool answer", Images: []string{image}}

	actual := assistantToolCheckpointMessage(calls, result, false)
	expected := assistantTextCheckpointMessage("tool answer", []string{image})
	expected.ToolCalls = checkpointToolCalls(calls)
	actualHistory, err := canonicalCheckpointMessages([]oaiMsg{actual})
	if err != nil {
		t.Fatal(err)
	}
	expectedHistory, err := canonicalCheckpointMessages([]oaiMsg{expected})
	if err != nil {
		t.Fatal(err)
	}
	if actualHistory.digests[0] != expectedHistory.digests[0] {
		t.Fatal("tool checkpoint dropped assistant image identity")
	}

	streamed := assistantToolCheckpointMessageWithContent(calls, "visible streamed preamble", []string{image})
	streamedExpected := assistantTextCheckpointMessage("visible streamed preamble", []string{image})
	streamedExpected.ToolCalls = checkpointToolCalls(calls)
	streamedHistory, err := canonicalCheckpointMessages([]oaiMsg{streamed})
	if err != nil {
		t.Fatal(err)
	}
	streamedExpectedHistory, err := canonicalCheckpointMessages([]oaiMsg{streamedExpected})
	if err != nil {
		t.Fatal(err)
	}
	if streamedHistory.digests[0] != streamedExpectedHistory.digests[0] {
		t.Fatal("streamed tool checkpoint dropped assistant image identity")
	}
}

func TestWP6ImageOnlyToolCheckpointMatchesReturnedContent(t *testing.T) {
	const image = "data:image/png;base64,V1A2"
	calls := []detectedToolCall{{ID: "call-image", Type: "function", Name: "lookup", Arguments: []byte(`{"q":"image"}`)}}
	result := chathub.Result{Images: []string{image}}

	recorder := httptest.NewRecorder()
	if err := writeToolResponse(recorder, "chatcmpl-image", "test", false, calls, result); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	returned, _ := openAIChoice(response)
	checkpoint := assistantToolCheckpointMessage(calls, result, false)
	returnedJSON, _ := json.Marshal(returned["content"])
	checkpointJSON, _ := json.Marshal(checkpoint.Content)
	if !bytes.Equal(returnedJSON, checkpointJSON) {
		t.Fatalf("returned content %s does not match checkpoint %s", returnedJSON, checkpointJSON)
	}
}

func TestWP6StreamedWhitespaceToolPreambleKeepsExactCheckpointContent(t *testing.T) {
	calls := []detectedToolCall{{ID: "call-space", Type: "function", Name: "lookup", Arguments: []byte(`{}`)}}
	message := assistantToolCheckpointMessageWithContent(calls, "  ", nil)
	if message.Content != "  " {
		t.Fatalf("streamed whitespace content was not preserved exactly: %#v", message.Content)
	}
}

func TestWP6ToolCheckpointDoesNotInventPreamble(t *testing.T) {
	calls := []detectedToolCall{{ID: "call-1", Type: "function", Name: "lookup", Arguments: []byte(`{"query":"one"}`)}}
	message := assistantToolCheckpointMessage(calls, chathub.Result{}, true)
	if message.Content != nil {
		t.Fatalf("tool checkpoint invented visible preamble: %#v", message.Content)
	}
}

func TestWP6ChatCompletionsExactPrefixSendsSystemAndOnlyNewDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A1", ConversationID: "conversation-a", SessionID: "session-1"},
		{Text: "A2", ConversationID: "conversation-a", SessionID: "session-2"},
		{Text: "A3", ConversationID: "conversation-a", SessionID: "session-3"},
	}}
	server := phase3Server(t, chat, path)

	turns := []string{
		`{"model":"gpt-5.6-sol","messages":[{"role":"system","content":"SYSTEM-POLICY"},{"role":"user","content":"USER-ONE"}]}`,
		`{"model":"gpt-5.6-sol","messages":[{"role":"system","content":"SYSTEM-POLICY"},{"role":"user","content":"USER-ONE"},{"role":"assistant","content":"A1"},{"role":"user","content":"USER-TWO"}]}`,
		`{"model":"gpt-5.6-sol","messages":[{"role":"system","content":"SYSTEM-POLICY"},{"role":"user","content":"USER-ONE"},{"role":"assistant","content":"A1"},{"role":"user","content":"USER-TWO"},{"role":"assistant","content":"A2"},{"role":"user","content":"USER-THREE"}]}`,
	}
	for _, body := range turns {
		recorder := httptest.NewRecorder()
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if len(chat.requests) != 3 {
		t.Fatalf("requests=%d", len(chat.requests))
	}
	if chat.requests[1].ConversationID != "conversation-a" || chat.requests[1].SessionID != "session-1" {
		t.Fatalf("second binding=%q/%q", chat.requests[1].ConversationID, chat.requests[1].SessionID)
	}
	if chat.requests[2].ConversationID != "conversation-a" || chat.requests[2].SessionID != "session-2" {
		t.Fatalf("third binding=%q/%q", chat.requests[2].ConversationID, chat.requests[2].SessionID)
	}
	for index, marker := range []string{"USER-TWO", "USER-THREE"} {
		prompt := chat.requests[index+1].Text
		if !strings.Contains(prompt, "SYSTEM-POLICY") || !strings.Contains(prompt, marker) {
			t.Fatalf("delta prompt %d missing policy/new user: %q", index+2, prompt)
		}
		for _, old := range []string{"USER-ONE", "A1"} {
			if strings.Contains(prompt, old) {
				t.Fatalf("delta prompt %d resent %q: %q", index+2, old, prompt)
			}
		}
		if index == 1 && (strings.Contains(prompt, "USER-TWO") || strings.Contains(prompt, "A2")) {
			t.Fatalf("third prompt resent accepted turn: %q", prompt)
		}
	}
}

func TestWP6CheckpointRestartAndMismatchRebind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	firstChat := &checkpointIntegrationChat{results: []chathub.Result{{Text: "A1", ConversationID: "conversation-old", SessionID: "session-old"}}}
	server := phase3Server(t, firstChat, path)
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"ORIGINAL"}]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	secondChat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A2", ConversationID: "conversation-old", SessionID: "session-new"},
		{Text: "replacement", ConversationID: "conversation-new", SessionID: "session-rebound"},
	}}
	restarted := phase3Server(t, secondChat, path)
	recorder = httptest.NewRecorder()
	restarted.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"ORIGINAL"},{"role":"assistant","content":"A1"},{"role":"user","content":"NEXT"}]}`))
	if recorder.Code != http.StatusOK || secondChat.requests[0].ConversationID != "conversation-old" {
		t.Fatalf("restart status=%d request=%#v body=%s", recorder.Code, secondChat.requests[0], recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	restarted.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"ORIGINA!"},{"role":"assistant","content":"A1"},{"role":"user","content":"NEXT"},{"role":"assistant","content":"A2"},{"role":"user","content":"AFTER-REWRITE"}]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("mismatch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request := secondChat.requests[1]
	if request.ConversationID != "" || request.SessionID != "" {
		t.Fatalf("mismatch reused binding=%q/%q", request.ConversationID, request.SessionID)
	}
	for _, marker := range []string{"ORIGINA!", "A1", "NEXT", "A2", "AFTER-REWRITE"} {
		if !strings.Contains(request.Text, marker) {
			t.Fatalf("rebind prompt missing %q: %q", marker, request.Text)
		}
	}
}

func TestWP6EightTurnDeltaRemainsExactAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{}
	for turn := 1; turn <= 8; turn++ {
		chat.results = append(chat.results, chathub.Result{Text: fmt.Sprintf("A%d", turn), ConversationID: "conversation-eight", SessionID: fmt.Sprintf("session-%d", turn)})
	}
	server := phase3Server(t, chat, path)
	messages := []oaiMsg{{Role: "system", Content: "SYSTEM"}}
	for turn := 1; turn <= 8; turn++ {
		messages = append(messages, oaiMsg{Role: "user", Content: fmt.Sprintf("U%d", turn)})
		body := mustJSON(oaiReq{Model: "gpt-5.6-sol", Messages: messages})
		recorder := httptest.NewRecorder()
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("turn %d status=%d body=%s", turn, recorder.Code, recorder.Body.String())
		}
		if turn > 1 {
			request := chat.requests[turn-1]
			if request.ConversationID != "conversation-eight" || !strings.Contains(request.Text, "SYSTEM") || !strings.Contains(request.Text, fmt.Sprintf("U%d", turn)) {
				t.Fatalf("turn %d request=%#v", turn, request)
			}
			if strings.Contains(request.Text, fmt.Sprintf("U%d", turn-1)) || strings.Contains(request.Text, fmt.Sprintf("A%d", turn-1)) {
				t.Fatalf("turn %d resent accepted history: %q", turn, request.Text)
			}
		}
		messages = append(messages, oaiMsg{Role: "assistant", Content: fmt.Sprintf("A%d", turn)})
	}
}

func TestWP6StreamingTerminalAcceptsBeforeNextDeltaTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A1", ConversationID: "conversation-stream", SessionID: "session-1"},
		{Text: "A2", ConversationID: "conversation-stream", SessionID: "session-2"},
		{Text: "A3", ConversationID: "conversation-stream", SessionID: "session-3"},
	}}
	server := phase3Server(t, chat, path)

	first := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", first))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	second := `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"U1"},{"role":"assistant","content":"A1"},{"role":"user","content":"U2"}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", second))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "data: [DONE]") {
		t.Fatalf("stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if chat.requests[1].ConversationID != "conversation-stream" || chat.requests[1].SessionID != "session-1" || strings.Contains(chat.requests[1].Text, "U1") || strings.Contains(chat.requests[1].Text, "A1") {
		t.Fatalf("stream continuation request=%#v", chat.requests[1])
	}

	third := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"},{"role":"assistant","content":"A1"},{"role":"user","content":"U2"},{"role":"assistant","content":"A2"},{"role":"user","content":"U3"}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", third))
	if recorder.Code != http.StatusOK || chat.requests[2].SessionID != "session-2" {
		t.Fatalf("third status=%d request=%#v body=%s", recorder.Code, chat.requests[2], recorder.Body.String())
	}
}

func TestWP6UpstreamFailureInvalidatesCheckpointAndRetryStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{
		results: []chathub.Result{
			{Text: "A1", ConversationID: "conversation-old", SessionID: "session-1"},
			{Text: "A2", ConversationID: "conversation-new", SessionID: "session-new"},
		},
		failures: map[int]error{2: fmt.Errorf("deterministic upstream failure")},
	}
	server := phase3Server(t, chat, path)
	first := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", first))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	continuation := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"},{"role":"assistant","content":"A1"},{"role":"user","content":"U2"}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", continuation))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("failed turn status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("failed turn retained uncertain checkpoint: %#v", got)
	}

	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", continuation))
	if recorder.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	retry := chat.requests[2]
	if retry.ConversationID != "" || retry.SessionID != "" {
		t.Fatalf("retry reused uncertain binding: %#v", retry)
	}
	for _, marker := range []string{"U1", "A1", "U2"} {
		if !strings.Contains(retry.Text, marker) {
			t.Fatalf("fresh retry omitted active context %q: %q", marker, retry.Text)
		}
	}
}

func TestWP6ExactDeltaPreservesNewAttachments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A1", ConversationID: "conversation-attachment", SessionID: "session-1"},
		{Text: "A2", ConversationID: "conversation-attachment", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)
	first := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", first))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	second := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"U1"},{"role":"assistant","content":"A1"},{"role":"user","content":[{"type":"text","text":"U2"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", second))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request := chat.requests[1]
	if request.ConversationID != "conversation-attachment" || strings.Contains(request.Text, "U1") || !strings.Contains(request.Text, "U2") {
		t.Fatalf("attachment delta request=%#v", request)
	}
	if len(request.Attachments) != 1 || request.Attachments[0].URL != "data:image/png;base64,QUJD" {
		t.Fatalf("new attachment was not forwarded exactly: %#v", request.Attachments)
	}
}

func TestWP6ExactDeltaOmitsHistoricalAttachmentsAndReusesConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A1", ConversationID: "conversation-historical-attachment", SessionID: "session-1"},
		{Text: "A2", ConversationID: "conversation-historical-attachment", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)
	historical := `[{"type":"text","text":"U1"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]`
	first := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":` + historical + `}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", first))
	if recorder.Code != http.StatusOK || len(chat.requests) != 1 || len(chat.requests[0].Attachments) != 1 {
		t.Fatalf("first status=%d requests=%#v body=%s", recorder.Code, chat.requests, recorder.Body.String())
	}

	second := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":` + historical + `},{"role":"assistant","content":"A1"},{"role":"user","content":"U2"}]}`
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", second))
	if recorder.Code != http.StatusOK || len(chat.requests) != 2 {
		t.Fatalf("second status=%d requests=%#v body=%s", recorder.Code, chat.requests, recorder.Body.String())
	}
	request := chat.requests[1]
	if request.ConversationID != "conversation-historical-attachment" || request.SessionID != "session-1" {
		t.Fatalf("historical attachment lost continuity: %#v", request)
	}
	if len(request.Attachments) != 0 || strings.Contains(request.Text, "U1") || !strings.Contains(request.Text, "U2") {
		t.Fatalf("historical attachment/content was resent: %#v", request)
	}
}

func TestWP6TopLevelAttachmentsAreAlwaysForwardedAsCurrentTurnAnnotations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "A1", ConversationID: "conversation-top-attachment", SessionID: "session-1"},
		{Text: "A2", ConversationID: "conversation-top-attachment", SessionID: "session-2"},
		{Text: "A3", ConversationID: "conversation-top-attachment", SessionID: "session-3"},
	}}
	server := phase3Server(t, chat, path)
	messages := []oaiMsg{{Role: "user", Content: "U1"}}
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", mustJSON(oaiReq{Model: "gpt-5.6-sol", Messages: messages})))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	messages = append(messages, oaiMsg{Role: "assistant", Content: "A1"}, oaiMsg{Role: "user", Content: "U2"})
	for index, attachment := range []chathub.Attachment{
		{Type: "file", Name: "first.txt", URL: "data:text/plain;base64,QQ==", MimeType: "text/plain"},
		{Type: "file", Name: "second.txt", URL: "data:text/plain;base64,Qg==", MimeType: "text/plain"},
	} {
		recorder = httptest.NewRecorder()
		body := oaiReq{Model: "gpt-5.6-sol", Messages: messages, Attachments: []chathub.Attachment{attachment}}
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", mustJSON(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("attachment turn %d status=%d body=%s", index+1, recorder.Code, recorder.Body.String())
		}
		request := chat.requests[index+1]
		if request.ConversationID != "conversation-top-attachment" || len(request.Attachments) != 1 || request.Attachments[0] != attachment {
			t.Fatalf("attachment turn %d request=%#v", index+1, request)
		}
		messages = append(messages, oaiMsg{Role: "assistant", Content: fmt.Sprintf("A%d", index+2)}, oaiMsg{Role: "user", Content: fmt.Sprintf("U%d", index+3)})
	}
}

func TestWP6PublicDecodePreservesLargeStructuredNumberIdentity(t *testing.T) {
	decode := func(number string) checkpointHistory {
		t.Helper()
		body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":{"number":` + number + `}}]}`
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		var decoded oaiReq
		if err := decodeBoundedJSON(httptest.NewRecorder(), request, requestBodySafetyBytes, &decoded); err != nil {
			t.Fatal(err)
		}
		history, err := canonicalCheckpointMessages(decoded.Messages)
		if err != nil {
			t.Fatal(err)
		}
		return history
	}
	first := decode("9007199254740992")
	second := decode("9007199254740993")
	if equalStrings(first.digests, second.digests) {
		t.Fatalf("large structured integers collided: %#v %#v", first, second)
	}
}

func TestWP6ConversationDriftFailsClosedButSessionRotationSucceeds(t *testing.T) {
	server := phase3Server(t, &checkpointIntegrationChat{}, filepath.Join(t.TempDir(), "checkpoints.json"))
	body := oaiReq{Messages: []oaiMsg{{Role: "user", Content: "drift"}}}
	checkpointContext := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), "owner").Context()
	turn, err := server.beginOpenAICheckpoint(checkpointContext, &body)
	if err != nil {
		t.Fatal(err)
	}
	turn.Observe(chathub.Result{ConversationID: "conversation-a", SessionID: "session-1"})
	turn.Observe(chathub.Result{ConversationID: "conversation-b", SessionID: "session-2"})
	if err := turn.Accept(oaiMsg{Role: "assistant", Content: "answer"}); !errors.Is(err, ErrCheckpointConversationDrift) {
		t.Fatalf("conversation drift error = %v", err)
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("conversation drift retained checkpoint: %#v", got)
	}

	body = oaiReq{Messages: []oaiMsg{{Role: "user", Content: "rotation"}}}
	turn, err = server.beginOpenAICheckpoint(checkpointContext, &body)
	if err != nil {
		t.Fatal(err)
	}
	turn.Observe(chathub.Result{ConversationID: "conversation-stable", SessionID: "session-1"})
	turn.Observe(chathub.Result{ConversationID: "conversation-stable", SessionID: "session-2"})
	if err := turn.Accept(oaiMsg{Role: "assistant", Content: "answer"}); err != nil {
		t.Fatal(err)
	}
	views := checkpointViewsForTest(t, server.checkpoints)
	if len(views) != 1 || views[0].ConversationID != "conversation-stable" || views[0].SessionID != "session-2" {
		t.Fatalf("session rotation checkpoint = %#v", views)
	}
}

type phase3NoFlushWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *phase3NoFlushWriter) Header() http.Header            { return w.header }
func (w *phase3NoFlushWriter) WriteHeader(status int)         { w.status = status }
func (w *phase3NoFlushWriter) Write(body []byte) (int, error) { return w.body.Write(body) }

func TestWP6LegacyStreamWithoutFlusherDoesNotAdvanceCheckpoint(t *testing.T) {
	chat := &checkpointIntegrationChat{results: []chathub.Result{{Text: "unused", ConversationID: "conversation", SessionID: "session"}}}
	server := phase3Server(t, chat, filepath.Join(t.TempDir(), "checkpoints.json"))
	writer := &phase3NoFlushWriter{header: make(http.Header)}
	server.chatStream(writer, phase3Request(http.MethodPost, "/api/chat/stream", `{"model":"gpt-5.6-sol","sessionKey":"key","message":"hello"}`))
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", writer.status, writer.body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("non-streamable writer reached upstream: %#v", chat.requests)
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("non-streamable writer advanced checkpoint: %#v", got)
	}
}

type phase3BlockingChat struct {
	started chan struct{}
	release chan struct{}
}

func (chat *phase3BlockingChat) Chat(_ context.Context, _ chathub.Account, _ chathub.Request) (chathub.Result, error) {
	close(chat.started)
	<-chat.release
	return chathub.Result{Text: "answer", ConversationID: "old-account-conversation", SessionID: "session"}, nil
}
func (chat *phase3BlockingChat) ChatWithDelta(ctx context.Context, account chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	return chat.Chat(ctx, account, request)
}
func (chat *phase3BlockingChat) ChatWithEvents(ctx context.Context, account chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	return chat.Chat(ctx, account, request)
}

func TestWP6AccountLifecycleWaitsForInflightCheckpointThenClearsIt(t *testing.T) {
	blocking := &phase3BlockingChat{started: make(chan struct{}), release: make(chan struct{})}
	server := newAdminSecurityServer(t, "administrator-password")
	if _, err := server.tokens.Upsert(testTokenSet("mode-change")); err != nil {
		t.Fatal(err)
	}
	server.chat = blocking
	server.checkpoints = openCheckpointForTest(t)
	chatDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"old account turn"}]}`))
		chatDone <- recorder
	}()
	<-blocking.started
	logoutDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.logoutSingleAccount(recorder, httptest.NewRequest(http.MethodPost, "/api/account/logout", nil))
		logoutDone <- recorder
	}()
	select {
	case <-logoutDone:
		t.Fatal("logout crossed an in-flight checkpoint lifecycle")
	case <-time.After(50 * time.Millisecond):
	}
	close(blocking.release)
	if recorder := <-chatDone; recorder.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := <-logoutDone; recorder.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("account lifecycle retained old checkpoint: %#v", got)
	}
}

func TestWP6ChatModeChangeWaitsForInflightCheckpointThenClearsIt(t *testing.T) {
	blocking := &phase3BlockingChat{started: make(chan struct{}), release: make(chan struct{})}
	server := newAdminSecurityServer(t, "administrator-password")
	if _, err := server.tokens.Upsert(testTokenSet("mode-change")); err != nil {
		t.Fatal(err)
	}
	server.chat = blocking
	server.checkpoints = openCheckpointForTest(t)
	chatDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"private turn"}]}`))
		chatDone <- recorder
	}()
	<-blocking.started
	settingsDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		settings := server.settings.get()
		settings.ChatMode = chatModeNormal
		recorder := httptest.NewRecorder()
		server.adminSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(mustJSON(settings))))
		settingsDone <- recorder
	}()
	select {
	case <-settingsDone:
		t.Fatal("chat mode change crossed an in-flight checkpoint lifecycle")
	case <-time.After(50 * time.Millisecond):
	}
	close(blocking.release)
	if recorder := <-chatDone; recorder.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := <-settingsDone; recorder.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if server.settings.get().ChatMode != chatModeNormal {
		t.Fatalf("chat mode=%q", server.settings.get().ChatMode)
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("mode change retained private checkpoint: %#v", got)
	}
}

func TestWP6ResponsesPreviousResponseUsesOpaqueCheckpointWithoutStoredHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "R1", ConversationID: "conversation-responses", SessionID: "session-1"},
		{Text: "R2", ConversationID: "conversation-responses", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)
	recorder := httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", `{"model":"gpt-5.6-sol","input":"RESPONSES-ONE"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	first := wp1DecodeJSON(t, recorder.Body.String())
	responseID, _ := first["id"].(string)
	if !strings.HasPrefix(responseID, "resp_") {
		t.Fatalf("response id=%q", responseID)
	}

	restarted := phase3Server(t, chat, path)
	recorder = httptest.NewRecorder()
	body := fmt.Sprintf(`{"model":"gpt-5.6-sol","previous_response_id":%q,"input":"RESPONSES-TWO"}`, responseID)
	restarted.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("continuation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("requests=%d", len(chat.requests))
	}
	request := chat.requests[1]
	if request.ConversationID != "conversation-responses" || request.SessionID != "session-1" {
		t.Fatalf("continuation binding=%q/%q", request.ConversationID, request.SessionID)
	}
	if !strings.Contains(request.Text, "RESPONSES-TWO") || strings.Contains(request.Text, "RESPONSES-ONE") || strings.Contains(request.Text, "R1") {
		t.Fatalf("continuation prompt=%q", request.Text)
	}

	recorder = httptest.NewRecorder()
	restarted.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", `{"model":"gpt-5.6-sol","previous_response_id":"resp_unknown","input":"NOPE"}`))
	if recorder.Code != http.StatusBadRequest || len(chat.requests) != 2 {
		t.Fatalf("unknown parent status=%d calls=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
	}
}

func TestWP6AnthropicActiveHistoryUsesExactDeltaCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "ANTHROPIC-A1", ConversationID: "conversation-anthropic", SessionID: "session-1"},
		{Text: "ANTHROPIC-A2", ConversationID: "conversation-anthropic", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)
	first := `{"model":"gpt-5.6-sol","system":"ANTHROPIC-SYSTEM","messages":[{"role":"user","content":"ANTHROPIC-U1"}],"max_tokens":64}`
	recorder := httptest.NewRecorder()
	server.anthropicMessages(recorder, phase3Request(http.MethodPost, "/v1/messages", first))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	second := `{"model":"gpt-5.6-sol","system":"ANTHROPIC-SYSTEM","messages":[{"role":"user","content":"ANTHROPIC-U1"},{"role":"assistant","content":"ANTHROPIC-A1"},{"role":"user","content":"ANTHROPIC-U2"}],"max_tokens":64}`
	recorder = httptest.NewRecorder()
	server.anthropicMessages(recorder, phase3Request(http.MethodPost, "/v1/messages", second))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request := chat.requests[1]
	if request.ConversationID != "conversation-anthropic" || request.SessionID != "session-1" || !strings.Contains(request.Text, "ANTHROPIC-SYSTEM") || !strings.Contains(request.Text, "ANTHROPIC-U2") {
		t.Fatalf("continuation request=%#v", request)
	}
	if strings.Contains(request.Text, "ANTHROPIC-U1") || strings.Contains(request.Text, "ANTHROPIC-A1") {
		t.Fatalf("accepted Anthropic history resent: %q", request.Text)
	}
}

func TestWP6LegacySessionKeyUsesAppendOnlyCheckpointAndIgnoresRawBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "L1", ConversationID: "conversation-legacy", SessionID: "session-1"},
		{Text: "L2", ConversationID: "conversation-legacy", SessionID: "session-2"},
		{Text: "fresh", ConversationID: "conversation-fresh", SessionID: "session-fresh"},
	}}
	server := phase3Server(t, chat, path)
	for _, body := range []string{
		`{"model":"gpt-5.6-sol","sessionKey":"legacy-key","message":"LEGACY-ONE","conversationId":"caller-conversation","sessionId":"caller-session"}`,
		`{"model":"gpt-5.6-sol","sessionKey":"legacy-key","message":"LEGACY-TWO"}`,
		`{"model":"gpt-5.6-sol","message":"NO-KEY","conversationId":"caller-conversation","sessionId":"caller-session"}`,
	} {
		recorder := httptest.NewRecorder()
		server.chatOnce(recorder, phase3Request(http.MethodPost, "/api/chat", body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if chat.requests[0].ConversationID != "" || chat.requests[0].SessionID != "" {
		t.Fatalf("first turn trusted caller binding: %#v", chat.requests[0])
	}
	if chat.requests[1].ConversationID != "conversation-legacy" || chat.requests[1].SessionID != "session-1" || chat.requests[1].Text != "LEGACY-TWO" {
		t.Fatalf("legacy continuation=%#v", chat.requests[1])
	}
	if chat.requests[2].ConversationID != "" || chat.requests[2].SessionID != "" {
		t.Fatalf("unkeyed turn trusted caller binding: %#v", chat.requests[2])
	}
}

func TestWP6ResponsesToolResultUsesCheckpointedCallIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: `{"calls":[{"name":"lookup","arguments":{"query":"phase3"}}]}`, ConversationID: "conversation-tool", SessionID: "session-1"},
		{Text: "TOOL-FINAL", ConversationID: "public-conversation-tool", SessionID: "public-session-2"},
	}}
	server := phase3Server(t, chat, path)
	firstBody := `{"model":"gpt-5.6-sol","input":"USE-LOOKUP","tools":[{"type":"function","name":"lookup","description":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}]}`
	recorder := httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", firstBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	first := wp1DecodeJSON(t, recorder.Body.String())
	responseID, _ := first["id"].(string)
	output, _ := first["output"].([]any)
	callID := ""
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call" {
			callID, _ = item["call_id"].(string)
		}
	}
	if callID == "" {
		t.Fatalf("function call missing: %#v", first)
	}

	secondBody := fmt.Sprintf(`{"model":"gpt-5.6-sol","previous_response_id":%q,"input":[{"type":"function_call_output","call_id":%q,"output":"TOOL-RESULT-MARKER"}]}`, responseID, callID)
	recorder = httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", secondBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("continuation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("requests=%d", len(chat.requests))
	}
	request := chat.requests[1]
	if request.ConversationID != "" || request.SessionID != "" || !strings.Contains(request.Text, "TOOL-RESULT-MARKER") || !strings.Contains(request.Text, "USE-LOOKUP") {
		t.Fatalf("tool continuation request=%#v", request)
	}
	if !strings.Contains(request.Text, callID) || !strings.Contains(request.Text, `"role":"assistant"`) {
		t.Fatalf("tool continuation did not replay caller-visible tool identity: %#v", request)
	}
}

func TestWP6StreamingResponsesCommitsOpaqueCursorAtTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "STREAM-R1", ConversationID: "conversation-responses-stream", SessionID: "session-1"},
		{Text: "STREAM-R2", ConversationID: "conversation-responses-stream", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)
	recorder := httptest.NewRecorder()
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", `{"model":"gpt-5.6-sol","stream":true,"input":"STREAM-U1"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	completed := wp1ResponsesCompleted(t, recorder.Body.String())
	responseID, _ := completed["id"].(string)
	if !strings.HasPrefix(responseID, "resp_") {
		t.Fatalf("response id=%q", responseID)
	}

	recorder = httptest.NewRecorder()
	body := fmt.Sprintf(`{"model":"gpt-5.6-sol","previous_response_id":%q,"input":"STREAM-U2"}`, responseID)
	server.responses(recorder, phase3Request(http.MethodPost, "/v1/responses", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("continuation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 2 || chat.requests[1].ConversationID != "conversation-responses-stream" || chat.requests[1].SessionID != "session-1" || strings.Contains(chat.requests[1].Text, "STREAM-U1") || !strings.Contains(chat.requests[1].Text, "STREAM-U2") {
		t.Fatalf("requests=%#v", chat.requests)
	}
}

func TestWP6HermesReloadedToolResultNameKeepsExactConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: `{"calls":[{"name":"lookup","arguments":{"query":"delta"}}]}`, ConversationID: "conversation-tool-chat", SessionID: "session-1"},
		{Text: "TOOL-ANSWER", ConversationID: "public-conversation-tool-chat", SessionID: "public-session-2"},
		{Text: "AFTER-TOOL-ANSWER", ConversationID: "public-conversation-tool-chat", SessionID: "public-session-3"},
	}}
	server := phase3Server(t, chat, path)
	tool := chathub.Tool{Type: "function", Function: json.RawMessage(`{"name":"lookup","description":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}`)}
	firstMessages := []oaiMsg{{Role: "user", Content: "CALL-LOOKUP"}}
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", mustJSON(oaiReq{Model: "gpt-5.6-sol", Messages: firstMessages, Tools: []chathub.Tool{tool}})))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	response := wp1DecodeJSON(t, recorder.Body.String())
	message, _ := openAIChoice(response)
	encodedMessage, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var assistant oaiMsg
	if err := json.Unmarshal(encodedMessage, &assistant); err != nil {
		t.Fatal(err)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant=%#v", assistant)
	}
	callID, _ := assistant.ToolCalls[0]["id"].(string)
	secondMessages := append(append([]oaiMsg{}, firstMessages...), assistant, oaiMsg{Role: "tool", Name: "lookup", ToolCallID: callID, Content: "TOOL-RESULT-DELTA"})
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", mustJSON(oaiReq{Model: "gpt-5.6-sol", Messages: secondMessages})))
	if recorder.Code != http.StatusOK {
		t.Fatalf("continuation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("requests=%d", len(chat.requests))
	}
	request := chat.requests[1]
	if request.ConversationID != "" || request.SessionID != "" || !strings.Contains(request.Text, "TOOL-RESULT-DELTA") || !strings.Contains(request.Text, "CALL-LOOKUP") || !strings.Contains(request.Text, callID) {
		t.Fatalf("tool result continuation=%#v", request)
	}
	secondResponse := wp1DecodeJSON(t, recorder.Body.String())
	secondMessage, _ := openAIChoice(secondResponse)
	secondEncoded, err := json.Marshal(secondMessage)
	if err != nil {
		t.Fatal(err)
	}
	var secondAssistant oaiMsg
	if err := json.Unmarshal(secondEncoded, &secondAssistant); err != nil {
		t.Fatal(err)
	}
	reloadedMessages := append([]oaiMsg{}, secondMessages...)
	reloadedMessages[len(reloadedMessages)-1].Name = ""
	thirdMessages := append(append(reloadedMessages, secondAssistant), oaiMsg{Role: "user", Content: "AFTER-TOOL-USER"})
	recorder = httptest.NewRecorder()
	server.openaiChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", mustJSON(oaiReq{Model: "gpt-5.6-sol", Messages: thirdMessages})))
	if recorder.Code != http.StatusOK {
		t.Fatalf("third status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	thirdRequest := chat.requests[2]
	if thirdRequest.ConversationID != "public-conversation-tool-chat" || thirdRequest.SessionID != "public-session-2" || !strings.Contains(thirdRequest.Text, "AFTER-TOOL-USER") {
		t.Fatalf("third continuation=%#v", thirdRequest)
	}
	for _, historical := range []string{"CALL-LOOKUP", "TOOL-RESULT-DELTA", "TOOL-ANSWER"} {
		if strings.Contains(thirdRequest.Text, historical) {
			t.Fatalf("third continuation replayed accepted %q: %q", historical, thirdRequest.Text)
		}
	}
}
