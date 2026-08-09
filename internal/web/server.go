package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"m365-native/internal/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type pendingPKCE struct {
	Verifier         string
	RedirectURI      string
	OAuth            auth.OAuthConfig
	ProfileID        string
	Staged           bool
	DiscardOnFailure bool
	Created          time.Time
	Status           string
	Account          any
	ErrorCode        string
	Error            string
}

type chatService interface {
	Chat(context.Context, chathub.Account, chathub.Request) (chathub.Result, error)
	ChatWithDelta(context.Context, chathub.Account, chathub.Request, func(string) error) (chathub.Result, error)
	ChatWithEvents(context.Context, chathub.Account, chathub.Request, chathub.StreamHandler) (chathub.Result, error)
}

type Server struct {
	mu                  sync.Mutex
	tokenMu             sync.Mutex
	checkpointLifecycle sync.RWMutex
	tokens              *auth.Store
	oauthProfiles       *auth.OAuthProfileManager
	pkce                map[string]pendingPKCE
	browserPKCERun      browserPKCERunner
	browserPKCEConfig   func() auth.OAuthConfig
	browserPKCEActive   bool
	chat                chatService
	checkpoints         *transportCheckpointStore
	adminPassword       string
	adminCredentialMode adminCredentialMode
	adminSessions       map[string]adminSession
	adminSecurity       adminSecurityPolicy
	mustChangePassword  bool
	loginAttempts       map[string]loginAttempt
	clock               func() time.Time
	apiKeys             *apiKeyStore
	debug               *debugStore
	settings            *settingsStore
	catalogEvidence     *catalogEvidenceProjection
	resourceToken       func(context.Context, string) (string, error)
	resourceInvalidate  func(string, string)
	artifactOrigin      string
	artifacts           *artifactStore
	artifactFetch       *artifactFetchClient
	mcp                 *mcp.Server
}

func newConfiguredChatHubClient(settings *settingsStore) *chathub.Client {
	client := chathub.NewClient()
	client.Trace = logChathubTrace
	client.PrivateMode = func() bool {
		return settings == nil || settings.get().ChatMode != chatModeNormal
	}
	return client
}

func New() (*Server, error) {
	settings := openSettingsStore()
	if settings.loadErr != nil {
		return nil, fmt.Errorf("load runtime settings: %w", settings.loadErr)
	}
	catalogEvidence, err := defaultAcceptedWP2CatalogProjection(settings.get())
	if err != nil {
		return nil, fmt.Errorf("load accepted WP2 catalog evidence: %w", err)
	}
	oauthProfiles, err := auth.OpenOAuthProfileManager(auth.CachePath(), auth.CurrentOAuthConfig())
	if err != nil {
		return nil, fmt.Errorf("load OAuth token profiles: %w", err)
	}
	_, store, err := oauthProfiles.ActiveStore()
	if err != nil {
		return nil, fmt.Errorf("load active OAuth token profile: %w", err)
	}
	credential, err := loadAdminCredential()
	if err != nil {
		return nil, fmt.Errorf("load administrator credential: %w", err)
	}
	adminSecurity, err := loadAdminSecurityPolicy()
	if err != nil {
		return nil, fmt.Errorf("load management security policy: %w", err)
	}
	checkpoints, err := openConfiguredTransportCheckpointStore()
	if err != nil {
		return nil, fmt.Errorf("load transport checkpoints: %w", err)
	}
	artifactOrigin, err := configuredArtifactPublicOrigin()
	if err != nil {
		return nil, fmt.Errorf("load artifact public origin: %w", err)
	}
	server := &Server{
		tokens:              store,
		oauthProfiles:       oauthProfiles,
		pkce:                map[string]pendingPKCE{},
		chat:                newConfiguredChatHubClient(settings),
		checkpoints:         checkpoints,
		adminPassword:       credential.Password,
		adminCredentialMode: credential.Mode,
		adminSessions:       map[string]adminSession{},
		adminSecurity:       adminSecurity,
		mustChangePassword:  credential.Mode == adminCredentialBootstrap,
		loginAttempts:       map[string]loginAttempt{},
		apiKeys:             openAPIKeys(),
		debug:               openDebugStore(),
		settings:            settings,
		catalogEvidence:     catalogEvidence,
		artifactOrigin:      artifactOrigin,
	}
	server.mcp = server.newMCPRuntime()
	if err := server.configureArtifactService(); err != nil {
		return nil, fmt.Errorf("load artifact service: %w", err)
	}
	return server, nil
}

func (s *Server) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/admin/login", s.adminLogin)
	m.HandleFunc("/api/admin/logout", s.adminLogout)
	m.HandleFunc("/api/admin/session", s.adminSession)
	m.HandleFunc("/api/admin/change-password", s.adminChangePassword)
	m.HandleFunc("/api/admin/keys", s.adminKeys)
	m.HandleFunc("/api/admin/settings", s.adminSettings)
	m.HandleFunc("/api/admin/proxy-pool", s.proxyPool)
	m.HandleFunc("/api/admin/deployments", s.deployments)
	m.HandleFunc("/api/admin/deployment", s.deploymentAction)
	m.HandleFunc("/api/admin/deployment/check", s.deploymentCheck)
	m.HandleFunc("/api/admin/debug/logs", s.debugList)
	m.HandleFunc("/api/admin/debug/detail", s.debugDetail)
	m.HandleFunc("/api/admin/debug/session", s.debugSession)
	m.HandleFunc("/api/admin/debug/export", s.debugExport)
	m.HandleFunc("/debug", s.debugPage)
	m.HandleFunc("/api/health", s.health)
	m.HandleFunc("/api/version", s.version)
	m.HandleFunc("/api/update", s.update)
	m.HandleFunc("/api/account", s.accountStatus)
	m.HandleFunc("/api/account/refresh", s.refreshSingleAccount)
	m.HandleFunc("/api/account/logout", s.logoutSingleAccount)
	m.HandleFunc("/api/auth/start", s.startPKCE)
	m.HandleFunc("/api/auth/status", s.pkceStatus)
	m.HandleFunc("/api/auth/callback", s.callbackPKCE)
	m.HandleFunc("/api/auth/candidate/chat", s.validateOAuthCandidateChat)
	m.HandleFunc("/api/auth/browser/default/start", s.startDefaultClientBrowserPKCE)
	m.HandleFunc("/api/chat", s.chatOnce)
	m.HandleFunc("/api/chat/stream", s.chatStream)
	m.HandleFunc("/api/conversations", s.conversations)
	m.HandleFunc("/api/conversations/delete", s.deleteConversation)
	m.HandleFunc("/v1/models", s.openaiModels)
	m.HandleFunc("/v1/chat/completions", s.openaiChat)
	m.HandleFunc("/v1/responses", s.responses)
	m.HandleFunc("/v1/messages", s.anthropicMessages)
	m.HandleFunc("/v1/images/generations", s.imageGenerations)
	m.HandleFunc("/v1/mcp", s.mcpStreamable)
	m.HandleFunc("/v1/mcp/sse", s.mcpLegacySSE)
	m.HandleFunc("/v1/mcp/message", s.mcpLegacyMessage)
	m.HandleFunc(artifactRoutePrefix, s.artifactContent)
	m.HandleFunc("/", s.rootPage)
	return requestID(httpTrace(securityHeaders(s.adminRequestSecurity(s.adminMiddleware(s.debugMiddleware(m))))))
}

