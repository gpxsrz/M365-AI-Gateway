package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func issue65SSEObjects(t *testing.T, body string) ([]map[string]any, int) {
	t.Helper()
	var objects []map[string]any
	done := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			done++
			continue
		}
		var object map[string]any
		if err := json.Unmarshal([]byte(payload), &object); err != nil {
			t.Fatalf("decode SSE payload: %v; payload=%s", err, payload)
		}
		objects = append(objects, object)
	}
	return objects, done
}

func issue65UsageChunk(t *testing.T, objects []map[string]any) map[string]any {
	t.Helper()
	var usageChunk map[string]any
	for _, object := range objects {
		choices, _ := object["choices"].([]any)
		usage, hasUsage := object["usage"]
		if len(choices) == 0 && hasUsage && usage != nil {
			if usageChunk != nil {
				t.Fatal("more than one usage-bearing SSE chunk")
			}
			usageChunk = object
		}
	}
	if usageChunk == nil {
		t.Fatal("missing final usage-bearing SSE chunk")
	}
	return usageChunk
}

func TestIssue65StreamOptionsAreRecognizedAndStrictlyValidated(t *testing.T) {
	var body oaiReq
	if err := json.Unmarshal([]byte(`{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`), &body); err != nil {
		t.Fatal(err)
	}
	if _, preserved := body.IngressExtensions["stream_options"]; preserved {
		t.Fatal("stream_options remained a preserved unknown top-level extension")
	}
	options, err := parseChatStreamOptions(body.StreamOptions, body.Stream)
	if err != nil {
		t.Fatalf("parseChatStreamOptions() err=%v", err)
	}
	if !options.IncludeUsage {
		t.Fatal("include_usage=true was not recognized")
	}

	for _, raw := range []string{
		`{"include_usage":"true"}`,
		`{"include_usage":1}`,
		`{"include_usage":null}`,
		`{"include_obfuscation":null}`,
		`{"include_usage":true,"future_flag":true}`,
	} {
		if _, err := parseChatStreamOptions(json.RawMessage(raw), true); err == nil {
			t.Fatalf("invalid stream_options accepted: %s", raw)
		}
	}
	if _, err := parseChatStreamOptions(json.RawMessage(`{"include_usage":true}`), false); err == nil {
		t.Fatal("stream_options accepted when stream=false")
	}

	options, err = parseChatStreamOptions(json.RawMessage(`{"include_obfuscation":false}`), true)
	if err != nil || !options.IncludeObfuscationSet {
		t.Fatalf("current OpenAI include_obfuscation field should be recognized-but-ignored: options=%+v err=%v", options, err)
	}
}

func TestIssue65BufferedStreamIncludesUsageBeforeDone(t *testing.T) {
	response := map[string]any{
		"id":      "chatcmpl-buffered",
		"model":   "gpt-5.6-reasoning",
		"created": float64(1234),
		"choices": []any{map[string]any{
			"index": float64(0),
			"message": map[string]any{
				"role": "assistant", "content": "buffered-answer",
			},
			"finish_reason": "stop",
		}},
		"m365": map[string]any{"usage_source": "unavailable_from_chathub"},
	}
	usage := chatCompletionStreamUsage{
		Include: true, Model: "gpt-5.6-reasoning",
		Input: []oaiMsg{{Role: "user", Content: "buffered-question"}},
	}
	rr := httptest.NewRecorder()
	if err := writeBufferedChatCompletionStreamWithUsage(rr, response, routeResolution{}, nil, nil, usage); err != nil {
		t.Fatal(err)
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d body=%s", done, rr.Body.String())
	}
	usageChunk := issue65UsageChunk(t, objects)
	metadata, _ := usageChunk["m365"].(map[string]any)
	if metadata["usage_source"] != usageSourceTiktoken || metadata["usage_values_are_estimates"] != true {
		t.Fatalf("buffered usage provenance=%#v", metadata)
	}
	for _, object := range objects {
		choices, _ := object["choices"].([]any)
		if len(choices) > 0 {
			if value, ok := object["usage"]; !ok || value != nil {
				t.Fatalf("buffered ordinary chunk lacks usage:null: %#v", object)
			}
		}
	}
}

func TestIssue68HermesBufferedToolContinuationPreservesOuterStreamUsage(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "The lookup result is available."}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"stream_options":{"include_usage":true},
		"messages":[
			{"role":"user","content":"Look up x."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_lookup","content":"{\"answer\":\"ok\"}"},
			{"role":"user","content":"Summarize the result without repeating the lookup."}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"lookup",
				"description":"Look up a value.",
				"parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}
			}
		}],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	server.hermesOpenAIChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d body=%s", done, rr.Body.String())
	}
	usageChunk := issue65UsageChunk(t, objects)
	usage, _ := usageChunk["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) <= 0 || usage["completion_tokens"].(float64) <= 0 {
		t.Fatalf("buffered continuation usage=%#v", usage)
	}
	for _, object := range objects {
		choices, _ := object["choices"].([]any)
		if len(choices) > 0 {
			if value, ok := object["usage"]; !ok || value != nil {
				t.Fatalf("buffered continuation ordinary chunk lacks usage:null: %#v", object)
			}
		}
	}
}

