use std::{
    collections::{HashMap, HashSet},
    fs::{File, OpenOptions},
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};

#[cfg(unix)]
use std::os::fd::AsRawFd;

use rand::Rng;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use time::{Duration, OffsetDateTime};

use crate::{agent_ledger::AgentLedger, error::GatewayError, private_file};

const SCHEMA: &str = "wp6-transport-checkpoints/rust-v2";
const LEGACY_SCHEMA: &str = "wp6-transport-checkpoints/rust-v1";
const TTL: Duration = Duration::hours(24);
const MAX_RECORDS: usize = 256;
const MAX_MESSAGES: usize = 4_096;
const MESSAGE_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/message/v1\0";
const CHAIN_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/chain/v1\0";
const OWNER_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/owner/v1\0";
const KEY_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/key/v1\0";
const CURSOR_DOMAIN: &[u8] = b"m365/wp6/transport-checkpoint/cursor/v1\0";
const MAX_CURSORS: usize = 64;
const INTEGRITY_KEY_BYTES: usize = 32;

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct CheckpointMessage {
    pub role: String,
    pub content: Value,
    #[serde(skip)]
    pub empty_recovery_synthetic: bool,
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

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct RecoveryView {
    pub id: String,
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
    #[error("transport checkpoint has an unresolved in-flight turn; reconcile before retry")]
    RecoveryRequired,
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
    RecoveryRequired,
    Change(E),
    Restore { change: E, restore: CheckpointError },
    Finalize(CheckpointError),
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
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    in_flight_message_digests: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    in_flight_upstream_started: Option<bool>,
    #[serde(default, skip_serializing_if = "is_false")]
    terminal_unknown: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    ledger_mac: String,
}

#[derive(Clone, Deserialize, Serialize)]
struct ResponseCursor {
    digest: String,
    revision: u64,
}

struct State {
    records: HashMap<String, Record>,
    recovery_leases: HashSet<String>,
}

struct FileLease {
    _file: File,
}

impl FileLease {
    fn blocking(path: &Path) -> Result<Self, CheckpointError> {
        Self::acquire(path, false)
    }

    fn try_record(path: &Path) -> Result<Self, CheckpointError> {
        Self::acquire(path, true)
    }

    fn acquire(path: &Path, nonblocking: bool) -> Result<Self, CheckpointError> {
        let parent = path.parent().ok_or_else(|| {
            CheckpointError::Persistence(format!(
                "checkpoint lock path has no parent: {}",
                path.display()
            ))
        })?;
        std::fs::create_dir_all(parent).map_err(|error| {
            CheckpointError::Persistence(format!("{}: {error}", parent.display()))
        })?;
        let mut options = OpenOptions::new();
        options.create(true).read(true).write(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let file = options.open(path).map_err(|error| {
            CheckpointError::Persistence(format!("{}: {error}", path.display()))
        })?;
        #[cfg(unix)]
        {
            const LOCK_EX: i32 = 2;
            const LOCK_NB: i32 = 4;
            let flags = LOCK_EX | if nonblocking { LOCK_NB } else { 0 };
            // SAFETY: flock only observes the valid file descriptor owned by
            // this FileLease and does not outlive the descriptor.
            let result = unsafe { flock(file.as_raw_fd(), flags) };
            if result != 0 {
                let error = std::io::Error::last_os_error();
                if nonblocking && matches!(error.raw_os_error(), Some(11 | 35)) {
                    return Err(CheckpointError::RecoveryRequired);
                }
                return Err(CheckpointError::Persistence(format!(
                    "{}: {error}",
                    path.display()
                )));
            }
        }
        #[cfg(not(unix))]
        if nonblocking {
            return Err(CheckpointError::Persistence(
                "cross-process checkpoint recovery requires a Unix file lock".to_owned(),
            ));
        }
        Ok(Self { _file: file })
    }
}

#[cfg(unix)]
unsafe extern "C" {
    fn flock(fd: i32, operation: i32) -> i32;
}

pub struct CheckpointStore {
    path: PathBuf,
    integrity_key: String,
    state: Mutex<State>,
    #[cfg(test)]
    fail_next_clear_then_restore: std::sync::atomic::AtomicBool,
}

pub struct CheckpointTurn {
    store: Arc<CheckpointStore>,
    record_id: String,
    revision: u64,
    rollback_record: Option<Record>,
    base_digests: Vec<String>,
    base_chain: Vec<String>,
    closed: bool,
    upstream_started: bool,
    recovery_lease: bool,
    _record_lease: Option<FileLease>,
    pub binding: Binding,
    pub outbound: Vec<CheckpointMessage>,
    pub rebound: bool,
    pub(crate) prior_ledger: AgentLedger,
}

impl CheckpointStore {
    fn save(&self, state: &State) -> Result<(), CheckpointError> {
        save_file(&self.path, &self.integrity_key, state)
    }

    pub fn open(path: impl Into<PathBuf>) -> Result<Arc<Self>, CheckpointError> {
        let path = path.into();
        let _lock = FileLease::blocking(&global_lock_path(&path))?;
        let integrity_key = load_or_create_integrity_key(&path)?;
        let (records, migrated_legacy) = load_records(&path, &integrity_key, true)?;
        let state = State {
            records,
            recovery_leases: HashSet::new(),
        };
        if migrated_legacy {
            save_file(&path, &integrity_key, &state)?;
        }
        Ok(Arc::new(Self {
            path,
            integrity_key,
            state: Mutex::new(state),
            #[cfg(test)]
            fail_next_clear_then_restore: std::sync::atomic::AtomicBool::new(false),
        }))
    }

    #[cfg(test)]
    pub(crate) fn fail_next_clear_then_restore_for_test(&self) {
        self.fail_next_clear_then_restore
            .store(true, std::sync::atomic::Ordering::Release);
    }

    pub fn begin_full(
        self: &Arc<Self>,
        namespace: &str,
        owner: &str,
        key: &str,
        messages: &[CheckpointMessage],
        force_new: bool,
    ) -> Result<CheckpointTurn, CheckpointError> {
        self.begin_full_inner(namespace, owner, key, messages, force_new, false)
    }

    fn begin_full_inner(
        self: &Arc<Self>,
        namespace: &str,
        owner: &str,
        key: &str,
        messages: &[CheckpointMessage],
        force_new: bool,
        allow_inflight_recovery: bool,
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
        if allow_inflight_recovery {
            return self.begin_full_recovery_inner(
                namespace,
                messages,
                force_new,
                digests,
                chain,
                owner_digest,
                key_digest,
            );
        }
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, now);
        if migrated {
            self.save(&state)?;
        }
        if state.records.values().any(|record| {
            record.namespace == namespace
                && record.owner_digest == owner_digest
                && record.key_digest == key_digest
                && (record.terminal_unknown || record.in_flight)
        }) {
            return Err(CheckpointError::RecoveryRequired);
        }
        let snapshot = state.records.clone();
        let mut candidates = state
            .records
            .values()
            .filter(|record| {
                record.namespace == namespace
                    && record.owner_digest == owner_digest
                    && record.key_digest == key_digest
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
            let rollback_record = record.clone();
            let accepted = record.accepted_count;
            record.in_flight = true;
            record.in_flight_message_digests = digests.clone();
            record.in_flight_upstream_started = Some(false);
            record.revision += 1;
            record.updated_at = now;
            let turn = CheckpointTurn {
                store: Arc::clone(self),
                record_id: id.clone(),
                revision: record.revision,
                rollback_record: Some(rollback_record),
                base_digests: digests,
                base_chain: chain,
                closed: false,
                upstream_started: false,
                recovery_lease: false,
                _record_lease: None,
                binding: Binding {
                    conversation_id: record.conversation_id.clone(),
                    session_id: record.session_id.clone(),
                },
                outbound: outbound_after_accepted(messages, accepted),
                rebound,
                prior_ledger: record.tool_ledger.clone(),
            };
            if let Err(error) = self.save(&state) {
                state.records = snapshot.clone();
                return Err(error);
            }
            return Ok(turn);
        }

        if !key_digest.is_empty()
            && !force_new
            && state.records.values().any(|record| {
                record.namespace == namespace
                    && record.owner_digest == owner_digest
                    && record.key_digest == key_digest
                    && record.accepted_count > 0
            })
        {
            return Err(CheckpointError::ConversationDrift);
        }
        if state.records.len() >= MAX_RECORDS {
            let evict = state
                .records
                .values()
                .filter(|record| !record.in_flight && !record.terminal_unknown)
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
            in_flight_message_digests: digests.clone(),
            in_flight_upstream_started: Some(false),
            terminal_unknown: false,
            ledger_mac: String::new(),
        };
        state.records.insert(id.clone(), record);
        if let Err(error) = self.save(&state) {
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
            upstream_started: false,
            recovery_lease: false,
            _record_lease: None,
            binding: Binding::default(),
            outbound: messages.to_vec(),
            rebound,
            prior_ledger: AgentLedger::default(),
        })
    }

    #[allow(clippy::too_many_arguments)]
    fn begin_full_recovery_inner(
        self: &Arc<Self>,
        namespace: &str,
        messages: &[CheckpointMessage],
        force_new: bool,
        digests: Vec<String>,
        chain: Vec<String>,
        owner_digest: String,
        key_digest: String,
    ) -> Result<CheckpointTurn, CheckpointError> {
        if force_new {
            return Err(CheckpointError::RecoveryRequired);
        }

        let id = {
            let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
            let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
            let mut state = self.state.lock().expect("checkpoint state poisoned");
            state.records = records;
            prune(&mut state, OffsetDateTime::now_utc());
            if migrated {
                save_file(&self.path, &self.integrity_key, &state)?;
            }
            let mut candidates = state
                .records
                .values()
                .filter(|record| {
                    record.namespace == namespace
                        && record.owner_digest == owner_digest
                        && record.key_digest == key_digest
                        && record.in_flight
                        && prefix(&record.message_digests, &digests)
                })
                .map(|record| record.id.clone())
                .collect::<Vec<_>>();
            candidates.sort_by_key(|candidate| {
                std::cmp::Reverse(
                    state
                        .records
                        .get(candidate)
                        .map_or(0, |record| record.accepted_count),
                )
            });
            match candidates.as_slice() {
                [id] => id.clone(),
                [] => return Err(CheckpointError::RecoveryRequired),
                _ => return Err(CheckpointError::Ambiguous),
            }
        };

        let record_lease = FileLease::try_record(&record_lock_path(&self.path, &id))?;
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            save_file(&self.path, &self.integrity_key, &state)?;
        }
        let record = state
            .records
            .get(&id)
            .ok_or(CheckpointError::RecoveryRequired)?;
        if !record.in_flight {
            return Err(CheckpointError::RecoveryRequired);
        }
        if record.in_flight_message_digests != digests {
            return Err(CheckpointError::ConversationDrift);
        }
        if record.in_flight_upstream_started != Some(true) {
            return Err(CheckpointError::RecoveryRequired);
        }
        if state.recovery_leases.contains(&id) {
            return Err(CheckpointError::RecoveryRequired);
        }
        state.recovery_leases.insert(id.clone());
        let record = state.records.get_mut(&id).expect("record was checked");
        let rollback_record = record.clone();
        let accepted = record.accepted_count;
        record.revision += 1;
        record.updated_at = OffsetDateTime::now_utc();
        let turn = CheckpointTurn {
            store: Arc::clone(self),
            record_id: id.clone(),
            revision: record.revision,
            rollback_record: Some(rollback_record),
            base_digests: digests,
            base_chain: chain,
            closed: false,
            upstream_started: false,
            recovery_lease: true,
            _record_lease: Some(record_lease),
            binding: Binding {
                conversation_id: record.conversation_id.clone(),
                session_id: record.session_id.clone(),
            },
            outbound: outbound_after_accepted(messages, accepted),
            rebound: false,
            prior_ledger: record.tool_ledger.clone(),
        };
        if let Err(error) = save_file(&self.path, &self.integrity_key, &state) {
            state.recovery_leases.remove(&id);
            return Err(error);
        }
        Ok(turn)
    }

    pub(crate) fn begin_full_recovery(
        self: &Arc<Self>,
        namespace: &str,
        owner: &str,
        key: &str,
        messages: &[CheckpointMessage],
        force_new: bool,
    ) -> Result<CheckpointTurn, CheckpointError> {
        self.begin_full_inner(namespace, owner, key, messages, force_new, true)
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
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            self.save(&state)?;
        }
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
                drop(_global_lease);
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
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            self.save(&state)?;
        }
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
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            self.save(&state)?;
        }
        let mut views = state
            .records
            .values()
            .filter(|record| !record.terminal_unknown && !record.conversation_id.is_empty())
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

    pub(crate) fn recovery_views(&self) -> Result<Vec<RecoveryView>, CheckpointError> {
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            save_file(&self.path, &self.integrity_key, &state)?;
        }
        let mut views = state
            .records
            .values()
            .filter(|record| record.in_flight && record.in_flight_upstream_started == Some(true))
            .map(|record| RecoveryView {
                id: record.id.clone(),
                updated_at: record.updated_at,
            })
            .collect::<Vec<_>>();
        views.sort_by(|left, right| left.id.cmp(&right.id));
        Ok(views)
    }

    pub(crate) fn reconcile_unknown(&self, id: &str) -> Result<bool, CheckpointError> {
        if !valid_record_id(id) {
            return Err(CheckpointError::Identity);
        }
        {
            let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
            let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
            let mut state = self.state.lock().expect("checkpoint state poisoned");
            state.records = records;
            prune(&mut state, OffsetDateTime::now_utc());
            if migrated {
                save_file(&self.path, &self.integrity_key, &state)?;
            }
            if state.recovery_leases.contains(id) {
                return Err(CheckpointError::RecoveryRequired);
            }
            let Some(record) = state.records.get(id) else {
                return Ok(false);
            };
            if !record.in_flight || record.in_flight_upstream_started != Some(true) {
                return Err(CheckpointError::RecoveryRequired);
            }
        }
        let _record_lease = FileLease::try_record(&record_lock_path(&self.path, id))?;
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        prune(&mut state, OffsetDateTime::now_utc());
        if migrated {
            save_file(&self.path, &self.integrity_key, &state)?;
        }
        if state.recovery_leases.contains(id) {
            return Err(CheckpointError::RecoveryRequired);
        }
        let Some(record) = state.records.get_mut(id) else {
            return Err(CheckpointError::Persistence(
                "checkpoint disappeared during reconciliation".to_owned(),
            ));
        };
        if !record.in_flight || record.in_flight_upstream_started != Some(true) {
            return Err(CheckpointError::RecoveryRequired);
        }
        record.in_flight = false;
        record.in_flight_message_digests.clear();
        record.in_flight_upstream_started = Some(false);
        record.terminal_unknown = true;
        record.revision += 1;
        record.updated_at = OffsetDateTime::now_utc();
        save_file(&self.path, &self.integrity_key, &state)?;
        state.recovery_leases.remove(id);
        Ok(true)
    }

    pub fn delete(&self, id: &str) -> Result<bool, CheckpointError> {
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated {
            self.save(&state)?;
        }
        if state.records.values().any(|record| record.in_flight)
            || state
                .records
                .get(id)
                .is_some_and(|record| record.terminal_unknown)
        {
            return Err(CheckpointError::RecoveryRequired);
        }
        let snapshot = state.records.clone();
        if state.records.remove(id).is_none() {
            return Ok(false);
        }
        if let Err(error) = self.save(&state) {
            state.records = snapshot;
            return Err(error);
        }
        Ok(true)
    }

    pub fn clear(&self) -> Result<(), CheckpointError> {
        let _global_lease = FileLease::blocking(&global_lock_path(&self.path))?;
        let (records, migrated) = load_records(&self.path, &self.integrity_key, false)?;
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated {
            self.save(&state)?;
        }
        if state.records.values().any(|record| record.in_flight) {
            return Err(CheckpointError::RecoveryRequired);
        }
        let snapshot = state.records.clone();
        state.records.retain(|_, record| record.terminal_unknown);
        if let Err(error) = self.save(&state) {
            state.records = snapshot;
            return Err(error);
        }
        Ok(())
    }

    pub(crate) fn clear_then<T, E>(
        &self,
        change: impl FnOnce() -> Result<T, E>,
    ) -> Result<T, ClearThenError<E>> {
        let _global_lease = match FileLease::blocking(&global_lock_path(&self.path)) {
            Ok(lease) => lease,
            Err(_) => return Err(ClearThenError::Clear),
        };
        let (records, migrated) = match load_records(&self.path, &self.integrity_key, false) {
            Ok(result) => result,
            Err(CheckpointError::RecoveryRequired) => {
                return Err(ClearThenError::RecoveryRequired);
            }
            Err(_) => return Err(ClearThenError::Clear),
        };
        let mut state = self.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated && self.save(&state).is_err() {
            return Err(ClearThenError::Clear);
        }
        if state.records.values().any(|record| record.in_flight) {
            return Err(ClearThenError::RecoveryRequired);
        }
        let snapshot = state.records.clone();
        let recovery_path = clear_then_recovery_path(&self.path);
        if save_file(&recovery_path, &self.integrity_key, &state).is_err() {
            return Err(ClearThenError::Clear);
        }
        state.records.retain(|_, record| record.terminal_unknown);
        if self.save(&state).is_err() {
            state.records = snapshot;
            return Err(ClearThenError::Clear);
        }
        match change() {
            Ok(value) => match remove_clear_then_recovery(&self.path) {
                Ok(()) => Ok(value),
                Err(error) => match self.retain_clear_then_recovery(&snapshot) {
                    Ok(()) => Err(ClearThenError::Finalize(error)),
                    Err(retain) => Err(ClearThenError::Finalize(CheckpointError::Persistence(
                        format!(
                            "clear_then cleanup failed: {error}; recovery retention failed: {retain}"
                        ),
                    ))),
                },
            },
            Err(change) => {
                state.records = snapshot.clone();
                #[cfg(test)]
                let restore_result = if self
                    .fail_next_clear_then_restore
                    .swap(false, std::sync::atomic::Ordering::AcqRel)
                {
                    Err(CheckpointError::Persistence(
                        "injected clear_then restore failure".to_owned(),
                    ))
                } else {
                    self.save(&state)
                };
                #[cfg(not(test))]
                let restore_result = self.save(&state);
                match restore_result {
                    Ok(()) => match remove_clear_then_recovery(&self.path) {
                        Ok(()) => Err(ClearThenError::Change(change)),
                        Err(restore) => {
                            let restore = match self.retain_clear_then_recovery(&snapshot) {
                                Ok(()) => restore,
                                Err(retain) => CheckpointError::Persistence(format!(
                                    "clear_then cleanup failed: {restore}; recovery retention failed: {retain}"
                                )),
                            };
                            Err(ClearThenError::Restore { change, restore })
                        }
                    },
                    Err(restore) => Err(ClearThenError::Restore { change, restore }),
                }
            }
        }
    }

    fn retain_clear_then_recovery(
        &self,
        records: &HashMap<String, Record>,
    ) -> Result<(), CheckpointError> {
        save_file(
            &clear_then_recovery_path(&self.path),
            &self.integrity_key,
            &State {
                records: records.clone(),
                recovery_leases: HashSet::new(),
            },
        )
    }
}

