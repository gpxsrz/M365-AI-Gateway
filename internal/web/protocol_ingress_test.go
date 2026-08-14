package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesIngressPreservesFutureItemsAndExtensions(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.6-reasoning",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"future_response_block","payload":{"id":9007199254740993}}],"future_message_state":{"cursor":"opaque"}},
			{"type":"future_input_item","opaque":{"id":9007199254740993}}
		],
		"tools":[{"type":"function","name":"lookup","description":"find","parameters":{"type":"object"},"future_tool_state":{"readOnlyHint":true}}],
		"future_response_extension":{"opaque":"keep"}
	}`)
	var request responsesRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(request.IngressRaw, []byte(`"future_response_extension"`)) ||
		!bytes.Contains(request.IngressExtensions["future_response_extension"], []byte(`"keep"`)) {
		t.Fatalf("responses top-level evidence lost: raw=%s extensions=%v", request.IngressRaw, request.IngressExtensions)
	}
	if len(request.InputEvidence) != 2 {
		t.Fatalf("input evidence=%d", len(request.InputEvidence))
	}
	first := request.InputEvidence[0]
	if first.Type != "message" || !bytes.Contains(first.Extensions["future_message_state"], []byte(`"opaque"`)) {
		t.Fatalf("responses message evidence=%+v", first)
	}
	if len(first.UnknownContentParts) != 1 || first.UnknownContentTypes[0] != "future_response_block" {
		t.Fatalf("responses unknown content=%v/%v", first.UnknownContentParts, first.UnknownContentTypes)
	}
	second := request.InputEvidence[1]
	if second.Type != "future_input_item" || !second.UnsupportedType || !bytes.Contains(second.Raw, []byte(`9007199254740993`)) {
		t.Fatalf("future responses item not preserved: %+v", second)
	}
	if len(request.ToolEvidence) != 1 || !bytes.Contains(request.ToolEvidence[0].Extensions["future_tool_state"], []byte(`"readOnlyHint":true`)) {
		t.Fatalf("responses tool evidence=%+v", request.ToolEvidence)
	}
	rr := httptest.NewRecorder()
	setIngressEvidenceSummaryHeaders(rr, summarizeResponsesIngressEvidence(request))
	if got := rr.Header().Get("X-M365-Preserved-Extension-Counts"); got != "top=1,message=1,item=2,content=1,tool=1,format=0,reasoning=0" {
		t.Fatalf("responses preserved counts=%q", got)
	}
	for _, want := range []string{"item-type:future_input_item", "item:opaque"} {
		if !strings.Contains(rr.Header().Get("X-M365-Preserved-Extension-Names"), want) {
			t.Fatalf("responses preserved names=%q missing %q", rr.Header().Get("X-M365-Preserved-Extension-Names"), want)
		}
	}

	canonical, err := request.openAI()
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical.Messages) != 1 {
		t.Fatalf("unsupported future input item must not become a model message: %#v", canonical.Messages)
	}
	prompt, _ := flattenPromptMessages(canonical.Messages, nil)
	if strings.Contains(prompt, "future_input_item") || strings.Contains(prompt, "future_response_block") || strings.Contains(prompt, "9007199254740993") {
		t.Fatalf("unsupported Responses evidence leaked into ChatHub projection: %s", prompt)
	}
}

func TestAnthropicIngressPreservesFutureBlocksAndExtensions(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.6-reasoning",
		"max_tokens":128,
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"future_anthropic_block","payload":{"id":9007199254740993}}],"future_message_state":{"opaque":"keep"}}],
		"tools":[{"name":"lookup","description":"find","input_schema":{"type":"object"},"annotations":{"read_only":true},"future_tool_state":{"opaque":"keep"}}],
		"future_anthropic_extension":{"opaque":"keep"}
	}`)
	var request anthropicRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(request.IngressRaw, []byte(`"future_anthropic_extension"`)) ||
		!bytes.Contains(request.IngressExtensions["future_anthropic_extension"], []byte(`"keep"`)) {
		t.Fatalf("anthropic top-level evidence lost: raw=%s extensions=%v", request.IngressRaw, request.IngressExtensions)
	}
	if len(request.Messages) != 1 || !bytes.Contains(request.Messages[0].IngressExtensions["future_message_state"], []byte(`"keep"`)) {
		t.Fatalf("anthropic message evidence=%+v", request.Messages)
	}
	if len(request.Messages[0].UnknownContentParts) != 1 || request.Messages[0].UnknownContentTypes[0] != "future_anthropic_block" {
		t.Fatalf("anthropic unknown content=%v/%v", request.Messages[0].UnknownContentParts, request.Messages[0].UnknownContentTypes)
	}
	if len(request.Tools) != 1 || !bytes.Contains(request.Tools[0].IngressExtensions["future_tool_state"], []byte(`"keep"`)) {
		t.Fatalf("anthropic tool evidence=%+v", request.Tools)
	}
	rr := httptest.NewRecorder()
	setIngressEvidenceSummaryHeaders(rr, summarizeAnthropicIngressEvidence(request))
	if got := rr.Header().Get("X-M365-Preserved-Extension-Counts"); got != "top=1,message=1,item=0,content=1,tool=1,format=0,reasoning=0" {
		t.Fatalf("anthropic preserved counts=%q", got)
	}

	canonical, err := request.openAI()
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := flattenPromptMessages(canonical.Messages, nil)
	if strings.Contains(prompt, "future_anthropic_block") || strings.Contains(prompt, "9007199254740993") || strings.Contains(prompt, "future_message_state") {
		t.Fatalf("unsupported Anthropic evidence leaked into ChatHub projection: %s", prompt)
	}
}

func TestResponseFormatIngressPreservesOuterExtensions(t *testing.T) {
	raw := []byte(`{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"]}},"future_format_control":{"opaque":"keep"}}`)
	var format responseFormat
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&format); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(format.IngressRaw, []byte(`"future_format_control"`)) ||
		!bytes.Contains(format.IngressExtensions["future_format_control"], []byte(`"keep"`)) {
		t.Fatalf("response_format evidence lost: raw=%s extensions=%v", format.IngressRaw, format.IngressExtensions)
	}
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	idSchema, _ := properties["id"].(map[string]any)
	if got := idSchema["const"]; got == nil || got.(json.Number).String() != "9007199254740993" {
		t.Fatalf("schema numeric identity changed: %#v", got)
	}
	canonical, err := json.Marshal(format)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("future_format_control")) {
		t.Fatalf("request-scoped response_format evidence leaked into canonical serialization: %s", canonical)
	}
	rr := httptest.NewRecorder()
	setIngressEvidenceSummaryHeaders(rr, callerIngressEvidenceSummary{Format: len(format.IngressExtensions), Names: []string{"format:future_format_control"}})
	if got := rr.Header().Get("X-M365-Preserved-Extension-Counts"); got != "top=0,message=0,item=0,content=0,tool=0,format=1,reasoning=0" {
		t.Fatalf("response_format preserved counts=%q", got)
	}
}
