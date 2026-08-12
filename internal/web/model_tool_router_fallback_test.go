package web

import (
	"encoding/json"
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

const executeCodeRouterTool = `{
	"type":"function",
	"function":{
		"name":"execute_code",
		"description":"Execute Python code.",
		"parameters":{
			"type":"object",
			"properties":{"code":{"type":"string"}},
			"required":["code"]
		}
	}
}`

func longExecuteCodeRoutingOutput(t *testing.T, repeat int) (string, string) {
	t.Helper()
	const sentinel = "MIDDLE_SENTINEL_REQUIRED_FOR_VALID_PYTHON"
	code := "print('BEGIN')\n" +
		strings.Repeat("# padding before sentinel\n", repeat) +
		"if " + sentinel + " := True:\n" +
		strings.Repeat("    pass  # padding after sentinel\n", repeat) +
		"print('END')\n"
	raw, err := json.Marshal(map[string]any{
		"calls": []any{map[string]any{
			"name":      "execute_code",
			"arguments": map[string]any{"code": code},
		}},
		"answer": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), sentinel
}

func TestAutoToolRouterLongValidStructuredArgumentsBypassRepair(t *testing.T) {
	routeOutput, sentinel := longExecuteCodeRoutingOutput(t, 180)
	if len(routeOutput) <= 6000 {
		t.Fatalf("fixture must exceed historical repair preview limit, got %d bytes", len(routeOutput))
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{{Text: routeOutput}}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 1 {
				t.Fatalf("upstream requests=%d, valid routing JSON must not enter repair", len(chat.requests))
			}
			if !strings.Contains(rr.Body.String(), sentinel) || !strings.Contains(rr.Body.String(), `"name":"execute_code"`) {
				t.Fatalf("long execute_code call lost identity or sentinel: %s", rr.Body.String())
			}
		})
	}
}

func TestAutoToolRouterRepairReceivesCompleteLongStructuredArguments(t *testing.T) {
	validOutput, sentinel := longExecuteCodeRoutingOutput(t, 180)
	malformedOutput := strings.TrimSuffix(validOutput, "}")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{
				{Text: malformedOutput},
				{Text: validOutput},
			}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 2 {
				t.Fatalf("upstream requests=%d, want router and repair", len(chat.requests))
			}
			repairPrompt := chat.requests[1].Text
			if !strings.Contains(repairPrompt, sentinel) || !strings.HasSuffix(repairPrompt, malformedOutput) {
				t.Fatalf("repair input was not lossless: prompt_len=%d output_len=%d sentinel=%t", len(repairPrompt), len(malformedOutput), strings.Contains(repairPrompt, sentinel))
			}
			if !strings.Contains(rr.Body.String(), sentinel) {
				t.Fatalf("repaired execute_code call lost sentinel: %s", rr.Body.String())
			}
		})
	}
}

func TestAutoToolRouterOversizeRepairFailsClosedBeforeSecondUpstreamCall(t *testing.T) {
	validOutput, _ := longExecuteCodeRoutingOutput(t, 2300)
	malformedOutput := strings.TrimSuffix(validOutput, "}")
	if utf16CodeUnits(malformedOutput) <= defaultTextInputLimitUTF16 {
		t.Fatalf("fixture must exceed repair budget before adding instructions, got %d UTF-16 units", utf16CodeUnits(malformedOutput))
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{{Text: malformedOutput}}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 1 {
				t.Fatalf("upstream requests=%d, oversize repair must fail before a second upstream call", len(chat.requests))
			}
			response := wp1DecodeJSON(t, rr.Body.String())
			errBody, _ := response["error"].(map[string]any)
			limit, limitOK := errBody["limit"].(float64)
			received, receivedOK := errBody["received"].(float64)
			if errBody["code"] != "tool_router_repair_input_too_large" ||
				errBody["limit_type"] != "repair_prompt_utf16" ||
				errBody["terminal"] != true ||
				errBody["retryable"] != false ||
				errBody["recommended_action"] != "regenerate_tool_routing_decision" ||
				!limitOK || int(limit) != defaultTextInputLimitUTF16 ||
				!receivedOK || int(received) <= defaultTextInputLimitUTF16 {
				t.Fatalf("unexpected machine-readable repair failure: %#v", errBody)
			}
		})
	}
}

func TestAutoToolRouterSchemaInvalidDecisionDoesNotEnterRepair(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":2}}],"answer":""}`},
		{Text: "The proposed tool arguments were invalid."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Check status."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, schema-invalid but parseable JSON must skip repair and continue to answer", len(chat.requests))
	}
	if strings.HasPrefix(chat.requests[1].Text, "Repair the previous tool-routing output") {
		t.Fatalf("schema-invalid decision incorrectly entered repair: %s", chat.requests[1].Text)
	}
}

func TestAutoToolRouterFilteredKnownCallDoesNotEnterRepair(t *testing.T) {
	routeOutput := `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`
	chat := &continuationChat{results: []chathub.Result{
		{Text: routeOutput},
		{Text: "Status was already checked."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Check status."},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_status","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"status\"}"}}]},
			{"role":"tool","tool_call_id":"call_status","content":"ok"},
			{"role":"user","content":"Continue."}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, filtered duplicate must skip repair and continue to answer", len(chat.requests))
	}
	if strings.HasPrefix(chat.requests[1].Text, "Repair the previous tool-routing output") {
		t.Fatalf("filtered duplicate incorrectly entered repair: %s", chat.requests[1].Text)
	}
}

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
