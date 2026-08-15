package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	interactiveQueueMaxWaiting           = 64
	memoryQueueMaxWaiting                = 1
	sharedThrottleInitialCooldownSeconds = 1125
	milestoneMemoryLease                 = 300 * time.Second
)

var sharedThrottleCooldownSeconds = [...]int{
	sharedThrottleInitialCooldownSeconds,
	2250,
	4500,
	9000,
	18000,
}

type sharedCircuitState string

const (
	sharedCircuitClosed        sharedCircuitState = "CLOSED"
	sharedCircuitOpen          sharedCircuitState = "OPEN"
	sharedCircuitHalfOpenReady sharedCircuitState = "HALF_OPEN_READY"
	sharedCircuitProbeInFlight sharedCircuitState = "PROBE_IN_FLIGHT"
	sharedCircuitRecovery      sharedCircuitState = "RECOVERY"
)

type compatibilityTrafficSnapshot struct {
	TrafficMode                 string    `json:"trafficMode"`
	InteractiveInFlight         int       `json:"interactiveInFlight"`
	InteractiveWaiting          int       `json:"interactiveWaiting"`
	ExternalUserInFlight        int       `json:"externalUserInFlight"`
	AutonomousInFlight          int       `json:"autonomousInFlight"`
	AutonomousWaiting           int       `json:"autonomousWaiting"`
	EffectiveHermesConcurrency  int       `json:"effectiveHermesConcurrency"`
	MemoryInFlight              int       `json:"memoryInFlight"`
	MemoryWaiting               int       `json:"memoryWaiting"`
	MemoryPendingCount          int       `json:"memoryPendingCount"`
	OldestMemoryAgeSeconds      int64     `json:"oldestMemoryAgeSeconds"`
	MemoryYieldPending          bool      `json:"memoryYieldPending"`
	MemoryYieldActive           bool      `json:"memoryYieldActive"`
	MemoryYieldDeadline         time.Time `json:"memoryYieldDeadline,omitzero"`
	LastMemoryYieldOutcome      string    `json:"lastMemoryYieldOutcome,omitempty"`
	LastMemoryYieldDuration     int64     `json:"lastMemoryYieldDurationMs,omitempty"`
	LastSuccessfulRetain        time.Time `json:"lastSuccessfulRetain,omitzero"`
	LastSuccessfulConsolidation time.Time `json:"lastSuccessfulConsolidation,omitzero"`
	LastHard429                 time.Time `json:"lastHard429,omitzero"`
	LastSoftThrottle            time.Time `json:"lastSoftThrottle,omitzero"`
	ThrottleStreak              int       `json:"throttleStreak"`
	SharedCooldownRemaining     int       `json:"sharedCooldownRemainingSeconds"`
	ReaskSuppressedCount        uint64    `json:"reaskSuppressedCount"`
	Memory429Count              uint64    `json:"memory429Count"`
	Shared429Count              uint64    `json:"shared429Count"`
	Last429Source               string    `json:"last429Source,omitempty"`
	SharedCircuitState          string    `json:"sharedCircuitState"`
	SharedCooldownLevel         int       `json:"sharedCooldownLevel"`
	SharedCooldownUntil         time.Time `json:"sharedCooldownUntil,omitzero"`
	InteractiveHoldoffUntil     time.Time `json:"interactiveHoldoffUntil,omitzero"`
}

type compatibilityTrafficController struct {
	mu                            sync.Mutex
	interactiveInFlight           int
	interactiveWaiting            int
	memoryInFlight                int
	memoryWaiting                 int
	memory429Count                uint64
	shared429Count                uint64
	last429Source                 string
	sharedCooldownLevel           int
	sharedCooldownUntil           time.Time
	sharedCircuitState            sharedCircuitState
	interactiveHoldoffUntil       time.Time
	memoryYieldPending            bool
	memoryYieldActive             bool
	memoryYieldArmedAt            time.Time
	memoryYieldStartedAt          time.Time
	memoryYieldDeadline           time.Time
	lastMemoryYieldOutcome        string
	lastMemoryYieldDuration       time.Duration
	lastSuccessfulRetain          time.Time
	lastSuccessfulConsolidation   time.Time
	lastRetainOperationID         string
	lastConsolidationOperationID  string
	seenHindsightEvents           map[string]time.Time
	nextInteractiveWaiterID       uint64
	interactiveQueue              []uint64
	interactiveWaiterClass        map[uint64]hermesRequestClass
	interactiveAutonomousInFlight int
	interactiveExternalInFlight   int
	configuredInteractiveMax      int
	nextWaiterID                  uint64
	memoryQueue                   []uint64
	memoryWaiterAt                map[uint64]time.Time
	memoryInFlightStartedAt       time.Time
	lastHard429                   time.Time
	lastSoftThrottle              time.Time
	reaskSuppressedCount          uint64
}