func (s *Server) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/login" || r.URL.Path == "/api/admin/session" || r.URL.Path == "/api/admin/change-password" || r.URL.Path == "/api/admin/logout" || r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			if _, artifact := artifactCapabilityToken(r.URL.Path); artifact {
				next.ServeHTTP(w, r)
				return
			}
			ownerID, ok := s.authenticateAPIKey(r)
			if !ok {
				http.Error(w, `{"error":{"message":"valid API key required","type":"auth_error"}}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, withAPIKeyOwner(r, ownerID))
			return
		}
		s.mu.Lock()
		credentialAvailable := s.adminPassword != "" && s.adminCredentialMode != adminCredentialUnavailable
		s.mu.Unlock()
		if !credentialAvailable {
			http.Error(w, `{"error":{"message":"管理員憑證無法使用","type":"configuration_error"}}`, http.StatusServiceUnavailable)
			return
		}
		if !s.validAdminSession(r) {
			writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "需要先以管理員身分登入")
			return
		}
		s.mu.Lock()
		mustChange := s.mustChangePassword
		s.mu.Unlock()
		if mustChange && r.URL.Path != "/api/admin/change-password" && r.URL.Path != "/api/admin/logout" {
			writeOpenAIError(w, http.StatusForbidden, "password_change_required", "使用管理主控台前必須先變更管理員密碼")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validAdminSession(r *http.Request) bool {
	c, err := r.Cookie("m365_admin_session")
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.adminSessions[c.Value]
	now := s.adminNow()
	if !ok || session.CreatedAt.IsZero() || session.LastSeenAt.IsZero() || session.ExpiresAt.IsZero() || !now.Before(session.ExpiresAt) || now.Sub(session.LastSeenAt) >= adminSessionIdleTimeout {
		delete(s.adminSessions, c.Value)
		return false
	}
	session.LastSeenAt = now
	s.adminSessions[c.Value] = session
	return true
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	ip, now := clientIP(r), s.adminNow()
	if ok, wait := s.loginAllowed(ip, now); !ok {
		seconds := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", fmt.Sprint(seconds))
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "登入失敗次數過多，請稍後再試")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	s.mu.Lock()
	password := s.adminPassword
	mode := s.adminCredentialMode
	s.mu.Unlock()
	if password == "" || mode == adminCredentialUnavailable || mode == adminCredentialBootstrapConsumed {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "管理員憑證無法使用")
		return
	}
	if decodeErr != nil || body.Password == "" || subtle.ConstantTimeCompare([]byte(body.Password), []byte(password)) != 1 {
		s.recordLoginFailure(ip, now)
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "管理員密碼不正確")
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeOpenAIError(w, 500, "internal_error", "無法建立管理員工作階段")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	mustChange, err := s.establishAdminSession(password, mode, token, now)
	if errors.Is(err, errAdminCredentialUnavailable) || errors.Is(err, errAdminBootstrapConsumed) {
		writeOpenAIError(w, http.StatusServiceUnavailable, "configuration_error", "管理員憑證無法使用")
		return
	}
	if errors.Is(err, errAdminCredentialChanged) {
		writeOpenAIError(w, http.StatusUnauthorized, "auth_error", "管理員密碼不正確")
		return
	}
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "storage_error", "無法使一次性 bootstrap secret 失效")
		return
	}
	s.clearLoginFailures(ip)
	http.SetCookie(w, &http.Cookie{Name: "m365_admin_session", Value: token, Path: "/", HttpOnly: true, Secure: secureAdminCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: int(adminSessionAbsoluteTimeout.Seconds())})
	jsonOut(w, map[string]any{"status": "authenticated", "must_change_password": mustChange})
}
func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	if c, e := r.Cookie("m365_admin_session"); e == nil {
		s.mu.Lock()
		delete(s.adminSessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "m365_admin_session", Path: "/", HttpOnly: true, Secure: secureAdminCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	jsonOut(w, map[string]string{"status": "logged_out"})
}
func (s *Server) adminSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	authenticated := s.validAdminSession(r)
	s.mu.Lock()
	mustChange := s.mustChangePassword
	s.mu.Unlock()
	jsonOut(w, map[string]bool{"authenticated": authenticated, "must_change_password": authenticated && mustChange})
}

func (s *Server) adminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOut(w, map[string]any{"keys": s.apiKeys.list()})
	case http.MethodPost:
		var b struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil {
			http.Error(w, "JSON 格式錯誤", 400)
			return
		}
		if strings.TrimSpace(b.Name) == "" {
			b.Name = "API key"
		}
		rec, raw, e := s.apiKeys.create(b.Name)
		if e != nil {
			http.Error(w, "無法建立 API Key", 500)
			return
		}
		jsonOut(w, map[string]any{"key": raw, "record": rec})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		revoked, e := s.apiKeys.revoke(id)
		if e != nil {
			http.Error(w, "無法撤銷 API Key", http.StatusInternalServerError)
			return
		}
		if !revoked {
			http.Error(w, "找不到 API Key", 404)
			return
		}
		jsonOut(w, map[string]string{"status": "revoked"})
	default:
		http.Error(w, "不支援此 HTTP 方法", 405)
	}
}
func (s *Server) authenticateAPIKey(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if raw == "" {
		v := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			raw = strings.TrimSpace(v[7:])
		}
	}
	if raw == "" || s.apiKeys == nil {
		return "", false
	}
	return s.apiKeys.authenticate(raw)
}

func (s *Server) validAPIKey(r *http.Request) bool {
	_, ok := s.authenticateAPIKey(r)
	return ok
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	store := s.activeTokenStore()
	connected := false
	var oauthConfig auth.OAuthConfig
	var tokenPath string
	if store != nil {
		_, connected = store.First()
		oauthConfig = store.Config()
		tokenPath = store.Path()
	}
	jsonOut(w, map[string]any{
		"status":           "ok",
		"auth":             []string{"pkce"},
		"chat":             "chathub",
		"clientId":         oauthConfig.ClientID,
		"scope":            oauthConfig.Scope,
		"tokenCache":       tokenPath,
		"accountConnected": connected,
	})
}

type singleAccountView struct {
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

func accountView(account auth.AccountToken) singleAccountView {
	return singleAccountView{Status: account.Status, ExpiresAt: account.ExpiresAt, UpdatedAt: account.UpdatedAt}
}

func (s *Server) accountStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "不支援此 HTTP 方法", http.StatusMethodNotAllowed)
		return
	}
	store := s.activeTokenStore()
	if store == nil {
		jsonOut(w, map[string]any{"account": nil})
		return
	}
	account, ok := store.First()
	if !ok {
		jsonOut(w, map[string]any{"account": nil})
		return
	}
	jsonOut(w, map[string]any{"account": accountView(account)})
}

func (s *Server) refreshSingleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "不支援此 HTTP 方法", http.StatusMethodNotAllowed)
		return
	}
	store := s.activeTokenStore()
	if store == nil {
		writeOpenAIError(w, http.StatusNotFound, "account_not_found", "尚未登入 Microsoft 帳號")
		return
	}
	account, ok := store.First()
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "account_not_found", "尚未登入 Microsoft 帳號")
		return
	}
	account, err := store.EnsureValid(account.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "token_refresh_error", "無法重新整理帳號權杖")
		return
	}
	jsonOut(w, map[string]any{"status": "refreshed", "account": accountView(account)})
}

func (s *Server) logoutSingleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "不支援此 HTTP 方法", http.StatusMethodNotAllowed)
		return
	}
	if err := s.resetTransportCheckpoints(func() error {
		store := s.activeTokenStore()
		if store == nil {
			return nil
		}
		account, ok := store.First()
		if !ok {
			return nil
		}
		return store.Delete(account.ID)
	}); err != nil {
		http.Error(w, "無法刪除帳號", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"status": "logged_out"})
}

func (s *Server) startPKCE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	target, failure := s.resolvePKCEProfileTarget(w, r)
	if failure != nil {
		writePKCEStartFailure(w, *failure)
		return
	}
	started, failure := s.beginPKCEForTarget(target)
	if failure != nil {
		if target.Created && s.oauthProfiles != nil {
			_ = s.oauthProfiles.Discard(target.ProfileID)
		}
		writePKCEStartFailure(w, *failure)
		return
	}
	jsonOut(w, map[string]any{
		"status":           "pkce_ready",
		"state":            started.State,
		"oauthProfileId":   target.ProfileID,
		"oauthProfileKind": target.Kind,
		"staged":           target.Staged,
		"url":              started.AuthorizationURL,
		"redirectUri":      target.OAuth.RedirectURI,
		"note":             "正常流程會自動完成 callback；只有自動 handoff 無法使用時才使用手動 JSON POST 備援。",
	})
}

func (s *Server) pkceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Error(w, "缺少 state", http.StatusBadRequest)
		return
	}
	now := s.oauthNow()
	var expiredProfile pendingPKCE
	s.mu.Lock()
	p, ok := s.pkce[state]
	if ok && p.Status == "pending" && now.Sub(p.Created) > pkceTransactionTTL {
		p.Status = "expired"
		p.Verifier = ""
		p.ErrorCode = "oauth_state_expired"
		p.Error = "OAuth 授權工作階段已過期，請重新開始授權"
		s.pkce[state] = p
		expiredProfile = p
	}
	s.mu.Unlock()
	if expiredProfile.ProfileID != "" {
		s.discardPKCEProfileOnFailure(expiredProfile)
	}
	if !ok {
		jsonOut(w, map[string]any{"status": "expired", "errorCode": "oauth_state_mismatch"})
		return
	}
	out := map[string]any{
		"status":         p.Status,
		"oauthProfileId": p.ProfileID,
		"staged":         p.Staged,
	}
	if p.Account != nil {
		out["account"] = p.Account
	}
	if p.Error != "" {
		out["error"] = p.Error
	}
	if p.ErrorCode != "" {
		out["errorCode"] = p.ErrorCode
	}
	jsonOut(w, out)
}

func (s *Server) callbackPKCE(w http.ResponseWriter, r *http.Request) {
	s.handlePKCECallback(w, r)
}

func writeOAuthCompletionPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html lang="zh-TW"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>M365 Copilot2API 授權完成</title><style>body{font:16px system-ui;text-align:center;padding:15vh 20px;color:#242424}main{max-width:520px;margin:auto}h1{font-size:26px}</style></head><body><main><h1>授權完成</h1><p>Microsoft 帳號已登入，可以關閉此頁面。</p><script>if(window.opener){window.opener.postMessage({type:"m365-auth-complete"},window.location.origin);setTimeout(()=>window.close(),300)}</script></main></body></html>`)
}

