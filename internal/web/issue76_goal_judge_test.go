package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m365-native/internal/chathub"
)

const issue76DoneVerdict = `{"verdict":"done","reason":"all acceptance evidence is satisfied"}`

func TestIssue76V1ControlPlaneCheckpointIsForceNewUntracked(t *testing.T) {
	control, ok := compatibilityCheckpointControl("/v1/chat/completions")
	if !ok {
		t.Fatal("/v1/chat/completions is not recognized as a compatibility checkpoint surface")
	}
	if control.Namespace != "auxiliary-control-plane" || !control.ForceNew || !control.Untracked {
		t.Fatalf("control-plane checkpoint control = %#v", control)
	}
}

func TestIssue76V1DoneVerdictBypassesAgentCompletionEvidenceGuard(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: issue76DoneVerdict}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"system","content":"Reply ONLY with one JSON object."},{"role":"user","content":"Is the goal complete?"}]}`

	recorder := httptest.NewRecorder()
	request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), "issue76-control-plane")
	server.openaiChat(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != issue76DoneVerdict {
		t.Fatalf("control-plane verdict was rewritten: got=%q want=%q", got, issue76DoneVerdict)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("upstream requests=%d want=1", len(chat.requests))
	}
	if strings.Contains(chat.requests[0].Text, "EVIDENCE_LEDGER:") || strings.Contains(chat.requests[0].Text, "FINAL ANSWER RULE:") {
		t.Fatalf("control-plane prompt received Agent-only evidence policy: %s", chat.requests[0].Text)
	}
}

func TestIssue76V1StreamingDoneVerdictBypassesAgentCompletionEvidenceGuard(t *testing.T) {
	chat := &wp1CandidateChat{
		result: chathub.Result{Text: issue76DoneVerdict},
		events: []chathub.StreamEvent{{Kind: "text", Text: issue76DoneVerdict}},
	}
	server, rawKey := issue76RouteServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","stream":true,"messages":[{"role":"system","content":"Reply ONLY with one JSON object."},{"role":"user","content":"Is the goal complete?"}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+rawKey)
	server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := continuationStreamText(t, recorder.Body.String()); got != issue76DoneVerdict {
		t.Fatalf("streamed control-plane verdict was rewritten: got=%q want=%q stream=%s", got, issue76DoneVerdict, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), unconfirmedToolOutcomeResponse) {
		t.Fatalf("streamed control-plane verdict hit Agent completion guard: %s", recorder.Body.String())
	}
	if checkpoints := checkpointViewsForTest(t, server.checkpoints); len(checkpoints) != 0 {
		t.Fatalf("streaming /v1 control-plane request persisted transport checkpoint state: %#v", checkpoints)
	}
}

func TestIssue76V1ToolRouterDoesNotInjectAgentEvidence(t *testing.T) {
	chat := &continuationChat{results: []chathub.Result{
		{Text: `{"calls":[]}`},
		{Text: "control-plane answer"},
	}}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[
			{"role":"user","content":"Run a prior action."},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{\"command\":\"prior\"}"}}]},
			{"role":"user","content":"Now make an independent control-plane decision."}
		],
		"tools":[{"type":"function","function":{"name":"terminal","description":"Run a command.","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}}],
		"tool_choice":"auto"
	}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	server.openaiChat(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 2 {
		t.Fatalf("upstream requests=%d want router+answer", len(chat.requests))
	}
	for i, upstream := range chat.requests {
		if strings.Contains(upstream.Text, "EVIDENCE_LEDGER:") || strings.Contains(upstream.Text, "Completed calls are final evidence") {
			t.Fatalf("control-plane request %d leaked Agent evidence policy: %s", i, upstream.Text)
		}
	}
}

func TestIssue76HermesStillRejectsUnsupportedDoneClaim(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: issue76DoneVerdict}}
	server := newWP1CandidateServer(t, chat)
	body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"system","content":"You are Hermes."},{"role":"user","content":"Report whether deployment is done."}]}`

	recorder := httptest.NewRecorder()
	request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "issue76-hermes")
	server.openaiChat(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choices, _ := response["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if got := message["content"]; got != unconfirmedToolOutcomeResponse {
		t.Fatalf("Hermes completion guard regressed: got=%q", got)
	}
}

func TestIssue76V1RouteIsP2AutonomousControlPlane(t *testing.T) {
	blocking := &phase3BlockingChat{started: make(chan struct{}), release: make(chan struct{})}
	server, rawKey := issue76RouteServer(t, blocking)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := issue76RouteRequest(context.Background(), rawKey)
		server.Routes().ServeHTTP(recorder, request)
		done <- recorder
	}()
	<-blocking.started

	snapshot := server.compatTraffic.snapshot()
	if snapshot.ExternalUserInFlight != 0 || snapshot.AutonomousInFlight != 1 {
		t.Fatalf("/v1 control-plane traffic did not enter P2: %#v", snapshot)
	}

	close(blocking.release)
	if recorder := <-done; recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if checkpoints := checkpointViewsForTest(t, server.checkpoints); len(checkpoints) != 0 {
		t.Fatalf("/v1 control-plane request persisted transport checkpoint state: %#v", checkpoints)
	}
}

func TestIssue76V1ControlPlaneDoesNotPreemptMemoryYield(t *testing.T) {
	chat := &captureSingleAccountChat{}
	server, rawKey := issue76RouteServer(t, chat)
	server.compatTraffic.mu.Lock()
	server.compatTraffic.memoryYieldPending = true
	server.compatTraffic.memoryYieldActive = true
	server.compatTraffic.memoryYieldDeadline = time.Now().Add(time.Second)
	server.compatTraffic.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, issue76RouteRequest(ctx, rawKey))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("control-plane request bypassed MEMORY_YIELD: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.requests) != 0 {
		t.Fatalf("control-plane request reached upstream while MEMORY_YIELD was active: %d requests", len(chat.requests))
	}
	snapshot := server.compatTraffic.snapshot()
	if snapshot.LastMemoryYieldOutcome == "preempted_by_interactive" {
		t.Fatalf("P2 control-plane request preempted MEMORY_YIELD: %#v", snapshot)
	}
}

func issue76RouteServer(t *testing.T, chat chatService) (*Server, string) {
	t.Helper()
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.InteractiveMaxConcurrent = 2
	settings.InteractiveQueueTimeoutSeconds = 1
	settings.MemoryMaxConcurrent = 1
	settings.MemoryQueueTimeoutSeconds = 1
	settings.InteractivePriorityHoldoffSeconds = 0
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("issue76-route")); err != nil {
		t.Fatal(err)
	}
	_, rawKey, err := server.apiKeys.create("issue76-control-plane")
	if err != nil {
		t.Fatal(err)
	}
	server.chat = chat
	server.compatTraffic = newCompatibilityTrafficController()
	return server, rawKey
}

func issue76RouteRequest(ctx context.Context, rawKey string) *http.Request {
	body := `{"model":"gpt-5.6-reasoning","messages":[{"role":"system","content":"Reply ONLY with one JSON object."},{"role":"user","content":"Is the goal complete?"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+rawKey)
	return request
}
