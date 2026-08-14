package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func toolRoundHistory(rounds int) []map[string]any {
	messages := []map[string]any{{"role": "user", "content": "Complete this long-running task."}}
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("call-%02d", i+1)
		messages = append(messages,
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      "inspect_step",
						"arguments": fmt.Sprintf(`{"step":%d}`, i+1),
					},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": id, "content": fmt.Sprintf("step %d complete", i+1)},
		)
	}
	return messages
}

func repeatedToolRoundHistory(rounds int) []map[string]any {
	messages := []map[string]any{{"role": "user", "content": "Poll until this impossible condition changes."}}
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("poll-%03d", i+1)
		messages = append(messages,
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []map[string]any{{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      "poll_status",
						"arguments": `{"id":1}`,
					},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": id, "content": "still pending"},
		)
	}
	return messages
}

func requestWithMessages(t *testing.T, path string, messages []map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "gpt-5.6-reasoning",
		"messages": messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
}

func requestWithToolRounds(t *testing.T, path string, rounds int) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "gpt-5.6-reasoning",
		"messages": toolRoundHistory(rounds),
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
}

func decodeToolRoundError(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v body=%s", err, recorder.Body.String())
	}
	errorBody, _ := envelope["error"].(map[string]any)
	if errorBody == nil {
		t.Fatalf("missing error object: %#v", envelope)
	}
	return errorBody
}

func TestAgentLedgerNonPositiveRoundLimitFallsBackToGenericSixteen(t *testing.T) {
	ledger := agentLedger{ToolRounds: 16}
	if err := ledger.CanContinue(0); err == nil || !strings.Contains(err.Error(), "tool round limit reached: 16") {
		t.Fatalf("non-positive limit did not fail closed to 16 rounds: %v", err)
	}
}

func TestHermesSixteenCompletedToolRoundsCanContinue(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "LONG_TASK_CONTINUES", ConversationID: "conversation", SessionID: "session"}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.hermesOpenAIChat(recorder, requestWithToolRounds(t, "/hermes/v1/chat/completions", 16))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("ChatHub requests=%d want=1", len(chat.requests))
	}
}

func TestGenericSixteenCompletedToolRoundsRemainTerminal(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "unexpected"}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.interactiveOpenAIChat(recorder, requestWithToolRounds(t, "/v1/chat/completions", 16))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("ChatHub requests=%d want=0", len(chat.requests))
	}
	errorBody := decodeToolRoundError(t, recorder)
	if errorBody["profile"] != "generic" || errorBody["limit"] != float64(16) || errorBody["completed_rounds"] != float64(16) || errorBody["retryable"] != false {
		t.Fatalf("generic round-limit metadata=%#v", errorBody)
	}
}

func TestMemorySixteenCompletedToolRoundsRemainTerminal(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "unexpected"}}
	server := newWP1CandidateServer(t, chat)
	server.settings.v.MemoryCompatibilityEnabled = true
	recorder := httptest.NewRecorder()
	server.memoryOpenAIChat(recorder, requestWithToolRounds(t, "/memory/v1/chat/completions", 16))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("ChatHub requests=%d want=0", len(chat.requests))
	}
	errorBody := decodeToolRoundError(t, recorder)
	if errorBody["profile"] != "memory" || errorBody["limit"] != float64(16) || errorBody["completed_rounds"] != float64(16) || errorBody["retryable"] != false {
		t.Fatalf("memory round-limit metadata=%#v", errorBody)
	}
}

func responsesToolRoundInput(rounds int) []any {
	input := []any{map[string]any{"role": "user", "content": "Complete this long-running task."}}
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("resp-call-%02d", i+1)
		input = append(input,
			map[string]any{"type": "function_call", "call_id": id, "name": "inspect_step", "arguments": fmt.Sprintf(`{"step":%d}`, i+1)},
			map[string]any{"type": "function_call_output", "call_id": id, "output": fmt.Sprintf("step %d complete", i+1)},
		)
	}
	return input
}

func anthropicToolRoundMessages(rounds int) []any {
	messages := []any{map[string]any{"role": "user", "content": "Complete this long-running task."}}
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("anth-call-%02d", i+1)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "inspect_step", "input": map[string]any{"step": i + 1}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": fmt.Sprintf("step %d complete", i+1)}}},
		)
	}
	return messages
}

func TestResponsesAndAnthropicKeepGenericSixteenRoundLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Server, *httptest.ResponseRecorder)
	}{
		{
			name: "responses",
			run: func(server *Server, recorder *httptest.ResponseRecorder) {
				body, _ := json.Marshal(map[string]any{"model": "gpt-5.6-reasoning", "input": responsesToolRoundInput(16)})
				server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)))
			},
		},
		{
			name: "anthropic",
			run: func(server *Server, recorder *httptest.ResponseRecorder) {
				body, _ := json.Marshal(map[string]any{"model": "gpt-5.6-reasoning", "max_tokens": 64, "messages": anthropicToolRoundMessages(16)})
				server.anthropicMessages(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "unexpected"}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			tc.run(server, recorder)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if len(chat.requests) != 0 {
				t.Fatalf("ChatHub requests=%d want=0", len(chat.requests))
			}
			errorBody := decodeToolRoundError(t, recorder)
			if errorBody["profile"] != "generic" || errorBody["limit"] != float64(16) || errorBody["completed_rounds"] != float64(16) || errorBody["retryable"] != false {
				t.Fatalf("%s round-limit metadata=%#v", tc.name, errorBody)
			}
		})
	}
}

