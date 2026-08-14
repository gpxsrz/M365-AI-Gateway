package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func trafficTestSettings() runtimeSettings {
	v := defaultRuntimeSettings()
	v.InteractiveMaxConcurrent = 1
	v.InteractiveQueueTimeoutSeconds = 1
	v.MemoryMaxConcurrent = 1
	v.MemoryQueueTimeoutSeconds = 1
	v.InteractivePriorityHoldoffSeconds = 0
	v.MemoryBackoffInitialSeconds = 1
	v.MemoryBackoffMaxSeconds = 2
	return v
}

func acquireInteractiveForTest(t *testing.T, controller *compatibilityTrafficController, cfg runtimeSettings) func(time.Duration) {
	t.Helper()
	release, err := controller.acquireInteractive(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestCompatibilityTrafficSnapshotOmitsInactiveDeadlines(t *testing.T) {
	raw, err := json.Marshal(newCompatibilityTrafficController().snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sharedCooldownUntil", "interactiveHoldoffUntil"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("inactive deadline %q leaked into snapshot: %s", field, raw)
		}
	}
}

func TestCompatibilityTrafficInteractiveConcurrencyIsBounded(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	firstRelease, err := c.acquireInteractive(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	type admitted struct {
		release func(time.Duration)
		err     error
	}
	secondResult := make(chan admitted, 1)
	go func() {
		release, err := c.acquireInteractive(context.Background(), cfg)
		secondResult <- admitted{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.InteractiveInFlight == 1 && snapshot.InteractiveWaiting == 1
	})
	select {
	case result := <-secondResult:
		t.Fatalf("second interactive request bypassed capacity: %#v", result)
	case <-time.After(60 * time.Millisecond):
	}
	firstRelease(0)
	second := <-secondResult
	if second.err != nil {
		t.Fatal(second.err)
	}
	second.release(0)
	if snapshot := c.snapshot(); snapshot.InteractiveInFlight != 0 || snapshot.InteractiveWaiting != 0 {
		t.Fatalf("interactive capacity was not released: %#v", snapshot)
	}
}

func TestInteractiveAdmissionIsSharedAcrossAPIKeyOwnersForSingleAccount(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.InteractiveMaxConcurrent = 1
	settings.InteractiveQueueTimeoutSeconds = 1
	settings.InteractivePriorityHoldoffSeconds = 0
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	server.compatTraffic = newCompatibilityTrafficController()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan int, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), "owner-a")
		server.serveInteractiveOpenAI(recorder, request, func(w http.ResponseWriter, _ *http.Request) {
			close(firstStarted)
			<-releaseFirst
			w.WriteHeader(http.StatusNoContent)
		})
		firstDone <- recorder.Code
	}()
	<-firstStarted

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	secondRan := false
	second := httptest.NewRecorder()
	request := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx), "owner-b")
	server.serveInteractiveOpenAI(second, request, func(http.ResponseWriter, *http.Request) {
		secondRan = true
	})
	if secondRan || second.Code != http.StatusServiceUnavailable || second.Header().Get("Retry-After") == "" {
		t.Fatalf("different API-key owner bypassed single-account capacity: ran=%t status=%d retry-after=%q body=%s", secondRan, second.Code, second.Header().Get("Retry-After"), second.Body.String())
	}

	close(releaseFirst)
	if status := <-firstDone; status != http.StatusNoContent {
		t.Fatalf("first interactive request status=%d", status)
	}
}

func TestCompatibilityTrafficInteractiveQueueIsBoundedAndCancellable(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.InteractiveQueueTimeoutSeconds = 10
	c.mu.Lock()
	for i := 0; i < interactiveQueueMaxWaiting; i++ {
		c.interactiveQueue = append(c.interactiveQueue, uint64(i))
	}
	c.interactiveWaiting = len(c.interactiveQueue)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.acquireInteractive(ctx, cfg)
	var admission *interactiveAdmissionError
	if !errors.As(err, &admission) || time.Since(start) > 20*time.Millisecond || admission.retryAfter < 1 {
		t.Fatalf("full interactive queue did not fail fast: err=%v elapsed=%v", err, time.Since(start))
	}
	if snapshot := c.snapshot(); snapshot.InteractiveWaiting != interactiveQueueMaxWaiting {
		t.Fatalf("interactive queue changed after rejection: %#v", snapshot)
	}

	c.mu.Lock()
	c.interactiveQueue = nil
	c.interactiveWaiting = 0
	c.interactiveInFlight = cfg.InteractiveMaxConcurrent
	c.mu.Unlock()
	cancelled, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	if _, err := c.acquireInteractive(cancelled, cfg); err == nil {
		t.Fatal("cancelled interactive waiter unexpectedly admitted")
	}
	if snapshot := c.snapshot(); snapshot.InteractiveWaiting != 0 {
		t.Fatalf("cancelled interactive waiter remained queued: %#v", snapshot)
	}
}

