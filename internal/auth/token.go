package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"m365-native/internal/outbound"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Email        string    `json:"email,omitempty"`
	DisplayName  string    `json:"display_name,omitempty"`
	HomeOID      string    `json:"home_oid,omitempty"`
	TenantID     string    `json:"tenant_id,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type TokenEndpointError struct {
	Status int
	Code   string
}

func (e *TokenEndpointError) Error() string {
	code := strings.TrimSpace(e.Code)
	if code == "" {
		code = "token_endpoint_error"
	}
	return fmt.Sprintf("token endpoint HTTP %d: %s", e.Status, code)
}

func (t TokenSet) Valid() bool {
	return t.AccessToken != "" && time.Now().Before(t.ExpiresAt.Add(-30*time.Second))
}

func ExchangeCodeWithConfig(config OAuthConfig, code, verifier, redirect string) (TokenSet, error) {
	config, err := normalizeOAuthConfig(config)
	if err != nil {
		return TokenSet{}, err
	}
	form := url.Values{}
	form.Set("client_id", config.ClientID)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)
	form.Set("scope", config.Scope)
	return requestToken(config.TokenEndpoint, form)
}

func Refresh(refreshToken string) (TokenSet, error) {
	return RefreshWithConfig(CurrentOAuthConfig(), refreshToken)
}

func RefreshWithConfig(config OAuthConfig, refreshToken string) (TokenSet, error) {
	return refreshWithConfigContext(context.Background(), config, refreshToken)
}

func requestToken(endpoint string, form url.Values) (TokenSet, error) {
	return requestTokenContext(context.Background(), endpoint, form)
}

const oauthRefreshRequestTimeout = 60 * time.Second

func refreshWithConfigContext(ctx context.Context, config OAuthConfig, refreshToken string) (TokenSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeOAuthConfig(config)
	if err != nil {
		return TokenSet{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, oauthRefreshRequestTimeout)
	defer cancel()
	form := url.Values{}
	form.Set("client_id", config.ClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("scope", config.Scope)
	return requestTokenContext(ctx, config.TokenEndpoint, form)
}

func requestTokenContext(ctx context.Context, endpoint string, form url.Values) (TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := outbound.HTTPClient().Do(req)
	if err != nil {
		return TokenSet{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TokenSet{}, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return TokenSet{}, fmt.Errorf("decode token response: %w", err)
	}
	if tr.Error != "" {
		return TokenSet{}, &TokenEndpointError{Status: resp.StatusCode, Code: safeTokenErrorCode(tr.Error, form)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TokenSet{}, &TokenEndpointError{Status: resp.StatusCode, Code: "http_error"}
	}
	if tr.AccessToken == "" {
		return TokenSet{}, &TokenEndpointError{Status: resp.StatusCode, Code: "empty_access_token"}
	}
	set := TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		IDToken:      tr.IDToken,
		TokenType:    tr.TokenType,
		Scope:        tr.Scope,
		ExpiresIn:    tr.ExpiresIn,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
	if claims, err := decodeJWTClaims(tr.AccessToken); err == nil {
		set.Email = firstNonEmpty(claims["unique_name"], claims["upn"], claims["preferred_username"], claims["email"])
		set.DisplayName = firstNonEmpty(claims["name"], set.Email)
		set.HomeOID = firstNonEmpty(claims["oid"], claims["sub"])
		set.TenantID = firstNonEmpty(claims["tid"], claims["tenant_id"])
	}
	if tr.IDToken != "" {
		if claims, err := decodeJWTClaims(tr.IDToken); err == nil {
			if set.Email == "" {
				set.Email = firstNonEmpty(claims["preferred_username"], claims["email"], claims["upn"])
				set.DisplayName = firstNonEmpty(claims["name"], set.Email)
				set.HomeOID = firstNonEmpty(claims["oid"], claims["sub"], set.HomeOID)
			}
			set.TenantID = firstNonEmpty(set.TenantID, claims["tid"], claims["tenant_id"])
		}
	}
	return set, nil
}

func safeTokenErrorCode(code string, form url.Values) string {
	code = strings.TrimSpace(code)
	for _, key := range []string{"refresh_token", "code", "code_verifier"} {
		if secret := form.Get(key); secret != "" && strings.Contains(code, secret) {
			return "oauth_error"
		}
	}
	if code == "" || len(code) > 128 {
		return "oauth_error"
	}
	for _, r := range code {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return "oauth_error"
		}
	}
	return code
}

func decodeJWTClaims(token string) (map[string]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid jwt")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = t
		}
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
