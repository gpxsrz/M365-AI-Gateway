package web

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type compatibilityTrafficSnapshot struct {
	HermesInFlight      int       `json:"hermesInFlight"`
	MemoryInFlight      int       `json:"memoryInFlight"`
	MemoryWaiting       int       `json:"memoryWaiting"`
	Memory429Count      uint64    `json:"memory429Count"`
	MemoryCooldownUntil time.Time `json:"memoryCooldownUntil,omitempty"`
	HermesHoldoffUntil  time.Time `json:"hermesHoldoffUntil,omitempty"`
}

type compatibilityTrafficController struct {
	mu                  sync.Mutex
	hermesInFlight      int
	memoryInFlight      int
	memoryWaiting       int
	memory429Count      uint64
	memoryBackoff       time.Duration
	memoryCooldownUntil time.Time
	hermesHoldoffUntil  time.Time
	nextWaiterID        uint64
	memoryQueue         []uint64
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
		HermesInFlight:      c.hermesInFlight,
		MemoryInFlight:      c.memoryInFlight,
		MemoryWaiting:       c.memoryWaiting,
		Memory429Count:      c.memory429Count,
		MemoryCooldownUntil: c.memoryCooldownUntil,
		HermesHoldoffUntil:  c.hermesHoldoffUntil,
	}
}

func (c *compatibilityTrafficController) beginHermes() func(time.Duration) {
	if c == nil {
		return func(time.Duration) {}
	}
	c.mu.Lock()
	c.hermesInFlight++
	c.mu.Unlock()
	return func(holdoff time.Duration) {
		c.mu.Lock()
		if c.hermesInFlight > 0 {
			c.hermesInFlight--
		}
		until := time.Now().Add(holdoff)
		if until.After(c.hermesHoldoffUntil) {
			c.hermesHoldoffUntil = until
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
	until := c.hermesHoldoffUntil
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

func (c *compatibilityTrafficController) acquireMemory(ctx context.Context, cfg runtimeSettings) (func(int), error) {
	if c == nil {
		return func(int) {}, nil
	}
	queueTimeout := time.Duration(cfg.MemoryQueueTimeoutSeconds) * time.Second
	queueCtx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()

	c.mu.Lock()
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
		allowed := atHead && c.hermesInFlight == 0 &&
			!now.Before(c.hermesHoldoffUntil) &&
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
