package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const memoryQueueMaxWaiting = 64

type compatibilityTrafficSnapshot struct {
	InteractiveInFlight     int       `json:"interactiveInFlight"`
	MemoryInFlight          int       `json:"memoryInFlight"`
	MemoryWaiting           int       `json:"memoryWaiting"`
	Memory429Count          uint64    `json:"memory429Count"`
	Shared429Count          uint64    `json:"shared429Count"`
	Last429Source           string    `json:"last429Source,omitempty"`
	MemoryCooldownUntil     time.Time `json:"memoryCooldownUntil,omitempty"`
	InteractiveHoldoffUntil time.Time `json:"interactiveHoldoffUntil,omitempty"`
}

type compatibilityTrafficController struct {
	mu                      sync.Mutex
	interactiveInFlight     int
	memoryInFlight          int
	memoryWaiting           int
	memory429Count          uint64
	shared429Count          uint64
	last429Source           string
	memoryBackoff           time.Duration
	memoryCooldownUntil     time.Time
	interactiveHoldoffUntil time.Time
	nextWaiterID            uint64
	memoryQueue             []uint64
}

func newCompatibilityTrafficController() *compatibilityTrafficController {
	return &compatibilityTrafficController{}
}

func (c *compatibilityTrafficController) snapshot() compatibilityTrafficSnapshot {
	if c == nil {
		return compatibilityTrafficSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return compatibilityTrafficSnapshot{
		InteractiveInFlight:     c.interactiveInFlight,
		MemoryInFlight:          c.memoryInFlight,
		MemoryWaiting:           c.memoryWaiting,
		Memory429Count:          c.memory429Count,
		Shared429Count:          c.shared429Count,
		Last429Source:           c.last429Source,
		MemoryCooldownUntil:     c.memoryCooldownUntil,
		InteractiveHoldoffUntil: c.interactiveHoldoffUntil,
	}
}

func (c *compatibilityTrafficController) beginInteractive() func(time.Duration) {
	if c == nil {
		return func(time.Duration) {}
	}
	c.mu.Lock()
	c.interactiveInFlight++
	c.mu.Unlock()
	return func(holdoff time.Duration) {
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
}

type memoryAdmissionError struct {
	err        error
	retryAfter int
}

func (e *memoryAdmissionError) Error() string { return e.err.Error() }
func (e *memoryAdmissionError) Unwrap() error { return e.err }

func (c *compatibilityTrafficController) removeWaiterLocked(id uint64) {
	for i, queued := range c.memoryQueue {
		if queued == id {
			c.memoryQueue = append(c.memoryQueue[:i], c.memoryQueue[i+1:]...)
			break
		}
	}
	c.memoryWaiting = len(c.memoryQueue)
}

func (c *compatibilityTrafficController) retryAfterLocked(cfg runtimeSettings, id uint64) int {
	now := time.Now()
	until := c.interactiveHoldoffUntil
	if c.memoryCooldownUntil.After(until) {
		until = c.memoryCooldownUntil
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

func (c *compatibilityTrafficController) applyRateLimitLocked(cfg runtimeSettings, source string) {
	initial := time.Duration(cfg.MemoryBackoffInitialSeconds) * time.Second
	maximum := time.Duration(cfg.MemoryBackoffMaxSeconds) * time.Second
	if c.memoryBackoff < initial {
		c.memoryBackoff = initial
	} else {
		c.memoryBackoff *= 2
		if c.memoryBackoff > maximum {
			c.memoryBackoff = maximum
		}
	}
	c.memoryCooldownUntil = time.Now().Add(c.memoryBackoff)
	c.shared429Count++
	c.last429Source = source
}

func (c *compatibilityTrafficController) observeInteractiveStatus(status int, cfg runtimeSettings, retryAfter string) {
	if c == nil || status != http.StatusTooManyRequests {
		return
	}
	c.mu.Lock()
	c.applyRateLimitLocked(cfg, "interactive")
	c.extendCooldownLocked(retryAfter, time.Now())
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
	if until.After(c.memoryCooldownUntil) {
		c.memoryCooldownUntil = until
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
	if len(c.memoryQueue) >= memoryQueueMaxWaiting {
		retryAfter := c.retryAfterLocked(cfg, c.nextWaiterID)
		c.mu.Unlock()
		return nil, &memoryAdmissionError{err: errors.New("memory waiting queue is full"), retryAfter: retryAfter}
	}
	id := c.nextWaiterID
	c.nextWaiterID++
	c.memoryQueue = append(c.memoryQueue, id)
	c.memoryWaiting = len(c.memoryQueue)
	c.mu.Unlock()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		now := time.Now()
		c.mu.Lock()
		atHead := len(c.memoryQueue) > 0 && c.memoryQueue[0] == id
		allowed := atHead && c.interactiveInFlight == 0 &&
			!now.Before(c.interactiveHoldoffUntil) &&
			!now.Before(c.memoryCooldownUntil) &&
			c.memoryInFlight < cfg.MemoryMaxConcurrent
		if allowed {
			c.memoryQueue = c.memoryQueue[1:]
			c.memoryWaiting = len(c.memoryQueue)
			c.memoryInFlight++
			c.mu.Unlock()
			return func(status int) {
				c.mu.Lock()
				if c.memoryInFlight > 0 {
					c.memoryInFlight--
				}
				if status == http.StatusTooManyRequests {
					c.memory429Count++
					c.applyRateLimitLocked(cfg, "memory")
				} else if status >= 200 && status < 500 {
					c.memoryBackoff = 0
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
			return nil, &memoryAdmissionError{err: queueCtx.Err(), retryAfter: retryAfter}
		case <-ticker.C:
		}
	}
}

type statusTrackingResponseWriter struct {
	http.ResponseWriter
	status        int
	outcomeStatus int
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

func (w *statusTrackingResponseWriter) finalStatus() int {
	if w.outcomeStatus != 0 {
		return w.outcomeStatus
	}
	if w.status != 0 {
		return w.status
	}
	return http.StatusOK
}
