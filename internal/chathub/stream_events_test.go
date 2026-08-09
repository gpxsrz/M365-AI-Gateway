package chathub

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestWebSocketDialFailurePreservesStatusAndCause(t *testing.T) {
	cause := errors.New("bad handshake")
	withStatus := webSocketDialFailure(http.StatusForbidden, cause)
	if !errors.Is(withStatus, cause) || !strings.Contains(withStatus.Error(), "HTTP 403") {
		t.Fatalf("status dial error=%q", withStatus)
	}
	withoutStatus := webSocketDialFailure(0, cause)
	if !errors.Is(withoutStatus, cause) || !strings.Contains(withoutStatus.Error(), "ws dial: bad handshake") {
		t.Fatalf("statusless dial error=%q", withoutStatus)
	}
}

func TestClassifyUpdateMessages(t *testing.T) {
	got := classifyUpdateMessages([]any{
		map[string]any{"author": "bot", "text": "我先查一下", "messageType": ""},
		map[string]any{"messageType": "Progress", "contentType": "SearchResults", "text": "正在搜索"},
		map[string]any{"toolName": "web_search", "arguments": map[string]any{"query": "golang"}},
	})
	if len(got) != 3 || got[0].Kind != "text" || got[1].Kind != "progress" || got[2].Kind != "tool" {
		t.Fatalf("unexpected events: %#v", got)
	}
	if got[2].ToolName != "web_search" || len(got[2].Arguments) == 0 {
		t.Fatalf("tool fields missing: %#v", got[2])
	}
}

func TestExtractToolEventsNestedAndDeduped(t *testing.T) {
	seen := map[string]bool{}
	arg := map[string]any{"plugin": map[string]any{"functionName": "web_search", "functionArguments": map[string]any{"query": "golang"}, "messageType": "Progress", "contentType": "SearchResults"}}
	got := extractToolEvents([]any{arg, arg}, seen)
	if len(got) != 1 || got[0].ToolName != "web_search" || got[0].MessageType != "Progress" || got[0].ContentType != "SearchResults" {
		t.Fatalf("unexpected nested tools: %#v", got)
	}
}

func TestExtractToolEventsPreservesArgumentlessToolShape(t *testing.T) {
	got := extractToolEvents(map[string]any{
		"messageType": "Progress",
		"contentType": "ToolCall",
		"pluginName":  "delete_file",
	}, map[string]bool{})
	if len(got) != 1 || got[0].Kind != "tool" || got[0].ToolName != "delete_file" || len(got[0].Arguments) != 0 {
		t.Fatalf("unexpected argumentless tool event: %#v", got)
	}
}
