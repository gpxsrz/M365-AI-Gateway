package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func wp6Phase5RawEvents() []json.RawMessage {
	return []json.RawMessage{json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[` +
		`{"messageType":"Progress","contentType":"SearchResults","text":"SEARCH_QUERY"},` +
		`{"messageType":"Progress","contentOrigin":"ChainOfThoughtSummary","text":"REAL_REASONING_SUMMARY","hiddenText":"HIDDEN_CHAIN"},` +
		`{"messageType":"","contentOrigin":"DeepLeo","text":"Grounded answer","references":{"ref":{"targetLink":"https://support.microsoft.com/topic","isCitedInResponse":true,"displayData":{"type":"Web","content":"Microsoft Support"}}}}` +
		`]}]}`)}
}

func TestWP6GenericCallerToolPromptsPreserveNativeBing(t *testing.T) {
	tool := map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}
	routerPrompt := modelToolRouterPrompt("question", []map[string]any{tool}, "auto", 2)
	for _, phrase := range []string{
		"caller",
		"Microsoft native Bing",
		"citations",
		"grounding",
		"read-only",
		"When the user requests Microsoft native Bing, perform the search before returning the caller-side JSON decision.",
		"Preserve actual SearchResults and any upstream references/citations",
		`"answer":"direct final answer"`,
	} {
		if !strings.Contains(routerPrompt, phrase) {
			t.Fatalf("router prompt missing %q: %s", phrase, routerPrompt)
		}
	}
}

func TestWP6RouterNoCallerAnswerDoesNotReuseJSONOnlyConversation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{
				Text:           `{"calls":[],"answer":"Grounded answer"}`,
				ConversationID: "router-conversation",
				SessionID:      "router-session",
				Events:         wp6Phase5RawEvents(),
			}}
			server := newWP1CandidateServer(t, chat)
			body := map[string]any{
				"model":    "gpt-5.6-sol",
				"stream":   stream,
				"messages": []any{map[string]any{"role": "user", "content": "search"}},
				"tools": []any{map[string]any{"type": "function", "function": map[string]any{
					"name": "read_file", "description": "Read file contents without changing state.", "parameters": map[string]any{"type": "object"},
				}}},
			}
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(body))))

			if recorder.Code != http.StatusOK || len(chat.requests) != 1 {
				t.Fatalf("status=%d requests=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
			}
			encoded := recorder.Body.String()
			if !strings.Contains(encoded, "Grounded answer") || strings.Contains(encoded, `\"calls\":[]`) {
				t.Fatalf("router envelope crossed public boundary: %s", encoded)
			}
			for _, marker := range []string{`"search_result_markers":1`, `"targetLink":"https://support.microsoft.com/topic"`} {
				if !strings.Contains(encoded, marker) {
					t.Fatalf("missing Bing evidence %s: %s", marker, encoded)
				}
			}
		})
	}
}

func TestWP6RouterRepairKeepsDirectAnswerOutOfJSONOnlyConversation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &wp1CandidateChat{results: []chathub.Result{
				{Text: "Grounded answer", ConversationID: "router-conversation", SessionID: "router-session", Events: wp6Phase5RawEvents()},
				{Text: `{"calls":[],"answer":"Grounded answer"}`, ConversationID: "router-conversation", SessionID: "router-session"},
			}}
			server := newWP1CandidateServer(t, chat)
			body := map[string]any{
				"model":    "gpt-5.6-sol",
				"stream":   stream,
				"messages": []any{map[string]any{"role": "user", "content": "search"}},
				"tools": []any{map[string]any{"type": "function", "function": map[string]any{
					"name": "read_file", "description": "Read file contents without changing state.", "parameters": map[string]any{"type": "object"},
				}}},
			}
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(body))))

			if recorder.Code != http.StatusOK || len(chat.requests) != 2 {
				t.Fatalf("status=%d requests=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
			}
			if repair := chat.requests[1].Text; !strings.Contains(repair, `"answer":"direct final answer"`) {
				t.Fatalf("repair prompt uses stale router envelope: %s", repair)
			}
			encoded := recorder.Body.String()
			if !strings.Contains(encoded, "Grounded answer") || strings.Contains(encoded, `\"calls\":[]`) {
				t.Fatalf("router repair envelope crossed public boundary: %s", encoded)
			}
			for _, marker := range []string{`"search_result_markers":1`, `"targetLink":"https://support.microsoft.com/topic"`} {
				if !strings.Contains(encoded, marker) {
					t.Fatalf("missing original Bing evidence %s: %s", marker, encoded)
				}
			}
		})
	}
}

