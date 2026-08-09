package web

import (
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const routerFallbackTool = `{
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
}`

func TestAutoToolRouterInvalidDecisionFallsBackToText(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: "I am okay."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Are you okay?"}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and ordinary answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%#v response=%#v", response["choices"], response)
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("invalid auto routing emitted a tool call: %#v", message)
	}
	if got := message["content"]; got != "I am okay." {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestRequiredToolRouterInvalidDecisionUsesConstrainedRetry(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}]}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Check status with the tool."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"required"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and required retry", len(chat.requests))
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
}

func TestStreamingRequiredToolRouterInvalidDecisionUsesConstrainedRetry(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}]}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[{"role":"user","content":"Check status with the tool."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"required"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and required retry", len(chat.requests))
	}
	stream := rr.Body.String()
	if !strings.Contains(stream, `"name":"terminal"`) || !strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("required retry did not emit terminal call: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestStreamingNamedToolRouterNoCallRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{Text: "NO_TOOL_NEEDED"}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want router only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "did not select the requested tool") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestNamedToolRouterInvalidDecisionRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{}, {}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router and repair only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "invalid tool routing decision") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestNamedToolRouterNoCallRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{Text: "NO_TOOL_NEEDED"}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want router only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "did not select the requested tool") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestAutoAnswerDoesNotInventUnregisteredBashTool(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: "NO_TOOL_NEEDED"},
		{Text: "Use the amd64 stable package:\n```bash\n{\"command\":\"sudo apt install ./google-chrome-stable_current_amd64.deb\"}\n```"},
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
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router and ordinary answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("unregistered bash tool was emitted: %#v", message)
	}
	if got := fmt.Sprint(message["content"]); !strings.Contains(got, "google-chrome-stable_current_amd64.deb") {
		t.Fatalf("ordinary answer was not preserved: %q", got)
	}
}

func TestAutoToolRouterFallbackStillEnforcesPendingEvidence(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: "Deployment completed successfully."},
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
			{"role":"user","content":"Are you okay?"}
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
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and guarded answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != unconfirmedToolOutcomeResponse {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}
