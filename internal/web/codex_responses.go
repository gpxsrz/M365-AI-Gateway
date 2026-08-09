package web

import (
	"encoding/json"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

func responsesMessageContentBlocks(content any) []any {
	blocks := []any{}
	appendText := func(text string) {
		if text != "" {
			blocks = append(blocks, map[string]any{"type": "output_text", "text": text, "annotations": []any{}})
		}
	}
	appendImage := func(url string) {
		if chathub.IsImageURL(url) {
			blocks = append(blocks, map[string]any{"type": "output_image", "image_url": strings.TrimSpace(url)})
		}
	}
	switch value := content.(type) {
	case string:
		appendText(value)
	case []any:
		for _, raw := range value {
			block, _ := raw.(map[string]any)
			typ, _ := block["type"].(string)
			switch typ {
			case "text", "output_text":
				text, _ := block["text"].(string)
				appendText(text)
			case "image_url", "output_image":
				if direct, _ := block["image_url"].(string); direct != "" {
					appendImage(direct)
					continue
				}
				image, _ := block["image_url"].(map[string]any)
				url, _ := image["url"].(string)
				appendImage(url)
			}
		}
	}
	return blocks
}

func responsesM365Metadata(src map[string]any, usageSource string) map[string]any {
	metadata := map[string]any{}
	if existing, ok := src["m365"].(map[string]any); ok {
		for key, value := range existing {
			metadata[key] = value
		}
	}
	for key, value := range localUsageMetadata(usageSource) {
		metadata[key] = value
	}
	return metadata
}

func responsesReasoningText(content any) string {
	text, _ := content.(string)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func responsesReasoningItem(id, text, status string) map[string]any {
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return map[string]any{
		"type": "reasoning", "id": id, "status": status,
		"summary": summary,
	}
}

// writeResponsesResult projects an internal OpenAI-style result into the
// Responses events and completion shape consumed by Codex.
func writeResponsesResult(w http.ResponseWriter, model string, stream bool, src map[string]any) {
	id := firstNonEmpty(fmt.Sprint(src["m365_response_id"]), "resp_"+uuid.NewString())
	msg, _ := openAIChoice(src)
	var output []any
	calls, _ := msg["tool_calls"].([]any)
	reasoning := responsesReasoningText(msg["reasoning_content"])
	if reasoning != "" {
		output = append(output, responsesReasoningItem("rs_"+uuid.NewString(), reasoning, "completed"))
	}
	content := responsesMessageContentBlocks(msg["content"])
	if len(content) > 0 {
		output = append(output, map[string]any{"type": "message", "id": "msg_" + uuid.NewString(), "role": "assistant", "status": "completed", "content": content})
	} else if len(calls) > 0 {
		output = append(output, map[string]any{"type": "message", "id": "msg_" + uuid.NewString(), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": toolPlanSummaryFromMaps(calls), "annotations": []any{}}}})
	}
	for _, raw := range calls {
		tc, _ := raw.(map[string]any)
		fn, _ := tc["function"].(map[string]any)
		if tc["type"] == "custom" {
			output = append(output, map[string]any{"type": "custom_tool_call", "id": "ctc_" + uuid.NewString(), "call_id": tc["id"], "name": fn["name"], "input": customToolInput(fn["arguments"]), "status": "completed"})
			continue
		}
		output = append(output, map[string]any{"type": "function_call", "id": "fc_" + uuid.NewString(), "call_id": tc["id"], "name": fn["name"], "arguments": fn["arguments"], "status": "completed"})
	}
	usage, _ := src["usage"].(map[string]any)
	usageSource, _ := src["m365_usage_source"].(string)
	if usage == nil {
		estimate := estimateResponsesUsage(model, nil, nil, nil, fmt.Sprint(msg["content"]))
		usage = estimate.Values
		usageSource = estimate.Source
	}
	if usageSource == "" {
		usageSource = usageSourceHeuristic
	}
	resp := map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": model, "output": output, "usage": usage, "m365": responsesM365Metadata(src, usageSource)}
	if !stream {
		jsonOut(w, resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	emit := func(name string, v any) {
		writeSSE(w, name, v)
		if f != nil {
			f.Flush()
		}
	}
	emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "model": model, "output": []any{}}})
	for i, item := range output {
		m, _ := item.(map[string]any)
		addedItem := item
		if m["type"] == "reasoning" {
			addedItem = responsesReasoningItem(fmt.Sprint(m["id"]), "", "in_progress")
		} else if m["type"] == "function_call" {
			// Arguments arrive in function_call_arguments.delta. Including them
			// here too would make conforming clients append duplicate JSON.
			added := make(map[string]any, len(m))
			for k, v := range m {
				added[k] = v
			}
			added["arguments"] = ""
			added["status"] = "in_progress"
			addedItem = added
		}
		emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": i, "item": addedItem})
		if m["type"] == "reasoning" {
			summaries, _ := m["summary"].([]any)
			if len(summaries) > 0 {
				summary, _ := summaries[0].(map[string]any)
				text, _ := summary["text"].(string)
				part := map[string]any{"type": "summary_text", "text": text}
				emit("response.reasoning_summary_part.added", map[string]any{"type": "response.reasoning_summary_part.added", "output_index": i, "item_id": m["id"], "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
				emit("response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": i, "item_id": m["id"], "summary_index": 0, "delta": text})
				emit("response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "output_index": i, "item_id": m["id"], "summary_index": 0, "text": text})
				emit("response.reasoning_summary_part.done", map[string]any{"type": "response.reasoning_summary_part.done", "output_index": i, "item_id": m["id"], "summary_index": 0, "part": part})
			}
		} else if m["type"] == "message" {
			content, _ := m["content"].([]any)
			for contentIndex, rawContent := range content {
				c, _ := rawContent.(map[string]any)
				if c["type"] != "output_text" {
					continue
				}
				emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": i, "content_index": contentIndex, "delta": c["text"]})
			}
		} else if m["type"] == "function_call" {
			args, _ := m["arguments"].(string)
			emit("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": i, "item_id": m["id"], "delta": args})
			emit("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": i, "item_id": m["id"], "arguments": args})
		} else if m["type"] == "custom_tool_call" {
			input, _ := m["input"].(string)
			emit("response.custom_tool_call_input.delta", map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": i, "item_id": m["id"], "delta": input})
			emit("response.custom_tool_call_input.done", map[string]any{"type": "response.custom_tool_call_input.done", "output_index": i, "item_id": m["id"], "input": input})
		}
		emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
	}
	emit("response.completed", map[string]any{"type": "response.completed", "response": resp})
}

func customToolInput(arguments any) string {
	if s, ok := arguments.(string); ok {
		var v struct {
			Input string `json:"input"`
		}
		if json.Unmarshal([]byte(s), &v) == nil {
			return v.Input
		}
	}
	return ""
}
