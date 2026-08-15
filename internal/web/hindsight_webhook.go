package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	hindsightWebhookPath     = "/internal/hindsight/webhook"
	hindsightWebhookMaxBytes = 64 << 10
)

type hindsightWebhookEvent struct {
	Event       string    `json:"event"`
	BankID      string    `json:"bank_id"`
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
	Data        struct {
		DocumentID      string   `json:"document_id,omitempty"`
		Tags            []string `json:"tags,omitempty"`
		MemoryUnitCount *int     `json:"memory_unit_count,omitempty"`
	} `json:"data"`
}

func hindsightWebhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validHindsightWebhookSignature(secret, provided string, payload []byte) bool {
	secret = strings.TrimSpace(secret)
	provided = strings.TrimSpace(provided)
	if secret == "" || provided == "" {
		return false
	}
	expected := hindsightWebhookSignature(secret, payload)
	return hmac.Equal([]byte(provided), []byte(expected))
}

func (s *Server) hindsightWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	secret := strings.TrimSpace(os.Getenv("M365_HINDSIGHT_WEBHOOK_SECRET"))
	if secret == "" {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "Hindsight webhook secret is not configured")
		return
	}
	reader := http.MaxBytesReader(w, r.Body, hindsightWebhookMaxBytes)
	payload, err := io.ReadAll(reader)
	if err != nil {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "Hindsight webhook payload is too large")
		return
	}
	if !validHindsightWebhookSignature(secret, r.Header.Get("X-Hindsight-Signature"), payload) {
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "invalid Hindsight webhook signature")
		return
	}
	var event hindsightWebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid Hindsight webhook payload")
		return
	}
	event.Event = strings.TrimSpace(event.Event)
	event.Status = strings.TrimSpace(event.Status)
	event.OperationID = strings.TrimSpace(event.OperationID)
	if headerEvent := strings.TrimSpace(r.Header.Get("X-Hindsight-Event")); headerEvent != "" && headerEvent != event.Event {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Hindsight webhook event header does not match payload")
		return
	}
	switch event.Event {
	case "retain.completed", "consolidation.completed":
		if event.OperationID == "" || event.Timestamp.IsZero() {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "Hindsight webhook operation_id and timestamp are required")
			return
		}
		s.compatibilityTrafficRuntime().observeHindsightEvent(event.Event, event.OperationID, event.Status, event.Timestamp)
	default:
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "unsupported Hindsight webhook event")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
