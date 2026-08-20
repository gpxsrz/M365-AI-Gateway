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
        let account = AccountToken {
            id: id.clone(),
            email: token.email,
            display_name: token.display_name,
            status: "online".to_owned(),
            access_token: token.access_token,
            refresh_token: if token.refresh_token.is_empty() {
                previous
                    .as_ref()
                    .map(|account| account.refresh_token.clone())
                    .unwrap_or_default()
            } else {
                token.refresh_token
            },
            expires_at: token.expires_at,
            updated_at: now,
            oid: if token.home_oid.is_empty() {
                id.clone()
            } else {
                token.home_oid
            },
            tid: if token.tenant_id.is_empty() {
                previous
                    .as_ref()
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
        let account = self.first().ok_or(AuthError::AccountUnavailable)?;
        if account.refresh_token.is_empty() {
            return Err(AuthError::Expired);
        }
        self.refresh(&account.refresh_token, scope)
            .await
            .map(|token| token.access_token)
    }

    async fn request_token<const N: usize>(
        &self,
        form: [(&str, &str); N],
    ) -> Result<TokenSet, AuthError> {
        let response = self
            .client
            .post(&self.config.token_endpoint)
            .form(form.as_slice())
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

fn token_set(payload: TokenResponse) -> TokenSet {
    let access_claims = decode_jwt_claims(&payload.access_token).unwrap_or_default();
    let id_claims = decode_jwt_claims(&payload.id_token).unwrap_or_default();
    let expires_in = payload.expires_in.max(0);
    TokenSet {
        access_token: payload.access_token,
        refresh_token: payload.refresh_token,
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
            expires_at: OffsetDateTime::from_unix_timestamp(updated + 1_000).unwrap(),
            updated_at: OffsetDateTime::from_unix_timestamp(updated).unwrap(),
            oid: id.to_owned(),
            tid: String::new(),
            client_id: DEFAULT_CLIENT_ID.to_owned(),
        }
    }
}
