package mcp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	LatestProtocolVersion = "2025-11-25"
	SessionHeader         = "Mcp-Session-Id"
	ProtocolHeader        = "MCP-Protocol-Version"

	defaultMaxSessions          = 128
	defaultMaxPendingPerSession = 64
	defaultMaxMessageBytes      = int64(8 << 20)
	defaultSessionTTL           = 30 * time.Minute
	defaultCallTimeout          = 30 * time.Second
)

var supportedProtocolVersions = map[string]struct{}{
	"2024-11-05":          {},
	"2025-03-26":          {},
	"2025-06-18":          {},
	LatestProtocolVersion: {},
}

var errMCPServerClosed = errors.New("MCP server closed")

// ToolProvider is the one logical tool registry/dispatch seam shared by all
// transports. MCP tools remain caller-side tools; this interface does not
// represent Microsoft-native Bing, Code Interpreter, or Files capabilities.
type ToolProvider interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error)
}

type ServerOptions struct {
	Provider             ToolProvider
	OriginPolicy         func(*http.Request) error
	MaxSessions          int
	MaxPendingPerSession int
	MaxMessageBytes      int64
	SessionTTL           time.Duration
	CallTimeout          time.Duration
	Now                  func() time.Time
}

type Server struct {
	mu                   sync.Mutex
	sessions             map[string]*session
	closed               bool
	done                 chan struct{}
	providerSlots        chan struct{}
	provider             ToolProvider
	originPolicy         func(*http.Request) error
	maxSessions          int
	maxPendingPerSession int
	maxMessageBytes      int64
	sessionTTL           time.Duration
	callTimeout          time.Duration
	now                  func() time.Time
}

