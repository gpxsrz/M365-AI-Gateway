package auth

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// Office web Copilot first-party client (verified working with ChatHub via browser PKCE).
// The default authority is multi-tenant so any supported Microsoft account can sign in.
const DefaultClientID = "c0ab8ce9-e9a0-42e7-b064-33d422df41f1"
const DefaultAuthority = "https://login.microsoftonline.com/common"
const DefaultRedirectURI = "https://login.microsoftonline.com/common/oauth2/nativeclient"
const DefaultScope = "openid profile offline_access https://substrate.office.com/sydney/M365Chat.Read https://substrate.office.com/sydney/sydney.readwrite"

type OAuthConfig struct {
	ClientID          string `json:"client_id"`
	Authority         string `json:"authority"`
	RedirectURI       string `json:"redirect_uri"`
	Scope             string `json:"scope"`
	AuthorizeEndpoint string `json:"authorize_endpoint"`
	TokenEndpoint     string `json:"token_endpoint"`
}

func CurrentOAuthConfig() OAuthConfig {
	return OAuthConfig{
		ClientID:          ClientID(),
		Authority:         Authority(),
		RedirectURI:       RedirectURI(),
		Scope:             Scope(),
		AuthorizeEndpoint: AuthorizeEndpoint(),
		TokenEndpoint:     TokenEndpoint(),
	}
}

func normalizeOAuthConfig(config OAuthConfig) (OAuthConfig, error) {
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.Authority = strings.TrimSpace(config.Authority)
	config.RedirectURI = strings.TrimSpace(config.RedirectURI)
	config.Scope = strings.TrimSpace(config.Scope)
	config.AuthorizeEndpoint = strings.TrimSpace(config.AuthorizeEndpoint)
	config.TokenEndpoint = strings.TrimSpace(config.TokenEndpoint)
	if config.ClientID == "" || config.Authority == "" || config.RedirectURI == "" || config.Scope == "" || config.AuthorizeEndpoint == "" || config.TokenEndpoint == "" {
		return OAuthConfig{}, errors.New("OAuth configuration is incomplete")
	}
	for label, raw := range map[string]string{
		"authority":          config.Authority,
		"redirect URI":       config.RedirectURI,
		"authorize endpoint": config.AuthorizeEndpoint,
		"token endpoint":     config.TokenEndpoint,
	} {
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.Scheme == "" || parsed.Host == "" {
			return OAuthConfig{}, errors.New("invalid OAuth " + label)
		}
	}
	return config, nil
}

func ClientID() string {
	if v := os.Getenv("M365_CLIENT_ID"); v != "" {
		return v
	}
	return DefaultClientID
}

func Authority() string {
	if v := os.Getenv("M365_AUTHORITY"); v != "" {
		return v
	}
	return DefaultAuthority
}

func RedirectURI() string {
	if v := os.Getenv("M365_REDIRECT_URI"); v != "" {
		return v
	}
	return DefaultRedirectURI
}

func Scope() string {
	if v := os.Getenv("M365_SCOPE"); v != "" {
		return v
	}
	return DefaultScope
}

func AuthorizeEndpoint() string {
	if v := os.Getenv("M365_AUTHORIZE_ENDPOINT"); v != "" {
		return v
	}
	return Authority() + "/oauth2/v2.0/authorize"
}

func TokenEndpoint() string {
	if v := os.Getenv("M365_TOKEN_ENDPOINT"); v != "" {
		return v
	}
	return Authority() + "/oauth2/v2.0/token"
}
