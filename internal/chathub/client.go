package chathub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"m365-native/internal/outbound"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func foldStreamText(current, update string, cumulative bool) (next, delta string) {
	if update == "" {
		return current, ""
	}
	if !cumulative {
		return current + update, update
	}
	if current == "" {
		return update, update
	}
	if strings.HasPrefix(update, current) {
		return update, strings.TrimPrefix(update, current)
	}
	if strings.HasPrefix(current, update) {
		return current, ""
	}
	// A divergent snapshot is ambiguous. Keep the longest stable text already
	// observed rather than duplicating or replacing it with an unrelated frame.
	return current, ""
}

const (
	rs          = "\x1e"
	defaultTone = "magic"
	wsBase      = "wss://substrate.office.com/m365Copilot/Chathub"
)

// Variants mirrored from the verified browser / Python probe.
const variants = "EnableMcpServerWidgets,feature.EnableMcpServerWidgets,feature.EnableLuForChatCIQ,feature.enableChatCIQPlugin,EnableRequestPlugins,feature.EnableSensitivityLabels,EnableUnsupportedUrlDetector,feature.IsCustomEngineCopilotEnabled,feature.bizchatfluxv3,feature.enablechatpages,feature.enableCodeCanvas,feature.turnOnWorkTabRecommendation,turnOffWorkTabUpsellFromClient,feature.turnOnDARecommendation,feature.IsStreamingModeInChatRequestEnabled,IncludeSourceAttributionsConcise,SkipPublishEmptyMessage,feature.EnableDeduplicatingSourceAttributions,Enable3PActionProgressMessages,feature.enableClientWebRtc,feature.EnableMeetingRecapOfSeriesMeetingWithCiq,feature.EnableReferencesListCompleteSignal,feature.StorageMessageSplitDisabled,feature.EnableCuaTakeControlApi,feature.cwcallowedos,feature.disabledisallowedmsgs,feature.enableCitationsForSynthesisData,feature.enableGenerateGraphicArtOptionsSet,cdximagen,feature.EnableUpdatedUXForConfirmationDialog,feature.EnableClientFileURLSupportForOfficeWebPaidCopilot,feature.EnableDesignEditorImageGrounding,feature.EnableDesignerEditor,feature.OfficeWebToHelix,feature.OfficeDesktopToHelix,feature.M365TeamsHubToHelix,feature.OwaHubToHelix,feature.MonarchHubToHelix,feature.Win32OutlookHubToHelix,feature.MacOutlookHubToHelix,Agt_bizchat_enableGpt5ForHelix"

type Account struct {
	AccessToken      string
	GraphAccessToken string
	OID              string
	TID              string
}

type Request struct {
	Text                 string
	Tone                 string
	ConversationID       string
	SessionID            string
	Attachments          []Attachment
	Tools                []Tool
	ToolChoice           any
	ToolCallLimit        int
	MCPServerURL         string // URL of the MCP HTTP SSE server for tool discovery
	DisableBuiltInSearch bool   // internal compatibility path: omit Bing/native search from this request
	// Started is true only for the first turn of a ChatHub conversation.
	Started bool
}

// StreamEvent is the protocol-neutral event exposed while ChatHub is still
// producing a response. Text events are safe to show immediately; progress and
// tool events are normally buffered by protocol adapters.
type StreamEvent struct {
	Kind        string
	Text        string
	MessageType string
	ContentType string
	ToolName    string
	Arguments   json.RawMessage
	Raw         json.RawMessage
}

type StreamHandler func(StreamEvent) error

type Result struct {
	Text           string
	ConversationID string
	SessionID      string
	RequestID      string
	Throttling     any
	RawResult      string
	Events         []json.RawMessage
	Normalized     []Event
	Images         []string
	Artifacts      []Artifact
	Attributions   []Attribution
	UnknownEvents  []Event
	Terminal       TerminalState
}