func newCompatibilityTrafficController() *compatibilityTrafficController {
	return &compatibilityTrafficController{
		sharedCircuitState:     sharedCircuitClosed,
		interactiveWaiterClass: map[uint64]hermesRequestClass{},
		memoryWaiterAt:         map[uint64]time.Time{},
		seenHindsightEvents:    map[string]time.Time{},
	}
}

func (c *compatibilityTrafficController) seenHindsightEventLocked(eventType, operationID string, eventAt time.Time) bool {
	if operationID == "" {
		return false
	}
	key := eventType + "\x00" + operationID
	if _, ok := c.seenHindsightEvents[key]; ok {
		return true
	}
	if len(c.seenHindsightEvents) >= 256 {
		var oldestKey string
		var oldestAt time.Time
		for candidate, seenAt := range c.seenHindsightEvents {
			if oldestKey == "" || seenAt.Before(oldestAt) {
				oldestKey, oldestAt = candidate, seenAt
			}
		}
		delete(c.seenHindsightEvents, oldestKey)
	}
	c.seenHindsightEvents[key] = eventAt
	return false
}

func (c *compatibilityTrafficController) finishMemoryYieldLocked(outcome string, now time.Time) {
	if !c.memoryYieldPending && !c.memoryYieldActive {
		return
	}
	started := c.memoryYieldStartedAt
	if started.IsZero() {
		started = c.memoryYieldArmedAt
	}
	if !started.IsZero() && !now.Before(started) {
		c.lastMemoryYieldDuration = now.Sub(started)
	}
	c.lastMemoryYieldOutcome = outcome
	c.memoryYieldPending = false
	c.memoryYieldActive = false
	c.memoryYieldArmedAt = time.Time{}
	c.memoryYieldStartedAt = time.Time{}
	c.memoryYieldDeadline = time.Time{}
}

func (c *compatibilityTrafficController) refreshMemoryYieldLocked(now time.Time) {
	if (c.memoryYieldPending || c.memoryYieldActive) && !c.memoryYieldDeadline.IsZero() && !now.Before(c.memoryYieldDeadline) {
		c.finishMemoryYieldLocked("timeout", now)
	}
}

func (c *compatibilityTrafficController) trafficModeLocked(now time.Time) string {
	c.refreshSharedCircuitStateLocked(now)
	c.refreshMemoryYieldLocked(now)
	switch c.sharedCircuitState {
	case sharedCircuitOpen, sharedCircuitHalfOpenReady, sharedCircuitProbeInFlight:
		return "UPSTREAM_COOLDOWN"
	case sharedCircuitRecovery:
		return "RECOVERY"
	}
	if c.memoryYieldPending || c.memoryYieldActive {
		return "MEMORY_YIELD"
	}
	if c.interactiveAutonomousInFlight > 0 || c.memoryWaiting > 0 {
		return "HERMES_BUSY"
	}
	return "NORMAL"
}

func (c *compatibilityTrafficController) autonomousWaitingLocked() int {
	count := 0
	for _, id := range c.interactiveQueue {
		if c.interactiveWaiterClass[id] != hermesRequestExternalUser {
			count++
		}
	}
	return count
}

