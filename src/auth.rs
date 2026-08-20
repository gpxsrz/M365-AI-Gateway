use std::{
    env,
    path::{Path, PathBuf},
    sync::Mutex,
};

use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::Rng;
use reqwest::{Client, StatusCode};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use time::{Duration, OffsetDateTime};
use url::Url;

use crate::{error::GatewayError, private_file};

pub const DEFAULT_CLIENT_ID: &str = "c0ab8ce9-e9a0-42e7-b064-33d422df41f1";
pub const DEFAULT_AUTHORITY: &str = "https://login.microsoftonline.com/common";
pub const DEFAULT_REDIRECT_URI: &str =
    "https://login.microsoftonline.com/common/oauth2/nativeclient";
pub const DEFAULT_SCOPE: &str = "openid profile offline_access https://substrate.office.com/sydney/M365Chat.Read https://substrate.office.com/sydney/sydney.readwrite";
pub const TOKEN_CACHE_SCHEMA: &str = "m365-oauth-token-cache/v1";
const TEAMS_WEB_CLIENT_ID: &str = "5e3ce6c0-2b1f-4285-8d4b-75ee78787346";
const TEAMS_WEB_ORIGIN: &str = "https://teams.microsoft.com";
pub(crate) const TEAMS_REDIRECT_URI: &str = "https://teams.microsoft.com/v2";
pub(crate) const TEAMS_AUTHORIZE_SCOPE: &str =
    "openid profile offline_access https://ic3.teams.office.com/.default";
const TEAMS_IC3_SCOPE: &str = "https://ic3.teams.office.com/.default offline_access";

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct OAuthConfig {
    pub client_id: String,
    pub authority: String,
    pub redirect_uri: String,
    pub scope: String,
    pub authorize_endpoint: String,
    pub token_endpoint: String,
}

impl OAuthConfig {
    pub fn from_env() -> Result<Self, AuthError> {
        let authority = env_value("M365_AUTHORITY", DEFAULT_AUTHORITY);
        Self {
            client_id: env_value("M365_CLIENT_ID", DEFAULT_CLIENT_ID),
            redirect_uri: env_value("M365_REDIRECT_URI", DEFAULT_REDIRECT_URI),
            scope: env_value("M365_SCOPE", DEFAULT_SCOPE),
            authorize_endpoint: env_value(
                "M365_AUTHORIZE_ENDPOINT",
                &format!("{authority}/oauth2/v2.0/authorize"),
            ),
            token_endpoint: env_value(
                "M365_TOKEN_ENDPOINT",
                &format!("{authority}/oauth2/v2.0/token"),
            ),
            authority,
        }
        .validate()
    }

