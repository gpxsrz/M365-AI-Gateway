package web

import (
	"fmt"
	"strings"
	"testing"
)

func TestCompactToolResultKeepsHeadTailAndError(t *testing.T) {
	s := "start\n" + strings.Repeat("progress line\n", 1000) + "ERROR: build failed\nexit code 1"
	got := compactToolResult(s, 800)
	if len(got) > 900 || !strings.Contains(got, "start") || !strings.Contains(got, "ERROR: build failed") || !strings.Contains(got, "exit code 1") || !strings.Contains(got, "truncated") {
		t.Fatalf("bad compact result: %d %q", len(got), got)
	}
}

func TestAgentLedgerDetectsRepeatedFailure(t *testing.T) {
	msgs := []oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "run", "arguments": "{\"cmd\":\"build\"}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "exit code 1: failed"},
		{Role: "assistant", ToolCalls: []map[string]any{{"id": "c2", "type": "function", "function": map[string]any{"name": "run", "arguments": "{\"cmd\":\"build\"}"}}}},
		{Role: "tool", ToolCallID: "c2", Content: "exit code 1: failed"},
	}
	l := buildAgentLedger(msgs)
	if !l.RepeatedFailure {
		t.Fatalf("expected repeated failure: %+v", l)
	}
	if !strings.Contains(l.RouterContext(), "change strategy") {
		t.Fatal(l.RouterContext())
	}
}

func TestAgentLedgerEvidenceAndUniqueCallIDs(t *testing.T) {
	a := scopedCallID("run", "{}", 0, "turn-a")
	b := scopedCallID("run", "{}", 0, "turn-b")
	if a == b {
		t.Fatal("call IDs collide across turns")
	}
	l := buildAgentLedger([]oaiMsg{{Role: "assistant", ToolCalls: []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "create", "arguments": "{}"}}}}, {Role: "tool", ToolCallID: "c1", Content: "created"}})
	if len(l.Completed) != 1 || !strings.Contains(l.RouterContext(), "c1") {
		t.Fatalf("missing evidence: %+v", l)
	}
}

func TestAgentLedgerCountsParallelCallsAsOneRound(t *testing.T) {
	msgs := []oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{
			{"id": "read-a", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"a"}`}},
			{"id": "read-b", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"b"}`}},
		}},
		{Role: "tool", ToolCallID: "read-a", Content: "A"},
		{Role: "tool", ToolCallID: "read-b", Content: "B"},
	}
	ledger := buildAgentLedger(msgs)
	if ledger.ToolRounds != 1 {
		t.Fatalf("parallel tool turn counted as %d rounds; want 1", ledger.ToolRounds)
	}
	if err := ledger.CanContinue(2); err != nil {
		t.Fatalf("one parallel tool turn should remain below a two-round limit: %v", err)
	}
}

func TestAgentLedgerDetectsRepeatedCallAndRoundLimit(t *testing.T) {
	var msgs []oaiMsg
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, oaiMsg{Role: "assistant", ToolCalls: []map[string]any{{"id": id, "type": "function", "function": map[string]any{"name": "poll", "arguments": "{\"id\":1}"}}}}, oaiMsg{Role: "tool", ToolCallID: id, Content: "still pending"})
	}
	l := buildAgentLedger(msgs)
	if !l.RepeatedCall || l.ToolRounds != 4 {
		t.Fatalf("loop not detected: %+v", l)
	}
	if err := l.CanContinue(3); err == nil {
		t.Fatal("expected round limit")
	}
}

