package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"m365-native/internal/chathub"
)

func TestHandoffV15ToolExecutionIDsAreUniqueFromOperationIdentity(t *testing.T) {
	first := callID("lookup", `{"id":1}`, 0)
	second := callID("lookup", `{"id":1}`, 0)
	if first == second {
		t.Fatalf("execution IDs reused operation signature: %q", first)
	}
	if toolArgumentsSHA256(`{"id":1}`) != toolArgumentsSHA256(` { "id" : 1 } `) {
		t.Fatal("operation signature is not canonical independently of execution ID")
	}
}

func TestHandoffV15CallerToolSchemaUsesCompleteJSONSchemaAndExactNumbers(t *testing.T) {
	tool := chathub.Tool{Type: "function", Function: json.RawMessage(`{"name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"],"additionalProperties":false}}`)}
	tools := toolDefinitionMaps([]chathub.Tool{tool})
	valid := detectedToolCall{ID: "valid", Name: "lookup", Arguments: json.RawMessage(`{"id":9007199254740993}`)}
	invalid := detectedToolCall{ID: "invalid", Name: "lookup", Arguments: json.RawMessage(`{"id":9007199254740992}`)}
	allowed, rejected := filterAllowedToolCalls([]detectedToolCall{valid, invalid}, tools, "auto")
	if !rejected || len(allowed) != 1 || allowed[0].ID != "valid" || string(allowed[0].Arguments) != string(valid.Arguments) {
		t.Fatalf("schema validation lost exact-number/const semantics: allowed=%#v rejected=%t", allowed, rejected)
	}
}

func TestHandoffV15HistoricalCompletedToolDoesNotProveCurrentTurnExecution(t *testing.T) {
	messages := []oaiMsg{
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []map[string]any{{"id": "old", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"id":1}`}}}},
		{Role: "tool", ToolCallID: "old", Content: "old-result"},
		{Role: "assistant", Content: "old-answer"},
		{Role: "user", Content: "do it again"},
	}
	current := buildAgentLedger(activeMessages(messages))
	calls := []detectedToolCall{{ID: "new", Name: "lookup", Arguments: json.RawMessage(`{"id":1}`)}}
	if got := filterKnownCalls(calls, current); len(got) != 1 {
		t.Fatalf("historical evidence suppressed current-turn execution: %#v", got)
	}
	if completionEvidenceAllows("done", current) {
		t.Fatal("current turn without a tool result was treated as executed from historical evidence")
	}
}

func TestHandoffV15CheckpointKeepsSameTurnEvidenceButDropsCompletedEvidenceOnNewUserTurn(t *testing.T) {
	args := `{"id":1}`
	argsDigest := toolArgumentsSHA256(args)
	identityDigest := toolCallIdentityDigest("lookup", argsDigest)
	record := &transportCheckpointRecord{
		CompletedToolEvidence:        []toolEvidence{{ID: "old", Name: "lookup", ArgumentsSHA256: argsDigest, ResultLength: 2, ResultSHA256: stringSHA256("ok"), hasResult: true}},
		CompletedToolIdentityDigests: []string{identityDigest},
		PendingToolCalls:             []checkpointPendingToolCall{{CallID: "pending", Name: "write", ArgumentsDigest: toolArgumentsSHA256(`{"id":2}`)}},
	}

	stored := checkpointAgentLedger(record)
	sameTurn := checkpointExecutionLedger(stored, []oaiMsg{{Role: "tool", ToolCallID: "pending", Content: "ok"}})
	if !sameTurn.hasKnownCall("lookup", args) || len(sameTurn.Completed) != 1 {
		t.Fatalf("same-turn completed evidence was lost: %#v", sameTurn)
	}

	newTurn := checkpointExecutionLedger(stored, []oaiMsg{{Role: "user", Content: "do it again"}})
	if newTurn.hasKnownCall("lookup", args) || len(newTurn.Completed) != 0 {
		t.Fatalf("historical completed evidence leaked into new turn: %#v", newTurn)
	}
	if !newTurn.hasKnownCall("write", `{"id":2}`) {
		t.Fatalf("pending safety identity was not preserved: %#v", newTurn)
	}
	if len(newTurn.Pending) != 1 || newTurn.Pending[0].ID != "pending" {
		t.Fatalf("unresolved pending evidence was not preserved: %#v", newTurn)
	}
}

func TestHandoffV15ResponseFormatJSONSchemaIsDeterministicallyValidated(t *testing.T) {
	format := &responseFormat{Type: "json_schema", JSONSchema: map[string]any{
		"name":   "answer",
		"strict": true,
		"schema": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"answer": map[string]any{"type": "integer"}},
			"required":             []any{"answer"},
			"additionalProperties": false,
		},
	}}
	if got, err := validateResponseFormatText(`{"answer":2}`, format); err != nil || got != `{"answer":2}` {
		t.Fatalf("valid schema output rejected: got=%q err=%v", got, err)
	}
	if _, err := validateResponseFormatText(`{"answer":"two"}`, format); err == nil {
		t.Fatal("invalid schema output was accepted")
	}
	if _, err := validateResponseFormatText(`not json`, &responseFormat{Type: "json_object"}); err == nil {
		t.Fatal("json_object accepted non-JSON")
	}
}

func TestHandoffV15DisengagedAndAuthErrorDoNotCollapseIntoEmptyResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal chathub.TerminalState
		code     string
	}{
		{name: "disengaged", terminal: chathub.TerminalState{Kind: "disengaged", Error: "policy stop"}, code: "upstream_disengaged"},
		{name: "auth", terminal: chathub.TerminalState{Kind: "auth_error", Error: "provider auth failed"}, code: "upstream_auth_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Terminal: test.terminal}}
			server := newWP1CandidateServer(t, chat)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"hello"}]}`))
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, request)
			if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) || strings.Contains(recorder.Body.String(), `"code":"upstream_empty_response"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandoffV15ReconnectableCloseTerminalDoesNotCollapseIntoGenericUpstreamError(t *testing.T) {
	allowReconnect := true
	terminal := chathub.TerminalState{Kind: "close", Error: "server closing", AllowReconnect: &allowReconnect}
	chat := &wp1CandidateChat{
		result: chathub.Result{Terminal: terminal},
		err:    &chathub.TerminalError{State: terminal},
	}
	server := newWP1CandidateServer(t, chat)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"hello"}]}`))
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, request)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"code":"upstream_closed_reconnectable"`) || !strings.Contains(recorder.Body.String(), "server closing") {
		t.Fatalf("reconnectable close semantics collapsed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	streamRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-reasoning","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	streamRecorder := httptest.NewRecorder()
	server.openaiChat(streamRecorder, streamRequest)
	if !strings.Contains(streamRecorder.Body.String(), `"code":"upstream_closed_reconnectable"`) || !strings.Contains(streamRecorder.Body.String(), "server closing") || strings.Contains(streamRecorder.Body.String(), `"code":"upstream_error"`) {
		t.Fatalf("streaming reconnectable close semantics collapsed: body=%s", streamRecorder.Body.String())
	}

	hubTerminal := chathub.TerminalState{Kind: "error", Error: "HubException: provider rejected the turn"}
	hubChat := &wp1CandidateChat{result: chathub.Result{Terminal: hubTerminal}, err: &chathub.TerminalError{State: hubTerminal}}
	hubServer := newWP1CandidateServer(t, hubChat)
	hubRecorder := httptest.NewRecorder()
	hubServer.openaiChat(hubRecorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-reasoning","messages":[{"role":"user","content":"hello"}]}`)))
	if hubRecorder.Code != http.StatusBadGateway || !strings.Contains(hubRecorder.Body.String(), `"code":"upstream_terminal_error"`) || !strings.Contains(hubRecorder.Body.String(), "HubException") {
		t.Fatalf("type=3 terminal semantics collapsed: status=%d body=%s", hubRecorder.Code, hubRecorder.Body.String())
	}
}