func TestIssue65StreamOptionsRequireStreamingAtHTTPBoundary(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "should-not-run"}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":false,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_stream_options") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("stream_options with stream=false reached upstream: requests=%d", len(chat.requests))
	}
}

func TestIssue65IncludeObfuscationIsRecognizedButExplicitlyIgnored(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "ok"}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":false,"include_obfuscation":false},"messages":[{"role":"user","content":"hello"}]}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-M365-Ignored-Parameters"); !strings.Contains(got, "stream_options.include_obfuscation") {
		t.Fatalf("include_obfuscation was silently ignored: %q", got)
	}
	if strings.Contains(rr.Header().Get("X-M365-Preserved-Extension-Names"), "stream_options") {
		t.Fatalf("stream_options incorrectly preserved as unknown: %q", rr.Header().Get("X-M365-Preserved-Extension-Names"))
	}
}

func TestIssue65TextStreamIncludeUsage(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "hello", ConversationID: "conv", SessionID: "session", RequestID: "request"}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Header().Get("X-M365-Preserved-Extension-Names"), "stream_options") {
		t.Fatalf("stream_options still exposed as preserved extension: %q", rr.Header().Get("X-M365-Preserved-Extension-Names"))
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d body=%s", done, rr.Body.String())
	}
	usageChunk := issue65UsageChunk(t, objects)
	usage, _ := usageChunk["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) <= 0 || usage["completion_tokens"].(float64) <= 0 || usage["total_tokens"].(float64) <= 0 {
		t.Fatalf("usage=%#v", usage)
	}
	metadata, _ := usageChunk["m365"].(map[string]any)
	if metadata["usage_source"] != usageSourceTiktoken || metadata["usage_values_are_estimates"] != true || metadata["usage_estimate_scope"] != "visible_request_and_completion" {
		t.Fatalf("usage provenance=%#v", metadata)
	}
	var content strings.Builder
	for _, object := range objects {
		choices, _ := object["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		if usage, exists := object["usage"]; !exists || usage != nil {
			t.Fatalf("ordinary include_usage chunk must carry usage:null: %#v", object)
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if part, _ := delta["content"].(string); part != "" {
			content.WriteString(part)
		}
	}
	if content.String() != "hello" {
		t.Fatalf("reconstructed content=%q", content.String())
	}
}

func TestIssue65IncludeUsageFalseDoesNotAddUsageChunk(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "plain"}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hello"}]}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d", done)
	}
	for _, object := range objects {
		choices, _ := object["choices"].([]any)
		if len(choices) == 0 && object["usage"] != nil {
			t.Fatalf("unexpected usage chunk: %#v", object)
		}
	}
}

func TestIssue65ToolCallStreamIncludesUsageBeforeDone(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{ConversationID: "conv", SessionID: "session", RequestID: "request"},
		events: []chathub.StreamEvent{{Kind: "tool", ToolName: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}},
	}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"lookup x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"tool_choice":"auto"}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d body=%s", done, rr.Body.String())
	}
	usageChunk := issue65UsageChunk(t, objects)
	usage, _ := usageChunk["usage"].(map[string]any)
	if usage["completion_tokens"].(float64) <= 0 {
		t.Fatalf("tool-call completion usage=%#v", usage)
	}
	if !strings.Contains(rr.Body.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("tool stream terminal semantics regressed: %s", rr.Body.String())
	}
}

func TestIssue65RouterToolCallStreamIncludesUsageBeforeDone(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:           `{"calls":[{"name":"lookup","arguments":{"q":"x"}}]}`,
		ConversationID: "scratch-conv", SessionID: "scratch-session", RequestID: "scratch-request",
	}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "router"
	server.settings.v = settings
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"lookup x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"tool_choice":"auto"}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	objects, done := issue65SSEObjects(t, rr.Body.String())
	if done != 1 {
		t.Fatalf("[DONE] count=%d body=%s", done, rr.Body.String())
	}
	usageChunk := issue65UsageChunk(t, objects)
	usage, _ := usageChunk["usage"].(map[string]any)
	if usage["completion_tokens"].(float64) <= 0 {
		t.Fatalf("router tool completion usage=%#v", usage)
	}
	if !strings.Contains(rr.Body.String(), `"finish_reason":"tool_calls"`) {
		t.Fatalf("router tool stream terminal semantics regressed: %s", rr.Body.String())
	}
}

func TestIssue65InvalidStreamOptionsRejectBeforeUpstream(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "should-not-run"}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":true,"stream_options":{"include_usage":"yes"},"messages":[{"role":"user","content":"hello"}]}`
	rr := httptest.NewRecorder()
	server.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid_stream_options") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("invalid stream_options reached upstream: requests=%d", len(chat.requests))
	}
}
