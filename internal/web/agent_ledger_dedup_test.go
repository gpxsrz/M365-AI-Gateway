package web

import "testing"

func TestCanonicalToolArgumentsDeduplicateEquivalentJSON(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		Name:            "workspace_write_file",
		ArgumentsSHA256: toolArgumentsSHA256(`{"path":"main.go","content":"x"}`),
	}}}
	if !ledger.hasKnownCall("workspace_write_file", ` { "content":"x", "path":"main.go" } `) {
		t.Fatal("equivalent JSON arguments were not deduplicated")
	}
}

func TestFilterKnownCallsKeepsNewArguments(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		Name:            "workspace_write_file",
		ArgumentsSHA256: toolArgumentsSHA256(`{"path":"main.go","content":"old"}`),
	}}}
	calls := []detectedToolCall{
		{Name: "workspace_write_file", Arguments: []byte(`{"path":"main.go","content":"old"}`)},
		{Name: "workspace_write_file", Arguments: []byte(`{"path":"main.go","content":"new"}`)},
	}
	got := filterKnownCalls(calls, ledger)
	if len(got) != 1 || string(got[0].Arguments) != `{"path":"main.go","content":"new"}` {
		t.Fatalf("unexpected filtered calls: %#v", got)
	}
}

func TestFilterKnownCallsSuppressesPendingEquivalentJSON(t *testing.T) {
	ledger := agentLedger{Pending: []toolEvidence{{
		Name:            "terminal",
		ArgumentsSHA256: toolArgumentsSHA256(`{"command":"deploy","timeout":30}`),
	}}}
	calls := []detectedToolCall{{
		Name:      "terminal",
		Arguments: []byte(` { "timeout": 30, "command": "deploy" } `),
	}}
	if got := filterKnownCalls(calls, ledger); len(got) != 0 {
		t.Fatalf("pending equivalent call was not suppressed: %#v", got)
	}
}

func TestRouterContextStaysCompact(t *testing.T) {
	ledger := agentLedger{Completed: []toolEvidence{{
		ID: "call_1", Name: "workspace_write_file", ArgumentsSHA256: toolArgumentsSHA256(`{"path":"main.go"}`), ResultLength: len("written successfully"), ResultSHA256: stringSHA256("written successfully"), Preview: "written successfully", hasResult: true,
	}}}
	ctx := ledger.RouterContext()
	if len(ctx) > 2000 {
		t.Fatalf("router context unexpectedly large: %d bytes", len(ctx))
	}
	if len(ctx) == 0 {
		t.Fatal("router context is empty")
	}
}