func anyMessages(messages []map[string]any) []any {
	out := make([]any, len(messages))
	for i := range messages {
		out[i] = messages[i]
	}
	return out
}

func TestHermesBeyondSixteenRoundsPreservesCheckpointToolIdentity(t *testing.T) {
	first := chathub.Result{Text: `{"calls":[{"name":"inspect_step","arguments":{"step":17}}]}`, ConversationID: "conversation-long", SessionID: "session-17"}
	second := chathub.Result{Text: `{"calls":[],"answer":"LONG_TASK_DONE"}`, ConversationID: "conversation-long", SessionID: "session-18"}
	final := chathub.Result{Text: "LONG_TASK_DONE", ConversationID: "public-conversation-long", SessionID: "public-session-18"}
	chat := &wp6Phase5SequenceChat{results: []chathub.Result{first, second, final}}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	server.checkpoints, _ = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	settings := server.settings.get()
	settings.ToolPlanningMode = "router"
	server.settings.v = settings
	tool := map[string]any{"type": "function", "function": map[string]any{
		"name":        "inspect_step",
		"description": "Inspect the next step without changing external state.",
		"parameters": map[string]any{
			"type":       "object",
			"required":   []any{"step"},
			"properties": map[string]any{"step": map[string]any{"type": "integer"}},
		},
	}}
	firstBody := map[string]any{
		"model":       "gpt-5.6-sol",
		"session_key": "issue49-long-task",
		"messages":    anyMessages(toolRoundHistory(16)),
		"tools":       []any{tool},
	}
	firstRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(mustJSON(firstBody))), "issue49-owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	firstOut := wp1DecodeJSON(t, firstRecorder.Body.String())
	firstMessage, _ := openAIChoice(firstOut)
	calls, _ := firstMessage["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("first tool_calls=%#v response=%#v", firstMessage["tool_calls"], firstOut)
	}
	callID, _ := calls[0].(map[string]any)["id"].(string)
	if callID == "" {
		t.Fatal("missing generated tool call id")
	}

	secondMessages := anyMessages(toolRoundHistory(16))
	secondMessages = append(secondMessages, firstMessage, map[string]any{"role": "tool", "tool_call_id": callID, "content": "step 17 complete"})
	secondBody := map[string]any{
		"model":       "gpt-5.6-sol",
		"session_key": "issue49-long-task",
		"messages":    secondMessages,
		"tools":       []any{tool},
	}
	secondRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(mustJSON(secondBody))), "issue49-owner"))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	secondOut := wp1DecodeJSON(t, secondRecorder.Body.String())
	secondMessage, _ := openAIChoice(secondOut)
	if secondMessage["content"] != "LONG_TASK_DONE" {
		t.Fatalf("second response=%#v", secondMessage)
	}
	if len(chat.requests) != 3 {
		t.Fatalf("ChatHub requests=%d want=3", len(chat.requests))
	}
	if chat.requests[0].ConversationID != "" || chat.requests[0].SessionID != "" || chat.requests[1].ConversationID != "" || chat.requests[1].SessionID != "" {
		t.Fatalf("router phases reused scratch conversation identity: %#v", chat.requests)
	}
	if chat.requests[2].ConversationID != "" || chat.requests[2].SessionID != "" {
		t.Fatalf("first public answer reused scratch binding: %#v", chat.requests[2])
	}
	if !strings.Contains(chat.requests[1].Text, "step 17 complete") || !strings.Contains(chat.requests[2].Text, "step 17 complete") {
		t.Fatalf("continuation payload lost tool result: router=%q public=%q", chat.requests[1].Text, chat.requests[2].Text)
	}
}

func TestHermesRepeatedRunawayLoopStillStopsAtFinalCeiling(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "unexpected"}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.hermesOpenAIChat(recorder, requestWithMessages(t, "/hermes/v1/chat/completions", repeatedToolRoundHistory(128)))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("ChatHub requests=%d want=0", len(chat.requests))
	}
	errorBody := decodeToolRoundError(t, recorder)
	if errorBody["profile"] != "hermes" || errorBody["limit"] != float64(128) || errorBody["completed_rounds"] != float64(128) || errorBody["terminal"] != true || errorBody["retryable"] != false || errorBody["code"] != "tool_round_limit" || errorBody["limit_type"] != "tool_rounds" {
		t.Fatalf("Hermes runaway round-limit metadata=%#v", errorBody)
	}
}

func TestHermesFinalToolRoundLimitRemainsTerminal(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "unexpected"}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.hermesOpenAIChat(recorder, requestWithToolRounds(t, "/hermes/v1/chat/completions", 128))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("ChatHub requests=%d want=0", len(chat.requests))
	}
	errorBody := decodeToolRoundError(t, recorder)
	if errorBody["profile"] != "hermes" || errorBody["limit"] != float64(128) || errorBody["completed_rounds"] != float64(128) || errorBody["retryable"] != false {
		t.Fatalf("Hermes round-limit metadata=%#v", errorBody)
	}
}
