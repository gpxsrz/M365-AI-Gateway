package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

func errorMessage(raw []byte, fallback string) string {
	_, _, message := openAIErrorDetails(raw)
	if message != "" {
		return message
	}
	if s := strings.TrimSpace(string(raw)); s != "" {
		return s
	}
	return fallback
}
func writeOpenAIError(w http.ResponseWriter, status int, typ, msg string) {
	writeOpenAIErrorCode(w, status, typ, "", msg)
}

func writeOpenAIErrorCode(w http.ResponseWriter, status int, typ, code, msg string) {
	errBody := map[string]any{"message": msg, "type": typ}
	if code != "" {
		errBody["code"] = code
	}
	writeOpenAIErrorObject(w, status, errBody)
}

func writeOpenAIErrorObject(w http.ResponseWriter, status int, errBody map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": errBody})
}

func writeToolRouterRepairInputTooLarge(w http.ResponseWriter, received, limit int) {
	writeOpenAIErrorObject(w, http.StatusBadGateway, map[string]any{
		"message":            "tool router repair input exceeds the safe internal text budget; refusing to truncate structured arguments",
		"type":               "upstream_error",
		"code":               "tool_router_repair_input_too_large",
		"limit_type":         "repair_prompt_utf16",
		"limit":              limit,
		"received":           received,
		"terminal":           true,
		"retryable":          false,
		"recommended_action": "regenerate_tool_routing_decision",
	})
}

func openAIErrorDetails(raw []byte) (typ, code, message string) {
	errBody := openAIErrorObject(raw)
	typ, _ = errBody["type"].(string)
	code, _ = errBody["code"].(string)
	message, _ = errBody["message"].(string)
	return typ, code, message
}

func openAIErrorObject(raw []byte) map[string]any {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	errBody, _ := v["error"].(map[string]any)
	if errBody == nil {
		return nil
	}
	return errBody
}

func writeResponsesError(w http.ResponseWriter, status int, typ, msg string) {
	writeOpenAIError(w, status, typ, msg)
}

func writeResponsesErrorCode(w http.ResponseWriter, status int, typ, code, msg string) {
	writeOpenAIErrorCode(w, status, typ, code, msg)
}

func writeAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	writeAnthropicErrorCode(w, status, typ, "", msg)
}

func writeAnthropicErrorCode(w http.ResponseWriter, status int, typ, code, msg string) {
	errBody := map[string]any{"type": typ, "message": msg}
	if code != "" {
		errBody["code"] = code
	}
	writeAnthropicErrorObject(w, status, errBody)
}

func writeAnthropicErrorObject(w http.ResponseWriter, status int, errBody map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": errBody})
}
