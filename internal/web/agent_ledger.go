package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

type toolEvidence struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
	ResultLength    int    `json:"result_length"`
	ResultSHA256    string `json:"result_sha256,omitempty"`
	Failed          bool   `json:"failed"`
	Preview         string `json:"preview,omitempty"`
	hasResult       bool   `json:"-"`
}
type agentLedger struct {
	Completed           []toolEvidence `json:"completed"`
	Pending             []toolEvidence `json:"pending"`
	ToolRounds          int            `json:"tool_rounds"`
	RepeatedCall        bool           `json:"repeated_call"`
	RepeatedFailure     bool           `json:"repeated_failure"`
	RepetitionSignature string         `json:"repetition_signature,omitempty"`
	KnownCallDigests    []string       `json:"-"`
}

var failureSignal = regexp.MustCompile(`(?i)(exit\s*(code|status)?\s*[:=]?\s*[1-9]\d*|\berror\b|\bfailed\b|\bfailure\b|exception|traceback|timed?\s*out|permission denied|not found|refused)`)
var unsupportedSuccess = regexp.MustCompile(`(?i)\b(installed|created|written|executed|ran|started|deployed|deleted|verified|completed|succeeded|success(?:ful(?:ly)?)?|done|finished|passed|applied)\b`)
var unsupportedServiceState = regexp.MustCompile(`(?i)\b(service|server|daemon|application|app|deployment)\s+(is|was)\s+(currently\s+|now\s+)?(running|active)\b`)
var unsupportedResolvedState = regexp.MustCompile(`(?i)\b(issue|bug|problem|failure)\s+(is|was|has been)\s+(fixed|resolved)\b`)

const unconfirmedToolOutcomeResponse = "I cannot confirm completion because no matching tool results were returned. No external action has been verified."
const completedToolCallSuppressedResponse = "The matching tool call was not reissued because its result is already present in the conversation."
const toolResultPreviewBytes = 4000

func claimsUnsupportedSuccess(answer string) bool {
	return unsupportedSuccess.MatchString(answer) || unsupportedServiceState.MatchString(answer) || unsupportedResolvedState.MatchString(answer)
}

func suppressedKnownCallResponse(l agentLedger) string {
	if len(l.Pending) > 0 {
		return unconfirmedToolOutcomeResponse
	}
	return completedToolCallSuppressedResponse
}

func compactToolResult(s string, limit int) string {
	s = strings.TrimSpace(s)
	return boundedUTF8Preview(s, limit)
}

func boundedUTF8Preview(s string, limit int) string {
	if limit < 200 {
		limit = 200
	}
	if len(s) <= limit {
		return s
	}
	marker := fmt.Sprintf("\n... [truncated %d bytes] ...\n", len(s)-limit)
	budget := limit - len(marker)
	if budget < 2 {
		budget = 2
	}
	head := budget / 3
	tail := budget - head
	prefix := utf8SafePrefix(s, head)
	suffix := utf8SafeSuffix(s, tail)
	marker = fmt.Sprintf("\n... [truncated %d bytes] ...\n", len(s)-len(prefix)-len(suffix))
	if len(prefix)+len(marker)+len(suffix) > limit {
		suffix = utf8SafeSuffix(s, limit-len(prefix)-len(marker))
	}
	return prefix + marker + suffix
}

func utf8SafePrefix(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	end := limit
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return s[:end]
}

