package chathub

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestToolIngressPreservesOpaqueTopLevelExtensions(t *testing.T) {
	raw := []byte(`{
		"type":"function",
		"function":{"name":"lookup","description":"find","parameters":{"type":"object","x-schema-extension":{"id":9007199254740993}},"future_function_extension":{"x":1}},
		"future_tool_annotations":{"readOnlyHint":true,"opaque_id":9007199254740993}
	}`)
	var tool Tool
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&tool); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "function" || !bytes.Contains(tool.Function, []byte(`"future_function_extension"`)) {
		t.Fatalf("canonical tool/function changed: %#v", tool)
	}
	if !bytes.Contains(tool.IngressRaw, []byte(`"future_tool_annotations"`)) ||
		!bytes.Contains(tool.IngressExtensions["future_tool_annotations"], []byte(`9007199254740993`)) {
		t.Fatalf("tool extension evidence lost: raw=%s extensions=%v", tool.IngressRaw, tool.IngressExtensions)
	}
	if !bytes.Contains(tool.FunctionExtensions["future_function_extension"], []byte(`"x":1`)) {
		t.Fatalf("function extension evidence lost: %v", tool.FunctionExtensions)
	}
	canonical, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("future_tool_annotations")) || bytes.Contains(canonical, []byte("future_function_extension")) {
		t.Fatalf("request-scoped tool evidence leaked into canonical serialization: %s", canonical)
	}
	if !bytes.Contains(canonical, []byte(`"x-schema-extension":{"id":9007199254740993}`)) {
		t.Fatalf("supported parameters schema was not preserved: %s", canonical)
	}
}
