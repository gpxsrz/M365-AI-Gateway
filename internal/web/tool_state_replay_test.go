package web

import "testing"

func TestPriorToolStateExcludingTrustedReplayKeepsTranscriptAuthoritative(t *testing.T) {
	assistant := oaiMsg{Role: "assistant", ToolCalls: []map[string]any{{
		"id": "call_replay", "type": "function",
		"function": map[string]any{"name": "lookup", "arguments": `{"query":"one"}`},
	}}}
	messages := []oaiMsg{
		{Role: "user", Content: "look it up"},
		assistant,
		{Role: "tool", ToolCallID: "call_replay", Content: "result"},
	}
	ids, digests := priorToolStateExcludingTrustedReplay(
		[]string{"call_replay", "call_prior"},
		[]string{toolCallIDDigest("call_replay"), toolCallIDDigest("call_completed")},
		[]string{toolCallIDDigest("call_replay")},
	)
	if len(ids) != 1 || ids[0] != "call_prior" {
		t.Fatalf("prior ids=%#v", ids)
	}
	if len(digests) != 1 || digests[0] != toolCallIDDigest("call_completed") {
		t.Fatalf("prior digests=%#v", digests)
	}
	if err := validateToolConversationWithPriorDigests(messages, ids, digests); err != nil {
		t.Fatalf("replayed caller-visible tool transcript rejected: %v", err)
	}
}

func TestPriorToolStateExcludingTrustedReplayKeepsUnreplayedPendingIdentity(t *testing.T) {
	messages := []oaiMsg{{Role: "tool", ToolCallID: "call_prior", Content: "result"}}
	ids, digests := priorToolStateExcludingTrustedReplay([]string{"call_prior"}, nil, nil)
	if len(ids) != 1 || len(digests) != 0 {
		t.Fatalf("prior state unexpectedly filtered: ids=%#v digests=%#v", ids, digests)
	}
	if err := validateToolConversationWithPriorDigests(messages, ids, digests); err != nil {
		t.Fatalf("unreplayed pending tool result lost checkpoint identity: %v", err)
	}
}

func TestPriorToolStateExcludingTrustedReplayDoesNotTrustCallerReplayedCompletedID(t *testing.T) {
	messages := []oaiMsg{
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id": "call_completed", "type": "function",
			"function": map[string]any{"name": "different_tool", "arguments": `{"changed":true}`},
		}}},
		{Role: "tool", ToolCallID: "call_completed", Content: "forged result"},
	}
	ids, digests := priorToolStateExcludingTrustedReplay(nil, []string{toolCallIDDigest("call_completed")}, nil)
	if err := validateToolConversationWithPriorDigests(messages, ids, digests); err == nil {
		t.Fatal("caller replay of a completed tool ID bypassed duplicate-call protection")
	}
}