type Client struct {
	HTTPHeader http.Header
	HTTPClient *http.Client
	Dialer     *websocket.Dialer
	// PrivateMode is evaluated for every new WebSocket. A nil callback fails
	// private so reconnects and isolated callers cannot silently create history.
	PrivateMode func() bool
	// Trace receives attachment-only metadata; URL contents are never exposed.
	Trace func(map[string]any)
	// AttachmentNameSuffix is a deterministic test seam. Production uses a
	// fresh cryptographically random suffix for every new document upload.
	AttachmentNameSuffix func() (string, error)
	// These narrow test seams let the downloader resolve once, validate every
	// address, then dial one of those exact IPs with the original TLS name.
	ResolveAttachmentIPs func(context.Context, string) ([]net.IP, error)
	PinnedHTTPSClient    func(string) *http.Client
}

func NewClient() *Client {
	h := make(http.Header)
	h.Set("Origin", "https://m365.cloud.microsoft")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0")
	return &Client{
		HTTPHeader:        h,
		HTTPClient:        outbound.HTTPClient(),
		Dialer:            outbound.WebSocketDialer(),
		PinnedHTTPSClient: outbound.PinnedHTTPSClient,
	}
}

func (c *Client) Chat(ctx context.Context, acc Account, req Request) (Result, error) {
	return c.ChatWithDelta(ctx, acc, req, nil)
}

func (c *Client) privateModeEnabled() bool {
	if c.PrivateMode == nil {
		return true
	}
	return c.PrivateMode()
}

const (
	webSocketDialMaxAttempts = 2
	webSocketDialRetryDelay  = 100 * time.Millisecond
)

func webSocketDialFailure(status int, retryAfter string, err error) error {
	if status == http.StatusTooManyRequests {
		return &RateLimitError{StatusCode: status, RetryAfter: normalizeRetryAfter(retryAfter), Err: err}
	}
	if status > 0 {
		return fmt.Errorf("ws dial failed: HTTP %d: %w", status, err)
	}
	return fmt.Errorf("ws dial: %w", err)
}

func retryableWebSocketDialFailure(status int, err error) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case 0:
		var networkError net.Error
		return errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	default:
		return false
	}
}

