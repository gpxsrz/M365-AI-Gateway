package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

func modelToolRouterPrompt(prompt string, tools []map[string]any, choice any, limits ...int) string {
	defs, _ := json.Marshal(tools)
	mode := normalizedToolChoiceMode(choice)
	limit := 1
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	return fmt.Sprintf(`You are a tool selection assistant. Based on the user request, decide the next caller-side tool call or calls.

Available tools: %s

Mode: %s

Maximum calls this turn: %d

Rules:
- Return JSON only as {"calls":[{"name":"function_name","arguments":{}}],"answer":""}.
- If no caller-side tool is needed, use {"calls":[],"answer":"direct final answer"} and put the complete user-facing answer in answer.
- If any caller-side call is returned, answer must be empty.
- Caller execution tools are separate from Microsoft native Bing web search, citations, grounding, and read-only information retrieval. Native Bing and those native read-only capabilities remain allowed. When the user requests Microsoft native Bing, perform the search before returning the caller-side JSON decision. Preserve actual SearchResults and any upstream references/citations. When both are needed, preserve native grounding and still return the caller decision in this JSON format.
- Multiple calls are allowed only when they are mutually independent and clearly read-only.
- Commands, mutations, dependent operations, and uncertain operations must be returned one at a time.
- Never exceed Maximum calls this turn.
- Only use tools from the available list above
- Validate all arguments against the tool's schema
- Do not invent tools that are not in the list

User request and evidence:
%s`, defs, mode, limit, prompt)
}

func modelToolRouterRepairPrompt(output string) string {
	return `Repair the previous tool-routing output into JSON only with shape {"calls":[{"name":"function_name","arguments":{}}],"answer":""}. Do not invent calls. If no caller-side tool is needed, use {"calls":[],"answer":"direct final answer"} and preserve the complete user-facing answer in answer. OUTPUT:
` + compactToolResult(output, 6000)
}

func parseModelToolDecision(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	text = strings.TrimSpace(text)
	// Try the new natural language format first: CALL_TOOL: name({...})
	if strings.HasPrefix(text, "CALL_TOOL:") || strings.HasPrefix(text, "call_tool:") {
		parts := strings.SplitN(text, ":", 2)
		if len(parts) == 2 {
			rest := strings.TrimSpace(parts[1])
			start := strings.Index(rest, "(")
			end := strings.LastIndex(rest, ")")
			if start > 0 && end > start {
				name := strings.TrimSpace(rest[:start])
				args := json.RawMessage(strings.TrimSpace(rest[start+1 : end]))
				fn := toolFunction(name, tools)
				if fn != nil && toolChoiceAllows(choice, name) && validateToolArgumentsRaw(args, fn) == nil {
					return []detectedToolCall{{ID: callID(name, string(args), 0), Type: toolType(name, tools), Name: name, Arguments: append(json.RawMessage(nil), args...)}}, true
				}
			}
		}
	}
	if strings.Contains(text, "NO_TOOL_NEEDED") || strings.Contains(text, "no_tool_needed") {
		return nil, true
	}
	// Fallback: try the old JSON format
	if i := strings.Index(text, "```"); i >= 0 {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text[i+3:], "```"), "json"))
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var envelope struct {
		Calls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
	}
	if json.Unmarshal([]byte(text[start:end+1]), &envelope) != nil {
		return nil, false
	}
	out := make([]detectedToolCall, 0, len(envelope.Calls))
	for i, c := range envelope.Calls {
		fn := toolFunction(c.Name, tools)
		if fn == nil || len(c.Arguments) == 0 || !toolChoiceAllows(choice, c.Name) || validateToolArgumentsRaw(c.Arguments, fn) != nil {
			continue
		}
		out = append(out, detectedToolCall{ID: callID(c.Name, string(c.Arguments), i), Type: toolType(c.Name, tools), Name: c.Name, Arguments: append(json.RawMessage(nil), c.Arguments...)})
	}
	return out, true
}

func parseModelToolDirectAnswer(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "```"); i >= 0 {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text[i+3:], "```"), "json"))
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", false
	}
	var envelope struct {
		Calls  []json.RawMessage `json:"calls"`
		Answer string            `json:"answer"`
	}
	if json.Unmarshal([]byte(text[start:end+1]), &envelope) != nil || len(envelope.Calls) != 0 {
		return "", false
	}
	answer := strings.TrimSpace(envelope.Answer)
	return answer, answer != ""
}
