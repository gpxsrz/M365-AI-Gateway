package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"m365-native/internal/chathub"
)

func TestWriteCanonicalTerminalErrorMapsChatHub429(t *testing.T) {
	rr := httptest.NewRecorder()
	err := &chathub.RateLimitError{StatusCode: http.StatusTooManyRequests, Err: errors.New("synthetic")}
	if !writeCanonicalTerminalError(rr, err) {
		t.Fatal("rate limit error was not handled")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After missing")
	}
	if !strings.Contains(rr.Body.String(), "upstream_rate_limited") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

type memoryRepairRateLimitChat struct{ calls int }

func (c *memoryRepairRateLimitChat) Chat(_ context.Context, _ chathub.Account, _ chathub.Request) (chathub.Result, error) {
	c.calls++
	if c.calls == 1 {
		return chathub.Result{Text: `{"城市":"台中"}`, ConversationID: "memory-conversation", SessionID: "memory-session"}, nil
	}
	return chathub.Result{}, &chathub.RateLimitError{StatusCode: http.StatusTooManyRequests, Err: errors.New("synthetic repair 429")}
}
func (c *memoryRepairRateLimitChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}
func (c *memoryRepairRateLimitChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return c.Chat(ctx, account, req)
}

func TestMemorySchemaRepair429TriggersCooldown(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	if _, err := server.tokens.Upsert(testTokenSet("memory-repair-429")); err != nil {
		t.Fatal(err)
	}
	server.chat = &memoryRepairRateLimitChat{}
	server.compatTraffic = newCompatibilityTrafficController()
	body := `{"model":"m365-auto","messages":[{"role":"user","content":"我住台中"}],"response_format":{"type":"json_schema","json_schema":{"name":"memory","schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}}`
	req := withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/memory/v1/chat/completions", strings.NewReader(body)), "memory-owner")
	rr := httptest.NewRecorder()
	server.memoryOpenAIChat(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	snap := server.compatTraffic.snapshot()
	if snap.Memory429Count != 1 || !snap.MemoryCooldownUntil.After(time.Now()) {
		t.Fatalf("cooldown snapshot=%#v", snap)
	}
}

func TestWriteCanonicalTerminalStreamErrorMarksLogical429(t *testing.T) {
	rr := httptest.NewRecorder()
	tracked := &statusTrackingResponseWriter{ResponseWriter: rr}
	tracked.WriteHeader(http.StatusOK) // SSE headers have already been committed.
	err := &chathub.RateLimitError{StatusCode: http.StatusTooManyRequests, Err: errors.New("synthetic")}
	if !writeCanonicalTerminalStreamError(tracked, err) {
		t.Fatal("stream rate limit error was not handled")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("committed SSE status changed to %d", rr.Code)
	}
	if got := tracked.finalStatus(); got != http.StatusTooManyRequests {
		t.Fatalf("logical final status=%d", got)
	}
	if !strings.Contains(rr.Body.String(), "upstream_rate_limited") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}
