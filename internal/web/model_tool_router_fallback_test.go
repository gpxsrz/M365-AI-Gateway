package web

import (
	"context"
	"encoding/json"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type issue66PhaseChat struct {
	requests   []chathub.Request
	routeText  string
	repairText string
	finalText  string
	withRepair bool
}

func (f *issue66PhaseChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	phase := len(f.requests)
	f.requests = append(f.requests, req)

	conversationID := req.ConversationID
	sessionID := req.SessionID
	if conversationID == "" {
		conversationID = fmt.Sprintf("scratch-conversation-%d", phase)
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("scratch-session-%d", phase)
	}

	text := f.finalText
	switch {
	case phase == 0:
		text = f.routeText
	case f.withRepair && phase == 1:
		text = f.repairText
	default:
		// Reproduce #66: if final-answer continues the same non-empty
		// conversation used by the router, ChatHub behaves as if the router
		// instruction still applies and emits another tool-router envelope.
		if len(f.requests) > 1 && f.requests[0].ConversationID != "" && req.ConversationID == f.requests[0].ConversationID {
			text = `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`
		}
	}

	return chathub.Result{
		Text:           text,
		FinalText:      text,
		StreamedText:   text,
		TextRelation:   "equal",
		TextSource:     "final",
		ConversationID: conversationID,
		SessionID:      sessionID,
	}, nil
}

func (f *issue66PhaseChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, emit func(string) error) (chathub.Result, error) {
	result, err := f.Chat(ctx, account, req)
	if err == nil && emit != nil && result.Text != "" {
		if emitErr := emit(result.Text); emitErr != nil {
			return chathub.Result{}, emitErr
		}
	}
	return result, err
}

func (f *issue66PhaseChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	result, err := f.Chat(ctx, account, req)
	if err == nil && emit != nil && result.Text != "" {
		if emitErr := emit(chathub.StreamEvent{Kind: "text", Text: result.Text}); emitErr != nil {
			return chathub.Result{}, emitErr
		}
	}
	return result, err
}

func issue66CheckpointServer(t *testing.T, chat *issue66PhaseChat, owner string) *Server {
	t.Helper()
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "issue66-checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, server.checkpoints, "chat-completions", owner, "", []oaiMsg{{Role: "user", Content: "Earlier request."}})
	acceptForTest(t, turn, "public-conversation", "public-session", []oaiMsg{{Role: "assistant", Content: "Earlier answer."}}, "")
	return server
}

func issue66ContinuationBody(routeRequest string) string {
	return `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Earlier request."},
			{"role":"assistant","content":"Earlier answer."},
			{"role":"user","content":` + mustJSON(routeRequest) + `}
		],
		"attachments":[{"type":"file","url":"data:text/plain;base64,QQ==","name":"evidence.txt","mimeType":"text/plain"}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
}

const routerFallbackTool = `{
	"type":"function",
	"function":{
		"name":"terminal",
		"description":"Run a command.",
		"parameters":{
			"type":"object",
			"properties":{"command":{"type":"string"}},
			"required":["command"]
		}
	}
}`

const executeCodeRouterTool = `{
	"type":"function",
	"function":{
		"name":"execute_code",
		"description":"Execute Python code.",
		"parameters":{
			"type":"object",
			"properties":{"code":{"type":"string"}},
			"required":["code"]
		}
	}
}`

func longExecuteCodeRoutingOutput(t *testing.T, repeat int) (string, string) {
	t.Helper()
	const sentinel = "MIDDLE_SENTINEL_REQUIRED_FOR_VALID_PYTHON"
	code := "print('BEGIN')\n" +
		strings.Repeat("# padding before sentinel\n", repeat) +
		"if " + sentinel + " := True:\n" +
		strings.Repeat("    pass  # padding after sentinel\n", repeat) +
		"print('END')\n"
	raw, err := json.Marshal(map[string]any{
		"calls": []any{map[string]any{
			"name":      "execute_code",
			"arguments": map[string]any{"code": code},
		}},
		"answer": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), sentinel
}

func TestAutoToolRouterLongValidStructuredArgumentsBypassRepair(t *testing.T) {
	routeOutput, sentinel := longExecuteCodeRoutingOutput(t, 180)
	if len(routeOutput) <= 6000 {
		t.Fatalf("fixture must exceed historical repair preview limit, got %d bytes", len(routeOutput))
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{{Text: routeOutput}}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 1 {
				t.Fatalf("upstream requests=%d, valid routing JSON must not enter repair", len(chat.requests))
			}
			if !strings.Contains(rr.Body.String(), sentinel) || !strings.Contains(rr.Body.String(), `"name":"execute_code"`) {
				t.Fatalf("long execute_code call lost identity or sentinel: %s", rr.Body.String())
			}
		})
	}
}

func TestIssue66RouterAndRepairAreIsolatedFromPublicFinalAnswer(t *testing.T) {
	const owner = "issue66-router-repair"
	chat := &issue66PhaseChat{
		routeText:  "not a routing envelope",
		repairText: "still not a routing envelope",
		finalText:  "SAFE_PUBLIC_FINAL",
		withRepair: true,
	}
	server := issue66CheckpointServer(t, chat, owner)
	rr := httptest.NewRecorder()
	request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(issue66ContinuationBody("Check status."))), owner)
	server.openaiChat(rr, request)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want route, repair, final", len(chat.requests))
	}
	for i := 0; i < 2; i++ {
		if chat.requests[i].ConversationID != "" || chat.requests[i].SessionID != "" {
			t.Fatalf("scratch phase %d reused public binding: conversation=%q session=%q", i, chat.requests[i].ConversationID, chat.requests[i].SessionID)
		}
		if len(chat.requests[i].Attachments) != 0 {
			t.Fatalf("scratch phase %d carried caller attachments: %#v", i, chat.requests[i].Attachments)
		}
	}
	finalRequest := chat.requests[2]
	if finalRequest.ConversationID != "public-conversation" || finalRequest.SessionID != "public-session" {
		t.Fatalf("final answer binding=%q/%q, want public checkpoint binding", finalRequest.ConversationID, finalRequest.SessionID)
	}
	if len(finalRequest.Attachments) != 1 || finalRequest.Attachments[0].Name != "evidence.txt" {
		t.Fatalf("public final answer lost caller attachment: %#v", finalRequest.Attachments)
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "SAFE_PUBLIC_FINAL" {
		t.Fatalf("content=%#v body=%s", message["content"], rr.Body.String())
	}
	views := checkpointViewsForTest(t, server.checkpoints)
	if len(views) != 1 || views[0].ConversationID != "public-conversation" || views[0].SessionID != "public-session" {
		t.Fatalf("scratch phases mutated public checkpoint binding: %#v", views)
	}
}

func TestIssue66KnownCallSuppressionDoesNotFallIntoContaminatedFinalAnswer(t *testing.T) {
	const owner = "issue66-known-call"
	chat := &issue66PhaseChat{
		routeText: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`,
		finalText: "KNOWN_CALL_PUBLIC_FINAL",
	}
	server := issue66CheckpointServer(t, chat, owner)
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Earlier request."},
			{"role":"assistant","content":"Earlier answer."},
			{"role":"user","content":"Check status."},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_status","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"status\"}"}}]},
			{"role":"tool","tool_call_id":"call_status","content":"ok"},
			{"role":"user","content":"Continue without repeating the same call."}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), owner)
	server.openaiChat(rr, request)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want isolated route then public final answer", len(chat.requests))
	}
	if chat.requests[0].ConversationID != "" || chat.requests[0].SessionID != "" {
		t.Fatalf("known-call route reused public binding: %#v", chat.requests[0])
	}
	if chat.requests[1].ConversationID != "public-conversation" || chat.requests[1].SessionID != "public-session" {
		t.Fatalf("known-call final answer binding=%q/%q", chat.requests[1].ConversationID, chat.requests[1].SessionID)
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("known call was reissued: %#v", message)
	}
	if message["content"] != "KNOWN_CALL_PUBLIC_FINAL" {
		t.Fatalf("content=%#v body=%s", message["content"], rr.Body.String())
	}
}