func (s *Server) resolveAccount(_ string) (auth.AccountToken, error) {
	return s.activeAccount()
}

type chatBody struct {
	Model          string               `json:"model,omitempty"`
	AccountID      string               `json:"accountId"`
	Message        string               `json:"message"`
	Prompt         string               `json:"prompt"`
	Tone           string               `json:"tone"`
	ConversationID string               `json:"conversationId"`
	SessionID      string               `json:"sessionId"`
	SessionKey     string               `json:"sessionKey"`
	Attachments    []chathub.Attachment `json:"attachments,omitempty"`
	Tools          []chathub.Tool       `json:"tools,omitempty"`
	// Legacy OpenAI-compatible clients still send functions/function_call.
	Functions       []json.RawMessage `json:"functions,omitempty"`
	ToolChoice      any               `json:"tool_choice,omitempty"`
	FunctionCall    any               `json:"function_call,omitempty"`
	Reasoning       *reasoningConfig  `json:"reasoning,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Verbosity       string            `json:"verbosity,omitempty"`
	ResponseFormat  *responseFormat   `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type       string         `json:"type"`
	JSONSchema map[string]any `json:"json_schema,omitempty"`
}

func (s *Server) chatOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		// try extract from access token claims on the fly
		if claimsOID, claimsTID := extractOIDTID(acc.AccessToken); claimsOID != "" {
			acc.OID = claimsOID
			acc.TID = claimsTID
		}
	}
	if acc.OID == "" || acc.TID == "" {
		http.Error(w, "account missing oid/tid — re-login with PKCE browser client", http.StatusBadRequest)
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
		Text:           text,
		Tone:           resolution.ResolvedTone,
		ConversationID: body.ConversationID,
		SessionID:      body.SessionID,
		Attachments:    body.Attachments,
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
	res.Images = validImageURLs(res.Images)
	if !requireUsableLegacyNonStreamResult(w, res, body.Tools) {
		return
	}
	turn.Observe(res)
	if err := turn.Accept(assistantTextCheckpointMessage(res.Text, res.Images)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := map[string]any{
		"status":         "ok",
		"model":          resolution.ResponseModel,
		"text":           res.Text,
		"conversationId": res.ConversationID,
		"sessionId":      res.SessionID,
		"requestId":      res.RequestID,
		"throttling":     res.Throttling,
		"semanticEvents": chathub.SemanticEvents(res.Events),
		"images":         res.Images,
		"m365":           compatM365Metadata(res, resolution),
	}
	if reasoning := chathub.ReasoningContent(res.Events); reasoning != "" {
		response["reasoning_content"] = reasoning
	}
	jsonOut(w, response)
}

func (s *Server) openaiModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings := serverRuntimeSettings(s)
	data := modelCatalogForSettingsAndEvidence(settings, s.catalogEvidence.forSettings(settings))
	created := time.Now().Unix()
	for _, model := range data {
		model["created"] = created
	}
	// Codex v0.144.5 requires `models`, while OpenAI-compatible clients use
	// `data`. Keep both aliases backed by the same catalog.
	jsonOut(w, map[string]any{"object": "list", "data": data, "models": data})
}

type oaiMsg struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []map[string]any `json:"tool_calls,omitempty"`
	SidecarGenerated bool             `json:"-"`
}