func utf8SafeSuffix(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	start := len(s) - limit
	for start < len(s) && !utf8.ValidString(s[start:]) {
		start++
	}
	return s[start:]
}
func scopedCallID(_ string, _ string, _ int, _ string) string {
	return "call_" + uuid.NewString()
}
func buildAgentLedger(messages []oaiMsg, prior ...agentLedger) agentLedger {
	calls := map[string]toolEvidence{}
	order := []string{}
	knownCallDigests := []string{}
	known := map[string]struct{}{}
	toolRounds := 0
	if len(prior) > 0 {
		toolRounds = prior[0].ToolRounds
		for _, digest := range prior[0].KnownCallDigests {
			if _, duplicate := known[digest]; duplicate {
				continue
			}
			known[digest] = struct{}{}
			knownCallDigests = append(knownCallDigests, digest)
		}
		for _, evidence := range prior[0].Completed {
			evidence.hasResult = true
			calls[evidence.ID] = evidence
			order = append(order, evidence.ID)
		}
		for _, evidence := range prior[0].Pending {
			calls[evidence.ID] = evidence
			order = append(order, evidence.ID)
		}
	}
	for _, m := range messages {
		if m.Role == "assistant" {
			addedRound := false
			for _, raw := range m.ToolCalls {
				id, _ := raw["id"].(string)
				fn, _ := raw["function"].(map[string]any)
				name, _ := fn["name"].(string)
				args := fmt.Sprint(fn["arguments"])
				if id != "" {
					if _, exists := calls[id]; !exists {
						calls[id] = toolEvidence{ID: id, Name: name, ArgumentsSHA256: toolArgumentsSHA256(args)}
						order = append(order, id)
						addedRound = true
					}
				}
			}
			if addedRound {
				toolRounds++
			}
		}
		if m.Role == "tool" {
			if e, ok := calls[m.ToolCallID]; ok {
				result := contentToString(m.Content)
				e.ResultLength = len(result)
				e.ResultSHA256 = stringSHA256(result)
				e.Failed = m.ToolResultIsError || failureSignal.MatchString(result)
				e.Preview = boundedUTF8Preview(result, toolResultPreviewBytes)
				e.hasResult = true
				calls[m.ToolCallID] = e
			}
		}
	}
	l := agentLedger{KnownCallDigests: knownCallDigests, ToolRounds: toolRounds}
	seenCall := map[string]int{}
	seenFailure := map[string]int{}
	for _, id := range order {
		e := calls[id]
		identityDigest := toolCallIdentityDigest(e.Name, e.ArgumentsSHA256)
		if _, duplicate := known[identityDigest]; !duplicate {
			known[identityDigest] = struct{}{}
			l.KnownCallDigests = append(l.KnownCallDigests, identityDigest)
		}
		sig := e.Name + "\x00" + e.ArgumentsSHA256
		seenCall[sig]++
		if seenCall[sig] >= 2 {
			l.RepeatedCall = true
			l.RepetitionSignature = sig
		}
		if !e.hasResult {
			l.Pending = append(l.Pending, e)
		} else {
			l.Completed = append(l.Completed, e)
			if e.Failed {
				fs := e.Name + "\x00" + e.ArgumentsSHA256 + "\x00" + e.ResultSHA256
				seenFailure[fs]++
				if seenFailure[fs] >= 2 {
					l.RepeatedFailure = true
					l.RepetitionSignature = fs
				}
			}
		}
	}
	return l
}
func (l agentLedger) RouterContext() string {
	type compact struct {
		Completed    []toolEvidence `json:"completed"`
		Pending      []toolEvidence `json:"pending"`
		RepeatedCall bool           `json:"repeated_call"`
	}
	completed := append([]toolEvidence(nil), l.Completed...)
	for i := range completed {
		completed[i].Preview = ""
	}
	b, _ := json.Marshal(compact{completed, l.Pending, l.RepeatedCall})
	hint := "Use only this compact evidence. Completed calls are final evidence. Pending calls have unknown outcomes because no matching tool result was returned. Do not automatically issue the same name and arguments as any completed or pending call. Report pending outcomes as unconfirmed unless independent evidence resolves them."
	if l.RepeatedFailure {
		hint += " The same call failed repeatedly; change strategy instead of retrying unchanged."
	}
	return hint + "\nEVIDENCE_LEDGER: " + string(b)
}
func canonicalToolArguments(s string) string {
	s = strings.TrimSpace(s)
	var v any
	decoder := json.NewDecoder(strings.NewReader(s))
	decoder.UseNumber()
	if decoder.Decode(&v) == nil && ensureJSONEOF(decoder) == nil {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return s
}

func stringSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func toolArgumentsSHA256(arguments string) string {
	return checkpointDigest(transportCheckpointHashDomain, canonicalToolArguments(arguments))
}

func toolCallIdentityDigest(name, argumentsDigest string) string {
	return checkpointDigest(transportCheckpointToolIdentityDomain, name+"\x00"+argumentsDigest)
}

func (l agentLedger) hasKnownCall(name, args string) bool {
	want := toolArgumentsSHA256(args)
	wantIdentity := toolCallIdentityDigest(name, want)
	for _, digest := range l.KnownCallDigests {
		if digest == wantIdentity {
			return true
		}
	}
	for _, group := range [][]toolEvidence{l.Completed, l.Pending} {
		for _, e := range group {
			if e.Name == name && e.ArgumentsSHA256 == want {
				return true
			}
		}
	}
	return false
}
func filterKnownCalls(calls []detectedToolCall, l agentLedger) []detectedToolCall {
	out := calls[:0]
	batch := make(map[string]struct{}, len(calls))
	for _, c := range calls {
		identity := c.Name + "\x00" + toolArgumentsSHA256(string(c.Arguments))
		if _, duplicate := batch[identity]; duplicate || l.hasKnownCall(c.Name, string(c.Arguments)) {
			continue
		}
		batch[identity] = struct{}{}
		out = append(out, c)
	}
	return out
}

func (l agentLedger) hasFailedCompletedEvidence() bool {
	for _, evidence := range l.Completed {
		if evidence.Failed {
			return true
		}
	}
	return false
}
func (l agentLedger) CanContinue(maxRounds int) error {
	if maxRounds <= 0 {
		maxRounds = 16
	}
	if l.ToolRounds >= maxRounds {
		return fmt.Errorf("tool round limit reached: %d", maxRounds)
	}
	if len(l.Pending) > 0 {
		return fmt.Errorf("pending tool results must be returned before another turn")
	}
	return nil
}
func configuredHermesMaxToolRounds(settings *settingsStore) int {
	if raw, ok := os.LookupEnv("M365_HERMES_MAX_TOOL_ROUNDS"); ok {
		if n, e := strconv.Atoi(strings.TrimSpace(raw)); e == nil && n > 0 && n <= 512 {
			return n
		}
		return 128
	}
	if settings != nil {
		if n := settings.get().HermesMaxToolRounds; n > 0 && n <= 512 {
			return n
		}
	}
	return 128
}

func configuredToolRoundLimit(path string, settings *settingsStore) (string, int) {
	switch {
	case hermesCompatibilityRequest(path):
		return "hermes", configuredHermesMaxToolRounds(settings)
	case memoryCompatibilityRequest(path):
		return "memory", configuredMaxToolRounds(settings)
	default:
		return "generic", configuredMaxToolRounds(settings)
	}
}

func configuredMaxToolRounds(settings *settingsStore) int {
	if raw, ok := os.LookupEnv("M365_MAX_TOOL_ROUNDS"); ok {
		if n, e := strconv.Atoi(strings.TrimSpace(raw)); e == nil && n > 0 && n <= 512 {
			return n
		}
		return 16
	}
	if settings != nil {
		if n := settings.get().MaxToolRounds; n > 0 && n <= 512 {
			return n
		}
	}
	return 16
}
func activeMessages(messages []oaiMsg) []oaiMsg {
	last := -1
	for i, m := range messages {
		if m.Role == "user" {
			last = i
		}
	}
	if last <= 0 {
		return messages
	}
	return messages[last:]
}
func completionEvidenceAllows(answer string, l agentLedger) bool {
	claimsSuccess := claimsUnsupportedSuccess(answer)
	if len(l.Pending) > 0 {
		// Missing tool results mean the execution outcome is unknown. Any success
		// claim is unsupported, even when the same answer also contains refusal
		// or uncertainty language. Neutral or explicitly incomplete answers are
		// safe to pass through.
		return !claimsSuccess
	}
	if len(l.Completed) > 0 {
		if !claimsSuccess {
			return true
		}
		for _, evidence := range l.Completed {
			if evidence.ResultLength > 0 && !evidence.Failed {
				return true
			}
		}
		return false
	}
	return !claimsSuccess
}
func completedCallIDs(l agentLedger) []string {
	o := make([]string, 0, len(l.Completed))
	for _, e := range l.Completed {
		o = append(o, e.ID)
	}
	sort.Strings(o)
	return o
}
