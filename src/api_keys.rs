use std::{
    path::{Path, PathBuf},
    sync::Mutex,
    time::Duration,
};

use rand::Rng;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use time::OffsetDateTime;

use crate::{error::GatewayError, private_file};

const USAGE_PERSIST_INTERVAL: Duration = Duration::from_secs(60);

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApiKeyRecord {
    pub id: String,
    pub name: String,
    pub prefix: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hash: String,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub last_used_at: Option<OffsetDateTime>,
    pub revoked: bool,
}

#[derive(Default, Deserialize, Serialize)]
struct PersistedKeys {
    #[serde(default)]
    keys: Vec<ApiKeyRecord>,
}

pub struct ApiKeyStore {
    path: PathBuf,
    state: Mutex<ApiKeyState>,
}

struct ApiKeyState {
    keys: Vec<ApiKeyRecord>,
    last_usage_persist_attempt: Option<OffsetDateTime>,
}

impl ApiKeyStore {
    pub fn open(path: impl Into<PathBuf>) -> Result<Self, GatewayError> {
        let path = path.into();
        let keys = private_file::read_json::<PersistedKeys>(&path)?
            .unwrap_or_default()
            .keys;
        Ok(Self {
            path,
            state: Mutex::new(ApiKeyState {
                keys,
                last_usage_persist_attempt: None,
            }),
        })
    }

    pub fn create(&self, name: impl Into<String>) -> Result<(ApiKeyRecord, String), GatewayError> {
        let mut random = [0_u8; 32];
        rand::rng().fill(&mut random);
        let encoded = hex(&random);
        let raw = format!("m365_{encoded}");
        let record = ApiKeyRecord {
            id: hex(&random[..8]),
            name: name.into(),
            prefix: raw[..12].to_owned(),
            hash: key_hash(&raw),
            created_at: OffsetDateTime::now_utc(),
            last_used_at: None,
            revoked: false,
        };

        let mut state = self.state.lock().expect("API key store poisoned");
        state.keys.push(record.clone());
        if let Err(error) = save(&self.path, &state.keys) {
            state.keys.pop();
            return Err(error);
        }
        Ok((record, raw))
    }

    pub fn list(&self) -> Vec<ApiKeyRecord> {
        self.state
            .lock()
            .expect("API key store poisoned")
            .keys
            .iter()
            .cloned()
            .map(|mut record| {
                record.hash.clear();
                record
            })
            .collect()
    }

    pub fn revoke(&self, id: &str) -> Result<bool, GatewayError> {
        let mut state = self.state.lock().expect("API key store poisoned");
        let Some(position) = state
            .keys
            .iter()
            .position(|record| record.id == id && !record.revoked)
        else {
            return Ok(false);
        };
        state.keys[position].revoked = true;
        if let Err(error) = save(&self.path, &state.keys) {
            state.keys[position].revoked = false;
            return Err(error);
        }
        Ok(true)
    }

    pub fn authenticate(&self, raw: &str) -> Option<String> {
        let mut state = self.state.lock().expect("API key store poisoned");
        let digest = key_hash(raw);
        let position = state
            .keys
            .iter()
            .position(|record| record.hash == digest && !record.revoked)?;
        let now = OffsetDateTime::now_utc();
        state.keys[position].last_used_at = Some(now);
        let should_persist = state.last_usage_persist_attempt.is_none_or(|last| {
            now - last >= time::Duration::seconds(USAGE_PERSIST_INTERVAL.as_secs() as i64)
        });
        if should_persist {
            state.last_usage_persist_attempt = Some(now);
            let _ = save(&self.path, &state.keys);
        }
        Some(state.keys[position].id.clone())
    }
}

fn save(path: &Path, keys: &[ApiKeyRecord]) -> Result<(), GatewayError> {
    private_file::write_json(
        path,
        &PersistedKeys {
            keys: keys.to_vec(),
        },
    )
}

fn key_hash(raw: &str) -> String {
    hex(&Sha256::digest(raw.as_bytes()))
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

    #[test]
    fn raw_key_is_returned_once_and_never_listed_or_persisted() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("api-keys.json");
        let store = ApiKeyStore::open(&path).unwrap();
        let (created, raw) = store.create("Hermes").unwrap();
        assert!(raw.starts_with("m365_"));
        assert_eq!(store.authenticate(&raw), Some(created.id.clone()));
        assert!(store.list()[0].hash.is_empty());
        let persisted = std::fs::read_to_string(&path).unwrap();
        assert!(!persisted.contains(&raw));

        drop(store);
        let reopened = ApiKeyStore::open(&path).unwrap();
        assert_eq!(reopened.authenticate(&raw), Some(created.id.clone()));
        assert!(reopened.revoke(&created.id).unwrap());
        assert_eq!(reopened.authenticate(&raw), None);
    }
}
