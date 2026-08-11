package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesRejectsConflictingContinuationSelectors(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol","input":"continue","previous_response_id":"resp_parent","conversation":"conversation-key"}`))
	server.responses(rr, withAPIKeyOwner(req, "responses-conflict"))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "conflicting_continuation") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
