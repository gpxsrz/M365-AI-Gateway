package web

import (
	"encoding/json"

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
