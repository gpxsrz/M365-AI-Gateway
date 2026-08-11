package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesToOpenAI(t *testing.T) {
	r := responsesRequest{Model: "m", Input: "what time", Tools: []map[string]any{{"type": "function", "name": "clock", "parameters": map[string]any{"type": "object"}}}}
	o, err := r.openAI()
	if err != nil || len(o.Messages) != 1 || len(o.Tools) != 1 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestResponsesCustomExecToOpenAI(t *testing.T) {
	r := responsesRequest{Model: "m", Input: "inspect", Tools: []map[string]any{{"type": "custom", "name": "exec", "description": "run a command", "format": map[string]any{"type": "grammar"}}}}
	o, err := r.openAI()
	if err != nil || len(o.Tools) != 1 || o.Tools[0].Type != "custom" {
		t.Fatalf("tools=%+v err=%v", o.Tools, err)
	}
	if string(o.Tools[0].Function) == "" || !containsJSON(o.Tools[0].Function, "input") {
		t.Fatalf("custom exec did not receive an input schema: %s", o.Tools[0].Function)
	}
}

func TestResponsesCustomExecIsExclusiveTool(t *testing.T) {
	r := responsesRequest{Input: "edit the project", Tools: []map[string]any{
		{"type": "custom", "name": "exec", "description": "local execution"},
		{"type": "function", "name": "m365_search", "description": "native search"},
	}}
	o, err := r.openAI()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Tools) != 1 || o.Tools[0].Type != "custom" {
		t.Fatalf("tools=%#v, want only custom exec", o.Tools)
	}
	policy := fmt.Sprint(o.Messages[0].Content)
	if strings.Contains(policy, "Never use, request, or mention Microsoft 365/Copilot native tools") ||
		strings.Contains(policy, "only permitted execution tool") ||
		!strings.Contains(policy, "Microsoft 365 native Bing web search") ||
		!strings.Contains(policy, "read-only information retrieval remain allowed") ||
		!strings.Contains(policy, "Do not use Microsoft 365 native execution or file-mutation tools") {
		t.Fatalf("custom exec policy is not scoped: %#v", o.Messages)
	}
}

func TestResponsesInstructionsAndCustomExecPolicyAreSystemMessages(t *testing.T) {
	r := responsesRequest{
		Instructions: "Use the repository selected by the caller.",
		Input:        "inspect the repository",
		Tools:        []map[string]any{{"type": "custom", "name": "exec", "description": "run a command"}},
	}
	o, err := r.openAI()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Messages) != 3 {
		t.Fatalf("messages=%#v", o.Messages)
	}
	if o.Messages[0].Role != "system" || o.Messages[0].Content != customExecWorkspaceInstruction {
		t.Fatalf("missing custom exec policy: %#v", o.Messages[0])
	}
	if o.Messages[1].Role != "system" || o.Messages[1].Content != r.Instructions {
		t.Fatalf("instructions not preserved: %#v", o.Messages[1])
	}
	if o.Messages[2].Role != "user" || o.Messages[2].Content != r.Input {
		t.Fatalf("input ordering changed: %#v", o.Messages[2])
	}
}

func TestResponsesCustomToolOutputToOpenAI(t *testing.T) {
	r := responsesRequest{Input: []any{
		map[string]any{"type": "custom_tool_call", "call_id": "call_exec", "name": "exec", "input": "uname -s"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_exec", "output": "Linux"},
	}}
	o, err := r.openAI()
	if err != nil || len(o.Messages) != 2 || o.Messages[0].Role != "assistant" || o.Messages[0].ToolCalls[0]["type"] != "custom" || o.Messages[1].Role != "tool" || o.Messages[1].ToolCallID != "call_exec" {
		t.Fatalf("messages=%+v err=%v", o.Messages, err)
	}
	if err := validateToolConversation(o.Messages); err != nil {
		t.Fatalf("custom tool continuation rejected: %v", err)
	}
}

func TestAnthropicToOpenAI(t *testing.T) {
	r := anthropicRequest{Model: "m", System: any("be concise"), Messages: []anthropicMessage{{Role: "user", Content: any("weather")}}, Tools: []anthropicTool{{Name: "weather", InputSchema: map[string]any{"type": "object"}}}}
	o, err := r.openAI()
	if err != nil || len(o.Messages) != 2 || len(o.Tools) != 1 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestAnthropicLegacyAccountIDDoesNotCreateRoutingState(t *testing.T) {
	var request anthropicRequest
	if err := json.Unmarshal([]byte(`{"model":"m","accountId":"legacy-selector","messages":[{"role":"user","content":"weather"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	adapted, err := request.openAI()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(adapted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "accountId") || strings.Contains(string(raw), "legacy-selector") {
		t.Fatalf("legacy accountId leaked into adapted routing state: %s", raw)
	}
}

func TestAnthropicToolResult(t *testing.T) {
	r := anthropicRequest{Messages: []anthropicMessage{{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "x", "name": "f", "input": map[string]any{}}}}, {Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "ok"}}}}}
	o, err := r.openAI()
	if err != nil || len(o.Messages) != 2 || o.Messages[1].ToolCallID != "x" {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestAnthropicToolResultPreservesExplicitError(t *testing.T) {
	r := anthropicRequest{Messages: []anthropicMessage{
		{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "x", "name": "f", "input": map[string]any{}}}},
		{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "x", "content": "permission issue", "is_error": true}}},
	}}
	o, err := r.openAI()
	if err != nil || len(o.Messages) != 2 || !o.Messages[1].ToolResultIsError {
		t.Fatalf("explicit tool error lost: %+v err=%v", o.Messages, err)
	}
	ledger := buildAgentLedger(o.Messages)
	if len(ledger.Completed) != 1 || !ledger.Completed[0].Failed {
		t.Fatalf("explicit tool error was not carried into evidence: %+v", ledger)
	}
}