func waitWebSocketDialRetry(ctx context.Context) error {
	timer := time.NewTimer(webSocketDialRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) dialChatWebSocket(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	for attempt := 1; attempt <= webSocketDialMaxAttempts; attempt++ {
		conn, response, err := c.Dialer.DialContext(ctx, wsURL, c.HTTPHeader.Clone())
		if err == nil {
			return conn, nil
		}
		status := 0
		retryAfter := ""
		if response != nil {
			status = response.StatusCode
			retryAfter = response.Header.Get("Retry-After")
			if response.Body != nil {
				_ = response.Body.Close()
			}
		}
		if contextErr := callerContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		failure := webSocketDialFailure(status, retryAfter, err)
		if attempt == webSocketDialMaxAttempts || !retryableWebSocketDialFailure(status, err) {
			log.Printf("chathub ws_dial_failed status=%d attempt=%d", status, attempt)
			return nil, failure
		}
		log.Printf("chathub ws_dial_retry status=%d attempt=%d next_attempt=%d", status, attempt, attempt+1)
		if err := waitWebSocketDialRetry(ctx); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("ws dial failed without a terminal result")
}

func normalizeRetryAfter(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if seconds, err := strconv.ParseUint(value, 10, 63); err == nil {
		return strconv.FormatUint(seconds, 10)
	}
	if when, err := http.ParseTime(value); err == nil {
		return when.UTC().Format(http.TimeFormat)
	}
	return ""
}

func callerContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if networkError, ok := err.(net.Error); ok && networkError.Timeout() {
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return nil
}

// ChatWithEvents is the compatibility entry point for the full event stream.
// The initial implementation exposes every upstream text delta immediately;
// the existing ChatWithDelta path remains the source of truth until the
// SignalR frame parser is migrated to emit progress/tool events as well.
func (c *Client) ChatWithEvents(ctx context.Context, acc Account, req Request, handler StreamHandler) (Result, error) {
	return c.chatWithHandlers(ctx, acc, req, func(text string) error {
		if handler == nil {
			return nil
		}
		return handler(StreamEvent{Kind: "text", Text: text})
	}, handler)
}

// ChatWithDelta preserves Chat semantics while exposing upstream text deltas as
// soon as SignalR delivers them. onDelta must return quickly; returning an error
// cancels the request. Full snapshot messages are retained for final-result
// reconstruction but are not emitted as deltas, preventing duplicate text.
func (c *Client) ChatWithDelta(ctx context.Context, acc Account, req Request, onDelta func(string) error) (Result, error) {
	return c.chatWithHandlers(ctx, acc, req, onDelta, nil)
}

func (c *Client) chatWithHandlers(ctx context.Context, acc Account, req Request, onDelta func(string) error, onEvent StreamHandler) (Result, error) {
	startedAt := time.Now()
	log.Printf("chathub timing start prompt_len=%d", len(req.Text))
	if acc.AccessToken == "" || acc.OID == "" || acc.TID == "" {
		return Result{}, fmt.Errorf("missing access token / oid / tid")
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return Result{}, fmt.Errorf("empty prompt and no attachments")
	}
	if req.Tone == "" {
		req.Tone = defaultTone
	}
	firstTurn := req.Started
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
		firstTurn = true
	}
	if req.ConversationID == "" {
		req.ConversationID = uuid.NewString()
		firstTurn = true
	}
	requestID := uuid.NewString()
	if err := c.uploadAttachments(ctx, acc, req.ConversationID, req.Attachments); err != nil {
		return Result{}, fmt.Errorf("upload attachment: %w", err)
	}

	privateMode := c.privateModeEnabled()
	wsURL, err := buildWSURL(acc, req.SessionID, req.ConversationID, requestID, privateMode)
	if err != nil {
		return Result{}, err
	}

	dialStarted := time.Now()
	conn, err := c.dialChatWebSocket(ctx, wsURL)
	log.Printf("chathub timing ws_dial_ms=%d total_ms=%d", time.Since(dialStarted).Milliseconds(), time.Since(startedAt).Milliseconds())
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	stopContextClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopContextClose()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return Result{}, fmt.Errorf("set chat read deadline: %w", err)
		}
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return Result{}, fmt.Errorf("set chat write deadline: %w", err)
		}
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"protocol":"json","version":1}`+rs)); err != nil {
		if ctxErr := callerContextError(ctx, err); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("handshake send: %w", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		if ctxErr := callerContextError(ctx, err); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("handshake recv: %w", err)
	}

	payload := chatPayload(req.Text, req.SessionID, req.ConversationID, requestID, req.Tone, firstTurn, req.Attachments, req.Tools, req.ToolChoice, req.ToolCallLimit, req.MCPServerURL, req.DisableBuiltInSearch)
	log.Printf("chathub prompt-trace text=%d tools=%d payload=%d", len(req.Text), len(req.Tools), len(payload))
	if c.Trace != nil {
		dataURLCount := 0
		for _, attachment := range req.Attachments {
			if strings.HasPrefix(attachment.URL, "data:") {
				dataURLCount++
			}
		}
		c.Trace(map[string]any{
			"stage":                     "chathub_payload",
			"attachment_count":          len(req.Attachments),
			"data_url_attachment_count": dataURLCount,
			"payload_has_attachments":   strings.Contains(payload, `"attachments"`),
			"private_mode":              privateMode,
		})
	}
	log.Printf("chathub timing handshake_ms=%d", time.Since(dialStarted).Milliseconds())
	payloadSentAt := time.Now()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		if ctxErr := callerContextError(ctx, err); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("chat send: %w", err)
	}

	var streamedText string
	emitText := func(update string, cumulative bool) error {
		next, delta := foldStreamText(streamedText, update, cumulative)
		if delta == "" {
			return nil
		}
		if streamedText == "" {
			log.Printf("chathub timing first_delta_ms=%d len=%d", time.Since(payloadSentAt).Milliseconds(), len(delta))
		}
		streamedText = next
		if onDelta != nil {
			return onDelta(delta)
		}
		return nil
	}
	var final string
	var throttling any
	var rawResult string
	var events []json.RawMessage
	seenStreamTools := map[string]bool{}

	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctxErr := callerContextError(ctx, err); ctxErr != nil {
				return Result{}, ctxErr
			}
			// Never convert a timeout or dropped WebSocket into a successful
			// partial response. A response is complete only after SignalR type 3.
			return Result{}, fmt.Errorf("ws read before completion: %w", err)
		}
		for _, part := range strings.Split(string(msg), rs) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			events = append(events, json.RawMessage(append([]byte(nil), part...)))
			var obj map[string]any
			if err := json.Unmarshal([]byte(part), &obj); err != nil {
				continue
			}
			t, _ := obj["type"].(float64)
			target, _ := obj["target"].(string)

			// SignalR ping
			if int(t) == 6 {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":6}`+rs))
				continue
			}

			if int(t) == 1 && target == "update" {
				args, _ := obj["arguments"].([]any)
				for _, raw := range args {
					arg, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					msgs, _ := arg["messages"].([]any)
					if onEvent != nil {
						for _, ev := range extractToolEvents(arg, seenStreamTools) {
							if err := onEvent(ev); err != nil {
								return Result{}, err
							}
						}

						for _, ev := range classifyUpdateMessages(msgs) {
							if !artifactBearingMap(arg) {
								ev.Raw = eventRaw(arg)
							}
							if ev.Kind != "text" {
								if err := onEvent(ev); err != nil {
									return Result{}, err
								}
							}
						}
					}
					toolFrame := false
					for _, mraw := range msgs {
						m, _ := mraw.(map[string]any)
						mt, _ := m["messageType"].(string)
						ct, _ := m["contentType"].(string)
						if mt == "Progress" || ct == "SearchResults" || ct == "Code" || ct == "ToolCall" {
							toolFrame = true
						}
					}
					if w, ok := arg["writeAtCursor"].(string); ok && w != "" && !toolFrame {
						if err := emitText(w, false); err != nil {
							return Result{}, err
						}
					}
					if thr, ok := arg["throttling"]; ok {
						throttling = thr
					}
					if msgs, ok := arg["messages"].([]any); ok {
						for _, mraw := range msgs {
							m, ok := mraw.(map[string]any)
							if !ok {
								continue
							}
							author, _ := m["author"].(string)
							text, _ := m["text"].(string)
							mt, _ := m["messageType"].(string)
							if author == "bot" && mt == "" && text != "" {
								// ChatHub often sends the first visible text as a full snapshot,
								// followed by cursor deltas. Emit only the unseen suffix.
								if err := emitText(text, true); err != nil {
									return Result{}, err
								}
							}
						}
					}
				}
				continue
			}

			if int(t) == 2 {
				item, _ := obj["item"].(map[string]any)
				if item != nil {
					if thr, ok := item["throttling"]; ok {
						throttling = thr
					}
					if res, ok := item["result"].(map[string]any); ok {
						rawResult, _ = res["value"].(string)
						if msg, ok := res["message"].(string); ok {
							final = msg
						}
					}
				}
				// completion frame often follows; keep reading a bit but we already have content
				continue
			}

			if int(t) == 3 {
				event := normalize(json.RawMessage(part))
				if event.ErrorText != "" {
					state := TerminalState{Kind: "error", Error: event.ErrorText}
					result := Result{ConversationID: req.ConversationID, SessionID: req.SessionID, RequestID: requestID, Events: events, Terminal: state}
					if err := CanonicalizeResult(&result); err != nil {
						return result, err
					}
					return result, &TerminalError{State: result.Terminal}
				}
				// end of stream
				log.Printf("chathub timing completion_frame_ms=%d streamed_text=%d events=%d", time.Since(payloadSentAt).Milliseconds(), len(streamedText), len(events))
				text := final
				if text == "" {
					text = streamedText
				}
				result := Result{
					Text:           text,
					ConversationID: req.ConversationID,
					SessionID:      req.SessionID,
					RequestID:      requestID,
					Throttling:     throttling,
					RawResult:      rawResult,
					Events:         events,
					Images:         imageURLs(events),
					Terminal:       TerminalState{Kind: "complete"},
				}
				if err := CanonicalizeResult(&result); err != nil {
					return result, err
				}
				return result, nil
			}

			if int(t) == 7 {
				event := normalize(json.RawMessage(part))
				state := TerminalState{Kind: "close", Error: event.ErrorText, AllowReconnect: event.AllowReconnect}
				result := Result{ConversationID: req.ConversationID, SessionID: req.SessionID, RequestID: requestID, Events: events, Terminal: state}
				if err := CanonicalizeResult(&result); err != nil {
					return result, err
				}
				return result, &TerminalError{State: result.Terminal}
			}
		}
	}
}

