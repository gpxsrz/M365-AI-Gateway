use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};

use rand::Rng;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use time::{Duration, OffsetDateTime};

use crate::{agent_ledger::AgentLedger, error::GatewayError, private_file};

const SCHEMA: &str = "wp6-transport-checkpoints/rust-v1";
const TTL: Duration = Duration::hours(24);
const MAX_RECORDS: usize = 256;
const MAX_MESSAGES: usize = 4_096;
const MESSAGE_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/message/v1\0";
const CHAIN_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/chain/v1\0";
const OWNER_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/owner/v1\0";
const KEY_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/key/v1\0";
const CURSOR_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/cursor/v1\0";
const MAX_CURSORS: usize = 64;

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct CheckpointMessage {
    pub role: String,
    pub content: Value,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tool_call_id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tool_calls: Vec<Value>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub tool_result_is_error: bool,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CheckpointView {
    pub id: String,
    pub conversation_id: String,
    pub session_id: String,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub updated_at: OffsetDateTime,
}

#[derive(Clone, Debug, Default)]
pub struct Binding {
    pub conversation_id: String,
    pub session_id: String,
}

#[derive(Debug, thiserror::Error)]
pub enum CheckpointError {
    #[error("transport checkpoint identity is required")]
    Identity,
    #[error("transport checkpoint key is required")]
    KeyRequired,
    #[error("transport checkpoint response cursor is unknown")]
    UnknownCursor,
    #[error("transport checkpoint match is ambiguous")]
    Ambiguous,
    #[error("transport checkpoint already has an in-flight turn")]
    Busy,
    #[error("transport checkpoint capacity reached")]
    Capacity,
    #[error("transport checkpoint history limit reached")]
    HistoryLimit,
    #[error("transport checkpoint turn is stale")]
    Stale,
    #[error("transport checkpoint conversation identity changed")]
    ConversationDrift,
    #[error("transport checkpoint persistence failed: {0}")]
    Persistence(String),
}

impl From<GatewayError> for CheckpointError {
    fn from(error: GatewayError) -> Self {
        Self::Persistence(error.to_string())
    }
}

#[derive(Debug)]
pub(crate) enum ClearThenError<E> {
    Clear,
    Change(E),
    Restore { change: E, restore: CheckpointError },
}

#[derive(Clone, Deserialize, Serialize)]
struct CheckpointFile {
    schema: String,
    records: Vec<Record>,
}

#[derive(Clone, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct Record {
    id: String,
    namespace: String,
    owner_digest: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    key_digest: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    conversation_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    session_id: String,
    accepted_count: usize,
    message_digests: Vec<String>,
    hash_chain: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    response_cursors: Vec<ResponseCursor>,
    #[serde(default)]
    tool_ledger: AgentLedger,
    #[serde(with = "time::serde::rfc3339")]
    created_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    updated_at: OffsetDateTime,
    revision: u64,
    #[serde(default, skip_serializing_if = "is_false")]
    in_flight: bool,
}

#[derive(Clone, Deserialize, Serialize)]
struct ResponseCursor {
    digest: String,
    revision: u64,
}

struct State {
    records: HashMap<String, Record>,
}

pub struct CheckpointStore {
    path: PathBuf,
    state: Mutex<State>,
}

pub struct CheckpointTurn {
    store: Arc<CheckpointStore>,
    record_id: String,
    revision: u64,
    rollback_record: Option<Record>,
    base_digests: Vec<String>,
    base_chain: Vec<String>,
    closed: bool,
    pub binding: Binding,
    pub outbound: Vec<CheckpointMessage>,
    pub rebound: bool,
    pub(crate) prior_ledger: AgentLedger,
}

impl CheckpointStore {
    pub fn open(path: impl Into<PathBuf>) -> Result<Arc<Self>, CheckpointError> {
        let path = path.into();
        let file = private_file::read_json::<CheckpointFile>(&path)?;
        let now = OffsetDateTime::now_utc();
        let records = match file {
            Some(file) if file.schema == SCHEMA => file
                .records
                .into_iter()
                .filter(|record| {
                    valid_record(record) && now - record.updated_at <= TTL && !record.in_flight
                })
                .map(|record| (record.id.clone(), record))
                .collect(),
            Some(file) => {
                return Err(CheckpointError::Persistence(format!(
                    "unsupported schema {:?}",
                    file.schema
                )));
            }
            None => HashMap::new(),
        };
        Ok(Arc::new(Self {
            path,
            state: Mutex::new(State { records }),
        }))
    }

