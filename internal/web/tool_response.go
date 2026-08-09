package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"strings"
	"time"
)

// toolPlanSummary tells the client what will happen before the structured call.
// It must describe the concrete operation, rather than repeat a generic phrase.
func toolPlanSummary(calls []detectedToolCall) string {
	if len(calls) == 0 {
		return "我将整理当前请求并继续处理。"
	}
	plans := make([]string, 0, len(calls))
	for _, c := range calls {
		plans = append(plans, toolPlan(c))
	}
	return strings.Join(plans, "\n\n")
}

func toolPlan(c detectedToolCall) string {
	return toolPlanFor(c.Name, c.Arguments)
}

func toolPlanFor(name string, arguments []byte) string {
	var args map[string]any
	_ = json.Unmarshal(arguments, &args)
	verb := "调用 " + name
	purpose := "获取该工具返回的信息"
	var target string
	for _, key := range []string{"command", "cmd", "path", "query", "url", "input", "prompt"} {
		if s, ok := args[key].(string); ok && strings.TrimSpace(s) != "" {
			target = strings.TrimSpace(s)
			break
		}
	}
	switch {
	case strings.Contains(name, "shell") || strings.Contains(name, "exec") || strings.Contains(name, "command"):
		verb = "执行工作区命令"
		purpose = "读取项目状态、运行检查或完成用户指定的命令"
	case strings.Contains(name, "read") || strings.Contains(name, "file"):
		verb = "读取文件内容"
		purpose = "检查文件内容并据此继续处理"
	case strings.Contains(name, "write") || strings.Contains(name, "edit") || strings.Contains(name, "update"):
		verb = "修改项目文件"
		purpose = "应用请求的变更并保留现有逻辑"
	case strings.Contains(name, "search") || strings.Contains(name, "browser") || strings.Contains(name, "fetch"):
		verb = "查询外部信息"
		purpose = "获取相关资料并用于当前回答"
	}
	if target != "" {
		if len([]rune(target)) > 180 {
			target = string([]rune(target)[:180]) + "…"
		}
		return fmt.Sprintf("我将执行：%s。\n\n目的：%s。\n\n预期：返回结果后继续处理。", verb+"："+target, purpose)
	}
	return fmt.Sprintf("我将执行：%s。\n\n目的：%s。\n\n预期：返回结果后继续处理。", verb, purpose)
}

func toolPlanSummaryFromMaps(calls []any) string {
	converted := make([]detectedToolCall, 0, len(calls))
	for _, raw := range calls {
		tc, _ := raw.(map[string]any)
		fn, _ := tc["function"].(map[string]any)
		name, _ := fn["name"].(string)
		args, _ := fn["arguments"].(string)
		converted = append(converted, detectedToolCall{Name: name, Arguments: []byte(args)})
	}
	return toolPlanSummary(converted)
}

func writeToolResponse(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, res chathub.Result, preambleSent ...bool) error {
	return writeToolResponseWithRoute(w, id, model, stream, calls, res, routeResolution{}, preambleSent...)
}

func writeToolResponseWithRoute(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, res chathub.Result, route routeResolution, preambleSent ...bool) error {
	return writeToolResponseWithPolicy(w, id, model, stream, calls, res, route, nativePolicySnapshot{}, preambleSent...)
}

func writeToolResponseWithPolicy(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, res chathub.Result, route routeResolution, policy nativePolicySnapshot, preambleSent ...bool) error {
	return writeToolResponseWithMetadata(w, id, model, stream, calls, res, route, policy, nil, "", preambleSent...)
}

func writeToolResponseWithMetadata(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, res chathub.Result, route routeResolution, policy nativePolicySnapshot, metadata map[string]any, reasoning string, preambleSent ...bool) error {
	toolCalls := toolCallMaps(calls)
	summary := toolPlanSummary(calls)
	textContent := summary
	if !stream && strings.TrimSpace(res.Text) != "" {
		textContent = res.Text
	}
	images := validImageURLs(res.Images)
	content := any(textContent)
	if len(images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": textContent}}
		for _, url := range images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		}
		content = parts
	}
	msg := map[string]any{"role": "assistant", "content": content, "tool_calls": toolCalls}
	if reasoning == "" {
		reasoning = chathub.ReasoningContent(res.Events)
	}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	if metadata == nil {
		metadata = compatM365Metadata(res)
		if route.RequestedModel != "" {
			metadata = compatM365Metadata(res, route)
		}
	}
	if policy.Schema != "" {
		metadata = withNativePolicy(metadata, policy)
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)
		emit := func(v any) {
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(v))
			if flusher != nil {
				flusher.Flush()
			}
		}
		base := func(delta map[string]any, finish any) map[string]any {
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			if len(metadata) > 0 {
				chunk["m365"] = metadata
			}
			return chunk
		}
		if len(preambleSent) == 0 || !preambleSent[0] {
			delta := map[string]any{"role": "assistant", "content": summary}
			if reasoning != "" {
				delta["reasoning_content"] = reasoning
			}
			emit(base(delta, nil))
		}
		if len(images) > 0 {
			emit(base(map[string]any{"images": images}, nil))
		}
		for i, tc := range calls {
			typ := tc.Type
			if typ == "" {
				typ = "function"
			}
			emit(base(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": tc.ID, "type": typ, "function": map[string]any{"name": tc.Name, "arguments": string(tc.Arguments)}}}}, nil))
		}
		emit(base(map[string]any{}, "tool_calls"))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return nil
	}
	jsonOut(w, map[string]any{"id": id, "object": "chat.completion", "model": model, "choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "tool_calls"}}, "m365": metadata})
	return nil
}

var errUnavailableToolCall = errors.New("tool call was not exposed by the client")

func toolDefinitionMaps(tools []chathub.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		function, err := decodeExactJSONObject(tool.Function)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"type": tool.Type, "function": function})
	}
	return out
}

