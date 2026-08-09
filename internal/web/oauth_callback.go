package web

import (
	"encoding/json"
	"errors"
	"io"
	"m365-native/internal/auth"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	pkceTransactionTTL       = 10 * time.Minute
	pkceTransactionRetention = 30 * time.Minute
	maxOAuthCallbackBody     = 16 << 10
)

type oauthCallbackInput struct {
	CallbackURL      string `json:"callback_url"`
	Code             string `json:"code"`
	State            string `json:"state"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type oauthCallbackFailure struct {
	Status  int
	Code    string
	Message string
}

func setOAuthCallbackHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func writeOAuthCallbackError(w http.ResponseWriter, failure oauthCallbackFailure) {
	writeOpenAIErrorCode(w, failure.Status, "oauth_callback_error", failure.Code, failure.Message)
}

func (s *Server) oauthNow() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) activeOAuthConfig() auth.OAuthConfig {
	if store := s.activeTokenStore(); store != nil {
		return store.Config()
	}
	return auth.CurrentOAuthConfig()
}

func (s *Server) bindPKCEConfig(transaction pendingPKCE) pendingPKCE {
	if transaction.OAuth.ClientID == "" {
		transaction.OAuth = s.activeOAuthConfig()
	}
	if transaction.RedirectURI == "" {
		transaction.RedirectURI = transaction.OAuth.RedirectURI
	}
	return transaction
}

func (s *Server) prunePKCELocked(now time.Time) []pendingPKCE {
	var discarded []pendingPKCE
	for state, transaction := range s.pkce {
		if transaction.Created.IsZero() || now.Sub(transaction.Created) > pkceTransactionRetention {
			delete(s.pkce, state)
			if transaction.DiscardOnFailure && transaction.Staged && transaction.Status != "authenticated" {
				discarded = append(discarded, transaction)
			}
		}
	}
	return discarded
}

func (s *Server) discardPrunedPKCEProfiles(transactions []pendingPKCE) {
	for _, transaction := range transactions {
		s.discardPKCEProfileOnFailure(transaction)
	}
}

func (s *Server) peekPKCE(state string) (pendingPKCE, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction, ok := s.pkce[state]
	if !ok {
		return pendingPKCE{}, false
	}
	return s.bindPKCEConfig(transaction), true
}

func (s *Server) claimPKCE(state string) (pendingPKCE, *oauthCallbackFailure) {
	now := s.oauthNow()
	s.mu.Lock()
	pruned := s.prunePKCELocked(now)
	defer func() {
		s.mu.Unlock()
		s.discardPrunedPKCEProfiles(pruned)
	}()
	transaction, ok := s.pkce[state]
	if !ok {
		return pendingPKCE{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_state_mismatch", Message: "OAuth state 不符合目前的授權工作階段"}
	}
	transaction = s.bindPKCEConfig(transaction)
	if transaction.Status != "pending" {
		return pendingPKCE{}, &oauthCallbackFailure{Status: http.StatusConflict, Code: "oauth_state_replayed", Message: "OAuth state 已使用或正在處理，請重新開始授權"}
	}
	if now.Sub(transaction.Created) > pkceTransactionTTL {
		transaction.Status = "expired"
		transaction.ErrorCode = "oauth_state_expired"
		transaction.Error = "OAuth 授權工作階段已過期，請重新開始授權"
		transaction.Verifier = ""
		s.pkce[state] = transaction
		return transaction, &oauthCallbackFailure{Status: http.StatusGone, Code: "oauth_state_expired", Message: transaction.Error}
	}
	claimed := transaction
	transaction.Status = "processing"
	transaction.Verifier = ""
	transaction.Error = ""
	transaction.ErrorCode = ""
	s.pkce[state] = transaction
	return claimed, nil
}

func (s *Server) finishPKCE(state, status, errorCode, message string, account any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction, ok := s.pkce[state]
	if !ok || transaction.Status != "processing" {
		return
	}
	transaction.Status = status
	transaction.Verifier = ""
	transaction.ErrorCode = errorCode
	transaction.Error = message
	transaction.Account = account
	s.pkce[state] = transaction
}

func (s *Server) discardPKCEProfileOnFailure(transaction pendingPKCE) {
	if !transaction.DiscardOnFailure || !transaction.Staged || transaction.ProfileID == "" || s.oauthProfiles == nil {
		return
	}
	_ = s.oauthProfiles.Discard(transaction.ProfileID)
}

type oauthCallbackResult struct {
	Transaction pendingPKCE
	Stored      storedOAuthAccount
}

func (s *Server) completePKCEInput(input oauthCallbackInput) (oauthCallbackResult, *oauthCallbackFailure) {
	transaction, failure := s.claimPKCE(input.State)
	if failure != nil {
		if failure.Code == "oauth_state_expired" {
			s.discardPKCEProfileOnFailure(transaction)
		}
		return oauthCallbackResult{Transaction: transaction}, failure
	}
	if input.Error != "" {
		code := "oauth_authorization_failed"
		message := "Microsoft 授權失敗，請重新開始授權"
		status := "error"
		if input.Error == "access_denied" || input.Error == "user_cancelled" {
			code = "oauth_authorization_cancelled"
			message = "Microsoft 授權已取消，請在需要時重新開始授權"
			status = "cancelled"
		}
		s.finishPKCE(input.State, status, code, message, nil)
		s.discardPKCEProfileOnFailure(transaction)
		return oauthCallbackResult{Transaction: transaction}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: code, Message: message}
	}

	tokenSet, err := auth.ExchangeCodeWithConfig(transaction.OAuth, input.Code, transaction.Verifier, transaction.RedirectURI)
	if err != nil {
		message := "OAuth 授權碼交換失敗，請重新開始授權"
		s.finishPKCE(input.State, "error", "oauth_token_exchange_failed", message, nil)
		s.discardPKCEProfileOnFailure(transaction)
		return oauthCallbackResult{Transaction: transaction}, &oauthCallbackFailure{Status: http.StatusBadGateway, Code: "oauth_token_exchange_failed", Message: message}
	}
	stored, storeFailure := s.storeOAuthTokenSet(transaction.ProfileID, tokenSet)
	if storeFailure != nil {
		s.finishPKCE(input.State, "error", storeFailure.Code, storeFailure.Message, nil)
		s.discardPKCEProfileOnFailure(transaction)
		return oauthCallbackResult{Transaction: transaction}, &oauthCallbackFailure{Status: http.StatusInternalServerError, Code: storeFailure.Code, Message: storeFailure.Message}
	}
	s.finishPKCE(input.State, "authenticated", "", "", stored.Account)
	return oauthCallbackResult{Transaction: transaction, Stored: stored}, nil
}

func (s *Server) handlePKCECallback(w http.ResponseWriter, r *http.Request) {
	setOAuthCallbackHeaders(w)
	var (
		input   oauthCallbackInput
		failure *oauthCallbackFailure
	)
	switch r.Method {
	case http.MethodGet:
		input, failure = s.parseLoopbackCallback(r)
	case http.MethodPost:
		input, failure = s.parseManualCallback(w, r)
	default:
		failure = &oauthCallbackFailure{Status: http.StatusMethodNotAllowed, Code: "oauth_callback_method_not_allowed", Message: "此 callback 僅接受已註冊 loopback GET 或 JSON POST 備援"}
	}
	if failure != nil {
		writeOAuthCallbackError(w, *failure)
		return
	}

	result, failure := s.completePKCEInput(input)
	if failure != nil {
		writeOAuthCallbackError(w, *failure)
		return
	}
	if r.Method == http.MethodGet {
		writeOAuthCompletionPage(w)
		return
	}
	jsonOut(w, map[string]any{
		"status":           "authenticated",
		"account":          result.Stored.Account,
		"oauthProfileId":   result.Stored.Manifest.ProfileID,
		"oauthProfileKind": result.Stored.Manifest.Kind,
		"staged":           result.Transaction.Staged,
	})
}

func (s *Server) parseManualCallback(w http.ResponseWriter, r *http.Request) (oauthCallbackInput, *oauthCallbackFailure) {
	if r.URL.RawQuery != "" {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_query_forbidden", Message: "手動備援不得把 callback 資料放在 URL query"}
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusUnsupportedMediaType, Code: "oauth_callback_content_type", Message: "手動備援必須使用 application/json"}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthCallbackBody))
	decoder.DisallowUnknownFields()
	var decoded *oauthCallbackInput
	if err := decoder.Decode(&decoded); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusRequestEntityTooLarge, Code: "oauth_callback_body_too_large", Message: "callback 資料超過允許大小"}
		}
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_invalid_json", Message: "callback JSON 格式錯誤或包含未允許欄位"}
	}
	if decoded == nil {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_invalid_json", Message: "callback JSON 必須是物件"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_invalid_json", Message: "callback JSON 只能包含一個物件"}
	}
	input := trimOAuthCallbackInput(*decoded)
	if input.CallbackURL != "" {
		fromURL, err := oauthInputFromURL(input.CallbackURL)
		if err != nil {
			return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_invalid_url", Message: "callback_url 格式錯誤"}
		}
		if failure := mergeOAuthCallbackInput(&input, fromURL); failure != nil {
			return oauthCallbackInput{}, failure
		}
		input = trimOAuthCallbackInput(input)
	}
	if failure := validateOAuthCallbackMaterial(input); failure != nil {
		return oauthCallbackInput{}, failure
	}
	transaction, ok := s.peekPKCE(input.State)
	if !ok {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_state_mismatch", Message: "OAuth state 不符合目前的授權工作階段"}
	}
	if input.CallbackURL != "" && !oauthRedirectURLMatches(input.CallbackURL, transaction.RedirectURI) {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_redirect_mismatch", Message: "callback_url 與本次授權設定的 redirect URI 不符"}
	}
	return input, nil
}

func (s *Server) parseLoopbackCallback(r *http.Request) (oauthCallbackInput, *oauthCallbackFailure) {
	input, err := oauthInputFromValues(r.URL.Query())
	if err != nil {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_invalid_query", Message: "OAuth callback query 格式錯誤"}
	}
	input = trimOAuthCallbackInput(input)
	if failure := validateOAuthCallbackMaterial(input); failure != nil {
		return oauthCallbackInput{}, failure
	}
	transaction, ok := s.peekPKCE(input.State)
	if !ok {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_state_mismatch", Message: "OAuth state 不符合目前的授權工作階段"}
	}
	if !isRegisteredLoopbackRedirect(transaction.RedirectURI) {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusMethodNotAllowed, Code: "oauth_callback_loopback_required", Message: "GET callback 僅適用於已註冊的 loopback redirect"}
	}
	if !oauthLoopbackRequestMatches(r, transaction.RedirectURI) {
		return oauthCallbackInput{}, &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_redirect_mismatch", Message: "loopback callback 的 scheme、host、port 或 path 不符"}
	}
	return input, nil
}

func trimOAuthCallbackInput(input oauthCallbackInput) oauthCallbackInput {
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)
	input.Code = strings.TrimSpace(input.Code)
	input.State = strings.TrimSpace(input.State)
	input.Error = strings.TrimSpace(input.Error)
	input.ErrorDescription = strings.TrimSpace(input.ErrorDescription)
	return input
}

func validateOAuthCallbackMaterial(input oauthCallbackInput) *oauthCallbackFailure {
	if input.State == "" {
		return &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_state_required", Message: "callback 缺少 OAuth state"}
	}
	if input.Code != "" && input.Error != "" {
		return &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_material_conflict", Message: "callback 不得同時包含 code 與 error"}
	}
	if input.Code == "" && input.Error == "" {
		return &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_code_required", Message: "callback 缺少授權碼或錯誤狀態"}
	}
	return nil
}

func mergeOAuthCallbackInput(destination *oauthCallbackInput, source oauthCallbackInput) *oauthCallbackFailure {
	for _, pair := range []struct {
		destination *string
		source      string
	}{
		{destination: &destination.Code, source: source.Code},
		{destination: &destination.State, source: source.State},
		{destination: &destination.Error, source: source.Error},
		{destination: &destination.ErrorDescription, source: source.ErrorDescription},
	} {
		if pair.source == "" {
			continue
		}
		if *pair.destination != "" && *pair.destination != pair.source {
			return &oauthCallbackFailure{Status: http.StatusBadRequest, Code: "oauth_callback_material_conflict", Message: "callback_url 與 JSON 欄位內容不一致"}
		}
		*pair.destination = pair.source
	}
	return nil
}

func oauthInputFromURL(raw string) (oauthCallbackInput, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Fragment != "" {
		return oauthCallbackInput{}, errors.New("invalid callback URL")
	}
	return oauthInputFromValues(parsed.Query())
}

func oauthInputFromValues(values url.Values) (oauthCallbackInput, error) {
	code, err := singleOAuthValue(values, "code")
	if err != nil {
		return oauthCallbackInput{}, err
	}
	state, err := singleOAuthValue(values, "state")
	if err != nil {
		return oauthCallbackInput{}, err
	}
	oauthError, err := singleOAuthValue(values, "error")
	if err != nil {
		return oauthCallbackInput{}, err
	}
	description, err := singleOAuthValue(values, "error_description")
	if err != nil {
		return oauthCallbackInput{}, err
	}
	return oauthCallbackInput{Code: code, State: state, Error: oauthError, ErrorDescription: description}, nil
}

func singleOAuthValue(values url.Values, key string) (string, error) {
	items := values[key]
	if len(items) > 1 {
		return "", errors.New("duplicate OAuth value")
	}
	if len(items) == 0 {
		return "", nil
	}
	return items[0], nil
}

func oauthRedirectURLMatches(callbackURL, configuredRedirect string) bool {
	callback, err := url.Parse(callbackURL)
	if err != nil {
		return false
	}
	configured, err := url.Parse(configuredRedirect)
	if err != nil {
		return false
	}
	return oauthURLIdentity(callback) == oauthURLIdentity(configured)
}

func oauthURLIdentity(parsed *url.URL) string {
	if parsed == nil || !parsed.IsAbs() || parsed.User != nil {
		return ""
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host) + path
}

func isRegisteredLoopbackRedirect(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || strings.ToLower(parsed.Scheme) != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	return hostname == "127.0.0.1" || hostname == "localhost"
}

func oauthLoopbackRequestMatches(r *http.Request, configuredRedirect string) bool {
	configured, err := url.Parse(configuredRedirect)
	if err != nil {
		return false
	}
	scheme := "http"
	if info, ok := adminRequestInfoFrom(r); ok && info.scheme != "" {
		scheme = info.scheme
	} else if r.URL.Scheme != "" {
		scheme = strings.ToLower(r.URL.Scheme)
	} else if r.TLS != nil {
		scheme = "https"
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	requestIdentity := strings.ToLower(scheme) + "://" + strings.ToLower(r.Host) + path
	return requestIdentity == oauthURLIdentity(configured)
}
