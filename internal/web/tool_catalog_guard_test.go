package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type routerStreamChat struct {
	requests []chathub.Request
}

func (f *routerStreamChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	return chathub.Result{Text: "NO_TOOL_NEEDED"}, nil
}

func (f *routerStreamChat) ChatWithDelta(_ context.Context, _ chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	return chathub.Result{}, nil
}

func (f *routerStreamChat) ChatWithEvents(_ context.Context, _ chathub.Account, req chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	if err := emit(chathub.StreamEvent{Kind: "tool", ToolName: "bash", Arguments: []byte(`{"command":"sudo apt install chrome"}`)}); err != nil {
		return chathub.Result{}, err
	}
	if err := emit(chathub.StreamEvent{Kind: "text", Text: "Use the amd64 stable Chrome package."}); err != nil {
		return chathub.Result{}, err
	}
	return chathub.Result{Text: "Use the amd64 stable Chrome package."}, nil
}

func bufferedToolResponse(name, arguments string) map[string]any {
	return map[string]any{
		"id":    "chatcmpl-buffered",
		"model": "gpt-5.6-reasoning",
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role":    "assistant",
				"content": "tool plan",
				"tool_calls": []any{map[string]any{
					"id":   "call_buffered",
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": arguments,
					},
				}},
			},
		}},
	}
}

func TestMemorySchemaFinalOutboundBudgetFailsBeforeChatHub(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "should not run"}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.TextInputLimitUTF16 = 80
	server.settings.v = settings
	rr := httptest.NewRecorder()
	body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"short"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"long_protocol_property_name":{"type":"string"}},"required":["long_protocol_property_name"]}}}}`
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest || len(chat.requests) != 0 || !strings.Contains(rr.Body.String(), "text_policy_exceeded") {
		t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
	}
}

func TestInvalidResponseFormatFailsBeforeChatHub(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "should not run"}}
	server := newWP1CandidateServer(t, chat)
	rr := httptest.NewRecorder()
	body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"remember"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"$ref":"https://example.invalid/schema.json"}}}}`
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest || len(chat.requests) != 0 || !strings.Contains(rr.Body.String(), "invalid_response_format") {
		t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
	}
}

func TestInvalidToolChoiceFailsBeforeChatHub(t *testing.T) {
	for _, raw := range []string{
		`"bogus"`,
		`{"type":"function"}`,
		`{"type":"not_function","function":{"name":"terminal"}}`,
		`{"unexpected":true}`,
	} {
		t.Run(raw, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "should not run"}}
			server := newWP1CandidateServer(t, chat)
			rr := httptest.NewRecorder()
			body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"run"}],"tools":[` + routerFallbackTool + `],"tool_choice":` + raw + `}`
			server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest || len(chat.requests) != 0 || !strings.Contains(rr.Body.String(), "invalid_tool_choice") {
				t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
			}
		})
	}
}

func terminalCatalog() []chathub.Tool {
	return []chathub.Tool{{
		Type:     "function",
		Function: json.RawMessage(`{"name":"terminal","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}`),
	}}
}

func TestBufferedStreamRejectsUnregisteredToolCallBeforeWrite(t *testing.T) {
	rr := httptest.NewRecorder()
	err := writeBufferedChatCompletionStream(
		rr,
		bufferedToolResponse("bash", `{"command":"sudo apt install chrome"}`),
		routeResolution{},
		terminalCatalog(),
		"auto",
	)
	if !errors.Is(err, errUnavailableToolCall) {
		t.Fatalf("error=%v, want unavailable tool call", err)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("invalid buffered call wrote response bytes: %s", rr.Body.String())
	}
}

func TestBufferedStreamPreservesRegisteredCallWhenMixedWithUnregisteredCall(t *testing.T) {
	response := bufferedToolResponse("terminal", `{"command":"status"}`)
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	calls, _ := message["tool_calls"].([]any)
	message["tool_calls"] = append(calls, map[string]any{
		"id":   "call_unregistered",
		"type": "function",
		"function": map[string]any{
			"name":      "bash",
			"arguments": `{"command":"sudo apt install chrome"}`,
		},
	})

	rr := httptest.NewRecorder()
	err := writeBufferedChatCompletionStream(rr, response, routeResolution{}, terminalCatalog(), "auto")
	if err != nil {
		t.Fatalf("mixed buffered response rejected valid call: %v", err)
	}
	stream := rr.Body.String()
	if !strings.Contains(stream, `"name":"terminal"`) || strings.Contains(stream, `"name":"bash"`) {
		t.Fatalf("mixed buffered response was not filtered correctly: %s", stream)
	}
	if got := strings.Count(stream, `"finish_reason":"tool_calls"`); got != 1 {
		t.Fatalf("tool terminal count=%d stream=%s", got, stream)
	}
}

func TestBufferedStreamPreservesRegisteredToolCall(t *testing.T) {
	rr := httptest.NewRecorder()
	err := writeBufferedChatCompletionStream(
		rr,
		bufferedToolResponse("terminal", `{"command":"status"}`),
		routeResolution{},
		terminalCatalog(),
		"auto",
	)
	if err != nil {
		t.Fatalf("registered tool call rejected: %v", err)
	}
	stream := rr.Body.String()
	if !strings.Contains(stream, `"name":"terminal"`) || !strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("registered tool call was not preserved: %s", stream)
	}
}