func writeBufferedChatCompletionStream(w http.ResponseWriter, response map[string]any, route routeResolution, tools []chathub.Tool, choice any, policies ...nativePolicySnapshot) error {
	id, _ := response["id"].(string)
	model, _ := response["model"].(string)
	message, finish := openAIChoice(response)
	if id == "" || model == "" || message == nil {
		return fmt.Errorf("invalid buffered chat completion")
	}

	text := ""
	reasoning, _ := message["reasoning_content"].(string)
	images := []string{}
	switch content := message["content"].(type) {
	case string:
		text = content
	case []any:
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			switch part["type"] {
			case "text":
				text += fmt.Sprint(part["text"])
			case "image_url":
				image, _ := part["image_url"].(map[string]any)
				if url, _ := image["url"].(string); chathub.IsImageURL(url) {
					images = append(images, url)
				}
			}
		}
	}

	if rawCalls, ok := message["tool_calls"].([]any); ok && len(rawCalls) > 0 {
		calls := make([]detectedToolCall, 0, len(rawCalls))
		for _, raw := range rawCalls {
			call, _ := raw.(map[string]any)
			fn, _ := call["function"].(map[string]any)
			callID, _ := call["id"].(string)
			callType, _ := call["type"].(string)
			name, _ := fn["name"].(string)
			arguments, _ := fn["arguments"].(string)
			if callID == "" || name == "" || arguments == "" {
				return fmt.Errorf("invalid buffered tool call")
			}
			calls = append(calls, detectedToolCall{ID: callID, Type: callType, Name: name, Arguments: []byte(arguments)})
		}
		allowedCalls, rejected := filterAllowedToolCalls(calls, toolDefinitionMaps(tools), choice)
		if len(allowedCalls) > 0 {
			policy := nativePolicySnapshot{}
			if len(policies) > 0 {
				policy = policies[0]
			}
			metadata, _ := response["m365"].(map[string]any)
			return writeToolResponseWithMetadata(w, id, model, true, allowedCalls, chathub.Result{Text: text, Images: images}, route, policy, metadata, reasoning)
		}
		if rejected {
			return fmt.Errorf("%w", errUnavailableToolCall)
		}
		return fmt.Errorf("buffered chat completion has no valid tool calls")
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return fmt.Errorf("buffered chat completion has no deliverable content")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	created := response["created"]
	if created == nil {
		created = time.Now().Unix()
	}
	metadata := response["m365"]
	emit := func(delta map[string]any, terminal any) {
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": terminal}},
		}
		if metadata != nil {
			chunk["m365"] = metadata
		}
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	first := true
	if reasoning != "" {
		emit(map[string]any{"role": "assistant", "reasoning_content": reasoning}, nil)
		first = false
	}
	if text != "" {
		delta := map[string]any{"content": text}
		if first {
			delta["role"] = "assistant"
			first = false
		}
		emit(delta, nil)
	}
	if len(images) > 0 {
		delta := map[string]any{"images": images}
		if first {
			delta["role"] = "assistant"
		}
		emit(delta, nil)
	}
	if finish == "" {
		finish = "stop"
	}
	emit(map[string]any{}, finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeTextStreamEnd(w http.ResponseWriter, id, model string, routes ...routeResolution) {
	route := routeResolution{}
	if len(routes) > 0 {
		route = routes[0]
	}
	writeTextStreamEndWithPolicy(w, id, model, route, nativePolicySnapshot{})
}

func writeTextStreamEndWithPolicy(w http.ResponseWriter, id, model string, route routeResolution, policy nativePolicySnapshot, results ...chathub.Result) {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}
	metadata := map[string]any{}
	if len(results) > 0 {
		metadata = compatM365Metadata(results[0], route)
	} else if route.RequestedModel != "" {
		metadata = route.metadata()
	}
	if policy.Schema != "" {
		metadata = withNativePolicy(metadata, policy)
	}
	if len(metadata) > 0 {
		chunk["m365"] = metadata
	}
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