    pub fn validate(mut self) -> Result<Self, AuthError> {
        self.client_id = self.client_id.trim().to_owned();
        self.authority = self.authority.trim().trim_end_matches('/').to_owned();
        self.redirect_uri = self.redirect_uri.trim().to_owned();
        self.scope = self.scope.split_whitespace().collect::<Vec<_>>().join(" ");
        self.authorize_endpoint = self.authorize_endpoint.trim().to_owned();
        self.token_endpoint = self.token_endpoint.trim().to_owned();
        if self.client_id.is_empty()
            || self.authority.is_empty()
            || self.redirect_uri.is_empty()
            || self.scope.is_empty()
            || self.authorize_endpoint.is_empty()
            || self.token_endpoint.is_empty()
        {
            return Err(AuthError::Configuration(
                "OAuth configuration is incomplete".to_owned(),
            ));
        }
        for (label, raw) in [
            ("authority", self.authority.as_str()),
            ("redirect URI", self.redirect_uri.as_str()),
            ("authorize endpoint", self.authorize_endpoint.as_str()),
            ("token endpoint", self.token_endpoint.as_str()),
        ] {
            let parsed = Url::parse(raw)
                .map_err(|_| AuthError::Configuration(format!("invalid OAuth {label}")))?;
            if !matches!(parsed.scheme(), "http" | "https")
                || parsed.host_str().is_none()
                || !parsed.username().is_empty()
                || parsed.password().is_some()
            {
                return Err(AuthError::Configuration(format!("invalid OAuth {label}")));
            }
        }
        Ok(self)
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountToken {
    pub id: String,
    pub email: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    pub status: String,
    pub access_token: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub refresh_token: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub teams_refresh_token: String,
    #[serde(with = "time::serde::rfc3339")]
    pub expires_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub oid: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tid: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub client_id: String,
}

#[derive(Clone, Debug)]
pub struct TokenSet {
    pub access_token: String,
    pub refresh_token: String,
    pub teams_refresh_token: String,
    pub id_token: String,
    pub token_type: String,
    pub scope: String,
    pub expires_in: i64,
    pub expires_at: OffsetDateTime,
    pub email: String,
    pub display_name: String,
    pub home_oid: String,
    pub tenant_id: String,
}

impl TokenSet {
    pub(crate) fn bind_teams_refresh(mut self, teams: Self) -> Result<Self, AuthError> {
        if teams.refresh_token.is_empty()
            || !valid_teams_token_for(&teams, &self.home_oid, &self.tenant_id)
        {
            return Err(AuthError::Storage(
                "Teams authorization does not match the Microsoft account".to_owned(),
            ));
        }
        self.teams_refresh_token = teams.refresh_token;
        Ok(self)
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct TokenCache {
    #[serde(default)]
    schema: String,
    #[serde(default)]
    accounts: Vec<AccountToken>,
}

pub struct TokenStore {
    path: PathBuf,
    config: OAuthConfig,
    client: Client,
    cache: Mutex<TokenCache>,
    resource_refresh: tokio::sync::Mutex<()>,
}

impl TokenStore {
    pub fn open(path: impl Into<PathBuf>, config: OAuthConfig) -> Result<Self, AuthError> {
        let path = path.into();
        let config = config.validate()?;
        let mut cache =
            private_file::read_json::<TokenCache>(&path)?.unwrap_or_else(|| TokenCache {
                schema: TOKEN_CACHE_SCHEMA.to_owned(),
                accounts: Vec::new(),
            });
        if cache.schema.is_empty() {
            return Err(AuthError::Storage(
                "token cache schema is required".to_owned(),
            ));
        }
        if cache.schema != TOKEN_CACHE_SCHEMA {
            return Err(AuthError::Storage(format!(
                "unsupported token cache schema {:?}",
                cache.schema
            )));
        }
        let needs_migration = cache.accounts.len() > 1;
        if needs_migration {
            cache.accounts.sort_by(|left, right| {
                right
                    .updated_at
                    .cmp(&left.updated_at)
                    .then_with(|| right.id.cmp(&left.id))
            });
            cache.accounts.truncate(1);
            save_cache(&path, &cache)?;
        }
        Ok(Self {
            path,
            config,
            client: Client::builder()
                .timeout(std::time::Duration::from_secs(60))
                .build()
                .map_err(|error| AuthError::Http(error.to_string()))?,
            cache: Mutex::new(cache),
            resource_refresh: tokio::sync::Mutex::new(()),
        })
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn config(&self) -> &OAuthConfig {
        &self.config
    }

    pub fn first(&self) -> Option<AccountToken> {
        self.cache
            .lock()
            .expect("token store poisoned")
            .accounts
            .first()
            .cloned()
    }

    pub(crate) fn snapshot_to(&self, path: &Path) -> Result<(), AuthError> {
        let cache = self.cache.lock().expect("token store poisoned").clone();
        save_cache(path, &cache)
    }

    pub fn upsert(&self, token: TokenSet) -> Result<AccountToken, AuthError> {
        let now = OffsetDateTime::now_utc();
        let previous = self.first();
        let id = first_non_empty([
            token.home_oid.as_str(),
            token.email.as_str(),
            &format!("account-{}", now.unix_timestamp()),
        ]);
        let previous_same_account = previous.as_ref().filter(|account| {
            account.id == id
                || (!token.home_oid.is_empty() && account.oid == token.home_oid)
                || (token.home_oid.is_empty()
                    && !token.email.is_empty()
                    && account.email == token.email)
        });
        let account = AccountToken {
            id: id.clone(),
            email: token.email,
            display_name: token.display_name,
            status: "online".to_owned(),
            access_token: token.access_token,
            refresh_token: if token.refresh_token.is_empty() {
                previous_same_account
                    .map(|account| account.refresh_token.clone())
                    .unwrap_or_default()
            } else {
                token.refresh_token
            },
            teams_refresh_token: if token.teams_refresh_token.is_empty() {
                previous_same_account
                    .map(|account| account.teams_refresh_token.clone())
                    .unwrap_or_default()
            } else {
                token.teams_refresh_token
            },
            expires_at: token.expires_at,
            updated_at: now,
            oid: if token.home_oid.is_empty() {
                id.clone()
            } else {
                token.home_oid
            },
            tid: if token.tenant_id.is_empty() {
                previous_same_account
                    .map(|account| account.tid.clone())
                    .unwrap_or_default()
            } else {
                token.tenant_id
            },
            client_id: self.config.client_id.clone(),
        };
        let mut cache = self.cache.lock().expect("token store poisoned");
        let old = cache.accounts.clone();
        cache.accounts = vec![account.clone()];
        if let Err(error) = save_cache(&self.path, &cache) {
            cache.accounts = old;
            return Err(error);
        }
        Ok(account)
    }

    pub fn delete(&self, id: &str) -> Result<bool, AuthError> {
        let mut cache = self.cache.lock().expect("token store poisoned");
        let old = cache.accounts.clone();
        cache
            .accounts
            .retain(|account| account.id != id && account.oid != id && account.email != id);
        if cache.accounts.len() == old.len() {
            return Ok(false);
        }
        if let Err(error) = save_cache(&self.path, &cache) {
            cache.accounts = old;
            return Err(error);
        }
        Ok(true)
    }

    pub async fn ensure_valid(&self, id: &str) -> Result<AccountToken, AuthError> {
        let account = self
            .first()
            .filter(|account| account.id == id || account.oid == id || account.email == id)
            .ok_or(AuthError::AccountUnavailable)?;
        if OffsetDateTime::now_utc() < account.expires_at - Duration::seconds(30) {
            return Ok(account);
        }
        if account.refresh_token.is_empty() {
            self.mark_expired(&account.id)?;
            return Err(AuthError::Expired);
        }
        let token = self
            .refresh(&account.refresh_token, &self.config.scope)
            .await?;
        let token = TokenSet {
            email: if token.email.is_empty() {
                account.email
            } else {
                token.email
            },
            display_name: if token.display_name.is_empty() {
                account.display_name
            } else {
                token.display_name
            },
            home_oid: if token.home_oid.is_empty() {
                account.oid
            } else {
                token.home_oid
            },
            tenant_id: if token.tenant_id.is_empty() {
                account.tid
            } else {
                token.tenant_id
            },
            ..token
        };
        self.upsert(token)
    }

    pub async fn exchange_code(
        &self,
        code: &str,
        verifier: &str,
        redirect_uri: &str,
    ) -> Result<TokenSet, AuthError> {
        self.request_token([
            ("client_id", self.config.client_id.as_str()),
            ("grant_type", "authorization_code"),
            ("code", code),
            ("redirect_uri", redirect_uri),
            ("code_verifier", verifier),
            ("scope", self.config.scope.as_str()),
        ])
        .await
    }

    pub async fn refresh(&self, refresh_token: &str, scope: &str) -> Result<TokenSet, AuthError> {
        self.request_token([
            ("client_id", self.config.client_id.as_str()),
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token),
            ("scope", scope),
        ])
        .await
    }

    pub async fn resource_access_token(&self, scope: &str) -> Result<String, AuthError> {
        let _refresh = self.resource_refresh.lock().await;
        let account = self.first().ok_or(AuthError::AccountUnavailable)?;
        if account.refresh_token.is_empty() {
            return Err(AuthError::Expired);
        }
        let token = self.refresh(&account.refresh_token, scope).await?;
        self.accept_resource_refresh(&account, &token.refresh_token)?;
        Ok(token.access_token)
    }

    pub(crate) async fn teams_ic3_access_token(&self) -> Result<(String, String), AuthError> {
        let _refresh = self.resource_refresh.lock().await;
        let account = self.first().ok_or(AuthError::AccountUnavailable)?;
        if account.teams_refresh_token.is_empty() {
            return Err(AuthError::Expired);
        }
        let token = self
            .request_token_with_origin(
                [
                    ("client_id", TEAMS_WEB_CLIENT_ID),
                    ("grant_type", "refresh_token"),
                    ("refresh_token", account.teams_refresh_token.as_str()),
                    ("scope", TEAMS_IC3_SCOPE),
                ],
                Some(TEAMS_WEB_ORIGIN),
            )
            .await?;
        if !valid_teams_token_for(&token, &account.oid, &account.tid) {
            return Err(AuthError::Storage(
                "Teams IC3 token has the wrong audience, scope, or account".to_owned(),
            ));
        }
        self.accept_teams_refresh(&account, &token.refresh_token)?;
        Ok((token.access_token, account.oid))
    }

    pub(crate) async fn exchange_teams_code(
        &self,
        code: &str,
        verifier: &str,
    ) -> Result<TokenSet, AuthError> {
        self.request_token_with_origin(
            [
                ("client_id", TEAMS_WEB_CLIENT_ID),
                ("grant_type", "authorization_code"),
                ("code", code),
                ("redirect_uri", TEAMS_REDIRECT_URI),
                ("code_verifier", verifier),
                ("scope", TEAMS_AUTHORIZE_SCOPE),
            ],
            Some(TEAMS_WEB_ORIGIN),
        )
        .await
    }

    fn accept_resource_refresh(
        &self,
        account: &AccountToken,
        rotated_refresh_token: &str,
    ) -> Result<(), AuthError> {
        let mut cache = self.cache.lock().expect("token store poisoned");
        let Some(current) = cache.accounts.first_mut() else {
            return Err(AuthError::AccountUnavailable);
        };
        if current.id != account.id
            || current.updated_at != account.updated_at
            || current.refresh_token != account.refresh_token
        {
            return Err(AuthError::Storage(
                "active account changed during resource refresh".to_owned(),
            ));
        }
        if rotated_refresh_token.is_empty() || rotated_refresh_token == account.refresh_token {
            return Ok(());
        }
        let previous =
            std::mem::replace(&mut current.refresh_token, rotated_refresh_token.to_owned());
        if let Err(error) = save_cache(&self.path, &cache) {
            cache.accounts[0].refresh_token = previous;
            return Err(error);
        }
        Ok(())
    }

    fn accept_teams_refresh(
        &self,
        account: &AccountToken,
        rotated_refresh_token: &str,
    ) -> Result<(), AuthError> {
        let mut cache = self.cache.lock().expect("token store poisoned");
        let Some(current) = cache.accounts.first_mut() else {
            return Err(AuthError::AccountUnavailable);
        };
        if current.id != account.id
            || current.updated_at != account.updated_at
            || current.teams_refresh_token != account.teams_refresh_token
        {
            return Err(AuthError::Storage(
                "active account changed during Teams refresh".to_owned(),
            ));
        }
        if rotated_refresh_token.is_empty() || rotated_refresh_token == account.teams_refresh_token
        {
            return Ok(());
        }
        let previous = std::mem::replace(
            &mut current.teams_refresh_token,
            rotated_refresh_token.to_owned(),
        );
        if let Err(error) = save_cache(&self.path, &cache) {
            cache.accounts[0].teams_refresh_token = previous;
            return Err(error);
        }
        Ok(())
    }

    async fn request_token<const N: usize>(
        &self,
        form: [(&str, &str); N],
    ) -> Result<TokenSet, AuthError> {
        self.request_token_with_origin(form, None).await
    }

    async fn request_token_with_origin<const N: usize>(
        &self,
        form: [(&str, &str); N],
        origin: Option<&str>,
    ) -> Result<TokenSet, AuthError> {
        let mut request = self
            .client
            .post(&self.config.token_endpoint)
            .form(form.as_slice());
        if let Some(origin) = origin {
            request = request.header(reqwest::header::ORIGIN, origin);
        }
        let response = request
            .send()
            .await
            .map_err(|error| AuthError::Http(error.to_string()))?;
        let status = response.status();
        let payload: TokenResponse =
            response
                .json()
                .await
                .map_err(|_| AuthError::TokenEndpoint {
                    status,
                    code: "decode_error".to_owned(),
                })?;
        if let Some(code) = payload.error {
            return Err(AuthError::TokenEndpoint {
                status,
                code: safe_error_code(&code),
            });
        }
        if !status.is_success() {
            return Err(AuthError::TokenEndpoint {
                status,
                code: "http_error".to_owned(),
            });
        }
        if payload.access_token.is_empty() {
            return Err(AuthError::TokenEndpoint {
                status,
                code: "empty_access_token".to_owned(),
            });
        }
        Ok(token_set(payload))
    }

    fn mark_expired(&self, id: &str) -> Result<(), AuthError> {
        let mut cache = self.cache.lock().expect("token store poisoned");
        let Some(account) = cache.accounts.iter_mut().find(|account| account.id == id) else {
            return Err(AuthError::AccountUnavailable);
        };
        account.status = "expired".to_owned();
        save_cache(&self.path, &cache)
    }
}

#[derive(Default, Deserialize)]
struct TokenResponse {
    #[serde(default)]
    access_token: String,
    #[serde(default)]
    refresh_token: String,
    #[serde(default)]
    id_token: String,
    #[serde(default)]
    token_type: String,
    #[serde(default)]
    scope: String,
    #[serde(default)]
    expires_in: i64,
    error: Option<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum AuthError {
    #[error("{0}")]
    Configuration(String),
    #[error("{0}")]
    Storage(String),
    #[error("{0}")]
    Http(String),
    #[error("token endpoint HTTP {status}: {code}")]
    TokenEndpoint { status: StatusCode, code: String },
    #[error("active account unavailable")]
    AccountUnavailable,
    #[error("token_expired: refresh token missing or expired")]
    Expired,
}

impl From<GatewayError> for AuthError {
    fn from(value: GatewayError) -> Self {
        Self::Storage(value.to_string())
    }
}

pub fn verifier() -> String {
    let mut bytes = [0_u8; 32];
    rand::rng().fill(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

pub fn challenge(verifier: &str) -> String {
    URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()))
}

pub fn authorization_url(
    config: &OAuthConfig,
    state: &str,
    verifier: &str,
) -> Result<Url, AuthError> {
    let mut url = Url::parse(&config.authorize_endpoint)
        .map_err(|_| AuthError::Configuration("invalid OAuth authorize endpoint".to_owned()))?;
    url.query_pairs_mut()
        .append_pair("client_id", &config.client_id)
        .append_pair("response_type", "code")
        .append_pair("redirect_uri", &config.redirect_uri)
        .append_pair("response_mode", "query")
        .append_pair("scope", &config.scope)
        .append_pair("state", state)
        .append_pair("code_challenge", &challenge(verifier))
        .append_pair("code_challenge_method", "S256");
    Ok(url)
}

pub(crate) fn teams_authorization_url(state: &str, verifier: &str) -> Result<Url, AuthError> {
    let mut url = Url::parse("https://login.microsoftonline.com/common/oauth2/v2.0/authorize")
        .map_err(|_| AuthError::Configuration("invalid Teams authorize endpoint".to_owned()))?;
    url.query_pairs_mut()
        .append_pair("client_id", TEAMS_WEB_CLIENT_ID)
        .append_pair("response_type", "code")
        .append_pair("redirect_uri", TEAMS_REDIRECT_URI)
        .append_pair("response_mode", "query")
        .append_pair("scope", TEAMS_AUTHORIZE_SCOPE)
        .append_pair("state", state)
        .append_pair("code_challenge", &challenge(verifier))
        .append_pair("code_challenge_method", "S256")
        .append_pair("prompt", "none");
    Ok(url)
}

fn token_set(payload: TokenResponse) -> TokenSet {
    let access_claims = decode_jwt_claims(&payload.access_token).unwrap_or_default();
    let id_claims = decode_jwt_claims(&payload.id_token).unwrap_or_default();
    let expires_in = payload.expires_in.max(0);
    TokenSet {
        access_token: payload.access_token,
        refresh_token: payload.refresh_token,
        teams_refresh_token: String::new(),
        id_token: payload.id_token,
        token_type: payload.token_type,
        scope: payload.scope,
        expires_in,
        expires_at: OffsetDateTime::now_utc() + Duration::seconds(expires_in),
        email: first_non_empty([
            claim(&access_claims, "unique_name"),
            claim(&access_claims, "upn"),
            claim(&access_claims, "preferred_username"),
            claim(&id_claims, "preferred_username"),
            claim(&id_claims, "email"),
        ]),
        display_name: first_non_empty([claim(&access_claims, "name"), claim(&id_claims, "name")]),
        home_oid: first_non_empty([
            claim(&access_claims, "oid"),
            claim(&access_claims, "sub"),
            claim(&id_claims, "oid"),
            claim(&id_claims, "sub"),
        ]),
        tenant_id: first_non_empty([
            claim(&access_claims, "tid"),
            claim(&access_claims, "tenant_id"),
            claim(&id_claims, "tid"),
            claim(&id_claims, "tenant_id"),
        ]),
    }
}

fn decode_jwt_claims(token: &str) -> Option<serde_json::Map<String, serde_json::Value>> {
    let payload = token.split('.').nth(1)?;
    let bytes = URL_SAFE_NO_PAD.decode(payload).ok()?;
    serde_json::from_slice::<serde_json::Value>(&bytes)
        .ok()?
        .as_object()
        .cloned()
}

pub(crate) fn teams_ic3_token_class(token: &str) -> &'static str {
    let claims = decode_jwt_claims(token);
    let audience = claims
        .as_ref()
        .and_then(|claims| claims.get("aud"))
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default();
    let app_id = claims
        .as_ref()
        .and_then(|claims| claims.get("appid").or_else(|| claims.get("azp")))
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default();
    let scopes = claims
        .as_ref()
        .and_then(|claims| claims.get("scp"))
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default();
    if audience == "https://ic3.teams.office.com"
        && app_id == TEAMS_WEB_CLIENT_ID
        && scopes
            .split_ascii_whitespace()
            .any(|scope| scope == "Teams.AccessAsUser.All")
    {
        "ic3_teams_access"
    } else if audience == "https://ic3.teams.office.com" {
        "ic3_other_scope"
    } else if claims.is_some() {
        "other_audience"
    } else {
        "opaque"
    }
}

fn valid_teams_token_for(token: &TokenSet, oid: &str, tid: &str) -> bool {
    teams_ic3_token_class(&token.access_token) == "ic3_teams_access"
        && !oid.is_empty()
        && token.home_oid == oid
        && !tid.is_empty()
        && token.tenant_id == tid
}

fn claim<'a>(claims: &'a serde_json::Map<String, serde_json::Value>, name: &str) -> &'a str {
    claims
        .get(name)
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default()
}

fn first_non_empty<const N: usize>(values: [&str; N]) -> String {
    values
        .into_iter()
        .find(|value| !value.trim().is_empty())
        .unwrap_or_default()
        .to_owned()
}

fn safe_error_code(code: &str) -> String {
    let code = code.trim();
    if code.is_empty()
        || code.len() > 128
        || !code
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-' | b'.'))
    {
        "oauth_error".to_owned()
    } else {
        code.to_owned()
    }
}

fn save_cache(path: &Path, cache: &TokenCache) -> Result<(), AuthError> {
    private_file::write_json(path, cache).map_err(Into::into)
}

fn env_value(name: &str, default: &str) -> String {
    env::var(name).unwrap_or_else(|_| default.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config() -> OAuthConfig {
        OAuthConfig {
            client_id: DEFAULT_CLIENT_ID.to_owned(),
            authority: DEFAULT_AUTHORITY.to_owned(),
            redirect_uri: DEFAULT_REDIRECT_URI.to_owned(),
            scope: DEFAULT_SCOPE.to_owned(),
            authorize_endpoint: format!("{DEFAULT_AUTHORITY}/oauth2/v2.0/authorize"),
            token_endpoint: format!("{DEFAULT_AUTHORITY}/oauth2/v2.0/token"),
        }
    }

    #[test]
    fn pkce_url_contains_challenge_without_verifier() {
        let verifier = verifier();
        let url = authorization_url(&config(), "state-1", &verifier).unwrap();
        let query: std::collections::HashMap<_, _> = url.query_pairs().into_owned().collect();
        assert_eq!(query["code_challenge"], challenge(&verifier));
        assert_eq!(query["code_challenge_method"], "S256");
        assert!(!url.as_str().contains(&verifier));
    }

    #[test]
    fn teams_pkce_url_contains_challenge_without_verifier() {
        let verifier = verifier();
        let url = teams_authorization_url("teams-state", &verifier).unwrap();
        let query: std::collections::HashMap<_, _> = url.query_pairs().into_owned().collect();
        assert_eq!(query["client_id"], TEAMS_WEB_CLIENT_ID);
        assert_eq!(query["redirect_uri"], TEAMS_REDIRECT_URI);
        assert_eq!(query["scope"], TEAMS_AUTHORIZE_SCOPE);
        assert_eq!(query["state"], "teams-state");
        assert_eq!(query["code_challenge"], challenge(&verifier));
        assert_eq!(query["code_challenge_method"], "S256");
        assert_eq!(query["prompt"], "none");
        assert!(!url.as_str().contains(&verifier));
    }

    #[test]
    fn teams_refresh_is_bound_only_to_the_same_account() {
        let primary = token_for(
            "same-oid",
            "same-tenant",
            "primary-access",
            "primary-refresh",
        );
        let teams = token_for(
            "same-oid",
            "same-tenant",
            &teams_access_token("same-oid", "same-tenant"),
            "teams-refresh",
        );
        let bound = primary.clone().bind_teams_refresh(teams).unwrap();
        assert_eq!(bound.refresh_token, "primary-refresh");
        assert_eq!(bound.teams_refresh_token, "teams-refresh");

        let wrong_account = token_for(
            "other-oid",
            "same-tenant",
            &teams_access_token("other-oid", "same-tenant"),
            "teams-refresh",
        );
        assert!(primary.bind_teams_refresh(wrong_account).is_err());
    }

    #[test]
    fn upsert_does_not_reuse_teams_access_for_a_different_account_with_the_same_email() {
        let root = tempfile::tempdir().unwrap();
        let store = TokenStore::open(root.path().join("accounts.json"), config()).unwrap();
        let mut first = token_for("first-oid", "first-tenant", "access-1", "refresh-1");
        first.email = "shared@example.invalid".to_owned();
        first.teams_refresh_token = "teams-refresh-1".to_owned();
        store.upsert(first).unwrap();

        let mut same_account = token_for("first-oid", "first-tenant", "access-1b", "refresh-1b");
        same_account.email = "shared@example.invalid".to_owned();
        assert_eq!(
            store.upsert(same_account).unwrap().teams_refresh_token,
            "teams-refresh-1"
        );

        let mut second = token_for("second-oid", "second-tenant", "access-2", "refresh-2");
        second.email = "shared@example.invalid".to_owned();
        let account = store.upsert(second).unwrap();

        assert_eq!(account.oid, "second-oid");
        assert_eq!(account.tid, "second-tenant");
        assert!(account.teams_refresh_token.is_empty());
    }

    #[test]
    fn teams_token_class_accepts_the_exact_client_in_v1_or_v2_claims() {
        for client_claim in ["appid", "azp"] {
            let mut payload = serde_json::json!({
                "aud": "https://ic3.teams.office.com",
                "scp": "Teams.AccessAsUser.All",
            });
            payload[client_claim] = TEAMS_WEB_CLIENT_ID.into();
            let claims = URL_SAFE_NO_PAD.encode(serde_json::to_vec(&payload).unwrap());
            assert_eq!(
                teams_ic3_token_class(&format!("header.{claims}.signature")),
                "ic3_teams_access"
            );
        }
    }

    #[test]
    fn token_store_migrates_to_newest_single_account() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("accounts.json");
        let older = account("old", 10);
        let newer = account("new", 20);
        private_file::write_json(
            &path,
            &TokenCache {
                schema: TOKEN_CACHE_SCHEMA.to_owned(),
                accounts: vec![older, newer],
            },
        )
        .unwrap();
        let store = TokenStore::open(&path, config()).unwrap();
        assert_eq!(store.first().unwrap().id, "new");
        let persisted: TokenCache = private_file::read_json(&path).unwrap().unwrap();
        assert_eq!(persisted.accounts.len(), 1);
    }

    #[test]
    fn resource_refresh_persists_only_the_rotated_refresh_token() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("accounts.json");
        let original = account("active", 20);
        private_file::write_json(
            &path,
            &TokenCache {
                schema: TOKEN_CACHE_SCHEMA.to_owned(),
                accounts: vec![original.clone()],
            },
        )
        .unwrap();
        let store = TokenStore::open(&path, config()).unwrap();
        store
            .accept_resource_refresh(&original, "refresh-rotated")
            .unwrap();

        let reopened = TokenStore::open(&path, config()).unwrap();
        let persisted = reopened.first().unwrap();
        assert_eq!(persisted.refresh_token, "refresh-rotated");
        assert_eq!(persisted.access_token, original.access_token);
        assert_eq!(persisted.expires_at, original.expires_at);
        assert_eq!(persisted.updated_at, original.updated_at);
    }

    #[test]
    fn resource_refresh_rejects_an_account_that_changed_during_the_request() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("accounts.json");
        let original = account("active", 20);
        private_file::write_json(
            &path,
            &TokenCache {
                schema: TOKEN_CACHE_SCHEMA.to_owned(),
                accounts: vec![original.clone()],
            },
        )
        .unwrap();
        let store = TokenStore::open(&path, config()).unwrap();
        store
            .upsert(token_for(
                "replacement-oid",
                "replacement-tenant",
                "replacement-access",
                "replacement-refresh",
            ))
            .unwrap();

        assert!(
            store
                .accept_resource_refresh(&original, &original.refresh_token)
                .is_err()
        );
        assert!(
            store
                .accept_teams_refresh(&original, &original.teams_refresh_token)
                .is_err()
        );
    }