func TestWP6RouterDirectAnswerBlocksNativeMutationBeforeEmission(t *testing.T) {
	mutation := []json.RawMessage{json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`)}
	tests := []struct {
		name     string
		results  []chathub.Result
		requests int
	}{
		{name: "direct", requests: 1, results: []chathub.Result{{Text: `{"calls":[],"answer":"Grounded answer"}`, Events: mutation}}},
		{name: "initial before repair", requests: 1, results: []chathub.Result{{Text: "Grounded answer", Events: mutation}, {Text: `{"calls":[],"answer":"Grounded answer"}`}}},
		{name: "repair", requests: 2, results: []chathub.Result{{Text: "Grounded answer"}, {Text: `{"calls":[],"answer":"Grounded answer"}`, Events: mutation}}},
	}
	for _, tc := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				chat := &wp1CandidateChat{results: tc.results}
				server := newWP1CandidateServer(t, chat)
				body := map[string]any{
					"model":    "gpt-5.6-sol",
					"stream":   stream,
					"messages": []any{map[string]any{"role": "user", "content": "search"}},
					"tools": []any{map[string]any{"type": "function", "function": map[string]any{
						"name": "read_file", "description": "Read file contents without changing state.", "parameters": map[string]any{"type": "object"},
					}}},
				}
				recorder := httptest.NewRecorder()
				server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(body))))

				if recorder.Code != http.StatusBadGateway || wp1ErrorCode(t, recorder) != "native_mutation_blocked" {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if len(chat.requests) != tc.requests {
					t.Fatalf("requests=%d want=%d", len(chat.requests), tc.requests)
				}
				if strings.Contains(recorder.Body.String(), "Grounded answer") || strings.Contains(recorder.Body.String(), `"tool_calls"`) {
					t.Fatalf("blocked native mutation crossed public boundary: %s", recorder.Body.String())
				}
			})
		}
	}
}

func TestWP6ToolResponseUsesOnlyRealReasoningAndKeepsSearchMetadata(t *testing.T) {
	call := []detectedToolCall{{ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}}
	result := chathub.Result{Text: "```read_file\n{\"path\":\"a\"}\n```", Events: wp6Phase5RawEvents()}

	nonStream := httptest.NewRecorder()
	if err := writeToolResponseWithPolicy(nonStream, "id", "model", false, call, result, routeResolution{}, nativePolicySnapshot{}); err != nil {
		t.Fatal(err)
	}
	out := wp1DecodeJSON(t, nonStream.Body.String())
	message, _ := openAIChoice(out)
	if message["reasoning_content"] != "REAL_REASONING_SUMMARY" || message["content"] == message["reasoning_content"] {
		t.Fatalf("reasoning provenance/visible content=%#v", message)
	}
	assertWP6SearchMetadata(t, out)

	stream := httptest.NewRecorder()
	if err := writeToolResponseWithPolicy(stream, "id", "model", true, call, result, routeResolution{}, nativePolicySnapshot{}); err != nil {
		t.Fatal(err)
	}
	body := stream.Body.String()
	if !strings.Contains(body, `"reasoning_content":"REAL_REASONING_SUMMARY"`) || strings.Contains(body, "正在分析请求并准备回答") || strings.Contains(body, "HIDDEN_CHAIN") {
		t.Fatalf("stream reasoning=%s", body)
	}

	withoutReasoning := httptest.NewRecorder()
	if err := writeToolResponseWithPolicy(withoutReasoning, "id", "model", false, call, chathub.Result{Text: result.Text}, routeResolution{}, nativePolicySnapshot{}); err != nil {
		t.Fatal(err)
	}
	message, _ = openAIChoice(wp1DecodeJSON(t, withoutReasoning.Body.String()))
	if _, exists := message["reasoning_content"]; exists {
		t.Fatalf("fabricated reasoning without upstream event: %#v", message)
	}
}

func TestWP6BufferedToolStreamKeepsSearchAndReasoning(t *testing.T) {
	tool := chathub.Tool{Type: "function", Function: json.RawMessage(`{"name":"read_file","parameters":{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}}`)}
	calls := []detectedToolCall{{ID: "call-1", Type: "function", Name: "read_file", Arguments: json.RawMessage(`{"path":"a"}`)}}
	result := chathub.Result{Text: "inspect", Events: wp6Phase5RawEvents()}

	buffered := httptest.NewRecorder()
	if err := writeToolResponseWithPolicy(buffered, "id", "model", false, calls, result, routeResolution{}, nativePolicySnapshot{}); err != nil {
		t.Fatal(err)
	}
	stream := httptest.NewRecorder()
	if err := writeBufferedChatCompletionStream(stream, wp1DecodeJSON(t, buffered.Body.String()), routeResolution{}, []chathub.Tool{tool}, "auto"); err != nil {
		t.Fatal(err)
	}
	body := stream.Body.String()
	for _, marker := range []string{`"reasoning_content":"REAL_REASONING_SUMMARY"`, `"search_result_markers":1`, `"targetLink":"https://support.microsoft.com/topic"`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("buffered tool stream missing %s: %s", marker, body)
		}
	}
	if strings.Contains(body, "HIDDEN_CHAIN") {
		t.Fatalf("buffered tool stream leaked hidden reasoning: %s", body)
	}
}