func TestHandoffV15AnthropicStreamingAndMaxTokensDowngradesAreExplicit(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "ok", ConversationID: "conversation", SessionID: "session", RequestID: "request"}}
	server := newWP1CandidateServer(t, chat)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"gpt-5.6-reasoning",
		"max_tokens":64,
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	recorder := httptest.NewRecorder()
	server.anthropicMessages(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-M365-Streaming-Semantics"); got != "posthoc-adapter" {
		t.Fatalf("streaming downgrade was silent: %q", got)
	}
	if got := recorder.Header().Get("X-M365-Ignored-Parameters"); !strings.Contains(got, "max_tokens") {
		t.Fatalf("Anthropic max_tokens downgrade was silent: %q", got)
	}
}

func TestHandoffV15CodeInterpreterImageArtifactMaterializesThroughPrivateStore(t *testing.T) {
	const upstream = "https://us-prod.asyncgw.teams.microsoft.com/v1/objects/abc/views/original/handoff_ci_chart.png"
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		artifacts:      store,
		artifactOrigin: "https://sidecar.example.test",
		artifactFetch: &artifactFetchClient{
			HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != upstream || request.Header.Get("Authorization") != "Bearer ic3-token" {
					t.Fatalf("unexpected CI image request: %s auth=%t", request.URL.Host, request.Header.Get("Authorization") != "")
				}
				return artifactResponse(http.StatusOK, "png-bytes"), nil
			})},
			Token: func(context.Context, string) (string, error) { return "ic3-token", nil },
		},
	}
	result := chathub.Result{Events: []json.RawMessage{json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"GeneratedCode","contentOrigin":"CodeInterpreter","text":"{\"status\":\"Success\",\"outputFiles\":[{\"reference_id\":\"turn1file3\",\"fileName\":\"handoff_ci_chart.png\",\"fileStoreType\":\"AMS\",\"size\":13458,\"codeResultImageUrl\":\"https://us-prod.asyncgw.teams.microsoft.com/v1/objects/abc/views/original/handoff_ci_chart.png\"}]}"}]}]}`)}}
	request := httptest.NewRequest(http.MethodPost, "https://sidecar.example.test/v1/chat/completions", nil)
	if _, err := server.materializeArtifacts(context.Background(), request, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "image" || !strings.HasPrefix(result.Artifacts[0].PublicURL, "https://sidecar.example.test/v1/artifacts/") {
		t.Fatalf("materialized CI image artifact=%#v", result.Artifacts)
	}
	if !strings.Contains(result.Text, "[下載 handoff_ci_chart.png](https://sidecar.example.test/v1/artifacts/") || strings.Contains(result.Text, upstream) {
		t.Fatalf("caller text=%q", result.Text)
	}
}

func TestHandoffV15IgnoredCompatibilityParametersAreNotSilent(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "ok", ConversationID: "conversation", SessionID: "session", RequestID: "request"}}
	server := newWP1CandidateServer(t, chat)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"hello"}],
		"temperature":0.2,
		"top_p":0.9,
		"max_tokens":64,
		"seed":42
	}`))
	recorder := httptest.NewRecorder()
	server.openaiChat(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	header := recorder.Header().Get("X-M365-Ignored-Parameters")
	for _, name := range []string{"max_tokens", "seed", "temperature", "top_p"} {
		if !strings.Contains(header, name) {
			t.Fatalf("ignored parameter %q was silent: header=%q", name, header)
		}
	}
}
