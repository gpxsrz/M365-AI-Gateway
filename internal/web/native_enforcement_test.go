package web

import (
	"encoding/json"
	"m365-native/internal/chathub"
	"strings"
	"testing"
)

func TestScanNativeToolCallsEnforcesSidecarEmissionBoundary(t *testing.T) {
	tools := []chathub.Tool{nativePolicyTestTool("function", "get_current_time")}
	events := []json.RawMessage{
		json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"get_current_time","arguments":{"timezone":"Asia/Taipei"}}}`),
		json.RawMessage(`{"messageType":"Progress","contentType":"SearchResults","toolName":"web_search","arguments":{"query":"golang"}}`),
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`),
	}

	scan := scanNativeToolCalls(events, tools)
	if len(scan.Calls) != 1 || scan.Calls[0].Name != "get_current_time" {
		t.Fatalf("calls=%#v", scan.Calls)
	}
	if scan.ReadOnlyObserved != 1 {
		t.Fatalf("read-only observations=%d", scan.ReadOnlyObserved)
	}
	if len(scan.Blocked) != 1 || scan.Blocked[0].Name != "delete_file" || scan.Blocked[0].Reason != "undeclared_native_tool" {
		t.Fatalf("blocked=%#v", scan.Blocked)
	}
}

func TestSearchResultsLabelDoesNotAuthorizeUnknownToolIdentity(t *testing.T) {
	events := []json.RawMessage{json.RawMessage(`{"messageType":"Progress","contentType":"SearchResults","toolName":"delete_file","arguments":{"path":"notes.txt"}}`)}
	scan := scanNativeToolCalls(events, nil)
	if scan.ReadOnlyObserved != 0 || len(scan.Blocked) != 1 || scan.Blocked[0].Name != "delete_file" {
		t.Fatalf("scan=%#v", scan)
	}
}

func TestArgumentlessNativeToolShapeFailsClosed(t *testing.T) {
	events := []json.RawMessage{
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file"}`),
	}
	scan := scanNativeToolCalls(events, nil)
	if len(scan.Blocked) != 1 || scan.Blocked[0].Name != "delete_file" || scan.Blocked[0].Reason != "missing_arguments" {
		t.Fatalf("scan=%#v", scan)
	}

	_, _, err := classifyNativeStreamToolEvent(chathub.StreamEvent{Kind: "tool", ContentType: "ToolCall", ToolName: "delete_file"}, nil)
	if err == nil || !strings.Contains(err.Error(), "delete_file") {
		t.Fatalf("argumentless stream tool accepted: %v", err)
	}
}

func TestProtectedArtifactMetadataCannotBecomeDeclaredCallerTool(t *testing.T) {
	const protected = "https://artifact.asyncgw.teams.microsoft.com/v1/objects/id/views/original/output.csv"
	tools := []chathub.Tool{nativePolicyTestTool("function", "python_execution")}
	events := []json.RawMessage{json.RawMessage(`{"pluginName":"python_execution","arguments":{"nested":{"outputFiles":[{"codeResultFileUrl":"` + protected + `"}]}}}`)}
	if scan := scanNativeToolCalls(events, tools); len(scan.Calls) != 0 {
		t.Fatalf("protected artifact became non-stream caller call: %#v", scan.Calls)
	}
	_, _, err := classifyNativeStreamToolEvent(chathub.StreamEvent{
		Kind:      "tool",
		ToolName:  "python_execution",
		Arguments: json.RawMessage(`{"nested":{"codeResultFileUrl":"` + protected + `"}}`),
	}, tools)
	if err == nil || !strings.Contains(err.Error(), "protected artifact") {
		t.Fatalf("protected artifact became stream caller call: %v", err)
	}
	if got := validImageURLs([]string{protected}); len(got) != 0 {
		t.Fatalf("protected artifact became caller image: %#v", got)
	}
	mixed := []json.RawMessage{json.RawMessage(`{"messages":[{"pluginName":"get_current_time","arguments":{"timezone":"Asia/Taipei"}},{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","outputFiles":[{"codeResultFileUrl":"` + protected + `"}]}]}`)}
	if scan := scanNativeToolCalls(mixed, []chathub.Tool{nativePolicyTestTool("function", "get_current_time")}); len(scan.Calls) != 1 || scan.Calls[0].Name != "get_current_time" {
		t.Fatalf("artifact sibling suppressed or became a native caller call: %#v", scan)
	}
}

func TestExecutionEnforcedPolicyIsScopedAndDoesNotClaimUpstreamControl(t *testing.T) {
	policy, err := resolveNativePolicy("native", []chathub.Tool{nativePolicyTestTool("function", "get_current_time")})
	if err != nil {
		t.Fatal(err)
	}
	policy = withSidecarExecutionEnforcement(policy)
	if policy.NativeMutationStatus != "not_requested" ||
		policy.MutationEnforcement != EnforcementExecution ||
		!policy.MutationEnforcementVerified ||
		policy.MutationEnforcementScope != "sidecar_tool_emission" ||
		policy.UpstreamNativeMutationStatus != "unverified" ||
		policy.RequestedCapabilities.NativeMutationTools != PolicyDisabled ||
		policy.EffectiveCapabilities.NativeMutationTools != PolicyDisabled ||
		policy.EffectiveCapabilities.NativeSearch != PolicyInherit ||
		policy.EffectiveCapabilities.ClientExecutionTools != PolicyEnabled {
		t.Fatalf("policy=%#v", policy)
	}
}

func TestExecutionEnforcementPreservesRequestMutationStatus(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []chathub.Tool
		want  string
	}{
		{name: "no tools", want: "not_requested"},
		{name: "custom exec", tools: []chathub.Tool{nativePolicyTestTool("custom", "exec")}, want: "not_requested"},
		{name: "ambiguous native tool", tools: []chathub.Tool{nativePolicyTestTool("function", "terminal")}, want: "unverified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := resolveNativePolicy("native", test.tools)
			if err != nil {
				t.Fatal(err)
			}
			policy = withSidecarExecutionEnforcement(policy)
			if policy.NativeMutationStatus != test.want {
				t.Fatalf("status=%q policy=%#v", policy.NativeMutationStatus, policy)
			}
		})
	}
}