func buildWSURL(acc Account, sessionID, conversationID, requestID string, private bool) (string, error) {
	q := url.Values{}
	q.Set("chatsessionid", requestID)
	q.Set("clientrequestid", requestID)
	q.Set("X-SessionId", sessionID)
	q.Set("ConversationId", conversationID)
	q.Set("access_token", acc.AccessToken)
	q.Set("variants", variants)
	// source must keep quotes like the browser probe
	q.Set("source", `"officeweb"`)
	q.Set("product", "Office")
	q.Set("agentHost", "Bizchat.FullScreen")
	q.Set("licenseType", "Starter")
	q.Set("agent", "web")
	q.Set("scenario", "OfficeWebIncludedCopilot")
	if private {
		q.Set("disableMemory", "1")
	}

	// url.Values encodes quotes; probe used safe='",' so keep quotes unescaped-ish.
	// Gorilla/url will encode " to %22 which MS accepts.
	u := fmt.Sprintf("%s/%s@%s?%s", wsBase, acc.OID, acc.TID, q.Encode())
	return u, nil
}

func (c *Client) uploadAttachments(ctx context.Context, acc Account, conversationID string, attachments []Attachment) error {
	if len(attachments) > maxActiveAttachments {
		return fmt.Errorf("too many active attachments: limit is %d", maxActiveAttachments)
	}
	for i := range attachments {
		a := &attachments[i]
		switch a.Type {
		case "file":
			if a.DocID != "" && a.ReferenceURL != "" && a.UploadedConversationID == conversationID {
				continue
			}
			a.DocID, a.ReferenceURL, a.TransportName, a.UploadedConversationID = "", "", "", ""
			if err := c.uploadDocument(ctx, acc, conversationID, a); err != nil {
				return err
			}
			continue
		case "image":
			if a.DocID != "" && a.UploadedConversationID == conversationID {
				continue
			}
			a.DocID, a.FileType, a.UploadedConversationID = "", "", ""
			if err := c.uploadImage(ctx, acc, conversationID, i, a); err != nil {
				return err
			}
			continue
		case "":
			return fmt.Errorf("attachment type is required")
		default:
			return fmt.Errorf("unsupported attachment type %q", a.Type)
		}
	}
	return nil
}

