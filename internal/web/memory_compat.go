package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"m365-native/internal/chathub"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	memoryCompatibilityPrefix = "/memory/v1/"
	hermesCompatibilityPrefix = "/hermes/v1/"
)

func memoryCompatibilityRequest(path string) bool {
	return strings.HasPrefix(path, memoryCompatibilityPrefix)
}

func hermesCompatibilityRequest(path string) bool {
	return strings.HasPrefix(path, hermesCompatibilityPrefix)
}

func compatibilityCheckpointControl(path string) (checkpointRequestControl, bool) {
	switch {
	case memoryCompatibilityRequest(path):
		return checkpointRequestControl{Namespace: "memory-provider", ForceNew: true, Untracked: true}, true
	case hermesCompatibilityRequest(path):
		return checkpointRequestControl{Namespace: "hermes"}, true
	default:
		return checkpointRequestControl{}, false
	}
}

func (s *Server) interactiveOpenAIChat(w http.ResponseWriter, r *http.Request) {
	s.serveInteractiveOpenAI(w, r, s.openaiChat)
}

func (s *Server) serveInteractiveOpenAI(w http.ResponseWriter, r *http.Request, handler http.HandlerFunc) {
	s.serveInteractiveRequest(w, r, func(w http.ResponseWriter, status int, message string) {
		writeOpenAIError(w, status, "rate_limit_error", message)
	}, handler)
}

func (s *Server) serveInteractiveRequest(w http.ResponseWriter, r *http.Request, reject func(http.ResponseWriter, int, string), handler http.HandlerFunc) {
	cfg := serverRuntimeSettings(s)
	traffic := s.compatibilityTrafficRuntime()
	release, err := traffic.acquireInteractive(r.Context(), cfg)
	if err != nil {
		retryAfter := 1
		if admission, ok := err.(*interactiveAdmissionError); ok && admission.retryAfter > 0 {
			retryAfter = admission.retryAfter
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		reject(w, http.StatusServiceUnavailable, "Interactive request is waiting for shared Microsoft account capacity")
		return
	}
	defer release(time.Duration(cfg.InteractivePriorityHoldoffSeconds) * time.Second)
	tracked := &statusTrackingResponseWriter{ResponseWriter: w}
	defer func() {
		traffic.observeInteractiveStatus(tracked.finalStatus(), cfg, tracked.Header().Get("Retry-After"))
	}()
	handler(tracked, r)
}

func (s *Server) compatibilityTrafficRuntime() *compatibilityTrafficController {
	if s == nil {
		return newCompatibilityTrafficController()
	}
	// One Server instance owns exactly one Microsoft 365 account. The shared
	// process-local controller is therefore account-scoped by construction;
	// API-key owners isolate caller/checkpoint state, not upstream capacity.
	s.mu.Lock()
	if s.compatTraffic == nil {
		s.compatTraffic = newCompatibilityTrafficController()
	}
	traffic := s.compatTraffic
	s.mu.Unlock()
	return traffic
}

func (s *Server) memoryOpenAIChat(w http.ResponseWriter, r *http.Request) {
	cfg := serverRuntimeSettings(s)
	if !cfg.MemoryCompatibilityEnabled {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "Memory Provider compatibility profile is disabled")
		return
	}
	traffic := s.compatibilityTrafficRuntime()
	release, err := traffic.acquireMemory(r.Context(), cfg)
	if err != nil {
		retryAfter := 1
		if admission, ok := err.(*memoryAdmissionError); ok && admission.retryAfter > 0 {
			retryAfter = admission.retryAfter
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeOpenAIError(w, http.StatusServiceUnavailable, "rate_limit_error", "Memory Provider request is waiting for interactive capacity")
		return
	}
	tracked := &statusTrackingResponseWriter{ResponseWriter: w}
	defer func() {
		status := tracked.finalStatus()
		release(status)
		if status == http.StatusTooManyRequests {
			traffic.honorRetryAfter(tracked.Header().Get("Retry-After"))
		}
	}()
	control, _ := compatibilityCheckpointControl(r.URL.Path)
	control.Mode = checkpointFullHistory
	s.openaiChat(tracked, r.WithContext(withCheckpointRequest(r.Context(), control)))
}

func (s *Server) hermesOpenAIChat(w http.ResponseWriter, r *http.Request) {
	cfg := serverRuntimeSettings(s)
	if !cfg.HermesCompatibilityEnabled {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "Hermes compatibility profile is disabled")
		return
	}
	control, _ := compatibilityCheckpointControl(r.URL.Path)
	control.Mode = checkpointFullHistory
	s.serveInteractiveOpenAI(w, r.WithContext(withCheckpointRequest(r.Context(), control)), s.openaiChat)
}

