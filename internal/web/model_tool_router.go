package web

import (
	"encoding/json"
	"fmt"
	"log"
	"m365-native/internal/chathub"
	"strings"
)

func modelToolRouterPrompt(prompt string, tools []map[string]any, choice any, limits ...int) string {
	defs, _ := json.Marshal(tools)
	mode := normalizedToolChoiceMode(choice)
	limit := 1
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	return fmt.Sprintf(`You are a tool selection assistant. Based on the user request, decide the next caller-side tool call or calls.

Available tools: %s

Mode: %s

Maximum calls this turn: %d

Rules:
- Return JSON only as {"calls":[{"name":"function_name","arguments":{}}],"answer":""}.
- If no caller-side tool is needed, use {"calls":[],"answer":"direct final answer"} and put the complete user-facing answer in answer.
- If any caller-side call is returned, answer must be empty.
- Caller execution tools are separate from Microsoft native Bing web search, citations, grounding, and read-only information retrieval. Native Bing and those native read-only capabilities remain allowed. When the user requests Microsoft native Bing, perform the search before returning the caller-side JSON decision. Preserve actual SearchResults and any upstream references/citations. When both are needed, preserve native grounding and still return the caller decision in this JSON format.
- Multiple calls are allowed only when they are mutually independent and clearly read-only.
- Commands, mutations, dependent operations, and uncertain operations must be returned one at a time.
- Never exceed Maximum calls this turn.
- Only use tools from the available list above
- Validate all arguments against the tool's schema
- Do not invent tools that are not in the list

User request and evidence:
%s`, defs, mode, limit, prompt)
}

func modelToolRouterRepairPrompt(output string) string {
	return `Repair the previous tool-routing output into JSON only with shape {"calls":[{"name":"function_name","arguments":{}}],"answer":""}. Do not invent calls. If no caller-side tool is needed, use {"calls":[],"answer":"direct final answer"} and preserve the complete user-facing answer in answer. OUTPUT:
` + output
}