    pub fn begin_full(
        self: &Arc<Self>,
        namespace: &str,
        owner: &str,
        key: &str,
        messages: &[CheckpointMessage],
        force_new: bool,
    ) -> Result<CheckpointTurn, CheckpointError> {
        if !valid_identity(namespace, 128) || !valid_identity(owner, 4_096) {
            return Err(CheckpointError::Identity);
        }
        if !key.is_empty() && !valid_identity(key, 4_096) {
            return Err(CheckpointError::Identity);
        }
        if messages.len() > MAX_MESSAGES {
            return Err(CheckpointError::HistoryLimit);
        }
        let digests = message_digests(messages)?;
        let chain = hash_chain(&digests);
        let owner_digest = digest(OWNER_DOMAIN, owner.as_bytes());
        let key_digest = if key.is_empty() {
            String::new()
        } else {
            digest(KEY_DOMAIN, key.as_bytes())
        };
        let now = OffsetDateTime::now_utc();
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        prune(&mut state, now);
        let snapshot = state.records.clone();
        let mut candidates = state
            .records
            .values()
            .filter(|record| {
                record.namespace == namespace
                    && record.owner_digest == owner_digest
                    && (key_digest.is_empty() || record.key_digest == key_digest)
                    && prefix(&record.message_digests, &digests)
            })
            .map(|record| record.id.clone())
            .collect::<Vec<_>>();
        candidates.sort_by_key(|id| {
            std::cmp::Reverse(
                state
                    .records
                    .get(id)
                    .map_or(0, |record| record.accepted_count),
            )
        });
        let selected = (!force_new).then(|| candidates.first().cloned()).flatten();
        let rebound = force_new
            || (selected.is_none()
                && state.records.values().any(|record| {
                    record.namespace == namespace && record.owner_digest == owner_digest
                }));
        if let Some(id) = selected {
            let record = state.records.get_mut(&id).unwrap();
            if record.in_flight {
                return Err(CheckpointError::Busy);
            }
            let rollback_record = record.clone();
            let accepted = record.accepted_count.min(messages.len());
            record.in_flight = true;
            record.revision += 1;
            record.updated_at = now;
            let turn = CheckpointTurn {
                store: Arc::clone(self),
                record_id: id,
                revision: record.revision,
                rollback_record: Some(rollback_record),
                base_digests: digests,
                base_chain: chain,
                closed: false,
                binding: Binding {
                    conversation_id: record.conversation_id.clone(),
                    session_id: record.session_id.clone(),
                },
                outbound: messages[accepted..].to_vec(),
                rebound,
                prior_ledger: record.tool_ledger.clone(),
            };
            if let Err(error) = save(&self.path, &state) {
                state.records = snapshot;
                return Err(error);
            }
            return Ok(turn);
        }

        if state.records.len() >= MAX_RECORDS {
            let evict = state
                .records
                .values()
                .filter(|record| !record.in_flight)
                .min_by_key(|record| record.updated_at)
                .map(|record| record.id.clone())
                .ok_or(CheckpointError::Capacity)?;
            state.records.remove(&evict);
        }
        if !key_digest.is_empty() {
            state.records.retain(|_, record| {
                !(record.namespace == namespace
                    && record.owner_digest == owner_digest
                    && record.key_digest == key_digest)
            });
        }
        let id = random_hex(16);
        let record = Record {
            id: id.clone(),
            namespace: namespace.to_owned(),
            owner_digest,
            key_digest,
            conversation_id: String::new(),
            session_id: String::new(),
            accepted_count: 0,
            message_digests: Vec::new(),
            hash_chain: Vec::new(),
            response_cursors: Vec::new(),
            tool_ledger: AgentLedger::default(),
            created_at: now,
            updated_at: now,
            revision: 1,
            in_flight: true,
        };
        state.records.insert(id.clone(), record);
        if let Err(error) = save(&self.path, &state) {
            state.records = snapshot;
            return Err(error);
        }
        Ok(CheckpointTurn {
            store: Arc::clone(self),
            record_id: id,
            revision: 1,
            rollback_record: None,
            base_digests: digests,
            base_chain: chain,
            closed: false,
            binding: Binding::default(),
            outbound: messages.to_vec(),
            rebound,
            prior_ledger: AgentLedger::default(),
        })
    }

