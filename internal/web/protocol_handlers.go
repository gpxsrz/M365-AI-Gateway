package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/google/uuid"
)

type pipeResponseWriter struct {
	h      http.Header
	w      *io.PipeWriter
	status int
}

func replayRecordedResponse(w http.ResponseWriter, recorded *httptest.ResponseRecorder) {
	for name, values := range recorded.Header() {
		w.Header()[name] = append([]string(nil), values...)
	}
	status := recorded.Code
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(recorded.Body.Bytes())
}

func (p *pipeResponseWriter) Header() http.Header { return p.h }
func (p *pipeResponseWriter) WriteHeader(n int) {
	if p.status == 0 {
		p.status = n
	}
}
func (p *pipeResponseWriter) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = 200
	}
	return p.w.Write(b)
}
func (p *pipeResponseWriter) Flush() {}

func readBoundedSSELine(reader *bufio.Reader, limit int64) (string, error) {
	var line strings.Builder
	for {
		fragment, err := reader.ReadSlice('\n')
		if int64(line.Len()+len(fragment)) > limit {
			return "", errRequestBodyTooLarge
		}
		line.Write(fragment)
		switch {
		case err == nil:
			return strings.TrimSuffix(strings.TrimSuffix(line.String(), "\n"), "\r"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && line.Len() > 0:
			return line.String(), nil
		default:
			return "", err
		}
	}
}

// streamResponsesAdapter converts the internal OpenAI SSE incrementally instead
// of buffering the entire completion in httptest.ResponseRecorder.
func (s *Server) streamResponsesAdapter(w http.ResponseWriter, r *http.Request, o oaiReq, model string, policy nativePolicySnapshot) {
	o.Stream = true
	b, _ := json.Marshal(o)
	innerContext, cancelInner := context.WithCancel(r.Context())
	defer cancelInner()
	r2 := r.Clone(innerContext)
	r2.Method = http.MethodPost
	r2.Body = io.NopCloser(bytes.NewReader(b))
	r2.ContentLength = int64(len(b))
	pr, pw := io.Pipe()
	irw := &pipeResponseWriter{h: make(http.Header), w: pw}
	innerDone := make(chan struct{})
	go func() {
		s.openaiChat(irw, r2)
		_ = pw.Close()
		close(innerDone)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	policyMetadata := withNativePolicy(map[string]any{}, policy)
	emit := func(name string, v any) {
		if event, ok := v.(map[string]any); ok {
			event["m365"] = policyMetadata
		}
		writeSSE(w, name, v)
		if flusher != nil {
			flusher.Flush()
		}
	}
	id := "resp_" + uuid.NewString()
	if execution := checkpointExecutionFrom(r.Context()); execution != nil && execution.turn != nil && execution.turn.responseID != "" {
		id = execution.turn.responseID
	}
	created := time.Now().Unix()
	emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "model": model, "output": []any{}}})

	var text strings.Builder
	var reasoning strings.Builder
	var innerError strings.Builder
	images := []string{}
	innerM365 := map[string]any{}
	var innerStreamError map[string]any
	messageID := "msg_" + uuid.NewString()
	contentID := "txt_" + uuid.NewString()
	reasoningID := "rs_" + uuid.NewString()
	textStarted := false
	messageOutputIndex := -1
	reasoningOutputIndex := -1
	nextOutputIndex := 0
	type tcState struct {
		ID, Name, Args, Type string
		ItemID               string
		OutputIndex          int
		ArgumentParts        []string
	}
	calls := map[int]*tcState{}
	reader := bufio.NewReaderSize(pr, 64<<10)
	var innerReadError error
	for {
		line, readErr := readBoundedSSELine(reader, requestBodySafetyBytes)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			innerReadError = readErr
			cancelInner()
			_ = pr.CloseWithError(readErr)
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			if strings.TrimSpace(line) != "" {
				innerError.WriteString(line)
			}
			continue
		}
		if line == "data: [DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
			continue
		}
		if metadata, ok := chunk["m365"].(map[string]any); ok {
			for key, value := range metadata {
				innerM365[key] = value
			}
		}
		if streamErr, ok := chunk["error"].(map[string]any); ok {
			innerStreamError = streamErr
			continue
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if content := responsesReasoningText(delta["reasoning_content"]); content != "" {
			if reasoningOutputIndex < 0 {
				reasoningOutputIndex = nextOutputIndex
				nextOutputIndex++
				emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": reasoningOutputIndex, "item": responsesReasoningItem(reasoningID, "", "in_progress")})
				emit("response.reasoning_summary_part.added", map[string]any{"type": "response.reasoning_summary_part.added", "output_index": reasoningOutputIndex, "item_id": reasoningID, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
			}
			reasoning.WriteString(content)
			emit("response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": reasoningOutputIndex, "item_id": reasoningID, "summary_index": 0, "delta": content})
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			text.WriteString(content)
			if !textStarted {
				textStarted = true
				messageOutputIndex = nextOutputIndex
				nextOutputIndex++
				emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": messageOutputIndex, "item": map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "in_progress", "content": []any{map[string]any{"type": "output_text", "id": contentID, "text": "", "annotations": []any{}}}}})
			}
			emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": messageOutputIndex, "content_index": 0, "item_id": messageID, "delta": content})
		}
		if rawImages, ok := delta["images"].([]any); ok {
			for _, rawImage := range rawImages {
				url, _ := rawImage.(string)
				url = strings.TrimSpace(url)
				if !chathub.IsImageURL(url) {
					continue
				}
				duplicate := false
				for _, existing := range images {
					if existing == url {
						duplicate = true
						break
					}
				}
				if !duplicate {
					images = append(images, url)
				}
			}
		}
		if rawCalls, ok := delta["tool_calls"].([]any); ok {
			for _, raw := range rawCalls {
				tc, _ := raw.(map[string]any)
				idxValue, ok := tc["index"].(float64)
				if !ok {
					continue
				}
				idx := int(idxValue)
				st := calls[idx]
				typ := "function"
				if v, ok := tc["type"].(string); ok && v == "custom" {
					typ = "custom"
				}
				if st == nil {
					prefix := "fc_"
					if typ == "custom" {
						prefix = "ctc_"
					}
					st = &tcState{ItemID: prefix + uuid.NewString(), Type: typ, OutputIndex: nextOutputIndex}
					nextOutputIndex++
					calls[idx] = st
				}
				if v, ok := tc["id"].(string); ok {
					st.ID = v
				}
				fn, _ := tc["function"].(map[string]any)
				if v, ok := fn["name"].(string); ok {
					st.Name += v
				}
				if v, ok := fn["arguments"].(string); ok {
					st.Args += v
					if v != "" {
						st.ArgumentParts = append(st.ArgumentParts, v)
					}
				}
			}
		}
	}
	<-innerDone
	if innerStreamError != nil {
		typ, _ := innerStreamError["type"].(string)
		code, _ := innerStreamError["code"].(string)
		message, _ := innerStreamError["message"].(string)
		if typ == "" {
			typ = "upstream_error"
		}
		if code == "" {
			code = "upstream_error"
		}
		if message == "" {
			message = "inner chat stream failed"
		}
		emit("response.failed", map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": id, "object": "response", "status": "failed", "model": model,
				"error": map[string]any{"type": typ, "code": code, "message": message, "http_status": http.StatusBadGateway},
			},
		})
		return
	}
	if innerReadError != nil || irw.status >= http.StatusBadRequest {
		status := irw.status
		if status == 0 {
			status = http.StatusBadGateway
		}
		typ, code, message := openAIErrorDetails([]byte(innerError.String()))
		if typ == "" {
			typ = "upstream_error"
		}
		if code == "" {
			code = "upstream_error"
		}
		if message == "" {
			message = "inner chat request failed"
		}
		emit("response.failed", map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": id, "object": "response", "status": "failed", "model": model,
				"error": map[string]any{"type": typ, "code": code, "message": message, "http_status": status},
			},
		})
		return
	}
	if len(calls) == 0 && strings.TrimSpace(text.String()) == "" && len(images) == 0 && strings.TrimSpace(reasoning.String()) == "" {
		emit("response.failed", map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id": id, "object": "response", "status": "failed", "model": model,
				"error": map[string]any{"code": "upstream_empty_response", "message": "ChatHub returned no text, tool call, or image result"},
			},
		})
		return
	}
	for _, st := range calls {
		if st == nil {
			continue
		}
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.Name) == "" {
			emit("response.failed", map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id": id, "object": "response", "status": "failed", "model": model,
					"error": map[string]any{"type": "upstream_error", "code": "invalid_tool_call_stream", "message": "tool call stream ended without a stable call_id and name"},
				},
			})
			return
		}
	}

	outputByIndex := map[int]any{}
	if reasoningOutputIndex >= 0 {
		outputByIndex[reasoningOutputIndex] = responsesReasoningItem(reasoningID, reasoning.String(), "completed")
	}
	messageTextIndex := -1
	var completedMessage map[string]any
	if textStarted || strings.TrimSpace(text.String()) != "" || len(images) > 0 {
		if messageOutputIndex < 0 {
			messageOutputIndex = nextOutputIndex
			nextOutputIndex++
		}
		content := []any{}
		if strings.TrimSpace(text.String()) != "" {
			messageTextIndex = len(content)
			content = append(content, map[string]any{"type": "output_text", "id": contentID, "text": text.String(), "annotations": []any{}})
		}
		for _, url := range images {
			content = append(content, map[string]any{"type": "output_image", "image_url": url})
		}
		inProgress := map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "in_progress", "content": content}
		if !textStarted {
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": messageOutputIndex, "item": inProgress})
			if messageTextIndex >= 0 {
				emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": messageOutputIndex, "content_index": messageTextIndex, "item_id": messageID, "delta": text.String()})
			}
		}
		completedMessage = map[string]any{"type": "message", "id": messageID, "role": "assistant", "status": "completed", "content": content}
		outputByIndex[messageOutputIndex] = completedMessage
	}

	callsByOutputIndex := map[int]*tcState{}
	for _, st := range calls {
		if st != nil {
			callsByOutputIndex[st.OutputIndex] = st
		}
	}
	for outputIndex := 0; outputIndex < nextOutputIndex; outputIndex++ {
		if outputIndex == reasoningOutputIndex {
			text := reasoning.String()
			part := map[string]any{"type": "summary_text", "text": text}
			emit("response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "output_index": outputIndex, "item_id": reasoningID, "summary_index": 0, "text": text})
			emit("response.reasoning_summary_part.done", map[string]any{"type": "response.reasoning_summary_part.done", "output_index": outputIndex, "item_id": reasoningID, "summary_index": 0, "part": part})
			emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": outputByIndex[outputIndex]})
			continue
		}
		if outputIndex == messageOutputIndex && completedMessage != nil {
			if messageTextIndex >= 0 {
				emit("response.output_text.done", map[string]any{"type": "response.output_text.done", "output_index": outputIndex, "content_index": messageTextIndex, "item_id": messageID, "text": text.String()})
			}
			emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": completedMessage})
			continue
		}
		st := callsByOutputIndex[outputIndex]
		if st == nil {
			continue
		}
		if st.Type == "custom" {
			input := customToolInput(st.Args)
			inProgress := map[string]any{"type": "custom_tool_call", "id": st.ItemID, "call_id": st.ID, "name": st.Name, "input": "", "status": "in_progress"}
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": inProgress})
			item := map[string]any{"type": "custom_tool_call", "id": st.ItemID, "call_id": st.ID, "name": st.Name, "input": input, "status": "completed"}
			outputByIndex[outputIndex] = item
			emit("response.custom_tool_call_input.delta", map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": outputIndex, "item_id": st.ItemID, "delta": input})
			emit("response.custom_tool_call_input.done", map[string]any{"type": "response.custom_tool_call_input.done", "output_index": outputIndex, "item_id": st.ItemID, "input": input})
			emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item})
			continue
		}
		inProgress := map[string]any{"type": "function_call", "id": st.ItemID, "call_id": st.ID, "name": st.Name, "arguments": "", "status": "in_progress"}
		emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": inProgress})
		for _, part := range st.ArgumentParts {
			emit("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": outputIndex, "item_id": st.ItemID, "delta": part})
		}
		item := map[string]any{"type": "function_call", "id": st.ItemID, "call_id": st.ID, "name": st.Name, "arguments": st.Args, "status": "completed"}
		outputByIndex[outputIndex] = item
		emit("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": outputIndex, "item_id": st.ItemID, "arguments": st.Args})
		emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item})
	}

	output := make([]any, 0, len(outputByIndex))
	for outputIndex := 0; outputIndex < nextOutputIndex; outputIndex++ {
		if item, ok := outputByIndex[outputIndex]; ok {
			output = append(output, item)
		}
	}
	usageOutput := text.String() + strings.Join(images, "")
	for _, call := range calls {
		usageOutput += call.Name + call.Args
	}
	estimate := estimateResponsesUsage(model, o.Messages, o.Tools, o.ToolChoice, usageOutput)
	innerM365 = withNativePolicy(innerM365, policy)
	resp := map[string]any{"id": id, "object": "response", "created_at": created, "status": "completed", "model": model, "output": output, "usage": estimate.Values, "m365": responsesM365Metadata(map[string]any{"m365": innerM365}, estimate.Source)}
	if execution := checkpointExecutionFrom(r.Context()); execution != nil {
		if err := execution.Accept(); err != nil {
			emit("response.failed", map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id": id, "object": "response", "status": "failed", "model": model,
					"error": map[string]any{"type": "checkpoint_error", "code": "checkpoint_error", "message": err.Error()},
				},
			})
			return
		}
	}
	emit("response.completed", map[string]any{"type": "response.completed", "response": resp})
}

