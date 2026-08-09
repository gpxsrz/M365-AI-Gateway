package web

import (
	"context"
	"encoding/json"
	"io"
	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"net/http"
	"strings"
	"time"
)

func (s *Server) validateOAuthCandidateChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var body struct {
		ProfileID string `json:"profileId"`
	}
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.ProfileID) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "需要有效的候選 OAuth profile")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "JSON 只能包含一個物件")
		return
	}
	if s.oauthProfiles == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "oauth_profile_error", "OAuth profile 管理功能目前無法使用")
		return
	}
	manifest, store, err := s.oauthProfiles.OpenStore(strings.TrimSpace(body.ProfileID))
	if err != nil || manifest.Kind != "staged" {
		writeOpenAIError(w, http.StatusBadRequest, "oauth_profile_error", "指定的候選 OAuth profile 無法使用")
		return
	}
	account, ok := store.First()
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "account_not_found", "候選 OAuth profile 尚未完成登入")
		return
	}
	account, err = store.EnsureValid(account.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "token_refresh_error", "候選帳號權杖無法使用")
		return
	}
	if account.OID == "" || account.TID == "" {
		account.OID, account.TID = extractOIDTID(account.AccessToken)
	}
	if account.OID == "" || account.TID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "account_identity_error", "候選帳號缺少必要身分資訊")
		return
	}
	timeout := time.Duration(serverRuntimeSettings(s).ChatTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, err := s.chat.Chat(ctx, chathub.Account{AccessToken: account.AccessToken, OID: account.OID, TID: account.TID}, chathub.Request{Text: "Reply with OK only.", Tone: "magic"})
	if err != nil || strings.TrimSpace(result.Text) == "" {
		writeOpenAIError(w, http.StatusBadGateway, "upstream_error", "候選 ChatHub 驗證失敗")
		return
	}
	if _, err := s.oauthProfiles.RecordValidation(manifest.ProfileID, auth.OAuthProfileValidationChatHub); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "oauth_profile_error", "無法記錄候選 ChatHub 驗證")
		return
	}
	jsonOut(w, map[string]string{"status": "validated"})
}
