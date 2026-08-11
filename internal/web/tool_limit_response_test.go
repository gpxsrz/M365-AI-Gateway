package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func TestConfiguredLimitRejectsExcessToolCallsInsteadOfTruncating(t *testing.T) {
	calls := []detectedToolCall{{ID: "call_1", Name: "first", Arguments: json.RawMessage(`{"x":1}`)}, {ID: "call_2", Name: "second", Arguments: json.RawMessage(`{"y":2}`)}}
	if err := validateToolCallLimit(calls, 1); err == nil {
		t.Fatal("expected excess tool calls to be rejected")
	}
	if err := validateToolCallLimit(calls, 2); err != nil {
		t.Fatalf("calls within configured limit were rejected: %v", err)
	}
}

func TestToolCallLimitViolationInvalidatesCheckpointContinuation(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:           `{"calls":[{"name":"read_file","arguments":{"path":"a"}},{"name":"search_code","arguments":{"query":"b"}}]}`,
		ConversationID: "conv-overflow",
		SessionID:      "session-overflow",
	}}
	server := newWP1CandidateServer(t, chat)
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	server.settings.v.MaxToolCallsPerTurn = 1
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read file contents.", "parameters": map[string]any{"type": "object"}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "search_code", "description": "Search source code.", "parameters": map[string]any{"type": "object"}}},
	}
	body := map[string]any{
		"model":       "gpt-5.6-sol",
		"session_key": "overflow-session",
		"messages":    []any{map[string]any{"role": "user", "content": "inspect"}},
		"tools":       tools,
	}
	recorder := httptest.NewRecorder()
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(body))), "tool-limit-owner")
	server.openaiChat(recorder, req)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "tool_call_limit_exceeded") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if records := checkpointViewsForTest(t, server.checkpoints); len(records) != 0 {
		t.Fatalf("overflowed upstream conversation remained reusable: %#v", records)
	}
}

func TestToolResponseOmitsGeneratedNarration(t *testing.T) {
	calls := []detectedToolCall{{ID: "call_1", Type: "function", Name: "lookup", Arguments: json.RawMessage(`{"query":"one"}`)}}
	nonStream := httptest.NewRecorder()
	if err := writeToolResponse(nonStream, "chatcmpl_test", "test", false, calls, chathub.Result{}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(nonStream.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	message, _ := openAIChoice(out)
	if message["content"] != nil {
		t.Fatalf("tool response invented visible narration: %#v", message["content"])
	}

	stream := httptest.NewRecorder()
	if err := writeToolResponse(stream, "chatcmpl_test", "test", true, calls, chathub.Result{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stream.Body.String(), `"content":`) {
		t.Fatalf("streamed tool response invented visible narration: %s", stream.Body.String())
	}
}