    pub fn begin_delta(
        self: &Arc<Self>,
        namespace: &str,
        owner: &str,
        key: &str,
        messages: &[CheckpointMessage],
    ) -> Result<CheckpointTurn, CheckpointError> {
        if !valid_identity(namespace, 128) || !valid_identity(owner, 4_096) {
            return Err(CheckpointError::Identity);
        }
        if !valid_identity(key, 4_096) {
            return Err(CheckpointError::KeyRequired);
        }
        let owner_digest = digest(OWNER_DOMAIN, owner.as_bytes());
        let key_digest = digest(KEY_DOMAIN, key.as_bytes());
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        prune(&mut state, OffsetDateTime::now_utc());
        let matches = state
            .records
            .values()
            .filter(|record| {
                record.namespace == namespace
                    && record.owner_digest == owner_digest
                    && record.key_digest == key_digest
            })
            .map(|record| record.id.clone())
            .collect::<Vec<_>>();
        match matches.as_slice() {
            [] => {
                drop(state);
                self.begin_full(namespace, owner, key, messages, false)
            }
            [id] => begin_append(self, &mut state, id, messages),
            _ => Err(CheckpointError::Ambiguous),
        }
    }

    pub fn begin_response(
        self: &Arc<Self>,
        owner: &str,
        parent: &str,
        messages: &[CheckpointMessage],
    ) -> Result<CheckpointTurn, CheckpointError> {
        if !valid_identity(owner, 4_096) {
            return Err(CheckpointError::Identity);
        }
        if !valid_identity(parent, 4_096) {
            return Err(CheckpointError::UnknownCursor);
        }
        let owner_digest = digest(OWNER_DOMAIN, owner.as_bytes());
        let cursor_digest = digest(CURSOR_DOMAIN, parent.as_bytes());
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        prune(&mut state, OffsetDateTime::now_utc());
        let matches = state
            .records
            .values()
            .filter(|record| {
                record.owner_digest == owner_digest
                    && record.response_cursors.iter().any(|cursor| {
                        cursor.digest == cursor_digest
                            && (cursor.revision == record.revision
                                || (record.in_flight && cursor.revision + 1 == record.revision))
                    })
            })
            .map(|record| record.id.clone())
            .collect::<Vec<_>>();
        match matches.as_slice() {
            [] => Err(CheckpointError::UnknownCursor),
            [id] => begin_append(self, &mut state, id, messages),
            _ => Err(CheckpointError::Ambiguous),
        }
    }

    pub fn list(&self) -> Result<Vec<CheckpointView>, CheckpointError> {
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        prune(&mut state, OffsetDateTime::now_utc());
        let mut views = state
            .records
            .values()
            .filter(|record| !record.conversation_id.is_empty())
            .map(|record| CheckpointView {
                id: record.id.clone(),
                conversation_id: record.conversation_id.clone(),
                session_id: record.session_id.clone(),
                created_at: record.created_at,
                updated_at: record.updated_at,
            })
            .collect::<Vec<_>>();
        views.sort_by_key(|view| view.created_at);
        Ok(views)
    }

    pub fn delete(&self, id: &str) -> Result<bool, CheckpointError> {
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        let snapshot = state.records.clone();
        if state.records.remove(id).is_none() {
            return Ok(false);
        }
        if let Err(error) = save(&self.path, &state) {
            state.records = snapshot;
            return Err(error);
        }
        Ok(true)
    }

    pub fn clear(&self) -> Result<(), CheckpointError> {
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        let snapshot = state.records.clone();
        state.records.clear();
        if let Err(error) = save(&self.path, &state) {
            state.records = snapshot;
            return Err(error);
        }
        Ok(())
    }

    pub(crate) fn clear_then<T, E>(
        &self,
        change: impl FnOnce() -> Result<T, E>,
    ) -> Result<T, ClearThenError<E>> {
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        let snapshot = state.records.clone();
        state.records.clear();
        if save(&self.path, &state).is_err() {
            state.records = snapshot;
            return Err(ClearThenError::Clear);
        }
        match change() {
            Ok(value) => Ok(value),
            Err(change) => {
                state.records = snapshot;
                match save(&self.path, &state) {
                    Ok(()) => Err(ClearThenError::Change(change)),
                    Err(restore) => Err(ClearThenError::Restore { change, restore }),
                }
            }
        }
    }
}