func TestAutoToolRouterUsesValidatedStreamWhenFinalSnapshotIsMalformed(t *testing.T) {
	validOutput, sentinel := longExecuteCodeRoutingOutput(t, 80)
	malformedFinal := validOutput[:len(validOutput)-900]
	if json.Valid([]byte(malformedFinal)) {
		t.Fatal("fixture final snapshot must be malformed")
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{{
				Text:         malformedFinal,
				FinalText:    malformedFinal,
				StreamedText: validOutput,
				TextRelation: "divergent",
				TextSource:   "final",
			}}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 1 {
				t.Fatalf("upstream requests=%d, validated streamed router decision must bypass repair", len(chat.requests))
			}
			if !strings.Contains(rr.Body.String(), sentinel) || !strings.Contains(rr.Body.String(), `"name":"execute_code"`) {
				t.Fatalf("validated streamed tool call lost identity or sentinel: %s", rr.Body.String())
			}
		})
	}
}

func TestAutoToolRouterUsesValidatedStreamDirectAnswerWhenFinalSnapshotIsMalformed(t *testing.T) {
	final := `{"calls":[]`
	streamed := `{"calls":[],"answer":"STREAM_RECOVERED_DIRECT_ANSWER"}`
	chat := &continuationChat{results: []chathub.Result{
		{
			Text:         final,
			FinalText:    final,
			StreamedText: streamed,
			TextRelation: "divergent",
			TextSource:   "final",
		},
		{Text: "STREAM_RECOVERED_DIRECT_ANSWER"},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Give the grounded answer."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, validated streamed direct answer must bypass repair but still use an isolated public final-answer turn", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	message := response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "STREAM_RECOVERED_DIRECT_ANSWER" {
		t.Fatalf("content=%#v body=%s", message["content"], rr.Body.String())
	}
}

func TestAutoToolRouterDoesNotLongestWinDivergentInvalidStream(t *testing.T) {
	validOutput := `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`
	malformedFinal := strings.TrimSuffix(validOutput, "}")
	invalidStream := `{"calls":[{"name":"terminal","arguments":{"command":2}}],"answer":""}` + strings.Repeat(" ", 2000)
	chat := &continuationChat{results: []chathub.Result{
		{Text: malformedFinal, FinalText: malformedFinal, StreamedText: invalidStream, TextRelation: "divergent", TextSource: "final"},
		{Text: validOutput},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Check status."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, divergent schema-invalid stream must not bypass repair", len(chat.requests))
	}
	if strings.Contains(chat.requests[1].Text, invalidStream) {
		t.Fatal("repair incorrectly used divergent schema-invalid streamed text")
	}
	if !strings.HasSuffix(chat.requests[1].Text, malformedFinal) {
		t.Fatalf("repair did not preserve the primary malformed final snapshot: %s", chat.requests[1].Text)
	}
}

func TestAutoToolRouterRepairReceivesCompleteLongStructuredArguments(t *testing.T) {
	validOutput, sentinel := longExecuteCodeRoutingOutput(t, 180)
	malformedOutput := strings.TrimSuffix(validOutput, "}")
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{
				{Text: malformedOutput},
				{Text: validOutput},
			}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 2 {
				t.Fatalf("upstream requests=%d, want router and repair", len(chat.requests))
			}
			repairPrompt := chat.requests[1].Text
			if !strings.Contains(repairPrompt, sentinel) || !strings.HasSuffix(repairPrompt, malformedOutput) {
				t.Fatalf("repair input was not lossless: prompt_len=%d output_len=%d sentinel=%t", len(repairPrompt), len(malformedOutput), strings.Contains(repairPrompt, sentinel))
			}
			if !strings.Contains(rr.Body.String(), sentinel) {
				t.Fatalf("repaired execute_code call lost sentinel: %s", rr.Body.String())
			}
		})
	}
}