func memorySchemaInstruction(format *responseFormat) string {
	if format == nil || format.Type != "json_schema" {
		return ""
	}
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	if schema == nil {
		return ""
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return "\n\nMEMORY_PROVIDER_JSON_CONTRACT:\n" +
		"Return exactly one JSON value matching the JSON Schema below. " +
		"Property names are protocol identifiers: copy them exactly, never translate, rename, add, or omit them. " +
		"Do not wrap the JSON in Markdown and do not add prose.\nJSON_SCHEMA:\n" + string(encoded)
}

func memoryStructuredJSONCandidate(text string) (string, bool) {
	normalized := normalizeJSONText(text)
	if _, err := decodeExactJSONValue([]byte(normalized)); err == nil {
		return normalized, true
	}

	raw := []byte(normalized)
	matchStart := -1
	matchEnd := -1
	match := ""
	for i := 0; i < len(raw); i++ {
		if !memoryWrappedJSONValueStart(raw[i]) || !memoryJSONValueBoundary(raw, i-1) {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw[i:]))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			continue
		}
		end := i + int(decoder.InputOffset())
		if end <= i || !memoryJSONValueBoundary(raw, end) {
			continue
		}
		candidate := strings.TrimSpace(string(raw[i:end]))
		if _, err := decodeExactJSONValue([]byte(candidate)); err != nil {
			continue
		}
		if matchStart >= 0 {
			return "", false
		}
		matchStart, matchEnd, match = i, end, candidate
		i = end - 1
	}
	if matchStart < 0 {
		return "", false
	}
	if strings.ContainsAny(string(raw[:matchStart]), "{}[]") || strings.ContainsAny(string(raw[matchEnd:]), "{}[]") {
		return "", false
	}
	return match, true
}

type memoryStructuredResponseAnalysis struct {
	Result              chathub.Result
	Formatted           string
	RepairCandidate     string
	RepairFormatErr     error
	Source              string
	Valid               bool
	EntirelyNonJSONText bool
}

func analyzeMemoryStructuredResponse(result chathub.Result, format *responseFormat) memoryStructuredResponseAnalysis {
	analysis := memoryStructuredResponseAnalysis{Result: result, EntirelyNonJSONText: true}
	for _, evidence := range resultTextEvidenceCandidates(result) {
		if !memoryEntirelyNonJSONText(evidence.text) {
			analysis.EntirelyNonJSONText = false
		}
		candidate, ok := memoryStructuredJSONCandidate(evidence.text)
		if !ok {
			continue
		}
		if normalized, candidateErr := validateResponseFormatText(candidate, format); candidateErr == nil {
			analysis.Result.Text = candidate
			analysis.Result.TextSource = evidence.source
			analysis.Result.TextRelation = "exact"
			analysis.Result.FinalText = ""
			analysis.Result.StreamedText = ""
			analysis.Formatted = normalized
			analysis.Source = evidence.source
			analysis.Valid = true
			return analysis
		} else if analysis.RepairCandidate == "" {
			analysis.RepairCandidate = candidate
			analysis.RepairFormatErr = candidateErr
		}
	}
	return analysis
}

func memoryEntirelyNonJSONText(text string) bool {
	// A structured re-ask is only safe when there is no JSON-shaped evidence
	// to preserve or disambiguate. Any container delimiter means the response
	// may contain malformed or competing structured candidates and must keep
	// the existing fail-closed behavior instead of being regenerated.
	return !strings.ContainsAny(normalizeJSONText(text), "{}[]")
}

