package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m365-native/internal/chathub"
)

func TestIssue71HermesRequestClassificationUsesFrameworkMarkers(t *testing.T) {
	tests := []struct {
		name string
		text string
		want hermesRequestClass
	}{
		{name: "external", text: "請繼續處理 Issue #71", want: hermesRequestExternalUser},
		{name: "async batch", text: "[ASYNC DELEGATION BATCH COMPLETE — deleg_123]\nresults", want: hermesRequestAsyncCompletion},
		{name: "async single", text: "[ASYNC DELEGATION COMPLETE — deleg_123]\nresult", want: hermesRequestAsyncCompletion},
		{name: "goal continuation", text: "[Continuing toward your standing goal]\nGoal: finish", want: hermesRequestAutonomousContinuation},
		{name: "kanban continuation", text: "[Continuing toward this kanban task — judge says it is not done yet]\nReason: test", want: hermesRequestAutonomousContinuation},
		{name: "compression continuation", text: "Continue from the compressed conversation context above. This marker exists because no human user turn was available.", want: hermesRequestAutonomousContinuation},
		{name: "length continuation", text: "[System: Your previous response was truncated by the output length limit. Continue exactly where you left off.]", want: hermesRequestAutonomousContinuation},
		{name: "ack continuation", text: "[System: Continue now. Execute the required tool calls and only send your final answer after completing the task.]", want: hermesRequestAutonomousContinuation},
		{name: "verify continuation", text: "[System: You edited code in this turn, but the workspace does not have fresh passing verification evidence yet.\nChanged paths: x]", want: hermesRequestAutonomousContinuation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := oaiReq{Messages: []oaiMsg{{Role: "user", Content: tc.text}}}
			if got := classifyHermesRequest(body); got != tc.want {
				t.Fatalf("class=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestIssue71HermesRequestClassificationUsesLatestUserTurnOnly(t *testing.T) {
	body := oaiReq{Messages: []oaiMsg{
		{Role: "user", Content: "[ASYNC DELEGATION COMPLETE — stale]"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "real fresh user request"},
	}}
	if got := classifyHermesRequest(body); got != hermesRequestExternalUser {
		t.Fatalf("class=%q want=%q", got, hermesRequestExternalUser)
	}
}

func TestIssue71MilestoneYieldWaitsForDurableRetainEvent(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.InteractiveMaxConcurrent = 2
	cfg.InteractiveQueueTimeoutSeconds = 2
	cfg.MemoryQueueTimeoutSeconds = 2

	c.observeHermesCompletion(hermesRequestAsyncCompletion, http.StatusOK)
	if snap := c.snapshot(); !snap.MemoryYieldPending || snap.TrafficMode != "MEMORY_YIELD" {
		t.Fatalf("milestone did not arm yield: %#v", snap)
	}

	type interactiveResult struct {
		release func(time.Duration)
		err     error
	}
	autonomous := make(chan interactiveResult, 1)
	go func() {
		release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
		autonomous <- interactiveResult{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.InteractiveWaiting == 1 && snapshot.MemoryYieldPending
	})

	select {
	case got := <-autonomous:
		if got.release != nil {
			got.release(0)
		}
		t.Fatalf("autonomous continuation bypassed milestone barrier: err=%v", got.err)
	case <-time.After(60 * time.Millisecond):
	}

	memoryRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Memory did not receive milestone lease: %v", err)
	}
	if snap := c.snapshot(); !snap.MemoryYieldActive || snap.MemoryYieldPending {
		memoryRelease(http.StatusOK)
		t.Fatalf("Memory lease did not become active: %#v", snap)
	}
	memoryRelease(http.StatusOK)

	// Memory HTTP success is only upstream request success, not Hindsight
	// retain durability. The autonomous continuation must remain blocked.
	select {
	case got := <-autonomous:
		if got.release != nil {
			got.release(0)
		}
		t.Fatalf("Memory HTTP 200 incorrectly passed durability barrier: err=%v", got.err)
	case <-time.After(60 * time.Millisecond):
	}

	eventAt := time.Now().UTC()
	c.observeHindsightEvent("retain.completed", "retain-op-1", "completed", eventAt)
	got := <-autonomous
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(0)
	snap := c.snapshot()
	if snap.MemoryYieldPending || snap.MemoryYieldActive || snap.LastMemoryYieldOutcome != "retain_durable" {
		t.Fatalf("retain.completed did not release milestone barrier: %#v", snap)
	}
	if snap.LastSuccessfulRetain.IsZero() {
		t.Fatalf("last successful retain was not recorded: %#v", snap)
	}
}

type issue71StaticChat struct{ calls int }

func (c *issue71StaticChat) Chat(context.Context, chathub.Account, chathub.Request) (chathub.Result, error) {
	c.calls++
	return chathub.Result{Text: "ok", ConversationID: "issue71-conversation", SessionID: "issue71-session"}, nil
}
func (c *issue71StaticChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}
func (c *issue71StaticChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func TestIssue71HermesRouteArmsMilestoneOnlyAfterSuccessfulAsyncCompletion(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.HermesCompatibilityEnabled = true
	settings.InteractivePriorityHoldoffSeconds = 0
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("issue71-hermes-class")); err != nil {
		t.Fatal(err)
	}
	chat := &issue71StaticChat{}
	server.chat = chat
	server.compatTraffic = newCompatibilityTrafficController()
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"[ASYNC DELEGATION BATCH COMPLETE — deleg_123]\\nresults"}]}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)), "issue71-owner")
	rr := httptest.NewRecorder()
	server.hermesOpenAIChat(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 1 {
		t.Fatalf("upstream calls=%d want=1", chat.calls)
	}
	snap := server.compatTraffic.snapshot()
	if !snap.MemoryYieldPending || snap.TrafficMode != "MEMORY_YIELD" {
		t.Fatalf("successful async completion did not arm milestone: %#v", snap)
	}
}

