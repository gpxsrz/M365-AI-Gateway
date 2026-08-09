package chathub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarizeSearchEventsReturnsOnlyRedactedClassesAndCounts(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{
		"type":1,
		"target":"update",
		"arguments":[{"messages":[
			{"messageType":"Progress","contentType":"SearchResults","text":"SENSITIVE_QUERY","searchQueries":["SENSITIVE_QUERY"],"sourceAttributions":[{"provider":"bing","url":"https://sensitive.example/path"}]},
			{"messageType":"","contentType":"","text":"SENSITIVE_ANSWER"}
		]}]
	}`)}

	summary := SummarizeSearchEvents(raw)
	if summary.RawFrames != 1 || summary.SearchProgress != 1 || summary.Message != 1 || summary.SourceAttributionMarkers != 1 || summary.SearchResultMarkers != 1 || !summary.SourceAttributionObserved {
		t.Fatalf("summary=%#v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SENSITIVE_QUERY", "SENSITIVE_ANSWER", "sensitive.example", "provider", "url", "text", "queries"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("redacted summary leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSummarizeSearchEventsCountsNestedMarkersWithoutContent(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":6}`),
		json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"Progress","contentType":"Code","text":"secret code"},{"messageType":"Progress","contentType":"Other","text":"secret tool"}]}],"nested":{"sourceAttribution":{"id":"secret"},"contentType":"SearchResults"}}`),
	}
	summary := SummarizeSearchEvents(raw)
	if summary.RawFrames != 2 || summary.CodeProgress != 1 || summary.ToolProgress != 1 || summary.SourceAttributionMarkers != 1 || summary.SearchResultMarkers != 1 || !summary.SourceAttributionObserved {
		t.Fatalf("summary=%#v", summary)
	}
}
