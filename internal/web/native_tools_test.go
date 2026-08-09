package web

import (
	"encoding/json"
	"m365-native/internal/chathub"
	"testing"
)

func TestNativeToolCallsOnlyFromFrame(t *testing.T) {
	tools := []chathub.Tool{{Type: "function", Function: json.RawMessage(`{"name":"get_current_time","parameters":{"type":"object"}}`)}}
	events := []json.RawMessage{json.RawMessage(`{"type":1,"target":"plugin","arguments":{"pluginName":"get_current_time","arguments":{"timezone":"Asia/Shanghai"}}}`)}
	c := nativeToolCalls(events, tools)
	if len(c) != 1 || c[0].Name != "get_current_time" || string(c[0].Arguments) != "{"+`"timezone":"Asia/Shanghai"`+"}" {
		t.Fatalf("%+v", c)
	}
	if len(nativeToolCalls([]json.RawMessage{json.RawMessage(`{"text":"现在几点"}`)}, tools)) != 0 {
		t.Fatal("inferred a tool call")
	}
	mixed := []json.RawMessage{json.RawMessage(`{"codeResultImageUrl":"https://cdn.example.test/protected-output.png","safe":{"pluginName":"get_current_time","arguments":{"timezone":"UTC"}}}`)}
	mixedCalls := nativeToolCalls(mixed, tools)
	if len(mixedCalls) != 1 || mixedCalls[0].Name != "get_current_time" || string(mixedCalls[0].Arguments) != `{"timezone":"UTC"}` {
		t.Fatalf("protected sibling suppressed safe native tool: %#v", mixedCalls)
	}
}