func TestAutoToolRouterOversizeRepairFailsClosedBeforeSecondUpstreamCall(t *testing.T) {
	validOutput, _ := longExecuteCodeRoutingOutput(t, 2300)
	malformedOutput := strings.TrimSuffix(validOutput, "}")
	if utf16CodeUnits(malformedOutput) <= defaultTextInputLimitUTF16 {
		t.Fatalf("fixture must exceed repair budget before adding instructions, got %d UTF-16 units", utf16CodeUnits(malformedOutput))
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			chat := &continuationChat{results: []chathub.Result{{Text: malformedOutput}}}
			s := newWP1CandidateServer(t, &wp1CandidateChat{})
			s.chat = chat
			streamField := ""
			if stream {
				streamField = `"stream":true,`
			}
			body := `{` + streamField + `
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Run the supplied code."}],
		"tools":[` + executeCodeRouterTool + `],
		"tool_choice":"auto"
	}`
			rr := httptest.NewRecorder()
			s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

			if rr.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if len(chat.requests) != 1 {
				t.Fatalf("upstream requests=%d, oversize repair must fail before a second upstream call", len(chat.requests))
			}
			response := wp1DecodeJSON(t, rr.Body.String())
			errBody, _ := response["error"].(map[string]any)
			limit, limitOK := errBody["limit"].(float64)
			received, receivedOK := errBody["received"].(float64)
			if errBody["code"] != "tool_router_repair_input_too_large" ||
				errBody["limit_type"] != "repair_prompt_utf16" ||
				errBody["terminal"] != true ||
				errBody["retryable"] != false ||
				errBody["recommended_action"] != "regenerate_tool_routing_decision" ||
				!limitOK || int(limit) != defaultTextInputLimitUTF16 ||
				!receivedOK || int(received) <= defaultTextInputLimitUTF16 {
				t.Fatalf("unexpected machine-readable repair failure: %#v", errBody)
			}
		})
	}
}

