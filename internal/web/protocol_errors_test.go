package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicErrorEnvelope(t *testing.T) {
	rr := httptest.NewRecorder()
	writeAnthropicError(rr, 400, "invalid_request_error", "bad json")
	var v map[string]any
	if json.Unmarshal(rr.Body.Bytes(), &v) != nil || v["type"] != "error" {
		t.Fatalf("body=%s", rr.Body.String())
	}
	e, _ := v["error"].(map[string]any)
	if e["type"] != "invalid_request_error" || e["message"] != "bad json" {
		t.Fatalf("body=%s", rr.Body.String())
	}
	if !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		t.Fatal("missing json content type")
	}
}
func TestOpenAIErrorEnvelopeAndExtraction(t *testing.T) {
	rr := httptest.NewRecorder()
	writeResponsesError(rr, 409, "tool_round_limit", "limit reached")
	if got := errorMessage(rr.Body.Bytes(), "fallback"); got != "limit reached" {
		t.Fatalf("message=%q", got)
	}
}
func TestMaxToolRoundsConfig(t *testing.T) {
	t.Setenv("M365_MAX_TOOL_ROUNDS", "7")
	if configuredMaxToolRounds(openSettingsStore()) != 7 {
		t.Fatal("configured limit ignored")
	}
	t.Setenv("M365_MAX_TOOL_ROUNDS", "9999")
	if configuredMaxToolRounds(openSettingsStore()) != 16 {
		t.Fatal("invalid limit did not fall back to the generic 16-round ceiling")
	}
}