func TestWP6ReservedNativeBingToolNameIsRejectedAcrossAdapters(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		run  func(*Server, http.ResponseWriter, *http.Request)
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"bInGwEbSeArCh","parameters":{"type":"object"}}}]}`,
			run:  (*Server).openaiChat,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5.6-sol","input":"search","tools":[{"type":"custom","name":"exec"},{"type":"function","name":"BINGWEBSEARCH","parameters":{"type":"object"}}]}`,
			run:  (*Server).responses,
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			body: `{"model":"gpt-5.6-sol","max_tokens":128,"messages":[{"role":"user","content":"search"}],"tools":[{"name":"BiNgWeBsEaRcH","input_schema":{"type":"object"}}]}`,
			run:  (*Server).anthropicMessages,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "must not run"}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			test.run(server, recorder, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"reserved_native_tool_name"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if len(chat.requests) != 0 {
				t.Fatalf("reserved tool reached ChatHub: %#v", chat.requests)
			}
		})
	}
}

func TestWP6BingMatrixB2AndB3PreservesSearchAndCallerSeparation(t *testing.T) {
	tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
	for _, tc := range []struct {
		name      string
		text      string
		wantCalls int
	}{
		{name: "B2 tools registered Bing only", text: "Grounded answer"},
		{name: "B3 Bing and caller same turn", text: "```read_file\n{\"path\":\"a\"}\n```", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: tc.text, ConversationID: "conversation", SessionID: "session", Events: wp6Phase5RawEvents()}}
			server := newWP1CandidateServer(t, chat)
			settings := server.settings.get()
			settings.ToolPlanningMode = "native"
			server.settings.v = settings
			body := map[string]any{"model": "gpt-5.6-sol", "messages": []any{map[string]any{"role": "user", "content": "search and maybe inspect"}}, "tools": tools}
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(body))))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			out := wp1DecodeJSON(t, recorder.Body.String())
			assertWP6SearchMetadata(t, out)
			message, _ := openAIChoice(out)
			calls, _ := message["tool_calls"].([]any)
			if len(calls) != tc.wantCalls {
				t.Fatalf("caller calls=%d want=%d response=%#v", len(calls), tc.wantCalls, out)
			}
		})
	}
}