impl CheckpointTurn {
    pub(crate) fn mark_upstream_started(&mut self) -> Result<(), CheckpointError> {
        if self.closed {
            return Err(CheckpointError::Stale);
        }
        if self.upstream_started {
            return Ok(());
        }
        if self._record_lease.is_none() {
            self._record_lease = Some(FileLease::try_record(&record_lock_path(
                &self.store.path,
                &self.record_id,
            ))?);
        }
        let _global_lease = FileLease::blocking(&global_lock_path(&self.store.path))?;
        let (records, migrated) = load_records(&self.store.path, &self.store.integrity_key, false)?;
        let mut state = self.store.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated {
            self.store.save(&state)?;
        }
        let snapshot = state.records.clone();
        let record = state
            .records
            .get_mut(&self.record_id)
            .filter(|record| record.in_flight && record.revision == self.revision)
            .ok_or(CheckpointError::Stale)?;
        record.in_flight_upstream_started = Some(true);
        record.updated_at = OffsetDateTime::now_utc();
        if let Err(error) = self.store.save(&state) {
            state.records = snapshot;
            return Err(error);
        }
        state.recovery_leases.insert(self.record_id.clone());
        self.recovery_lease = true;
        self.upstream_started = true;
        Ok(())
    }

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
        let _global_lease = FileLease::blocking(&global_lock_path(&self.store.path))?;
        let (records, migrated) = load_records(&self.store.path, &self.store.integrity_key, false)?;
        let mut state = self.store.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated {
            self.store.save(&state)?;
        }
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
            if self.upstream_started {
                state.recovery_leases.remove(&self.record_id);
                self.recovery_lease = false;
                self.closed = true;
                return Err(CheckpointError::ConversationDrift);
            }
            state.records.remove(&self.record_id);
            if let Err(error) = self.store.save(&state) {
                state.records = snapshot;
                return Err(error);
            }
            state.recovery_leases.remove(&self.record_id);
            self.recovery_lease = false;
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
        record.in_flight_message_digests.clear();
        record.in_flight_upstream_started = Some(false);
        record.revision += 1;
        record.updated_at = OffsetDateTime::now_utc();
        if let Err(error) = self.store.save(&state) {
            state.records = snapshot;
            return Err(error);
        }
        if self.recovery_lease {
            state.recovery_leases.remove(&self.record_id);
            self.recovery_lease = false;
        }
        self.closed = true;
        Ok(())
    }

    pub fn abort(&mut self) -> Result<(), CheckpointError> {
        if self.closed {
            return Ok(());
        }
        if self.upstream_started {
            return Err(CheckpointError::RecoveryRequired);
        }
        let _global_lease = FileLease::blocking(&global_lock_path(&self.store.path))?;
        let (records, migrated) = load_records(&self.store.path, &self.store.integrity_key, false)?;
        let mut state = self.store.state.lock().expect("checkpoint state poisoned");
        state.records = records;
        if migrated {
            self.store.save(&state)?;
        }
        let snapshot = state.records.clone();
        let stale = state
            .records
            .get(&self.record_id)
            .is_none_or(|record| !record.in_flight || record.revision != self.revision);
        if stale {
            if self.recovery_lease {
                state.recovery_leases.remove(&self.record_id);
                self.recovery_lease = false;
            }
            self.closed = true;
            return Err(CheckpointError::Stale);
        }
        if let Some(record) = self.rollback_record.clone() {
            state.records.insert(self.record_id.clone(), record);
        } else {
            state.records.remove(&self.record_id);
        }
        if let Err(error) = self.store.save(&state) {
            state.records = snapshot;
            return Err(error);
        }
        if self.recovery_lease {
            state.recovery_leases.remove(&self.record_id);
            self.recovery_lease = false;
        }
        self.closed = true;
        Ok(())
    }
}

