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
	v.MemoryMaxConcurrent = 1
	v.MemoryQueueTimeoutSeconds = 1
	v.InteractivePriorityHoldoffSeconds = 0
	v.MemoryBackoffInitialSeconds = 1
	v.MemoryBackoffMaxSeconds = 2
	return v
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
	release := controller.beginInteractive()
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
	endInteractive := c.beginInteractive()
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
	if snap.Memory429Count != 1 || !snap.MemoryCooldownUntil.After(time.Now()) {
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
	endInteractive := c.beginInteractive()
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
	endInteractive := c.beginInteractive()
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
