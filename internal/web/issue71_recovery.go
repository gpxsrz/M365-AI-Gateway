package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) adminTrafficRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Action) != "complete" {
		writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "invalid_recovery_action", "action must be complete")
		return
	}
	traffic := s.compatibilityTrafficRuntime()
	if err := traffic.completeRecovery(); err != nil {
		writeOpenAIErrorCode(w, http.StatusConflict, "invalid_state_error", "recovery_not_ready", err.Error())
		return
	}
	jsonOut(w, map[string]any{"ok": true, "compatibilityTraffic": traffic.snapshotForSettings(serverRuntimeSettings(s))})
}
