package jsonschemautil

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeExactUsesJSONNumberAndRejectsTrailingValues(t *testing.T) {
	var value map[string]any
	if err := DecodeExact([]byte(`{"n":9007199254740993}`), &value); err != nil {
		t.Fatal(err)
	}
	if got, ok := value["n"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("decoded number = %#v", value["n"])
	}
	if err := DecodeExact([]byte(`{"a":1} {"b":2}`), &value); err == nil || err.Error() != "one JSON value required" {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestCompileRejectsExternalReferences(t *testing.T) {
	_, err := Compile(map[string]any{"$ref": "https://example.test/schema.json"}, "urn:test:external-ref")
	if err == nil || !strings.Contains(err.Error(), "external JSON Schema reference is not allowed") {
		t.Fatalf("external reference error = %v", err)
	}

	compiled, err := Compile(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []any{"name"},
	}, "urn:test:local")
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(map[string]any{"name": "ok"}); err != nil {
		t.Fatal(err)
	}
}