    #[tokio::test]
    async fn teams_ic3_refresh_uses_the_teams_client_and_spa_origin() {
        let captured = std::sync::Arc::new(Mutex::new(None));
        let server_capture = captured.clone();
        let access_token = teams_access_token("active", "tenant");
        let app = axum::Router::new().route(
            "/",
            axum::routing::post(
                move |headers: axum::http::HeaderMap, body: String| async move {
                    *server_capture.lock().unwrap() = Some((headers, body));
                    axum::Json(serde_json::json!({
                        "access_token": access_token,
                        "refresh_token": "teams-refresh-rotated",
                        "expires_in": 3600
                    }))
                },
            ),
        );
        let listener = tokio::net::TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .await
            .unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("accounts.json");
        let mut original = account("active", 20);
        original.tid = "tenant".to_owned();
        original.teams_refresh_token = "teams-refresh".to_owned();
        private_file::write_json(
            &path,
            &TokenCache {
                schema: TOKEN_CACHE_SCHEMA.to_owned(),
                accounts: vec![original],
            },
        )
        .unwrap();
        let mut oauth = config();
        oauth.token_endpoint = format!("http://{address}/");
        let store = TokenStore::open(&path, oauth).unwrap();
        let (token, account_oid) = store.teams_ic3_access_token().await.unwrap();
        assert_eq!(teams_ic3_token_class(&token), "ic3_teams_access");
        assert_eq!(account_oid, "active");

        let (headers, body) = captured.lock().unwrap().take().unwrap();
        assert_eq!(
            headers.get(reqwest::header::ORIGIN).unwrap(),
            TEAMS_WEB_ORIGIN
        );
        let form = url::form_urlencoded::parse(body.as_bytes())
            .into_owned()
            .collect::<std::collections::HashMap<_, _>>();
        assert_eq!(form["client_id"], TEAMS_WEB_CLIENT_ID);
        assert_eq!(form["scope"], TEAMS_IC3_SCOPE);
        assert_eq!(form["grant_type"], "refresh_token");
        assert_eq!(form["refresh_token"], "teams-refresh");
        let persisted = store.first().unwrap();
        assert_eq!(persisted.refresh_token, "refresh");
        assert_eq!(persisted.teams_refresh_token, "teams-refresh-rotated");
        server.abort();
    }