func TestWP6BingMatrixB1WithoutCallerToolsPreservesSearchAndCitations(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "Grounded answer", ConversationID: "conversation", SessionID: "session", Events: wp6Phase5RawEvents()}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"search"}]}`)))
	if recorder.Code != http.StatusOK || len(chat.requests) != 1 || len(chat.requests[0].Tools) != 0 {
		t.Fatalf("status=%d requests=%#v body=%s", recorder.Code, chat.requests, recorder.Body.String())
	}
	assertWP6SearchMetadata(t, wp1DecodeJSON(t, recorder.Body.String()))
}

func TestWP6NoIncomingSearchEvidenceDoesNotFabricateBingMetadata(t *testing.T) {
	metadata := compatM365Metadata(chathub.Result{Text: "ordinary answer", Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"text":"ordinary answer"}]}]}`)}})
	if _, exists := metadata["search"]; exists {
		t.Fatalf("fabricated search metadata: %#v", metadata)
	}
	if _, exists := metadata["references"]; exists {
		t.Fatalf("fabricated references: %#v", metadata)
	}
}

func TestWP6DefaultRouterCarriesBingEvidenceIntoFinalAnswer(t *testing.T) {
	route := chathub.Result{Text: `{"calls":[]}`, Events: wp6Phase5RawEvents()}
	answer := chathub.Result{Text: "Grounded answer"}
	chat := &wp6Phase5SequenceChat{results: []chathub.Result{route, answer}}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if recorder.Code != http.StatusOK || len(chat.requests) != 2 {
		t.Fatalf("status=%d requests=%d body=%s", recorder.Code, len(chat.requests), recorder.Body.String())
	}
	out := wp1DecodeJSON(t, recorder.Body.String())
	assertWP6SearchMetadata(t, out)
	message, _ := openAIChoice(out)
	if message["content"] != "Grounded answer" {
		t.Fatalf("message=%#v", message)
	}
	encoded := recorder.Body.String()
	if strings.Contains(encoded, "REAL_REASONING_SUMMARY") || strings.Contains(encoded, "HIDDEN_CHAIN") || strings.Contains(encoded, "SEARCH_QUERY") {
		t.Fatalf("internal router content crossed the public boundary: %s", encoded)
	}
}

func TestWP6SearchMetadataSurvivesResponsesAndAnthropicAdapters(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Server, http.ResponseWriter)
	}{
		{
			name: "responses",
			run: func(server *Server, writer http.ResponseWriter) {
				server.responses(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"search"}`)))
			},
		},
		{
			name: "anthropic",
			run: func(server *Server, writer http.ResponseWriter) {
				server.anthropicMessages(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.6-sol","max_tokens":128,"messages":[{"role":"user","content":"search"}]}`)))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "Grounded answer", Events: wp6Phase5RawEvents()}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			tc.run(server, recorder)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			assertWP6SearchMetadata(t, wp1DecodeJSON(t, recorder.Body.String()))
		})
	}
}

type wp6Phase5SequenceChat struct {
	results  []chathub.Result
	requests []chathub.Request
}

