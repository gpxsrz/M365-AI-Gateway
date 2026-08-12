package web

import (
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatCompletionsTextPolicyAppliesToCheckpointOutboundDelta(t *testing.T) {
	oldText := strings.Repeat("a", 70_000)
	newText := strings.Repeat("b", 70_000)
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:           "ok",
		ConversationID: "conversation-1",
		SessionID:      "session-1",
		RequestID:      "request-1",
	}}
	server := newWP1CandidateServer(t, chat)
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}

	first := `{"model":"gpt-5.6-reasoning","session_key":"text-policy-delta","messages":[{"role":"user","content":` + mustJSON(oldText) + `}]}`
	firstRecorder := httptest.NewRecorder()
	server.openaiChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(first)), "owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	second := `{"model":"gpt-5.6-reasoning","session_key":"text-policy-delta","messages":[` +
		`{"role":"user","content":` + mustJSON(oldText) + `},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + mustJSON(newText) + `}]}`
	secondRecorder := httptest.NewRecorder()
	server.openaiChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(second)), "owner"))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("continued status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream calls=%d want 2", len(chat.requests))
	}
	if strings.Contains(chat.requests[1].Text, strings.Repeat("a", 128)) || !strings.Contains(chat.requests[1].Text, strings.Repeat("b", 128)) {
		t.Fatalf("second upstream request did not contain only the checkpoint delta: len=%d", len(chat.requests[1].Text))
	}
}

func TestAnthropicTextPolicyAppliesToCheckpointOutboundDelta(t *testing.T) {
	oldText := strings.Repeat("a", 70_000)
	newText := strings.Repeat("b", 70_000)
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	chat := &checkpointIntegrationChat{results: []chathub.Result{
		{Text: "ok", ConversationID: "conversation-anthropic", SessionID: "session-1"},
		{Text: "done", ConversationID: "conversation-anthropic", SessionID: "session-2"},
	}}
	server := phase3Server(t, chat, path)

	first := `{"model":"gpt-5.6-reasoning","system":"policy","messages":[{"role":"user","content":` + mustJSON(oldText) + `}],"max_tokens":64}`
	firstRecorder := httptest.NewRecorder()
	server.anthropicMessages(firstRecorder, phase3Request(http.MethodPost, "/v1/messages", first))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	second := `{"model":"gpt-5.6-reasoning","system":"policy","messages":[` +
		`{"role":"user","content":` + mustJSON(oldText) + `},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + mustJSON(newText) + `}],"max_tokens":64}`
	secondRecorder := httptest.NewRecorder()
	server.anthropicMessages(secondRecorder, phase3Request(http.MethodPost, "/v1/messages", second))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("continued status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream calls=%d want 2", len(chat.requests))
	}
	if chat.requests[1].ConversationID != "conversation-anthropic" || strings.Contains(chat.requests[1].Text, strings.Repeat("a", 128)) || !strings.Contains(chat.requests[1].Text, strings.Repeat("b", 128)) {
		t.Fatalf("Anthropic continuation did not use the existing binding and outbound delta: conversation=%q len=%d", chat.requests[1].ConversationID, len(chat.requests[1].Text))
	}
}

func TestChatCompletionsTextPolicyStillRejectsOversizedCheckpointDelta(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:           "ok",
		ConversationID: "conversation-1",
		SessionID:      "session-1",
		RequestID:      "request-1",
	}}
	server := newWP1CandidateServer(t, chat)
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}

	first := `{"model":"gpt-5.6-reasoning","session_key":"text-policy-delta","messages":[{"role":"user","content":"first"}]}`
	firstRecorder := httptest.NewRecorder()
	server.openaiChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(first)), "owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	second := `{"model":"gpt-5.6-reasoning","session_key":"text-policy-delta","messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + mustJSON(over) + `}]}`
	secondRecorder := httptest.NewRecorder()
	server.openaiChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(second)), "owner"))
	if secondRecorder.Code != http.StatusBadRequest || wp1ErrorCode(t, secondRecorder) != "text_policy_exceeded" {
		t.Fatalf("oversized delta status=%d code=%q body=%s", secondRecorder.Code, wp1ErrorCode(t, secondRecorder), secondRecorder.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("oversized delta reached upstream: calls=%d", len(chat.requests))
	}

	retry := `{"model":"gpt-5.6-reasoning","session_key":"text-policy-delta","messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"smaller retry"}]}`
	retryRecorder := httptest.NewRecorder()
	server.openaiChat(retryRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(retry)), "owner"))
	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retryRecorder.Code, retryRecorder.Body.String())
	}
	if len(chat.requests) != 2 || chat.requests[1].ConversationID != "conversation-1" {
		t.Fatalf("policy rejection discarded the existing checkpoint binding: calls=%d conversation=%q", len(chat.requests), chat.requests[len(chat.requests)-1].ConversationID)
	}
}

func TestHermesTextPolicyMapsOversizedCheckpointDeltaToRecoverableContextCode(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:           "ok",
		ConversationID: "conversation-hermes",
		SessionID:      "session-hermes",
		RequestID:      "request-hermes",
	}}
	server := newWP1CandidateServer(t, chat)
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}

	first := `{"model":"gpt-5.6-reasoning","session_key":"hermes-text-policy-delta","messages":[{"role":"user","content":"first"}]}`
	firstRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(first)), "owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	second := `{"model":"gpt-5.6-reasoning","session_key":"hermes-text-policy-delta","messages":[` +
		`{"role":"user","content":"first"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + mustJSON(over) + `}]}`
	secondRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(second)), "owner"))
	if secondRecorder.Code != http.StatusBadRequest || wp1ErrorCode(t, secondRecorder) != "context_length_exceeded" {
		t.Fatalf("oversized Hermes delta status=%d code=%q body=%s", secondRecorder.Code, wp1ErrorCode(t, secondRecorder), secondRecorder.Body.String())
	}
	hermesError := strings.ToLower(secondRecorder.Body.String())
	if !strings.Contains(hermesError, "input is too long") || !strings.Contains(hermesError, "utf-16") {
		t.Fatalf("Hermes delta error lacks the recoverable marker or real UTF-16 policy: %s", secondRecorder.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("oversized Hermes delta reached upstream: calls=%d", len(chat.requests))
	}
}