func memorySchemaAllowsStructuredReask(format *responseFormat) bool {
	if format == nil || format.Type != "json_schema" {
		return false
	}
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	typeName, _ := schema["type"].(string)
	return typeName == "object" || typeName == "array"
}

func memorySchemaReaskPrompt(callerEvidence string, format *responseFormat) string {
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	encoded, _ := json.Marshal(schema)
	return fmt.Sprintf(`MEMORY_PROVIDER_SCHEMA_REASK
The previous upstream response was entirely non-JSON and is not structured evidence. Do not copy facts from it.
Re-answer using only the CALLER_EVIDENCE below and the caller's JSON Schema.
Do not add, replace, normalize, infer, or invent scalar values merely to satisfy the schema.
Property names are protocol identifiers: copy them exactly and never translate or rename them.
Return exactly one JSON value matching JSON_SCHEMA, with no Markdown or prose.

JSON_SCHEMA:
%s

CALLER_EVIDENCE:
%s`, string(encoded), callerEvidence)
}

func memoryWrappedJSONValueStart(b byte) bool {
	// Outside an exact JSON response, only containers have an unambiguous
	// structural boundary. Bare strings, numbers, booleans, and null can occur
	// naturally in prose and must not be promoted into a structured response.
	return b == '{' || b == '['
}

func memoryJSONValueBoundary(raw []byte, index int) bool {
	if index < 0 || index >= len(raw) {
		return true
	}
	b := raw[index]
	return !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_')
}

func memorySchemaRepairPrompt(invalidText string, format *responseFormat, validationErr error) string {
	schema, _ := format.JSONSchema["schema"].(map[string]any)
	encoded, _ := json.Marshal(schema)
	return fmt.Sprintf(`MEMORY_PROVIDER_SCHEMA_REPAIR
The previous candidate is valid JSON but did not satisfy the caller's JSON Schema.
Repair the PREVIOUS_CANDIDATE only. You may correct protocol property names, but you must preserve the exact container structure, property order, and scalar values.
Do not answer the original user request again and do not add, replace, normalize, infer, or invent scalar values merely to satisfy the schema.
Property names are protocol identifiers: copy them exactly and never translate or rename them.
Return JSON only, with no Markdown or prose.

VALIDATION_ERROR:
%s

JSON_SCHEMA:
%s

PREVIOUS_CANDIDATE:
%s`, validationErr, string(encoded), invalidText)
}

func memoryRepairPreservesFacts(previousText, repairedText string, formats ...*responseFormat) error {
	previousNormalized := normalizeJSONText(previousText)
	repairedNormalized := normalizeJSONText(repairedText)
	previous, err := decodeExactJSONValue([]byte(previousNormalized))
	if err != nil {
		return fmt.Errorf("memory repair requires a valid JSON candidate: %w", err)
	}
	repaired, err := decodeExactJSONValue([]byte(repairedNormalized))
	if err != nil {
		return fmt.Errorf("memory repair returned invalid JSON: %w", err)
	}
	available := map[string]int{}
	collectMemoryScalarValues(previous, available)
	used := map[string]int{}
	collectMemoryScalarValues(repaired, used)
	if len(available) != len(used) {
		return fmt.Errorf("memory repair changed the scalar value set")
	}
	for value, count := range available {
		if used[value] != count {
			return fmt.Errorf("memory repair changed scalar value %s", value)
		}
	}
	previousSignature, err := memoryRepairSignature(previousNormalized)
	if err != nil {
		return fmt.Errorf("memory repair previous signature: %w", err)
	}
	repairedSignature, err := memoryRepairSignature(repairedNormalized)
	if err != nil {
		return fmt.Errorf("memory repair repaired signature: %w", err)
	}
	if previousSignature != repairedSignature {
		return fmt.Errorf("memory repair changed structure or scalar order")
	}
	if len(formats) > 0 && formats[0] != nil {
		schema, _ := formats[0].JSONSchema["schema"].(map[string]any)
		if schema != nil {
			if err := memoryRepairPreservesSchemaAssociation(previous, repaired, schema); err != nil {
				return err
			}
		}
	}
	return nil
}

