use std::{
    collections::BTreeMap,
    path::{Path, PathBuf},
    sync::Mutex,
};

use serde::{Deserialize, Serialize};
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
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum TransitionKind {
    Register,
    Claim,
    Start,
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

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskAuthority {
    pub task_id: String,
    pub active_run_id: String,
    pub lifecycle_state: LifecycleState,
    pub authority_revision: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_agent_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fencing_identity: Option<String>,
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
    decisions: Vec<DecisionRecord>,
    next_decision_seq: u64,
}

impl Default for StateFile {
    fn default() -> Self {
        Self {
            schema: SCHEMA.to_owned(),
            tasks: BTreeMap::new(),
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
            owner_agent_id: None,
            fencing_identity: None,
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
}
