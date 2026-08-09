package chathub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWP6ToolProtocolCarriesParallelCeiling(t *testing.T) {
	definition, err := json.Marshal(map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}})
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{Type: "function", Function: definition}
	parallel := toolProtocolPrompt("inspect", []Tool{tool}, "auto", 2)
	if !strings.Contains(parallel, "at most 2 fenced tool blocks") || strings.Contains(parallel, "ONLY one fenced block") {
		t.Fatalf("parallel prompt=%s", parallel)
	}
	serial := toolProtocolPrompt("inspect", []Tool{tool}, "auto", 1)
	if !strings.Contains(serial, "at most 1 fenced tool blocks") || !strings.Contains(serial, "one at a time") {
		t.Fatalf("serial prompt=%s", serial)
	}
}
