package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"m365-native/internal/chathub"
)

type chatStreamOptions struct {
	IncludeUsage          bool
	IncludeObfuscation    bool
	IncludeObfuscationSet bool
}

func parseChatStreamOptions(raw json.RawMessage, stream bool) (chatStreamOptions, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return chatStreamOptions{}, nil
	}
	if !stream {
		return chatStreamOptions{}, fmt.Errorf("stream_options requires stream=true")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return chatStreamOptions{}, fmt.Errorf("stream_options must be an object")
	}
	if fields == nil {
		return chatStreamOptions{}, fmt.Errorf("stream_options must be an object")
	}
	var options chatStreamOptions
	for name, value := range fields {
		switch name {
		case "include_usage":
			if strings.TrimSpace(string(value)) == "null" {
				return chatStreamOptions{}, fmt.Errorf("stream_options.include_usage must be boolean")
			}
			if err := json.Unmarshal(value, &options.IncludeUsage); err != nil {
				return chatStreamOptions{}, fmt.Errorf("stream_options.include_usage must be boolean")
			}
		case "include_obfuscation":
			if strings.TrimSpace(string(value)) == "null" {
				return chatStreamOptions{}, fmt.Errorf("stream_options.include_obfuscation must be boolean")
			}
			if err := json.Unmarshal(value, &options.IncludeObfuscation); err != nil {
				return chatStreamOptions{}, fmt.Errorf("stream_options.include_obfuscation must be boolean")
			}
			options.IncludeObfuscationSet = true
		default:
			return chatStreamOptions{}, fmt.Errorf("unsupported stream_options field %q", name)
		}
	}
	return options, nil
}

type chatCompletionStreamUsage struct {
	Include    bool
	Model      string
	Input      []oaiMsg
	Tools      []chathub.Tool
	ToolChoice any
}

func newChatCompletionStreamUsage(options chatStreamOptions, model string, body oaiReq) chatCompletionStreamUsage {
	return chatCompletionStreamUsage{
		Include: options.IncludeUsage, Model: model,
		Input: body.Messages, Tools: body.Tools, ToolChoice: body.ToolChoice,
	}
}

func (usage chatCompletionStreamUsage) estimate(output string, calls []detectedToolCall) responsesUsageEstimate {
	if len(calls) > 0 {
		var completion strings.Builder
		completion.WriteString(output)
		for _, call := range calls {
			completion.WriteString("\n")
			completion.WriteString(call.Name)
			completion.WriteString(string(call.Arguments))
		}
		output = completion.String()
	}
	return estimateResponsesUsage(usage.Model, usage.Input, usage.Tools, usage.ToolChoice, output)
}

func chatCompletionVisibleOutput(text, reasoning string) string {
	if strings.TrimSpace(reasoning) == "" {
		return text
	}
	if text == "" {
		return reasoning
	}
	return text + "\n" + reasoning
}

func chatCompletionUsageValues(estimate responsesUsageEstimate) map[string]any {
	return map[string]any{
		"prompt_tokens":     estimate.Values["input_tokens"],
		"completion_tokens": estimate.Values["output_tokens"],
		"total_tokens":      estimate.Values["total_tokens"],
	}
}

func cloneMetadataMap(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata)+4)
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func chatCompletionUsageMetadata(metadata map[string]any, source string) map[string]any {
	out := cloneMetadataMap(metadata)
	for key, value := range localUsageMetadata(source) {
		out[key] = value
	}
	return out
}

func addChatCompletionUsageNull(chunk map[string]any, usage chatCompletionStreamUsage) {
	if usage.Include {
		chunk["usage"] = nil
	}
}

func writeChatCompletionUsageChunk(w http.ResponseWriter, id, model string, created any, usage chatCompletionStreamUsage, output string, calls []detectedToolCall, metadata map[string]any) {
	if !usage.Include {
		return
	}
	if created == nil {
		created = time.Now().Unix()
	}
	estimate := usage.estimate(output, calls)
	chunk := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{}, "usage": chatCompletionUsageValues(estimate),
		"m365": chatCompletionUsageMetadata(metadata, estimate.Source),
	}
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