func issue71WebhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestIssue71HindsightRetainWebhookRequiresHMACAndReleasesBarrier(t *testing.T) {
	const secret = "issue71-test-webhook-secret-with-enough-entropy"
	t.Setenv("M365_HINDSIGHT_WEBHOOK_SECRET", secret)
	server := newAdminSecurityServer(t, "administrator-password")
	server.compatTraffic = newCompatibilityTrafficController()
	server.compatTraffic.observeHermesCompletion(hermesRequestAsyncCompletion, http.StatusOK)
	eventAt := time.Now().UTC().Format(time.RFC3339Nano)
	payload := []byte(`{"event":"retain.completed","bank_id":"issue71-bank","operation_id":"op-durable-1","status":"completed","timestamp":"` + eventAt + `","data":{"document_id":"doc-1","tags":["session:issue71"],"memory_unit_count":3}}`)

	bad := httptest.NewRequest(http.MethodPost, "/internal/hindsight/webhook", strings.NewReader(string(payload)))
	bad.Header.Set("X-Hindsight-Event", "retain.completed")
	bad.Header.Set("X-Hindsight-Signature", "sha256=deadbeef")
	badRecorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(badRecorder, bad)
	if badRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status=%d body=%s", badRecorder.Code, badRecorder.Body.String())
	}
	if snap := server.compatTraffic.snapshot(); !snap.MemoryYieldPending {
		t.Fatalf("invalid webhook mutated milestone state: %#v", snap)
	}

	good := httptest.NewRequest(http.MethodPost, "/internal/hindsight/webhook", strings.NewReader(string(payload)))
	good.Header.Set("X-Hindsight-Event", "retain.completed")
	good.Header.Set("X-Hindsight-Signature", issue71WebhookSignature(secret, payload))
	goodRecorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(goodRecorder, good)
	if goodRecorder.Code != http.StatusNoContent {
		t.Fatalf("valid webhook status=%d body=%s", goodRecorder.Code, goodRecorder.Body.String())
	}
	snap := server.compatTraffic.snapshot()
	if snap.MemoryYieldPending || snap.LastMemoryYieldOutcome != "retain_durable" || snap.LastSuccessfulRetain.IsZero() {
		t.Fatalf("valid retain webhook did not release milestone: %#v", snap)
	}
}

func TestIssue71ExternalUserPreemptsPendingMilestoneYield(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.InteractiveMaxConcurrent = 2
	c.observeHermesCompletion(hermesRequestAsyncCompletion, http.StatusOK)
	release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	release(0)
	snap := c.snapshot()
	if snap.MemoryYieldPending || snap.MemoryYieldActive || snap.LastMemoryYieldOutcome != "preempted_by_interactive" {
		t.Fatalf("external user did not preempt pending yield: %#v", snap)
	}
}

func TestIssue71MilestoneYieldTimeoutReleasesAutonomousContinuation(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	c.observeHermesCompletion(hermesRequestAsyncCompletion, http.StatusOK)
	c.mu.Lock()
	c.memoryYieldDeadline = time.Now().Add(-time.Millisecond)
	c.mu.Unlock()
	release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatal(err)
	}
	release(0)
	if snap := c.snapshot(); snap.LastMemoryYieldOutcome != "timeout" || snap.MemoryYieldPending || snap.MemoryYieldActive {
		t.Fatalf("expired milestone yield did not fail open to autonomous work: %#v", snap)
	}
}

