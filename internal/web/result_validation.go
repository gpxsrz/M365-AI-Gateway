package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"m365-native/internal/chathub"
	"net/http"
	"strconv"
	"strings"
)

func validImageURLs(images []string) []string {
	out := make([]string, 0, len(images))
	seen := map[string]bool{}
	for _, image := range images {
		image = strings.TrimSpace(image)
		if !chathub.IsImageURL(image) || chathub.ContainsProtectedArtifactReference(image) || seen[image] {
			continue
		}
		seen[image] = true
		out = append(out, image)
	}
	return out
}

func usableChatResult(res chathub.Result, tools []chathub.Tool) bool {
	if strings.TrimSpace(res.Text) != "" || len(validImageURLs(res.Images)) > 0 || strings.TrimSpace(chathub.ReasoningContent(res.Events)) != "" {
		return true
	}
	return len(nativeToolCalls(res.Events, tools)) > 0
}

func requireUsableChatResult(w http.ResponseWriter, res chathub.Result, tools []chathub.Tool) bool {
	if usableChatResult(res, tools) {
		return true
	}
	if writeCanonicalTerminalFailure(w, res.Terminal) {
		return false
	}
	writeUpstreamEmptyResponse(w)
	return false
}

func requireSafeNativeToolEmission(w http.ResponseWriter, res chathub.Result, tools []chathub.Tool) (nativeToolCallScan, bool) {
	scan := scanNativeToolCalls(res.Events, tools)
	if err := blockedNativeToolError(scan); err != nil {
		writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "native_mutation_blocked", err.Error())
		return scan, false
	}
	return scan, true
}

func legacyTextResultFailureMessage(res chathub.Result, tools []chathub.Tool) string {
	if len(res.Images) > 0 || len(nativeToolCalls(res.Events, tools)) > 0 || len(res.Events) > 0 || len(res.Normalized) > 0 || strings.TrimSpace(res.RawResult) != "" {
		return "Upstream returned content, but the legacy text adapter has no deliverable result"
	}
	return "Upstream returned no usable result"
}

func requireUsableLegacyNonStreamResult(w http.ResponseWriter, res chathub.Result, tools []chathub.Tool) bool {
	if strings.TrimSpace(res.Text) != "" || len(res.Images) > 0 {
		return true
	}
	if writeCanonicalTerminalFailure(w, res.Terminal) {
		return false
	}
	writeUpstreamEmptyResponseMessage(w, legacyTextResultFailureMessage(res, tools))
	return false
}

func requireUsableLegacyTextResult(w http.ResponseWriter, res chathub.Result, tools []chathub.Tool) bool {
	if strings.TrimSpace(res.Text) != "" {
		return true
	}
	if writeCanonicalTerminalFailure(w, res.Terminal) {
		return false
	}
	writeUpstreamEmptyResponseMessage(w, legacyTextResultFailureMessage(res, tools))
	return false
}

func canonicalTerminalFailure(terminal chathub.TerminalState) (code, message string, ok bool) {
	message = strings.TrimSpace(terminal.Error)
	if message == "" {
		message = "Microsoft 365 Copilot ended the request without a deliverable answer"
	}
	switch terminal.Kind {
	case "disengaged":
		return "upstream_disengaged", message, true
	case "auth_error":
		return "upstream_auth_error", message, true
	case "error":
		return "upstream_terminal_error", message, true
	case "close":
		if terminal.AllowReconnect != nil && *terminal.AllowReconnect {
			return "upstream_closed_reconnectable", message, true
		}
		return "upstream_closed", message, true
	default:
		return "", "", false
	}
}

func writeCanonicalTerminalFailure(w http.ResponseWriter, terminal chathub.TerminalState) bool {
	code, message, ok := canonicalTerminalFailure(terminal)
	if !ok {
		return false
	}
	writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", code, message)
	return true
}

func writeCanonicalTerminalError(w http.ResponseWriter, err error) bool {
	var rateLimit *chathub.RateLimitError
	if errors.As(err, &rateLimit) {
		w.Header().Set("Retry-After", canonicalRetryAfter(rateLimit.RetryAfter))
		writeOpenAIErrorCode(w, http.StatusTooManyRequests, "rate_limit_error", "upstream_rate_limited", "Microsoft 365 Copilot is temporarily rate limited")
		return true
	}
	var terminal *chathub.TerminalError
	if !errors.As(err, &terminal) {
		return false
	}
	return writeCanonicalTerminalFailure(w, terminal.State)
}

func writeCanonicalTerminalStreamError(w http.ResponseWriter, err error) bool {
	var rateLimit *chathub.RateLimitError
	if errors.As(err, &rateLimit) {
		retryAfter := canonicalRetryAfter(rateLimit.RetryAfter)
		w.Header().Set("Retry-After", retryAfter)
		if tracker, ok := w.(interface{ setOutcomeStatus(int) }); ok {
			tracker.setOutcomeStatus(http.StatusTooManyRequests)
		}
		writeChatStreamRateLimitError(w, retryAfter)
		return true
	}
	var terminal *chathub.TerminalError
	if !errors.As(err, &terminal) {
		return false
	}
	code, message, ok := canonicalTerminalFailure(terminal.State)
	if !ok {
		return false
	}
	writeChatStreamError(w, code, message)
	return true
}

func writeUpstreamEmptyResponse(w http.ResponseWriter) {
	writeUpstreamEmptyResponseMessage(w, "ChatHub returned no text, tool call, or image result")
}

func writeUpstreamEmptyResponseMessage(w http.ResponseWriter, message string) {
	writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "upstream_empty_response", message)
}

func canonicalRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 63); err == nil {
		return strconv.FormatUint(seconds, 10)
	}
	if when, err := http.ParseTime(value); err == nil {
		return when.UTC().Format(http.TimeFormat)
	}
	return "1"
}

func writeChatStreamRateLimitError(w http.ResponseWriter, retryAfter string) {
	writeChatStreamErrorPayload(w, map[string]any{
		"type":        "upstream_error",
		"code":        "upstream_rate_limited",
		"message":     "Microsoft 365 Copilot is temporarily rate limited",
		"retry_after": retryAfter,
	})
}

func writeChatStreamError(w http.ResponseWriter, code, message string) {
	writeChatStreamErrorPayload(w, map[string]any{
		"type":    "upstream_error",
		"code":    code,
		"message": message,
	})
}

func writeChatStreamErrorPayload(w http.ResponseWriter, errorPayload map[string]any) {
	payload, _ := json.Marshal(map[string]any{"error": errorPayload})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