type session struct {
	mu          sync.Mutex
	id          string
	owner       string
	protocol    string
	initialized bool
	legacy      bool
	provider    ToolProvider
	createdAt   time.Time
	lastUsedAt  time.Time
	messages    chan []byte
	closed      chan struct{}
	closeOnce   sync.Once
	inflight    map[string]context.CancelFunc
	cancelled   map[string]struct{}
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewServer(options ServerOptions) *Server {
	provider := options.Provider
	if provider == nil {
		provider = emptyToolProvider{}
	}
	if options.MaxSessions <= 0 {
		options.MaxSessions = defaultMaxSessions
	}
	if options.MaxPendingPerSession <= 0 {
		options.MaxPendingPerSession = defaultMaxPendingPerSession
	}
	if options.MaxMessageBytes <= 0 {
		options.MaxMessageBytes = defaultMaxMessageBytes
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = defaultSessionTTL
	}
	if options.CallTimeout <= 0 {
		options.CallTimeout = defaultCallTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.OriginPolicy == nil {
		options.OriginPolicy = rejectBrowserOrigin
	}
	return &Server{
		sessions:             make(map[string]*session),
		done:                 make(chan struct{}),
		providerSlots:        make(chan struct{}, options.MaxPendingPerSession),
		provider:             provider,
		originPolicy:         options.OriginPolicy,
		maxSessions:          options.MaxSessions,
		maxPendingPerSession: options.MaxPendingPerSession,
		maxMessageBytes:      options.MaxMessageBytes,
		sessionTTL:           options.SessionTTL,
		callTimeout:          options.CallTimeout,
		now:                  options.Now,
	}
}

func (s *Server) ServeStreamableHTTP(w http.ResponseWriter, r *http.Request, owner string) {
	if !s.allowRequest(w, r, owner) {
		return
	}
	if modernProtocolRequest(r) {
		s.serveOfficialModern(w, r)
		return
	}
	switch r.Method {
	case http.MethodHead:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		s.serveStreamablePost(w, r, owner)
	case http.MethodGet:
		w.Header().Set("Allow", "HEAD, POST, DELETE")
		writeHTTPError(w, http.StatusMethodNotAllowed, "server-initiated streams are not supported")
	case http.MethodDelete:
		s.serveStreamableDelete(w, r, owner)
	default:
		w.Header().Set("Allow", "HEAD, POST, DELETE")
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) ServeLegacySSE(w http.ResponseWriter, r *http.Request, owner string) {
	if !s.allowRequest(w, r, owner) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	sess, err := s.newSession(owner, "", true)
	if err != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "MCP session capacity reached")
		return
	}
	defer s.removeSession(sess.id, owner)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /v1/mcp/message?sessionId=%s\n\n", url.QueryEscape(sess.id))
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sess.closed:
			return
		case message := <-sess.messages:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", message)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) ServeLegacyMessage(w http.ResponseWriter, r *http.Request, owner string) {
	if !s.allowRequest(w, r, owner) {
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeHTTPError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	sessionValues := r.URL.Query()["sessionId"]
	if len(sessionValues) != 1 {
		writeHTTPError(w, http.StatusBadRequest, "sessionId required")
		return
	}
	sessionID := strings.TrimSpace(sessionValues[0])
	if sessionID == "" {
		writeHTTPError(w, http.StatusBadRequest, "sessionId required")
		return
	}
	sess := s.lookupSession(sessionID, owner)
	if sess == nil || !sess.legacy {
		writeHTTPError(w, http.StatusNotFound, "MCP session not found")
		return
	}
	request, status, err := s.readRequest(r)
	if err != nil {
		response := newRPCError(json.RawMessage("null"), rpcCodeForReadError(status), rpcMessageForReadError(status))
		s.sendLegacyResponse(w, r, sess, response, status)
		return
	}
	response := s.dispatch(r.Context(), sess, request)
	if response == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.sendLegacyResponse(w, r, sess, response, http.StatusAccepted)
}

func (s *Server) SessionCount() int {
	s.mu.Lock()
	expired := s.cleanupExpiredLocked(s.now())
	count := len(s.sessions)
	s.mu.Unlock()
	closeSessions(expired)
	return count
}

func (s *Server) PendingCount() int {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	count := 0
	for _, sess := range sessions {
		sess.mu.Lock()
		count += len(sess.inflight)
		sess.mu.Unlock()
	}
	return count
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	sessions := make([]*session, 0, len(s.sessions))
	for id, sess := range s.sessions {
		delete(s.sessions, id)
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	closeSessions(sessions)
}

func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request, owner string) bool {
	if strings.TrimSpace(owner) == "" {
		writeHTTPError(w, http.StatusUnauthorized, "valid API key required")
		return false
	}
	if err := s.originPolicy(r); err != nil {
		writeHTTPError(w, http.StatusForbidden, "invalid Origin")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	return true
}

func rejectBrowserOrigin(r *http.Request) error {
	if len(r.Header.Values("Origin")) == 0 {
		return nil
	}
	return errors.New("browser Origin requires an explicit trusted-origin policy")
}

func (s *Server) serveStreamablePost(w http.ResponseWriter, r *http.Request, owner string) {
	accept := strings.Join(r.Header.Values("Accept"), ",")
	if !accepts(accept, "application/json") || !accepts(accept, "text/event-stream") {
		writeHTTPError(w, http.StatusNotAcceptable, "Accept must include application/json and text/event-stream")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeHTTPError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	request, status, err := s.readRequest(r)
	if err != nil {
		writeRPC(w, status, newRPCError(json.RawMessage("null"), rpcCodeForReadError(status), rpcMessageForReadError(status)))
		return
	}

	sessionID, sessionHeaderPresent, sessionHeaderValid := optionalSingleHeader(r.Header, SessionHeader)
	if !sessionHeaderValid {
		writeHTTPError(w, http.StatusBadRequest, "invalid MCP session header")
		return
	}
	if request.Method == "initialize" {
		if sessionHeaderPresent {
			writeRPC(w, http.StatusBadRequest, newRPCError(request.ID, -32600, "initialize must not reuse a session"))
			return
		}
		protocol, err := negotiatedProtocol(request.Params)
		if err != nil {
			writeRPC(w, http.StatusBadRequest, newRPCError(request.ID, -32602, "invalid initialize params"))
			return
		}
		sess, err := s.newSession(owner, protocol, false)
		if err != nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "MCP session capacity reached")
			return
		}
		response := s.dispatch(r.Context(), sess, request)
		if response == nil || response.Error != nil {
			s.removeSession(sess.id, owner)
			writeRPC(w, http.StatusBadRequest, response)
			return
		}
		w.Header().Set(SessionHeader, sess.id)
		writeRPC(w, http.StatusOK, response)
		return
	}

	if !sessionHeaderPresent {
		writeHTTPError(w, http.StatusBadRequest, "MCP session header required")
		return
	}
	sess := s.lookupSession(sessionID, owner)
	if sess == nil || sess.legacy {
		writeHTTPError(w, http.StatusNotFound, "MCP session not found")
		return
	}
	protocol, protocolPresent, protocolValid := optionalSingleHeader(r.Header, ProtocolHeader)
	if !protocolValid || !protocolPresent || protocol != sess.protocol {
		writeHTTPError(w, http.StatusBadRequest, "MCP protocol header mismatch")
		return
	}
	response := s.dispatch(r.Context(), sess, request)
	if response == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, http.StatusOK, response)
}

func (s *Server) serveStreamableDelete(w http.ResponseWriter, r *http.Request, owner string) {
	sess := s.streamableSessionFromHeaders(w, r, owner)
	if sess == nil {
		return
	}
	s.removeSession(sess.id, owner)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) streamableSessionFromHeaders(w http.ResponseWriter, r *http.Request, owner string) *session {
	sessionID, sessionPresent, sessionValid := optionalSingleHeader(r.Header, SessionHeader)
	if !sessionValid || !sessionPresent {
		writeHTTPError(w, http.StatusBadRequest, "MCP session header required")
		return nil
	}
	sess := s.lookupSession(sessionID, owner)
	if sess == nil || sess.legacy {
		writeHTTPError(w, http.StatusNotFound, "MCP session not found")
		return nil
	}
	protocol, protocolPresent, protocolValid := optionalSingleHeader(r.Header, ProtocolHeader)
	if !protocolValid || !protocolPresent || protocol != sess.protocol {
		writeHTTPError(w, http.StatusBadRequest, "MCP protocol header mismatch")
		return nil
	}
	return sess
}

func (s *Server) dispatch(parent context.Context, sess *session, request *jsonRPCRequest) *jsonRPCResponse {
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" || !validRequestID(request.ID) {
		return newRPCError(validResponseID(request.ID), -32600, "invalid request")
	}
	if request.Method == "initialize" {
		if len(request.ID) == 0 {
			return newRPCError(json.RawMessage("null"), -32600, "initialize requires a request ID")
		}
		return s.initializeSession(sess, request)
	}
	if request.Method == "notifications/initialized" {
		if len(request.ID) != 0 {
			return newRPCError(request.ID, -32600, "initialized must be a notification")
		}
		sess.mu.Lock()
		if sess.protocol != "" {
			sess.initialized = true
		}
		sess.mu.Unlock()
		return nil
	}
	if request.Method == "notifications/cancelled" {
		if len(request.ID) != 0 {
			return newRPCError(request.ID, -32600, "cancelled must be a notification")
		}
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(request.Params, &params) == nil {
			sess.cancelRequest(params.RequestID)
		}
		return nil
	}
	if len(request.ID) == 0 {
		return nil
	}
	if !sess.isInitialized() {
		return newRPCError(request.ID, -32600, "session not initialized")
	}

	switch request.Method {
	case "ping":
		return jsonRPCResult(request.ID, map[string]any{})
	case "tools/list":
		return s.listTools(parent, sess, request)
	case "tools/call":
		return s.callTool(parent, sess, request)
	default:
		return newRPCError(request.ID, -32601, "method not found")
	}
}