impl Drop for CheckpointTurn {
    fn drop(&mut self) {
        if !self.upstream_started {
            let _ = self.abort();
        } else if self.recovery_lease {
            let store = Arc::clone(&self.store);
            if let Ok(mut state) = store.state.lock() {
                state.recovery_leases.remove(&self.record_id);
                self.recovery_lease = false;
            }
        }
    }
}

fn global_lock_path(path: &Path) -> PathBuf {
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("checkpoint");
    path.with_file_name(format!(".{name}.lock"))
}

fn record_lock_path(path: &Path, id: &str) -> PathBuf {
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("checkpoint");
    path.with_file_name(format!(".{name}.recovery.{id}.lock"))
}

fn integrity_key_path(path: &Path) -> PathBuf {
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("checkpoint");
    path.with_file_name(format!(".{name}.key"))
}

fn clear_then_recovery_path(path: &Path) -> PathBuf {
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("checkpoint");
    path.with_file_name(format!(".{name}.clear-then-recovery"))
}

fn remove_clear_then_recovery(path: &Path) -> Result<(), CheckpointError> {
    let recovery_path = clear_then_recovery_path(path);
    match std::fs::remove_file(&recovery_path) {
        Ok(()) => {}
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => {
            return Err(CheckpointError::Persistence(format!(
                "{}: {error}",
                recovery_path.display()
            )));
        }
    }
    if let Some(parent) = recovery_path.parent() {
        File::open(parent)
            .and_then(|directory| directory.sync_all())
            .map_err(|error| {
                CheckpointError::Persistence(format!("{}: {error}", parent.display()))
            })?;
    }
    Ok(())
}