func chatPayload(text, sessionID, conversationID, requestID, tone string, firstTurn bool, attachments []Attachment, tools []Tool, toolChoice any, toolCallLimit int, mcpServerURL string, disableBuiltInSearch ...bool) string {
	text = toolProtocolPrompt(text, tools, toolChoice, toolCallLimit)
	message := map[string]any{
		"author":                "user",
		"inputMethod":           "Keyboard",
		"text":                  text,
		"entityAnnotationTypes": []string{"People", "File", "Event", "Email", "TeamsMessage"},
		"requestId":             requestID,
		"locationInfo": map[string]any{
			"timeZoneOffset": 8,
			"timeZone":       "Asia/Shanghai",
		},
		"locale":            "zh-cn",
		"messageType":       "Chat",
		"experienceType":    "Default",
		"adaptiveCards":     []any{},
		"clientPreferences": map[string]any{},
	}
	// The browser does not send an OpenAI attachments array to ChatHub. It
	// sends a file annotation after the file has been uploaded by Office.
	annotations := make([]any, 0, len(attachments))
	for _, a := range attachments {
		if a.DocID == "" || a.UploadedConversationID != conversationID {
			continue
		}
		if a.Type == "file" {
			if a.TransportName != "" && a.ReferenceURL != "" {
				annotations = append(annotations, map[string]any{
					"id": a.DocID, "text": a.TransportName, "url": a.ReferenceURL,
					"messageAnnotationType": "LocalFile",
				})
			}
			continue
		}
		if a.Type != "image" {
			continue
		}
		if a.Name == "" {
			a.Name = "image." + a.FileType
		}
		fileType := a.FileType
		if fileType == "" {
			fileType = strings.TrimPrefix(strings.ToLower(a.MimeType), "image/")
		}
		if fileType == "" || fileType == "image" || fileType == "*" {
			fileType = "jpg"
		}
		annotations = append(annotations, map[string]any{
			"id": a.DocID,
			"messageAnnotationMetadata": map[string]any{
				"@type": "File", "annotationType": "File",
				"fileType": fileType, "fileName": a.Name,
			},
			"messageAnnotationType": "ImageFile",
		})
	}
	if len(annotations) > 0 {
		message["messageAnnotations"] = annotations
		message["connectedFederatedConnections"] = []string{"dummyId"}
	}
	optionsSets := []any{
		"search_result_progress_messages_with_search_queries",
		"update_textdoc_response_after_streaming",
		"deepleo_networking_timeout_10minutes_canmore",
		"cwc_flux_image",
		"cwc_code_interpreter",
		"cwc_code_interpreter_amsfix",
		"cwcfluxgptv",
		"flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch",
		"gptvnorm2048",
		"cwc_code_interpreter_citation_fix",
		"code_interpreter_interactive_charts_inline_image",
		"code_interpreter_matplotlib_patching",
		"code_interpreter_interactive_charts",
		"cwc_fileupload_odb",
		"update_memory_plugin",
		"add_custom_instructions",
		"cwc_flux_v3",
		"flux_v3_progress_messages",
		"enable_batch_token_processing",
		"enable_gg_gpt",
	}
	searchDisabled := len(disableBuiltInSearch) > 0 && disableBuiltInSearch[0]
	chat := map[string]any{
		"arguments": []any{
			map[string]any{
				"source":              "officeweb",
				"clientCorrelationId": uuid.NewString(),
				"sessionId":           sessionID,
				"optionsSets":         optionsSets,
				"options":             map[string]any{},
				"allowedMessageTypes": []string{
					"Chat", "Suggestion", "Disengaged", "Progress", "EndOfRequest", "InternalLoaderMessage",
					"GeneratedCode", "GenerateContentQuery", "ReferencesListComplete", "RenderCardRequest",
					"SearchQuery", "InternalSearchQuery", "SemanticSerp", "AuthError",
				},
				"sliceIds":          []any{},
				"threadLevelGptId":  map[string]any{},
				"conversationId":    conversationID,
				"traceId":           uuid.NewString(),
				"isStartOfSession":  firstTurn,
				"productThreadType": "Office",
				"clientInfo": map[string]any{
					"clientPlatform": "mcmcopilot-web",
					"clientAppName":  "Office",
				},
				"tone":          tone,
				"streamingMode": "ConciseWithPadding",
				"message":       message,

				"plugins":    clientPlugins(callerToolsForChoice(tools, toolChoice), callerMCPForChoice(mcpServerURL, toolChoice), searchDisabled),
				"toolChoice": toolChoice,
			},
		},
		"invocationId": "0",
		"target":       "chat",
		"type":         4,
	}
	metrics := map[string]any{
		"arguments": []any{
			map[string]any{
				"Timestamps": map[string]string{
					"ConnectionStart":       "",
					"UserInputStart":        "",
					"ConnectionEstablished": "",
					"UserInputSubmit":       "",
				},
			},
		},
		"target": "Metrics",
		"type":   1,
	}
	b1, _ := json.Marshal(chat)
	b2, _ := json.Marshal(metrics)
	return string(b1) + rs + string(b2) + rs
}