func TestIssue71MemoryIngressAllowsOnlyActiveOnePlusWaitingOne(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.MemoryQueueTimeoutSeconds = 2
	firstRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	type memoryResult struct {
		release func(int)
		err     error
	}
	second := make(chan memoryResult, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		second <- memoryResult{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.MemoryInFlight == 1 && snapshot.MemoryWaiting == 1
	})
	start := time.Now()
	_, err = c.acquireMemory(context.Background(), cfg)
	var admission *memoryAdmissionError
	if !errors.As(err, &admission) || admission.code != "memory_capacity_deferred" || time.Since(start) > 30*time.Millisecond {
		firstRelease(http.StatusOK)
		t.Fatalf("third Memory request did not fail fast as deferred capacity: err=%v elapsed=%v", err, time.Since(start))
	}
	firstRelease(http.StatusOK)
	got := <-second
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(http.StatusOK)
}

func TestIssue71ExternalUserBypassesQueuedAutonomousAndAutonomousConcurrencyIsOne(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.InteractiveMaxConcurrent = 2
	cfg.InteractiveQueueTimeoutSeconds = 2
	firstAutoRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatal(err)
	}
	type interactiveResult struct {
		release func(time.Duration)
		err     error
	}
	secondAuto := make(chan interactiveResult, 1)
	go func() {
		release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
		secondAuto <- interactiveResult{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.InteractiveInFlight == 1 && snapshot.InteractiveWaiting == 1
	})

	externalCtx, cancelExternal := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelExternal()
	externalRelease, err := c.acquireInteractiveClass(externalCtx, cfg, hermesRequestExternalUser)
	if err != nil {
		firstAutoRelease(0)
		t.Fatalf("external user was stuck behind autonomous waiter: %v", err)
	}
	externalRelease(0)
	select {
	case got := <-secondAuto:
		if got.release != nil {
			got.release(0)
		}
		firstAutoRelease(0)
		t.Fatalf("second autonomous request bypassed autonomous concurrency=1: err=%v", got.err)
	case <-time.After(60 * time.Millisecond):
	}
	firstAutoRelease(0)
	got := <-secondAuto
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(0)
}

func TestIssue71MemoryAdmissionDuringSharedThrottleIsFailFastAndStructured(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	chat := &issue71StaticChat{}
	server.chat = chat
	server.compatTraffic = newCompatibilityTrafficController()
	server.compatTraffic.observeInteractiveStatus(http.StatusTooManyRequests, "")
	start := time.Now()
	rr := httptest.NewRecorder()
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"retain"}]}`)), "issue71-memory-owner")
	server.memoryOpenAIChat(rr, req)
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("throttled Memory admission did not fail fast: %v", time.Since(start))
	}
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "upstream_throttle") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatalf("Retry-After missing: headers=%v", rr.Header())
	}
	if chat.calls != 0 {
		t.Fatalf("throttled Memory admission reached upstream %d time(s)", chat.calls)
	}
}

func TestIssue71TrafficSnapshotExposesAdaptivePressure(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.InteractiveMaxConcurrent = 2
	cfg.MemoryQueueTimeoutSeconds = 2
	autonomousRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatal(err)
	}
	type memoryResult struct {
		release func(int)
		err     error
	}
	memory := make(chan memoryResult, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		memory <- memoryResult{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.MemoryWaiting == 1
	})
	c.mu.Lock()
	for _, id := range c.memoryQueue {
		c.memoryWaiterAt[id] = time.Now().Add(-5 * time.Second)
	}
	c.mu.Unlock()
	snap := c.snapshotForSettings(cfg)
	if snap.TrafficMode != "HERMES_BUSY" || snap.EffectiveHermesConcurrency != 1 || snap.AutonomousInFlight != 1 {
		autonomousRelease(0)
		t.Fatalf("adaptive pressure projection=%#v", snap)
	}
	if snap.MemoryPendingCount != 1 || snap.OldestMemoryAgeSeconds < 4 {
		autonomousRelease(0)
		t.Fatalf("Memory pressure projection=%#v", snap)
	}
	autonomousRelease(0)
	got := <-memory
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(http.StatusOK)
}

func TestIssue71SoftThrottleObservabilityCountsSuppressedReask(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("issue71-soft-observe")); err != nil {
		t.Fatal(err)
	}
	server.chat = &memorySoftThrottleRateLimitChat{}
	server.compatTraffic = newCompatibilityTrafficController()
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"retain"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}}}}`
	rr := httptest.NewRecorder()
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "issue71-owner")
	server.memoryOpenAIChat(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	snap := server.compatTraffic.snapshotForSettings(settings)
	if snap.LastSoftThrottle.IsZero() || !snap.LastHard429.IsZero() || snap.ReaskSuppressedCount != 1 {
		t.Fatalf("soft-throttle observability=%#v", snap)
	}
	if snap.ThrottleStreak != 1 || snap.SharedCooldownRemaining <= 0 || snap.TrafficMode != "UPSTREAM_COOLDOWN" {
		t.Fatalf("breaker observability=%#v", snap)
	}
}

