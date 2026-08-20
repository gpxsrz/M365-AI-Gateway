use std::{collections::HashMap, sync::Mutex};

use rand::Rng;
use serde::Serialize;
use time::{Duration, OffsetDateTime};

use crate::auth::{AccountToken, OAuthConfig, authorization_url, verifier};

const TRANSACTION_TTL: Duration = Duration::minutes(10);
const TRANSACTION_RETENTION: Duration = Duration::minutes(30);

#[derive(Clone)]
struct PendingPkce {
    verifier: String,
    redirect_uri: String,
    oauth: OAuthConfig,
    created_at: OffsetDateTime,
    status: PkceState,
    account: Option<AccountView>,
    error_code: String,
    error: String,
    profile_id: String,
    profile_kind: String,
    staged: bool,
    discard_on_failure: bool,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum PkceState {
    Pending,
    Processing,
    Authenticated,
    Cancelled,
    Error,
    Expired,
}

impl PkceState {
    fn as_str(self) -> &'static str {
        match self {
            Self::Pending => "pending",
            Self::Processing => "processing",
            Self::Authenticated => "authenticated",
            Self::Cancelled => "cancelled",
            Self::Error => "error",
            Self::Expired => "expired",
        }
    }
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AccountView {
    pub status: String,
    #[serde(with = "time::serde::rfc3339")]
    pub expires_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

impl From<&AccountToken> for AccountView {
    fn from(account: &AccountToken) -> Self {
        Self {
            status: account.status.clone(),
            expires_at: account.expires_at,
            updated_at: account.updated_at,
        }
    }
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PkceStart {
    pub status: &'static str,
    pub state: String,
    pub oauth_profile_id: String,
    pub oauth_profile_kind: String,
    pub staged: bool,
    pub url: String,
    pub redirect_uri: String,
    pub note: &'static str,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PkceStatus {
    pub status: &'static str,
    pub oauth_profile_id: String,
    pub staged: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub account: Option<AccountView>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error_code: String,
}

#[derive(Clone, Debug)]
pub struct ClaimedPkce {
    pub verifier: String,
    pub redirect_uri: String,
    pub oauth: OAuthConfig,
    pub profile_id: String,
    pub staged: bool,
    pub discard_on_failure: bool,
}

#[derive(Clone, Debug, thiserror::Error)]
#[error("{message}")]
pub struct PkceError {
    pub status: u16,
    pub code: &'static str,
    pub message: &'static str,
}

pub struct PkceManager {
    transactions: Mutex<HashMap<String, PendingPkce>>,
}

impl Default for PkceManager {
    fn default() -> Self {
        Self {
            transactions: Mutex::new(HashMap::new()),
        }
    }
}

impl PkceManager {
    pub fn start(&self, oauth: OAuthConfig, now: OffsetDateTime) -> Result<PkceStart, PkceError> {
        self.start_target(oauth, "legacy", "legacy", false, now)
    }

    pub fn start_target(
        &self,
        oauth: OAuthConfig,
        profile_id: &str,
        profile_kind: &str,
        staged: bool,
        now: OffsetDateTime,
    ) -> Result<PkceStart, PkceError> {
        self.start_target_owned(oauth, profile_id, profile_kind, staged, false, now)
    }

    pub fn start_target_owned(
        &self,
        oauth: OAuthConfig,
        profile_id: &str,
        profile_kind: &str,
        staged: bool,
        discard_on_failure: bool,
        now: OffsetDateTime,
    ) -> Result<PkceStart, PkceError> {
        let verifier = verifier();
        let mut state_bytes = [0_u8; 16];
        rand::rng().fill(&mut state_bytes);
        let state = hex(&state_bytes);
        let url = authorization_url(&oauth, &state, &verifier).map_err(|_| PkceError {
            status: 500,
            code: "oauth_authorization_url_failed",
            message: "無法建立 OAuth 授權網址",
        })?;
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        transactions.insert(
            state.clone(),
            PendingPkce {
                verifier,
                redirect_uri: oauth.redirect_uri.clone(),
                oauth,
                created_at: now,
                status: PkceState::Pending,
                account: None,
                error_code: String::new(),
                error: String::new(),
                profile_id: profile_id.to_owned(),
                profile_kind: profile_kind.to_owned(),
                staged,
                discard_on_failure,
            },
        );
        let transaction = transactions.get(&state).unwrap();
        Ok(PkceStart {
            status: "pkce_ready",
            state,
            oauth_profile_id: transaction.profile_id.clone(),
            oauth_profile_kind: transaction.profile_kind.clone(),
            staged: transaction.staged,
            url: url.into(),
            redirect_uri: transaction.redirect_uri.clone(),
            note: "正常流程會自動完成 callback；只有自動 handoff 無法使用時才使用手動 JSON POST 備援。",
        })
    }

    pub fn prune_discard_targets(&self, now: OffsetDateTime) -> Vec<String> {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        let mut targets = Vec::new();
        transactions.retain(|_, transaction| {
            let retained = now - transaction.created_at <= TRANSACTION_RETENTION;
            if !retained
                && transaction.staged
                && transaction.discard_on_failure
                && transaction.status != PkceState::Authenticated
            {
                targets.push(transaction.profile_id.clone());
            }
            retained
        });
        targets.sort_unstable();
        targets.dedup();
        targets
    }

    pub fn take_discard_target(&self, state: &str) -> Option<String> {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        let transaction = transactions.get_mut(state)?;
        if !transaction.staged
            || !transaction.discard_on_failure
            || !matches!(
                transaction.status,
                PkceState::Cancelled | PkceState::Error | PkceState::Expired
            )
        {
            return None;
        }
        transaction.discard_on_failure = false;
        Some(transaction.profile_id.clone())
    }

    pub fn status(&self, state: &str, now: OffsetDateTime) -> PkceStatus {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        let Some(transaction) = transactions.get_mut(state) else {
            return PkceStatus {
                status: "expired",
                oauth_profile_id: "legacy".to_owned(),
                staged: false,
                account: None,
                error: String::new(),
                error_code: "oauth_state_mismatch".to_owned(),
            };
        };
        if transaction.status == PkceState::Pending
            && now - transaction.created_at > TRANSACTION_TTL
        {
            transaction.status = PkceState::Expired;
            transaction.verifier.clear();
            transaction.error_code = "oauth_state_expired".to_owned();
            transaction.error = "OAuth 授權工作階段已過期，請重新開始授權".to_owned();
        }
        view(transaction)
    }

    pub fn redirect_uri(&self, state: &str) -> Option<String> {
        self.transactions
            .lock()
            .expect("PKCE state poisoned")
            .get(state)
            .map(|transaction| transaction.redirect_uri.clone())
    }

    pub fn claim(&self, state: &str, now: OffsetDateTime) -> Result<ClaimedPkce, PkceError> {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        transactions.retain(|_, transaction| now - transaction.created_at <= TRANSACTION_RETENTION);
        let transaction = transactions.get_mut(state).ok_or(PkceError {
            status: 400,
            code: "oauth_state_mismatch",
            message: "OAuth state 不符合目前的授權工作階段",
        })?;
        if transaction.status != PkceState::Pending {
            return Err(PkceError {
                status: 409,
                code: "oauth_state_replayed",
                message: "OAuth state 已使用或正在處理，請重新開始授權",
            });
        }
        if now - transaction.created_at > TRANSACTION_TTL {
            transaction.status = PkceState::Expired;
            transaction.verifier.clear();
            transaction.error_code = "oauth_state_expired".to_owned();
            transaction.error = "OAuth 授權工作階段已過期，請重新開始授權".to_owned();
            return Err(PkceError {
                status: 410,
                code: "oauth_state_expired",
                message: "OAuth 授權工作階段已過期，請重新開始授權",
            });
        }
        let claimed = ClaimedPkce {
            verifier: transaction.verifier.clone(),
            redirect_uri: transaction.redirect_uri.clone(),
            oauth: transaction.oauth.clone(),
            profile_id: transaction.profile_id.clone(),
            staged: transaction.staged,
            discard_on_failure: transaction.discard_on_failure,
        };
        transaction.status = PkceState::Processing;
        transaction.verifier.clear();
        transaction.error.clear();
        transaction.error_code.clear();
        Ok(claimed)
    }

    pub fn authenticated(&self, state: &str, account: AccountView) {
        self.finish(state, PkceState::Authenticated, "", "", Some(account));
    }

    pub fn cancelled(&self, state: &str) {
        self.finish(
            state,
            PkceState::Cancelled,
            "oauth_authorization_cancelled",
            "Microsoft 授權已取消，請在需要時重新開始授權",
            None,
        );
    }

    pub fn failed(&self, state: &str, code: &str, message: &str) {
        self.finish(state, PkceState::Error, code, message, None);
    }

    pub fn fail_pending(&self, state: &str, code: &str, message: &str) {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        let Some(transaction) = transactions.get_mut(state) else {
            return;
        };
        if transaction.status != PkceState::Pending {
            return;
        }
        transaction.status = PkceState::Error;
        transaction.verifier.clear();
        transaction.error_code = code.to_owned();
        transaction.error = message.to_owned();
    }

    fn finish(
        &self,
        state: &str,
        status: PkceState,
        error_code: &str,
        error: &str,
        account: Option<AccountView>,
    ) {
        let mut transactions = self.transactions.lock().expect("PKCE state poisoned");
        let Some(transaction) = transactions.get_mut(state) else {
            return;
        };
        if transaction.status != PkceState::Processing {
            return;
        }
        transaction.status = status;
        transaction.verifier.clear();
        transaction.account = account;
        transaction.error_code = error_code.to_owned();
        transaction.error = error.to_owned();
    }
}

fn view(transaction: &PendingPkce) -> PkceStatus {
    PkceStatus {
        status: transaction.status.as_str(),
        oauth_profile_id: transaction.profile_id.clone(),
        staged: transaction.staged,
        account: transaction.account.clone(),
        error: transaction.error.clone(),
        error_code: transaction.error_code.clone(),
    }
}

fn hex(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut output = String::with_capacity(bytes.len() * 2);
    for &byte in bytes {
        output.push(DIGITS[(byte >> 4) as usize] as char);
        output.push(DIGITS[(byte & 0x0f) as usize] as char);
    }
    output
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::{DEFAULT_AUTHORITY, DEFAULT_CLIENT_ID, DEFAULT_REDIRECT_URI, DEFAULT_SCOPE};

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

    fn now() -> OffsetDateTime {
        OffsetDateTime::from_unix_timestamp(1_800_000_000).unwrap()
    }

    #[test]
    fn state_can_only_be_claimed_once() {
        let manager = PkceManager::default();
        let started = manager.start(config(), now()).unwrap();
        assert!(manager.claim(&started.state, now()).is_ok());
        let replay = manager.claim(&started.state, now()).unwrap_err();
        assert_eq!(replay.code, "oauth_state_replayed");
    }

    #[test]
    fn pending_state_expires_after_ten_minutes() {
        let manager = PkceManager::default();
        let started = manager.start(config(), now()).unwrap();
        let status = manager.status(&started.state, now() + Duration::minutes(11));
        assert_eq!(status.status, "expired");
        assert_eq!(status.error_code, "oauth_state_expired");
    }

    #[test]
    fn owned_candidates_are_cleaned_only_when_abandoned() {
        let manager = PkceManager::default();
        let abandoned = manager
            .start_target_owned(config(), "oauthp_abandoned", "staged", true, true, now())
            .unwrap();
        let completed = manager
            .start_target_owned(config(), "oauthp_completed", "staged", true, true, now())
            .unwrap();
        manager.claim(&completed.state, now()).unwrap();
        manager.authenticated(
            &completed.state,
            AccountView {
                status: "active".to_owned(),
                expires_at: now() + Duration::hours(1),
                updated_at: now(),
            },
        );

        assert_eq!(
            manager.prune_discard_targets(now() + Duration::minutes(31)),
            vec!["oauthp_abandoned"]
        );
        assert_eq!(manager.status(&abandoned.state, now()).status, "expired");
        assert_eq!(manager.status(&completed.state, now()).status, "expired");
    }

    #[test]
    fn expired_owned_candidate_cleanup_is_claimed_once() {
        let manager = PkceManager::default();
        let started = manager
            .start_target_owned(config(), "oauthp_expired", "staged", true, true, now())
            .unwrap();
        let error = manager
            .claim(&started.state, now() + Duration::minutes(11))
            .unwrap_err();
        assert_eq!(error.code, "oauth_state_expired");
        assert_eq!(
            manager.take_discard_target(&started.state),
            Some("oauthp_expired".to_owned())
        );
        assert_eq!(manager.take_discard_target(&started.state), None);
    }
}
