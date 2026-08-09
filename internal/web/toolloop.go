package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type detectedToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func toolType(name string, tools []map[string]any) string {
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if n, _ := f["name"].(string); n == name {
			if typ, _ := t["type"].(string); typ != "" {
				return typ
			}
		}
	}
	return "function"
}

func allowedToolNames(tools []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		if f, ok := t["function"].(map[string]any); ok {
			if n, ok := f["name"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
}

func filterAllowedToolCalls(calls []detectedToolCall, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	allowed := allowedToolNames(tools)
	out := make([]detectedToolCall, 0, len(calls))
	rejected := false
	for _, call := range calls {
		if !allowed[call.Name] || !toolChoiceAllows(choice, call.Name) {
			rejected = true
			continue
		}
		if validateToolArgumentsRaw(call.Arguments, toolFunction(call.Name, tools)) != nil {
			rejected = true
			continue
		}
		out = append(out, call)
	}
	return out, rejected
}

func toolChoiceAllows(choice any, name string) bool {
	if choice == nil {
		return true
	}
	if s, ok := choice.(string); ok {
		return s != "none" && (s != "required" || name != "")
	}
	if m, ok := choice.(map[string]any); ok {
		if f, ok := m["function"].(map[string]any); ok {
			n, _ := f["name"].(string)
			return n == name
		}
		if n, ok := m["name"].(string); ok {
			return n == name
		}
	}
	return true
}

func callID(_ string, _ string, _ int) string {
	return "call_" + uuid.NewString()
}

func extractToolCalls(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	start := strings.Index(text, "<m365-tool-call>")
	end := strings.Index(text, "</m365-tool-call>")
	if start < 0 || end <= start {
		return nil, false
	}
	type wireCall struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	raw := bytes.TrimSpace([]byte(text[start+len("<m365-tool-call>") : end]))
	var items []wireCall
	if len(raw) > 0 && raw[0] == '[' {
		if json.Unmarshal(raw, &items) != nil {
			return nil, false
		}
	} else {
		var item wireCall
		if json.Unmarshal(raw, &item) != nil {
			return nil, false
		}
		items = []wireCall{item}
	}
	allowed := allowedToolNames(tools)
	out := make([]detectedToolCall, 0, len(items))
	for i, item := range items {
		if !allowed[item.Name] || !toolChoiceAllows(choice, item.Name) || validateToolArgumentsRaw(item.Arguments, toolFunction(item.Name, tools)) != nil {
			continue
		}
		out = append(out, detectedToolCall{ID: callID(item.Name, string(item.Arguments), i), Type: toolType(item.Name, tools), Name: item.Name, Arguments: append(json.RawMessage(nil), item.Arguments...)})
	}
	return out, len(out) > 0
}

func validateToolResult(messages []oaiMsg, known map[string]bool) error {
	for _, m := range messages {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				return fmt.Errorf("tool_call_id required")
			}
			if len(known) > 0 && !known[m.ToolCallID] {
				return fmt.Errorf("unknown tool_call_id: %s", m.ToolCallID)
			}
		}
	}
	return nil
}