impl CheckpointTurn {
    pub fn accept(
        mut self,
        binding: Binding,
        produced: &[CheckpointMessage],
    ) -> Result<(), CheckpointError> {
        self.accept_inner(binding, produced, "", None)
    }

    pub fn accept_response(
        mut self,
        binding: Binding,
        produced: &[CheckpointMessage],
        response_id: &str,
    ) -> Result<(), CheckpointError> {
        if !valid_identity(response_id, 4_096) {
            self.abort()?;
            return Err(CheckpointError::Identity);
        }
        self.accept_inner(binding, produced, response_id, None)
    }

    pub(crate) fn accept_with_ledger(
        mut self,
        binding: Binding,
        produced: &[CheckpointMessage],
        ledger: AgentLedger,
    ) -> Result<(), CheckpointError> {
        self.accept_inner(binding, produced, "", Some(ledger))
    }

    pub(crate) fn accept_response_with_ledger(
        mut self,
        binding: Binding,
        produced: &[CheckpointMessage],
        response_id: &str,
        ledger: AgentLedger,
    ) -> Result<(), CheckpointError> {
        if !valid_identity(response_id, 4_096) {
            self.abort()?;
            return Err(CheckpointError::Identity);
        }
        self.accept_inner(binding, produced, response_id, Some(ledger))
    }

    fn accept_inner(
        &mut self,
        binding: Binding,
        produced: &[CheckpointMessage],
        response_id: &str,
        ledger: Option<AgentLedger>,
    ) -> Result<(), CheckpointError> {
        let produced_digests = message_digests(produced)?;
        if self.base_digests.len() + produced_digests.len() > MAX_MESSAGES {
            self.abort()?;
            return Err(CheckpointError::HistoryLimit);
        }
        let mut state = self.store.state.lock().expect("checkpoint state poisoned");
        let snapshot = state.records.clone();
        let drift = state
            .records
            .get(&self.record_id)
            .filter(|record| record.in_flight && record.revision == self.revision)
            .ok_or(CheckpointError::Stale)
            .map(|record| {
                !record.conversation_id.is_empty()
                    && record.conversation_id != binding.conversation_id
            })?;
        if drift {
            state.records.remove(&self.record_id);
            save(&self.store.path, &state)?;
            self.closed = true;
            return Err(CheckpointError::ConversationDrift);
        }
        let record = state.records.get_mut(&self.record_id).unwrap();
        record.conversation_id = binding.conversation_id;
        record.session_id = binding.session_id;
        record.message_digests = self.base_digests.clone();
        record.message_digests.extend(produced_digests);
        record.hash_chain = self.base_chain.clone();
        for digest in record.message_digests.iter().skip(record.hash_chain.len()) {
            let previous = record
                .hash_chain
                .last()
                .map(String::as_str)
                .unwrap_or_default();
            record.hash_chain.push(digest_chain(previous, digest));
        }
        record.accepted_count = record.message_digests.len();
        if let Some(ledger) = ledger {
            record.tool_ledger = ledger;
        }
        if !response_id.is_empty() {
            let cursor = ResponseCursor {
                digest: digest(CURSOR_DOMAIN, response_id.as_bytes()),
                revision: record.revision + 1,
            };
            if !record.response_cursors.iter().any(|existing| {
                existing.digest == cursor.digest && existing.revision == cursor.revision
            }) {
                record.response_cursors.push(cursor);
                if record.response_cursors.len() > MAX_CURSORS {
                    record
                        .response_cursors
                        .drain(..record.response_cursors.len() - MAX_CURSORS);
                }
            }
        }
        record.in_flight = false;
        record.revision += 1;
        record.updated_at = OffsetDateTime::now_utc();
        if let Err(error) = save(&self.store.path, &state) {
            state.records = snapshot;
            return Err(error);
        }
        self.closed = true;
        Ok(())
    }