    #[tokio::test]
    async fn teams_code_exchange_uses_pkce_and_the_fixed_teams_redirect() {
        let captured = std::sync::Arc::new(Mutex::new(None));
        let server_capture = captured.clone();
        let access_token = teams_access_token("active", "tenant");
        let app = axum::Router::new().route(
            "/",
            axum::routing::post(
                move |headers: axum::http::HeaderMap, body: String| async move {
                    *server_capture.lock().unwrap() = Some((headers, body));
                    axum::Json(serde_json::json!({
                        "access_token": access_token,
                        "refresh_token": "teams-refresh",
                        "expires_in": 3600
                    }))
                },
            ),
        );
        let listener = tokio::net::TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .await
            .unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });

        let root = tempfile::tempdir().unwrap();
        let mut oauth = config();
        oauth.token_endpoint = format!("http://{address}/");
        let store = TokenStore::open(root.path().join("accounts.json"), oauth).unwrap();
        let token = store
            .exchange_teams_code("one-time-code", "pkce-verifier")
            .await
            .unwrap();
        assert_eq!(token.refresh_token, "teams-refresh");
        assert_eq!(token.home_oid, "active");
        assert_eq!(token.tenant_id, "tenant");

        let (headers, body) = captured.lock().unwrap().take().unwrap();
        assert_eq!(
            headers.get(reqwest::header::ORIGIN).unwrap(),
            TEAMS_WEB_ORIGIN
        );
        let form = url::form_urlencoded::parse(body.as_bytes())
            .into_owned()
            .collect::<std::collections::HashMap<_, _>>();
        assert_eq!(form["client_id"], TEAMS_WEB_CLIENT_ID);
        assert_eq!(form["grant_type"], "authorization_code");
        assert_eq!(form["code"], "one-time-code");
        assert_eq!(form["redirect_uri"], TEAMS_REDIRECT_URI);
        assert_eq!(form["code_verifier"], "pkce-verifier");
        assert_eq!(form["scope"], TEAMS_AUTHORIZE_SCOPE);
        server.abort();
    }

    #[test]
    fn unsafe_oauth_error_code_is_redacted() {
        assert_eq!(safe_error_code("invalid_grant"), "invalid_grant");
        assert_eq!(safe_error_code("secret value"), "oauth_error");
    }

    fn account(id: &str, updated: i64) -> AccountToken {
        AccountToken {
            id: id.to_owned(),
            email: format!("{id}@example.invalid"),
            display_name: String::new(),
            status: "online".to_owned(),
            access_token: "secret".to_owned(),
            refresh_token: "refresh".to_owned(),
            teams_refresh_token: String::new(),
            expires_at: OffsetDateTime::from_unix_timestamp(updated + 1_000).unwrap(),
            updated_at: OffsetDateTime::from_unix_timestamp(updated).unwrap(),
            oid: id.to_owned(),
            tid: String::new(),
            client_id: DEFAULT_CLIENT_ID.to_owned(),
        }
    }

    fn teams_access_token(oid: &str, tid: &str) -> String {
        let claims = URL_SAFE_NO_PAD.encode(
            serde_json::to_vec(&serde_json::json!({
                "aud": "https://ic3.teams.office.com",
                "appid": TEAMS_WEB_CLIENT_ID,
                "scp": "Teams.AccessAsUser.All",
                "oid": oid,
                "tid": tid,
            }))
            .unwrap(),
        );
        format!("header.{claims}.signature")
    }

    fn token_for(oid: &str, tid: &str, access_token: &str, refresh_token: &str) -> TokenSet {
        TokenSet {
            access_token: access_token.to_owned(),
            refresh_token: refresh_token.to_owned(),
            teams_refresh_token: String::new(),
            id_token: String::new(),
            token_type: "Bearer".to_owned(),
            scope: String::new(),
            expires_in: 3_600,
            expires_at: OffsetDateTime::now_utc() + Duration::hours(1),
            email: String::new(),
            display_name: String::new(),
            home_oid: oid.to_owned(),
            tenant_id: tid.to_owned(),
        }
    }
}