func parseModelToolDecision(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	text = strings.TrimSpace(text)
	// Try the new natural language format first: CALL_TOOL: name({...})
	if strings.HasPrefix(text, "CALL_TOOL:") || strings.HasPrefix(text, "call_tool:") {
		parts := strings.SplitN(text, ":", 2)
		if len(parts) == 2 {
			rest := strings.TrimSpace(parts[1])
			start := strings.Index(rest, "(")
			end := strings.LastIndex(rest, ")")
			if start > 0 && end > start {
				name := strings.TrimSpace(rest[:start])
				args := json.RawMessage(strings.TrimSpace(rest[start+1 : end]))
				fn := toolFunction(name, tools)
				if fn != nil && toolChoiceAllows(choice, name) && validateToolArgumentsRaw(args, fn) == nil {
					return []detectedToolCall{{ID: callID(name, string(args), 0), Type: toolType(name, tools), Name: name, Arguments: append(json.RawMessage(nil), args...)}}, true
				}
			}
		}
	}
	if strings.Contains(text, "NO_TOOL_NEEDED") || strings.Contains(text, "no_tool_needed") {
		return nil, true
	}
	// Fallback: try the old JSON format
	if i := strings.Index(text, "```"); i >= 0 {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text[i+3:], "```"), "json"))
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var envelope struct {
		Calls []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"calls"`
	}
	if json.Unmarshal([]byte(text[start:end+1]), &envelope) != nil {
		return nil, false
	}
	out := make([]detectedToolCall, 0, len(envelope.Calls))
	for i, c := range envelope.Calls {
		fn := toolFunction(c.Name, tools)
		if fn == nil || len(c.Arguments) == 0 || !toolChoiceAllows(choice, c.Name) || validateToolArgumentsRaw(c.Arguments, fn) != nil {
			continue
		}
		out = append(out, detectedToolCall{ID: callID(c.Name, string(c.Arguments), i), Type: toolType(c.Name, tools), Name: c.Name, Arguments: append(json.RawMessage(nil), c.Arguments...)})
	}
	return out, true
}

func alternateModelToolDecisionUsable(text string, calls []detectedToolCall) bool {
	if len(calls) > 0 {
		return true
	}
	if _, ok := parseModelToolDirectAnswer(text); ok {
		return true
	}
	if envelope, ok, ambiguous := parseExactModelToolEnvelope(text); ok && !ambiguous && len(envelope.Calls) == 0 {
		return true
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "no_tool_needed")
}

func selectModelToolDecisionResult(result chathub.Result, tools []map[string]any, choice any) (chathub.Result, []detectedToolCall, bool, string) {
	calls, parsed := parseModelToolDecision(result.Text, tools, choice)
	source := result.TextSource
	if source == "" {
		source = "canonical"
	}
	if parsed {
		return result, calls, true, source
	}

	seen := map[string]bool{result.Text: true}
	candidates := []struct {
		text   string
		source string
	}{
		{text: result.FinalText, source: "final"},
		{text: result.StreamedText, source: "stream"},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.text) == "" || seen[candidate.text] {
			continue
		}
		seen[candidate.text] = true
		candidateCalls, candidateParsed := parseModelToolDecision(candidate.text, tools, choice)
		if !candidateParsed || !alternateModelToolDecisionUsable(candidate.text, candidateCalls) {
			continue
		}
		result.Text = candidate.text
		result.TextSource = candidate.source
		return result, candidateCalls, true, candidate.source
	}
	return result, calls, false, source
}

type modelToolCallDedup struct {
	Calls               []detectedToolCall
	Before              int
	After               int
	KnownCallSuppressed bool
}

func deduplicateModelToolCalls(calls []detectedToolCall, ledger agentLedger, parsed bool) modelToolCallDedup {
	before := len(calls)
	filtered := filterKnownCalls(calls, ledger)
	after := len(filtered)
	return modelToolCallDedup{
		Calls:               filtered,
		Before:              before,
		After:               after,
		KnownCallSuppressed: parsed && before > after,
	}
}

func modelToolDecisionParseReason(result chathub.Result, calls []detectedToolCall, parsed bool) string {
	if parsed {
		if len(calls) > 0 {
			return "tool_calls"
		}
		if _, ok := parseModelToolDirectAnswer(result.Text); ok {
			return "direct_answer"
		}
		if envelope, ok, ambiguous := parseExactModelToolEnvelope(strings.TrimSpace(result.Text)); ambiguous {
			return "envelope_shape"
		} else if ok && len(envelope.Calls) == 0 {
			return "empty_calls"
		}
		if strings.Contains(strings.ToLower(result.Text), "no_tool_needed") {
			return "no_tool_needed"
		}
		return "parsed_no_valid_calls"
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return "empty"
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "no_json_envelope"
	}
	if !json.Valid([]byte(text[start : end+1])) {
		return "json_syntax"
	}
	if _, ok, ambiguous := parseExactModelToolEnvelope(text[start : end+1]); ambiguous {
		return "envelope_shape"
	} else if !ok {
		return "envelope_shape"
	}
	return "unusable_decision"
}

func logModelToolDecisionSelection(requestID, phase string, result chathub.Result, source string, parsed bool, dedup modelToolCallDedup) {
	reason := modelToolDecisionParseReason(result, dedup.Calls, parsed)
	if parsed && dedup.Before > 0 {
		reason = "tool_calls"
	}
	log.Printf("[req-trace] id=%s stage=router_candidate phase=%s source=%s relation=%s parsed=%t parse_reason=%s calls_before_dedup=%d calls_after_dedup=%d known_call_suppressed=%t final_len=%d stream_len=%d canonical_len=%d", requestID, phase, source, result.TextRelation, parsed, reason, dedup.Before, dedup.After, dedup.KnownCallSuppressed, len(result.FinalText), len(result.StreamedText), len(result.Text))
}

func modelToolScratchRequest(text, tone string) chathub.Request {
	return chathub.Request{Text: text, Tone: tone, Started: true}
}

func logModelToolPhaseBinding(requestID, phase, scope, source string, request chathub.Request) {
	log.Printf("[req-trace] id=%s stage=tool_phase_binding phase=%s phase_scope=%s binding_source=%s session_rotated=%t", requestID, phase, scope, source, strings.TrimSpace(request.SessionID) == "")
}

func parseModelToolDirectAnswer(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "```"); i >= 0 {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text[i+3:], "```"), "json"))
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return "", false
	}
	var envelope struct {
		Calls  []json.RawMessage `json:"calls"`
		Answer string            `json:"answer"`
	}
	if json.Unmarshal([]byte(text[start:end+1]), &envelope) != nil || len(envelope.Calls) != 0 {
		return "", false
	}
	answer := strings.TrimSpace(envelope.Answer)
	return answer, answer != ""
}

type modelToolEnvelope struct {
	Calls  []json.RawMessage `json:"calls"`
	Answer string            `json:"answer"`
}

func parseExactModelToolEnvelope(text string) (modelToolEnvelope, bool, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return modelToolEnvelope{}, false, false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return modelToolEnvelope{}, false, false
	}
	var envelope modelToolEnvelope
	seenCalls := false
	seenAnswer := false
	duplicateKnown := false
	unknownField := false
	invalidKnownType := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return modelToolEnvelope{}, false, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return modelToolEnvelope{}, false, false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return modelToolEnvelope{}, false, false
		}
		rawText := strings.TrimSpace(string(raw))
		switch key {
		case "calls":
			if seenCalls {
				duplicateKnown = true
				continue
			}
			seenCalls = true
			if len(rawText) < 2 || rawText[0] != '[' || rawText[len(rawText)-1] != ']' || json.Unmarshal(raw, &envelope.Calls) != nil {
				invalidKnownType = true
			}
		case "answer":
			if seenAnswer {
				duplicateKnown = true
				continue
			}
			seenAnswer = true
			if len(rawText) < 2 || rawText[0] != '"' || json.Unmarshal(raw, &envelope.Answer) != nil {
				invalidKnownType = true
			}
		default:
			unknownField = true
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || strings.TrimSpace(text[decoder.InputOffset():]) != "" {
		return modelToolEnvelope{}, false, false
	}
	if !seenCalls || !seenAnswer {
		return modelToolEnvelope{}, false, false
	}
	if duplicateKnown || unknownField || invalidKnownType {
		return modelToolEnvelope{}, false, true
	}
	return envelope, true, false
}

func parseModelToolFinalAnswerEnvelope(text string) (answer string, hasCalls bool, ok bool, ambiguous bool) {
	envelope, ok, ambiguous := parseExactModelToolEnvelope(text)
	if ambiguous {
		return "", false, false, true
	}
	if !ok {
		return "", false, false, false
	}
	return envelope.Answer, len(envelope.Calls) > 0, true, false
}