func (c *compatibilityTrafficController) oldestMemoryAgeLocked(now time.Time) int64 {
	oldest := c.memoryInFlightStartedAt
	for _, id := range c.memoryQueue {
		at := c.memoryWaiterAt[id]
		if at.IsZero() {
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	if oldest.IsZero() || now.Before(oldest) {
		return 0
	}
	return int64(now.Sub(oldest).Seconds())
}

func (c *compatibilityTrafficController) effectiveHermesConcurrencyLocked(now time.Time) int {
	maxConcurrent := c.configuredInteractiveMax
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	switch c.trafficModeLocked(now) {
	case "UPSTREAM_COOLDOWN":
		return 0
	case "MEMORY_YIELD", "RECOVERY", "HERMES_BUSY":
		return 1
	default:
		return maxConcurrent
	}
}

func (c *compatibilityTrafficController) refreshSharedCircuitStateLocked(now time.Time) {
	if c.sharedCircuitState == "" {
		c.sharedCircuitState = sharedCircuitClosed
	}
	if c.sharedCircuitState == sharedCircuitOpen && !now.Before(c.sharedCooldownUntil) {
		c.sharedCircuitState = sharedCircuitHalfOpenReady
	}
}

func (c *compatibilityTrafficController) snapshot() compatibilityTrafficSnapshot {
	if c == nil {
		return compatibilityTrafficSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.refreshSharedCircuitStateLocked(now)
	c.refreshMemoryYieldLocked(now)
	return compatibilityTrafficSnapshot{
		TrafficMode:                 c.trafficModeLocked(now),
		InteractiveInFlight:         c.interactiveInFlight,
		InteractiveWaiting:          c.interactiveWaiting,
		ExternalUserInFlight:        c.interactiveExternalInFlight,
		AutonomousInFlight:          c.interactiveAutonomousInFlight,
		AutonomousWaiting:           c.autonomousWaitingLocked(),
		EffectiveHermesConcurrency:  c.effectiveHermesConcurrencyLocked(now),
		MemoryInFlight:              c.memoryInFlight,
		MemoryWaiting:               c.memoryWaiting,
		MemoryPendingCount:          c.memoryInFlight + c.memoryWaiting,
		OldestMemoryAgeSeconds:      c.oldestMemoryAgeLocked(now),
		MemoryYieldPending:          c.memoryYieldPending,
		MemoryYieldActive:           c.memoryYieldActive,
		MemoryYieldDeadline:         c.memoryYieldDeadline,
		LastMemoryYieldOutcome:      c.lastMemoryYieldOutcome,
		LastMemoryYieldDuration:     c.lastMemoryYieldDuration.Milliseconds(),
		LastSuccessfulRetain:        c.lastSuccessfulRetain,
		LastSuccessfulConsolidation: c.lastSuccessfulConsolidation,
		LastHard429:                 c.lastHard429,
		LastSoftThrottle:            c.lastSoftThrottle,
		ThrottleStreak:              c.sharedCooldownLevel,
		SharedCooldownRemaining: func() int {
			if c.sharedCooldownUntil.After(now) {
				return int(c.sharedCooldownUntil.Sub(now).Seconds()) + 1
			}
			return 0
		}(),
		ReaskSuppressedCount:    c.reaskSuppressedCount,
		Memory429Count:          c.memory429Count,
		Shared429Count:          c.shared429Count,
		Last429Source:           c.last429Source,
		SharedCircuitState:      string(c.sharedCircuitState),
		SharedCooldownLevel:     c.sharedCooldownLevel,
		SharedCooldownUntil:     c.sharedCooldownUntil,
		InteractiveHoldoffUntil: c.interactiveHoldoffUntil,
	}
}

func (c *compatibilityTrafficController) snapshotForSettings(cfg runtimeSettings) compatibilityTrafficSnapshot {
	if c == nil {
		return compatibilityTrafficSnapshot{}
	}
	c.mu.Lock()
	c.configuredInteractiveMax = cfg.InteractiveMaxConcurrent
	c.mu.Unlock()
	return c.snapshot()
}

func (c *compatibilityTrafficController) observeThrottleDetection(soft bool) {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	if soft {
		c.lastSoftThrottle = now
	} else {
		c.lastHard429 = now
	}
	c.mu.Unlock()
}

func (c *compatibilityTrafficController) observeReaskSuppressed(count int) {
	if c == nil || count <= 0 {
		return
	}
	c.mu.Lock()
	c.reaskSuppressedCount += uint64(count)
	c.mu.Unlock()
}

func (c *compatibilityTrafficController) observeHermesCompletion(class hermesRequestClass, status int) {
	if c == nil || class != hermesRequestAsyncCompletion || status < 200 || status >= 300 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	c.memoryYieldPending = true
	c.memoryYieldActive = false
	c.memoryYieldArmedAt = now
	c.memoryYieldStartedAt = time.Time{}
	c.memoryYieldDeadline = now.Add(milestoneMemoryLease)
	c.lastMemoryYieldOutcome = "pending"
	c.lastMemoryYieldDuration = 0
	c.mu.Unlock()
}

func (c *compatibilityTrafficController) observeHindsightEvent(eventType, operationID, status string, eventAt time.Time) {
	if c == nil || status != "completed" {
		return
	}
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seenHindsightEventLocked(eventType, operationID, eventAt) {
		return
	}
	switch eventType {
	case "retain.completed":
		c.lastRetainOperationID = operationID
		c.lastSuccessfulRetain = eventAt
		if (c.memoryYieldPending || c.memoryYieldActive) && !eventAt.Before(c.memoryYieldArmedAt) {
			c.finishMemoryYieldLocked("retain_durable", time.Now())
		}
	case "consolidation.completed":
		c.lastConsolidationOperationID = operationID
		c.lastSuccessfulConsolidation = eventAt
	}
}

func (c *compatibilityTrafficController) completeRecovery() error {
	if c == nil {
		return errors.New("shared traffic controller is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshSharedCircuitStateLocked(time.Now())
	if c.sharedCircuitState != sharedCircuitRecovery {
		return fmt.Errorf("shared circuit is %s, not RECOVERY", c.sharedCircuitState)
	}
	c.sharedCircuitState = sharedCircuitClosed
	c.sharedCooldownLevel = 0
	c.sharedCooldownUntil = time.Time{}
	return nil
}

func (c *compatibilityTrafficController) releaseInteractive(holdoff time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.interactiveInFlight > 0 {
		c.interactiveInFlight--
	}
	until := time.Now().Add(holdoff)
	if until.After(c.interactiveHoldoffUntil) {
		c.interactiveHoldoffUntil = until
	}
	c.mu.Unlock()
}

type memoryAdmissionError struct {
	err        error
	retryAfter int
	code       string
}

func (e *memoryAdmissionError) Error() string { return e.err.Error() }
func (e *memoryAdmissionError) Unwrap() error { return e.err }

type interactiveAdmissionError struct {
	err        error
	retryAfter int
}

func (e *interactiveAdmissionError) Error() string { return e.err.Error() }
func (e *interactiveAdmissionError) Unwrap() error { return e.err }

func (c *compatibilityTrafficController) removeInteractiveWaiterLocked(id uint64) {
	for i, queued := range c.interactiveQueue {
		if queued == id {
			c.interactiveQueue = append(c.interactiveQueue[:i], c.interactiveQueue[i+1:]...)
			break
		}
	}
	delete(c.interactiveWaiterClass, id)
	c.interactiveWaiting = len(c.interactiveQueue)
}

func (c *compatibilityTrafficController) enqueueInteractiveWaiterLocked(id uint64, class hermesRequestClass) {
	c.interactiveWaiterClass[id] = class
	if class != hermesRequestExternalUser {
		c.interactiveQueue = append(c.interactiveQueue, id)
		c.interactiveWaiting = len(c.interactiveQueue)
		return
	}
	// Preserve FIFO within the external-user class, but keep human/API user
	// turns ahead of queued autonomous work so a milestone barrier or a busy
	// background worker cannot turn into user-visible head-of-line blocking.
	insertAt := len(c.interactiveQueue)
	for i, queued := range c.interactiveQueue {
		if c.interactiveWaiterClass[queued] != hermesRequestExternalUser {
			insertAt = i
			break
		}
	}
	c.interactiveQueue = append(c.interactiveQueue, 0)
	copy(c.interactiveQueue[insertAt+1:], c.interactiveQueue[insertAt:])
	c.interactiveQueue[insertAt] = id
	c.interactiveWaiting = len(c.interactiveQueue)
}

func (c *compatibilityTrafficController) hasBlockingInteractiveWaiterLocked() bool {
	for _, id := range c.interactiveQueue {
		if c.interactiveWaiterClass[id] != hermesRequestAutonomousContinuation {
			return true
		}
	}
	return false
}

func (c *compatibilityTrafficController) removeWaiterLocked(id uint64) {
	for i, queued := range c.memoryQueue {
		if queued == id {
			c.memoryQueue = append(c.memoryQueue[:i], c.memoryQueue[i+1:]...)
			break
		}
	}
	delete(c.memoryWaiterAt, id)
	c.memoryWaiting = len(c.memoryQueue)
}

func (c *compatibilityTrafficController) retryAfterLocked(cfg runtimeSettings, id uint64) int {
	now := time.Now()
	until := c.interactiveHoldoffUntil
	if c.sharedCooldownUntil.After(until) {
		until = c.sharedCooldownUntil
	}
	seconds := 2 + int(id%4)
	if until.After(now) {
		remaining := int(time.Until(until).Seconds()) + 1
		if remaining > seconds {
			seconds = remaining
		}
	}
	if seconds > cfg.MemoryQueueTimeoutSeconds {
		seconds = cfg.MemoryQueueTimeoutSeconds
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func (c *compatibilityTrafficController) sharedThrottleRetryAfterLocked(now time.Time) int {
	if c.sharedCooldownUntil.After(now) {
		remaining := int(c.sharedCooldownUntil.Sub(now).Seconds()) + 1
		if remaining > 0 {
			return remaining
		}
	}
	level := c.sharedCooldownLevel
	if level < 1 {
		level = 1
	}
	if level > len(sharedThrottleCooldownSeconds) {
		level = len(sharedThrottleCooldownSeconds)
	}
	return sharedThrottleCooldownSeconds[level-1]
}

func (c *compatibilityTrafficController) interactiveRetryAfterLocked(cfg runtimeSettings, id uint64) int {
	seconds := 2 + int(id%4)
	if c.sharedCooldownUntil.After(time.Now()) {
		remaining := int(time.Until(c.sharedCooldownUntil).Seconds()) + 1
		if remaining > seconds {
			seconds = remaining
		}
	}
	if seconds > cfg.InteractiveQueueTimeoutSeconds {
		seconds = cfg.InteractiveQueueTimeoutSeconds
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func (c *compatibilityTrafficController) applyRateLimitLocked(source string) {
	if c.sharedCooldownLevel < len(sharedThrottleCooldownSeconds) {
		c.sharedCooldownLevel++
	}
	c.sharedCooldownUntil = time.Now().Add(time.Duration(sharedThrottleCooldownSeconds[c.sharedCooldownLevel-1]) * time.Second)
	c.sharedCircuitState = sharedCircuitOpen
	c.shared429Count++
	c.last429Source = source
}

func (c *compatibilityTrafficController) observeInteractiveStatus(status int, retryAfter string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.refreshSharedCircuitStateLocked(time.Now())
	if status == http.StatusTooManyRequests {
		c.applyRateLimitLocked("interactive")
		c.extendCooldownLocked(retryAfter, time.Now())
	} else if c.sharedCircuitState == sharedCircuitProbeInFlight {
		if status >= 200 && status < 300 {
			c.sharedCircuitState = sharedCircuitRecovery
		} else {
			c.sharedCircuitState = sharedCircuitHalfOpenReady
		}
	}
	c.mu.Unlock()
}

func (c *compatibilityTrafficController) honorRetryAfter(retryAfter string) {
	if c == nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	c.extendCooldownLocked(retryAfter, now)
	c.mu.Unlock()
}

func (c *compatibilityTrafficController) extendCooldownLocked(retryAfter string, now time.Time) {
	retryAfter = strings.TrimSpace(retryAfter)
	if retryAfter == "" {
		return
	}
	var delay time.Duration
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
		delay = seconds
	} else if when, err := http.ParseTime(retryAfter); err == nil && when.After(now) {
		delay = when.Sub(now)
	}
	if delay <= 0 {
		return
	}
	until := now.Add(delay)
	if until.After(c.sharedCooldownUntil) {
		c.sharedCooldownUntil = until
	}
}

func (c *compatibilityTrafficController) acquireInteractive(ctx context.Context, cfg runtimeSettings) (func(time.Duration), error) {
	return c.acquireInteractiveClass(ctx, cfg, hermesRequestExternalUser)
}

func (c *compatibilityTrafficController) acquireInteractiveClass(ctx context.Context, cfg runtimeSettings, class hermesRequestClass) (func(time.Duration), error) {
	if c == nil {
		return func(time.Duration) {}, nil
	}
	queueTimeout := time.Duration(cfg.InteractiveQueueTimeoutSeconds) * time.Second
	queueCtx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()

	c.mu.Lock()
	c.configuredInteractiveMax = cfg.InteractiveMaxConcurrent
	if len(c.interactiveQueue) >= interactiveQueueMaxWaiting {
		retryAfter := c.interactiveRetryAfterLocked(cfg, c.nextInteractiveWaiterID)
		c.mu.Unlock()
		return nil, &interactiveAdmissionError{err: errors.New("interactive waiting queue is full"), retryAfter: retryAfter}
	}
	id := c.nextInteractiveWaiterID
	c.nextInteractiveWaiterID++
	c.enqueueInteractiveWaiterLocked(id, class)
	c.mu.Unlock()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		now := time.Now()
		c.mu.Lock()
		c.refreshSharedCircuitStateLocked(now)
		c.refreshMemoryYieldLocked(now)
		if class == hermesRequestExternalUser && (c.memoryYieldPending || c.memoryYieldActive) {
			c.finishMemoryYieldLocked("preempted_by_interactive", now)
		}
		atHead := len(c.interactiveQueue) > 0 && c.interactiveQueue[0] == id
		allowed := false
		if atHead {
			if class != hermesRequestExternalUser && c.interactiveAutonomousInFlight >= 1 {
				allowed = false
			} else if class == hermesRequestAutonomousContinuation && (c.memoryYieldPending || c.memoryYieldActive) {
				allowed = false
			} else {
				switch c.sharedCircuitState {
				case sharedCircuitClosed:
					allowed = c.interactiveInFlight < cfg.InteractiveMaxConcurrent && !now.Before(c.sharedCooldownUntil)
				case sharedCircuitHalfOpenReady:
					allowed = class == hermesRequestExternalUser && c.interactiveInFlight == 0
				case sharedCircuitRecovery:
					allowed = c.interactiveInFlight < 1
				}
			}
		}
		if allowed {
			if c.sharedCircuitState == sharedCircuitHalfOpenReady {
				c.sharedCircuitState = sharedCircuitProbeInFlight
			}
			c.interactiveQueue = c.interactiveQueue[1:]
			c.interactiveWaiting = len(c.interactiveQueue)
			delete(c.interactiveWaiterClass, id)
			c.interactiveInFlight++
			if class != hermesRequestExternalUser {
				c.interactiveAutonomousInFlight++
			} else {
				c.interactiveExternalInFlight++
			}
			c.mu.Unlock()
			return func(holdoff time.Duration) {
				c.mu.Lock()
				if c.interactiveInFlight > 0 {
					c.interactiveInFlight--
				}
				if class != hermesRequestExternalUser && c.interactiveAutonomousInFlight > 0 {
					c.interactiveAutonomousInFlight--
				} else if class == hermesRequestExternalUser && c.interactiveExternalInFlight > 0 {
					c.interactiveExternalInFlight--
				}
				until := time.Now().Add(holdoff)
				if until.After(c.interactiveHoldoffUntil) {
					c.interactiveHoldoffUntil = until
				}
				c.mu.Unlock()
			}, nil
		}
		c.mu.Unlock()

		select {
		case <-queueCtx.Done():
			c.mu.Lock()
			retryAfter := c.interactiveRetryAfterLocked(cfg, id)
			c.removeInteractiveWaiterLocked(id)
			c.mu.Unlock()
			return nil, &interactiveAdmissionError{err: queueCtx.Err(), retryAfter: retryAfter}
		case <-ticker.C:
		}
	}
}

func (c *compatibilityTrafficController) acquireMemory(ctx context.Context, cfg runtimeSettings) (func(int), error) {
	if c == nil {
		return func(int) {}, nil
	}
	queueTimeout := time.Duration(cfg.MemoryQueueTimeoutSeconds) * time.Second
	queueCtx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()

	c.mu.Lock()
	now := time.Now()
	c.refreshSharedCircuitStateLocked(now)
	if c.sharedCircuitState != sharedCircuitClosed {
		retryAfter := c.sharedThrottleRetryAfterLocked(now)
		c.mu.Unlock()
		return nil, &memoryAdmissionError{err: errors.New("shared Microsoft account is throttled"), retryAfter: retryAfter, code: "upstream_throttle"}
	}
	if len(c.memoryQueue) >= memoryQueueMaxWaiting {
		retryAfter := c.retryAfterLocked(cfg, c.nextWaiterID)
		c.mu.Unlock()
		return nil, &memoryAdmissionError{err: errors.New("memory waiting queue is full"), retryAfter: retryAfter, code: "memory_capacity_deferred"}
	}
	id := c.nextWaiterID
	c.nextWaiterID++
	c.memoryQueue = append(c.memoryQueue, id)
	c.memoryWaiterAt[id] = time.Now()
	c.memoryWaiting = len(c.memoryQueue)
	c.mu.Unlock()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		now := time.Now()
		c.mu.Lock()
		c.refreshSharedCircuitStateLocked(now)
		c.refreshMemoryYieldLocked(now)
		atHead := len(c.memoryQueue) > 0 && c.memoryQueue[0] == id
		interactiveBlocked := c.interactiveWaiting > 0
		if c.memoryYieldPending || c.memoryYieldActive {
			interactiveBlocked = c.hasBlockingInteractiveWaiterLocked()
		}
		allowed := atHead && c.interactiveInFlight == 0 && !interactiveBlocked &&
			!now.Before(c.interactiveHoldoffUntil) &&
			!now.Before(c.sharedCooldownUntil) &&
			c.sharedCircuitState == sharedCircuitClosed &&
			c.memoryInFlight < cfg.MemoryMaxConcurrent
		if allowed {
			c.memoryQueue = c.memoryQueue[1:]
			delete(c.memoryWaiterAt, id)
			c.memoryWaiting = len(c.memoryQueue)
			c.memoryInFlight++
			c.memoryInFlightStartedAt = now
			if c.memoryYieldPending {
				c.memoryYieldPending = false
				c.memoryYieldActive = true
				c.memoryYieldStartedAt = now
			}
			c.mu.Unlock()
			return func(status int) {
				c.mu.Lock()
				if c.memoryInFlight > 0 {
					c.memoryInFlight--
				}
				if c.memoryInFlight == 0 {
					c.memoryInFlightStartedAt = time.Time{}
				}
				if status == http.StatusTooManyRequests {
					c.memory429Count++
					c.applyRateLimitLocked("memory")
				}
				c.mu.Unlock()
			}, nil
		}
		c.mu.Unlock()

		select {
		case <-queueCtx.Done():
			c.mu.Lock()
			retryAfter := c.retryAfterLocked(cfg, id)
			c.removeWaiterLocked(id)
			c.mu.Unlock()
			return nil, &memoryAdmissionError{err: queueCtx.Err(), retryAfter: retryAfter, code: "interactive_capacity_busy"}
		case <-ticker.C:
		}
	}
}

type statusTrackingResponseWriter struct {
	http.ResponseWriter
	status         int
	outcomeStatus  int
	softThrottle   bool
	retryPotential bool
}

func (w *statusTrackingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusTrackingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusTrackingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusTrackingResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusTrackingResponseWriter) setOutcomeStatus(status int) {
	w.outcomeStatus = status
}

func (w *statusTrackingResponseWriter) setSoftThrottle(soft bool) {
	w.softThrottle = soft
}

func (w *statusTrackingResponseWriter) setRetryPotential(potential bool) {
	w.retryPotential = potential
}

func (w *statusTrackingResponseWriter) finalStatus() int {
	if w.outcomeStatus != 0 {
		return w.outcomeStatus
	}
	if w.status != 0 {
		return w.status
	}
	return http.StatusOK
}
