package chathub

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolProtocolPrompt follows the community-compatible M365 convention:
// definitions are wrapped in <tools>, and calls are emitted as a fenced block
// whose info string is the exact tool name.
func toolProtocolPrompt(text string, tools []Tool, choice any, limits ...int) string {
	if len(tools) == 0 || strings.EqualFold(fmt.Sprint(choice), "none") {
		return text
	}
	var defs []string
	for _, t := range tools {
		var f struct {
			Name, Description string
			Parameters        json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(t.Function, &f) != nil || f.Name == "" {
			continue
		}
		params := strings.TrimSpace(string(f.Parameters))
		if params == "" || params == "null" {
			params = "{}"
		}
		defs = append(defs, fmt.Sprintf("%s — %s\n```%s\n%s\n```", f.Name, f.Description, f.Name, params))
	}
	if len(defs) == 0 {
		return text
	}
	limit := 1
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	return fmt.Sprintf(`You are an execution agent. The tools below are real tools exposed by the caller, not hypothetical M365 plugins.
Caller execution tools are separate from Microsoft native Bing web search, citations, grounding, and read-only information retrieval. Native Bing and those native read-only capabilities remain allowed when caller tools are registered. When a turn needs both native grounding and a caller tool, use the native capability and still emit the caller decision in the required fenced format.
When the user's request requires caller-side tools, emit at most %d fenced tool blocks. Each block's info string must be the exact tool name and its body must be a JSON object of arguments. Multiple blocks are allowed only for mutually independent, clearly read-only operations. Commands, mutations, dependent operations, and uncertain operations must be emitted one at a time. Do not say that the tool is unavailable. Do not wrap calls in XML or explanatory prose. Wait for every emitted tool result before claiming completion.

<tools>
%s
</tools>

User request:
%s`, limit, strings.Join(defs, "\n\n"), text)
}