func TestAutoToolRouterSchemaInvalidDecisionDoesNotEnterRepair(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":2}}],"answer":""}`},
		{Text: "The proposed tool arguments were invalid."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Check status."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, schema-invalid but parseable JSON must skip repair and continue to answer", len(chat.requests))
	}
	if strings.HasPrefix(chat.requests[1].Text, "Repair the previous tool-routing output") {
		t.Fatalf("schema-invalid decision incorrectly entered repair: %s", chat.requests[1].Text)
	}
}

func TestAutoToolRouterFilteredKnownCallDoesNotEnterRepair(t *testing.T) {
	routeOutput := `{"calls":[{"name":"terminal","arguments":{"command":"status"}}],"answer":""}`
	chat := &continuationChat{results: []chathub.Result{
		{Text: routeOutput},
		{Text: "Status was already checked."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Check status."},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_status","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"status\"}"}}]},
			{"role":"tool","tool_call_id":"call_status","content":"ok"},
			{"role":"user","content":"Continue."}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, filtered duplicate must skip repair and continue to answer", len(chat.requests))
	}
	if strings.HasPrefix(chat.requests[1].Text, "Repair the previous tool-routing output") {
		t.Fatalf("filtered duplicate incorrectly entered repair: %s", chat.requests[1].Text)
	}
}

func TestAutoToolRouterInvalidDecisionFallsBackToText(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: "I am okay."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Are you okay?"}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and ordinary answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices=%#v response=%#v", response["choices"], response)
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("invalid auto routing emitted a tool call: %#v", message)
	}
	if got := message["content"]; got != "I am okay." {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}

func TestRequiredToolRouterInvalidDecisionUsesConstrainedRetry(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}]}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Check status with the tool."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"required"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and required retry", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	calls, _ := message["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls=%#v response=%#v", message["tool_calls"], response)
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if got := function["name"]; got != "terminal" {
		t.Fatalf("tool name=%q response=%#v", got, response)
	}
}

func TestStreamingRequiredToolRouterInvalidDecisionUsesConstrainedRetry(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: `{"calls":[{"name":"terminal","arguments":{"command":"status"}}]}`},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[{"role":"user","content":"Check status with the tool."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"required"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and required retry", len(chat.requests))
	}
	stream := rr.Body.String()
	if !strings.Contains(stream, `"name":"terminal"`) || !strings.Contains(stream, `"finish_reason":"tool_calls"`) {
		t.Fatalf("required retry did not emit terminal call: %s", stream)
	}
	if got := strings.Count(stream, "data: [DONE]"); got != 1 {
		t.Fatalf("done count=%d stream=%s", got, stream)
	}
}

func TestStreamingNamedToolRouterNoCallRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{Text: "NO_TOOL_NEEDED"}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"stream":true,
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want router only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "did not select the requested tool") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestNamedToolRouterInvalidDecisionRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{}, {}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router and repair only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "invalid tool routing decision") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestNamedToolRouterNoCallRemainsFailClosed(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{{Text: "NO_TOOL_NEEDED"}}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Use terminal."}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":{"type":"function","function":{"name":"terminal"}}
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d, want router only", len(chat.requests))
	}
	if !strings.Contains(rr.Body.String(), "did not select the requested tool") {
		t.Fatalf("unexpected body=%s", rr.Body.String())
	}
}

func TestAutoAnswerDoesNotInventUnregisteredBashTool(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: "NO_TOOL_NEEDED"},
		{Text: "Use the amd64 stable package:\n```bash\n{\"command\":\"sudo apt install ./google-chrome-stable_current_amd64.deb\"}\n```"},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Which Chrome build should I use?"}],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d, want router and ordinary answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("unregistered bash tool was emitted: %#v", message)
	}
	if got := fmt.Sprint(message["content"]); !strings.Contains(got, "google-chrome-stable_current_amd64.deb") {
		t.Fatalf("ordinary answer was not preserved: %q", got)
	}
}

func TestAutoToolRouterFallbackStillEnforcesPendingEvidence(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{},
		{},
		{Text: "Deployment completed successfully."},
	}}
	s := newWP1CandidateServer(t, &wp1CandidateChat{})
	s.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Deploy the service."},
			{"role":"assistant","content":null,"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"deploy\"}"}}
			]},
			{"role":"user","content":"Are you okay?"}
		],
		"tools":[` + routerFallbackTool + `],
		"tool_choice":"auto"
	}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	s.openaiChat(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(chat.requests) != 3 {
		t.Fatalf("upstream requests=%d, want router, repair, and guarded answer", len(chat.requests))
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != unconfirmedToolOutcomeResponse {
		t.Fatalf("content=%q response=%#v", got, response)
	}
}
