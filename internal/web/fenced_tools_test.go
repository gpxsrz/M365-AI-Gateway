package web

import "testing"

func TestFencedWorkspaceShellIsStructuredToolCall(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "workspace_shell"}}}
	calls := fencedToolCalls("```workspace_shell\n{\"command\":\"find /workspace -type f -o -type d | sort\"}\n```", tools, "auto")
	if len(calls) != 1 {
		t.Fatalf("expected one structured tool call, got %d", len(calls))
	}
	if calls[0].Name != "workspace_shell" {
		t.Fatalf("unexpected tool name %q", calls[0].Name)
	}
	if string(calls[0].Arguments) != `{"command":"find /workspace -type f -o -type d | sort"}` {
		t.Fatalf("unexpected arguments: %s", calls[0].Arguments)
	}
}

func TestFencedBashIsNotInventedWhenUnregistered(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "terminal"}}}
	calls := fencedToolCalls("```bash\n{\"command\":\"sudo apt install chrome\"}\n```", tools, "auto")
	if len(calls) != 0 {
		t.Fatalf("unregistered bash tool was invented: %#v", calls)
	}
}

func TestPlainCommandJSONDoesNotInventBash(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "terminal"}}}
	calls := fencedToolCalls(`{"command":"sudo apt install chrome"}`, tools, "auto")
	if len(calls) != 0 {
		t.Fatalf("plain command JSON invented bash: %#v", calls)
	}
}

func TestFencedBashUsesRegisteredBashTool(t *testing.T) {
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "bash"}}}
	calls := fencedToolCalls("```bash\n{\"command\":\"echo ok\"}\n```", tools, "auto")
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("registered bash call not preserved: %#v", calls)
	}
}
