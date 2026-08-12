package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibilityNamespacesAreIsolated(t *testing.T) {
	if !memoryCompatibilityRequest("/memory/v1/chat/completions") {
		t.Fatal("memory namespace was not detected")
	}
	if !hermesCompatibilityRequest("/hermes/v1/chat/completions") {
		t.Fatal("Hermes namespace was not detected")
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/models", "/api/chat"} {
		if memoryCompatibilityRequest(path) || hermesCompatibilityRequest(path) {
			t.Fatalf("ordinary route %q entered a compatibility profile", path)
		}
	}
	memory, ok := compatibilityCheckpointControl("/memory/v1/chat/completions")
	if !ok || memory.Namespace != "memory-provider" || !memory.ForceNew {
		t.Fatalf("memory checkpoint control = %#v, %t", memory, ok)
	}
	hermes, ok := compatibilityCheckpointControl("/hermes/v1/chat/completions")
	if !ok || hermes.Namespace != "hermes" || hermes.ForceNew {
		t.Fatalf("Hermes checkpoint control = %#v, %t", hermes, ok)
	}
}

func TestHermesCompatibilityToggleControlsOnlyDedicatedProfileRoute(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`
	settings := server.settings.get()
	settings.HermesCompatibilityEnabled = false
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}

	disabled := httptest.NewRecorder()
	server.hermesOpenAIChat(disabled, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "Hermes compatibility profile is disabled") {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	settings.HermesCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("hermes-profile")); err != nil {
		t.Fatal(err)
	}
	enabled := httptest.NewRecorder()
	enabledRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "hermes-owner")
	server.hermesOpenAIChat(enabled, enabledRequest)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enabled status=%d body=%s", enabled.Code, enabled.Body.String())
	}
}

func TestHermesTextPolicyOverflowUsesRecoverableContextCodeOnlyOnDedicatedRoute(t *testing.T) {
	over := strings.Repeat("x", defaultTextInputLimitUTF16+1)
	body := `{"model":"gpt-5.6-sol","session_key":"hermes-text-policy","messages":[{"role":"user","content":` + mustJSON(over) + `}]}`
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}

	hermes := httptest.NewRecorder()
	hermesRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "hermes-owner")
	server.hermesOpenAIChat(hermes, hermesRequest)
	if hermes.Code != http.StatusBadRequest || wp1ErrorCode(t, hermes) != "context_length_exceeded" {
		t.Fatalf("Hermes status=%d code=%q body=%s", hermes.Code, wp1ErrorCode(t, hermes), hermes.Body.String())
	}
	hermesError := strings.ToLower(hermes.Body.String())
	if !strings.Contains(hermesError, "input is too long") || !strings.Contains(hermes.Body.String(), "128000") || !strings.Contains(hermesError, "utf-16") {
		t.Fatalf("Hermes compatibility error lacks the recoverable marker or real UTF-16 policy: %s", hermes.Body.String())
	}

	generic := httptest.NewRecorder()
	genericRequest := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), "generic-owner")
	server.openaiChat(generic, genericRequest)
	if generic.Code != http.StatusBadRequest || wp1ErrorCode(t, generic) != "text_policy_exceeded" {
		t.Fatalf("generic status=%d code=%q body=%s", generic.Code, wp1ErrorCode(t, generic), generic.Body.String())
	}
}

func TestMemoryCompatibilityRouteRequiresExplicitOptIn(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.chat = &captureSingleAccountChat{}
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"remember this"}]}`

	disabled := httptest.NewRecorder()
	server.memoryOpenAIChat(disabled, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if disabled.Code != http.StatusServiceUnavailable || !strings.Contains(disabled.Body.String(), "Memory Provider compatibility profile is disabled") {
		t.Fatalf("disabled status=%d body=%s", disabled.Code, disabled.Body.String())
	}

	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("memory-opt-in")); err != nil {
		t.Fatal(err)
	}
	enabled := httptest.NewRecorder()
	server.memoryOpenAIChat(enabled, httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)))
	if enabled.Code != http.StatusOK {
		t.Fatalf("enabled status=%d body=%s", enabled.Code, enabled.Body.String())
	}
}

func TestMemorySchemaInstructionPinsProtocolKeys(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"city": map[string]any{"type": "string"}},
			"required":             []any{"city"},
			"additionalProperties": false,
		},
	}}
	got := memorySchemaInstruction(format)
	for _, want := range []string{"copy them exactly", "never translate", `"city"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("instruction missing %q: %s", want, got)
		}
	}
}

func TestMemorySchemaRepairPromptIsBoundedToPreviousCandidate(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{"type": "object", "additionalProperties": false},
	}}
	got := memorySchemaRepairPrompt(`{"城市":"台中"}`, format, errors.New("unknown property"))
	for _, want := range []string{"Repair the PREVIOUS_CANDIDATE only", "must preserve the exact container structure, property order, and scalar values", `{"城市":"台中"}`, "unknown property"} {
		if !strings.Contains(got, want) {
			t.Fatalf("repair prompt missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "ORIGINAL_REQUEST") {
		t.Fatalf("repair prompt can answer the original request again: %s", got)
	}
}

func TestMemoryRepairPreservesFactsAllowsKeyRepair(t *testing.T) {
	if err := memoryRepairPreservesFacts(`{"城市":"台中","語言":"繁體中文"}`, `{"city":"台中","language":"繁體中文"}`); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryRepairPreservesFactsRejectsAmbiguousSchemaAssociation(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city":     map[string]any{"type": "string"},
				"language": map[string]any{"type": "string"},
			},
			"required": []any{"city", "language"},
		},
	}}
	if err := memoryRepairPreservesFacts(`{"城市":"台中","語言":"繁體中文"}`, `{"language":"台中","city":"繁體中文"}`, format); err == nil {
		t.Fatal("accepted ambiguous scalar-to-property swap")
	}
}

func TestMemoryRepairPreservesFactsAllowsProvableSchemaAssociation(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
				"age":  map[string]any{"type": "integer"},
			},
			"required": []any{"city", "age"},
		},
	}}
	if err := memoryRepairPreservesFacts(`{"城市":"台中","年齡":30}`, `{"city":"台中","age":30}`, format); err != nil {
		t.Fatalf("rejected provable key repair: %v", err)
	}
}

func TestMemoryRepairPreservesFactsRejectsInventedOrDuplicatedValues(t *testing.T) {
	for _, repaired := range []string{`{"city":"新竹"}`, `{"city":"台中","previous_city":"台中"}`} {
		if err := memoryRepairPreservesFacts(`{"城市":"台中"}`, repaired); err == nil {
			t.Fatalf("accepted unsafe repair %s", repaired)
		}
	}
}

func TestMemoryRepairPreservesFactsRejectsMalformedCandidate(t *testing.T) {
	if err := memoryRepairPreservesFacts(`not-json`, `{"city":"台中"}`); err == nil {
		t.Fatal("accepted repair from malformed candidate")
	}
}