type providerResult[T any] struct {
	value T
	err   error
}

func boundedProviderCall[T any](ctx context.Context, server *Server, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-server.done:
		return zero, errMCPServerClosed
	case server.providerSlots <- struct{}{}:
	}
	select {
	case <-ctx.Done():
		<-server.providerSlots
		return zero, ctx.Err()
	case <-server.done:
		<-server.providerSlots
		return zero, errMCPServerClosed
	default:
	}

	result := make(chan providerResult[T], 1)
	go func() {
		defer func() { <-server.providerSlots }()
		value, err := invoke(ctx)
		result <- providerResult[T]{value: value, err: err}
	}()
	select {
	case completed := <-result:
		return completed.value, completed.err
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-server.done:
		return zero, errMCPServerClosed
	}
}

func (s *Server) withInflight(parent context.Context, sess *session, id json.RawMessage, run func(context.Context) *jsonRPCResponse) (response *jsonRPCResponse) {
	ctx, cancel := context.WithTimeout(parent, s.callTimeout)
	if !sess.addInflight(id, cancel, s.maxPendingPerSession) {
		cancel()
		return newRPCError(id, -32000, "too many pending tool calls")
	}
	defer func() {
		cancelled := sess.finishInflight(id)
		cancel()
		if cancelled || parent.Err() != nil {
			response = nil
			return
		}
		select {
		case <-sess.closed:
			response = nil
		case <-s.done:
			response = nil
		default:
		}
	}()
	return run(ctx)
}

