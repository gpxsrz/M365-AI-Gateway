package chathub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func wp6Phase5SearchAndReasoningFrame() json.RawMessage {
	return json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[` +
		`{"messageType":"Progress","contentType":"SearchResults","text":"searching"},` +
		`{"messageType":"Progress","contentOrigin":"ChainOfThoughtSummary","text":"REAL_SUMMARY","hiddenText":"DO_NOT_EXPOSE"},` +
		`{"messageType":"Progress","addToChainOfThought":true,"text":"REAL_INCREMENT"},` +
		`{"messageType":"Progress","text":"GENERIC_PROGRESS"},` +
		`{"messageType":"","contentOrigin":"ChainOfThoughtSummary","text":"NOT_PROGRESS"},` +
		`{"messageType":"","contentOrigin":"DeepLeo","text":"answer","references":{` +
		`"ref-b":{"targetLink":"https://learn.microsoft.com/b","isCitedInResponse":false,"displayData":{"type":"Web","content":"B"}},` +
		`"ref-a":{"targetLink":"https://support.microsoft.com/a","isCitedInResponse":true,"displayData":{"type":"Web","content":"A"}},` +
		`"unsafe":{"targetLink":"javascript:alert(1)","isCitedInResponse":true},` +
		`"artifact":{"targetLink":"https://artifact.invalid/token","isCitedInResponse":true,"fileStoreType":"AMS","pluginName":"python_execution"}` +
		`}}]}]}`)
}

func TestWP6ReasoningSummaryRequiresVerifiedUpstreamProvenance(t *testing.T) {
	raw := []json.RawMessage{wp6Phase5SearchAndReasoningFrame()}
	if got := ReasoningContent(raw); got != "REAL_SUMMARY\nREAL_INCREMENT" {
		t.Fatalf("reasoning content=%q", got)
	}
	events := SemanticEvents(raw)
	var reasoning []string
	for _, event := range events {
		if event.Kind == "reasoning.summary" {
			reasoning = append(reasoning, event.Text)
		}
	}
	if strings.Join(reasoning, "|") != "REAL_SUMMARY|REAL_INCREMENT" {
		t.Fatalf("reasoning events=%#v", reasoning)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "DO_NOT_EXPOSE") {
		t.Fatalf("hidden chain-of-thought leaked into semantic events: %s", encoded)
	}
}

func TestWP6StreamClassifierSeparatesReasoningFromVisibleText(t *testing.T) {
	events := classifyUpdateMessages([]any{
		map[string]any{"messageType": "Progress", "contentOrigin": "ChainOfThoughtSummary", "text": "REAL_SUMMARY"},
		map[string]any{"messageType": "Progress", "addToChainOfThought": true, "text": "REAL_INCREMENT"},
		map[string]any{"messageType": "Progress", "text": "GENERIC_PROGRESS"},
		map[string]any{"text": "VISIBLE"},
	})
	if len(events) != 4 || events[0].Kind != "reasoning" || events[1].Kind != "reasoning" || events[2].Kind != "progress" || events[3].Kind != "text" {
		t.Fatalf("classified events=%#v", events)
	}
}

func TestWP6ExtractsSafeSearchReferencesAndSearchEvidence(t *testing.T) {
	raw := []json.RawMessage{wp6Phase5SearchAndReasoningFrame()}
	references := SearchReferences(raw, "")
	if len(references) != 2 || references[0].ID != "ref-a" || references[1].ID != "ref-b" {
		t.Fatalf("references=%#v", references)
	}
	if references[0].TargetLink != "https://support.microsoft.com/a" || !references[0].IsCitedInResponse || references[0].DisplayData["content"] != "A" {
		t.Fatalf("reference mapping=%#v", references[0])
	}
	summary := SummarizeSearchEvents(raw)
	if summary.SearchResultMarkers == 0 || summary.SearchProgress == 0 {
		t.Fatalf("search evidence=%#v", summary)
	}
}

func TestWP6SearchReferenceProjectionIsBoundedAndDeterministic(t *testing.T) {
	references := map[string]any{}
	for i := 69; i >= 0; i-- {
		id := fmt.Sprintf("ref-%02d", i)
		references[id] = map[string]any{"targetLink": "https://example.test/" + id, "displayData": map[string]any{"content": strings.Repeat("x", 5000)}}
	}
	frame, err := json.Marshal(map[string]any{"type": 1, "target": "update", "arguments": []any{map[string]any{"messages": []any{map[string]any{"references": references}}}}})
	if err != nil {
		t.Fatal(err)
	}
	got := SearchReferences([]json.RawMessage{frame}, "")
	if len(got) != maxSearchReferences || got[0].ID != "ref-00" || got[len(got)-1].ID != "ref-63" {
		t.Fatalf("bounded references=%#v", got)
	}
	if len([]rune(got[0].DisplayData["content"].(string))) != 4096 {
		t.Fatalf("display content was not bounded: %d", len([]rune(got[0].DisplayData["content"].(string))))
	}
}

func TestWP6ChatPayloadAlwaysRegistersNativeBingSeparately(t *testing.T) {
	tool := Tool{Type: "function", Function: json.RawMessage(`{"name":"read_file","description":"read","parameters":{"type":"object"}}`)}
	for _, tools := range [][]Tool{nil, {tool}} {
		payload := chatPayload("question", "session", "conversation", "request", "Magic", true, nil, tools, "auto", 2, "")
		parts := strings.Split(payload, rs)
		var frame map[string]any
		if json.Unmarshal([]byte(parts[0]), &frame) != nil {
			t.Fatalf("invalid payload: %s", payload)
		}
		arguments := frame["arguments"].([]any)[0].(map[string]any)
		plugins := arguments["plugins"].([]any)
		if len(plugins) != len(tools)+1 {
			t.Fatalf("plugins=%#v", plugins)
		}
		bing := plugins[0].(map[string]any)
		if bing["Id"] != "BingWebSearch" || bing["Source"] != "BuiltIn" {
			t.Fatalf("native Bing plugin=%#v", bing)
		}
	}
}

func TestWP6CallerToolPromptPreservesNativeBing(t *testing.T) {
	tool := Tool{Type: "function", Function: json.RawMessage(`{"name":"read_file","description":"read","parameters":{"type":"object"}}`)}
	prompt := toolProtocolPrompt("question", []Tool{tool}, "auto", 2)
	for _, phrase := range []string{"caller", "Microsoft native Bing", "citations", "grounding", "read-only"} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("prompt missing %q: %s", phrase, prompt)
		}
	}
}
