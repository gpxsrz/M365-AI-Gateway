package web

import (
	"encoding/json"
	"errors"
	"io"
	"m365-native/internal/auth"
	"mime"
	"net/http"
	"strings"
)

const maxPKCEStartBody = 16 << 10

type pkceStartRequest struct {
	ProfileID   string `json:"profileId,omitempty"`
	StageActive bool   `json:"stageActive,omitempty"`
}

type pkceProfileTarget struct {
	ProfileID string
	Kind      string
	Staged    bool
	Created   bool
	OAuth     auth.OAuthConfig
	Store     *auth.Store
}

type pkceStartFailure struct {
	Status  int
	Code    string
	Message string
}

func (s *Server) resolvePKCEProfileTarget(w http.ResponseWriter, r *http.Request) (pkceProfileTarget, *pkceStartFailure) {
	request, failure := parsePKCEStartRequest(w, r)
	if failure != nil {
		return pkceProfileTarget{}, failure
	}
	selected := 0
	if request.ProfileID != "" {
		selected++
	}
	if request.StageActive {
		selected++
	}
	if selected > 1 {
		return pkceProfileTarget{}, &pkceStartFailure{Status: http.StatusBadRequest, Code: "oauth_profile_target_conflict", Message: "OAuth 授權目標只能指定一種 profile 模式"}
	}

	if s.oauthProfiles == nil {
		if selected != 0 {
			return pkceProfileTarget{}, &pkceStartFailure{Status: http.StatusServiceUnavailable, Code: "oauth_profile_manager_unavailable", Message: "OAuth profile 管理功能目前無法使用"}
		}
		config := s.activeOAuthConfig()
		return pkceProfileTarget{ProfileID: "legacy", Kind: "legacy", OAuth: config, Store: s.activeTokenStore()}, nil
	}

	var (
		manifest auth.OAuthProfileManifest
		store    *auth.Store
		err      error
	)
	created := false
	switch {
	case request.StageActive:
		manifest, store, err = s.oauthProfiles.StageFromActive()
		created = err == nil
	case request.ProfileID != "":
		manifest, store, err = s.oauthProfiles.OpenStore(request.ProfileID)
	default:
		manifest, store, err = s.oauthProfiles.ActiveStore()
	}
	if err != nil {
		return pkceProfileTarget{}, &pkceStartFailure{Status: http.StatusBadRequest, Code: "oauth_profile_target_invalid", Message: "無法建立或開啟指定的 OAuth profile"}
	}
	return pkceProfileTarget{
		ProfileID: manifest.ProfileID,
		Kind:      manifest.Kind,
		Staged:    manifest.Kind == "staged",
		Created:   created,
		OAuth:     manifest.OAuth,
		Store:     store,
	}, nil
}

func parsePKCEStartRequest(w http.ResponseWriter, r *http.Request) (pkceStartRequest, *pkceStartFailure) {
	if r.Body == nil || r.Body == http.NoBody {
		return pkceStartRequest{}, nil
	}
	mediaType := ""
	if raw := strings.TrimSpace(r.Header.Get("Content-Type")); raw != "" {
		parsed, _, err := mime.ParseMediaType(raw)
		if err != nil {
			return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusUnsupportedMediaType, Code: "oauth_start_content_type", Message: "OAuth start body 必須使用 application/json"}
		}
		mediaType = parsed
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPKCEStartBody))
	decoder.DisallowUnknownFields()
	var decoded *pkceStartRequest
	if err := decoder.Decode(&decoded); err != nil {
		if errors.Is(err, io.EOF) {
			return pkceStartRequest{}, nil
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusRequestEntityTooLarge, Code: "oauth_start_body_too_large", Message: "OAuth start body 超過允許大小"}
		}
		return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusBadRequest, Code: "oauth_start_invalid_json", Message: "OAuth start JSON 格式錯誤或包含未允許欄位"}
	}
	if decoded == nil {
		return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusBadRequest, Code: "oauth_start_invalid_json", Message: "OAuth start JSON 必須是物件"}
	}
	request := *decoded
	if mediaType != "application/json" {
		return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusUnsupportedMediaType, Code: "oauth_start_content_type", Message: "OAuth start body 必須使用 application/json"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return pkceStartRequest{}, &pkceStartFailure{Status: http.StatusBadRequest, Code: "oauth_start_invalid_json", Message: "OAuth start JSON 只能包含一個物件"}
	}
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	return request, nil
}

func writePKCEStartFailure(w http.ResponseWriter, failure pkceStartFailure) {
	writeOpenAIErrorCode(w, failure.Status, "oauth_error", failure.Code, failure.Message)
}

func (s *Server) oauthStoreForProfile(profileID string) (auth.OAuthProfileManifest, *auth.Store, error) {
	if s.oauthProfiles == nil {
		return auth.OAuthProfileManifest{ProfileID: "legacy", Kind: "legacy", OAuth: s.activeOAuthConfig()}, s.activeTokenStore(), nil
	}
	var (
		manifest auth.OAuthProfileManifest
		store    *auth.Store
		err      error
	)
	if strings.TrimSpace(profileID) == "" {
		manifest, store, err = s.oauthProfiles.ActiveStore()
	} else {
		manifest, store, err = s.oauthProfiles.OpenStore(strings.TrimSpace(profileID))
	}
	if err != nil {
		return auth.OAuthProfileManifest{}, nil, err
	}
	if activeStore := s.activeTokenStore(); activeStore != nil && activeStore.Path() == store.Path() {
		store = activeStore
	}
	return manifest, store, nil
}

type oauthAccountContext struct {
	Manifest auth.OAuthProfileManifest
	Store    *auth.Store
}

func (s *Server) oauthAccountContext(profileID string) (oauthAccountContext, error) {
	manifest, store, err := s.oauthStoreForProfile(profileID)
	if err != nil {
		return oauthAccountContext{}, err
	}
	if store == nil {
		return oauthAccountContext{}, errors.New("OAuth token store is unavailable")
	}
	return oauthAccountContext{Manifest: manifest, Store: store}, nil
}

func managementAccountInContext(context oauthAccountContext, account auth.AccountToken) (managementAccountView, error) {
	return managementAccountView{
		Status:    account.Status,
		ExpiresAt: account.ExpiresAt,
		UpdatedAt: account.UpdatedAt,
	}, nil
}

func managementAccountForStore(_ *auth.Store, account auth.AccountToken) (managementAccountView, error) {
	return managementAccountInContext(oauthAccountContext{}, account)
}
