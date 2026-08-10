package web

import (
	"errors"
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
