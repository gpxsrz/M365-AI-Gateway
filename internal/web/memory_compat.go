package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
