package web

import (
	"strings"
	"testing"
)

func TestParseModelToolDecisionAutoAndParallel(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[{"name":"get_weather","arguments":{"city":"Beijing"}},{"name":"get_time","arguments":{"city":"Beijing"}}]}`, testTools(), "auto")
	if !ok || len(calls) != 2 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
func TestParseModelToolDecisionNoCall(t *testing.T) {
	calls, ok := parseModelToolDecision(`{"calls":[]}`, testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}

func TestParseModelToolDecisionRejectsUnparseableText(t *testing.T) {
	for _, text := range []string{"", "ordinary text"} {
		calls, ok := parseModelToolDecision(text, testTools(), "auto")
		if ok || len(calls) != 0 {
			t.Fatalf("text=%q calls=%v ok=%v", text, calls, ok)
		}
	}
}
func TestModelToolRouterPromptCarriesInterruptedCallEvidence(t *testing.T) {
	ledger := buildAgentLedger([]oaiMsg{{
		Role: "assistant",
		ToolCalls: []map[string]any{{
			"id":   "call_x",
			"type": "function",
			"function": map[string]any{
				"name":      "terminal",
				"arguments": `{"command":"deploy"}`,
			},
		}},
	}})
	p := modelToolRouterPrompt("Continue after interruption.\n"+ledger.RouterContext(), testTools(), "auto")
	for _, want := range []string{
		"Pending calls have unknown outcomes",
		"Do not automatically issue the same name and arguments",
		`"id":"call_x"`,
		`"name":"terminal"`,
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %q in continuation prompt: %s", want, p)
		}
	}
}

func TestParseModelToolDecisionRejectsBadSchema(t *testing.T) {
	calls, ok := parseModelToolDecision("```json\n{\"calls\":[{\"name\":\"get_weather\",\"arguments\":{\"city\":2}}]}\n```", testTools(), "auto")
	if !ok || len(calls) != 0 {
		t.Fatalf("calls=%v ok=%v", calls, ok)
	}
}
