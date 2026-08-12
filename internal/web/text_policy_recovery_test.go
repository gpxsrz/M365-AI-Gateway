package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func textPolicyErrorObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode text-policy error: %v body=%s", err, recorder.Body.String())
	}
	errorObject, _ := envelope["error"].(map[string]any)
	if errorObject == nil {
		t.Fatalf("missing error object: %#v", envelope)
	}
	return errorObject
}

func requireCallerTextRecoveryMetadata(t *testing.T, errorObject map[string]any, wantCode string, wantReceived int) {
	t.Helper()
	if got, _ := errorObject["code"].(string); got != wantCode {
		t.Fatalf("code=%q want=%q error=%#v", got, wantCode, errorObject)
	}
	if got, _ := errorObject["limit_type"].(string); got != "caller_text_utf16" {
		t.Fatalf("limit_type=%q error=%#v", got, errorObject)
	}
	limit, ok := errorObject["limit"].(float64)
	if !ok || int(limit) != defaultTextInputLimitUTF16 {
		t.Fatalf("limit=%v want=%d error=%#v", errorObject["limit"], defaultTextInputLimitUTF16, errorObject)
	}
	received, ok := errorObject["received"].(float64)
	if !ok || int(received) != wantReceived {
		t.Fatalf("received=%v want=%d error=%#v", errorObject["received"], wantReceived, errorObject)
	}
	if retryable, _ := errorObject["retryable_after_reduction"].(bool); !retryable {
		t.Fatalf("retryable_after_reduction=%v error=%#v", retryable, errorObject)
	}
	if got, _ := errorObject["recommended_action"].(string); got != "compact_or_split_and_retry" {
		t.Fatalf("recommended_action=%q error=%#v", got, errorObject)
	}
}

func TestGenericTextPolicyErrorsExposeRecoveryMetadataWithoutPretendingTokenOverflow(t *testing.T) {
	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	cases := []struct {
		name string
		call func(*Server, *httptest.ResponseRecorder)
	}{
		{name: "chat_completions", call: func(server *Server, recorder *httptest.ResponseRecorder) {
			server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":`+mustJSON(over)+`}]}`)))
		}},
		{name: "responses", call: func(server *Server, recorder *httptest.ResponseRecorder) {
			server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":`+mustJSON(over)+`}`)))
		}},
		{name: "anthropic_messages", call: func(server *Server, recorder *httptest.ResponseRecorder) {
			server.anthropicMessages(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":`+mustJSON(over)+`}]}`)))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			chat := &wp1CandidateChat{}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			test.call(server, recorder)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			errorObject := textPolicyErrorObject(t, recorder)
			requireCallerTextRecoveryMetadata(t, errorObject, "text_policy_exceeded", defaultTextInputLimitUTF16+1)
			message, _ := errorObject["message"].(string)
			if !strings.Contains(strings.ToLower(message), "utf-16") || strings.Contains(message, "context_length_exceeded") {
				t.Fatalf("generic transport policy was misrepresented as model context overflow: %q", message)
			}
			if len(chat.requests) != 0 {
				t.Fatalf("oversized request reached ChatHub: %d calls", len(chat.requests))
			}
		})
	}
}

func TestMemoryTextPolicyOverflowUsesHindsightRecoverySignalAndTransportMetadata(t *testing.T) {
	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	chat := &wp1CandidateChat{}
	server := newWP1CandidateServer(t, chat)
	server.settings.v.MemoryCompatibilityEnabled = true
	recorder := httptest.NewRecorder()
	server.memoryOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":`+mustJSON(over)+`}]}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	errorObject := textPolicyErrorObject(t, recorder)
	requireCallerTextRecoveryMetadata(t, errorObject, "context_length_exceeded", defaultTextInputLimitUTF16+1)
	message := strings.ToLower(errorObject["message"].(string))
	if !strings.Contains(message, "input is too long") || !strings.Contains(message, "utf-16") {
		t.Fatalf("memory overflow lacks a Hindsight-recognized marker or the real transport policy: %q", message)
	}
	if len(chat.requests) != 0 {
		t.Fatalf("oversized memory request reached ChatHub: %d calls", len(chat.requests))
	}
}

func TestProtocolAdaptersPreserveRecoveryMetadataForLateFlattenedPromptOverflow(t *testing.T) {
	const limit = 500
	systemText := strings.Repeat("s", 200)
	userText := strings.Repeat("u", 200)
	cases := []struct {
		name string
		call func(*Server, *httptest.ResponseRecorder)
	}{
		{name: "responses", call: func(server *Server, recorder *httptest.ResponseRecorder) {
			body := `{"model":"gpt-5.6-sol","instructions":` + mustJSON(systemText) + `,"input":` + mustJSON(userText) + `}`
			server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		}},
		{name: "anthropic_messages", call: func(server *Server, recorder *httptest.ResponseRecorder) {
			body := `{"model":"gpt-5.6-sol","system":` + mustJSON(systemText) + `,"messages":[{"role":"user","content":` + mustJSON(userText) + `}]}`
			server.anthropicMessages(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			chat := &wp1CandidateChat{}
			server := newWP1CandidateServer(t, chat)
			server.settings.v.TextInputLimitUTF16 = limit
			recorder := httptest.NewRecorder()
			test.call(server, recorder)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			errorObject := textPolicyErrorObject(t, recorder)
			if errorObject["code"] != "text_policy_exceeded" || errorObject["limit_type"] != "caller_text_utf16" || int(errorObject["limit"].(float64)) != limit || int(errorObject["received"].(float64)) <= limit {
				t.Fatalf("late transport-policy metadata was lost: %#v", errorObject)
			}
			if len(chat.requests) != 0 {
				t.Fatalf("late policy overflow reached ChatHub: %d calls", len(chat.requests))
			}
		})
	}
}

func TestMemorySchemaRepairOverflowKeepsMemoryRecoveryContractBeforeSecondChatHubCall(t *testing.T) {
	const limit = 1200
	candidate := `{"城市":` + mustJSON(strings.Repeat("台", 900)) + `}`
	chat := &wp1CandidateChat{result: chathub.Result{Text: candidate, ConversationID: "memory-conversation", SessionID: "memory-session"}}
	server := newWP1CandidateServer(t, chat)
	server.settings.v.MemoryCompatibilityEnabled = true
	server.settings.v.TextInputLimitUTF16 = limit
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"我住哪裡"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	recorder := httptest.NewRecorder()
	server.memoryOpenAIChat(recorder, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("repair overflow should stop before a second ChatHub call: calls=%d", len(chat.requests))
	}
	errorObject := textPolicyErrorObject(t, recorder)
	if errorObject["code"] != "context_length_exceeded" || errorObject["limit_type"] != "caller_text_utf16" || int(errorObject["limit"].(float64)) != limit || int(errorObject["received"].(float64)) <= limit {
		t.Fatalf("memory repair overflow contract=%#v", errorObject)
	}
	if !strings.Contains(strings.ToLower(errorObject["message"].(string)), "input is too long") {
		t.Fatalf("memory repair overflow lacks Hindsight recovery marker: %#v", errorObject)
	}
}
