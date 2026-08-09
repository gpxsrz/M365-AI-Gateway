package web

import (
	"encoding/json"
	"m365-native/internal/chathub"
	"testing"
)

func TestResolveNativePolicyClassification(t *testing.T) {
	tests := []struct {
		name           string
		planningMode   string
		tools          []chathub.Tool
		requested      string
		effective      string
		mutationStatus string
		wantErr        bool
	}{
		{name: "no tools", planningMode: "router", requested: "none", effective: "router", mutationStatus: "not_requested"},
		{name: "read only search", planningMode: "native", tools: []chathub.Tool{nativePolicyTestTool("function", "web_search")}, requested: "declared_tools", effective: "native", mutationStatus: "not_requested"},
		{name: "ambiguous terminal", planningMode: "native", tools: []chathub.Tool{nativePolicyTestTool("function", "terminal")}, requested: "declared_tools", effective: "native", mutationStatus: "unverified"},
		{name: "explicit mutation", planningMode: "native", tools: []chathub.Tool{nativePolicyTestTool("function", "write_file")}, requested: "declared_tools", effective: "native", mutationStatus: "unverified"},
		{name: "custom exec isolated", planningMode: "native", tools: []chathub.Tool{nativePolicyTestTool("custom", "exec")}, requested: "custom_exec_only", effective: "custom_exec_only", mutationStatus: "not_requested"},
		{name: "invalid planning mode", planningMode: "legacy", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolveNativePolicy(test.planningMode, test.tools)
			if test.wantErr {
				if err == nil {
					t.Fatal("invalid policy accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy.Schema != nativePolicySchemaV1 ||
				policy.RequestedToolPolicy != test.requested ||
				policy.EffectiveToolPolicy != test.effective ||
				policy.NativeMutationStatus != test.mutationStatus ||
				policy.NativeSearchStatus != "unverified" ||
				policy.RequestedCapabilities != (NativeCapabilityPolicy{
					ClientExecutionTools: PolicyEnabled,
					NativeSearch:         PolicyInherit,
					NativeGrounding:      PolicyInherit,
					NativeReadTools:      PolicyInherit,
					NativeMutationTools:  PolicyDisabled,
				}) ||
				policy.EffectiveCapabilities != (NativeCapabilityPolicy{
					ClientExecutionTools: PolicyEnabled,
					NativeSearch:         PolicyInherit,
					NativeGrounding:      PolicyInherit,
					NativeReadTools:      PolicyInherit,
					NativeMutationTools:  PolicyInherit,
				}) ||
				policy.MutationEnforcement != EnforcementNone ||
				policy.MutationEnforcementVerified {
				t.Fatalf("policy=%#v", policy)
			}
		})
	}
}

func TestResolveResponsesNativePolicyMarksCustomExecPromptOnly(t *testing.T) {
	policy, err := resolveResponsesNativePolicy("native", []chathub.Tool{nativePolicyTestTool("custom", "exec")})
	if err != nil {
		t.Fatal(err)
	}
	if policy.RequestedToolPolicy != "custom_exec_only" ||
		policy.EffectiveToolPolicy != "custom_exec_only" ||
		policy.MutationEnforcement != EnforcementPrompt ||
		policy.EffectiveCapabilities.NativeMutationTools != PolicyInherit ||
		policy.MutationEnforcementVerified {
		t.Fatalf("policy=%#v", policy)
	}
}

func nativePolicyTestTool(toolType, name string) chathub.Tool {
	definition, _ := json.Marshal(map[string]any{"name": name})
	return chathub.Tool{Type: toolType, Function: definition}
}