func (c *wp6Phase5SequenceChat) next(request chathub.Request) (chathub.Result, error) {
	c.requests = append(c.requests, request)
	if len(c.results) == 0 {
		return chathub.Result{}, fmt.Errorf("unexpected upstream call")
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result, nil
}

func (c *wp6Phase5SequenceChat) Chat(_ context.Context, _ chathub.Account, request chathub.Request) (chathub.Result, error) {
	return c.next(request)
}

func (c *wp6Phase5SequenceChat) ChatWithDelta(_ context.Context, _ chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	result, err := c.next(request)
	if err == nil && emit != nil {
		err = emit(result.Text)
	}
	return result, err
}

func (c *wp6Phase5SequenceChat) ChatWithEvents(_ context.Context, _ chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result, err := c.next(request)
	if err == nil && emit != nil {
		for _, event := range chathub.SemanticEvents(result.Events) {
			kind := "progress"
			if event.Kind == "reasoning.summary" {
				kind = "reasoning"
			}
			if emitErr := emit(chathub.StreamEvent{Kind: kind, Text: event.Text, MessageType: event.MessageType, ContentType: event.ContentType}); emitErr != nil {
				return chathub.Result{}, emitErr
			}
		}
		if result.Text != "" {
			err = emit(chathub.StreamEvent{Kind: "text", Text: result.Text})
		}
	}
	return result, err
}

func TestWP6B4ToolContinuationThenBingOnlyReusesCheckpoint(t *testing.T) {
	first := chathub.Result{Text: `{"calls":[{"name":"read_file","arguments":{"path":"a"}}]}`, ConversationID: "conversation", SessionID: "session-1", Events: wp6Phase5RawEvents()}
	second := chathub.Result{Text: `{"calls":[]}`, ConversationID: "conversation", SessionID: "session-2", Events: wp6Phase5RawEvents()}
	final := chathub.Result{Text: "Grounded continuation", ConversationID: "conversation", SessionID: "session-3"}
	chat := &wp6Phase5SequenceChat{results: []chathub.Result{first, second, final}}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	server.checkpoints, _ = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	settings := server.settings.get()
	settings.ToolPlanningMode = "router"
	server.settings.v = settings
	tool := map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	firstBody := map[string]any{"model": "gpt-5.6-sol", "session_key": "hermes-conversation", "messages": []any{map[string]any{"role": "user", "content": "inspect then search"}}, "tools": []any{tool}}
	firstRecorder := httptest.NewRecorder()
	server.openaiChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(firstBody))), "phase5-owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	firstOut := wp1DecodeJSON(t, firstRecorder.Body.String())
	firstMessage, _ := openAIChoice(firstOut)
	calls := firstMessage["tool_calls"].([]any)
	callID := calls[0].(map[string]any)["id"].(string)
	secondBody := map[string]any{"model": "gpt-5.6-sol", "messages": []any{
		map[string]any{"role": "user", "content": "inspect then search"}, firstMessage,
		map[string]any{"role": "tool", "tool_call_id": callID, "content": "file result"},
		map[string]any{"role": "user", "content": "now use Bing only"},
	}, "session_key": "hermes-conversation", "tools": []any{tool}}
	secondRecorder := httptest.NewRecorder()
	server.openaiChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(mustJSON(secondBody))), "phase5-owner"))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	secondOut := wp1DecodeJSON(t, secondRecorder.Body.String())
	assertWP6SearchMetadata(t, secondOut)
	secondMessage, _ := openAIChoice(secondOut)
	if _, hasCalls := secondMessage["tool_calls"]; hasCalls || secondMessage["content"] != "Grounded continuation" {
		t.Fatalf("B4 response=%#v", secondMessage)
	}
	if len(chat.requests) != 3 || chat.requests[1].ConversationID != "conversation" || chat.requests[1].SessionID != "session-1" || chat.requests[2].ConversationID != "conversation" || chat.requests[2].SessionID != "session-2" {
		t.Fatalf("B4 transport continuity=%#v", chat.requests)
	}
}

func TestWP6SerialToolSafetyContractPreservesHermesCheckpointContinuation(t *testing.T) {
	first := chathub.Result{Text: `{"calls":[{"name":"read_file","arguments":{"path":"a"}}]}`, ConversationID: "conversation", SessionID: "session-1"}
	second := chathub.Result{Text: `{"calls":[],"answer":"done"}`, ConversationID: "conversation", SessionID: "session-2"}
	chat := &wp6Phase5SequenceChat{results: []chathub.Result{first, second}}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	server.checkpoints, _ = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	settings := server.settings.get()
	settings.ToolPlanningMode = "router"
	server.settings.v = settings
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "description": "Read a file.", "parameters": map[string]any{"type": "object", "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "terminal", "description": "Run a command.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}},
	}
	firstBody := map[string]any{"model": "gpt-5.6-sol", "session_key": "serial-safety", "messages": []any{map[string]any{"role": "user", "content": "inspect safely"}}, "tools": tools, "parallel_tool_calls": true}
	firstRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(firstRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(mustJSON(firstBody))), "serial-owner"))
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	firstOut := wp1DecodeJSON(t, firstRecorder.Body.String())
	firstMessage, _ := openAIChoice(firstOut)
	calls := firstMessage["tool_calls"].([]any)
	callID := calls[0].(map[string]any)["id"].(string)
	secondBody := map[string]any{"model": "gpt-5.6-sol", "session_key": "serial-safety", "messages": []any{
		map[string]any{"role": "user", "content": "inspect safely"}, firstMessage,
		map[string]any{"role": "tool", "tool_call_id": callID, "content": "file result"},
	}, "tools": tools, "parallel_tool_calls": true}
	secondRecorder := httptest.NewRecorder()
	server.hermesOpenAIChat(secondRecorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(mustJSON(secondBody))), "serial-owner"))
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	secondOut := wp1DecodeJSON(t, secondRecorder.Body.String())
	secondMessage, _ := openAIChoice(secondOut)
	if secondMessage["content"] != "done" {
		t.Fatalf("continuation response=%#v", secondMessage)
	}
	if len(chat.requests) != 2 || !strings.Contains(chat.requests[0].Text, "Maximum calls this turn: 1") || !strings.Contains(chat.requests[1].Text, "Maximum calls this turn: 1") {
		t.Fatalf("serial contract was not stable across turns: %#v", chat.requests)
	}
	if chat.requests[1].ConversationID != "conversation" || chat.requests[1].SessionID != "session-1" {
		t.Fatalf("checkpoint continuity diverged after the serialized tool call: %#v", chat.requests)
	}
}