func TestActiveMessagesIgnoresOlderToolHistory(t *testing.T) {
	var msgs []oaiMsg
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("old%d", i)
		msgs = append(msgs,
			oaiMsg{Role: "assistant", ToolCalls: []map[string]any{{"id": id, "type": "function", "function": map[string]any{"name": "old", "arguments": "{}"}}}},
			oaiMsg{Role: "tool", ToolCallID: id, Content: "done"},
		)
	}
	msgs = append(msgs, oaiMsg{Role: "user", Content: "continue with a new model"})
	full := buildAgentLedger(msgs)
	active := buildAgentLedger(activeMessages(msgs))
	if full.ToolRounds < 20 {
		t.Fatalf("expected full history tools, got %d", full.ToolRounds)
	}
	if active.ToolRounds != 0 {
		t.Fatalf("new user turn should reset round limit scope, got %d", active.ToolRounds)
	}
	if err := active.CanContinue(16); err != nil {
		t.Fatalf("new user turn blocked by old history: %v", err)
	}
}

func TestRecoveryUserTurnKeepsPendingEvidenceInFullLedger(t *testing.T) {
	messages := []oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id":   "pending-1",
			"type": "function",
			"function": map[string]any{
				"name":      "deploy",
				"arguments": `{"target":"service"}`,
			},
		}}},
		{Role: "user", Content: "Continue after the interruption."},
	}
	if err := validateToolConversation(messages); err != nil {
		t.Fatalf("recovery turn rejected: %v", err)
	}
	full := buildAgentLedger(messages)
	if len(full.Pending) != 1 || full.Pending[0].ID != "pending-1" {
		t.Fatalf("pending evidence was erased: %+v", full)
	}
	active := buildAgentLedger(activeMessages(messages))
	if len(active.Pending) != 0 {
		t.Fatalf("old pending call leaked into active round limit scope: %+v", active)
	}
}

func TestCompletionGuardRejectsPendingAndUnsupportedSuccess(t *testing.T) {
	l := buildAgentLedger([]oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{
			{"id": "p1", "type": "function", "function": map[string]any{"name": "deploy", "arguments": "{}"}},
		}},
	})
	for _, answer := range []string{
		"Deployment completed successfully.",
		"It's done.",
		"The work is finished.",
		"The deployment passed.",
		"The update was applied.",
		"The service is running.",
		"I cannot confirm it, but deployment completed successfully.",
	} {
		if completionEvidenceAllows(answer, l) {
			t.Fatalf("pending action allowed as complete: %q", answer)
		}
	}
}

func TestCompletionGuardAllowsNeutralPendingStatus(t *testing.T) {
	ledger := buildAgentLedger([]oaiMsg{{
		Role: "assistant",
		ToolCalls: []map[string]any{{
			"id":       "p1",
			"type":     "function",
			"function": map[string]any{"name": "deploy", "arguments": "{}"},
		}},
	}})
	for _, answer := range []string{
		"A health check is running; the deployment outcome remains unconfirmed.",
		"The active request is still being observed; no completion is confirmed.",
		"The configuration was updated in memory, but no external action is confirmed.",
		"The proposed fix is documented; execution remains unconfirmed.",
	} {
		if !completionEvidenceAllows(answer, ledger) {
			t.Fatalf("neutral pending status was rejected: %q", answer)
		}
	}
}

func TestCompletionGuardRejectsSuccessWhenCompletedEvidenceFailed(t *testing.T) {
	ledger := buildAgentLedger([]oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id":       "c1",
			"type":     "function",
			"function": map[string]any{"name": "deploy", "arguments": "{}"},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "exit code 1: deployment failed"},
	})
	if completionEvidenceAllows("Deployment completed successfully.", ledger) {
		t.Fatal("failed completed evidence authorized a success claim")
	}
	if !completionEvidenceAllows("Deployment failed and remains incomplete.", ledger) {
		t.Fatal("honest failed outcome was rejected")
	}
}

func TestCompletionGuardRejectsUnsupportedSuccess(t *testing.T) {
	if completionEvidenceAllows("Installed, started, and verified successfully", buildAgentLedger(nil)) {
		t.Fatal("unsupported success allowed")
	}
	if !completionEvidenceAllows("I cannot confirm completion because no tool results were returned.", buildAgentLedger(nil)) {
		t.Fatal("honest incomplete response rejected")
	}
}