func TestNativeToolChoiceNoneRejectsToolOnlyResult(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"status"}}}`)},
	}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Explain without tools."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"none"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code := wp1ErrorCode(t, rr); code != "invalid_tool_call" {
		t.Fatalf("error code=%q body=%s", code, rr.Body.String())
	}
}

func TestNativeNamedToolChoiceRejectsDifferentTool(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"status"}}}`)},
	}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Use lookup."}],
		"tools":[
			` + routerFallbackTool + `,
			{"type":"function","function":{"name":"lookup","description":"Look up data.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}
		],
		"tool_choice":{"type":"function","function":{"name":"lookup"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code := wp1ErrorCode(t, rr); code != "invalid_tool_call" {
		t.Fatalf("error code=%q body=%s", code, rr.Body.String())
	}
}

func TestNativeToolChoiceNonePreservesTextAndDropsTool(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:   "Use the stable amd64 Chrome package.",
		Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"status"}}}`)},
	}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Explain without tools."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"none"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("tool_choice none emitted a tool call: %#v", message)
	}
	if got := message["content"]; got != "Use the stable amd64 Chrome package." {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestRouterKnownNativeToolOnlyResultIsSuppressed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: "NO_TOOL_NEEDED"},
		{Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"deploy"}}}`)}},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"deployment result"},
			{"role":"user","content":"Continue without repeating deployment."}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("known native call was reissued: %#v", message)
	}
	if got := message["content"]; got != completedToolCallSuppressedResponse {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestRouterNewNativeToolOnlyResultFailsClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: "NO_TOOL_NEEDED"},
		{Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"terminal","arguments":{"command":"verify"}}}`)}},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Explain the current state."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code := wp1ErrorCode(t, rr); code != "invalid_tool_call" {
		t.Fatalf("error code=%q body=%s", code, rr.Body.String())
	}
}

func TestNativeMixedKnownAndNewFencedCallsEmitsOnlyNewCall(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "```terminal\n{\"command\":\"deploy\"}\n```\n```terminal\n{\"command\":\"verify\"}\n```"}}
	s := newWP1CandidateServer(t, chat)
	settings := s.settings.get()
	settings.ToolPlanningMode = "native"
	s.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"tool","tool_call_id":"c1","content":"deployment result"},
			{"role":"user","content":"Continue without repeating deployment."}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	calls, _ := message["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls=%#v response=%#v", message["tool_calls"], response)
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if got := function["name"]; got != "terminal" {
		t.Fatalf("tool name=%q response=%#v", got, response)
	}
	arguments := fmt.Sprint(function["arguments"])
	if !strings.Contains(arguments, `"verify"`) || strings.Contains(arguments, `"deploy"`) {
		t.Fatalf("known call was not suppressed: arguments=%s response=%#v", arguments, response)
	}
	if content := fmt.Sprint(message["content"]); strings.Contains(content, "deploy") {
		t.Fatalf("known fenced call leaked into content: %q", content)
	}
}

func TestRouterNoToolDecisionCannotBeOverriddenByFencedRegisteredTool(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: "NO_TOOL_NEEDED"},
		{Text: "Use this command only if you choose to install it manually:\n```terminal\n{\"command\":\"sudo apt install chrome\"}\n```"},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Which Chrome build should I use?"}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("router no-tool decision was overridden: %#v", message)
	}
	if got := fmt.Sprint(message["content"]); !strings.Contains(got, "sudo apt install chrome") {
		t.Fatalf("ordinary answer was not preserved: %q", got)
	}
}

func TestTextOnlyFallbackRequestClearsAllToolInputs(t *testing.T) {
	body := oaiReq{
		Stream:       true,
		Tools:        terminalCatalog(),
		Functions:    []json.RawMessage{json.RawMessage(`{"name":"legacy"}`)},
		ToolChoice:   "auto",
		FunctionCall: map[string]any{"name": "legacy"},
	}
	fallback := textOnlyFallbackRequest(body)
	if fallback.Stream || len(fallback.Tools) != 0 || len(fallback.Functions) != 0 || fallback.ToolChoice != "none" || fallback.FunctionCall != nil {
		t.Fatalf("tool inputs remained in text fallback: %#v", fallback)
	}
	if !body.Stream || len(body.Tools) != 1 || len(body.Functions) != 1 || body.ToolChoice != "auto" || body.FunctionCall == nil {
		t.Fatalf("source request was mutated: %#v", body)
	}
}

func TestStreamingUnknownNativeToolFailsClosed(t *testing.T) {
	chat := &routerStreamChat{}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Which Chrome build should I use?"}],
		"stream":true,
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router and streamed answer", len(chat.requests))
	}
	if len(chat.requests[1].Tools) != 0 || chat.requests[1].ToolChoice != nil {
		t.Fatalf("router text answer still exposed tools: tools=%d choice=%#v", len(chat.requests[1].Tools), chat.requests[1].ToolChoice)
	}
	stream := rr.Body.String()
	if strings.Contains(stream, `"name":"bash"`) || strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("unregistered bash tool leaked into stream: %s", stream)
	}
	if got := continuationStreamText(t, stream); got != "" {
		t.Fatalf("blocked response emitted text=%q body=%s", got, stream)
	}
	if !strings.Contains(stream, `"code":"native_mutation_blocked"`) {
		t.Fatalf("unknown native tool did not fail closed: %s", stream)
	}
}
