package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func trafficTestSettings() runtimeSettings {
	v := defaultRuntimeSettings()
	v.MemoryMaxConcurrent = 1
	v.MemoryQueueTimeoutSeconds = 1
	v.HermesPriorityHoldoffSeconds = 0
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
	if snap := server.compatTraffic.snapshot(); snap.HermesInFlight != 1 {
		t.Fatalf("interactive in flight=%d", snap.HermesInFlight)
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

func TestCompatibilityTrafficHermesPriority(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	endHermes := c.beginHermes()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := c.acquireMemory(ctx, cfg); err == nil {
		t.Fatal("memory request unexpectedly started while Hermes was active")
	}
	endHermes(0)
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

func TestCompatibilityTrafficAdmissionRetryAfterIsNotFixedOneSecond(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := trafficTestSettings()
	cfg.MemoryQueueTimeoutSeconds = 10
	endHermes := c.beginHermes()
	defer endHermes(0)
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
