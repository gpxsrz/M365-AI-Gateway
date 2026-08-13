package web

import (
	"encoding/json"
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

func TestParseExactModelToolEnvelopeRejectsAmbiguousJSON(t *testing.T) {
	cases := map[string]struct {
		text      string
		ambiguous bool
	}{
		"ordinary json":                  {text: `{"status":"ok","items":[]}`},
		"duplicate calls nonempty empty": {text: `{"calls":[{"name":"terminal"}],"calls":[],"answer":"done"}`, ambiguous: true},
		"duplicate calls empty nonempty": {text: `{"calls":[],"calls":[{"name":"terminal"}],"answer":"done"}`, ambiguous: true},
		"duplicate calls around extra":   {text: `{"calls":[{"name":"terminal"}],"extra":true,"calls":[],"answer":"done"}`, ambiguous: true},
		"duplicate answer":               {text: `{"calls":[],"answer":"first","answer":"second"}`, ambiguous: true},
		"extra field":                    {text: `{"calls":[],"answer":"done","extra":true}`, ambiguous: true},
		"leading garbage":                {text: `prefix {"calls":[],"answer":"done"}`},
		"trailing garbage":               {text: `{"calls":[],"answer":"done"} suffix`},
		"calls null":                     {text: `{"calls":null,"answer":"done"}`, ambiguous: true},
		"calls object":                   {text: `{"calls":{},"answer":"done"}`, ambiguous: true},
		"answer null":                    {text: `{"calls":[],"answer":null}`, ambiguous: true},
		"answer number":                  {text: `{"calls":[],"answer":3}`, ambiguous: true},
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			envelope, ok, ambiguous := parseExactModelToolEnvelope(text.text)
			if ok {
				t.Fatalf("unexpected envelope=%#v", envelope)
			}
			if ambiguous != text.ambiguous {
				t.Fatalf("ambiguous=%v want=%v", ambiguous, text.ambiguous)
			}
		})
	}
}

func TestParseModelToolFinalAnswerEnvelopePreservesAnswerWhitespace(t *testing.T) {
	want := "  line 1\n\nline 2  \n"
	raw, err := json.Marshal(map[string]any{"calls": []any{}, "answer": want})
	if err != nil {
		t.Fatal(err)
	}
	answer, hasCalls, ok, ambiguous := parseModelToolFinalAnswerEnvelope(string(raw))
	if !ok || ambiguous || hasCalls || answer != want {
		t.Fatalf("answer=%q hasCalls=%v ok=%v ambiguous=%v", answer, hasCalls, ok, ambiguous)
	}
}

func TestModelToolRouterRepairPromptPreservesLongStructuredArguments(t *testing.T) {
	const sentinel = "MIDDLE_SENTINEL_REQUIRED_FOR_VALID_PYTHON"
	code := "print('BEGIN')\n" +
		strings.Repeat("# padding before sentinel\n", 180) +
		"if " + sentinel + " := True:\n" +
		strings.Repeat("    pass  # padding after sentinel\n", 180) +
		"print('END')\n"
	raw, err := json.Marshal(map[string]any{
		"calls": []any{map[string]any{
			"name": "execute_code",
			"arguments": map[string]any{
				"code": code,
			},
		}},
		"answer": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 6000 {
		t.Fatalf("fixture must exceed historical repair preview limit, got %d bytes", len(raw))
	}

	prompt := modelToolRouterRepairPrompt(string(raw))
	if !strings.Contains(prompt, sentinel) {
		t.Fatalf("repair prompt lost load-bearing middle sentinel; prompt_len=%d raw_len=%d", len(prompt), len(raw))
	}
	if !strings.HasSuffix(prompt, string(raw)) {
		t.Fatal("repair prompt must preserve the complete routing output as its source of truth")
	}
}