func (s *Server) listTools(parent context.Context, sess *session, request *jsonRPCRequest) *jsonRPCResponse {
	return s.withInflight(parent, sess, request.ID, func(ctx context.Context) *jsonRPCResponse {
		tools, err := boundedProviderCall(ctx, s, sess.provider.ListTools)
		if err != nil {
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				return newRPCError(request.ID, -32000, "tool registry timed out")
			case errors.Is(err, context.Canceled), errors.Is(err, errMCPServerClosed):
				return newRPCError(request.ID, -32000, "tool registry cancelled")
			default:
				return newRPCError(request.ID, -32603, "tool registry unavailable")
			}
		}
		tools, err = normalizeTools(tools)
		if err != nil {
			return newRPCError(request.ID, -32603, "tool registry invalid")
		}
		return jsonRPCResult(request.ID, map[string]any{"tools": tools})
	})
}

func (s *Server) initializeSession(sess *session, request *jsonRPCRequest) *jsonRPCResponse {
	protocol, err := negotiatedProtocol(request.Params)
	if err != nil {
		return newRPCError(request.ID, -32602, "invalid initialize params")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.protocol != "" && sess.protocol != protocol {
		return newRPCError(request.ID, -32600, "session already initialized")
	}
	if sess.protocol != "" && sess.legacy {
		return newRPCError(request.ID, -32600, "session already initialized")
	}
	sess.protocol = protocol
	return jsonRPCResult(request.ID, map[string]any{
		"protocolVersion": protocol,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "m365-copilot2api", "version": "wp6"},
	})
}