func TestCompatibilityTrafficInteractive429BlocksNewInteractiveAdmission(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	c.observeInteractiveStatus(http.StatusTooManyRequests, cfg, "1")
	snapshot := c.snapshot()
	if snapshot.Shared429Count != 1 || snapshot.Last429Source != "interactive" || !snapshot.SharedCooldownUntil.After(time.Now()) {
		t.Fatalf("interactive 429 did not create shared cooldown: %#v", snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := c.acquireInteractive(ctx, cfg); err == nil {
		t.Fatal("interactive request bypassed shared 429 cooldown")
	}
}

func TestCompatibilityTrafficQueuedInteractiveStaysAheadOfMemory(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	firstRelease, err := c.acquireInteractive(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan func(time.Duration), 1)
	go func() {
		release, err := c.acquireInteractive(context.Background(), cfg)
		if err != nil {
			secondResult <- nil
			return
		}
		secondResult <- release
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.InteractiveWaiting == 1
	})
	memoryResult := make(chan func(int), 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		if err != nil {
			memoryResult <- nil
			return
		}
		memoryResult <- release
	}()
	waitForCompatibilityTraffic(t, c, func(snapshot compatibilityTrafficSnapshot) bool {
		return snapshot.MemoryWaiting == 1
	})
	firstRelease(0)
	secondRelease := <-secondResult
	if secondRelease == nil {
		t.Fatal("queued interactive request failed admission")
	}
	select {
	case release := <-memoryResult:
		if release != nil {
			release(http.StatusOK)
		}
		t.Fatal("Memory request bypassed queued interactive traffic")
	case <-time.After(60 * time.Millisecond):
	}
	secondRelease(0)
	memoryRelease := <-memoryResult
	if memoryRelease == nil {
		t.Fatal("Memory request failed after interactive queue drained")
	}
	memoryRelease(http.StatusOK)
}

func TestInteractiveProtocolHandlersReturnRetryableAdmissionErrors(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		body      string
		invoke    func(*Server, http.ResponseWriter, *http.Request)
		anthropic bool
	}{
		{
			name:   "openai",
			target: "/v1/chat/completions",
			body:   `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`,
			invoke: func(server *Server, w http.ResponseWriter, r *http.Request) { server.interactiveOpenAIChat(w, r) },
		},
		{
			name:   "responses",
			target: "/v1/responses",
			body:   `{"model":"gpt-5.6-sol","input":"hello"}`,
			invoke: func(server *Server, w http.ResponseWriter, r *http.Request) { server.responses(w, r) },
		},
		{
			name:      "anthropic",
			target:    "/v1/messages",
			body:      `{"model":"gpt-5.6-sol","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
			invoke:    func(server *Server, w http.ResponseWriter, r *http.Request) { server.anthropicMessages(w, r) },
			anthropic: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newAdminSecurityServer(t, "administrator-password")
			settings := server.settings.get()
			settings.InteractiveMaxConcurrent = 1
			settings.InteractiveQueueTimeoutSeconds = 1
			if err := server.settings.save(settings); err != nil {
				t.Fatal(err)
			}
			server.compatTraffic = newCompatibilityTrafficController()
			server.compatTraffic.interactiveInFlight = 1
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body)).WithContext(ctx)
			recorder := httptest.NewRecorder()
			test.invoke(server, recorder, request)
			if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" {
				t.Fatalf("admission response status=%d retry-after=%q body=%s", recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
			}
			var envelope map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			errorObject, _ := envelope["error"].(map[string]any)
			if errorObject["type"] != "rate_limit_error" {
				t.Fatalf("unexpected admission error envelope: %#v", envelope)
			}
			if test.anthropic && envelope["type"] != "error" {
				t.Fatalf("Anthropic admission response lost top-level error type: %#v", envelope)
			}
		})
	}
}

func TestCompatibilityTrafficMemoryConcurrency(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	release, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := c.acquireMemory(ctx, cfg); err == nil {
		t.Fatal("second memory request unexpectedly bypassed concurrency limit")
	}
	release(http.StatusOK)
	if snap := c.snapshot(); snap.MemoryInFlight != 0 {
		t.Fatalf("memory in flight=%d", snap.MemoryInFlight)
	}
}

func TestLegacyV1ChatCountsAsInteractivePriority(t *testing.T) {
	blocking := &phase3BlockingChat{started: make(chan struct{}), release: make(chan struct{})}
	server := newAdminSecurityServer(t, "administrator-password")
	settings := server.settings.get()
	settings.HermesCompatibilityEnabled = false
	if err := server.settings.save(settings); err != nil {
		t.Fatal(err)
	}
	if _, err := server.tokens.Upsert(testTokenSet("legacy-v1-priority")); err != nil {
		t.Fatal(err)
	}
	server.chat = blocking
	server.compatTraffic = newCompatibilityTrafficController()
	chatDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.interactiveOpenAIChat(recorder, phase3Request(http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"interactive"}]}`))
		chatDone <- recorder
	}()
	<-blocking.started
	if snap := server.compatTraffic.snapshot(); snap.InteractiveInFlight != 1 {
		t.Fatalf("interactive in flight=%d", snap.InteractiveInFlight)
	}
	cfg := trafficTestSettings()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := server.compatTraffic.acquireMemory(ctx, cfg); err == nil {
		t.Fatal("memory request unexpectedly started while legacy /v1 chat was active")
	}
	close(blocking.release)
	if recorder := <-chatDone; recorder.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCompatibilityTrafficSnapshotUsesInteractiveMetricNames(t *testing.T) {
	controller := newCompatibilityTrafficController()
	release := acquireInteractiveForTest(t, controller, trafficTestSettings())
	defer release(0)
	raw, err := json.Marshal(controller.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"interactiveInFlight":1`) || strings.Contains(string(raw), "hermesInFlight") {
		t.Fatalf("traffic snapshot kept Hermes-specific generic metric: %s", raw)
	}
}

func TestCompatibilityTrafficInteractivePriority(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	endInteractive := acquireInteractiveForTest(t, c, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := c.acquireMemory(ctx, cfg); err == nil {
		t.Fatal("memory request unexpectedly started while Hermes was active")
	}
	endInteractive(0)
	release, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	release(http.StatusOK)
}

func TestCompatibilityTraffic429Backoff(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	release, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	release(http.StatusTooManyRequests)
	snap := c.snapshot()
	if snap.Memory429Count != 1 || !snap.SharedCooldownUntil.After(time.Now()) {
		t.Fatalf("unexpected 429 snapshot: %#v", snap)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := c.acquireMemory(ctx, cfg); err == nil {
		t.Fatal("memory request unexpectedly bypassed 429 cooldown")
	}
}

func TestCompatibilityTrafficMemoryQueueIsFIFO(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	firstRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	type admitted struct {
		id      int
		release func(int)
		err     error
	}
	results := make(chan admitted, 2)
	for _, id := range []int{1, 2} {
		id := id
		go func() {
			release, err := c.acquireMemory(context.Background(), cfg)
			results <- admitted{id: id, release: release, err: err}
		}()
		time.Sleep(20 * time.Millisecond)
	}
	firstRelease(http.StatusOK)
	first := <-results
	if first.err != nil || first.id != 1 {
		t.Fatalf("first admitted=%#v", first)
	}
	first.release(http.StatusOK)
	second := <-results
	if second.err != nil || second.id != 2 {
		t.Fatalf("second admitted=%#v", second)
	}
	second.release(http.StatusOK)
}

func TestCompatibilityTrafficMemoryQueueIsBounded(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.MemoryQueueTimeoutSeconds = 10
	endInteractive := acquireInteractiveForTest(t, c, cfg)
	defer endInteractive(0)
	for i := 0; i < memoryQueueMaxWaiting; i++ {
		c.memoryQueue = append(c.memoryQueue, uint64(i))
	}
	c.memoryWaiting = len(c.memoryQueue)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.acquireMemory(ctx, cfg); err == nil || time.Since(start) > 20*time.Millisecond {
		t.Fatalf("full queue did not fail fast: err=%v elapsed=%v", err, time.Since(start))
	}
	if snap := c.snapshot(); snap.MemoryWaiting != memoryQueueMaxWaiting {
		t.Fatalf("queue size changed after rejection: %#v", snap)
	}
}

func TestCompatibilityTrafficAdmissionRetryAfterIsNotFixedOneSecond(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.MemoryQueueTimeoutSeconds = 10
	endInteractive := acquireInteractiveForTest(t, c, cfg)
	defer endInteractive(0)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := c.acquireMemory(ctx, cfg)
	var admission *memoryAdmissionError
	if !errors.As(err, &admission) || admission.retryAfter <= 1 {
		t.Fatalf("admission error=%v retryAfter=%v", err, func() int {
			if admission == nil {
				return 0
			}
			return admission.retryAfter
		}())
	}
}

func waitForCompatibilityTraffic(t *testing.T, controller *compatibilityTrafficController, ready func(compatibilityTrafficSnapshot) bool) compatibilityTrafficSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot := controller.snapshot()
		if ready(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot := controller.snapshot()
	t.Fatalf("compatibility traffic condition was not reached: %#v", snapshot)
	return snapshot
}