func TestIssue71ConsolidationCompletionDoesNotPassMilestoneBarrier(t *testing.T) {
	c := newCompatibilityTrafficController()
	c.observeHermesCompletion(hermesRequestAsyncCompletion, http.StatusOK)
	eventAt := time.Now().UTC()
	c.observeHindsightEvent("consolidation.completed", "consolidation-op-1", "completed", eventAt)
	snap := c.snapshot()
	if !snap.MemoryYieldPending || snap.MemoryYieldActive || snap.LastSuccessfulConsolidation.IsZero() {
		t.Fatalf("consolidation incorrectly changed milestone barrier: %#v", snap)
	}
	c.observeHindsightEvent("retain.completed", "retain-op-after-consolidation", "completed", eventAt.Add(time.Millisecond))
	if snap := c.snapshot(); snap.MemoryYieldPending || snap.LastMemoryYieldOutcome != "retain_durable" {
		t.Fatalf("retain did not pass barrier after consolidation-only event: %#v", snap)
	}
}

func TestIssue71HundredMemoryBacklogRequestsDuringThrottleProduceZeroUpstreamRounds(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.MemoryCompatibilityEnabled = true
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	chat := &issue71StaticChat{}
	server.chat = chat
	server.compatTraffic = newCompatibilityTrafficController()
	server.compatTraffic.observeInteractiveStatus(http.StatusTooManyRequests, "")
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"backlog"}]}`
	for i := 0; i < 100; i++ {
		rr := httptest.NewRecorder()
		req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "issue71-memory-owner")
		server.memoryOpenAIChat(rr, req)
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "upstream_throttle") {
			t.Fatalf("attempt=%d status=%d body=%s", i+1, rr.Code, rr.Body.String())
		}
	}
	if chat.calls != 0 {
		t.Fatalf("100 throttled backlog requests produced %d upstream calls", chat.calls)
	}
}

type issue71SequenceChat struct {
	calls   int
	results []chathub.Result
	errors  []error
}

func (c *issue71SequenceChat) Chat(context.Context, chathub.Account, chathub.Request) (chathub.Result, error) {
	idx := c.calls
	c.calls++
	var result chathub.Result
	if idx < len(c.results) {
		result = c.results[idx]
	}
	if idx < len(c.errors) {
		return result, c.errors[idx]
	}
	return result, nil
}
func (c *issue71SequenceChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}
func (c *issue71SequenceChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func TestIssue71RouteFinalSoftThrottleDoesNotAmplifyIntoRepairOrReask(t *testing.T) {
	chat := &issue71SequenceChat{
		results: []chathub.Result{{Text: `{"calls":[],"answer":""}`}, {}},
		errors:  []error{nil, &chathub.RateLimitError{StatusCode: http.StatusTooManyRequests, SoftThrottle: true, Err: errors.New("synthetic final soft throttle")}},
	}
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	server.chat = chat
	settings := server.settings.get()
	settings.HermesCompatibilityEnabled = true
	settings.InteractivePriorityHoldoffSeconds = 0
	server.settings.v = settings
	body := `{
		"model":"gpt-5.6-reasoning",
		"messages":[{"role":"user","content":"Summarize the result."}],
		"tools":[{
			"type":"function",
			"function":{"name":"terminal","description":"Run a command.","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}
		}],
		"tool_choice":"auto"
	}`
	rr := httptest.NewRecorder()
	server.hermesOpenAIChat(rr, httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", strings.NewReader(body)))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if chat.calls != 2 {
		t.Fatalf("route->final soft throttle upstream calls=%d want exactly 2 with zero amplification", chat.calls)
	}
}

func TestIssue71HalfOpenProbeIsReservedForExternalUserTraffic(t *testing.T) {
	c := newCompatibilityTrafficController()
	c.mu.Lock()
	c.sharedCircuitState = sharedCircuitHalfOpenReady
	c.sharedCooldownUntil = time.Now().Add(-time.Second)
	c.mu.Unlock()

	cfg := trafficTestSettings()
	autonomousCtx, autonomousCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer autonomousCancel()
	autonomousResult := make(chan error, 1)
	go func() {
		release, err := c.acquireInteractiveClass(autonomousCtx, cfg, hermesRequestAutonomousContinuation)
		if release != nil {
			release(0)
		}
		autonomousResult <- err
	}()

	time.Sleep(35 * time.Millisecond)
	if snap := c.snapshot(); snap.SharedCircuitState != string(sharedCircuitHalfOpenReady) || snap.InteractiveInFlight != 0 {
		t.Fatalf("autonomous traffic consumed half-open probe: %#v", snap)
	}

	externalCtx, externalCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer externalCancel()
	release, err := c.acquireInteractiveClass(externalCtx, cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatalf("external user did not obtain controlled half-open probe: %v", err)
	}
	if snap := c.snapshot(); snap.SharedCircuitState != string(sharedCircuitProbeInFlight) || snap.InteractiveInFlight != 1 {
		t.Fatalf("external user did not become the controlled probe: %#v", snap)
	}
	release(0)

	if err := <-autonomousResult; err == nil {
		t.Fatal("autonomous continuation unexpectedly acquired the half-open probe")
	}
}

func TestIssue71AdminIndexAugmentationExposesTrafficAndLegacySettingStatus(t *testing.T) {
	raw := []byte(`<html><body><main>base</main></body></html>`)
	augmented := augmentIssue71AdminIndex(raw)
	for _, want := range []string{
		issue71UIAugmentationMarker,
		"trafficMode",
		"effectiveHermesConcurrency",
		"memoryPendingCount",
		"oldestMemoryAgeSeconds",
		"lastSuccessfulRetain",
		"lastSuccessfulConsolidation",
		"lastHard429",
		"lastSoftThrottle",
		"sharedCooldownRemainingSeconds",
		"reaskSuppressedCount",
		"Memory 429 舊版初始退避（相容欄位）",
		"Legacy Memory 429 Initial Backoff (compatibility field)",
		"/api/admin/traffic/recovery",
	} {
		if !strings.Contains(string(augmented), want) {
			t.Fatalf("admin augmentation missing %q", want)
		}
	}
	if got := strings.Count(string(augmentIssue71AdminIndex(augmented)), issue71UIAugmentationMarker); got != 1 {
		t.Fatalf("admin augmentation was not idempotent: marker count=%d", got)
	}
}

func TestIssue71HindsightWebhookOperationIDDeduplicatesAtLeastOnceDelivery(t *testing.T) {
	c := newCompatibilityTrafficController()
	firstAt := time.Now().UTC().Add(-time.Second)
	c.observeHindsightEvent("retain.completed", "duplicate-op", "completed", firstAt)
	c.observeHindsightEvent("retain.completed", "duplicate-op", "completed", firstAt.Add(time.Hour))
	if got := c.snapshot().LastSuccessfulRetain; !got.Equal(firstAt) {
		t.Fatalf("duplicate webhook changed retained timestamp: got=%v want=%v", got, firstAt)
	}
}

func TestIssue71RecoveryRequiresExplicitOperatorCompletion(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.compatTraffic = newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	server.compatTraffic.observeInteractiveStatus(http.StatusTooManyRequests, "")
	server.compatTraffic.mu.Lock()
	server.compatTraffic.sharedCooldownUntil = time.Now().Add(-time.Second)
	server.compatTraffic.mu.Unlock()
	probeRelease, err := server.compatTraffic.acquireInteractive(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	server.compatTraffic.observeInteractiveStatus(http.StatusOK, "")
	probeRelease(0)
	if snap := server.compatTraffic.snapshot(); snap.SharedCircuitState != "RECOVERY" {
		t.Fatalf("probe success state=%q want=RECOVERY", snap.SharedCircuitState)
	}

	rr := httptest.NewRecorder()
	server.adminTrafficRecovery(rr, httptest.NewRequest(http.MethodPost, "/api/admin/traffic/recovery", strings.NewReader(`{"action":"complete"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("complete recovery status=%d body=%s", rr.Code, rr.Body.String())
	}
	snap := server.compatTraffic.snapshotForSettings(server.settings.get())
	if snap.SharedCircuitState != "CLOSED" || snap.SharedCooldownLevel != 0 || snap.TrafficMode == "RECOVERY" {
		t.Fatalf("explicit recovery did not close breaker: %#v", snap)
	}

	second := httptest.NewRecorder()
	server.adminTrafficRecovery(second, httptest.NewRequest(http.MethodPost, "/api/admin/traffic/recovery", strings.NewReader(`{"action":"complete"}`)))
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "recovery_not_ready") {
		t.Fatalf("non-recovery completion status=%d body=%s", second.Code, second.Body.String())
	}
}