func (s *Server) callTool(parent context.Context, sess *session, request *jsonRPCRequest) *jsonRPCResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(request.Params, &params) != nil || strings.TrimSpace(params.Name) == "" {
		return newRPCError(request.ID, -32602, "invalid tool arguments")
	}
	arguments := map[string]any{}
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		var err error
		arguments, err = decodeJSONObject(params.Arguments)
		if err != nil {
			return newRPCError(request.ID, -32602, "invalid tool arguments")
		}
	}
	return s.withInflight(parent, sess, request.ID, func(ctx context.Context) *jsonRPCResponse {
		tools, err := boundedProviderCall(ctx, s, sess.provider.ListTools)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return jsonRPCResult(request.ID, CallResult{Content: []map[string]any{{"type": "text", "text": "tool call timed out"}}, IsError: true})
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, errMCPServerClosed) {
				return jsonRPCResult(request.ID, CallResult{Content: []map[string]any{{"type": "text", "text": "tool call cancelled"}}, IsError: true})
			}
			return newRPCError(request.ID, -32603, "tool registry unavailable")
		}
		tools, err = normalizeTools(tools)
		if err != nil {
			return newRPCError(request.ID, -32603, "tool registry invalid")
		}
		var selected Tool
		found := false
		for _, tool := range tools {
			if tool.Name == params.Name {
				selected = tool
				found = true
				break
			}
		}
		if !found {
			return newRPCError(request.ID, -32602, "unknown tool")
		}
		if err := validateToolValue(selected.InputSchema, arguments); err != nil {
			return newRPCError(request.ID, -32602, "tool arguments do not match input schema")
		}

		result, callErr := boundedProviderCall(ctx, s, func(callContext context.Context) (CallResult, error) {
			return sess.provider.CallTool(callContext, params.Name, arguments)
		})
		if callErr != nil {
			message := "tool execution failed"
			if errors.Is(callErr, context.DeadlineExceeded) {
				message = "tool call timed out"
			} else if errors.Is(callErr, context.Canceled) || errors.Is(callErr, errMCPServerClosed) {
				message = "tool call cancelled"
			}
			return jsonRPCResult(request.ID, CallResult{Content: []map[string]any{{"type": "text", "text": message}}, IsError: true})
		}
		result, err = normalizeCallResult(selected, result)
		if err != nil {
			return newRPCError(request.ID, -32603, "invalid tool result")
		}
		return jsonRPCResult(request.ID, result)
	})
}

func (s *Server) readRequest(r *http.Request) (*jsonRPCRequest, int, error) {
	reader := io.LimitReader(r.Body, s.maxMessageBytes+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if int64(len(body)) > s.maxMessageBytes {
		return nil, http.StatusRequestEntityTooLarge, errors.New("message too large")
	}
	var request jsonRPCRequest
	if len(body) == 0 || json.Unmarshal(body, &request) != nil {
		return nil, http.StatusBadRequest, errors.New("parse error")
	}
	return &request, http.StatusOK, nil
}

func (s *Server) newSession(owner, protocol string, legacy bool) (*session, error) {
	idBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	now := s.now()
	sess := &session{
		id:         base64.RawURLEncoding.EncodeToString(idBytes),
		owner:      owner,
		protocol:   protocol,
		legacy:     legacy,
		provider:   s.provider,
		createdAt:  now,
		lastUsedAt: now,
		messages:   make(chan []byte, s.maxPendingPerSession),
		closed:     make(chan struct{}),
		inflight:   make(map[string]context.CancelFunc),
		cancelled:  make(map[string]struct{}),
	}
	s.mu.Lock()
	expired := s.cleanupExpiredLocked(now)
	if s.closed {
		s.mu.Unlock()
		closeSessions(expired)
		return nil, errMCPServerClosed
	}
	if len(s.sessions) >= s.maxSessions {
		s.mu.Unlock()
		closeSessions(expired)
		return nil, errors.New("session capacity reached")
	}
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	closeSessions(expired)
	return sess, nil
}

func (s *Server) lookupSession(id, owner string) *session {
	now := s.now()
	s.mu.Lock()
	expired := s.cleanupExpiredLocked(now)
	sess := s.sessions[id]
	if sess != nil && sess.owner != owner {
		sess = nil
	}
	if sess != nil {
		sess.lastUsedAt = now
	}
	s.mu.Unlock()
	closeSessions(expired)
	return sess
}

func (s *Server) removeSession(id, owner string) {
	s.mu.Lock()
	sess := s.sessions[id]
	if sess != nil && sess.owner == owner {
		delete(s.sessions, id)
	} else {
		sess = nil
	}
	s.mu.Unlock()
	if sess != nil {
		sess.close()
	}
}

func (s *Server) cleanupExpiredLocked(now time.Time) []*session {
	var expired []*session
	for id, sess := range s.sessions {
		if now.Sub(sess.lastUsedAt) >= s.sessionTTL {
			delete(s.sessions, id)
			expired = append(expired, sess)
		}
	}
	return expired
}

func (s *Server) sendLegacyResponse(w http.ResponseWriter, r *http.Request, sess *session, response *jsonRPCResponse, status int) {
	body, err := json.Marshal(response)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, "response encoding failed")
		return
	}
	select {
	case sess.messages <- body:
		w.WriteHeader(status)
	case <-r.Context().Done():
		writeHTTPError(w, http.StatusRequestTimeout, "request cancelled")
	case <-sess.closed:
		writeHTTPError(w, http.StatusNotFound, "MCP session not found")
	default:
		writeHTTPError(w, http.StatusServiceUnavailable, "MCP response queue full")
	}
}

