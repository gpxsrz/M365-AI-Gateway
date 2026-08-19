package web

import (
	"context"
	"encoding/json"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type continuationChat struct {
	requests []chathub.Request
	results  []chathub.Result
}

func (f *continuationChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	if len(f.results) == 0 {
		return chathub.Result{}, nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

func (f *continuationChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, emit func(string) error) (chathub.Result, error) {
	result, err := f.Chat(ctx, account, req)
	if err == nil && emit != nil && result.Text != "" {
		if emitErr := emit(result.Text); emitErr != nil {
			return chathub.Result{}, emitErr
		}
	}
	return result, err
}

func (f *continuationChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result, err := f.Chat(ctx, account, req)
	if err == nil && emit != nil && result.Text != "" {
		if emitErr := emit(chathub.StreamEvent{Kind: "text", Text: result.Text}); emitErr != nil {
			return chathub.Result{}, emitErr
		}
	}
	return result, err
}

func continuationStreamText(t *testing.T, stream string) string {
	t.Helper()
	var text strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(stream, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode stream chunk: %v; payload=%s", err, payload)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if content, _ := delta["content"].(string); content != "" {
			text.WriteString(content)
		}
	}
	return text.String()
}

func TestFinalAnswerUnwrapsRouterEnvelopeAcrossOpenAIProfiles(t *testing.T) {
	cases := []struct {
		name string
		path string
		run  func(*Server, http.ResponseWriter, *http.Request)
	}{
		{name: "generic", path: "/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.interactiveOpenAIChat(w, r) }},
		{name: "hermes", path: "/hermes/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.hermesOpenAIChat(w, r) }},
		{name: "memory", path: "/memory/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.memoryOpenAIChat(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{
				{Text: `{"calls":[],"answer":""}`},
				{Text: `{"calls":[],"answer":"  line 1\n\nline 2  \n"}`},
			}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			settings := s.settings.get()
			settings.MemoryCompatibilityEnabled = true
			s.settings.v = settings
			body := `{
				"model":"gpt-5.6-reasoning",
				"messages":[{"role":"user","content":"Summarize the result."}],
				"tools":[{
					"type":"function",
					"function":{
						"name":"terminal",
						"description":"Run a command.",
						"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
					}
				}],
				"tool_choice":"auto"
			}`
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			rr := httptest.NewRecorder()

			tc.run(s, rr, r)

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 2 {
				t.Fatalf("upstream requests=%d, want router plus final answer", len(chat.requests))
			}
			response := wp1DecodeJSON(t, rr.Body.String())
			choices, _ := response["choices"].([]any)
			choice, _ := choices[0].(map[string]any)
			message, _ := choice["message"].(map[string]any)
			if got := message["content"]; got != "  line 1\n\nline 2  \n" {
				t.Fatalf("content=%q response=%s", got, rr.Body.String())
			}
		})
	}
}

func TestFinalAnswerStreamUnwrapsRouterEnvelopeAcrossOpenAIProfiles(t *testing.T) {
	cases := []struct {
		name string
		path string
		run  func(*Server, http.ResponseWriter, *http.Request)
	}{
		{name: "generic", path: "/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.interactiveOpenAIChat(w, r) }},
		{name: "hermes", path: "/hermes/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.hermesOpenAIChat(w, r) }},
		{name: "memory", path: "/memory/v1/chat/completions", run: func(s *Server, w http.ResponseWriter, r *http.Request) { s.memoryOpenAIChat(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{
				{Text: `{"calls":[],"answer":""}`},
				{Text: `{"calls":[],"answer":"  line 1\n\nline 2  \n"}`},
			}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			settings := s.settings.get()
			settings.MemoryCompatibilityEnabled = true
			s.settings.v = settings
			body := `{
				"model":"gpt-5.6-reasoning",
				"stream":true,
				"messages":[{"role":"user","content":"Summarize the result."}],
				"tools":[{
					"type":"function",
					"function":{
						"name":"terminal",
						"description":"Run a command.",
						"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
					}
				}],
				"tool_choice":"auto"
			}`
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			rr := httptest.NewRecorder()

			tc.run(s, rr, r)

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := continuationStreamText(t, rr.Body.String()); got != "  line 1\n\nline 2  \n" {
				t.Fatalf("stream text=%q stream=%s", got, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), `\"calls\"`) || strings.Contains(rr.Body.String(), `\\n`) {
				t.Fatalf("router envelope leaked into stream: %s", rr.Body.String())
			}
		})
	}
}

func TestHermesFinalAnswerPreservesOrdinaryJSON(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[],"answer":""}`},
		{Text: `{"status":"ok","items":[]}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Return JSON status."}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.hermesOpenAIChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != `{"status":"ok","items":[]}` {
		t.Fatalf("content=%q response=%s", got, rr.Body.String())
	}
}

func TestHermesFinalAnswerRejectsRouterEnvelopeWithCalls(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[],"answer":""}`},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"printf late"}}],"answer":""}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Summarize the result."}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.hermesOpenAIChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := wp1ErrorCode(t, rr); got != "invalid_tool_call" {
		t.Fatalf("error code=%q body=%s", got, rr.Body.String())
	}
}

func TestHermesFinalAnswerStreamRejectsDuplicateRouterCalls(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[],"answer":""}`},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"printf unsafe"}}],"calls":[],"answer":"done"}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[{"role":"user","content":"Summarize the result."}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.hermesOpenAIChat(rr, r)

	stream := rr.Body.String()
	if !strings.Contains(stream, `"code":"invalid_tool_call_stream"`) {
		t.Fatalf("missing fail-closed stream error: %s", stream)
	}
	if strings.Contains(stream, `"content":"done"`) || strings.Contains(stream, `\"calls\"`) {
		t.Fatalf("ambiguous router envelope leaked as final text: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesFinalAnswerPreservesMalformedJSON(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[],"answer":""}`},
		{Text: `{"calls":[],"answer":"line 1"`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Return the text exactly."}],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.hermesOpenAIChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != `{"calls":[],"answer":"line 1"` {
		t.Fatalf("content=%q response=%s", got, rr.Body.String())
	}
}

func TestHermesInterruptedToolSequenceReturnsUnconfirmedAnswer(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}},
				{"id":"c2","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"verify\"}"}},
				{"id":"c3","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"report\"}"}}
			]},
			{"role":"user","content":"Continue from the interruption without repeating completed work."}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want 1", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%#v", response["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != "I cannot confirm completion because no matching tool results were returned. No external action has been verified." {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestHermesInterruptedToolCallIsNotAutomaticallyReissued(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"deploy"}}]}`},
		{Text: "The previous tool outcome is still unconfirmed."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{
					"type":"object",
					"properties":{"command":{"type":"string"}},
					"required":["command"]
				}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router plus safe answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%#v", response["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, repeated := message["tool_calls"]; repeated {
		t.Fatalf("interrupted tool call was reissued: %#v", message)
	}
	if got := message["content"]; got != "The previous tool outcome is still unconfirmed." {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestHermesInterruptedToolSequenceAllowsNewArguments(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{
		Text: `{"calls":[{"name":"terminal","arguments":{"command":"verify"}}]}`,
	}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Verify the current state without repeating deployment."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{
					"type":"object",
					"properties":{"command":{"type":"string"}},
					"required":["command"]
				}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want one router request", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls=%#v response=%#v", message["tool_calls"], response)
	}
	call, _ := toolCalls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if function["name"] != "terminal" || function["arguments"] != `{"command":"verify"}` {
		t.Fatalf("unexpected new tool call: %#v", call)
	}
}