func (s *Server) runOpenAIAdapter(r *http.Request, o oaiReq) (map[string]any, []byte, int, error) {
	o.Stream = false
	o.StreamOptions = nil
	b, _ := json.Marshal(o)
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodPost
	r2.Body = io.NopCloser(bytes.NewReader(b))
	r2.ContentLength = int64(len(b))
	rr := httptest.NewRecorder()
	s.openaiChat(rr, r2)
	var out map[string]any
	err := json.Unmarshal(rr.Body.Bytes(), &out)
	return out, rr.Body.Bytes(), rr.Code, err
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	s.serveInteractiveRequest(w, r, func(w http.ResponseWriter, status int, message string) {
		writeResponsesError(w, status, "rate_limit_error", message)
	}, s.responsesCore)
}

func (s *Server) responsesCore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeResponsesError(w, 405, "invalid_request_error", "method not allowed")
		return
	}
	settings := serverRuntimeSettings(s)
	bodyLimit, err := requestBodyLimit(settings.TextInputLimitUTF16)
	if err != nil {
		writeResponsesError(w, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	var body responsesRequest
	if err := decodeBoundedJSON(w, r, bodyLimit, &body); err != nil {
		if isRequestBodyTooLarge(err) {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		writeResponsesError(w, 400, "invalid_request_error", "bad json")
		return
	}
	setIngressEvidenceSummaryHeaders(w, summarizeResponsesIngressEvidence(body))
	for _, tool := range body.Tools {
		name, _ := tool["name"].(string)
		if err := validateReservedNativeToolName(name); err != nil {
			writeResponsesErrorCode(w, http.StatusBadRequest, "invalid_request_error", "reserved_native_tool_name", err.Error())
			return
		}
	}
	o, err := body.openAI()
	if err != nil {
		writeResponsesError(w, 400, "invalid_request_error", err.Error())
		return
	}
	downgraded, err := adapterCompatibilityParameters(o)
	if err != nil {
		writeResponsesErrorCode(w, http.StatusBadRequest, "invalid_request_error", "invalid_parameter", err.Error())
		return
	}
	setDowngradedParameters(w, downgraded)
	if err := validateCallerText(o.Messages, settings.TextInputLimitUTF16); err != nil {
		writeOpenAITextPolicyError(w, r, err)
		return
	}
	r = withCallerTextValidated(r)
	nativePolicy, err := resolveResponsesNativePolicy(settings.ToolPlanningMode, o.Tools)
	if err != nil {
		if errors.Is(err, errReservedNativeToolName) {
			writeResponsesErrorCode(w, http.StatusBadRequest, "invalid_request_error", "reserved_native_tool_name", err.Error())
			return
		}
		writeResponsesErrorCode(w, http.StatusServiceUnavailable, "configuration_error", "invalid_native_policy", err.Error())
		return
	}
	nativePolicy = withSidecarExecutionEnforcement(nativePolicy)
	effort := o.ReasoningEffort
	if o.Reasoning != nil && strings.TrimSpace(o.Reasoning.Effort) != "" {
		effort = o.Reasoning.Effort
	}
	if _, routeErr := resolveRouteForSettings(o.Model, effort, settings); routeErr != nil {
		if typed, ok := routeErr.(*routeResolveError); ok {
			writeResponsesErrorCode(w, typed.Status, "invalid_request_error", typed.Code, typed.Message)
		} else {
			writeResponsesError(w, http.StatusBadRequest, "invalid_request_error", routeErr.Error())
		}
		return
	}
	if strings.TrimSpace(body.PreviousResponseID) != "" && strings.TrimSpace(body.Conversation) != "" {
		writeResponsesErrorCode(w, http.StatusBadRequest, "invalid_request_error", "conflicting_continuation", "previous_response_id and conversation are mutually exclusive")
		return
	}
	publicID := "resp_" + uuid.NewString()
	control := checkpointRequestControl{Namespace: "responses", ResponseID: publicID, ForceNew: body.NewConversation}
	switch {
	case body.PreviousResponseID != "":
		control.Mode = checkpointResponseParent
		control.ParentID = body.PreviousResponseID
	case strings.TrimSpace(body.Conversation) != "":
		control.Mode = checkpointAppendOnly
		control.Key = body.Conversation
	default:
		control.Mode = checkpointFullHistory
		control.ForceNew = true
	}
	checkpointContext := withCheckpointRequest(r.Context(), control)
	checkpointTurn, checkpointErr := s.beginOpenAICheckpoint(checkpointContext, &o)
	if checkpointErr != nil {
		status := http.StatusConflict
		if errors.Is(checkpointErr, ErrCheckpointUnknownCursor) || errors.Is(checkpointErr, ErrCheckpointNotFound) {
			status = http.StatusBadRequest
		}
		writeResponsesError(w, status, "checkpoint_error", checkpointErr.Error())
		return
	}
	execution := &checkpointExecution{turn: checkpointTurn}
	defer execution.Abort()
	r = r.WithContext(withCheckpointExecution(checkpointContext, execution))
	if body.Stream {
		s.streamResponsesAdapter(w, r, o, firstNonEmpty(body.Model, "m365-copilot"), nativePolicy)
		return
	}
	out, raw, status, err := s.runOpenAIAdapter(r, o)
	if status >= 400 {
		if errBody := openAIErrorObject(raw); errBody != nil {
			writeOpenAIErrorObject(w, status, errBody)
			return
		}
		writeResponsesError(w, status, "upstream_error", errorMessage(raw, "upstream protocol error"))
		return
	}
	if err != nil {
		writeResponsesError(w, http.StatusBadGateway, "upstream_error", "upstream protocol error: "+err.Error())
		return
	}
	if !responsesOutputHasContent(out) {
		writeResponsesErrorCode(w, http.StatusBadGateway, "upstream_error", "upstream_empty_response", "ChatHub returned an empty response; no reusable message was created")
		return
	}
	metadata, _ := out["m365"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	out["m365"] = withNativePolicy(metadata, nativePolicy)
	msg, _ := openAIChoice(out)
	outputForUsage := ""
	if msg != nil {
		outputForUsage = fmt.Sprint(msg["content"])
		if calls, ok := msg["tool_calls"].([]any); ok {
			outputForUsage += fmt.Sprint(calls)
		}
	}
	estimate := estimateResponsesUsage(firstNonEmpty(body.Model, "m365-copilot"), o.Messages, o.Tools, o.ToolChoice, outputForUsage)
	out["usage"] = estimate.Values
	out["m365_usage_source"] = estimate.Source
	if _, ok := out["id"].(string); ok {
		out["m365_response_id"] = publicID
	}
	if err := execution.Accept(); err != nil {
		writeResponsesError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
		return
	}
	writeResponsesResult(w, firstNonEmpty(body.Model, "m365-copilot"), body.Stream, out)
}

func responsesOutputHasContent(src map[string]any) bool {
	msg, _ := openAIChoice(src)
	if msg == nil {
		return false
	}
	if calls, ok := msg["tool_calls"].([]any); ok && len(calls) > 0 {
		return true
	}
	if responsesReasoningText(msg["reasoning_content"]) != "" {
		return true
	}
	return len(responsesMessageContentBlocks(msg["content"])) > 0
}

func (s *Server) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.serveInteractiveRequest(w, r, func(w http.ResponseWriter, status int, message string) {
		writeAnthropicError(w, status, "rate_limit_error", message)
	}, s.anthropicMessagesCore)
}

func (s *Server) anthropicMessagesCore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, 405, "invalid_request_error", "method not allowed")
		return
	}
	settings := serverRuntimeSettings(s)
	bodyLimit, err := requestBodyLimit(settings.TextInputLimitUTF16)
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	var body anthropicRequest
	if err := decodeBoundedJSON(w, r, bodyLimit, &body); err != nil {
		if isRequestBodyTooLarge(err) {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		writeAnthropicError(w, 400, "invalid_request_error", "bad json")
		return
	}
	setIngressEvidenceSummaryHeaders(w, summarizeAnthropicIngressEvidence(body))
	if body.Stream {
		setStreamingSemantics(w, "posthoc-adapter")
	}
	if body.MaxTokens > 0 {
		setIgnoredParameters(w, []string{"max_tokens"})
	}
	for _, tool := range body.Tools {
		if err := validateReservedNativeToolName(tool.Name); err != nil {
			writeAnthropicErrorCode(w, http.StatusBadRequest, "invalid_request_error", "reserved_native_tool_name", err.Error())
			return
		}
	}
	o, err := body.openAI()
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", err.Error())
		return
	}
	checkpointContext := withCheckpointRequest(r.Context(), checkpointRequestControl{Mode: checkpointFullHistory, Namespace: "anthropic"})
	checkpointTurn, checkpointErr := s.beginOpenAICheckpoint(checkpointContext, &o, func(messages []oaiMsg) error {
		return validateCallerText(messages, settings.TextInputLimitUTF16)
	})
	if checkpointErr != nil {
		var textLimitErr *callerTextLimitError
		if errors.As(checkpointErr, &textLimitErr) {
			writeOpenAITextPolicyError(w, r, checkpointErr)
			return
		}
		writeAnthropicError(w, http.StatusConflict, "checkpoint_error", checkpointErr.Error())
		return
	}
	execution := &checkpointExecution{turn: checkpointTurn}
	defer execution.Abort()
	r = withCallerTextValidated(r)
	r = r.WithContext(withCheckpointExecution(r.Context(), execution))
	out, raw, status, err := s.runOpenAIAdapter(r, o)
	if status >= 400 {
		if errBody := openAIErrorObject(raw); errBody != nil {
			writeAnthropicErrorObject(w, status, errBody)
			return
		}
		writeAnthropicError(w, status, "api_error", errorMessage(raw, "upstream protocol error"))
		return
	}
	if err != nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream protocol error: "+err.Error())
		return
	}
	projection, projectionErr := projectAnthropicResult(out)
	if projectionErr != nil {
		writeAnthropicErrorCode(w, http.StatusBadGateway, "api_error", "unsupported_content", projectionErr.Error())
		return
	}
	if err := execution.Accept(); err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
		return
	}
	writeAnthropicProjection(w, firstNonEmpty(body.Model, "m365-copilot"), body.Stream, out, projection)
}
