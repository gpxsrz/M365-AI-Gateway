package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"m365-native/internal/auth"
	"net/http"
	"os"
	"path/filepath"
)

type browserPKCECaptureRequest struct {
	AuthorizationURL string
	RedirectURI      string
	State            string
	ProfileDir       string
}

type browserPKCECapturedAuthorization struct {
	Code  string
	State string
	Error string
}

type browserPKCERunner func(context.Context, browserPKCECaptureRequest) (browserPKCECapturedAuthorization, error)

type pkceStartResult struct {
	State            string
	AuthorizationURL string
	Target           pkceProfileTarget
}

func defaultClientOAuthConfig() auth.OAuthConfig {
	return auth.OAuthConfig{
		ClientID:          auth.DefaultClientID,
		Authority:         auth.DefaultAuthority,
		RedirectURI:       auth.DefaultRedirectURI,
		Scope:             auth.DefaultScope,
		AuthorizeEndpoint: auth.DefaultAuthority + "/oauth2/v2.0/authorize",
		TokenEndpoint:     auth.DefaultAuthority + "/oauth2/v2.0/token",
	}
}

func (s *Server) defaultBrowserOAuthConfig() auth.OAuthConfig {
	if s.browserPKCEConfig != nil {
		return s.browserPKCEConfig()
	}
	return defaultClientOAuthConfig()
}

func (s *Server) beginPKCEForTarget(target pkceProfileTarget) (pkceStartResult, *pkceStartFailure) {
	verifier, err := auth.Verifier()
	if err != nil {
		return pkceStartResult{}, &pkceStartFailure{Status: http.StatusInternalServerError, Code: "oauth_pkce_verifier_failed", Message: "無法建立 PKCE 驗證資料"}
	}
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return pkceStartResult{}, &pkceStartFailure{Status: http.StatusInternalServerError, Code: "oauth_state_failed", Message: "無法建立 OAuth state"}
	}
	state := hex.EncodeToString(stateBytes)
	now := s.oauthNow()
	s.mu.Lock()
	if s.pkce == nil {
		s.pkce = map[string]pendingPKCE{}
	}
	pruned := s.prunePKCELocked(now)
	s.pkce[state] = pendingPKCE{
		Verifier:         verifier,
		RedirectURI:      target.OAuth.RedirectURI,
		OAuth:            target.OAuth,
		ProfileID:        target.ProfileID,
		Staged:           target.Staged,
		DiscardOnFailure: target.Created,
		Created:          now,
		Status:           "pending",
	}
	s.mu.Unlock()
	s.discardPrunedPKCEProfiles(pruned)
	return pkceStartResult{
		State: state,
		AuthorizationURL: auth.AuthorizationURL(
			target.OAuth.AuthorizeEndpoint,
			target.OAuth.ClientID,
			target.OAuth.RedirectURI,
			state,
			auth.Challenge(verifier),
			target.OAuth.Scope,
		),
		Target: target,
	}, nil
}

