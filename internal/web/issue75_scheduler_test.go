package web

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type issue75MemoryAdmission struct {
	release func(int)
	err     error
}

type issue75InteractiveAdmission struct {
	release func(time.Duration)
	err     error
}

func issue75Settings() runtimeSettings {
	cfg := trafficTestSettings()
	cfg.InteractiveMaxConcurrent = 2
	cfg.MemoryMaxConcurrent = 1
	cfg.InteractiveQueueTimeoutSeconds = 2
	cfg.MemoryQueueTimeoutSeconds = 2
	return cfg
}

func TestIssue75MemoryCanRunAlongsideAutonomousWithinSharedCapacity(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	autonomousRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatal(err)
	}
	defer autonomousRelease(0)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	memoryRelease, err := c.acquireMemory(ctx, cfg)
	if err != nil {
		t.Fatalf("Memory did not share the second account slot with autonomous traffic: %v", err)
	}
	defer memoryRelease(http.StatusOK)

	snap := c.snapshot()
	if snap.AutonomousInFlight != 1 || snap.MemoryInFlight != 1 || snap.InteractiveInFlight+snap.MemoryInFlight != 2 {
		t.Fatalf("unexpected shared occupancy: %#v", snap)
	}
}

func TestIssue75MemoryCountsAgainstSharedAccountCapacity(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	memoryRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memoryRelease(http.StatusOK)
	externalRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	defer externalRelease(0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := c.acquireInteractiveClass(ctx, cfg, hermesRequestExternalUser); err == nil {
		release(0)
		t.Fatal("third shared-account request bypassed total concurrency=2")
	}
}

func TestIssue75EligibleMemoryTakesFreedSlotBeforeQueuedAutonomous(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	autonomousRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatal(err)
	}
	externalRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		autonomousRelease(0)
		t.Fatal(err)
	}

	memoryResult := make(chan issue75MemoryAdmission, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		memoryResult <- issue75MemoryAdmission{release: release, err: err}
	}()
	secondAutonomous := make(chan issue75InteractiveAdmission, 1)
	go func() {
		release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAutonomousContinuation)
		secondAutonomous <- issue75InteractiveAdmission{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(s compatibilityTrafficSnapshot) bool {
		return s.MemoryWaiting == 1 && s.AutonomousWaiting == 1
	})

	externalRelease(0)
	select {
	case got := <-memoryResult:
		if got.err != nil {
			autonomousRelease(0)
			t.Fatalf("eligible P1 Memory failed admission after a slot opened: %v", got.err)
		}
		got.release(http.StatusOK)
	case <-time.After(250 * time.Millisecond):
		autonomousRelease(0)
		t.Fatal("eligible P1 Memory did not take the freed slot")
	}

	select {
	case got := <-secondAutonomous:
		if got.release != nil {
			got.release(0)
		}
		autonomousRelease(0)
		t.Fatalf("P2 autonomous bypassed the running-autonomous limit: %v", got.err)
	case <-time.After(60 * time.Millisecond):
	}

	autonomousRelease(0)
	got := <-secondAutonomous
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(0)
}

func TestIssue75ExternalUserTakesFreedSlotBeforeQueuedMemory(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	firstRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		firstRelease(0)
		t.Fatal(err)
	}

	memoryResult := make(chan issue75MemoryAdmission, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		memoryResult <- issue75MemoryAdmission{release: release, err: err}
	}()
	thirdExternal := make(chan issue75InteractiveAdmission, 1)
	go func() {
		release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
		thirdExternal <- issue75InteractiveAdmission{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(s compatibilityTrafficSnapshot) bool {
		return s.MemoryWaiting == 1 && s.InteractiveWaiting == 1
	})

	firstRelease(0)
	var thirdExternalRelease func(time.Duration)
	select {
	case got := <-thirdExternal:
		if got.err != nil {
			secondRelease(0)
			t.Fatalf("P0 external user failed admission: %v", got.err)
		}
		thirdExternalRelease = got.release
	case <-time.After(250 * time.Millisecond):
		secondRelease(0)
		t.Fatal("P0 external user did not take the freed slot")
	}

	select {
	case got := <-memoryResult:
		if got.release != nil {
			got.release(http.StatusOK)
		}
		thirdExternalRelease(0)
		secondRelease(0)
		t.Fatal("Memory bypassed a queued P0 external user")
	case <-time.After(60 * time.Millisecond):
	}
	thirdExternalRelease(0)
	secondRelease(0)
	got := <-memoryResult
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(http.StatusOK)
}