    pub fn abort(&mut self) -> Result<(), CheckpointError> {
        if self.closed {
            return Ok(());
        }
        let mut state = self.store.state.lock().expect("checkpoint state poisoned");
        let snapshot = state.records.clone();
        let stale = state
            .records
            .get(&self.record_id)
            .is_none_or(|record| !record.in_flight || record.revision != self.revision);
        if stale {
            self.closed = true;
            return Err(CheckpointError::Stale);
        }
        if let Some(record) = self.rollback_record.clone() {
            state.records.insert(self.record_id.clone(), record);
        } else {
            state.records.remove(&self.record_id);
        }
        if let Err(error) = save(&self.store.path, &state) {
            state.records = snapshot;
            return Err(error);
        }
        self.closed = true;
        Ok(())
    }
}

impl Drop for CheckpointTurn {
    fn drop(&mut self) {
        let _ = self.abort();
    }
}

fn save(path: &Path, state: &State) -> Result<(), CheckpointError> {
    let mut records = state.records.values().cloned().collect::<Vec<_>>();
    records.sort_by(|left, right| left.id.cmp(&right.id));
    private_file::write_json(
        path,
        &CheckpointFile {
            schema: SCHEMA.to_owned(),
            records,
        },
    )
    .map_err(Into::into)
}

fn begin_append(
    store: &Arc<CheckpointStore>,
    state: &mut State,
    record_id: &str,
    messages: &[CheckpointMessage],
) -> Result<CheckpointTurn, CheckpointError> {
    if messages.len() > MAX_MESSAGES {
        return Err(CheckpointError::HistoryLimit);
    }
    let delta_digests = message_digests(messages)?;
    let snapshot = state.records.clone();
    let record = state
        .records
        .get_mut(record_id)
        .ok_or(CheckpointError::UnknownCursor)?;
    if record.in_flight {
        return Err(CheckpointError::Busy);
    }
    if record.message_digests.len() + delta_digests.len() > MAX_MESSAGES {
        return Err(CheckpointError::HistoryLimit);
    }
    let rollback_record = record.clone();
    let mut digests = record.message_digests.clone();
    digests.extend(delta_digests);
    let chain = hash_chain(&digests);
    record.in_flight = true;
    record.revision += 1;
    record.updated_at = OffsetDateTime::now_utc();
    let turn = CheckpointTurn {
        store: Arc::clone(store),
        record_id: record_id.to_owned(),
        revision: record.revision,
        rollback_record: Some(rollback_record),
        base_digests: digests,
        base_chain: chain,
        closed: false,
        binding: Binding {
            conversation_id: record.conversation_id.clone(),
            session_id: record.session_id.clone(),
        },
        outbound: messages.to_vec(),
        rebound: false,
        prior_ledger: record.tool_ledger.clone(),
    };
    if let Err(error) = save(&store.path, state) {
        state.records = snapshot;
        return Err(error);
    }
    Ok(turn)
}

fn is_false(value: &bool) -> bool {
    !*value
}

fn prune(state: &mut State, now: OffsetDateTime) {
    state
        .records
        .retain(|_, record| now - record.updated_at <= TTL);
}

fn valid_record(record: &Record) -> bool {
    valid_identity(&record.id, 64)
        && valid_identity(&record.namespace, 128)
        && record.owner_digest.len() == 64
        && record.revision > 0
        && record.accepted_count == record.message_digests.len()
        && record.message_digests.len() == record.hash_chain.len()
        && record
            .response_cursors
            .iter()
            .all(|cursor| cursor.digest.len() == 64 && cursor.revision > 0)
        && record.tool_ledger.completed.len() + record.tool_ledger.pending.len() <= MAX_MESSAGES
        && record.updated_at >= record.created_at
}

fn valid_identity(value: &str, max: usize) -> bool {
    !value.trim().is_empty() && value.len() <= max && !value.chars().any(char::is_control)
}

fn message_digests(messages: &[CheckpointMessage]) -> Result<Vec<String>, CheckpointError> {
    messages
        .iter()
        .map(|message| {
            serde_json::to_vec(message)
                .map(|bytes| digest(MESSAGE_DOMAIN, &bytes))
                .map_err(|error| CheckpointError::Persistence(error.to_string()))
        })
        .collect()
}

fn hash_chain(digests: &[String]) -> Vec<String> {
    let mut chain = Vec::with_capacity(digests.len());
    for digest in digests {
        let previous = chain.last().map(String::as_str).unwrap_or_default();
        chain.push(digest_chain(previous, digest));
    }
    chain
}