func TestWP6StreamingReasoningComesFromRealEventsOnly(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "VISIBLE_FINAL", ConversationID: "conversation", SessionID: "session", Events: wp6Phase5RawEvents()},
		events: []chathub.StreamEvent{{Kind: "reasoning", Text: "REAL_REASONING_SUMMARY"}, {Kind: "text", Text: "VISIBLE_FINAL"}},
	}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"reason"}]}`)))
	body := recorder.Body.String()
	visible, reasoning := wp6ChatStreamText(t, body)
	if recorder.Code != http.StatusOK || reasoning != "REAL_REASONING_SUMMARY" || visible != "VISIBLE_FINAL" || strings.Contains(body, "正在分析请求并准备回答") || strings.Contains(body, "HIDDEN_CHAIN") {
		t.Fatalf("stream status=%d body=%s", recorder.Code, body)
	}
}

func wp6ChatStreamText(t *testing.T, body string) (string, string) {
	t.Helper()
	var visible, reasoning strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if content, _ := delta["content"].(string); content != "" {
			visible.WriteString(content)
		}
		if content, _ := delta["reasoning_content"].(string); content != "" {
			reasoning.WriteString(content)
		}
	}
	return visible.String(), reasoning.String()
}

func TestWP6HermesReasoningEffortIsMappedOrExplicitlyDegraded(t *testing.T) {
	for _, effort := range []string{"high", "max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "ok"}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			body := fmt.Sprintf(`{"model":"gpt-5.6-sol","reasoning_effort":%q,"messages":[{"role":"user","content":"reason"}]}`, effort)
			server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
			if recorder.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != "Gpt_5_6_Reasoning" {
				t.Fatalf("status=%d requests=%#v body=%s", recorder.Code, chat.requests, recorder.Body.String())
			}
			out := wp1DecodeJSON(t, recorder.Body.String())
			metadata := out["m365"].(map[string]any)
			if metadata["reasoning_effort_ignored"] != true {
				t.Fatalf("missing explicit stable-route degradation: %#v", metadata)
			}
		})
	}
}

func TestWP6RequiredRouteMappingsUseLiveVerifiedTones(t *testing.T) {
	for _, tc := range []struct {
		model string
		tone  string
	}{
		{model: "m365-auto", tone: "Magic"},
		{model: "quick", tone: "Chat"},
		{model: "think-deeper", tone: "Reasoning"},
		{model: "m365-gpt-5.6-think-deeper", tone: "Gpt_5_6_Reasoning"},
		{model: "m365-gpt-5.5-quick-response", tone: "Gpt_5_5_Chat"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			resolution, err := resolveRoute(tc.model, "high", nil)
			if err != nil || resolution.ResolvedTone != tc.tone || !resolution.ReasoningEffortIgnored {
				t.Fatalf("resolution=%#v err=%v", resolution, err)
			}
		})
	}
}

func assertWP6SearchMetadata(t *testing.T, response map[string]any) {
	t.Helper()
	metadata, _ := response["m365"].(map[string]any)
	search, _ := metadata["search"].(map[string]any)
	references, _ := metadata["references"].([]any)
	if search["search_result_markers"].(float64) < 1 || len(references) != 1 {
		t.Fatalf("search metadata=%#v", metadata)
	}
	reference := references[0].(map[string]any)
	if reference["targetLink"] != "https://support.microsoft.com/topic" || reference["isCitedInResponse"] != true {
		t.Fatalf("reference=%#v", reference)
	}
}