func (sess *session) isInitialized() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.initialized
}

func (sess *session) addInflight(id json.RawMessage, cancel context.CancelFunc, max int) bool {
	key, ok := requestIDKey(id)
	if !ok {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	select {
	case <-sess.closed:
		return false
	default:
	}
	if len(sess.inflight) >= max {
		return false
	}
	if _, exists := sess.inflight[key]; exists {
		return false
	}
	sess.inflight[key] = cancel
	return true
}

func (sess *session) finishInflight(id json.RawMessage) bool {
	key, ok := requestIDKey(id)
	if !ok {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	delete(sess.inflight, key)
	_, cancelled := sess.cancelled[key]
	delete(sess.cancelled, key)
	return cancelled
}

func (sess *session) cancelRequest(id json.RawMessage) {
	key, ok := requestIDKey(id)
	if !ok {
		return
	}
	sess.mu.Lock()
	cancel := sess.inflight[key]
	if cancel != nil {
		sess.cancelled[key] = struct{}{}
	}
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (sess *session) close() {
	sess.closeOnce.Do(func() {
		close(sess.closed)
		sess.mu.Lock()
		cancellations := make([]context.CancelFunc, 0, len(sess.inflight))
		for _, cancel := range sess.inflight {
			cancellations = append(cancellations, cancel)
		}
		sess.inflight = make(map[string]context.CancelFunc)
		sess.cancelled = make(map[string]struct{})
		sess.mu.Unlock()
		for _, cancel := range cancellations {
			cancel()
		}
	})
}

func closeSessions(sessions []*session) {
	for _, sess := range sessions {
		sess.close()
	}
}

func negotiatedProtocol(params json.RawMessage) (string, error) {
	var initialize struct {
		ProtocolVersion any             `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      json.RawMessage `json:"clientInfo"`
	}
	if json.Unmarshal(params, &initialize) != nil {
		return "", errors.New("invalid params")
	}
	protocol, ok := initialize.ProtocolVersion.(string)
	if !ok || strings.TrimSpace(protocol) == "" || !isJSONObject(initialize.Capabilities) || !isJSONObject(initialize.ClientInfo) {
		return "", errors.New("invalid params")
	}
	var clientInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(initialize.ClientInfo, &clientInfo) != nil || strings.TrimSpace(clientInfo.Name) == "" || strings.TrimSpace(clientInfo.Version) == "" {
		return "", errors.New("invalid params")
	}
	if _, supported := supportedProtocolVersions[protocol]; supported {
		return protocol, nil
	}
	return LatestProtocolVersion, nil
}

func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	_, ok := requestIDKey(id)
	return ok
}

func requestIDKey(id json.RawMessage) (string, bool) {
	if len(id) == 0 {
		return "", false
	}
	if id[0] == '"' {
		var value string
		if json.Unmarshal(id, &value) != nil {
			return "", false
		}
		return "s:" + value, true
	}
	value := string(id)
	if strings.ContainsAny(value, ".eE") {
		return "", false
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return "", false
	}
	return "i:" + strconv.FormatInt(number, 10), true
}

func validResponseID(id json.RawMessage) json.RawMessage {
	if validRequestID(id) && len(id) > 0 {
		return id
	}
	return json.RawMessage("null")
}

func normalizeTools(tools []Tool) ([]Tool, error) {
	if tools == nil {
		return []Tool{}, nil
	}
	seen := make(map[string]struct{}, len(tools))
	result := make([]Tool, len(tools))
	for index, tool := range tools {
		if !validToolName(tool.Name) {
			return nil, errors.New("invalid tool name")
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return nil, errors.New("duplicate tool name")
		}
		seen[tool.Name] = struct{}{}
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{"type": "object"}
		}
		if schemaType, _ := tool.InputSchema["type"].(string); schemaType != "object" {
			return nil, errors.New("invalid input schema")
		}
		if _, err := compileToolSchema(tool.InputSchema); err != nil {
			return nil, errors.New("invalid input schema")
		}
		if tool.OutputSchema != nil {
			if schemaType, _ := tool.OutputSchema["type"].(string); schemaType != "object" {
				return nil, errors.New("invalid output schema")
			}
			if _, err := compileToolSchema(tool.OutputSchema); err != nil {
				return nil, errors.New("invalid output schema")
			}
		}
		result[index] = tool
	}
	return result, nil
}

func optionalSingleHeader(header http.Header, name string) (string, bool, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false, true
	}
	if len(values) != 1 {
		return "", true, false
	}
	value := strings.TrimSpace(values[0])
	if value == "" || strings.Contains(value, ",") {
		return "", true, false
	}
	return value, true, true
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, error) {
	var object map[string]any
	if err := decodeExactJSON(raw, &object); err != nil || object == nil {
		return nil, errors.New("JSON object required")
	}
	return object, nil
}

func validToolName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func accepts(header, mediaType string) bool {
	for _, part := range strings.Split(header, ",") {
		value, _, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err == nil && (value == mediaType || value == "*/*") {
			return true
		}
	}
	return false
}

func newRPCError(id json.RawMessage, code int, message string) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func jsonRPCResult(id json.RawMessage, result any) *jsonRPCResponse {
	body, err := json.Marshal(result)
	if err != nil {
		return newRPCError(id, -32603, "response encoding failed")
	}
	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: body}
}

func writeRPC(w http.ResponseWriter, status int, response *jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if response != nil {
		_ = json.NewEncoder(w).Encode(response)
	}
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	writeRPC(w, status, newRPCError(json.RawMessage("null"), -32000, message))
}

func rpcCodeForReadError(status int) int {
	if status == http.StatusRequestEntityTooLarge {
		return -32000
	}
	return -32700
}

func rpcMessageForReadError(status int) string {
	if status == http.StatusRequestEntityTooLarge {
		return "MCP message too large"
	}
	return "parse error"
}

// StaticToolProvider is the smallest useful provider for local integration and
// deterministic interoperability tests.
type StaticToolProvider struct {
	mu     sync.RWMutex
	tools  []Tool
	onCall func(context.Context, string, map[string]any) (CallResult, error)
}

func NewStaticToolProvider(tools []Tool, onCall func(context.Context, string, map[string]any) (CallResult, error)) *StaticToolProvider {
	return &StaticToolProvider{tools: append([]Tool(nil), tools...), onCall: onCall}
}

func (p *StaticToolProvider) ListTools(context.Context) ([]Tool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Tool(nil), p.tools...), nil
}

func (p *StaticToolProvider) CallTool(ctx context.Context, name string, arguments map[string]any) (CallResult, error) {
	p.mu.RLock()
	onCall := p.onCall
	p.mu.RUnlock()
	if onCall == nil {
		return CallResult{}, errors.New("tool unavailable")
	}
	return onCall(ctx, name, arguments)
}

type emptyToolProvider struct{}

func (emptyToolProvider) ListTools(context.Context) ([]Tool, error) {
	return []Tool{}, nil
}

func (emptyToolProvider) CallTool(context.Context, string, map[string]any) (CallResult, error) {
	return CallResult{}, errors.New("tool unavailable")
}