fn digest_chain(previous: &str, digest_value: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(CHAIN_DOMAIN);
    hasher.update(previous.as_bytes());
    hasher.update(digest_value.as_bytes());
    hex(&hasher.finalize())
}

fn digest(domain: &[u8], value: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(domain);
    hasher.update(value);
    hex(&hasher.finalize())
}

fn prefix(prefix: &[String], full: &[String]) -> bool {
    prefix.len() <= full.len() && prefix.iter().zip(full).all(|(left, right)| left == right)
}

fn random_hex(size: usize) -> String {
    let mut bytes = vec![0_u8; size];
    rand::rng().fill(bytes.as_mut_slice());
    hex(&bytes)
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

    fn message(role: &str, text: &str) -> CheckpointMessage {
        CheckpointMessage {
            role: role.to_owned(),
            content: Value::String(text.to_owned()),
            name: String::new(),
            tool_call_id: String::new(),
            tool_calls: Vec::new(),
            tool_result_is_error: false,
        }
    }

    #[test]
    fn accepted_history_reuses_only_the_unsent_suffix() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let first = vec![message("user", "one")];
        let turn = store
            .begin_full("hermes", "owner", "session", &first, false)
            .unwrap();
        assert_eq!(turn.outbound.len(), 1);
        turn.accept(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "answer")],
        )
        .unwrap();
        let history = vec![
            message("user", "one"),
            message("assistant", "answer"),
            message("user", "two"),
        ];
        let turn = store
            .begin_full("hermes", "owner", "session", &history, false)
            .unwrap();
        assert_eq!(turn.binding.conversation_id, "conversation");
        assert_eq!(turn.outbound.len(), 1);
        assert_eq!(turn.outbound[0].content, "two");
    }

    #[test]
    fn accepted_history_reuse_preserves_prior_tool_evidence() {
        use crate::protocol::OpenAiMessage;

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let first = vec![message("user", "one")];
        let turn = store
            .begin_full("hermes", "owner", "session", &first, false)
            .unwrap();
        let ledger = crate::agent_ledger::build(&[
            OpenAiMessage::text("user", "run the check"),
            OpenAiMessage {
                role: "assistant".to_owned(),
                tool_calls: vec![serde_json::json!({
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "terminal", "arguments": "{}"}
                })],
                ..OpenAiMessage::default()
            },
            OpenAiMessage {
                role: "tool".to_owned(),
                content: Value::String(r#"{"output":"ok","exit_code":0}"#.to_owned()),
                tool_call_id: "call-1".to_owned(),
                ..OpenAiMessage::default()
            },
        ]);
        assert_eq!(ledger.completed.len(), 1);
        turn.accept_with_ledger(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "answer")],
            ledger,
        )
        .unwrap();

        let history = vec![
            message("user", "one"),
            message("assistant", "answer"),
            message("user", "two"),
        ];
        let turn = store
            .begin_full("hermes", "owner", "session", &history, false)
            .unwrap();

        assert_eq!(turn.outbound.len(), 1);
        assert_eq!(turn.outbound[0].content, "two");
        assert_eq!(turn.prior_ledger.completed.len(), 1);
        assert!(crate::agent_ledger::completion_evidence_allows(
            "The tool check completed successfully.",
            &turn.prior_ledger
        ));
    }

    #[test]
    fn persisted_checkpoint_contains_digests_not_private_text() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "private sentinel")],
                false,
            )
            .unwrap();
        turn.accept(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "secret answer")],
        )
        .unwrap();
        let persisted = std::fs::read_to_string(path).unwrap();
        assert!(!persisted.contains("private sentinel"));
        assert!(!persisted.contains("secret answer"));
    }

    #[test]
    fn clear_then_restores_checkpoint_when_the_change_fails() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "before mutation")],
                false,
            )
            .unwrap();
        turn.accept(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "answer")],
        )
        .unwrap();

        let result = store.clear_then(|| Err::<(), _>("forced change failure"));
        assert!(matches!(result, Err(ClearThenError::Change(_))));
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(
            CheckpointStore::open(&path).unwrap().list().unwrap().len(),
            1
        );

        store.clear_then(|| Ok::<_, &str>(())).unwrap();
        assert!(store.list().unwrap().is_empty());
    }
}
