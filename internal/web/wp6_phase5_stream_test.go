package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func TestWP6ResponsesNonStreamMapsOnlyUpstreamReasoningSummary(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "VISIBLE_FINAL", Events: wp6Phase5RawEvents()}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"reason"}`)))

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	output, _ := response["output"].([]any)
	if recorder.Code != http.StatusOK || len(output) < 2 {
		t.Fatalf("status=%d response=%#v", recorder.Code, response)
	}
	reasoning, _ := output[0].(map[string]any)
	summary, _ := reasoning["summary"].([]any)
	if reasoning["type"] != "reasoning" || len(summary) != 1 || summary[0].(map[string]any)["text"] != "REAL_REASONING_SUMMARY" {
		t.Fatalf("reasoning output=%#v", reasoning)
	}
	if strings.Contains(recorder.Body.String(), "HIDDEN_CHAIN") || strings.Contains(recorder.Body.String(), "正在分析请求并准备回答") {
		t.Fatalf("Responses non-stream leaked or fabricated reasoning: %s", recorder.Body.String())
	}
}

func TestWP6ResponsesReasoningOnlyIsDeliverable(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &wp1CandidateChat{
				result: chathub.Result{Events: wp6Phase5RawEvents()},
				events: []chathub.StreamEvent{{Kind: "reasoning", Text: "REAL_REASONING_SUMMARY"}},
			}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			body := fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"reason only","stream":%t}`, stream)
			server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			payload := recorder.Body.String()
			if recorder.Code != http.StatusOK || strings.Contains(payload, "upstream_empty_response") || !strings.Contains(payload, "REAL_REASONING_SUMMARY") {
				t.Fatalf("reasoning-only response was not deliverable: status=%d body=%s", recorder.Code, payload)
			}
			if stream && !strings.Contains(payload, "event: response.completed") {
				t.Fatalf("reasoning-only stream did not complete: %s", payload)
			}
			if !stream {
				var response map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				output, _ := response["output"].([]any)
				if len(output) != 1 || output[0].(map[string]any)["type"] != "reasoning" {
					t.Fatalf("reasoning-only output=%#v", output)
				}
			}
		})
	}
}

func TestWP6ResponsesDoesNotFabricateReasoning(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "VISIBLE_FINAL"}}
			server := newWP1CandidateServer(t, chat)
			recorder := httptest.NewRecorder()
			body := fmt.Sprintf(`{"model":"gpt-5.6-sol","input":"reason","stream":%t}`, stream)
			server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
			if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), `"type":"reasoning"`) {
				t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWP6LegacyStreamExposesOnlyUpstreamReasoningSummary(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{
		Text:   "VISIBLE_FINAL",
		Events: wp6Phase5RawEvents(),
	}}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.chatStream(recorder, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"gpt-5.6-sol","message":"reason"}`)))

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"reasoning_content":"REAL_REASONING_SUMMARY"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, body)
	}
	for _, forbidden := range []string{"正在分析请求并准备回答", "HIDDEN_CHAIN"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("legacy stream leaked or fabricated reasoning %q: %s", forbidden, body)
		}
	}
}

func TestWP6B3StreamingTerminalKeepsSearchEvidence(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{
			Text:   "```read_file\n{\"path\":\"a\"}\n```",
			Events: wp6Phase5RawEvents(),
		},
		events: []chathub.StreamEvent{{Kind: "text", Text: "```read_file\n{\"path\":\"a\"}\n```"}},
	}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"search and inspect"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}}}]}`
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	stream := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(stream, `"finish_reason":"tool_calls"`) || !strings.Contains(stream, `"search_result_markers":1`) || !strings.Contains(stream, `"targetLink":"https://support.microsoft.com/topic"`) {
		t.Fatalf("status=%d stream=%s", recorder.Code, stream)
	}
	if strings.Contains(stream, "HIDDEN_CHAIN") {
		t.Fatalf("stream leaked hidden upstream field: %s", stream)
	}
}

func TestWP6ResponsesStreamCompletionKeepsSearchEvidence(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: "Grounded answer", Events: wp6Phase5RawEvents()},
		events: []chathub.StreamEvent{{Kind: "reasoning", Text: "REAL_REASONING_SUMMARY"}, {Kind: "text", Text: "Grounded answer"}},
	}
	server := newWP1CandidateServer(t, chat)
	recorder := httptest.NewRecorder()
	server.responses(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":"search"}`)))

	stream := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(stream, "event: response.completed") || !strings.Contains(stream, "event: response.reasoning_summary_text.delta") || !strings.Contains(stream, `"delta":"REAL_REASONING_SUMMARY"`) || !strings.Contains(stream, `"search_result_markers":1`) || !strings.Contains(stream, `"targetLink":"https://support.microsoft.com/topic"`) {
		t.Fatalf("status=%d stream=%s", recorder.Code, stream)
	}
	if strings.Contains(stream, "HIDDEN_CHAIN") || strings.Contains(stream, "正在分析请求并准备回答") {
		t.Fatalf("Responses stream leaked hidden upstream field: %s", stream)
	}
}