func TestIssue75QueuedMemoryDoesNotWasteFreeSlotWhenMemoryAlreadyInFlight(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	firstMemoryRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer firstMemoryRelease(http.StatusOK)

	secondMemory := make(chan issue75MemoryAdmission, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		secondMemory <- issue75MemoryAdmission{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(s compatibilityTrafficSnapshot) bool {
		return s.MemoryInFlight == 1 && s.MemoryWaiting == 1
	})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	autonomousRelease, err := c.acquireInteractiveClass(ctx, cfg, hermesRequestAutonomousContinuation)
	if err != nil {
		t.Fatalf("P2 autonomous should use the free shared slot while P1 is class-capped: %v", err)
	}
	autonomousRelease(0)
}

func TestIssue75InteractiveHoldoffDoesNotDelayMemory(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	externalRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	externalRelease(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	memoryRelease, err := c.acquireMemory(ctx, cfg)
	if err != nil {
		t.Fatalf("legacy interactive holdoff still delayed P1 Memory: %v", err)
	}
	memoryRelease(http.StatusOK)
}

func TestIssue75MemoryWaitingBufferIsEight(t *testing.T) {
	if memoryQueueMaxWaiting != 8 {
		t.Fatalf("memory waiting buffer=%d want=8", memoryQueueMaxWaiting)
	}
}

func TestIssue75MemoryConcurrencyIsHardCappedAtOne(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()
	cfg.MemoryMaxConcurrent = 9

	firstRelease, err := c.acquireMemory(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer firstRelease(http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := c.acquireMemory(ctx, cfg); err == nil {
		release(http.StatusOK)
		t.Fatal("Memory runtime setting bypassed hard concurrency=1")
	}
}

func TestIssue75SharedAccountConcurrencyIsHardCappedAtTwo(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()
	cfg.InteractiveMaxConcurrent = 9

	firstRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	defer firstRelease(0)
	secondRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	defer secondRelease(0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if release, err := c.acquireInteractiveClass(ctx, cfg, hermesRequestExternalUser); err == nil {
		release(0)
		t.Fatal("runtime setting bypassed shared-account hard concurrency=2")
	}
}

func TestIssue75EligibleMemoryOutranksAsyncCompletion(t *testing.T) {
	c := newCompatibilityTrafficController()
	cfg := issue75Settings()

	firstRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestExternalUser)
	if err != nil {
		firstRelease(0)
		t.Fatal(err)
	}

	memoryResult := make(chan issue75MemoryAdmission, 1)
	go func() {
		release, err := c.acquireMemory(context.Background(), cfg)
		memoryResult <- issue75MemoryAdmission{release: release, err: err}
	}()
	asyncCompletion := make(chan issue75InteractiveAdmission, 1)
	go func() {
		release, err := c.acquireInteractiveClass(context.Background(), cfg, hermesRequestAsyncCompletion)
		asyncCompletion <- issue75InteractiveAdmission{release: release, err: err}
	}()
	waitForCompatibilityTraffic(t, c, func(s compatibilityTrafficSnapshot) bool {
		return s.MemoryWaiting == 1 && s.AutonomousWaiting == 1
	})

	firstRelease(0)
	select {
	case got := <-memoryResult:
		if got.err != nil {
			secondRelease(0)
			t.Fatalf("P1 Memory failed admission ahead of async completion: %v", got.err)
		}
		got.release(http.StatusOK)
	case <-time.After(250 * time.Millisecond):
		secondRelease(0)
		t.Fatal("P1 Memory did not take the freed slot ahead of async completion")
	}

	secondRelease(0)
	got := <-asyncCompletion
	if got.err != nil {
		t.Fatal(got.err)
	}
	got.release(0)
}
