package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"m365-native/internal/chathub"
)

func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	settings := serverRuntimeSettings(s)
	bodyLimit, err := requestBodyLimit(settings.TextInputLimitUTF16)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body chatBody
	if err := decodeBoundedJSON(w, r, bodyLimit, &body); err != nil {
		if isRequestBodyTooLarge(err) {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(firstNonEmpty(body.Message, body.Prompt))
	if text == "" && len(body.Attachments) == 0 {
		http.Error(w, "message or attachment required", http.StatusBadRequest)
		return
	}
	downgraded, err := normalizeCompatibilityParameters(body.Attachments, body.Verbosity)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	setDowngradedParameters(w, downgraded)
	if err := validateCallerString(text, settings.TextInputLimitUTF16); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	effort := body.ReasoningEffort
	if body.Reasoning != nil && strings.TrimSpace(body.Reasoning.Effort) != "" {
		effort = body.Reasoning.Effort
	}
	resolution, routeErr := resolveChatRoute(body.Model, body.Tone, effort, settings.ModelMappings)
	if routeErr != nil {
		if typed, ok := routeErr.(*routeResolveError); ok {
			writeOpenAIErrorCode(w, typed.Status, "invalid_request_error", typed.Code, typed.Message)
		} else {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", routeErr.Error())
		}
		return
	}
	turn, err := s.beginLegacyCheckpoint(body.SessionKey, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer turn.Abort()
	body.ConversationID = turn.binding.ConversationID
	body.SessionID = turn.binding.SessionID
	acc, err := s.activeAccount()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if acc.OID == "" || acc.TID == "" {
		if o, t := extractOIDTID(acc.AccessToken); o != "" {
			acc.OID, acc.TID = o, t
		}
	}
	if acc.OID == "" || acc.TID == "" {
		http.Error(w, "account missing oid/tid", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(settings.ChatTimeoutSeconds)*time.Second)
	defer cancel()
	account, err := s.chatHubAccount(ctx, acc, body.Attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res, err := s.chat.Chat(ctx, account, chathub.Request{
		Text: text, Tone: resolution.ResolvedTone, ConversationID: body.ConversationID, SessionID: body.SessionID, Attachments: body.Attachments,
	})
	if err != nil {
		if writeCanonicalTerminalError(w, err) {
			return
		}
		http.Error(w, upstreamError(err), http.StatusBadGateway)
		return
	}
	if _, safe := requireSafeNativeToolEmission(w, res, nil); !safe {
		return
	}
	if _, err := s.materializeArtifacts(ctx, r, &res); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !requireUsableLegacyTextResult(w, res, body.Tools) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for i, event := range chathub.SemanticEvents(res.Events) {
		writeSSE(w, "semantic", map[string]any{"index": i, "type": "m365.semantic", "event": event})
		flusher.Flush()
	}
	turn.Observe(res)
	if err := turn.Accept(assistantTextCheckpointMessage(res.Text, res.Images)); err != nil {
		writeSSE(w, "error", map[string]any{"type": "checkpoint_error", "message": err.Error()})
		flusher.Flush()
		return
	}
	done := map[string]any{
		"type": "done", "text": res.Text, "model": resolution.ResponseModel,
		"conversationId": res.ConversationID, "sessionId": res.SessionID, "requestId": res.RequestID,
		"throttling": res.Throttling, "m365": compatM365Metadata(res, resolution),
	}
	if reasoning := chathub.ReasoningContent(res.Events); reasoning != "" {
		done["reasoning_content"] = reasoning
	}
	writeSSE(w, "done", done)
	flusher.Flush()
}

func writeSSE(w http.ResponseWriter, name string, value any) {
	b, _ := json.Marshal(value)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
}