func (s *Server) startDefaultClientBrowserPKCE(w http.ResponseWriter, r *http.Request) {
	setOAuthCallbackHeaders(w)
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	activeStore := s.activeTokenStore()
	if s.oauthProfiles == nil || activeStore == nil {
		writeOpenAIErrorCode(w, http.StatusServiceUnavailable, "oauth_error", "oauth_profile_manager_unavailable", "OAuth profile 管理功能目前無法使用")
		return
	}
	if !s.claimBrowserPKCESession() {
		writeOpenAIErrorCode(w, http.StatusConflict, "oauth_error", "oauth_browser_session_active", "已有瀏覽器授權工作階段正在進行")
		return
	}
	releaseSession := true
	defer func() {
		if releaseSession {
			s.releaseBrowserPKCESession()
		}
	}()
	if _, _, err := s.oauthProfiles.ActiveStore(); err != nil {
		writeOpenAIErrorCode(w, http.StatusInternalServerError, "oauth_error", "oauth_active_profile_unavailable", "Active OAuth profile 無法使用")
		return
	}
	manifest, store, err := s.oauthProfiles.Stage(s.defaultBrowserOAuthConfig())
	if err != nil {
		writeOpenAIErrorCode(w, http.StatusInternalServerError, "oauth_error", "oauth_candidate_stage_failed", "無法建立候選 OAuth profile")
		return
	}
	target := pkceProfileTarget{
		ProfileID: manifest.ProfileID,
		Kind:      manifest.Kind,
		Staged:    true,
		Created:   true,
		OAuth:     manifest.OAuth,
		Store:     store,
	}
	started, failure := s.beginPKCEForTarget(target)
	if failure != nil {
		_ = s.oauthProfiles.Discard(manifest.ProfileID)
		writePKCEStartFailure(w, *failure)
		return
	}

	runner := s.browserPKCERun
	if runner == nil {
		runner = runBrowserPKCECapture
	}
	profileDir := browserPKCEProfileDir(activeStore.Path())
	ctx, cancel := context.WithTimeout(context.Background(), pkceTransactionTTL)
	releaseSession = false
	go func() {
		defer cancel()
		defer s.releaseBrowserPKCESession()
		captured, err := runner(ctx, browserPKCECaptureRequest{
			AuthorizationURL: started.AuthorizationURL,
			RedirectURI:      target.OAuth.RedirectURI,
			State:            started.State,
			ProfileDir:       profileDir,
		})
		if err != nil {
			code := "oauth_browser_capture_failed"
			message := "瀏覽器未能擷取 Microsoft 授權回呼，請重新開始授權"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				code = "oauth_browser_capture_timeout"
				message = "瀏覽器授權等待逾時，請重新開始授權"
			}
			s.failPendingPKCE(started.State, code, message)
			return
		}
		_ = s.completeCapturedPKCEAuthorization(started.State, captured)
	}()

	jsonOut(w, map[string]any{
		"status":           "browser_pkce_started",
		"state":            started.State,
		"oauthProfileKind": target.Kind,
		"staged":           true,
	})
}

func (s *Server) claimBrowserPKCESession() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browserPKCEActive {
		return false
	}
	s.browserPKCEActive = true
	return true
}

func (s *Server) releaseBrowserPKCESession() {
	s.mu.Lock()
	s.browserPKCEActive = false
	s.mu.Unlock()
}

func browserPKCEProfileDir(tokenPath string) string {
	if explicit := os.Getenv("M365_BROWSER_PROFILE"); explicit != "" {
		return explicit
	}
	return filepath.Join(filepath.Dir(tokenPath), "browser-profile")
}

func (s *Server) failPendingPKCE(state, errorCode, message string) {
	s.mu.Lock()
	transaction, ok := s.pkce[state]
	if ok && transaction.Status == "pending" {
		transaction.Status = "error"
		transaction.Verifier = ""
		transaction.ErrorCode = errorCode
		transaction.Error = message
		s.pkce[state] = transaction
	} else {
		ok = false
	}
	s.mu.Unlock()
	if ok {
		s.discardPKCEProfileOnFailure(transaction)
	}
}

func (s *Server) completeCapturedPKCEAuthorization(expectedState string, captured browserPKCECapturedAuthorization) *oauthCallbackFailure {
	input := trimOAuthCallbackInput(oauthCallbackInput{
		Code:  captured.Code,
		State: captured.State,
		Error: captured.Error,
	})
	if input.State != expectedState {
		failure := &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_state_mismatch", Message: "瀏覽器擷取的 OAuth state 不符合目前授權工作階段"}
		s.failPendingPKCE(expectedState, failure.Code, failure.Message)
		return failure
	}
	if failure := validateOAuthCallbackMaterial(input); failure != nil {
		s.failPendingPKCE(expectedState, failure.Code, failure.Message)
		return failure
	}
	_, failure := s.completePKCEInput(input)
	return failure
}