type oaiReq struct {
	Model          string          `json:"model"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Messages       []oaiMsg        `json:"messages"`
	Stream         bool            `json:"stream"`
	// optional account routing
	User           string               `json:"user"`
	AccountID      string               `json:"accountId"`
	ConversationID string               `json:"conversation_id"`
	SessionID      string               `json:"session_id"`
	SessionKey     string               `json:"session_key"`
	Attachments    []chathub.Attachment `json:"attachments,omitempty"`
	Tools          []chathub.Tool       `json:"tools,omitempty"`
	// Legacy OpenAI-compatible clients still send functions/function_call.
	Functions           []json.RawMessage `json:"functions,omitempty"`
	ToolChoice          any               `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	FunctionCall        any               `json:"function_call,omitempty"`
	Reasoning           *reasoningConfig  `json:"reasoning,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	Verbosity           string            `json:"verbosity,omitempty"`
	Temperature         json.RawMessage   `json:"temperature,omitempty"`
	TopP                json.RawMessage   `json:"top_p,omitempty"`
	MaxTokens           json.RawMessage   `json:"max_tokens,omitempty"`
	MaxCompletionTokens json.RawMessage   `json:"max_completion_tokens,omitempty"`
	Stop                json.RawMessage   `json:"stop,omitempty"`
	Seed                json.RawMessage   `json:"seed,omitempty"`
	FrequencyPenalty    json.RawMessage   `json:"frequency_penalty,omitempty"`
	PresencePenalty     json.RawMessage   `json:"presence_penalty,omitempty"`
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func contentToString(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "text" || t == "input_text" || t == "output_text" {
					if s, _ := m["text"].(string); s != "" {
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	default:
		return fmt.Sprint(v)
	}
}

func normalizeLegacyTools(body *oaiReq) {
	if len(body.Tools) == 0 && len(body.Functions) > 0 {
		body.Tools = make([]chathub.Tool, 0, len(body.Functions))
		for _, f := range body.Functions {
			body.Tools = append(body.Tools, chathub.Tool{Type: "function", Function: f})
		}
	}
	if body.ToolChoice == nil && body.FunctionCall != nil {
		body.ToolChoice = body.FunctionCall
	}
}

func textOnlyFallbackRequest(body oaiReq) oaiReq {
	body.Stream = false
	body.Tools = nil
	body.Functions = nil
	body.ToolChoice = "none"
	body.FunctionCall = nil
	return body
}

func (s *Server) openaiChat(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	startedAt := time.Now()
	log.Printf("[req-trace] id=%s stage=http_start stream=%t", requestID, r.URL.Query().Get("stream") == "true")
	defer func() {
		log.Printf("[req-trace] id=%s stage=http_return total_ms=%d", requestID, time.Since(startedAt).Milliseconds())
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings := serverRuntimeSettings(s)
	bodyLimit, err := requestBodyLimit(settings.TextInputLimitUTF16)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "configuration_error", err.Error())
		return
	}
	var body oaiReq
	if err := decodeBoundedJSON(w, r, bodyLimit, &body); err != nil {
		if isRequestBodyTooLarge(err) {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	setIgnoredParameters(w, ignoredOpenAICompatibilityParameters(body))
	needsTextValidation := !callerTextAlreadyValidated(r)
	responseFormat := body.ResponseFormat
	effort := body.ReasoningEffort
	if body.Reasoning != nil && strings.TrimSpace(body.Reasoning.Effort) != "" {
		effort = body.Reasoning.Effort
	}
	resolution, routeErr := resolveRoute(body.Model, effort, settings.ModelMappings)
	if routeErr != nil {
		if typed, ok := routeErr.(*routeResolveError); ok {
			writeOpenAIErrorCode(w, typed.Status, "invalid_request_error", typed.Code, typed.Message)
		} else {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", routeErr.Error())
		}
		return
	}
	tone := resolution.ResolvedTone
	normalizeLegacyTools(&body)
	nativePolicy, err := resolveNativePolicy(serverRuntimeSettings(s).ToolPlanningMode, body.Tools)
	if err != nil {
		if errors.Is(err, errReservedNativeToolName) {
			writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "reserved_native_tool_name", err.Error())
			return
		}
		writeOpenAIErrorCode(w, http.StatusServiceUnavailable, "configuration_error", "invalid_native_policy", err.Error())
		return
	}
	nativePolicy = withSidecarExecutionEnforcement(nativePolicy)
	log.Printf("[req-trace] id=%s stage=body_parsed messages=%d tools=%d choice=%s raw_bytes=%d", requestID, len(body.Messages), len(body.Tools), safeToolChoiceLog(normalizedToolChoiceMode(body.ToolChoice)), r.ContentLength)
	execution := checkpointExecutionFrom(r.Context())
	var priorToolCallIDs []string
	var priorSeenToolCallDigests []string
	if execution != nil && execution.turn != nil && execution.turn.turn != nil {
		priorToolCallIDs = execution.turn.turn.AllowedPriorToolCallIDs
		priorSeenToolCallDigests = execution.turn.turn.KnownPriorToolCallDigests
		body.ConversationID = execution.turn.binding.ConversationID
		body.SessionID = execution.turn.binding.SessionID
	}
	if err := validateToolConversationWithPriorDigests(body.Messages, priorToolCallIDs, priorSeenToolCallDigests); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "tool_protocol_error", err.Error())
		return
	}
	// Rebuild a protocol-neutral evidence ledger from actual tool calls/results.
	// Round limits apply only to the current user turn; the checkpoint layer
	// supplies same-turn execution evidence without treating older completed
	// operations as fresh proof in a newly appended user turn.
	ledger := buildAgentLedger(body.Messages)
	activeLedger := buildAgentLedger(activeMessages(body.Messages))
	if err := activeLedger.CanContinue(maxToolRounds()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "tool_round_limit", "message": err.Error(), "completed_calls": len(activeLedger.Completed)}})
		return
	}
	ownsCheckpoint := execution == nil
	if ownsCheckpoint {
		var validateOutbound func([]oaiMsg) error
		if needsTextValidation {
			validateOutbound = func(messages []oaiMsg) error {
				return validateCallerText(messages, settings.TextInputLimitUTF16)
			}
		}
		checkpointTurn, checkpointErr := s.beginOpenAICheckpoint(r.Context(), &body, validateOutbound)
		if checkpointErr != nil {
			var textLimitErr *callerTextLimitError
			if errors.As(checkpointErr, &textLimitErr) {
				writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "text_policy_exceeded", checkpointErr.Error())
				return
			}
			writeOpenAIError(w, http.StatusConflict, "checkpoint_error", checkpointErr.Error())
			return
		}
		execution = &checkpointExecution{turn: checkpointTurn}
		if needsTextValidation {
			r = withCallerTextValidated(r)
		}
		r = r.WithContext(withCheckpointExecution(r.Context(), execution))
		defer execution.Abort()
	} else if needsTextValidation {
		if err := validateCallerText(body.Messages, settings.TextInputLimitUTF16); err != nil {
			writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "text_policy_exceeded", err.Error())
			return
		}
		r = withCallerTextValidated(r)
	}
	if execution != nil && execution.turn != nil && execution.turn.turn != nil {
		ledger = checkpointExecutionLedger(execution.turn.turn.ToolLedger, execution.turn.turn.Outbound)
	}
	if body.Stream && (responseFormat != nil || len(ledger.Pending) > 0 || ledger.hasFailedCompletedEvidence() || (len(ledger.Completed) > 0 && len(body.Tools) > 0)) {
		// Tool-history streams must not commit upstream deltas before duplicate
		// suppression and completion-evidence validation have run. Execute the
		// existing non-stream pipeline once, then serialize its validated
		// text/image/tool result back to SSE. Plain text streams without exposed
		// tools keep the normal low-latency path.
		buffered, bufferedRaw, status, bufferedErr := s.runOpenAIAdapter(r, body)
		if status >= http.StatusBadRequest {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(bufferedRaw)
			return
		}
		if bufferedErr != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "buffered continuation response: "+bufferedErr.Error())
			return
		}
		selected := httptest.NewRecorder()
		writeErr := writeBufferedChatCompletionStream(selected, buffered, resolution, body.Tools, body.ToolChoice, nativePolicy)
		if errors.Is(writeErr, errUnavailableToolCall) && normalizedToolChoiceMode(body.ToolChoice) == "auto" {
			fallbackBody := textOnlyFallbackRequest(body)
			fallback, fallbackRaw, fallbackStatus, fallbackErr := s.runOpenAIAdapter(r, fallbackBody)
			if fallbackStatus >= http.StatusBadRequest {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(fallbackStatus)
				_, _ = w.Write(fallbackRaw)
				return
			}
			if fallbackErr != nil {
				writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "buffered text fallback response: "+fallbackErr.Error())
				return
			}
			selected = httptest.NewRecorder()
			writeErr = writeBufferedChatCompletionStream(selected, fallback, resolution, nil, "none", nativePolicy)
		}
		if writeErr != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", writeErr.Error())
			return
		}
		if ownsCheckpoint {
			if err := execution.Accept(); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
				return
			}
		}
		replayRecordedResponse(w, selected)
		return
	}
	// Full caller history is used above only for protocol validation and buffered
	// continuation selection. Model-visible evidence must be rebuilt from the
	// exact checkpoint outbound delta so accepted tool results are not replayed.
	if execution == nil || execution.turn == nil || execution.turn.turn == nil {
		ledger = buildAgentLedger(body.Messages)
	}
	// Preserve role boundaries when adapting OpenAI messages to ChatHub's
	// single message.text field. This keeps system/developer instructions,
	// history, and the current user turn distinguishable.
	var prompt string
	prompt, body.Attachments = flattenPromptMessages(body.Messages, body.Attachments)
	log.Printf("[req-trace] id=%s stage=prompt_flattened prompt_len=%d attachments=%d", requestID, len(prompt), len(body.Attachments))
	log.Printf("[multimodal-entry] messages=%d attachments=%d prompt_len=%d", len(body.Messages), len(body.Attachments), len(prompt))
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(body.Attachments) == 0 {
		http.Error(w, "messages required", http.StatusBadRequest)
		return
	}
	downgraded, err := normalizeCompatibilityParameters(body.Attachments, body.Verbosity)
	if err != nil {
		writeOpenAIErrorCode(w, http.StatusBadRequest, "invalid_request_error", "invalid_attachment", err.Error())
		return
	}
	setDowngradedParameters(w, downgraded)

	acc, err := s.activeAccount()
	if err != nil {
		log.Printf("[account] resolve_failed code=account_unavailable")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("[account] selected token_present=%t identity_ready=%t", acc.AccessToken != "", acc.OID != "" && acc.TID != "")
	if acc.OID == "" || acc.TID == "" {
		if o, t := extractOIDTID(acc.AccessToken); o != "" {
			acc.OID, acc.TID = o, t
		}
	}
	if acc.OID == "" || acc.TID == "" {
		http.Error(w, "account missing oid/tid", http.StatusBadRequest)
		return
	}

	// Normalize tools once. Selection is always made by the upstream model;
	// the gateway only validates its structured decision and converts protocols.
	toolMaps := toolDefinitionMaps(body.Tools)
	if body.ToolChoice == nil && len(toolMaps) > 0 {
		body.ToolChoice = "auto"
	}
	planningMode := s.settings.get().ToolPlanningMode
	answerTools := []chathub.Tool(nil)
	answerToolChoice := any(nil)
	allowAnswerToolCalls := planningMode == "native"
	if allowAnswerToolCalls {
		answerTools = body.Tools
		answerToolChoice = body.ToolChoice
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.settings.get().ChatTimeoutSeconds)*time.Second)
	defer cancel()
	account, err := s.chatHubAccount(ctx, acc, body.Attachments)
	if err != nil {
		writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "resource_token_unavailable", err.Error())
		return
	}
	// The stream is opened by the actual response path below. Do not emit a
	// tool preamble here: a request may contain tools in its schema while still
	// being an ordinary text question.
	streamPrimed := false
	var routerSearchEvidence chathub.Result
	// Streaming requests must not wait for the synchronous tool router. This
	// path forwards ordinary upstream text deltas immediately; tool routing for
	// non-streaming requests remains below until the event-level tool protocol
	// is available end-to-end.
	if planningMode == "router" && body.Stream && len(toolMaps) > 0 && fmt.Sprint(body.ToolChoice) != "none" {
		// Preserve the existing validated tool router for streaming tool turns.
		// Only fall through to text streaming when the router explicitly selects
		// no tool; this prevents a natural-language preamble from becoming a
		// completed assistant turn with the actual call lost.
		routePrompt := modelToolRouterPrompt(prompt+"\n"+ledger.RouterContext(), toolMaps, body.ToolChoice, configuredRequestToolCallLimit(body, s.settings))
		log.Printf("[req-trace] id=%s stage=router_start prompt_len=%d", requestID, len(routePrompt))
		routeRes, routeErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: routePrompt, Tone: tone, Attachments: body.Attachments}))
		log.Printf("[req-trace] id=%s stage=router_return elapsed_ms=%d err=%t", requestID, time.Since(startedAt).Milliseconds(), routeErr != nil)
		if routeErr != nil {
			if writeCanonicalTerminalError(w, routeErr) {
				return
			}
			http.Error(w, "tool router: "+routeErr.Error(), http.StatusBadGateway)
			return
		}
		if _, ok := requireSafeNativeToolEmission(w, routeRes, body.Tools); !ok {
			return
		}
		execution.Observe(routeRes)
		mergeSearchEvidence(&routerSearchEvidence, routeRes)
		carrier := routeRes
		carrierIsRouteResult := true
		calls, parsed := parseModelToolDecision(routeRes.Text, toolMaps, body.ToolChoice)
		calls = filterKnownCalls(calls, ledger)
		if !parsed {
			repairRes, repairErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: modelToolRouterRepairPrompt(routeRes.Text), Tone: tone, Attachments: body.Attachments}))
			if repairErr != nil && writeCanonicalTerminalError(w, repairErr) {
				return
			}
			if repairErr == nil {
				if _, ok := requireSafeNativeToolEmission(w, repairRes, body.Tools); !ok {
					return
				}
				execution.Observe(repairRes)
				mergeSearchEvidence(&routerSearchEvidence, repairRes)
				carrier = repairRes
				carrierIsRouteResult = false
				calls, parsed = parseModelToolDecision(repairRes.Text, toolMaps, body.ToolChoice)
				calls = filterKnownCalls(calls, ledger)
			}
		}
		if parsed {
			if answer, ok := parseModelToolDirectAnswer(carrier.Text); ok {
				if !carrierIsRouteResult {
					mergeSearchEvidence(&carrier, routerSearchEvidence)
				}
				carrier.Text = answer
				carrier.Images = validImageURLs(carrier.Images)
				if _, err := s.materializeArtifacts(ctx, r, &carrier); err != nil {
					writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "artifact_materialization_failed", err.Error())
					return
				}
				if !requireUsableChatResult(w, carrier, body.Tools) {
					return
				}
				if responseFormat != nil {
					formatted, formatErr := validateResponseFormatText(carrier.Text, responseFormat)
					if formatErr != nil {
						writeChatStreamError(w, "response_format_validation_failed", formatErr.Error())
						return
					}
					carrier.Text = formatted
				}
				if err := completeCheckpointExecution(execution, ownsCheckpoint, carrier, assistantTextCheckpointMessage(carrier.Text, carrier.Images)); err != nil {
					writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
					return
				}
				response := routerDirectAnswerCompletion("chatcmpl-"+uuid.NewString(), carrier, resolution, nativePolicy, responseFormat)
				if err := writeBufferedChatCompletionStream(w, response, resolution, body.Tools, body.ToolChoice, nativePolicy); err != nil {
					writeOpenAIError(w, http.StatusBadGateway, "upstream_error", err.Error())
				}
				return
			}
		}
		if parsed && len(calls) > 0 {
			scope := fmt.Sprintf("%d:%v:stream", len(body.Messages), completedCallIDs(ledger))
			for i := range calls {
				calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
			}
			calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
			if !carrierIsRouteResult {
				mergeSearchEvidence(&carrier, routerSearchEvidence)
			}
			carrier.Text = ""
			if err := completeCheckpointExecution(execution, ownsCheckpoint, carrier, assistantToolCheckpointMessage(calls, carrier, true)); err != nil {
				writeChatStreamError(w, "checkpoint_error", err.Error())
				return
			}
			_ = writeToolResponseWithPolicy(w, "chatcmpl-"+uuid.NewString(), resolution.ResponseModel, true, calls, carrier, resolution, nativePolicy)
			return
		}
		mode := normalizedToolChoiceMode(body.ToolChoice)
		if strings.HasPrefix(mode, "named:") {
			http.Error(w, "model did not select the requested tool", http.StatusBadGateway)
			return
		}
		if mode == "required" {
			defs, _ := json.Marshal(toolMaps)
			retryText := `Select at least one required next tool call from FUNCTION_DEFINITIONS. Validate every argument against its schema. Return JSON only as {"calls":[{"name":"function_name","arguments":{}}]}.
APPLICATION_REQUEST_AND_EVIDENCE:
` + prompt + "\n" + ledger.RouterContext() + "\nFUNCTION_DEFINITIONS:\n" + string(defs)
			retryRes, retryErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: retryText, Tone: tone, Attachments: body.Attachments}))
			if retryErr != nil && writeCanonicalTerminalError(w, retryErr) {
				return
			}
			if retryErr == nil {
				if _, ok := requireSafeNativeToolEmission(w, retryRes, body.Tools); !ok {
					return
				}
				execution.Observe(retryRes)
				calls, parsed = parseModelToolDecision(retryRes.Text, toolMaps, body.ToolChoice)
				calls = filterKnownCalls(calls, ledger)
				if parsed && len(calls) > 0 {
					scope := fmt.Sprintf("%d:%v:stream-required-retry", len(body.Messages), completedCallIDs(ledger))
					for i := range calls {
						calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
					}
					calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
					mergeSearchEvidence(&retryRes, routerSearchEvidence)
					retryRes.Text = ""
					if err := completeCheckpointExecution(execution, ownsCheckpoint, retryRes, assistantToolCheckpointMessage(calls, retryRes, true)); err != nil {
						writeChatStreamError(w, "checkpoint_error", err.Error())
						return
					}
					_ = writeToolResponseWithPolicy(w, "chatcmpl-"+uuid.NewString(), resolution.ResponseModel, true, calls, retryRes, resolution, nativePolicy)
					return
				}
			}
			http.Error(w, "model did not select a required tool after constrained retry", http.StatusBadGateway)
			return
		}
		if !parsed && mode != "auto" {
			http.Error(w, "model returned an invalid tool routing decision", http.StatusBadGateway)
			return
		}
	}
	if body.Stream {
		answerPrompt := prompt + "\n" + ledger.RouterContext() + "\nFINAL ANSWER RULE: Answer the user directly. If a tool is explicitly required, emit its structured call; otherwise return ordinary text."
		log.Printf("[req-trace] id=%s stage=answer_start prompt_len=%d", requestID, len(answerPrompt))
		answerReq := execution.Request(chathub.Request{Text: answerPrompt, Tone: tone, Attachments: body.Attachments, Tools: answerTools, ToolChoice: answerToolChoice, ToolCallLimit: configuredRequestToolCallLimit(body, s.settings)})
		id := "chatcmpl-" + uuid.NewString()
		model := resolution.ResponseModel
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()
		var text strings.Builder
		var artifactStreamBuffer strings.Builder
		var pending strings.Builder
		var visible strings.Builder
		var streamedTools []detectedToolCall
		streamedReasoning := map[string]struct{}{}
		first := true
		emitReasoning := func(part string) {
			part = strings.TrimSpace(part)
			if part == "" {
				return
			}
			if _, duplicate := streamedReasoning[part]; duplicate {
				return
			}
			streamedReasoning[part] = struct{}{}
			delta := map[string]any{"reasoning_content": part}
			if first {
				delta["role"] = "assistant"
				first = false
			}
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}, "m365": withNativePolicy(resolution.metadata(), nativePolicy)}
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
			flusher.Flush()
		}
		emitText := func(part string) {
			if part == "" {
				return
			}
			visible.WriteString(part)
			delta := map[string]any{"content": part}
			if first {
				delta["role"] = "assistant"
				first = false
			}
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}, "m365": withNativePolicy(resolution.metadata(), nativePolicy)}
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
			flusher.Flush()
		}
		emitImages := func(images []string) {
			if len(images) == 0 {
				return
			}
			delta := map[string]any{"images": images}
			if first {
				delta["role"] = "assistant"
				first = false
			}
			chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}, "m365": withNativePolicy(resolution.metadata(), nativePolicy)}
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(chunk))
			flusher.Flush()
		}
		res, err := s.chat.ChatWithEvents(ctx, account, answerReq, func(ev chathub.StreamEvent) error {
			if ev.Kind == "reasoning" {
				emitReasoning(ev.Text)
				return nil
			}
			if ev.Kind == "tool" && ev.ToolName != "" {
				emitClientCall, _, guardErr := classifyNativeStreamToolEvent(ev, body.Tools)
				if guardErr != nil {
					return guardErr
				}
				if emitClientCall {
					streamedTools = append(streamedTools, detectedToolCall{ID: "call_" + uuid.NewString(), Name: ev.ToolName, Arguments: ev.Arguments})
				}
				return nil
			}
			if ev.Kind != "text" || ev.Text == "" {
				return nil
			}
			text.WriteString(ev.Text)
			artifactStreamBuffer.WriteString(ev.Text)
			pending.WriteString(releaseArtifactSafePrefix(&artifactStreamBuffer))
			v := pending.String()
			// If the text contains a bash block or a JSON command, don't emit it as text
			// It will be caught by fencedToolCalls after the stream completes
			if strings.Contains(v, "```bash") || strings.Contains(v, "\"command\"") {
				return nil
			}
			if i := strings.Index(v, "```"); i >= 0 {
				emitText(v[:i])
				pending.Reset()
				pending.WriteString(v[i:])
				return nil
			}
			if runeCount := utf8.RuneCountInString(v); runeCount > 8 {
				cut := 0
				seen := 0
				for i := range v {
					if seen == runeCount-8 {
						cut = i
						break
					}
					seen++
				}
				emitText(v[:cut])
				pending.Reset()
				pending.WriteString(v[cut:])
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, errNativeMutationBlocked) {
				writeChatStreamError(w, "native_mutation_blocked", err.Error())
				return
			}
			if writeCanonicalTerminalStreamError(w, err) {
				return
			}
			writeChatStreamError(w, "upstream_error", upstreamError(err))
			return
		}
		mergeSearchEvidence(&res, routerSearchEvidence)
		res.Images = validImageURLs(res.Images)
		artifactMarkdown, artifactErr := s.materializeArtifacts(ctx, r, &res)
		if artifactErr != nil {
			writeChatStreamError(w, "artifact_materialization_failed", artifactErr.Error())
			return
		}
		// Reconcile delayed URL text against the fully materialized result. The
		// already emitted prefix must remain exact; otherwise fail closed rather
		// than expose or duplicate an upstream artifact reference.
		finalText := res.Text
		if strings.TrimSpace(finalText) == "" && artifactMarkdown == "" {
			finalText = text.String()
		}
		if !strings.HasPrefix(finalText, visible.String()) {
			writeChatStreamError(w, "artifact_materialization_failed", errArtifactMaterialization.Error())
			return
		}
		text.Reset()
		text.WriteString(finalText)
		pending.Reset()
		pending.WriteString(strings.TrimPrefix(finalText, visible.String()))
		detectedCalls := streamedTools
		if len(detectedCalls) == 0 {
			detectedCalls = fencedToolCalls(text.String(), toolMaps, body.ToolChoice)
		}
		detectedCalls, rejectedUnavailableCalls := filterAllowedToolCalls(detectedCalls, toolMaps, body.ToolChoice)
		streamedToolCalls := len(streamedTools) > 0 && len(detectedCalls) > 0
		calls := filterKnownCalls(detectedCalls, ledger)
		suppressedKnownCalls := len(detectedCalls) > len(calls)
		rejectedRouterCalls := !allowAnswerToolCalls && len(calls) > 0
		if rejectedRouterCalls {
			calls = nil
		}
		if len(calls) > 0 {
			if streamedToolCalls {
				emitText(pending.String())
			}
			calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
			if err := completeCheckpointExecution(execution, ownsCheckpoint, res, assistantToolCheckpointMessageWithContent(calls, visible.String(), res.Images)); err != nil {
				writeChatStreamError(w, "checkpoint_error", err.Error())
				return
			}
			res.Text = text.String()
			_ = writeToolResponseWithPolicy(w, id, model, true, calls, res, resolution, nativePolicy, true)
			return
		}
		if suppressedKnownCalls {
			if streamedToolCalls {
				if strings.TrimSpace(text.String()) != "" {
					emitText(pending.String())
				} else {
					emitText(suppressedKnownCallResponse(ledger))
				}
			} else {
				pending.Reset()
				prefix := strings.TrimSpace(text.String())
				if fence := strings.Index(prefix, "```"); fence >= 0 {
					prefix = strings.TrimSpace(prefix[:fence])
				}
				if first {
					if prefix != "" && completionEvidenceAllows(prefix, ledger) {
						emitText(prefix)
					} else {
						emitText(suppressedKnownCallResponse(ledger))
					}
				}
			}
			emitImages(res.Images)
			if err := completeCheckpointExecution(execution, ownsCheckpoint, res, assistantTextCheckpointMessage(visible.String(), res.Images)); err != nil {
				writeChatStreamError(w, "checkpoint_error", err.Error())
				return
			}
			writeTextStreamEndWithPolicy(w, id, model, resolution, nativePolicy, res)
			return
		}
		if strings.TrimSpace(text.String()) == "" && len(res.Images) == 0 {
			if rejectedUnavailableCalls || rejectedRouterCalls {
				writeChatStreamError(w, "invalid_tool_call_stream", "ChatHub returned a tool call that was not exposed by the client")
				return
			}
			writeChatStreamError(w, "upstream_empty_response", "ChatHub returned no text, tool call, or image result")
			return
		}
		emitText(pending.String())
		emitImages(res.Images)
		if err := completeCheckpointExecution(execution, ownsCheckpoint, res, assistantTextCheckpointMessage(visible.String(), res.Images)); err != nil {
			writeChatStreamError(w, "checkpoint_error", err.Error())
			return
		}
		writeTextStreamEndWithPolicy(w, id, model, resolution, nativePolicy, res)
		return
	}
	// Ask the upstream model to select and validate the next tool. The gateway
	// remains tool-agnostic; it only validates and serializes the decision.
	if planningMode == "router" && len(toolMaps) > 0 && fmt.Sprint(body.ToolChoice) != "none" {
		routePrompt := modelToolRouterPrompt(prompt+"\n"+ledger.RouterContext(), toolMaps, body.ToolChoice, configuredRequestToolCallLimit(body, s.settings))
		routeRes, routeErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: routePrompt, Tone: tone, Attachments: body.Attachments}))
		if routeErr != nil {
			if writeCanonicalTerminalError(w, routeErr) {
				return
			}
			http.Error(w, "tool router: "+routeErr.Error(), http.StatusBadGateway)
			return
		}
		if _, ok := requireSafeNativeToolEmission(w, routeRes, body.Tools); !ok {
			return
		}
		execution.Observe(routeRes)
		mergeSearchEvidence(&routerSearchEvidence, routeRes)
		carrier := routeRes
		carrierIsRouteResult := true
		calls, parsed := parseModelToolDecision(routeRes.Text, toolMaps, body.ToolChoice)
		calls = filterKnownCalls(calls, ledger)
		if !parsed {
			repairRes, repairErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: modelToolRouterRepairPrompt(routeRes.Text), Tone: tone, Attachments: body.Attachments}))
			if repairErr != nil && writeCanonicalTerminalError(w, repairErr) {
				return
			}
			if repairErr == nil {
				if _, ok := requireSafeNativeToolEmission(w, repairRes, body.Tools); !ok {
					return
				}
				execution.Observe(repairRes)
				mergeSearchEvidence(&routerSearchEvidence, repairRes)
				carrier = repairRes
				carrierIsRouteResult = false
				calls, parsed = parseModelToolDecision(repairRes.Text, toolMaps, body.ToolChoice)
				calls = filterKnownCalls(calls, ledger)
			}
			if !parsed {
				mode := normalizedToolChoiceMode(body.ToolChoice)
				if mode != "auto" && mode != "required" {
					http.Error(w, "model returned an invalid tool routing decision", http.StatusBadGateway)
					return
				}
			}
		}
		if parsed {
			if answer, ok := parseModelToolDirectAnswer(carrier.Text); ok {
				if !carrierIsRouteResult {
					mergeSearchEvidence(&carrier, routerSearchEvidence)
				}
				carrier.Text = answer
				carrier.Images = validImageURLs(carrier.Images)
				if _, err := s.materializeArtifacts(ctx, r, &carrier); err != nil {
					writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "artifact_materialization_failed", err.Error())
					return
				}
				if !requireUsableChatResult(w, carrier, body.Tools) {
					return
				}
				if responseFormat != nil {
					formatted, formatErr := validateResponseFormatText(carrier.Text, responseFormat)
					if formatErr != nil {
						writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "response_format_validation_failed", formatErr.Error())
						return
					}
					carrier.Text = formatted
				}
				if err := completeCheckpointExecution(execution, ownsCheckpoint, carrier, assistantTextCheckpointMessage(carrier.Text, carrier.Images)); err != nil {
					writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
					return
				}
				jsonOut(w, routerDirectAnswerCompletion("chatcmpl-"+uuid.NewString(), carrier, resolution, nativePolicy, responseFormat))
				return
			}
		}
		if len(calls) > 0 {
			scope := fmt.Sprintf("%d:%v", len(body.Messages), completedCallIDs(ledger))
			for i := range calls {
				calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
			}
			calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
			if !carrierIsRouteResult {
				mergeSearchEvidence(&carrier, routerSearchEvidence)
			}
			carrier.Text = ""
			if err := completeCheckpointExecution(execution, ownsCheckpoint, carrier, assistantToolCheckpointMessage(calls, carrier, body.Stream)); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
				return
			}
			_ = writeToolResponseWithPolicy(w, "chatcmpl-"+uuid.NewString(), resolution.ResponseModel, body.Stream, calls, carrier, resolution, nativePolicy, streamPrimed)
			return
		}
		if strings.HasPrefix(normalizedToolChoiceMode(body.ToolChoice), "named:") {
			http.Error(w, "model did not select the requested tool", http.StatusBadGateway)
			return
		}
		if fmt.Sprint(body.ToolChoice) == "required" {
			defs, _ := json.Marshal(toolMaps)
			retryText := `Select at least one required next tool call from FUNCTION_DEFINITIONS. Validate every argument against its schema. Return JSON only as {"calls":[{"name":"function_name","arguments":{}}]}.
APPLICATION_REQUEST_AND_EVIDENCE:
` + prompt + "\n" + ledger.RouterContext() + "\nFUNCTION_DEFINITIONS:\n" + string(defs)
			retryRes, retryErr := s.chat.Chat(ctx, account, execution.Request(chathub.Request{Text: retryText, Tone: tone, Attachments: body.Attachments}))
			if retryErr != nil && writeCanonicalTerminalError(w, retryErr) {
				return
			}
			if retryErr == nil {
				if _, ok := requireSafeNativeToolEmission(w, retryRes, body.Tools); !ok {
					return
				}
				execution.Observe(retryRes)
				calls, parsed = parseModelToolDecision(retryRes.Text, toolMaps, body.ToolChoice)
				calls = filterKnownCalls(calls, ledger)
				if parsed && len(calls) > 0 {
					scope := fmt.Sprintf("%d:%v:required-retry", len(body.Messages), completedCallIDs(ledger))
					for i := range calls {
						calls[i].ID = scopedCallID(calls[i].Name, string(calls[i].Arguments), i, scope)
					}
					calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
					mergeSearchEvidence(&retryRes, routerSearchEvidence)
					retryRes.Text = ""
					if err := completeCheckpointExecution(execution, ownsCheckpoint, retryRes, assistantToolCheckpointMessage(calls, retryRes, body.Stream)); err != nil {
						writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
						return
					}
					_ = writeToolResponseWithPolicy(w, "chatcmpl-"+uuid.NewString(), resolution.ResponseModel, body.Stream, calls, retryRes, resolution, nativePolicy, streamPrimed)
					return
				}
			}
			http.Error(w, "model did not select a required tool after constrained retry", http.StatusBadGateway)
			return
		}
	}
	answerPrompt := prompt + "\n" + ledger.RouterContext() + "\nFINAL ANSWER RULE: Report only actions supported by completed tool results. If the goal is not fully verified, state exactly what remains unconfirmed."
	answerReq := execution.Request(chathub.Request{Text: answerPrompt, Tone: tone, Attachments: body.Attachments, Tools: answerTools, ToolChoice: answerToolChoice, ToolCallLimit: configuredRequestToolCallLimit(body, s.settings)})
	res, err := s.chat.Chat(ctx, account, answerReq)
	if err != nil {
		if writeCanonicalTerminalError(w, err) {
			return
		}
		http.Error(w, upstreamError(err), http.StatusBadGateway)
		return
	}
	nativeScan, safe := requireSafeNativeToolEmission(w, res, body.Tools)
	if !safe {
		return
	}
	mergeSearchEvidence(&res, routerSearchEvidence)
	res.Images = validImageURLs(res.Images)
	if _, err := s.materializeArtifacts(ctx, r, &res); err != nil {
		writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "artifact_materialization_failed", err.Error())
		return
	}
	if !requireUsableChatResult(w, res, body.Tools) {
		return
	}

	model := resolution.ResponseModel
	id := "chatcmpl-" + uuid.NewString()
	knownCallSuppressed := false
	rejectedAnswerToolCall := false
	if detected := fencedToolCalls(res.Text, toolMaps, body.ToolChoice); len(detected) > 0 {
		detected, _ = filterAllowedToolCalls(detected, toolMaps, body.ToolChoice)
		calls := filterKnownCalls(detected, ledger)
		suppressedKnownCalls := len(detected) > len(calls)
		if len(calls) > 0 && allowAnswerToolCalls {
			calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
			toolResult := res
			toolResult.Text = ""
			if err := completeCheckpointExecution(execution, ownsCheckpoint, toolResult, assistantToolCheckpointMessage(calls, toolResult, body.Stream)); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
				return
			}
			_ = writeToolResponseWithPolicy(w, id, model, body.Stream, calls, toolResult, resolution, nativePolicy)
			return
		}
		if len(calls) > 0 && !allowAnswerToolCalls {
			rejectedAnswerToolCall = true
		}
		if (len(calls) == 0 && len(detected) > 0) || suppressedKnownCalls {
			knownCallSuppressed = true
			prefix := strings.TrimSpace(res.Text)
			if fence := strings.Index(prefix, "```"); fence >= 0 {
				prefix = strings.TrimSpace(prefix[:fence])
			}
			res.Text = prefix
		}
	}
	if detected := nativeScan.Calls; len(detected) > 0 {
		var rejectedUnavailableCalls bool
		detected, rejectedUnavailableCalls = filterAllowedToolCalls(detected, toolMaps, body.ToolChoice)
		if rejectedUnavailableCalls && len(detected) == 0 {
			rejectedAnswerToolCall = true
		}
		calls := filterKnownCalls(detected, ledger)
		suppressedKnownCalls := len(detected) > len(calls)
		if len(calls) > 0 && allowAnswerToolCalls {
			calls = limitToolCalls(calls, requestToolCallLimit(body, calls, s.settings))
			if err := completeCheckpointExecution(execution, ownsCheckpoint, res, assistantToolCheckpointMessage(calls, res, body.Stream)); err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
				return
			}
			_ = writeToolResponseWithPolicy(w, id, model, body.Stream, calls, res, resolution, nativePolicy)
			return
		}
		if len(calls) > 0 && !allowAnswerToolCalls {
			rejectedAnswerToolCall = true
		}
		if suppressedKnownCalls || (len(detected) > 0 && len(calls) == 0) {
			knownCallSuppressed = true
		}
	}
	if rejectedAnswerToolCall && !knownCallSuppressed {
		mode := normalizedToolChoiceMode(body.ToolChoice)
		if strings.HasPrefix(mode, "named:") || mode == "required" || (strings.TrimSpace(res.Text) == "" && len(res.Images) == 0) {
			writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "invalid_tool_call", "ChatHub returned a tool call that was not permitted by tool_choice")
			return
		}
	}
	if !completionEvidenceAllows(res.Text, ledger) {
		res.Text = unconfirmedToolOutcomeResponse
	}
	if knownCallSuppressed && strings.TrimSpace(res.Text) == "" {
		res.Text = suppressedKnownCallResponse(ledger)
	}
	created := time.Now().Unix()

	if responseFormat != nil {
		formatted, formatErr := validateResponseFormatText(res.Text, responseFormat)
		if formatErr != nil {
			writeOpenAIErrorCode(w, http.StatusBadGateway, "upstream_error", "response_format_validation_failed", formatErr.Error())
			return
		}
		res.Text = formatted
	}
	content := any(res.Text)
	if len(res.Images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": res.Text}}
		for _, u := range res.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
		}
		content = parts
	}
	if err := completeCheckpointExecution(execution, ownsCheckpoint, res, oaiMsg{Role: "assistant", Content: content}); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "checkpoint_error", err.Error())
		return
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if reasoning := chathub.ReasoningContent(res.Events); reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	jsonOut(w, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
		"m365": withNativePolicy(compatM365Metadata(res, resolution), nativePolicy),
	})
}

func routerDirectAnswerCompletion(id string, result chathub.Result, resolution routeResolution, policy nativePolicySnapshot, _ *responseFormat) map[string]any {
	text := result.Text
	content := any(text)
	if len(result.Images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": text}}
		for _, url := range result.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		}
		content = parts
	}
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning := chathub.ReasoningContent(result.Events); reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   resolution.ResponseModel,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}},
		"m365":    withNativePolicy(compatM365Metadata(result, resolution), policy),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func extractOIDTID(accessToken string) (oid, tid string) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ""
	}
	if v, ok := m["oid"].(string); ok {
		oid = v
	}
	if v, ok := m["tid"].(string); ok {
		tid = v
	}
	return oid, tid
}