func TestHermesPendingToolSequenceWithoutRecoveryTurnRemainsRejected(t *testing.T) {
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	errorBody := wp1DecodeJSON(t, rr.Body.String())["error"].(map[string]any)
	if errorType := errorBody["type"]; errorType != "tool_protocol_error" {
		t.Fatalf("error type=%q body=%s", errorType, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing tool result for tool_call_id: c1") {
		t.Fatalf("missing pending-call error: %s", rr.Body.String())
	}
}

func TestHermesInterruptedSequenceRejectsReusedToolCallID(t *testing.T) {
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"verify\"}"}}
			]}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	errorBody := wp1DecodeJSON(t, rr.Body.String())["error"].(map[string]any)
	if errorType := errorBody["type"]; errorType != "tool_protocol_error" {
		t.Fatalf("error type=%q body=%s", errorType, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "duplicate tool call id: c1") {
		t.Fatalf("missing duplicate-id error: %s", rr.Body.String())
	}
}

func TestHermesInterruptedToolSequenceBuffersStreamingSuccessClaim(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, "Deployment completed successfully.") {
		t.Fatalf("unsupported success escaped into stream: %s", stream)
	}
	if !strings.Contains(stream, "I cannot confirm completion because no matching tool results were returned. No external action has been verified.") {
		t.Fatalf("safe unconfirmed answer missing: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesInterruptedStreamingAllowsDistinctNewToolCall(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{
		Text: `{"calls":[{"name":"terminal","arguments":{"command":"verify"}}]}`,
	}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Verify the current state without repeating deployment."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	for _, want := range []string{`"name":"terminal"`, `\"command\":\"verify\"`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(stream, want) {
			t.Fatalf("missing %q in stream: %s", want, stream)
		}
	}
	if strings.Contains(stream, `\"command\":\"deploy\"`) {
		t.Fatalf("interrupted deployment was reissued: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesInterruptedStreamingPreservesImagesWithDistinctToolCall(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{
		Text:   `{"calls":[{"name":"terminal","arguments":{"command":"verify"}}]}`,
		Images: []string{"https://example.test/verification.png"},
	}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Verify the current state and include available visual evidence."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	for _, want := range []string{
		`"name":"terminal"`,
		`\"command\":\"verify\"`,
		"https://example.test/verification.png",
		`"finish_reason":"tool_calls"`,
	} {
		if !strings.Contains(stream, want) {
			t.Fatalf("missing %q in stream: %s", want, stream)
		}
	}
	if strings.Contains(stream, `\"command\":\"deploy\"`) {
		t.Fatalf("interrupted deployment was reissued: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesInterruptedStreamingPreservesImages(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:   "The prior execution outcome remains unconfirmed.",
		Images: []string{"https://example.test/status.png"},
	}}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Inspect the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"inspect\"}"}}
			]},
			{"role":"user","content":"Show any available evidence after the interruption."}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	for _, want := range []string{"The prior execution outcome remains unconfirmed.", "https://example.test/status.png", `"finish_reason":"stop"`} {
		if !strings.Contains(stream, want) {
			t.Fatalf("missing %q in stream: %s", want, stream)
		}
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesInterruptedFencedToolCallIsNotReissued(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[]}`},
		{Text: "```terminal\n{\"command\":\"deploy\"}\n```"},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, repeated := message["tool_calls"]; repeated {
		t.Fatalf("fenced interrupted call was reissued: %#v", message)
	}
	if got := message["content"]; got != unconfirmedToolOutcomeResponse {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestHermesInterruptedNativeToolCallIsNotReissued(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"deploy"}}}`)},
	}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, repeated := message["tool_calls"]; repeated {
		t.Fatalf("native interrupted call was reissued: %#v", message)
	}
	if got := message["content"]; got != unconfirmedToolOutcomeResponse {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestHermesInterruptedNativeToolCallPreservesNeutralAnswer(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:   "The previous tool outcome remains unconfirmed.",
		Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"deploy"}}}`)},
	}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, repeated := message["tool_calls"]; repeated {
		t.Fatalf("native interrupted call was reissued: %#v", message)
	}
	if got := message["content"]; got != "The previous tool outcome remains unconfirmed." {
		t.Fatalf("neutral answer was overwritten: %q", got)
	}
}