fn load_or_create_integrity_key(path: &Path) -> Result<String, CheckpointError> {
    let key_path = integrity_key_path(path);
    private_file::prepare_private_file(&key_path)?;
    if let Some(key) = private_file::read_json::<String>(&key_path)? {
        if key.len() == INTEGRITY_KEY_BYTES * 2 && key.bytes().all(|byte| byte.is_ascii_hexdigit())
        {
            return Ok(key);
        }
        return Err(CheckpointError::Persistence(
            "invalid checkpoint integrity key".to_owned(),
        ));
    }
    let key = random_hex(INTEGRITY_KEY_BYTES);
    private_file::write_json(&key_path, &key)?;
    Ok(key)
}

fn load_records(
    path: &Path,
    integrity_key: &str,
    recover_pre_upstream: bool,
) -> Result<(HashMap<String, Record>, bool), CheckpointError> {
    if clear_then_recovery_path(path).exists() {
        return Err(CheckpointError::RecoveryRequired);
    }
    let file = private_file::read_json::<CheckpointFile>(path)?;
    let now = OffsetDateTime::now_utc();
    let Some(file) = file else {
        return Ok((HashMap::new(), false));
    };
    let legacy_schema = file.schema == LEGACY_SCHEMA;
    if file.schema != SCHEMA && !legacy_schema {
        return Err(CheckpointError::Persistence(format!(
            "unsupported schema {:?}",
            file.schema
        )));
    }
    let mut records = HashMap::new();
    let mut migrated = legacy_schema;
    for mut record in file.records {
        if !valid_record(&record, legacy_schema) {
            return Err(CheckpointError::Persistence(
                "invalid checkpoint record".to_owned(),
            ));
        }
        if legacy_schema {
            // A pre-integrity file is readable only as a conservative
            // migration.  Existing result bytes cannot authorize an effect.
            record.tool_ledger.demote_unverified();
            if record.in_flight {
                // The old format did not record the pre-upstream phase, so
                // preserve it as uncertain rather than risk a replay.
                record.in_flight_upstream_started = Some(true);
            } else {
                record.in_flight_upstream_started = Some(false);
            }
            migrated = true;
        } else {
            let provided = record.ledger_mac.clone();
            record.ledger_mac.clear();
            let expected = record_ledger_mac(integrity_key, &record)?;
            if provided != expected {
                return Err(CheckpointError::Persistence(
                    "checkpoint integrity verification failed".to_owned(),
                ));
            }
            record.ledger_mac = provided;
        }
        if recover_pre_upstream
            && record.in_flight
            && record.in_flight_upstream_started == Some(false)
        {
            if record.accepted_count == 0
                && record.message_digests.is_empty()
                && record.conversation_id.is_empty()
                && record.session_id.is_empty()
            {
                migrated = true;
                continue;
            }
            record.in_flight = false;
            record.in_flight_message_digests.clear();
            migrated = true;
        }
        if record.in_flight_upstream_started.is_none() {
            record.in_flight_upstream_started = Some(record.in_flight);
            migrated = true;
        }
        if now - record.updated_at <= TTL || record.in_flight || record.terminal_unknown {
            records.insert(record.id.clone(), record);
        } else {
            migrated = true;
        }
    }
    Ok((records, migrated))
}

fn record_ledger_mac(integrity_key: &str, record: &Record) -> Result<String, CheckpointError> {
    let mut unsigned = record.clone();
    unsigned.ledger_mac.clear();
    let payload = serde_json::to_vec(&unsigned)
        .map_err(|error| CheckpointError::Persistence(error.to_string()))?;
    Ok(crate::hindsight::signature(integrity_key, &payload)
        .strip_prefix("sha256=")
        .unwrap_or_default()
        .to_owned())
}

fn save_file(path: &Path, integrity_key: &str, state: &State) -> Result<(), CheckpointError> {
    let mut records = state.records.values().cloned().collect::<Vec<_>>();
    records.sort_by(|left, right| left.id.cmp(&right.id));
    for record in &mut records {
        record.tool_ledger.normalize_unknown_results();
        record.ledger_mac = record_ledger_mac(integrity_key, record)?;
    }
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
    if record.terminal_unknown {
        return Err(CheckpointError::RecoveryRequired);
    }
    if record.in_flight {
        return Err(CheckpointError::RecoveryRequired);
    }
    if record.message_digests.len() + delta_digests.len() > MAX_MESSAGES {
        return Err(CheckpointError::HistoryLimit);
    }
    let rollback_record = record.clone();
    let mut digests = record.message_digests.clone();
    digests.extend(delta_digests);
    let chain = hash_chain(&digests);
    record.in_flight = true;
    record.in_flight_message_digests = digests.clone();
    record.in_flight_upstream_started = Some(false);
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
        upstream_started: false,
        recovery_lease: false,
        _record_lease: None,
        binding: Binding {
            conversation_id: record.conversation_id.clone(),
            session_id: record.session_id.clone(),
        },
        outbound: messages.to_vec(),
        rebound: false,
        prior_ledger: record.tool_ledger.clone(),
    };
    if let Err(error) = store.save(state) {
        state.records = snapshot;
        return Err(error);
    }
    Ok(turn)
}

fn is_false(value: &bool) -> bool {
    !*value
}

fn prune(state: &mut State, now: OffsetDateTime) {
    state.records.retain(|_, record| {
        record.in_flight || record.terminal_unknown || now - record.updated_at <= TTL
    });
}

fn valid_record(record: &Record, allow_legacy_statusless: bool) -> bool {
    valid_record_id(&record.id)
        && valid_identity(&record.namespace, 128)
        && record.owner_digest.len() == 64
        && record.revision > 0
        && record.accepted_count == record.message_digests.len()
        && record.message_digests.len() == record.hash_chain.len()
        && record
            .response_cursors
            .iter()
            .all(|cursor| cursor.digest.len() == 64 && cursor.revision > 0)
        && (record.in_flight || record.in_flight_message_digests.is_empty())
        && record
            .in_flight_message_digests
            .iter()
            .all(|value| value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit()))
        && (!record.terminal_unknown || !record.in_flight)
        && (record.in_flight || record.in_flight_upstream_started != Some(true))
        && (record.ledger_mac.is_empty() || is_digest(&record.ledger_mac))
        && record.tool_ledger.completed.len() + record.tool_ledger.pending.len() <= MAX_MESSAGES
        && if allow_legacy_statusless {
            record.tool_ledger.is_valid_persisted_legacy()
        } else {
            record.tool_ledger.is_valid_persisted()
        }
        && record.updated_at >= record.created_at
}

