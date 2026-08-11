package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeLegacyFunctionsAndNamedFunctionCall(t *testing.T) {
	fn := json.RawMessage(`{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}`)
	body := oaiReq{Functions: []json.RawMessage{fn}, FunctionCall: map[string]any{"name": "weather"}}
	normalizeLegacyTools(&body)
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" {
		t.Fatalf("tools=%+v", body.Tools)
	}
	choice, ok := body.ToolChoice.(map[string]any)
	if !ok || choice["name"] != "weather" {
		t.Fatalf("choice=%#v", body.ToolChoice)
	}
	if !toolChoiceAllows(body.ToolChoice, "weather") || toolChoiceAllows(body.ToolChoice, "clock") {
		t.Fatal("legacy named choice not enforced")
	}
}

func toolSource() map[string]any {
	return map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "weather", "arguments": "{\"city\":\"Paris\"}"}}}}}}}
}

func TestAnthropicStructuredTextContentMapsWithoutGoStringification(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			src := map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "structured text"}},
			}}}}
			rr := httptest.NewRecorder()
			if err := writeAnthropicResult(rr, "m", stream, src); err != nil {
				t.Fatal(err)
			}
			body := rr.Body.String()
			if strings.Contains(body, "map[") || !strings.Contains(body, "structured text") {
				t.Fatalf("structured content was stringified incorrectly: %s", body)
			}
			if stream && !strings.Contains(body, `"type":"text_delta"`) {
				t.Fatalf("structured text stream missing text delta: %s", body)
			}
		})
	}
}

func TestAnthropicStructuredImageContentIsRejected(t *testing.T) {
	src := map[string]any{"choices": []any{map[string]any{"message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}},
	}}}}
	for _, stream := range []bool{false, true} {
		rr := httptest.NewRecorder()
		err := writeAnthropicResult(rr, "m", stream, src)
		if err == nil || !strings.Contains(err.Error(), "image") {
			t.Fatalf("structured image stream=%t error=%v, want explicit rejection", stream, err)
		}
		if strings.Contains(rr.Body.String(), "map[") {
			t.Fatalf("structured image was Go-stringified before rejection: %s", rr.Body.String())
		}
	}
}

func TestAnthropicMixedTextAndToolUsePreservesBothBlocks(t *testing.T) {
	src := toolSource()
	message, _ := openAIChoice(src)
	message["content"] = "I will check the weather first."
	rr := httptest.NewRecorder()
	if err := writeAnthropicResult(rr, "m", false, src); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	blocks, _ := out["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("content blocks=%#v", blocks)
	}
	text, _ := blocks[0].(map[string]any)
	tool, _ := blocks[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "I will check the weather first." || tool["type"] != "tool_use" {
		t.Fatalf("mixed Anthropic content was not preserved: %#v", blocks)
	}
}

func TestResponsesToolSSEEvents(t *testing.T) {
	rr := httptest.NewRecorder()
	writeResponsesResult(rr, "m", true, toolSource())
	body := rr.Body.String()
	for _, want := range []string{"event: response.created", "event: response.output_item.added", "event: response.function_call_arguments.delta", "event: response.function_call_arguments.done", "event: response.output_item.done", "event: response.completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}

func TestResponsesToolSSEDoesNotDuplicateArguments(t *testing.T) {
	rr := httptest.NewRecorder()
	writeResponsesResult(rr, "m", true, toolSource())
	body := rr.Body.String()
	if !strings.Contains(body, `"arguments":"","call_id":"call_1"`) {
		t.Fatalf("initial function_call must have empty arguments: %s", body)
	}
	var accumulated string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event["type"] == "response.function_call_arguments.delta" {
			accumulated += event["delta"].(string)
		}
	}
	if accumulated != `{"city":"Paris"}` || !json.Valid([]byte(accumulated)) {
		t.Fatalf("client-accumulated arguments=%q", accumulated)
	}
}

func TestAnthropicToolSSEEvents(t *testing.T) {
	rr := httptest.NewRecorder()
	if err := writeAnthropicResult(rr, "m", true, toolSource()); err != nil {
		t.Fatal(err)
	}
	body := rr.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_start", "\"type\":\"tool_use\"", "\"type\":\"input_json_delta\"", "event: content_block_stop", "event: message_delta", "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
}