func TestHermesCompletedNativeToolCallIsNotReissuedInStream(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "The existing deployment result remains available."},
		events: []chathub.StreamEvent{
			{Kind: "text", Text: "The existing deployment result remains available."},
			{Kind: "tool", ToolName: "terminal", Arguments: json.RawMessage(`{"command":"deploy"}`)},
		},
	}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"deployment result"},
			{"role":"user","content":"Continue without repeating deployment."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, `"tool_calls"`) || strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("completed native call was reissued: %s", stream)
	}
	if got := continuationStreamText(t, stream); got != "The existing deployment result remains available." {
		t.Fatalf("safe text=%q stream=%s", got, stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesStructuredSuccessfulTerminalEvidenceAuthorizesStreamingSuccess(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
		events: []chathub.StreamEvent{{Kind: "text", Text: "Deployment completed successfully."}},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"printf ok\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"{\"output\":\"ok\",\"exit_code\":0,\"error\":null}"}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.hermesOpenAIChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if got := continuationStreamText(t, stream); got != "Deployment completed successfully." {
		t.Fatalf("successful evidence was suppressed: text=%q stream=%s", got, stream)
	}
	if strings.Contains(stream, unconfirmedToolOutcomeResponse) {
		t.Fatalf("successful evidence was rewritten as unconfirmed: %s", stream)
	}
}

func TestHermesFailedCompletedToolDoesNotAuthorizeStreamingSuccess(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
		events: []chathub.StreamEvent{
			{Kind: "text", Text: "Deployment completed successfully."},
			{Kind: "tool", ToolName: "terminal", Arguments: json.RawMessage(`{"command":"deploy"}`)},
		},
	}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"exit code 1: deployment failed"},
			{"role":"user","content":"Continue without repeating deployment."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, "Deployment completed successfully.") {
		t.Fatalf("failed evidence authorized streamed success: %s", stream)
	}
	if strings.Contains(stream, `"tool_calls"`) || strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("failed completed call was reissued: %s", stream)
	}
	if got := continuationStreamText(t, stream); got != unconfirmedToolOutcomeResponse {
		t.Fatalf("safe text=%q stream=%s", got, stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesFailedCompletedToolWithoutActiveToolsDoesNotAuthorizeStreamingSuccess(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
		events: []chathub.StreamEvent{{Kind: "text", Text: "Deployment completed successfully."}},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"exit code 1: deployment failed"},
			{"role":"user","content":"Explain the current state."}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, "Deployment completed successfully.") {
		t.Fatalf("failed evidence authorized streamed success without active tools: %s", stream)
	}
	if got := continuationStreamText(t, stream); got != unconfirmedToolOutcomeResponse {
		t.Fatalf("safe text=%q stream=%s", got, stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestHermesCompletedFencedToolCallIsNotReissuedInStream(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text: "The previous deployment is already recorded.\n```terminal\n{\"command\":\"deploy\"}\n```",
	}}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"deployment result"},
			{"role":"user","content":"Continue without repeating deployment."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"terminal",
				"description":"Run a command.",
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}
			}
		}],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, `"tool_calls"`) || strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("completed fenced call was reissued: %s", stream)
	}
	if got := continuationStreamText(t, stream); got != "The previous deployment is already recorded." {
		t.Fatalf("safe prefix=%q stream=%s", got, stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestResponsesInterruptedToolSequenceReturnsUnconfirmedAnswer(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Deploy the service."}]},
			{"type":"function_call","call_id":"c1","name":"terminal","arguments":"{\"command\":\"deploy\"}"},
			{"role":"user","content":[{"type":"input_text","text":"Continue after the interruption."}]}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.responses(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := rr.Body.String()
	if strings.Contains(response, "Deployment completed successfully.") {
		t.Fatalf("unsupported success escaped Responses adapter: %s", response)
	}
	if !strings.Contains(response, unconfirmedToolOutcomeResponse) {
		t.Fatalf("safe unconfirmed answer missing: %s", response)
	}
}

func TestStreamingResponsesInterruptedToolSequenceCompletesSafely(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"Deploy the service."}]},
			{"type":"function_call","call_id":"c1","name":"terminal","arguments":"{\"command\":\"deploy\"}"},
			{"role":"user","content":[{"type":"input_text","text":"Continue after the interruption."}]}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.responses(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	stream := rr.Body.String()
	if strings.Contains(stream, "Deployment completed successfully.") {
		t.Fatalf("unsupported success escaped Responses stream: %s", stream)
	}
	if !strings.Contains(stream, unconfirmedToolOutcomeResponse) {
		t.Fatalf("safe unconfirmed answer missing: %s", stream)
	}
	if got := strings.Count(stream, "event: response.completed"); got != 1 {
		t.Fatalf("response.completed count=%d stream=%s", got, stream)
	}
	if strings.Contains(stream, "event: response.failed") {
		t.Fatalf("unexpected response.failed: %s", stream)
	}
}

func TestAnthropicInterruptedToolSequenceReturnsUnconfirmedAnswer(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Deployment completed successfully."},
	}
	s := newWP1CandidateServer(t, chat)
	body := `{
		"model":"gpt-5.6-reasoning",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"c1","name":"terminal","input":{"command":"deploy"}}
			]},
			{"role":"user","content":"Continue after the interruption."}
		]
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.anthropicMessages(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := rr.Body.String()
	if strings.Contains(response, "Deployment completed successfully.") {
		t.Fatalf("unsupported success escaped Anthropic adapter: %s", response)
	}
	if !strings.Contains(response, unconfirmedToolOutcomeResponse) {
		t.Fatalf("safe unconfirmed answer missing: %s", response)
	}
}
