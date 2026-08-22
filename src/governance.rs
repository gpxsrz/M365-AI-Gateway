use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    sync::Mutex,
};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use time::OffsetDateTime;

use crate::{error::GatewayError, private_file};

const SCHEMA: &str = "m365/agent-governance/rust-v1";
const BASE_POLICY_VERSION: &str = "agent-governance/base-v1";
const BASE_EVALUATOR_VERSION: &str = "agent-governance/evaluator-v1";

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum LifecycleState {
    Ready,
    Claimed,
    Running,
    Blocked,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum TransitionKind {
    Register,
    Claim,
    Start,
    Block,
    Resume,
    SupersedeBlocker,
    ForceResume,
    Correct,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum DecisionOutcome {
    Allow,
    Deny,
    Defer,
    RequireApproval,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CreateTask {
    pub task_id: String,
    pub run_id: String,
    pub actor: String,
    pub acceptance_contract_digest: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TransitionRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub requested_transition: TransitionKind,
    pub agent_id: String,
    pub actor: String,
    pub fencing_identity: Option<String>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CorrectionRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub supersedes_decision_id: String,
    pub actor: String,
    pub reason: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum EvidenceKind {
    DependencyState,
    DurableReceipt,
    ApprovalGrant,
    ArtifactVerification,
    ElapsedTime,
    Heartbeat,
    EventSequence,
    Comment,
    ArtifactTimestamp,
}

impl EvidenceKind {
    fn resume_relevant(self) -> bool {
        matches!(
            self,
            Self::DependencyState
                | Self::DurableReceipt
                | Self::ApprovalGrant
                | Self::ArtifactVerification
        )
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StructuredCause {
    pub cause_id: String,
    pub schema_version: u32,
    pub fields: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct EvidenceRequirement {
    pub kind: EvidenceKind,
    pub subject: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct EvidenceObservation {
    pub kind: EvidenceKind,
    pub subject: String,
    pub identity: String,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum BlockerState {
    Active,
    Released,
    Superseded,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BlockerRecord {
    pub blocker_id: String,
    pub task_id: String,
    pub run_id: String,
    pub generation: u64,
    pub kind: String,
    pub cause_id: String,
    pub cause_schema_version: u32,
    pub cause_hash: String,
    pub blocked_at_authority_revision: u64,
    pub required_resume_evidence: Vec<EvidenceRequirement>,
    pub evidence_baseline: Vec<EvidenceObservation>,
    pub state: BlockerState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub released_at_authority_revision: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub supersedes_blocker_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub superseded_by_blocker_id: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BlockTaskRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub blocker_kind: String,
    pub cause: StructuredCause,
    pub required_resume_evidence: Vec<EvidenceRequirement>,
    pub evidence_baseline: Vec<EvidenceObservation>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BlockTaskResult {
    pub decision: DecisionRecord,
    pub blocker: Option<BlockerRecord>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumeBlockerRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub blocker_id: String,
    pub expected_blocker_generation: u64,
    pub actor: String,
    pub evidence: Vec<EvidenceObservation>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ResumeStatus {
    BlockerUnchanged,
    Resumed,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumeBlockerResult {
    pub status: ResumeStatus,
    pub decision: DecisionRecord,
    pub blocker: Option<BlockerRecord>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SupersedeBlockerRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub blocker_id: String,
    pub expected_blocker_generation: u64,
    pub actor: String,
    pub blocker_kind: String,
    pub cause: StructuredCause,
    pub required_resume_evidence: Vec<EvidenceRequirement>,
    pub evidence_baseline: Vec<EvidenceObservation>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SupersedeBlockerResult {
    pub decision: DecisionRecord,
    pub prior_blocker: Option<BlockerRecord>,
    pub new_blocker: Option<BlockerRecord>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ForceResumeRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub blocker_id: String,
    pub expected_blocker_generation: u64,
    pub actor: String,
    pub reason: String,
    pub audit_reference: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskAuthority {
    pub task_id: String,
    pub active_run_id: String,
    pub lifecycle_state: LifecycleState,
    pub authority_revision: u64,
    #[serde(default)]
    pub acceptance_contract_digest: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_agent_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fencing_identity: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub active_blocker_id: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DecisionRecord {
    pub decision_id: String,
    pub task_id: String,
    pub run_id: String,
    pub agent_id: String,
    pub requested_transition: TransitionKind,
    pub outcome: DecisionOutcome,
    pub reason: String,
    pub authority_before: u64,
    pub authority_after: u64,
    pub policy_version: String,
    pub evaluator_version: String,
    pub evidence_refs: Vec<String>,
    pub actor: String,
    #[serde(with = "time::serde::rfc3339")]
    pub evaluated_at: OffsetDateTime,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub performed_at: Option<OffsetDateTime>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fencing_identity: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub supersedes_decision_id: Option<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum GovernanceError {
    #[error("governance identity is invalid: {0}")]
    InvalidIdentity(&'static str),
    #[error("governance task already exists: {0}")]
    TaskExists(String),
    #[error("governance storage failed: {0}")]
    Storage(String),
}

impl From<GatewayError> for GovernanceError {
    fn from(error: GatewayError) -> Self {
        Self::Storage(error.to_string())
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct StateFile {
    schema: String,
    tasks: BTreeMap<String, TaskAuthority>,
    #[serde(default)]
    blockers: BTreeMap<String, BlockerRecord>,
    decisions: Vec<DecisionRecord>,
    next_decision_seq: u64,
}

impl Default for StateFile {
    fn default() -> Self {
        Self {
            schema: SCHEMA.to_owned(),
            tasks: BTreeMap::new(),
            blockers: BTreeMap::new(),
            decisions: Vec::new(),
            next_decision_seq: 1,
        }
    }
}

pub struct GovernanceStore {
    path: PathBuf,
    state: Mutex<StateFile>,
}

impl GovernanceStore {
    pub fn open(path: impl Into<PathBuf>) -> Result<Self, GovernanceError> {
        let path = path.into();
        let state = match private_file::read_json::<StateFile>(&path)? {
            Some(state) if state.schema == SCHEMA => state,
            Some(state) => {
                return Err(GovernanceError::Storage(format!(
                    "unsupported governance schema {:?}",
                    state.schema
                )));
            }
            None => StateFile::default(),
        };
        Ok(Self {
            path,
            state: Mutex::new(state),
        })
    }

    pub fn create_task(&self, request: CreateTask) -> Result<TaskAuthority, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity(
            "acceptance_contract_digest",
            &request.acceptance_contract_digest,
        )?;

        let mut state = self.state.lock().expect("governance state poisoned");
        if state.tasks.contains_key(&request.task_id) {
            return Err(GovernanceError::TaskExists(request.task_id));
        }
        let snapshot = state.clone();
        let now = OffsetDateTime::now_utc();
        let authority = TaskAuthority {
            task_id: request.task_id.clone(),
            active_run_id: request.run_id.clone(),
            lifecycle_state: LifecycleState::Ready,
            authority_revision: 1,
            acceptance_contract_digest: request.acceptance_contract_digest,
            owner_agent_id: None,
            fencing_identity: None,
            active_blocker_id: None,
        };
        let decision_id = next_decision_id(&mut state);
        state
            .tasks
            .insert(request.task_id.clone(), authority.clone());
        state.decisions.push(DecisionRecord {
            decision_id,
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: String::new(),
            requested_transition: TransitionKind::Register,
            outcome: DecisionOutcome::Allow,
            reason: "registered".to_owned(),
            authority_before: 0,
            authority_after: 1,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: Vec::new(),
            actor: request.actor,
            evaluated_at: now,
            performed_at: Some(now),
            fencing_identity: None,
            supersedes_decision_id: None,
        });
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(authority)
    }

    pub fn authority(&self, task_id: &str) -> Result<Option<TaskAuthority>, GovernanceError> {
        validate_identity("task_id", task_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state.tasks.get(task_id).cloned())
    }

    pub fn decisions(&self, task_id: &str) -> Result<Vec<DecisionRecord>, GovernanceError> {
        validate_identity("task_id", task_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state
            .decisions
            .iter()
            .filter(|decision| decision.task_id == task_id)
            .cloned()
            .collect())
    }

    pub fn blocker(&self, task_id: &str) -> Result<Option<BlockerRecord>, GovernanceError> {
        validate_identity("task_id", task_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        let Some(blocker_id) = state
            .tasks
            .get(task_id)
            .and_then(|authority| authority.active_blocker_id.as_deref())
        else {
            return Ok(None);
        };
        Ok(state.blockers.get(blocker_id).cloned())
    }

    pub fn blockers(&self, task_id: &str) -> Result<Vec<BlockerRecord>, GovernanceError> {
        validate_identity("task_id", task_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        let mut blockers = state
            .blockers
            .values()
            .filter(|blocker| blocker.task_id == task_id)
            .cloned()
            .collect::<Vec<_>>();
        blockers.sort_by_key(|blocker| blocker.generation);
        Ok(blockers)
    }

    pub fn block_task(
        &self,
        request: BlockTaskRequest,
    ) -> Result<BlockTaskResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("agent_id", &request.agent_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_identity("blocker_kind", &request.blocker_kind)?;
        validate_structured_cause(&request.cause)?;
        validate_evidence_requirements(&request.required_resume_evidence)?;
        validate_evidence_observations(&request.evidence_baseline)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut blocker = None;
        let (outcome, reason) = match authority.as_ref() {
            None => (DecisionOutcome::Deny, "task_not_found"),
            Some(authority) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            Some(authority)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            Some(_) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            Some(authority) if authority.lifecycle_state != LifecycleState::Running => {
                (DecisionOutcome::Deny, "block_not_available")
            }
            Some(authority)
                if authority.owner_agent_id.as_deref() != Some(request.agent_id.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            Some(authority)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            Some(_) => {
                let blocker_id = new_blocker_id();
                let generation = next_blocker_generation(&state, &request.task_id);
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Blocked;
                authority.authority_revision += 1;
                authority.owner_agent_id = None;
                authority.fencing_identity = None;
                authority.active_blocker_id = Some(blocker_id.clone());
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                let created = BlockerRecord {
                    blocker_id: blocker_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    generation,
                    kind: request.blocker_kind.clone(),
                    cause_id: request.cause.cause_id.clone(),
                    cause_schema_version: request.cause.schema_version,
                    cause_hash: cause_hash(&request.blocker_kind, &request.cause),
                    blocked_at_authority_revision: authority_after,
                    required_resume_evidence: request.required_resume_evidence.clone(),
                    evidence_baseline: request.evidence_baseline.clone(),
                    state: BlockerState::Active,
                    released_at_authority_revision: None,
                    supersedes_blocker_id: None,
                    superseded_by_blocker_id: None,
                };
                state.blockers.insert(blocker_id, created.clone());
                blocker = Some(created);
                (DecisionOutcome::Allow, "blocked")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.agent_id,
            requested_transition: TransitionKind::Block,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: request.evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity: Some(request.fencing_identity),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(BlockTaskResult { decision, blocker })
    }

    pub fn resume_blocker(
        &self,
        request: ResumeBlockerRequest,
    ) -> Result<ResumeBlockerResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("blocker_id", &request.blocker_id)?;
        validate_identity("actor", &request.actor)?;
        validate_evidence_observations(&request.evidence)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let blocker = state.blockers.get(&request.blocker_id).cloned();
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut status = ResumeStatus::BlockerUnchanged;
        let mut resulting_blocker = blocker.clone();
        let (outcome, reason) = match (authority.as_ref(), blocker.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Blocked => {
                (DecisionOutcome::Deny, "resume_not_available")
            }
            (Some(authority), _)
                if authority.active_blocker_id.as_deref() != Some(request.blocker_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_blocker")
            }
            (_, None) => (DecisionOutcome::Defer, "stale_blocker"),
            (_, Some(blocker))
                if blocker.task_id != request.task_id || blocker.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "blocker_scope_mismatch")
            }
            (_, Some(blocker)) if blocker.generation != request.expected_blocker_generation => {
                (DecisionOutcome::Defer, "stale_blocker_generation")
            }
            _ if request.evidence_refs.is_empty() => (DecisionOutcome::Defer, "evidence_required"),
            (_, Some(blocker)) if !resume_evidence_satisfied(blocker, &request.evidence) => {
                (DecisionOutcome::Defer, "BLOCKER_UNCHANGED")
            }
            (_, Some(_)) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Ready;
                authority.authority_revision += 1;
                authority.active_blocker_id = None;
                authority.owner_agent_id = None;
                authority.fencing_identity = None;
                authority_after = authority.authority_revision;
                let blocker = state.blockers.get_mut(&request.blocker_id).unwrap();
                blocker.state = BlockerState::Released;
                blocker.released_at_authority_revision = Some(authority_after);
                resulting_blocker = Some(blocker.clone());
                performed_at = Some(now);
                status = ResumeStatus::Resumed;
                (DecisionOutcome::Allow, "resumed")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: String::new(),
            requested_transition: TransitionKind::Resume,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: request.evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity: None,
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ResumeBlockerResult {
            status,
            decision,
            blocker: resulting_blocker,
        })
    }

    pub fn supersede_blocker(
        &self,
        request: SupersedeBlockerRequest,
    ) -> Result<SupersedeBlockerResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("blocker_id", &request.blocker_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("blocker_kind", &request.blocker_kind)?;
        validate_structured_cause(&request.cause)?;
        validate_evidence_requirements(&request.required_resume_evidence)?;
        validate_evidence_observations(&request.evidence_baseline)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let prior = state.blockers.get(&request.blocker_id).cloned();
        let now = OffsetDateTime::now_utc();
        let new_hash = cause_hash(&request.blocker_kind, &request.cause);
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut prior_result = prior.clone();
        let mut new_blocker = None;
        let (outcome, reason) = match (authority.as_ref(), prior.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Blocked => {
                (DecisionOutcome::Deny, "supersede_not_available")
            }
            (Some(authority), _)
                if authority.active_blocker_id.as_deref() != Some(request.blocker_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_blocker")
            }
            (_, None) => (DecisionOutcome::Defer, "stale_blocker"),
            (_, Some(blocker))
                if blocker.task_id != request.task_id || blocker.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "blocker_scope_mismatch")
            }
            (_, Some(blocker)) if blocker.generation != request.expected_blocker_generation => {
                (DecisionOutcome::Defer, "stale_blocker_generation")
            }
            _ if request.evidence_refs.is_empty() => (DecisionOutcome::Defer, "evidence_required"),
            (_, Some(blocker)) if blocker.cause_hash == new_hash => {
                (DecisionOutcome::Defer, "BLOCKER_UNCHANGED")
            }
            (_, Some(blocker)) => {
                let new_id = new_blocker_id();
                let generation = blocker.generation.saturating_add(1);
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.authority_revision += 1;
                authority.active_blocker_id = Some(new_id.clone());
                authority_after = authority.authority_revision;

                let old = state.blockers.get_mut(&request.blocker_id).unwrap();
                old.state = BlockerState::Superseded;
                old.superseded_by_blocker_id = Some(new_id.clone());
                prior_result = Some(old.clone());

                let created = BlockerRecord {
                    blocker_id: new_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    generation,
                    kind: request.blocker_kind.clone(),
                    cause_id: request.cause.cause_id.clone(),
                    cause_schema_version: request.cause.schema_version,
                    cause_hash: new_hash,
                    blocked_at_authority_revision: authority_after,
                    required_resume_evidence: request.required_resume_evidence.clone(),
                    evidence_baseline: request.evidence_baseline.clone(),
                    state: BlockerState::Active,
                    released_at_authority_revision: None,
                    supersedes_blocker_id: Some(request.blocker_id.clone()),
                    superseded_by_blocker_id: None,
                };
                state.blockers.insert(new_id, created.clone());
                new_blocker = Some(created);
                performed_at = Some(now);
                (DecisionOutcome::Allow, "blocker_superseded")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: String::new(),
            requested_transition: TransitionKind::SupersedeBlocker,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: request.evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity: None,
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(SupersedeBlockerResult {
            decision,
            prior_blocker: prior_result,
            new_blocker,
        })
    }

    pub fn force_resume(
        &self,
        request: ForceResumeRequest,
    ) -> Result<ResumeBlockerResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("blocker_id", &request.blocker_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("reason", &request.reason)?;
        validate_identity("audit_reference", &request.audit_reference)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let blocker = state.blockers.get(&request.blocker_id).cloned();
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut status = ResumeStatus::BlockerUnchanged;
        let mut resulting_blocker = blocker.clone();
        let (outcome, reason) = match (authority.as_ref(), blocker.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found".to_owned()),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found".to_owned())
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority".to_owned())
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Blocked => {
                (DecisionOutcome::Deny, "resume_not_available".to_owned())
            }
            (Some(authority), _)
                if authority.active_blocker_id.as_deref() != Some(request.blocker_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_blocker".to_owned())
            }
            (_, None) => (DecisionOutcome::Defer, "stale_blocker".to_owned()),
            (_, Some(blocker))
                if blocker.task_id != request.task_id || blocker.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "blocker_scope_mismatch".to_owned())
            }
            (_, Some(blocker)) if blocker.generation != request.expected_blocker_generation => (
                DecisionOutcome::Defer,
                "stale_blocker_generation".to_owned(),
            ),
            (_, Some(_)) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Ready;
                authority.authority_revision += 1;
                authority.active_blocker_id = None;
                authority.owner_agent_id = None;
                authority.fencing_identity = None;
                authority_after = authority.authority_revision;
                let blocker = state.blockers.get_mut(&request.blocker_id).unwrap();
                blocker.state = BlockerState::Released;
                blocker.released_at_authority_revision = Some(authority_after);
                resulting_blocker = Some(blocker.clone());
                performed_at = Some(now);
                status = ResumeStatus::Resumed;
                (DecisionOutcome::Allow, request.reason.clone())
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: String::new(),
            requested_transition: TransitionKind::ForceResume,
            outcome,
            reason,
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: vec![request.audit_reference],
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity: None,
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ResumeBlockerResult {
            status,
            decision,
            blocker: resulting_blocker,
        })
    }

    pub fn transition(
        &self,
        request: TransitionRequest,
    ) -> Result<DecisionRecord, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("agent_id", &request.agent_id)?;
        validate_identity("actor", &request.actor)?;
        if let Some(fencing_identity) = &request.fencing_identity {
            validate_identity("fencing_identity", fencing_identity)?;
        }
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut fencing_identity = request.fencing_identity.clone();
        let (outcome, reason) = match authority.as_ref() {
            None => (DecisionOutcome::Deny, "task_not_found"),
            Some(authority) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            Some(authority)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            Some(_) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            Some(_)
                if request.requested_transition == TransitionKind::Claim
                    && request.fencing_identity.is_some() =>
            {
                (DecisionOutcome::Deny, "caller_fencing_identity_not_allowed")
            }
            Some(authority)
                if request.requested_transition == TransitionKind::Claim
                    && (authority.lifecycle_state != LifecycleState::Ready
                        || authority.owner_agent_id.is_some()) =>
            {
                (DecisionOutcome::Deny, "claim_not_available")
            }
            Some(_) if request.requested_transition == TransitionKind::Claim => {
                let fence = new_fencing_identity();
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Claimed;
                authority.authority_revision += 1;
                authority.owner_agent_id = Some(request.agent_id.clone());
                authority.fencing_identity = Some(fence.clone());
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                fencing_identity = Some(fence);
                (DecisionOutcome::Allow, "claimed")
            }
            Some(authority)
                if request.requested_transition == TransitionKind::Start
                    && authority.lifecycle_state != LifecycleState::Claimed =>
            {
                (DecisionOutcome::Deny, "start_not_available")
            }
            Some(authority)
                if request.requested_transition == TransitionKind::Start
                    && authority.owner_agent_id.as_deref() != Some(request.agent_id.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            Some(authority)
                if request.requested_transition == TransitionKind::Start
                    && authority.fencing_identity.as_deref()
                        != request.fencing_identity.as_deref() =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            Some(_) if request.requested_transition == TransitionKind::Start => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Running;
                authority.authority_revision += 1;
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                (DecisionOutcome::Allow, "started")
            }
            Some(_) => (DecisionOutcome::Defer, "transition_not_implemented"),
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.agent_id,
            requested_transition: request.requested_transition,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: request.evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity,
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(decision)
    }

    pub fn correct_decision(
        &self,
        request: CorrectionRequest,
    ) -> Result<DecisionRecord, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("supersedes_decision_id", &request.supersedes_decision_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("reason", &request.reason)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let prior = state
            .decisions
            .iter()
            .find(|decision| decision.decision_id == request.supersedes_decision_id)
            .cloned();
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut fencing_identity = authority
            .as_ref()
            .and_then(|authority| authority.fencing_identity.clone());
        let (outcome, reason, agent_id) = match (authority.as_ref(), prior.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found", String::new()),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found", String::new())
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority", String::new())
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required", String::new())
            }
            (Some(_), None) => (
                DecisionOutcome::Deny,
                "superseded_decision_not_found",
                String::new(),
            ),
            (Some(_), Some(prior))
                if prior.task_id != request.task_id || prior.run_id != request.run_id =>
            {
                (
                    DecisionOutcome::Deny,
                    "superseded_decision_scope_mismatch",
                    prior.agent_id.clone(),
                )
            }
            (Some(_), Some(prior)) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.authority_revision += 1;
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                fencing_identity = authority.fencing_identity.clone();
                (
                    DecisionOutcome::Allow,
                    request.reason.as_str(),
                    prior.agent_id.clone(),
                )
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id,
            requested_transition: TransitionKind::Correct,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs: request.evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity,
            supersedes_decision_id: Some(request.supersedes_decision_id),
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(decision)
    }
}

fn save(path: &Path, state: &StateFile) -> Result<(), GovernanceError> {
    private_file::write_json(path, state).map_err(Into::into)
}

fn validate_identity(field: &'static str, value: &str) -> Result<(), GovernanceError> {
    let value = value.trim();
    if value.is_empty() || value.len() > 512 || value.chars().any(char::is_control) {
        return Err(GovernanceError::InvalidIdentity(field));
    }
    Ok(())
}

fn validate_evidence_refs(evidence_refs: &[String]) -> Result<(), GovernanceError> {
    for evidence_ref in evidence_refs {
        validate_identity("evidence_ref", evidence_ref)?;
    }
    Ok(())
}

fn validate_structured_cause(cause: &StructuredCause) -> Result<(), GovernanceError> {
    validate_identity("cause_id", &cause.cause_id)?;
    if cause.schema_version == 0 {
        return Err(GovernanceError::InvalidIdentity("cause_schema_version"));
    }
    let mut normalized_keys = std::collections::BTreeSet::new();
    for (key, value) in &cause.fields {
        validate_identity("cause_field_key", key)?;
        validate_identity("cause_field_value", value)?;
        if !normalized_keys.insert(key.trim().to_owned()) {
            return Err(GovernanceError::InvalidIdentity("cause_field_key"));
        }
    }
    Ok(())
}

fn validate_evidence_requirements(
    requirements: &[EvidenceRequirement],
) -> Result<(), GovernanceError> {
    if requirements.is_empty() {
        return Err(GovernanceError::InvalidIdentity("required_resume_evidence"));
    }
    for requirement in requirements {
        if !requirement.kind.resume_relevant() {
            return Err(GovernanceError::InvalidIdentity("required_resume_evidence"));
        }
        validate_identity("evidence_subject", &requirement.subject)?;
    }
    Ok(())
}

fn validate_evidence_observations(
    observations: &[EvidenceObservation],
) -> Result<(), GovernanceError> {
    for observation in observations {
        validate_identity("evidence_subject", &observation.subject)?;
        validate_identity("evidence_identity", &observation.identity)?;
    }
    Ok(())
}

fn cause_hash(kind: &str, cause: &StructuredCause) -> String {
    #[derive(Serialize)]
    #[serde(rename_all = "camelCase")]
    struct Projection {
        kind: String,
        cause_id: String,
        cause_schema_version: u32,
        fields: BTreeMap<String, String>,
    }

    let projection = Projection {
        kind: kind.trim().to_owned(),
        cause_id: cause.cause_id.trim().to_owned(),
        cause_schema_version: cause.schema_version,
        fields: cause
            .fields
            .iter()
            .map(|(key, value)| (key.trim().to_owned(), value.trim().to_owned()))
            .collect(),
    };
    let bytes = serde_json::to_vec(&projection).expect("structured cause serializes");
    let mut digest = Sha256::new();
    digest.update(b"m365/agent-governance/blocker-cause/v1\0");
    digest.update(bytes);
    format!("{:x}", digest.finalize())
}

fn resume_evidence_satisfied(
    blocker: &BlockerRecord,
    observations: &[EvidenceObservation],
) -> bool {
    blocker.required_resume_evidence.iter().all(|requirement| {
        requirement.kind.resume_relevant()
            && observations.iter().any(|observation| {
                if !observation.kind.resume_relevant()
                    || observation.kind != requirement.kind
                    || observation.subject != requirement.subject
                {
                    return false;
                }
                blocker
                    .evidence_baseline
                    .iter()
                    .find(|baseline| {
                        baseline.kind == requirement.kind && baseline.subject == requirement.subject
                    })
                    .is_none_or(|baseline| baseline.identity != observation.identity)
            })
    })
}

fn next_blocker_generation(state: &StateFile, task_id: &str) -> u64 {
    state
        .blockers
        .values()
        .filter(|blocker| blocker.task_id == task_id)
        .map(|blocker| blocker.generation)
        .max()
        .unwrap_or(0)
        .saturating_add(1)
}

fn new_blocker_id() -> String {
    format!("blocker-{:032x}", rand::random::<u128>())
}

fn next_decision_id(state: &mut StateFile) -> String {
    let seq = state.next_decision_seq;
    state.next_decision_seq = state.next_decision_seq.saturating_add(1);
    format!("decision-{seq:016x}")
}

fn new_fencing_identity() -> String {
    format!("fence-{:032x}", rand::random::<u128>())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn task_and_run_registration_is_durable_and_records_performed_decision() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();

        let authority = store
            .create_task(CreateTask {
                task_id: "task-1".to_owned(),
                run_id: "run-1".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();

        assert_eq!(authority.task_id, "task-1");
        assert_eq!(authority.active_run_id, "run-1");
        assert_eq!(authority.lifecycle_state, LifecycleState::Ready);
        assert_eq!(authority.authority_revision, 1);
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let authority = reopened.authority("task-1").unwrap().unwrap();
        assert_eq!(authority.authority_revision, 1);

        let decisions = reopened.decisions("task-1").unwrap();
        assert_eq!(decisions.len(), 1);
        let decision = &decisions[0];
        assert_eq!(decision.requested_transition, TransitionKind::Register);
        assert_eq!(decision.outcome, DecisionOutcome::Allow);
        assert_eq!(decision.authority_before, 0);
        assert_eq!(decision.authority_after, 1);
        assert_eq!(decision.actor, "scheduler");
        assert!(decision.performed_at.is_some());
    }

    #[test]
    fn stale_expected_revision_is_deferred_without_mutating_authority() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-stale".to_owned(),
                run_id: "run-stale".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();

        let decision = store
            .transition(TransitionRequest {
                task_id: "task-stale".to_owned(),
                run_id: "run-stale".to_owned(),
                expected_authority_revision: 0,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:claim-request".to_owned()],
            })
            .unwrap();

        assert_eq!(decision.outcome, DecisionOutcome::Defer);
        assert_eq!(decision.reason, "stale_authority");
        assert_eq!(decision.authority_before, 1);
        assert_eq!(decision.authority_after, 1);
        assert!(decision.performed_at.is_none());

        let authority = store.authority("task-stale").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Ready);
        assert_eq!(authority.authority_revision, 1);

        let decisions = store.decisions("task-stale").unwrap();
        assert_eq!(decisions.len(), 2);
        assert_eq!(decisions[1], decision);
    }

    #[test]
    fn current_revision_claim_is_performed_and_persists_fencing_identity() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-claim".to_owned(),
                run_id: "run-claim".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();

        let decision = store
            .transition(TransitionRequest {
                task_id: "task-claim".to_owned(),
                run_id: "run-claim".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();

        assert_eq!(decision.outcome, DecisionOutcome::Allow);
        assert_eq!(decision.reason, "claimed");
        assert_eq!(decision.authority_before, 1);
        assert_eq!(decision.authority_after, 2);
        assert!(decision.performed_at.is_some());
        let fence = decision.fencing_identity.clone().unwrap();
        assert!(!fence.is_empty());

        let authority = store.authority("task-claim").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Claimed);
        assert_eq!(authority.authority_revision, 2);
        assert_eq!(authority.owner_agent_id.as_deref(), Some("worker-a"));
        assert_eq!(authority.fencing_identity.as_deref(), Some(fence.as_str()));
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let authority = reopened.authority("task-claim").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Claimed);
        assert_eq!(authority.authority_revision, 2);
        assert_eq!(authority.owner_agent_id.as_deref(), Some("worker-a"));
        assert_eq!(authority.fencing_identity.as_deref(), Some(fence.as_str()));
    }

    #[test]
    fn start_requires_current_owner_and_fencing_identity() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-start".to_owned(),
                run_id: "run-start".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-start".to_owned(),
                run_id: "run-start".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:claim-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();

        let wrong_owner = store
            .transition(TransitionRequest {
                task_id: "task-start".to_owned(),
                run_id: "run-start".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-b".to_owned(),
                actor: "dispatcher-b".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:start-request".to_owned()],
            })
            .unwrap();
        assert_eq!(wrong_owner.outcome, DecisionOutcome::Deny);
        assert_eq!(wrong_owner.reason, "owner_mismatch");
        assert!(wrong_owner.performed_at.is_none());

        let wrong_fence = store
            .transition(TransitionRequest {
                task_id: "task-start".to_owned(),
                run_id: "run-start".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some("fence-stale".to_owned()),
                evidence_refs: vec!["evidence:start-request".to_owned()],
            })
            .unwrap();
        assert_eq!(wrong_fence.outcome, DecisionOutcome::Deny);
        assert_eq!(wrong_fence.reason, "fencing_mismatch");
        assert!(wrong_fence.performed_at.is_none());

        let authority = store.authority("task-start").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Claimed);
        assert_eq!(authority.authority_revision, 2);

        let started = store
            .transition(TransitionRequest {
                task_id: "task-start".to_owned(),
                run_id: "run-start".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(started.outcome, DecisionOutcome::Allow);
        assert_eq!(started.reason, "started");
        assert_eq!(started.authority_before, 2);
        assert_eq!(started.authority_after, 3);
        assert_eq!(started.fencing_identity.as_deref(), Some(fence.as_str()));
        assert!(started.performed_at.is_some());

        let authority = store.authority("task-start").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Running);
        assert_eq!(authority.authority_revision, 3);
        assert_eq!(authority.owner_agent_id.as_deref(), Some("worker-a"));
        assert_eq!(authority.fencing_identity.as_deref(), Some(fence.as_str()));
    }

    #[test]
    fn concurrent_duplicate_claims_have_single_authoritative_winner() {
        use std::sync::{Arc, Barrier};

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = Arc::new(GovernanceStore::open(&path).unwrap());
        store
            .create_task(CreateTask {
                task_id: "task-race".to_owned(),
                run_id: "run-race".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();
        let barrier = Arc::new(Barrier::new(3));

        let mut joins = Vec::new();
        for agent in ["worker-a", "worker-b"] {
            let store = Arc::clone(&store);
            let barrier = Arc::clone(&barrier);
            joins.push(std::thread::spawn(move || {
                barrier.wait();
                store
                    .transition(TransitionRequest {
                        task_id: "task-race".to_owned(),
                        run_id: "run-race".to_owned(),
                        expected_authority_revision: 1,
                        requested_transition: TransitionKind::Claim,
                        agent_id: agent.to_owned(),
                        actor: format!("dispatcher-{agent}"),
                        fencing_identity: None,
                        evidence_refs: vec![format!("evidence:{agent}-ready")],
                    })
                    .unwrap()
            }));
        }
        barrier.wait();
        let decisions = joins
            .into_iter()
            .map(|join| join.join().unwrap())
            .collect::<Vec<_>>();

        assert_eq!(
            decisions
                .iter()
                .filter(|decision| decision.outcome == DecisionOutcome::Allow)
                .count(),
            1
        );
        assert_eq!(
            decisions
                .iter()
                .filter(|decision| decision.outcome == DecisionOutcome::Defer)
                .count(),
            1
        );
        assert_eq!(
            decisions
                .iter()
                .filter(|decision| decision.performed_at.is_some())
                .count(),
            1
        );

        let authority = store.authority("task-race").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Claimed);
        assert_eq!(authority.authority_revision, 2);
        assert!(matches!(
            authority.owner_agent_id.as_deref(),
            Some("worker-a" | "worker-b")
        ));
        assert!(authority.fencing_identity.is_some());

        drop(store);
        let reopened = GovernanceStore::open(&path).unwrap();
        let ledger = reopened.decisions("task-race").unwrap();
        assert_eq!(ledger.len(), 3);
        assert_eq!(
            ledger
                .iter()
                .filter(|decision| {
                    decision.requested_transition == TransitionKind::Claim
                        && decision.outcome == DecisionOutcome::Allow
                        && decision.performed_at.is_some()
                })
                .count(),
            1
        );
    }

    #[test]
    fn correction_supersedes_prior_decision_without_rewriting_history() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-correction".to_owned(),
                run_id: "run-correction".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();
        let original = store
            .transition(TransitionRequest {
                task_id: "task-correction".to_owned(),
                run_id: "run-correction".to_owned(),
                expected_authority_revision: 0,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:stale-request".to_owned()],
            })
            .unwrap();
        assert_eq!(original.outcome, DecisionOutcome::Defer);

        let correction = store
            .correct_decision(CorrectionRequest {
                task_id: "task-correction".to_owned(),
                run_id: "run-correction".to_owned(),
                expected_authority_revision: 1,
                supersedes_decision_id: original.decision_id.clone(),
                actor: "governance-supervisor".to_owned(),
                reason: "classification corrected after evidence review".to_owned(),
                evidence_refs: vec!["audit:review-1".to_owned()],
            })
            .unwrap();

        assert_eq!(correction.requested_transition, TransitionKind::Correct);
        assert_eq!(correction.outcome, DecisionOutcome::Allow);
        assert_eq!(correction.authority_before, 1);
        assert_eq!(correction.authority_after, 2);
        assert_eq!(
            correction.supersedes_decision_id.as_deref(),
            Some(original.decision_id.as_str())
        );
        assert!(correction.performed_at.is_some());

        let authority = store.authority("task-correction").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Ready);
        assert_eq!(authority.authority_revision, 2);

        let ledger = store.decisions("task-correction").unwrap();
        assert_eq!(ledger.len(), 3);
        assert_eq!(ledger[1], original);
        assert_eq!(ledger[2], correction);
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let ledger = reopened.decisions("task-correction").unwrap();
        assert_eq!(ledger.len(), 3);
        assert_eq!(ledger[1], original);
        assert_eq!(ledger[2], correction);
    }

    #[test]
    fn current_revision_consequential_transition_requires_evidence_reference() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-evidence".to_owned(),
                run_id: "run-evidence".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:v1".to_owned(),
            })
            .unwrap();

        let decision = store
            .transition(TransitionRequest {
                task_id: "task-evidence".to_owned(),
                run_id: "run-evidence".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: Vec::new(),
            })
            .unwrap();

        assert_eq!(decision.outcome, DecisionOutcome::Defer);
        assert_eq!(decision.reason, "evidence_required");
        assert_eq!(decision.authority_before, 1);
        assert_eq!(decision.authority_after, 1);
        assert!(decision.performed_at.is_none());
        assert_eq!(
            store
                .authority("task-evidence")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Ready
        );
    }

    #[test]
    fn running_task_blocks_with_durable_structured_cause_and_contract_identity() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-block".to_owned(),
                run_id: "run-block".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:task-block:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-block".to_owned(),
                run_id: "run-block".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-block".to_owned(),
                run_id: "run-block".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();

        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: "task-block".to_owned(),
                run_id: "run-block".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "db-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([
                        ("status".to_owned(), "down".to_owned()),
                        ("cluster".to_owned(), "primary".to_owned()),
                    ]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:db-down".to_owned()],
            })
            .unwrap();

        assert_eq!(blocked.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(blocked.decision.requested_transition, TransitionKind::Block);
        assert_eq!(blocked.decision.authority_before, 3);
        assert_eq!(blocked.decision.authority_after, 4);
        let blocker = blocked.blocker.as_ref().unwrap();
        assert_eq!(blocker.generation, 1);
        assert_eq!(blocker.state, BlockerState::Active);
        assert_eq!(blocker.blocked_at_authority_revision, 4);
        assert_eq!(
            blocker.cause_hash,
            "ef2a73651e2dcc51e7c370441c307ae16b938bcbdca3e7e2a04d6f846a79e20a"
        );

        let authority = store.authority("task-block").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Blocked);
        assert_eq!(authority.authority_revision, 4);
        assert_eq!(authority.active_run_id, "run-block");
        assert_eq!(
            authority.acceptance_contract_digest,
            "contract:task-block:v1"
        );
        assert_eq!(
            authority.active_blocker_id.as_deref(),
            Some(blocker.blocker_id.as_str())
        );
        assert!(authority.owner_agent_id.is_none());
        assert!(authority.fencing_identity.is_none());
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(reopened.blocker("task-block").unwrap().unwrap(), *blocker);
        assert_eq!(
            reopened
                .authority("task-block")
                .unwrap()
                .unwrap()
                .acceptance_contract_digest,
            "contract:task-block:v1"
        );
    }

    #[test]
    fn same_cause_without_new_relevant_evidence_returns_blocker_unchanged() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-resume-unchanged".to_owned(),
                run_id: "run-resume-unchanged".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:resume:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-resume-unchanged".to_owned(),
                run_id: "run-resume-unchanged".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-resume-unchanged".to_owned(),
                run_id: "run-resume-unchanged".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: "task-resume-unchanged".to_owned(),
                run_id: "run-resume-unchanged".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: " dependency ".to_owned(),
                cause: StructuredCause {
                    cause_id: " db-unavailable ".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([(" cluster ".to_owned(), " primary ".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:db-down".to_owned()],
            })
            .unwrap();
        let blocker = blocked.blocker.unwrap();
        let authority_before = store.authority("task-resume-unchanged").unwrap().unwrap();

        let result = store
            .resume_blocker(ResumeBlockerRequest {
                task_id: "task-resume-unchanged".to_owned(),
                run_id: "run-resume-unchanged".to_owned(),
                expected_authority_revision: 4,
                blocker_id: blocker.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "dispatcher-a".to_owned(),
                evidence: vec![
                    EvidenceObservation {
                        kind: EvidenceKind::ElapsedTime,
                        subject: "db-primary".to_owned(),
                        identity: "300s".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::Heartbeat,
                        subject: "worker-a".to_owned(),
                        identity: "heartbeat-2".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::EventSequence,
                        subject: "task-resume-unchanged".to_owned(),
                        identity: "event-99".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::Comment,
                        subject: "task-resume-unchanged".to_owned(),
                        identity: "comment-2".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::ArtifactVerification,
                        subject: "unrelated-artifact".to_owned(),
                        identity: "sha:new".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::ArtifactTimestamp,
                        subject: "db-primary".to_owned(),
                        identity: "later".to_owned(),
                    },
                    EvidenceObservation {
                        kind: EvidenceKind::DependencyState,
                        subject: "db-primary".to_owned(),
                        identity: "down".to_owned(),
                    },
                ],
                evidence_refs: vec!["evidence:resume-attempt".to_owned()],
            })
            .unwrap();

        assert_eq!(result.status, ResumeStatus::BlockerUnchanged);
        assert_eq!(result.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(result.decision.reason, "BLOCKER_UNCHANGED");
        assert_eq!(result.decision.authority_before, 4);
        assert_eq!(result.decision.authority_after, 4);
        assert!(result.decision.performed_at.is_none());
        assert_eq!(result.blocker.as_ref(), Some(&blocker));
        assert_eq!(
            store.authority("task-resume-unchanged").unwrap().unwrap(),
            authority_before
        );
    }

    #[test]
    fn new_relevant_evidence_resumes_only_current_authority_and_blocker_generation() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:resume:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "db-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("cluster".to_owned(), "primary".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:db-down".to_owned()],
            })
            .unwrap();
        let blocker = blocked.blocker.unwrap();
        let new_evidence = vec![EvidenceObservation {
            kind: EvidenceKind::DependencyState,
            subject: "db-primary".to_owned(),
            identity: "ready".to_owned(),
        }];

        let stale_authority = store
            .resume_blocker(ResumeBlockerRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 3,
                blocker_id: blocker.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "dispatcher-a".to_owned(),
                evidence: new_evidence.clone(),
                evidence_refs: vec!["evidence:db-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(stale_authority.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(stale_authority.decision.reason, "stale_authority");

        let stale_generation = store
            .resume_blocker(ResumeBlockerRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 4,
                blocker_id: blocker.blocker_id.clone(),
                expected_blocker_generation: 2,
                actor: "dispatcher-a".to_owned(),
                evidence: new_evidence.clone(),
                evidence_refs: vec!["evidence:db-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(stale_generation.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(stale_generation.decision.reason, "stale_blocker_generation");

        let resumed = store
            .resume_blocker(ResumeBlockerRequest {
                task_id: "task-resume".to_owned(),
                run_id: "run-resume".to_owned(),
                expected_authority_revision: 4,
                blocker_id: blocker.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "dispatcher-a".to_owned(),
                evidence: new_evidence,
                evidence_refs: vec!["evidence:db-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(resumed.status, ResumeStatus::Resumed);
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(resumed.decision.reason, "resumed");
        assert_eq!(resumed.decision.authority_before, 4);
        assert_eq!(resumed.decision.authority_after, 5);
        assert!(resumed.decision.performed_at.is_some());
        let released = resumed.blocker.unwrap();
        assert_eq!(released.state, BlockerState::Released);
        assert_eq!(released.released_at_authority_revision, Some(5));

        let authority = store.authority("task-resume").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Ready);
        assert_eq!(authority.authority_revision, 5);
        assert_eq!(authority.active_run_id, "run-resume");
        assert_eq!(authority.acceptance_contract_digest, "contract:resume:v1");
        assert!(authority.active_blocker_id.is_none());
        assert!(authority.owner_agent_id.is_none());
        assert!(authority.fencing_identity.is_none());
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let blockers = reopened.blockers("task-resume").unwrap();
        assert_eq!(blockers, vec![released]);
        assert_eq!(
            reopened
                .authority("task-resume")
                .unwrap()
                .unwrap()
                .authority_revision,
            5
        );
    }

    #[test]
    fn blocker_supersession_preserves_lineage_and_same_cause_cannot_fake_generation() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:supersede:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "db-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("cluster".to_owned(), "primary".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:db-down".to_owned()],
            })
            .unwrap();
        let original = blocked.blocker.unwrap();

        let unchanged = store
            .supersede_blocker(SupersedeBlockerRequest {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                expected_authority_revision: 4,
                blocker_id: original.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "dispatcher-a".to_owned(),
                blocker_kind: " dependency ".to_owned(),
                cause: StructuredCause {
                    cause_id: " db-unavailable ".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([(" cluster ".to_owned(), " primary ".to_owned())]),
                },
                required_resume_evidence: original.required_resume_evidence.clone(),
                evidence_baseline: original.evidence_baseline.clone(),
                evidence_refs: vec!["evidence:reevaluate-same-cause".to_owned()],
            })
            .unwrap();
        assert_eq!(unchanged.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(unchanged.decision.reason, "BLOCKER_UNCHANGED");
        assert!(unchanged.new_blocker.is_none());
        assert_eq!(
            store.blockers("task-supersede").unwrap(),
            vec![original.clone()]
        );
        assert_eq!(
            store
                .authority("task-supersede")
                .unwrap()
                .unwrap()
                .authority_revision,
            4
        );

        let replaced = store
            .supersede_blocker(SupersedeBlockerRequest {
                task_id: "task-supersede".to_owned(),
                run_id: "run-supersede".to_owned(),
                expected_authority_revision: 4,
                blocker_id: original.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "dispatcher-a".to_owned(),
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "network-partition".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("segment".to_owned(), "storage".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "network-storage".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "network-storage".to_owned(),
                    identity: "partitioned".to_owned(),
                }],
                evidence_refs: vec!["evidence:network-partition".to_owned()],
            })
            .unwrap();
        assert_eq!(replaced.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(
            replaced.decision.requested_transition,
            TransitionKind::SupersedeBlocker
        );
        assert_eq!(replaced.decision.authority_before, 4);
        assert_eq!(replaced.decision.authority_after, 5);
        let old = replaced.prior_blocker.unwrap();
        let new = replaced.new_blocker.unwrap();
        assert_eq!(old.state, BlockerState::Superseded);
        assert_eq!(
            old.superseded_by_blocker_id.as_deref(),
            Some(new.blocker_id.as_str())
        );
        assert_eq!(new.generation, 2);
        assert_eq!(new.state, BlockerState::Active);
        assert_eq!(
            new.supersedes_blocker_id.as_deref(),
            Some(old.blocker_id.as_str())
        );
        assert_ne!(new.cause_hash, old.cause_hash);

        let authority = store.authority("task-supersede").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Blocked);
        assert_eq!(authority.authority_revision, 5);
        assert_eq!(authority.active_run_id, "run-supersede");
        assert_eq!(
            authority.acceptance_contract_digest,
            "contract:supersede:v1"
        );
        assert_eq!(
            authority.active_blocker_id.as_deref(),
            Some(new.blocker_id.as_str())
        );
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(reopened.blockers("task-supersede").unwrap(), vec![old, new]);
    }

    #[test]
    fn force_resume_requires_actor_reason_and_audit_and_keeps_blocker_history() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-force".to_owned(),
                run_id: "run-force".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:force:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-force".to_owned(),
                run_id: "run-force".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-force".to_owned(),
                run_id: "run-force".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let blocker = store
            .block_task(BlockTaskRequest {
                task_id: "task-force".to_owned(),
                run_id: "run-force".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "manual-gate".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("gate".to_owned(), "closed".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::ApprovalGrant,
                    subject: "manual-gate".to_owned(),
                }],
                evidence_baseline: Vec::new(),
                evidence_refs: vec!["evidence:manual-gate".to_owned()],
            })
            .unwrap()
            .blocker
            .unwrap();

        for (actor, reason, audit_reference, field) in [
            ("", "verified manually", "audit:1", "actor"),
            ("operator", "", "audit:1", "reason"),
            ("operator", "verified manually", "", "audit_reference"),
        ] {
            let error = store
                .force_resume(ForceResumeRequest {
                    task_id: "task-force".to_owned(),
                    run_id: "run-force".to_owned(),
                    expected_authority_revision: 4,
                    blocker_id: blocker.blocker_id.clone(),
                    expected_blocker_generation: 1,
                    actor: actor.to_owned(),
                    reason: reason.to_owned(),
                    audit_reference: audit_reference.to_owned(),
                })
                .unwrap_err();
            assert!(matches!(error, GovernanceError::InvalidIdentity(actual) if actual == field));
            assert_eq!(
                store
                    .authority("task-force")
                    .unwrap()
                    .unwrap()
                    .authority_revision,
                4
            );
        }

        let resumed = store
            .force_resume(ForceResumeRequest {
                task_id: "task-force".to_owned(),
                run_id: "run-force".to_owned(),
                expected_authority_revision: 4,
                blocker_id: blocker.blocker_id.clone(),
                expected_blocker_generation: 1,
                actor: "operator".to_owned(),
                reason: "verified manually under change record".to_owned(),
                audit_reference: "audit:chg-42".to_owned(),
            })
            .unwrap();
        assert_eq!(resumed.status, ResumeStatus::Resumed);
        assert_eq!(
            resumed.decision.requested_transition,
            TransitionKind::ForceResume
        );
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(
            resumed.decision.reason,
            "verified manually under change record"
        );
        assert_eq!(resumed.decision.actor, "operator");
        assert_eq!(resumed.decision.evidence_refs, vec!["audit:chg-42"]);
        assert!(resumed.decision.performed_at.is_some());
        assert_eq!(resumed.decision.authority_before, 4);
        assert_eq!(resumed.decision.authority_after, 5);

        let authority = store.authority("task-force").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Ready);
        assert_eq!(authority.authority_revision, 5);
        assert_eq!(authority.acceptance_contract_digest, "contract:force:v1");
        assert!(authority.active_blocker_id.is_none());
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let blockers = reopened.blockers("task-force").unwrap();
        assert_eq!(blockers.len(), 1);
        assert_eq!(blockers[0].blocker_id, blocker.blocker_id);
        assert_eq!(blockers[0].state, BlockerState::Released);
        assert_eq!(blockers[0].released_at_authority_revision, Some(5));
    }

    #[test]
    fn concurrent_same_cause_resume_passes_cannot_self_release_or_claim_worker() {
        use std::sync::{Arc, Barrier};

        let root = tempfile::tempdir().unwrap();
        let store =
            Arc::new(GovernanceStore::open(root.path().join("agent-governance.json")).unwrap());
        store
            .create_task(CreateTask {
                task_id: "task-block-race".to_owned(),
                run_id: "run-block-race".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:block-race:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-block-race".to_owned(),
                run_id: "run-block-race".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-block-race".to_owned(),
                run_id: "run-block-race".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let blocker = store
            .block_task(BlockTaskRequest {
                task_id: "task-block-race".to_owned(),
                run_id: "run-block-race".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "db-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("cluster".to_owned(), "primary".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db-primary".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:db-down".to_owned()],
            })
            .unwrap()
            .blocker
            .unwrap();

        let barrier = Arc::new(Barrier::new(3));
        let mut joins = Vec::new();
        for dispatcher in ["dispatcher-a", "dispatcher-b"] {
            let store = Arc::clone(&store);
            let barrier = Arc::clone(&barrier);
            let blocker_id = blocker.blocker_id.clone();
            joins.push(std::thread::spawn(move || {
                barrier.wait();
                store
                    .resume_blocker(ResumeBlockerRequest {
                        task_id: "task-block-race".to_owned(),
                        run_id: "run-block-race".to_owned(),
                        expected_authority_revision: 4,
                        blocker_id,
                        expected_blocker_generation: 1,
                        actor: dispatcher.to_owned(),
                        evidence: vec![
                            EvidenceObservation {
                                kind: EvidenceKind::Heartbeat,
                                subject: "worker-a".to_owned(),
                                identity: "new-heartbeat".to_owned(),
                            },
                            EvidenceObservation {
                                kind: EvidenceKind::DependencyState,
                                subject: "db-primary".to_owned(),
                                identity: "down".to_owned(),
                            },
                        ],
                        evidence_refs: vec![format!("evidence:{dispatcher}-pass")],
                    })
                    .unwrap()
            }));
        }
        barrier.wait();
        let results = joins
            .into_iter()
            .map(|join| join.join().unwrap())
            .collect::<Vec<_>>();
        assert!(results.iter().all(|result| {
            result.status == ResumeStatus::BlockerUnchanged
                && result.decision.outcome == DecisionOutcome::Defer
                && result.decision.reason == "BLOCKER_UNCHANGED"
                && result.decision.performed_at.is_none()
        }));

        let authority = store.authority("task-block-race").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Blocked);
        assert_eq!(authority.authority_revision, 4);
        assert_eq!(authority.active_run_id, "run-block-race");
        assert_eq!(
            authority.acceptance_contract_digest,
            "contract:block-race:v1"
        );
        assert_eq!(
            authority.active_blocker_id.as_deref(),
            Some(blocker.blocker_id.as_str())
        );

        let claim_while_blocked = store
            .transition(TransitionRequest {
                task_id: "task-block-race".to_owned(),
                run_id: "run-block-race".to_owned(),
                expected_authority_revision: 4,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-b".to_owned(),
                actor: "dispatcher-b".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-b-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(claim_while_blocked.outcome, DecisionOutcome::Deny);
        assert_eq!(claim_while_blocked.reason, "claim_not_available");
        assert_eq!(
            store
                .authority("task-block-race")
                .unwrap()
                .unwrap()
                .authority_revision,
            4
        );
    }
}