fn valid_record_id(value: &str) -> bool {
    !value.is_empty() && value.len() <= 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn is_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn valid_identity(value: &str, max: usize) -> bool {
    !value.trim().is_empty() && value.len() <= max && !value.chars().any(char::is_control)
}

fn message_digests(messages: &[CheckpointMessage]) -> Result<Vec<String>, CheckpointError> {
    messages
        .iter()
        .filter(|message| !message.empty_recovery_synthetic)
        .map(|message| {
            serde_json::to_vec(&canonical_checkpoint_message(message))
                .map(|bytes| digest(MESSAGE_DOMAIN, &bytes))
                .map_err(|error| CheckpointError::Persistence(error.to_string()))
        })
        .collect()
}

fn canonical_checkpoint_message(message: &CheckpointMessage) -> CheckpointMessage {
    let mut canonical = message.clone();
    if canonical.role == "assistant"
        && !canonical.tool_calls.is_empty()
        && canonical.content.as_str() == Some("")
    {
        canonical.content = Value::Null;
    }
    canonical.content = canonical_json(&canonical.content);
    canonical.tool_calls = canonical.tool_calls.iter().map(canonical_json).collect();
    canonical
}

fn canonical_json(value: &Value) -> Value {
    match value {
        Value::Array(values) => Value::Array(values.iter().map(canonical_json).collect()),
        Value::Object(object) => {
            let mut entries = object.iter().collect::<Vec<_>>();
            entries.sort_unstable_by(|left, right| left.0.cmp(right.0));
            let mut canonical = serde_json::Map::new();
            for (key, value) in entries {
                canonical.insert(key.clone(), canonical_json(value));
            }
            Value::Object(canonical)
        }
        _ => value.clone(),
    }
}

fn outbound_after_accepted(
    messages: &[CheckpointMessage],
    accepted_durable_count: usize,
) -> Vec<CheckpointMessage> {
    if accepted_durable_count == 0 {
        return messages.to_vec();
    }
    let mut durable_seen = 0;
    for (index, message) in messages.iter().enumerate() {
        if message.empty_recovery_synthetic {
            continue;
        }
        durable_seen += 1;
        if durable_seen == accepted_durable_count {
            return messages[index + 1..].to_vec();
        }
    }
    Vec::new()
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
    use serde_json::json;

    use super::*;

    fn message(role: &str, text: &str) -> CheckpointMessage {
        CheckpointMessage {
            role: role.to_owned(),
            content: Value::String(text.to_owned()),
            empty_recovery_synthetic: false,
            name: String::new(),
            tool_call_id: String::new(),
            tool_calls: Vec::new(),
            tool_result_is_error: false,
        }
    }

    fn synthetic_message(role: &str, text: &str) -> CheckpointMessage {
        let mut message = message(role, text);
        message.empty_recovery_synthetic = true;
        message
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
                    "function": {"name": "terminal", "arguments": "{\"command\":\"verify service-a\"}"}
                })],
                ..OpenAiMessage::default()
            },
            OpenAiMessage {
                role: "tool".to_owned(),
                content: Value::String(
                    r#"{"output":"ok","exit_code":0,"status":"completed"}"#.to_owned(),
                ),
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
        let ledger = serde_json::to_value(&turn.prior_ledger).unwrap();
        assert_eq!(ledger["completed"][0]["result_status"], "success");
    }

    #[test]
    fn reopening_a_pre_upstream_full_turn_can_be_retried_safely() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        std::mem::forget(turn);
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        let mut retry = reopened
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        retry.abort().unwrap();
        assert!(!std::fs::read_to_string(&path).unwrap().contains("inFlight"));
    }

    #[test]
    fn reopening_a_pre_upstream_append_turn_can_be_retried_safely() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
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
        let turn = store
            .begin_delta("hermes", "owner", "session", &[message("user", "two")])
            .unwrap();
        std::mem::forget(turn);
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        let mut retry = reopened
            .begin_delta("hermes", "owner", "session", &[message("user", "two")])
            .unwrap();
        retry.abort().unwrap();
        let persisted: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        assert!(
            !persisted["records"][0]["inFlight"]
                .as_bool()
                .unwrap_or(false)
        );
    }

    #[test]
    fn recovery_requires_a_durable_inflight_record() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();

        assert!(matches!(
            store.begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert!(!path.exists());
    }

    #[test]
    fn recovery_cannot_promote_an_accepted_checkpoint() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let before = std::fs::read(&path).unwrap();

        assert!(matches!(
            store.begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert_eq!(std::fs::read(&path).unwrap(), before);
    }

    #[test]
    fn terminal_unknown_cannot_be_bypassed_by_force_new_for_same_key() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);

        let recovery_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&recovery_id).unwrap());
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                true,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn empty_key_never_wildcards_a_keyed_uncertain_record() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let messages = [message("user", "one")];
        let mut turn = store
            .begin_full("hermes", "owner", "keyed-session", &messages, false)
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);

        let keyless = store
            .begin_full("hermes", "owner", "", &messages, false)
            .expect("keyless execution must not inherit a keyed in-flight fence");
        drop(keyless);
        assert!(matches!(
            store.begin_full_recovery("hermes", "owner", "", &messages, false),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert!(matches!(
            store.begin_full("hermes", "owner", "keyed-session", &messages, false),
            Err(CheckpointError::RecoveryRequired)
        ));

        let recovery_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&recovery_id).unwrap());
        let keyless = store
            .begin_full("hermes", "owner", "", &messages, false)
            .expect("keyless execution must not inherit a keyed tombstone fence");
        drop(keyless);
        assert!(matches!(
            store.begin_full("hermes", "owner", "keyed-session", &messages, false),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn unknown_reconciliation_id_does_not_leave_a_record_lock_file() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let id = "a".repeat(32);
        let lock_path = record_lock_path(&path, &id);

        assert!(!lock_path.exists());
        assert!(!store.reconcile_unknown(&id).unwrap());
        assert!(
            !lock_path.exists(),
            "unknown reconciliation IDs must not create permanent lock files"
        );
    }

    #[test]
    fn terminal_unknown_tombstone_is_hidden_but_still_fences_delete_and_replay() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one"), message("assistant", "answer")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);
        let recovery_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&recovery_id).unwrap());

        assert!(store.list().unwrap().is_empty());
        assert!(store.recovery_views().unwrap().is_empty());
        assert!(matches!(
            store.delete(&recovery_id),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn clear_removes_ordinary_checkpoints_but_retains_terminal_unknown_tombstones() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();

        let mut unknown = store
            .begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            )
            .unwrap();
        unknown.mark_upstream_started().unwrap();
        drop(unknown);
        let tombstone_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&tombstone_id).unwrap());

        store
            .begin_full(
                "hermes",
                "owner",
                "ordinary-session",
                &[message("user", "ordinary")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "ordinary-conversation".to_owned(),
                    session_id: "ordinary-upstream".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        assert_eq!(store.list().unwrap().len(), 1);

        store.clear().unwrap();
        assert!(store.list().unwrap().is_empty());
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));

        drop(store);
        let reopened = CheckpointStore::open(&path).unwrap();
        assert!(reopened.list().unwrap().is_empty());
        assert!(matches!(
            reopened.begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn clear_then_preserves_terminal_unknown_and_restores_ordinary_records_on_change_failure() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();

        let mut unknown = store
            .begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            )
            .unwrap();
        unknown.mark_upstream_started().unwrap();
        drop(unknown);
        let tombstone_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&tombstone_id).unwrap());

        store
            .begin_full(
                "hermes",
                "owner",
                "ordinary-session",
                &[message("user", "ordinary")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "ordinary-conversation".to_owned(),
                    session_id: "ordinary-upstream".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        assert_eq!(store.list().unwrap().len(), 1);

        let mut failed_change_called = false;
        let result = store.clear_then(|| {
            failed_change_called = true;
            Err::<(), _>("forced change failure")
        });
        assert!(failed_change_called);
        assert!(matches!(result, Err(ClearThenError::Change(_))));
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(
            CheckpointStore::open(&path).unwrap().list().unwrap().len(),
            1
        );
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));

        let mut successful_change_called = false;
        store
            .clear_then(|| {
                successful_change_called = true;
                Ok::<_, &str>(())
            })
            .unwrap();
        assert!(successful_change_called);
        assert!(store.list().unwrap().is_empty());
        assert!(matches!(
            CheckpointStore::open(&path).unwrap().begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn terminal_unknown_tombstone_survives_ttl_reopen() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);
        let recovery_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&recovery_id).unwrap());

        {
            let mut state = store.state.lock().expect("checkpoint state poisoned");
            let record = state.records.get_mut(&recovery_id).unwrap();
            record.updated_at = OffsetDateTime::now_utc() - TTL - Duration::seconds(1);
            record.created_at = record.updated_at - Duration::seconds(1);
            store.save(&state).unwrap();
        }
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        assert!(matches!(
            reopened.begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn terminal_unknown_tombstone_is_not_a_capacity_eviction_candidate() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);
        let recovery_id = store.recovery_views().unwrap()[0].id.clone();
        assert!(store.reconcile_unknown(&recovery_id).unwrap());

        for index in 0..(MAX_RECORDS - 1) {
            store
                .begin_full(
                    "hermes",
                    "owner",
                    &format!("stable-{index}"),
                    &[message("user", &format!("request-{index}"))],
                    false,
                )
                .unwrap()
                .accept(
                    Binding {
                        conversation_id: format!("conversation-{index}"),
                        session_id: format!("upstream-{index}"),
                    },
                    &[message("assistant", "answer")],
                )
                .unwrap();
        }

        let mut overflow = store
            .begin_full(
                "hermes",
                "owner",
                "overflow",
                &[message("user", "overflow")],
                false,
            )
            .unwrap();
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "unknown-session",
                &[message("user", "uncertain")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        overflow.abort().unwrap();
    }

    #[test]
    fn started_inflight_cannot_be_discarded_by_force_new_for_an_empty_key() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full("hermes", "owner", "", &[message("user", "original")], false)
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);

        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "",
                &[message("user", "retargeted")],
                true,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert_eq!(store.recovery_views().unwrap().len(), 1);
    }

    #[test]
    fn semantically_identical_tool_calls_reuse_checkpoint_across_json_key_order() {
        fn tool_call(keys_in_id_order: bool) -> Value {
            let mut function = serde_json::Map::new();
            function.insert(
                "arguments".to_owned(),
                Value::String("{\"path\":\"/tmp/probe\"}".to_owned()),
            );
            function.insert("name".to_owned(), Value::String("read_file".to_owned()));
            let mut call = serde_json::Map::new();
            if keys_in_id_order {
                call.insert("id".to_owned(), Value::String("call-1".to_owned()));
                call.insert("type".to_owned(), Value::String("function".to_owned()));
                call.insert("function".to_owned(), Value::Object(function));
            } else {
                call.insert("function".to_owned(), Value::Object(function));
                call.insert("id".to_owned(), Value::String("call-1".to_owned()));
                call.insert("type".to_owned(), Value::String("function".to_owned()));
            }
            Value::Object(call)
        }

        let root = tempfile::tempdir().unwrap();
        let store = CheckpointStore::open(root.path().join("checkpoints.json")).unwrap();
        let first_user = message("user", "read the probe");
        let first_assistant = CheckpointMessage {
            role: "assistant".to_owned(),
            content: Value::Null,
            empty_recovery_synthetic: false,
            name: String::new(),
            tool_call_id: String::new(),
            tool_calls: vec![tool_call(true)],
            tool_result_is_error: false,
        };
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                std::slice::from_ref(&first_user),
                false,
            )
            .unwrap()
            .accept(Binding::default(), std::slice::from_ref(&first_assistant))
            .unwrap();

        let second_assistant = CheckpointMessage {
            content: Value::String(String::new()),
            tool_calls: vec![tool_call(false)],
            ..first_assistant
        };
        let continuation = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    first_user,
                    second_assistant,
                    CheckpointMessage {
                        role: "tool".to_owned(),
                        content: Value::String("probe file".to_owned()),
                        empty_recovery_synthetic: false,
                        name: String::new(),
                        tool_call_id: "call-1".to_owned(),
                        tool_calls: Vec::new(),
                        tool_result_is_error: false,
                    },
                ],
                false,
            )
            .unwrap();
        assert_eq!(continuation.outbound.len(), 1);
        assert_eq!(continuation.outbound[0].role, "tool");
    }

    #[test]
    fn reconciliation_id_is_hex_and_cannot_escape_the_lock_directory() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();

        assert!(matches!(
            store.reconcile_unknown("../outside"),
            Err(CheckpointError::Identity)
        ));
        assert!(!root.path().join("outside.lock").exists());
    }

    #[test]
    fn upstream_started_turn_is_retained_for_reconciliation_when_dropped() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        assert!(matches!(
            reopened.begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn binding_drift_after_upstream_started_retains_recovery_state() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "one"),
                    message("assistant", "answer"),
                    message("user", "two"),
                ],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();

        let result = turn.accept(
            Binding {
                conversation_id: "different-conversation".to_owned(),
                session_id: "different-session".to_owned(),
            },
            &[message("assistant", "answer-two")],
        );
        assert!(matches!(result, Err(CheckpointError::ConversationDrift)));
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        assert!(matches!(
            reopened.begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "one"),
                    message("assistant", "answer"),
                    message("user", "two"),
                ],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn pre_upstream_binding_drift_save_failure_restores_memory_snapshot() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "one"),
                    message("assistant", "answer"),
                    message("user", "two"),
                ],
                false,
            )
            .unwrap();
        let record_id = turn.record_id.clone();
        std::fs::remove_file(&path).unwrap();
        std::fs::create_dir(&path).unwrap();

        let result = turn.accept(
            Binding {
                conversation_id: "different-conversation".to_owned(),
                session_id: "different-session".to_owned(),
            },
            &[message("assistant", "answer-two")],
        );
        assert!(matches!(result, Err(CheckpointError::Persistence(_))));
        let state = store.state.lock().unwrap();
        assert!(
            state
                .records
                .get(&record_id)
                .is_some_and(|record| record.in_flight)
        );
    }

    #[test]
    fn stale_inflight_records_are_not_pruned() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        {
            let mut state = store.state.lock().unwrap();
            let record = state.records.get_mut(&turn.record_id).unwrap();
            record.updated_at = OffsetDateTime::now_utc() - TTL - Duration::minutes(1);
            prune(&mut state, OffsetDateTime::now_utc());
            assert!(state.records.contains_key(&turn.record_id));
        }
        std::mem::forget(turn);
    }

    #[test]
    fn malformed_inflight_records_are_rejected_before_recovery() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        std::mem::forget(turn);
        drop(store);

        let mut file: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&path).unwrap()).unwrap();
        file["records"][0]["id"] = json!("");
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::Persistence(_))
        ));
    }
    #[test]
    fn divergent_inflight_full_prefix_never_deletes_recovery_state() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        let result = store.begin_full(
            "hermes",
            "owner",
            "session",
            &[message("user", "different")],
            false,
        );
        assert!(matches!(result, Err(CheckpointError::RecoveryRequired)));
        drop(turn);
        let result = store.begin_full(
            "hermes",
            "owner",
            "session",
            &[message("user", "different")],
            false,
        );
        assert!(matches!(result, Err(CheckpointError::RecoveryRequired)));
    }

    #[test]
    fn divergent_shorter_full_prefix_never_deletes_accepted_history() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let first = vec![message("user", "one")];
        store
            .begin_full("hermes", "owner", "session", &first, false)
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();

        let result = store.begin_full(
            "hermes",
            "owner",
            "session",
            &[message("user", "one")],
            false,
        );
        assert!(matches!(result, Err(CheckpointError::ConversationDrift)));

        let mut continuation = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "one"),
                    message("assistant", "answer"),
                    message("user", "two"),
                ],
                false,
            )
            .unwrap();
        assert_eq!(continuation.binding.conversation_id, "conversation");
        assert_eq!(continuation.outbound.len(), 1);
        assert_eq!(continuation.outbound[0].content, "two");
        continuation.abort().unwrap();
    }

    #[test]
    fn reopening_incomplete_ledger_evidence_fails_closed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        let ledger: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "call-1",
                "name": "deploy",
                "arguments_digest": "arguments",
                "result_length": 2,
                "result_digest": "result",
                "failed": false,
                "has_result": false,
                "result_status": "success",
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        turn.accept_with_ledger(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "answer")],
            ledger,
        )
        .unwrap();
        drop(store);

        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::Persistence(_))
        ));
    }

    #[test]
    fn reopening_contradictory_ledger_status_fails_closed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let ledger: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "call-1",
                "name": "deploy",
                "arguments_digest": "arguments",
                "result_length": 2,
                "result_digest": "result",
                "failed": true,
                "has_result": true,
                "result_status": "success",
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept_with_ledger(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
                ledger,
            )
            .unwrap();
        drop(store);

        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::Persistence(_))
        ));
    }

    #[test]
    fn reopening_invalid_ledger_evidence_fails_closed() {
        for (name, evidence) in [
            (
                "malformed-result-digest",
                json!({
                    "id": "call-1",
                    "name": "read_file",
                    "arguments_digest": "arguments",
                    "result_length": 2,
                    "result_digest": "result",
                    "failed": false,
                    "has_result": true,
                    "result_status": "success",
                }),
            ),
            (
                "success-without-result-bytes",
                json!({
                    "id": "call-1",
                    "name": "deploy",
                    "arguments_digest": "arguments",
                    "result_length": 0,
                    "result_digest": "result",
                    "failed": false,
                    "has_result": true,
                    "result_status": "success",
                }),
            ),
        ] {
            let root = tempfile::tempdir().unwrap();
            let path = root.path().join(format!("{name}.json"));
            let store = CheckpointStore::open(&path).unwrap();
            let ledger: AgentLedger = serde_json::from_value(json!({
                "completed": [evidence],
                "pending": [],
                "tool_rounds": 1,
                "repeated_call": false,
                "repeated_failure": false
            }))
            .unwrap();
            store
                .begin_full(
                    "hermes",
                    "owner",
                    "session",
                    &[message("user", "one")],
                    false,
                )
                .unwrap()
                .accept_with_ledger(
                    Binding {
                        conversation_id: "conversation".to_owned(),
                        session_id: "upstream-session".to_owned(),
                    },
                    &[message("assistant", "answer")],
                    ledger,
                )
                .unwrap();
            drop(store);

            assert!(matches!(
                CheckpointStore::open(&path),
                Err(CheckpointError::Persistence(_))
            ));
        }
    }

    #[test]
    fn persisted_tool_ledger_tampering_fails_integrity_validation() {
        use crate::protocol::OpenAiMessage;

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let ledger = crate::agent_ledger::build(&[
            OpenAiMessage::text("user", "run the check"),
            OpenAiMessage {
                role: "assistant".to_owned(),
                tool_calls: vec![json!({
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "terminal", "arguments": "{\"command\":\"verify service-a\"}"}
                })],
                ..OpenAiMessage::default()
            },
            OpenAiMessage {
                role: "tool".to_owned(),
                content: Value::String(
                    r#"{"output":"ok","exit_code":0,"status":"completed"}"#.to_owned(),
                ),
                tool_call_id: "call-1".to_owned(),
                ..OpenAiMessage::default()
            },
        ]);
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept_with_ledger(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
                ledger,
            )
            .unwrap();
        drop(store);

        let mut file: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        file["records"][0]["toolLedger"]["completed"][0]["result_length"] = json!(999);
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::Persistence(message))
                if message.contains("integrity verification failed")
        ));
    }

    #[test]
    fn current_checkpoint_without_a_ledger_mac_fails_before_recovery() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        drop(turn);
        drop(store);

        let mut file: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        file["records"][0]["ledgerMac"] = Value::String(String::new());
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::Persistence(message))
                if message.contains("integrity verification failed")
        ));
    }

    #[test]
    fn legacy_checkpoint_schema_migrates_to_the_fenced_schema() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        drop(store);

        let mut file: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        file["schema"] = Value::String(LEGACY_SCHEMA.to_owned());
        for field in [
            "inFlightMessageDigests",
            "inFlightUpstreamStarted",
            "terminalUnknown",
            "ledgerMac",
        ] {
            file["records"][0].as_object_mut().unwrap().remove(field);
        }
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        let _reopened = CheckpointStore::open(&path).unwrap();
        let migrated: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        assert_eq!(migrated["schema"], SCHEMA);
        assert!(migrated["records"][0]["ledgerMac"].is_string());
    }

    #[test]
    fn reopening_legacy_checkpoint_ledger_migrates_unknown_result() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let ledger: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id":"legacy-call",
                "name":"deploy",
                "arguments_digest":"0000000000000000000000000000000000000000000000000000000000000000",
                "result_length":2,
                "result_digest":"result",
                "failed":false,
                "has_result":true
            }],
            "pending":[],
            "tool_rounds":1,
            "repeated_call":false,
            "repeated_failure":false
        }))
        .unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept_with_ledger(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
                ledger,
            )
            .unwrap();
        drop(store);

        let mut file: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        file["schema"] = Value::String(LEGACY_SCHEMA.to_owned());
        file["records"][0]["ledgerMac"] = Value::String(String::new());
        let evidence = file["records"][0]["toolLedger"]["completed"][0]
            .as_object_mut()
            .unwrap();
        evidence.remove("result_status");
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        let reopened = CheckpointStore::open(&path).unwrap();
        let migrated: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        let migrated_evidence = &migrated["records"][0]["toolLedger"]["completed"][0];
        assert_eq!(migrated_evidence["result_length"], 0);
        assert_eq!(migrated_evidence["result_status"], "unknown");

        let mut continuation = reopened
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "one"),
                    message("assistant", "answer"),
                    message("user", "next"),
                ],
                false,
            )
            .unwrap();
        let ledger = serde_json::to_value(&continuation.prior_ledger).unwrap();
        assert_eq!(ledger["completed"][0]["result_status"], "unknown");
        continuation.abort().unwrap();
    }

    #[test]
    fn reopening_multi_entry_multi_record_legacy_ledger_migrates_every_result() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let legacy_ledger = |suffix: &str| {
            serde_json::from_value::<AgentLedger>(json!({
                "completed": [
                    {
                        "id":format!("{suffix}-one"),
                        "name":"deploy",
                        "arguments_digest":"0000000000000000000000000000000000000000000000000000000000000000",
                        "result_length":2,
                        "result_digest":"old-one",
                        "failed":false,
                        "has_result":true
                    },
                    {
                        "id":format!("{suffix}-two"),
                        "name":"verify",
                        "arguments_digest":"1111111111111111111111111111111111111111111111111111111111111111",
                        "result_length":3,
                        "result_digest":"old-two",
                        "failed":false,
                        "has_result":true
                    }
                ],
                "pending":[],
                "tool_rounds":1,
                "repeated_call":false,
                "repeated_failure":false
            }))
            .unwrap()
        };
        for (session, suffix) in [("session-a", "a"), ("session-b", "b")] {
            store
                .begin_full(
                    "hermes",
                    "owner",
                    session,
                    &[message("user", suffix)],
                    false,
                )
                .unwrap()
                .accept_with_ledger(
                    Binding {
                        conversation_id: format!("conversation-{suffix}"),
                        session_id: format!("upstream-{suffix}"),
                    },
                    &[message("assistant", "answer")],
                    legacy_ledger(suffix),
                )
                .unwrap();
        }
        drop(store);

        let mut file: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        file["schema"] = Value::String(LEGACY_SCHEMA.to_owned());
        for record in file["records"].as_array_mut().unwrap() {
            record["ledgerMac"] = Value::String(String::new());
            for evidence in record["toolLedger"]["completed"].as_array_mut().unwrap() {
                let evidence = evidence.as_object_mut().unwrap();
                evidence.remove("result_status");
            }
        }
        std::fs::write(&path, serde_json::to_vec(&file).unwrap()).unwrap();

        let reopened = CheckpointStore::open(&path).unwrap();
        drop(reopened);
        let reopened = CheckpointStore::open(&path).unwrap();
        let migrated: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        for record in migrated["records"].as_array().unwrap() {
            for evidence in record["toolLedger"]["completed"].as_array().unwrap() {
                assert_eq!(evidence["result_length"], 0);
                assert_eq!(evidence["result_status"], "unknown");
            }
        }
        assert_eq!(reopened.list().unwrap().len(), 2);
    }

    #[test]
    fn delete_refuses_when_another_checkpoint_is_in_flight() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session-a",
                &[message("user", "one")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation-a".to_owned(),
                    session_id: "upstream-session-a".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let stable_id = store.list().unwrap()[0].id.clone();
        let mut in_flight = store
            .begin_full(
                "hermes",
                "owner",
                "session-b",
                &[message("user", "two")],
                false,
            )
            .unwrap();
        in_flight.mark_upstream_started().unwrap();

        assert!(matches!(
            store.delete(&stable_id),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "session-b",
                &[message("user", "two")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        drop(in_flight);
        assert_eq!(store.list().unwrap().len(), 1);
    }

    #[test]
    fn matching_recovery_can_reopen_an_inflight_full_turn() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut first = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        first.mark_upstream_started().unwrap();
        drop(first);

        let mut recovery = store
            .begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            )
            .unwrap();
        assert_eq!(recovery.outbound.len(), 1);
        recovery.abort().unwrap();
        assert!(matches!(
            store.begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "one")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn matching_recovery_rejects_a_retargeted_inflight_suffix() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut first = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        first.mark_upstream_started().unwrap();
        drop(first);

        let persisted: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        assert_eq!(
            persisted["records"][0]["inFlightMessageDigests"]
                .as_array()
                .unwrap()
                .len(),
            1,
            "the unresolved non-synthetic transcript must be durable"
        );

        assert!(matches!(
            store.begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original"), message("user", "retargeted")],
                false,
            ),
            Err(CheckpointError::ConversationDrift)
        ));
    }

    #[test]
    fn matching_recovery_is_single_flight_per_checkpoint() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut first = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        first.mark_upstream_started().unwrap();
        drop(first);

        let recovery = store
            .begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        assert!(matches!(
            store.begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        drop(recovery);
        assert!(
            store
                .begin_full_recovery(
                    "hermes",
                    "owner",
                    "session",
                    &[message("user", "original")],
                    false,
                )
                .is_ok()
        );
    }

    #[test]
    fn separate_store_instances_share_the_os_recovery_lease() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut first = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        first.mark_upstream_started().unwrap();
        drop(first);
        drop(store);

        let first_store = CheckpointStore::open(&path).unwrap();
        let second_store = CheckpointStore::open(&path).unwrap();
        let recovery = first_store
            .begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        assert!(matches!(
            second_store.begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            ),
            Err(CheckpointError::RecoveryRequired)
        ));
        drop(recovery);
    }

    #[test]
    fn an_already_open_store_discovers_a_recovery_written_by_another_store() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let stale_store = CheckpointStore::open(&path).unwrap();
        let writer = CheckpointStore::open(&path).unwrap();
        let mut first = writer
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        first.mark_upstream_started().unwrap();
        drop(first);

        let mut recovery = stale_store
            .begin_full_recovery(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        recovery.abort().unwrap();
    }

    #[test]
    fn a_pre_upstream_reservation_is_rolled_back_on_reopen() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        std::mem::forget(turn);
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        assert!(reopened.recovery_views().unwrap().is_empty());
        let mut retry = reopened
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "original")],
                false,
            )
            .unwrap();
        retry.abort().unwrap();
    }

    #[test]
    fn synthetic_recovery_scaffolding_is_not_durable_checkpoint_identity() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let tool_call = CheckpointMessage {
            role: "assistant".to_owned(),
            content: Value::Null,
            empty_recovery_synthetic: false,
            name: String::new(),
            tool_call_id: String::new(),
            tool_calls: vec![serde_json::json!({
                "id": "call-1",
                "type": "function",
                "function": {"name": "inspect", "arguments": "{}"}
            })],
            tool_result_is_error: false,
        };
        let tool_result = CheckpointMessage {
            role: "tool".to_owned(),
            content: Value::String("ok".to_owned()),
            empty_recovery_synthetic: false,
            name: String::new(),
            tool_call_id: "call-1".to_owned(),
            tool_calls: Vec::new(),
            tool_result_is_error: false,
        };
        let recovery = vec![
            message("user", "inspect"),
            tool_call.clone(),
            tool_result.clone(),
            synthetic_message("assistant", "(empty)"),
            synthetic_message(
                "user",
                "You just executed tool calls but returned an empty response. Please process the tool results above and continue with the task.",
            ),
        ];
        let turn = store
            .begin_full("hermes", "owner", "session", &recovery, false)
            .unwrap();
        assert_eq!(turn.outbound.len(), recovery.len());
        assert!(turn.outbound[3].empty_recovery_synthetic);
        assert!(turn.outbound[4].empty_recovery_synthetic);
        turn.accept(
            Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[message("assistant", "answer")],
        )
        .unwrap();
        drop(store);

        let reopened = CheckpointStore::open(&path).unwrap();
        let normal_continuation = vec![
            message("user", "inspect"),
            tool_call,
            tool_result,
            message("assistant", "answer"),
            message("user", "continue"),
        ];
        let turn = reopened
            .begin_full("hermes", "owner", "session", &normal_continuation, false)
            .unwrap();

        assert!(
            !turn.rebound,
            "recovery scaffolding must not force a new checkpoint"
        );
        assert_eq!(turn.binding.conversation_id, "conversation");
        assert_eq!(turn.outbound.len(), 1);
        assert_eq!(turn.outbound[0].role, "user");
        assert_eq!(turn.outbound[0].content, "continue");
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
        let recovery_path = root.path().join(".checkpoints.json.clear-then-recovery");
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

        let mut recovery_was_durable_before_change = false;
        let result = store.clear_then(|| {
            recovery_was_durable_before_change = recovery_path.is_file();
            Err::<(), _>("forced change failure")
        });
        assert!(matches!(result, Err(ClearThenError::Change(_))));
        assert!(recovery_was_durable_before_change);
        assert!(!recovery_path.exists());
        assert_eq!(store.list().unwrap().len(), 1);
        assert_eq!(
            CheckpointStore::open(&path).unwrap().list().unwrap().len(),
            1
        );

        store.clear_then(|| Ok::<_, &str>(())).unwrap();
        assert!(store.list().unwrap().is_empty());
        assert!(!recovery_path.exists());
    }

    #[test]
    fn clear_then_retains_recovery_when_finalize_cleanup_fails() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let recovery_path = root.path().join(".checkpoints.json.clear-then-recovery");
        let store = CheckpointStore::open(&path).unwrap();

        let result = store.clear_then(|| {
            std::fs::remove_file(&recovery_path).unwrap();
            std::fs::create_dir(&recovery_path).unwrap();
            Ok::<_, &str>(())
        });

        assert!(matches!(result, Err(ClearThenError::Finalize(_))));
        assert!(recovery_path.is_dir());
        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn clear_then_restore_failure_leaves_durable_recovery_snapshot_and_fails_closed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let recovery_path = root.path().join(".checkpoints.json.clear-then-recovery");
        let store = CheckpointStore::open(&path).unwrap();
        store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "before mutation")],
                false,
            )
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();

        store.fail_next_clear_then_restore_for_test();
        let result = store.clear_then(|| Err::<(), _>("forced change failure"));

        let restore = match result {
            Err(ClearThenError::Restore { change, restore }) => {
                let _ = change;
                restore
            }
            other => panic!("expected typed restore failure, got {other:?}"),
        };
        assert!(
            restore
                .to_string()
                .contains("injected clear_then restore failure")
        );
        let recovery = private_file::read_json::<CheckpointFile>(&recovery_path)
            .unwrap()
            .expect("restore failure must retain a durable pre-change snapshot");
        assert_eq!(recovery.schema, SCHEMA);
        assert_eq!(recovery.records.len(), 1);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(&recovery_path)
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
            assert_eq!(
                std::fs::metadata(root.path()).unwrap().permissions().mode() & 0o777,
                0o700
            );
        }
        assert!(matches!(
            CheckpointStore::open(&path),
            Err(CheckpointError::RecoveryRequired)
        ));
    }

    #[test]
    fn destructive_checkpoint_operations_retain_upstream_started_turns() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let first = [message("user", "before mutation")];
        store
            .begin_full("hermes", "owner", "session", &first, false)
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "conversation".to_owned(),
                    session_id: "upstream-session".to_owned(),
                },
                &[message("assistant", "answer")],
            )
            .unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[
                    message("user", "before mutation"),
                    message("assistant", "answer"),
                ],
                false,
            )
            .unwrap();
        let id = turn.record_id.clone();
        turn.mark_upstream_started().unwrap();

        assert!(matches!(
            store.delete(&id),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert!(matches!(
            store.clear(),
            Err(CheckpointError::RecoveryRequired)
        ));
        assert_eq!(store.list().unwrap().len(), 1);
        assert!(std::fs::read_to_string(&path).unwrap().contains(&id));
    }

    #[test]
    fn clear_then_rejects_upstream_started_turn_without_running_change() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("checkpoints.json");
        let store = CheckpointStore::open(&path).unwrap();
        let mut turn = store
            .begin_full(
                "hermes",
                "owner",
                "session",
                &[message("user", "before mutation")],
                false,
            )
            .unwrap();
        turn.mark_upstream_started().unwrap();
        let mut called = false;

        let result = store.clear_then(|| {
            called = true;
            Ok::<_, &str>(())
        });

        assert!(matches!(result, Err(ClearThenError::RecoveryRequired)));
        assert!(!called);
        assert!(std::fs::read_to_string(&path).unwrap().contains("inFlight"));
    }
}
