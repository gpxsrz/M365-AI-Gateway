use std::path::{Path, PathBuf};

use rand::RngCore;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

use crate::{
    auth::{OAuthConfig, TOKEN_CACHE_SCHEMA, TokenStore},
    error::GatewayError,
    private_file,
};

const MANIFEST_SCHEMA: &str = "m365-oauth-profile/v1";

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub(crate) struct Validation {
    pub(crate) chathub: bool,
    pub(crate) refresh: bool,
    pub(crate) restart: bool,
    pub(crate) removal: bool,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct Manifest {
    pub(crate) schema: String,
    pub(crate) profile_id: String,
    pub(crate) kind: String,
    pub(crate) token_cache_schema: String,
    pub(crate) oauth: OAuthConfig,
    pub(crate) validation: Validation,
    #[serde(with = "time::serde::rfc3339")]
    pub(crate) created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub(crate) updated_at: OffsetDateTime,
}

#[derive(Clone)]
pub(crate) struct Store {
    root: PathBuf,
}

impl Store {
    pub(crate) fn open(base_token_path: &Path) -> Result<Self, GatewayError> {
        let parent = base_token_path
            .parent()
            .ok_or_else(|| GatewayError::Storage("OAuth token path has no parent".to_owned()))?;
        let stem = base_token_path
            .file_stem()
            .and_then(|value| value.to_str())
            .filter(|value| !value.is_empty())
            .unwrap_or("accounts");
        Ok(Self {
            root: parent.join(format!("{stem}-oauth-profiles")),
        })
    }

    pub(crate) fn stage_from_active(
        &self,
        active: &TokenStore,
    ) -> Result<(Manifest, TokenStore), GatewayError> {
        for _ in 0..8 {
            let id = profile_id();
            let directory = self.root.join(&id);
            if directory.exists() {
                continue;
            }
            let token_path = directory.join("accounts.json");
            active
                .snapshot_to(&token_path)
                .map_err(|error| GatewayError::Storage(error.to_string()))?;
            let now = OffsetDateTime::now_utc();
            let manifest = Manifest {
                schema: MANIFEST_SCHEMA.to_owned(),
                profile_id: id,
                kind: "staged".to_owned(),
                token_cache_schema: TOKEN_CACHE_SCHEMA.to_owned(),
                oauth: active.config().clone(),
                validation: Validation::default(),
                created_at: now,
                updated_at: now,
            };
            if let Err(error) = private_file::write_json(&directory.join("profile.json"), &manifest)
            {
                let _ = std::fs::remove_dir_all(&directory);
                return Err(error);
            }
            let store = TokenStore::open(token_path, manifest.oauth.clone())
                .map_err(|error| GatewayError::Storage(error.to_string()))?;
            return Ok((manifest, store));
        }
        Err(GatewayError::Storage(
            "unable to allocate OAuth profile ID".to_owned(),
        ))
    }

    pub(crate) fn stage(&self, oauth: OAuthConfig) -> Result<(Manifest, TokenStore), GatewayError> {
        for _ in 0..8 {
            let id = profile_id();
            let directory = self.root.join(&id);
            if directory.exists() {
                continue;
            }
            std::fs::create_dir_all(&directory)
                .map_err(|error| GatewayError::Storage(error.to_string()))?;
            let now = OffsetDateTime::now_utc();
            let manifest = Manifest {
                schema: MANIFEST_SCHEMA.to_owned(),
                profile_id: id,
                kind: "staged".to_owned(),
                token_cache_schema: TOKEN_CACHE_SCHEMA.to_owned(),
                oauth: oauth.clone(),
                validation: Validation::default(),
                created_at: now,
                updated_at: now,
            };
            if let Err(error) = private_file::write_json(&directory.join("profile.json"), &manifest)
            {
                let _ = std::fs::remove_dir_all(&directory);
                return Err(error);
            }
            let store = TokenStore::open(directory.join("accounts.json"), oauth)
                .map_err(|error| GatewayError::Storage(error.to_string()))?;
            return Ok((manifest, store));
        }
        Err(GatewayError::Storage(
            "unable to allocate OAuth profile ID".to_owned(),
        ))
    }

    pub(crate) fn open_store(&self, id: &str) -> Result<(Manifest, TokenStore), GatewayError> {
        if !valid_id(id) {
            return Err(GatewayError::InvalidRequest(
                "invalid OAuth profile ID".to_owned(),
            ));
        }
        let directory = self.root.join(id);
        let manifest = private_file::read_json::<Manifest>(&directory.join("profile.json"))?
            .ok_or_else(|| GatewayError::InvalidRequest("OAuth profile not found".to_owned()))?;
        if manifest.schema != MANIFEST_SCHEMA
            || manifest.profile_id != id
            || manifest.kind != "staged"
            || manifest.token_cache_schema != TOKEN_CACHE_SCHEMA
            || manifest.updated_at < manifest.created_at
        {
            return Err(GatewayError::Storage(
                "invalid OAuth profile manifest".to_owned(),
            ));
        }
        let store = TokenStore::open(directory.join("accounts.json"), manifest.oauth.clone())
            .map_err(|error| GatewayError::Storage(error.to_string()))?;
        Ok((manifest, store))
    }

    pub(crate) fn record_chathub(&self, id: &str) -> Result<Manifest, GatewayError> {
        let (mut manifest, _) = self.open_store(id)?;
        manifest.validation.chathub = true;
        manifest.updated_at = OffsetDateTime::now_utc();
        private_file::write_json(&self.root.join(id).join("profile.json"), &manifest)?;
        Ok(manifest)
    }

    pub(crate) fn discard(&self, id: &str) {
        if valid_id(id) {
            let _ = std::fs::remove_dir_all(self.root.join(id));
        }
    }
}

fn profile_id() -> String {
    let mut bytes = [0_u8; 16];
    rand::rng().fill_bytes(&mut bytes);
    let mut output = String::with_capacity(7 + bytes.len() * 2);
    output.push_str("oauthp_");
    for byte in bytes {
        use std::fmt::Write;
        let _ = write!(output, "{byte:02x}");
    }
    output
}

fn valid_id(value: &str) -> bool {
    value.len() == 7 + 32
        && value.starts_with("oauthp_")
        && value[7..].bytes().all(|byte| byte.is_ascii_hexdigit())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::OAuthConfig;

    #[test]
    fn candidate_profile_is_private_and_does_not_replace_active_store() {
        let root = tempfile::tempdir().unwrap();
        let oauth = OAuthConfig::from_env().unwrap();
        let active = TokenStore::open(root.path().join("accounts.json"), oauth).unwrap();
        let profiles = Store::open(active.path()).unwrap();
        let (manifest, candidate) = profiles.stage_from_active(&active).unwrap();
        assert_eq!(manifest.kind, "staged");
        assert!(candidate.first().is_none());
        assert!(active.first().is_none());
    }
}