func memoryRepairPreservesSchemaAssociation(previous, repaired any, schema map[string]any) error {
	switch before := previous.(type) {
	case map[string]any:
		after, ok := repaired.(map[string]any)
		if !ok || len(before) != len(after) {
			return fmt.Errorf("memory repair changed object shape")
		}
		properties, _ := schema["properties"].(map[string]any)
		var renamedBefore []string
		var renamedAfter []string
		for key, value := range before {
			if repairedValue, exists := after[key]; exists {
				if !memoryJSONValuesEqual(value, repairedValue) {
					return fmt.Errorf("memory repair changed value associated with property %q", key)
				}
				if childSchema, ok := properties[key].(map[string]any); ok {
					if err := memoryRepairPreservesSchemaAssociation(value, repairedValue, childSchema); err != nil {
						return err
					}
				}
			} else {
				renamedBefore = append(renamedBefore, key)
			}
		}
		for key := range after {
			if _, exists := before[key]; !exists {
				renamedAfter = append(renamedAfter, key)
			}
		}
		if len(renamedBefore) != len(renamedAfter) {
			return fmt.Errorf("memory repair changed object property count")
		}
		for _, oldKey := range renamedBefore {
			value := before[oldKey]
			var candidates []string
			for _, newKey := range renamedAfter {
				childSchema, ok := properties[newKey].(map[string]any)
				if !ok {
					continue
				}
				if validateWebSchemaValue(childSchema, value) == nil {
					candidates = append(candidates, newKey)
				}
			}
			if len(candidates) != 1 {
				return fmt.Errorf("memory repair cannot prove a unique schema property for renamed property %q", oldKey)
			}
			target := candidates[0]
			if !memoryJSONValuesEqual(value, after[target]) {
				return fmt.Errorf("memory repair changed scalar-to-property association for %q", target)
			}
			childSchema, _ := properties[target].(map[string]any)
			if err := memoryRepairPreservesSchemaAssociation(value, after[target], childSchema); err != nil {
				return err
			}
		}
		return nil
	case []any:
		after, ok := repaired.([]any)
		if !ok || len(before) != len(after) {
			return fmt.Errorf("memory repair changed array shape")
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for i := range before {
			if err := memoryRepairPreservesSchemaAssociation(before[i], after[i], itemSchema); err != nil {
				return err
			}
		}
		return nil
	default:
		if !memoryJSONValuesEqual(previous, repaired) {
			return fmt.Errorf("memory repair changed a scalar value")
		}
		return nil
	}
}

func memoryJSONValuesEqual(a, b any) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func memoryRepairSignature(text string) (string, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()
	var out strings.Builder
	if err := writeMemoryRepairSignatureValue(decoder, &out); err != nil {
		return "", err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("memory repair signature found trailing JSON token")
		}
		return "", err
	}
	return out.String(), nil
}

func writeMemoryRepairSignatureValue(decoder *json.Decoder, out *strings.Builder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			out.WriteByte('{')
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				if _, ok := key.(string); !ok {
					return fmt.Errorf("object key is not a string")
				}
				out.WriteString("<key>")
				if err := writeMemoryRepairSignatureValue(decoder, out); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim('}') {
				return fmt.Errorf("invalid object close")
			}
			out.WriteByte('}')
			return nil
		case '[':
			out.WriteByte('[')
			for decoder.More() {
				if err := writeMemoryRepairSignatureValue(decoder, out); err != nil {
					return err
				}
			}
			closeToken, err := decoder.Token()
			if err != nil || closeToken != json.Delim(']') {
				return fmt.Errorf("invalid array close")
			}
			out.WriteByte(']')
			return nil
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		return err
	}
	out.Write(encoded)
	return nil
}

func collectMemoryScalarValues(value any, out map[string]int) {
	switch v := value.(type) {
	case map[string]any:
		for _, child := range v {
			collectMemoryScalarValues(child, out)
		}
	case []any:
		for _, child := range v {
			collectMemoryScalarValues(child, out)
		}
	default:
		encoded, err := json.Marshal(v)
		if err == nil {
			out[string(encoded)]++
		}
	}
}
