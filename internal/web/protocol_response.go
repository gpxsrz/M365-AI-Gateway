package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func openAIChoice(v map[string]any) (map[string]any, string) {
	choices, _ := v["choices"].([]any)
	if len(choices) == 0 {
		return nil, ""
	}
	c, _ := choices[0].(map[string]any)
	m, _ := c["message"].(map[string]any)
	finish, _ := c["finish_reason"].(string)
	return m, finish
}

func anthropicM365Metadata(src map[string]any) map[string]any {
	metadata := map[string]any{
		"usage_source":                  "unavailable_from_chathub",
		"usage_values_are_placeholders": true,
	}
	if existing, ok := src["m365"].(map[string]any); ok {
		for key, value := range existing {
			metadata[key] = value
		}
	}
	return metadata
}

type anthropicResultProjection struct {
	blocks []any
	stop   string
}

func anthropicContentBlocks(content any) ([]any, error) {
	appendText := func(blocks []any, text string) []any {
		return append(blocks, map[string]any{"type": "text", "text": text})
	}
	var blocks []any
	switch value := content.(type) {
	case nil:
		return blocks, nil
	case string:
		if value != "" {
			blocks = appendText(blocks, value)
		}
		return blocks, nil
	case []any:
		for index, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unsupported Anthropic assistant content block at index %d", index)
			}
			typ, _ := block["type"].(string)
			switch typ {
			case "text", "output_text":
				text, ok := block["text"].(string)
				if !ok {
					return nil, fmt.Errorf("Anthropic assistant text block at index %d is missing string text", index)
				}
				blocks = appendText(blocks, text)
			case "image", "image_url", "output_image":
				return nil, fmt.Errorf("Anthropic assistant image content is unsupported; refusing to stringify structured image data")
			default:
				return nil, fmt.Errorf("unsupported Anthropic assistant content block type %q at index %d", typ, index)
			}
		}
		return blocks, nil
	default:
		return nil, fmt.Errorf("unsupported Anthropic assistant content type %T", content)
	}
}

func projectAnthropicResult(src map[string]any) (anthropicResultProjection, error) {
	msg, _ := openAIChoice(src)
	if msg == nil {
		return anthropicResultProjection{}, fmt.Errorf("Anthropic result is missing an assistant message")
	}
	blocks, err := anthropicContentBlocks(msg["content"])
	if err != nil {
		return anthropicResultProjection{}, err
	}
	stop := "end_turn"
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) > 0 {
		stop = "tool_use"
		for index, raw := range calls {
			tc, ok := raw.(map[string]any)
			if !ok {
				return anthropicResultProjection{}, fmt.Errorf("invalid Anthropic tool call at index %d", index)
			}
			fn, ok := tc["function"].(map[string]any)
			if !ok {
				return anthropicResultProjection{}, fmt.Errorf("invalid Anthropic tool call function at index %d", index)
			}
			var input any = map[string]any{}
			if arguments, ok := fn["arguments"].(string); ok && strings.TrimSpace(arguments) != "" {
				if err := json.Unmarshal([]byte(arguments), &input); err != nil {
					return anthropicResultProjection{}, fmt.Errorf("invalid Anthropic tool call arguments at index %d: %w", index, err)
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc["id"], "name": fn["name"], "input": input})
		}
	}
	return anthropicResultProjection{blocks: blocks, stop: stop}, nil
}

func writeAnthropicProjection(w http.ResponseWriter, model string, stream bool, src map[string]any, projection anthropicResultProjection) {
	id := "msg_" + uuid.NewString()
	metadata := anthropicM365Metadata(src)
	out := map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": projection.blocks, "stop_reason": projection.stop, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}, "m365": metadata}
	if !stream {
		jsonOut(w, out)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	emit := func(n string, v any) {
		writeSSE(w, n, v)
		if f != nil {
			f.Flush()
		}
	}
	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}, "m365": metadata}})
	for i, b := range projection.blocks {
		m, _ := b.(map[string]any)
		startBlock := b
		if m["type"] == "tool_use" {
			startBlock = map[string]any{"type": "tool_use", "id": m["id"], "name": m["name"], "input": map[string]any{}}
		}
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": startBlock})
		if m["type"] == "text" {
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": m["text"]}})
		} else if m["type"] == "tool_use" {
			partial, _ := json.Marshal(m["input"])
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(partial)}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": projection.stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 0}})
	emit("message_stop", map[string]any{"type": "message_stop", "model": model, "m365": metadata})
}
