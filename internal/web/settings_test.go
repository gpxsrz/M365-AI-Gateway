package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"m365-native/internal/chathub"
)

func parallelToolForTest(name, description string, parameters map[string]any) chathub.Tool {
	definition, _ := json.Marshal(map[string]any{"name": name, "description": description, "parameters": parameters})
	return chathub.Tool{Type: "function", Function: definition}
}

func parallelRawToolForTest(function map[string]any) chathub.Tool {
	definition, _ := json.Marshal(function)
	return chathub.Tool{Type: "function", Function: definition}
}

func TestLimitToolCalls(t *testing.T) {
	calls := []detectedToolCall{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := limitToolCalls(calls, 1)
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("got %#v", got)
	}
	if len(limitToolCalls(calls, 2)) != 2 {
		t.Fatal("expected two calls")
	}
	if len(limitToolCalls(calls, 99)) != 3 {
		t.Fatal("must preserve calls below limit")
	}
}
func TestSettingsPersistAndValidate(t *testing.T) {
	s := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	v := s.v
	v.MaxToolCallsPerTurn = 1
	v.MaxToolRounds = 32
	v.ChatTimeoutSeconds = 60
	v.ImageTimeoutSeconds = 90
	if err := s.save(v); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path); err != nil {
		t.Fatal(err)
	}
	v.MaxToolCallsPerTurn = 0
	if err := s.save(v); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestModelMappingsValidate(t *testing.T) {
	v := defaultRuntimeSettings()
	v.ModelMappings = []modelMapping{{PublicModel: "gpt-5.6-sol", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "GPT-5.6-Sol", DefaultReasoningLevel: "low"}}
	if err := validateSettings(v); err != nil {
		t.Fatal(err)
	}
	v.ModelMappings[0].UpstreamTone = "unknown"
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted unknown upstream tone")
	}
	v.ModelMappings[0].UpstreamTone = "Gpt_5_6_Reasoning"
	v.ModelMappings = append(v.ModelMappings, v.ModelMappings[0])
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted duplicate public model")
	}
	v.ModelMappings = []modelMapping{{PublicModel: "custom-codex-route", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Custom Codex Route", DefaultReasoningLevel: "medium"}}
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected custom public model: %v", err)
	}
}

func TestOutboundProxySettingValidation(t *testing.T) {
	v := defaultRuntimeSettings()
	v.OutboundProxy = "socks5://proxy.example:1080"
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected SOCKS5 proxy: %v", err)
	}
	v.OutboundProxy = "https://proxy.example:8443"
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected HTTPS proxy: %v", err)
	}
	v.OutboundProxy = "ftp://proxy.example:21"
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted unsupported proxy scheme")
	}
}

func TestAdaptiveToolCallLimitSerializesDependentOrMutatingCalls(t *testing.T) {
	calls := []detectedToolCall{{ID: "read", Type: "function", Name: "read_file"}, {ID: "exec", Type: "function", Name: "exec_command"}}
	tools := []chathub.Tool{
		parallelToolForTest("read_file", "Read file contents without changing state.", map[string]any{"type": "object"}),
		parallelToolForTest("exec_command", "Execute a command.", map[string]any{"type": "object"}),
	}
	if got := adaptiveToolCallLimit(calls, toolDefinitionMaps(tools), 4); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}
func TestAdaptiveToolCallLimitAllowsIndependentReadOnlyCalls(t *testing.T) {
	calls := []detectedToolCall{{ID: "read", Type: "function", Name: "read_file"}, {ID: "search", Type: "function", Name: "search_code"}}
	tools := []chathub.Tool{
		parallelToolForTest("read_file", "Read file contents without changing state.", map[string]any{"type": "object"}),
		parallelToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object"}),
	}
	if got := adaptiveToolCallLimit(calls, toolDefinitionMaps(tools), 4); got != 4 {
		t.Fatalf("got %d, want 4", got)
	}
}

func TestAdaptiveToolCallLimitFailsClosedOnDefinitionAndIdentityAmbiguity(t *testing.T) {
	safeCalls := []detectedToolCall{{ID: "read", Type: "function", Name: "read_file"}, {ID: "search", Type: "function", Name: "search_code"}}
	safeTools := []chathub.Tool{
		parallelToolForTest("read_file", "Read file contents without changing state.", map[string]any{"type": "object"}),
		parallelToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object"}),
	}
	tests := []struct {
		name  string
		calls []detectedToolCall
		tools []chathub.Tool
	}{
		{name: "misleading read name", calls: []detectedToolCall{safeCalls[0], {ID: "account", Type: "function", Name: "get_account"}}, tools: []chathub.Tool{safeTools[0], parallelToolForTest("get_account", "Delete an account permanently.", map[string]any{"type": "object"})}},
		{name: "missing definition", calls: safeCalls, tools: safeTools[:1]},
		{name: "missing safety evidence", calls: safeCalls, tools: []chathub.Tool{safeTools[0], parallelToolForTest("search_code", "", map[string]any{"type": "object"})}},
		{name: "duplicate definition", calls: safeCalls, tools: append(append([]chathub.Tool{}, safeTools...), safeTools[1])},
		{name: "conflicting annotations", calls: safeCalls, tools: []chathub.Tool{safeTools[0], parallelRawToolForTest(map[string]any{"name": "search_code", "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": true}, "parameters": map[string]any{"type": "object"}})}},
		{name: "false read-only annotation", calls: safeCalls, tools: []chathub.Tool{safeTools[0], parallelRawToolForTest(map[string]any{"name": "search_code", "annotations": map[string]any{"readOnlyHint": false}, "parameters": map[string]any{"type": "object"}})}},
		{name: "schema mutation signal", calls: safeCalls, tools: []chathub.Tool{safeTools[0], parallelToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object", "properties": map[string]any{"delete": map[string]any{"type": "boolean"}}})}},
		{name: "missing call id", calls: []detectedToolCall{safeCalls[0], {Type: "function", Name: "search_code"}}, tools: safeTools},
		{name: "duplicate call id", calls: []detectedToolCall{safeCalls[0], {ID: "read", Type: "function", Name: "search_code"}}, tools: safeTools},
		{name: "missing call type", calls: []detectedToolCall{safeCalls[0], {ID: "search", Name: "search_code"}}, tools: safeTools},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adaptiveToolCallLimit(test.calls, toolDefinitionMaps(test.tools), 2); got != 1 {
				t.Fatalf("unsafe or ambiguous calls were parallelized: limit=%d", got)
			}
		})
	}

	annotationSafe := []chathub.Tool{
		safeTools[0],
		parallelRawToolForTest(map[string]any{"name": "snapshot", "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false}, "parameters": map[string]any{"type": "object"}}),
	}
	annotationCalls := []detectedToolCall{safeCalls[0], {ID: "snapshot", Type: "function", Name: "snapshot"}}
	if got := adaptiveToolCallLimit(annotationCalls, toolDefinitionMaps(annotationSafe), 2); got != 2 {
		t.Fatalf("explicitly safe read-only definitions were serialized: limit=%d", got)
	}
}
