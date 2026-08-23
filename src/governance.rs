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
    Suspending,
    Suspended,
    Resuming,
    Blocked,
    Completing,
    Completed,
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
    IssueApprovalGrant,
    ConsumeApprovalGrant,
    RevokeApprovalGrant,
    RotateContext,
    BeginHandoff,
    SuspendHandoff,
    AcquireHandoff,
    ResumeHandoff,
    BeginCompletion,
    Complete,
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

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum PolicyLayer {
    CompanyRequirements,
    ProviderRequirements,
    ServicePolicy,
    ProfilePolicy,
    TaskPolicy,
    UserPreference,
    AgentIntent,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ApprovalOutcome {
    Allow,
    Deny,
    Timeout,
    Abort,
    RequireUserApproval,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum PolicyEvaluatorStatus {
    Resolved,
    Timeout,
    Aborted,
    ParseFailure,
    Unavailable,
    Unknown,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyRule {
    pub layer: PolicyLayer,
    pub policy_id: String,
    pub exception_id: Option<String>,
    pub requested_action: String,
    pub target_scope: String,
    pub outcome: ApprovalOutcome,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyEvaluationRequest {
    pub requested_action: String,
    pub target_scope: String,
    pub policy_version: String,
    pub evaluator_version: String,
    pub evaluator_status: PolicyEvaluatorStatus,
    pub evidence_refs: Vec<String>,
    pub rules: Vec<PolicyRule>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PolicyEvaluationResult {
    pub outcome: ApprovalOutcome,
    pub reason: String,
    pub governing_policy_id: Option<String>,
    pub governing_layer: Option<PolicyLayer>,
    pub exception_id: Option<String>,
    pub requested_action: String,
    pub target_scope: String,
    pub policy_version: String,
    pub evaluator_version: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalGrant {
    pub approval_id: String,
    pub actor: String,
    pub policy_layer: PolicyLayer,
    pub policy_id: String,
    pub exception_id: String,
    pub permitted_action: String,
    pub task_id: String,
    pub run_id: String,
    pub target_scope: String,
    pub authority_revision: u64,
    #[serde(with = "time::serde::rfc3339")]
    pub issued_at: OffsetDateTime,
    #[serde(with = "time::serde::rfc3339")]
    pub expires_at: OffsetDateTime,
    pub max_uses: u32,
    pub consumed_uses: u32,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub revoked_at: Option<OffsetDateTime>,
    pub fencing_identity: String,
    pub policy_version: String,
    pub evaluator_version: String,
    pub evidence_refs: Vec<String>,
    pub issuance_decision_id: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalGrantConsumption {
    pub consumption_id: String,
    pub approval_id: String,
    pub task_id: String,
    pub run_id: String,
    pub actor: String,
    pub permitted_action: String,
    pub target_scope: String,
    pub authority_revision: u64,
    pub fencing_identity: String,
    pub evidence_refs: Vec<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub consumed_at: OffsetDateTime,
    pub decision_id: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IssueApprovalGrantRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub approved_actor: String,
    pub issued_by: String,
    pub fencing_identity: String,
    pub evaluation: PolicyEvaluationRequest,
    pub expires_at: OffsetDateTime,
    pub max_uses: u32,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IssueApprovalGrantResult {
    pub decision: DecisionRecord,
    pub grant: Option<ApprovalGrant>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConsumeApprovalGrantRequest {
    pub approval_id: String,
    pub consumption_id: String,
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub actor: String,
    pub permitted_action: String,
    pub target_scope: String,
    pub fencing_identity: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ConsumeApprovalGrantResult {
    pub decision: DecisionRecord,
    pub grant: Option<ApprovalGrant>,
    pub consumption: Option<ApprovalGrantConsumption>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RevokeApprovalGrantRequest {
    pub approval_id: String,
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub actor: String,
    pub fencing_identity: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RevokeApprovalGrantResult {
    pub decision: DecisionRecord,
    pub grant: Option<ApprovalGrant>,
}

pub fn evaluate_policy(mut request: PolicyEvaluationRequest) -> PolicyEvaluationResult {
    let evaluator_failure = match request.evaluator_status {
        PolicyEvaluatorStatus::Resolved => None,
        PolicyEvaluatorStatus::Timeout => Some((ApprovalOutcome::Timeout, "evaluator_timeout")),
        PolicyEvaluatorStatus::Aborted => Some((ApprovalOutcome::Abort, "evaluator_aborted")),
        PolicyEvaluatorStatus::ParseFailure => {
            Some((ApprovalOutcome::Abort, "evaluator_parse_failure"))
        }
        PolicyEvaluatorStatus::Unavailable => {
            Some((ApprovalOutcome::Abort, "evaluator_unavailable"))
        }
        PolicyEvaluatorStatus::Unknown => Some((ApprovalOutcome::Abort, "evaluator_unknown")),
    };
    if let Some((outcome, reason)) = evaluator_failure {
        return PolicyEvaluationResult {
            outcome,
            reason: reason.to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }

    if validate_policy_evaluation_request(&request).is_err() {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Abort,
            reason: "invalid_policy_input".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }
    if request.evidence_refs.is_empty() {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Abort,
            reason: "evidence_required".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }
    if request.rules.iter().any(|rule| {
        rule.requested_action != request.requested_action
            || rule.target_scope != request.target_scope
    }) {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Abort,
            reason: "policy_rule_binding_mismatch".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }
    if request.rules.iter().any(|rule| {
        matches!(
            rule.outcome,
            ApprovalOutcome::Timeout | ApprovalOutcome::Abort
        )
    }) {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Abort,
            reason: "invalid_policy_outcome".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }
    let mut observed_layers = std::collections::BTreeSet::new();
    if request
        .rules
        .iter()
        .any(|rule| !observed_layers.insert(rule.layer))
    {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Abort,
            reason: "duplicate_policy_layer".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    }

    request.rules.sort_by_key(|rule| rule.layer);
    let Some(mut governing) = request.rules.first().cloned() else {
        return PolicyEvaluationResult {
            outcome: ApprovalOutcome::Deny,
            reason: "policy_required".to_owned(),
            governing_policy_id: None,
            governing_layer: None,
            exception_id: None,
            requested_action: request.requested_action,
            target_scope: request.target_scope,
            policy_version: request.policy_version,
            evaluator_version: request.evaluator_version,
            evidence_refs: request.evidence_refs,
        };
    };

    for rule in request.rules.into_iter().skip(1) {
        let strictness = |outcome| match outcome {
            ApprovalOutcome::Allow => Some(0),
            ApprovalOutcome::RequireUserApproval => Some(1),
            ApprovalOutcome::Deny => Some(2),
            ApprovalOutcome::Timeout | ApprovalOutcome::Abort => None,
        };
        let (Some(rule_strictness), Some(governing_strictness)) =
            (strictness(rule.outcome), strictness(governing.outcome))
        else {
            return PolicyEvaluationResult {
                outcome: ApprovalOutcome::Abort,
                reason: "invalid_policy_outcome".to_owned(),
                governing_policy_id: Some(governing.policy_id),
                governing_layer: Some(governing.layer),
                exception_id: governing.exception_id,
                requested_action: request.requested_action,
                target_scope: request.target_scope,
                policy_version: request.policy_version,
                evaluator_version: request.evaluator_version,
                evidence_refs: request.evidence_refs,
            };
        };
        if rule_strictness < governing_strictness {
            return PolicyEvaluationResult {
                outcome: ApprovalOutcome::Deny,
                reason: "lower_layer_relaxation".to_owned(),
                governing_policy_id: Some(governing.policy_id),
                governing_layer: Some(governing.layer),
                exception_id: governing.exception_id,
                requested_action: request.requested_action,
                target_scope: request.target_scope,
                policy_version: request.policy_version,
                evaluator_version: request.evaluator_version,
                evidence_refs: request.evidence_refs,
            };
        }
        if rule_strictness > governing_strictness {
            governing = rule;
        }
    }

    PolicyEvaluationResult {
        outcome: governing.outcome,
        reason: "policy_resolved".to_owned(),
        governing_policy_id: Some(governing.policy_id),
        governing_layer: Some(governing.layer),
        exception_id: governing.exception_id,
        requested_action: request.requested_action,
        target_scope: request.target_scope,
        policy_version: request.policy_version,
        evaluator_version: request.evaluator_version,
        evidence_refs: request.evidence_refs,
    }
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
pub struct ArtifactRequirement {
    pub artifact_id: String,
    pub after_mutation_id: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompletionContract {
    pub required_mutation_ids: Vec<String>,
    pub required_artifacts: Vec<ArtifactRequirement>,
    pub approval_required: bool,
    pub memory_durability_required: bool,
}

impl CompletionContract {
    pub fn digest(&self) -> String {
        completion_contract_digest(self)
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CompletionIntentSource {
    ModelFinal,
    TransportFinal,
    ToolLoopExhausted,
    AgentIntent,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CompletionGateState {
    Allow,
    Deny,
    Pending,
    Timeout,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ApprovalState {
    NotRequired,
    Allow,
    Deny,
    Pending,
    Timeout,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum MemoryDurabilityState {
    NotRequired,
    Accepted,
    Durable,
    Queued,
    Claimed,
    Processing,
    Degraded,
    Unsupported,
    Unknown,
    Failed,
    Timeout,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum MutationDurability {
    Accepted,
    Durable,
    Failed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MutationReceipt {
    pub mutation_id: String,
    pub receipt_id: String,
    pub authority_revision: u64,
    pub fencing_identity: String,
    pub durability: MutationDurability,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ArtifactVerification {
    pub artifact_id: String,
    pub identity: String,
    pub authority_revision: u64,
    pub fencing_identity: String,
    pub after_mutation_receipt_id: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompletionObservation {
    pub observed_authority_revision: u64,
    pub fencing_identity: String,
    pub acceptance_satisfied: bool,
    pub active_child_ids: Vec<String>,
    pub pending_consequential_mutation_ids: Vec<String>,
    pub mutation_receipts: Vec<MutationReceipt>,
    pub artifact_verifications: Vec<ArtifactVerification>,
    pub policy_state: CompletionGateState,
    pub approval_state: ApprovalState,
    pub memory_state: MemoryDurabilityState,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BeginCompletionRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub source: CompletionIntentSource,
    pub contract: CompletionContract,
    pub observation: CompletionObservation,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CompletionBarrierState {
    Active,
    Completed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CompletionBarrierRecord {
    pub completion_id: String,
    pub task_id: String,
    pub run_id: String,
    pub source: CompletionIntentSource,
    pub contract_digest: String,
    pub evidence_digest: String,
    #[serde(default)]
    pub mutation_receipts_digest: String,
    pub evidence_authority_revision: u64,
    pub began_at_authority_revision: u64,
    pub owner_agent_id: String,
    pub fencing_identity: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub memory_checkpoint_id: Option<String>,
    pub state: CompletionBarrierState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completed_at_authority_revision: Option<u64>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BeginCompletionResult {
    pub decision: DecisionRecord,
    pub barrier: Option<CompletionBarrierRecord>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FinishCompletionRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub completion_id: String,
    pub agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub contract: CompletionContract,
    pub observation: CompletionObservation,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FinishCompletionResult {
    pub decision: DecisionRecord,
    pub barrier: Option<CompletionBarrierRecord>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CapabilityStatus {
    Supported,
    Degraded,
    Unsupported,
    Incompatible,
    Unknown,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CapabilityIntegrationSeam {
    Adapter,
    Plugin,
    Hook,
    Gateway,
    Sidecar,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CapabilityProbeEvidence {
    pub schema_version: u32,
    pub adapter_id: String,
    pub adapter_version: String,
    pub upstream_id: String,
    pub upstream_version: String,
    pub requested_capability: String,
    pub integration_seam: CapabilityIntegrationSeam,
    pub surface_present: Option<bool>,
    pub version_compatible: Option<bool>,
    pub observed_field_families: Vec<String>,
    pub observed_semantics: Vec<String>,
    pub evidence_refs: Vec<String>,
}

pub trait CapabilityProbeEvidenceVerifier {
    /// Verify the referenced source evidence and its claimed semantics independently of the
    /// caller-provided envelope. Syntax or subject identity checks alone are insufficient.
    fn verifies(&self, evidence: &CapabilityProbeEvidence) -> bool;
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CapabilityProbeRequest {
    pub adapter_id: String,
    pub adapter_version: String,
    pub upstream_id: String,
    pub upstream_version: String,
    pub requested_capability: String,
    pub integration_seam: CapabilityIntegrationSeam,
    pub required_field_families: Vec<String>,
    pub required_semantics: Vec<String>,
    pub evidence: Option<CapabilityProbeEvidence>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CapabilityProbeResult {
    pub schema_version: u32,
    pub evidence_schema_version: Option<u32>,
    pub status: CapabilityStatus,
    pub adapter_id: String,
    pub adapter_version: String,
    pub upstream_id: String,
    pub upstream_version: String,
    pub requested_capability: String,
    pub integration_seam: CapabilityIntegrationSeam,
    pub surface_present: Option<bool>,
    pub version_compatible: Option<bool>,
    pub required_field_families: Vec<String>,
    pub required_semantics: Vec<String>,
    pub observed_field_families: Vec<String>,
    pub observed_semantics: Vec<String>,
    pub missing_field_families: Vec<String>,
    pub missing_semantics: Vec<String>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryRetainRequest {
    pub retain_request_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_revision: u64,
    pub content_digest: String,
    pub fencing_identity: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryDurabilityEvidence {
    pub schema_version: u32,
    pub adapter_id: String,
    pub adapter_version: String,
    pub upstream_id: String,
    pub upstream_version: String,
    pub retain_request_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_revision: u64,
    pub content_digest: String,
    pub fencing_identity: String,
    pub operation_id: String,
    #[serde(with = "time::serde::rfc3339")]
    pub durable_at: OffsetDateTime,
    pub evidence_refs: Vec<String>,
}

pub trait MemoryDurabilityEvidenceVerifier {
    /// Verify provider-defined terminal durability independently of the adapter envelope.
    fn verifies(&self, evidence: &MemoryDurabilityEvidence) -> bool;
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryRetainResult {
    pub retain_request_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_revision: u64,
    pub content_digest: String,
    pub fencing_identity: String,
    pub operation_id: String,
    pub durability: MemoryDurabilityState,
    pub evidence: Option<MemoryDurabilityEvidence>,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryRetainEvaluation {
    pub capability_status: CapabilityStatus,
    pub durability: MemoryDurabilityState,
    pub is_durable: bool,
    pub reason: String,
    pub operation_id: Option<String>,
    pub provider_evidence_refs: Vec<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum MemoryPortHealth {
    Healthy,
    Degraded,
    Unavailable,
    Unknown,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryPortProbe {
    pub capability: CapabilityProbeResult,
    pub health: MemoryPortHealth,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ContextLayer {
    KanbanDurableHistory,
    LongTermMemory,
    LiveModelContext,
    AcpAuthority,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum MemoryHydrateStatus {
    Hydrated,
    Degraded,
    Unsupported,
    Failed,
    Timeout,
    Unknown,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AuthoritySummary {
    pub task_id: String,
    pub run_id: String,
    pub lifecycle_state: LifecycleState,
    pub authority_revision: u64,
    pub acceptance_contract_digest: String,
    pub owner_agent_id: String,
    pub fencing_identity: String,
    pub active_blocker_id: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryHydrateRequest {
    pub hydrate_request_id: String,
    pub checkpoint_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_summary: AuthoritySummary,
    pub new_context_id: String,
    pub memory_query: String,
    pub selected_evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HydratedContextItem {
    pub memory_id: String,
    pub layer: ContextLayer,
    pub content: String,
    pub evidence_ref: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MemoryHydrateResult {
    pub hydrate_request_id: String,
    pub checkpoint_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_revision: u64,
    pub new_context_id: String,
    pub status: MemoryHydrateStatus,
    pub items: Vec<HydratedContextItem>,
    pub evidence_refs: Vec<String>,
}

pub trait MemoryPort {
    fn probe(&self, requested_capability: &str) -> MemoryPortProbe;
    fn retain(&self, request: &MemoryRetainRequest) -> MemoryRetainResult;
    fn hydrate(&self, request: &MemoryHydrateRequest) -> MemoryHydrateResult;
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ContextLifecyclePhase {
    PreCompact,
    RetainDurable,
    ContextCheckpoint,
    NewContext,
    TypedHydrate,
    PostCompactVerify,
    Failed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextCheckpoint {
    pub checkpoint_id: String,
    pub rotation_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub authority_summary: AuthoritySummary,
    pub kanban_history_ref: String,
    pub old_context_id: String,
    pub new_context_id: String,
    pub retain_request_id: String,
    pub retain_operation_id: String,
    pub capability_status: CapabilityStatus,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub retain_capability: Option<CapabilityProbeResult>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hydrate_capability: Option<CapabilityProbeResult>,
    pub phase: ContextLifecyclePhase,
    pub phase_trace: Vec<ContextLifecyclePhase>,
    pub memory_evidence_refs: Vec<String>,
    pub selected_evidence_refs: Vec<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub verified_at: Option<OffsetDateTime>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ContextRotationRequest {
    pub rotation_id: String,
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub lineage_id: String,
    pub kanban_history_ref: String,
    pub old_context_id: String,
    pub new_context_id: String,
    pub retain_request_id: String,
    pub memory_content_digest: String,
    pub memory_query: String,
    pub selected_evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ContextRotationResult {
    pub decision: DecisionRecord,
    pub checkpoint: Option<ContextCheckpoint>,
    pub hydration: Option<MemoryHydrateResult>,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum HandoffCheckpointState {
    Suspending,
    Suspended,
    Resuming,
    Resumed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HandoffCheckpoint {
    pub checkpoint_id: String,
    pub task_id: String,
    pub run_id: String,
    pub lineage_id: String,
    pub root_agent_id: String,
    pub parent_agent_id: String,
    pub old_owner_agent_id: String,
    pub replacement_agent_id: String,
    pub acceptance_contract_digest: String,
    pub memory_durability_required: bool,
    pub source_authority_revision: u64,
    pub suspending_authority_revision: u64,
    pub suspended_authority_revision: Option<u64>,
    pub resuming_authority_revision: Option<u64>,
    pub resumed_authority_revision: Option<u64>,
    pub old_ownership_generation: u64,
    pub new_ownership_generation: Option<u64>,
    pub old_fencing_identity: String,
    pub new_fencing_identity: Option<String>,
    pub active_blocker_id: Option<String>,
    pub blocker_evidence_baseline: Vec<EvidenceObservation>,
    pub pending_consequential_mutation_ids: Vec<String>,
    pub mutation_receipts: Vec<MutationReceipt>,
    pub context_checkpoint_id: String,
    pub memory_checkpoint_id: Option<String>,
    pub handoff_capability: CapabilityProbeResult,
    pub evidence_refs: Vec<String>,
    pub state: HandoffCheckpointState,
    #[serde(with = "time::serde::rfc3339")]
    pub created_at: OffsetDateTime,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub released_at: Option<OffsetDateTime>,
    #[serde(default, with = "time::serde::rfc3339::option")]
    pub resumed_at: Option<OffsetDateTime>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BeginHandoffRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub lineage_id: String,
    pub root_agent_id: String,
    pub parent_agent_id: String,
    pub old_owner_agent_id: String,
    pub replacement_agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub contract: CompletionContract,
    pub handoff_capability: CapabilityProbeResult,
    pub blocker_evidence_baseline: Vec<EvidenceObservation>,
    pub pending_consequential_mutation_ids: Vec<String>,
    pub mutation_receipts: Vec<MutationReceipt>,
    pub context_checkpoint_id: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct HandoffTransitionResult {
    pub decision: DecisionRecord,
    pub checkpoint: Option<HandoffCheckpoint>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SuspendHandoffRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub checkpoint_id: String,
    pub old_owner_agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub handoff_capability: CapabilityProbeResult,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AcquireHandoffRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub checkpoint_id: String,
    pub replacement_agent_id: String,
    pub actor: String,
    pub handoff_capability: CapabilityProbeResult,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AcquireHandoffResult {
    pub decision: DecisionRecord,
    pub checkpoint: Option<HandoffCheckpoint>,
    pub fencing_identity: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumeHandoffRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub checkpoint_id: String,
    pub replacement_agent_id: String,
    pub actor: String,
    pub fencing_identity: String,
    pub new_context_id: String,
    pub memory_query: String,
    pub handoff_capability: CapabilityProbeResult,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResumeHandoffResult {
    pub decision: DecisionRecord,
    pub checkpoint: Option<HandoffCheckpoint>,
    pub hydration: Option<MemoryHydrateResult>,
}

pub fn evaluate_memory_retain(
    capability: &CapabilityProbeResult,
    request: &MemoryRetainRequest,
    result: MemoryRetainResult,
    verifier: &dyn MemoryDurabilityEvidenceVerifier,
) -> Result<MemoryRetainEvaluation, GovernanceError> {
    validate_memory_retain_request(request)?;
    validate_identity("retain_request_id", &result.retain_request_id)?;
    validate_identity("task_id", &result.task_id)?;
    validate_identity("run_id", &result.run_id)?;
    validate_identity("lineage_id", &result.lineage_id)?;
    validate_identity("operation_id", &result.operation_id)?;
    validate_evidence_refs(&result.evidence_refs)?;

    let capability_failure = match capability.status {
        CapabilityStatus::Supported => None,
        CapabilityStatus::Degraded => Some((
            MemoryDurabilityState::Degraded,
            "memory_capability_degraded",
        )),
        CapabilityStatus::Unsupported => Some((
            MemoryDurabilityState::Unsupported,
            "memory_capability_unsupported",
        )),
        CapabilityStatus::Incompatible => Some((
            MemoryDurabilityState::Unsupported,
            "memory_capability_incompatible",
        )),
        CapabilityStatus::Unknown => {
            Some((MemoryDurabilityState::Unknown, "memory_capability_unknown"))
        }
    };
    if let Some((durability, reason)) = capability_failure {
        return Ok(MemoryRetainEvaluation {
            capability_status: capability.status,
            durability,
            is_durable: false,
            reason: reason.to_owned(),
            operation_id: Some(result.operation_id),
            provider_evidence_refs: Vec::new(),
        });
    }
    if capability.requested_capability != "memory.retain_durable" {
        return Ok(memory_retain_failure(
            capability.status,
            MemoryDurabilityState::Failed,
            "memory_capability_mismatch",
            Some(result.operation_id),
        ));
    }
    if result.retain_request_id != request.retain_request_id
        || result.task_id != request.task_id
        || result.run_id != request.run_id
        || result.lineage_id != request.lineage_id
        || result.authority_revision != request.authority_revision
        || result.content_digest != request.content_digest
        || result.fencing_identity != request.fencing_identity
    {
        return Ok(memory_retain_failure(
            capability.status,
            MemoryDurabilityState::Failed,
            "memory_retain_binding_mismatch",
            Some(result.operation_id),
        ));
    }
    if result.durability != MemoryDurabilityState::Durable {
        return Ok(memory_retain_failure(
            capability.status,
            result.durability,
            "memory_not_durable",
            Some(result.operation_id),
        ));
    }
    let Some(evidence) = result.evidence else {
        return Ok(memory_retain_failure(
            capability.status,
            MemoryDurabilityState::Failed,
            "memory_durability_evidence_required",
            Some(result.operation_id),
        ));
    };
    validate_memory_durability_evidence(&evidence)?;
    let evidence_bound = evidence.schema_version == 1
        && evidence.adapter_id == capability.adapter_id
        && evidence.adapter_version == capability.adapter_version
        && evidence.upstream_id == capability.upstream_id
        && evidence.upstream_version == capability.upstream_version
        && evidence.retain_request_id == request.retain_request_id
        && evidence.task_id == request.task_id
        && evidence.run_id == request.run_id
        && evidence.lineage_id == request.lineage_id
        && evidence.authority_revision == request.authority_revision
        && evidence.content_digest == request.content_digest
        && evidence.fencing_identity == request.fencing_identity
        && evidence.operation_id == result.operation_id
        && verifier.verifies(&evidence);
    if !evidence_bound {
        return Ok(memory_retain_failure(
            capability.status,
            MemoryDurabilityState::Failed,
            "memory_durability_evidence_rejected",
            Some(result.operation_id),
        ));
    }
    if evidence.evidence_refs.is_empty() {
        return Ok(memory_retain_failure(
            capability.status,
            MemoryDurabilityState::Failed,
            "memory_durability_evidence_required",
            Some(result.operation_id),
        ));
    }

    Ok(MemoryRetainEvaluation {
        capability_status: capability.status,
        durability: MemoryDurabilityState::Durable,
        is_durable: true,
        reason: "memory_durable".to_owned(),
        operation_id: Some(result.operation_id),
        provider_evidence_refs: evidence.evidence_refs,
    })
}

fn memory_retain_failure(
    capability_status: CapabilityStatus,
    durability: MemoryDurabilityState,
    reason: &str,
    operation_id: Option<String>,
) -> MemoryRetainEvaluation {
    MemoryRetainEvaluation {
        capability_status,
        durability,
        is_durable: false,
        reason: reason.to_owned(),
        operation_id,
        provider_evidence_refs: Vec::new(),
    }
}

pub fn evaluate_capability_probe(
    request: CapabilityProbeRequest,
    verifier: &dyn CapabilityProbeEvidenceVerifier,
) -> Result<CapabilityProbeResult, GovernanceError> {
    validate_identity("adapter_id", &request.adapter_id)?;
    validate_identity("adapter_version", &request.adapter_version)?;
    validate_identity("upstream_id", &request.upstream_id)?;
    validate_identity("upstream_version", &request.upstream_version)?;
    validate_identity("requested_capability", &request.requested_capability)?;
    for value in request
        .required_field_families
        .iter()
        .chain(&request.required_semantics)
    {
        validate_identity("capability_semantic", value)?;
    }
    if let Some(evidence) = &request.evidence {
        validate_identity("evidence_adapter_id", &evidence.adapter_id)?;
        validate_identity("evidence_adapter_version", &evidence.adapter_version)?;
        validate_identity("evidence_upstream_id", &evidence.upstream_id)?;
        validate_identity("evidence_upstream_version", &evidence.upstream_version)?;
        validate_identity(
            "evidence_requested_capability",
            &evidence.requested_capability,
        )?;
        for value in evidence
            .observed_field_families
            .iter()
            .chain(&evidence.observed_semantics)
        {
            validate_identity("capability_semantic", value)?;
        }
        validate_evidence_refs(&evidence.evidence_refs)?;
    }

    let evidence_bound = request.evidence.as_ref().is_some_and(|evidence| {
        evidence.schema_version == 1
            && evidence.adapter_id == request.adapter_id
            && evidence.adapter_version == request.adapter_version
            && evidence.upstream_id == request.upstream_id
            && evidence.upstream_version == request.upstream_version
            && evidence.requested_capability == request.requested_capability
            && evidence.integration_seam == request.integration_seam
            && verifier.verifies(evidence)
    });
    let evidence = if evidence_bound {
        request.evidence
    } else {
        None
    };
    let (
        evidence_schema_version,
        surface_present,
        version_compatible,
        observed_field_families,
        observed_semantics,
        evidence_refs,
    ) = match evidence {
        Some(evidence) => (
            Some(evidence.schema_version),
            evidence.surface_present,
            evidence.version_compatible,
            evidence.observed_field_families,
            evidence.observed_semantics,
            evidence.evidence_refs,
        ),
        None => (None, None, None, Vec::new(), Vec::new(), Vec::new()),
    };

    let required_field_families = normalize_capability_values(request.required_field_families);
    let required_semantics = normalize_capability_values(request.required_semantics);
    let observed_field_families = normalize_capability_values(observed_field_families);
    let observed_semantics = normalize_capability_values(observed_semantics);
    let missing_field_families =
        missing_capability_values(&required_field_families, &observed_field_families);
    let missing_semantics = missing_capability_values(&required_semantics, &observed_semantics);
    let semantic_contract_declared =
        !required_field_families.is_empty() || !required_semantics.is_empty();
    let coverage_complete = missing_field_families.is_empty() && missing_semantics.is_empty();
    let status = if evidence_refs.is_empty() {
        CapabilityStatus::Unknown
    } else if version_compatible == Some(false) {
        CapabilityStatus::Incompatible
    } else if surface_present == Some(false) {
        CapabilityStatus::Unsupported
    } else if surface_present == Some(true) && version_compatible == Some(true) {
        if !semantic_contract_declared {
            CapabilityStatus::Unknown
        } else if coverage_complete {
            CapabilityStatus::Supported
        } else {
            CapabilityStatus::Degraded
        }
    } else {
        CapabilityStatus::Unknown
    };

    Ok(CapabilityProbeResult {
        schema_version: 1,
        evidence_schema_version,
        status,
        adapter_id: request.adapter_id,
        adapter_version: request.adapter_version,
        upstream_id: request.upstream_id,
        upstream_version: request.upstream_version,
        requested_capability: request.requested_capability,
        integration_seam: request.integration_seam,
        surface_present,
        version_compatible,
        required_field_families,
        required_semantics,
        observed_field_families,
        observed_semantics,
        missing_field_families,
        missing_semantics,
        evidence_refs,
    })
}

fn normalize_capability_values(mut values: Vec<String>) -> Vec<String> {
    values.sort();
    values.dedup();
    values
}

fn missing_capability_values(required: &[String], observed: &[String]) -> Vec<String> {
    required
        .iter()
        .filter(|required| !observed.contains(required))
        .cloned()
        .collect()
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum RuntimeState {
    Unknown,
    Starting,
    Running,
    Waiting,
    Idle,
    Stopped,
    Failed,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "status", content = "value", rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ProjectionValue<T> {
    Value(T),
    SourceAbsent,
    SourceStale {
        value: T,
        observed_authority_revision: u64,
    },
    Redacted,
    SchemaDowngrade,
    ProjectionOmission,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum RuntimeProjectionField {
    RootAgentId,
    ParentAgentId,
    AgentId,
    TaskId,
    RunId,
    Provider,
    Profile,
    Role,
    RuntimeState,
    LifecycleState,
    LeaseGeneration,
    WaitingOn,
    LastActivity,
    LastTransition,
    AuthorityRevision,
    Environment,
    EvidenceClass,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ProjectionAuthorityScope {
    ObserveOnly,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ProjectionProvenance {
    AcpCanonicalState,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentRuntimeRecord {
    pub task_id: String,
    pub run_id: String,
    pub root_agent_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub parent_agent_id: Option<String>,
    pub agent_id: String,
    pub provider: String,
    pub profile: String,
    pub role: String,
    pub runtime_state: RuntimeState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub waiting_on: Option<String>,
    pub observed_authority_revision: u64,
    pub runtime_event_seq: u64,
    pub environment: String,
    pub evidence_class: String,
    pub actor: String,
    pub evidence_refs: Vec<String>,
    #[serde(with = "time::serde::rfc3339")]
    pub observed_at: OffsetDateTime,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RuntimeObservationRequest {
    pub task_id: String,
    pub run_id: String,
    pub expected_authority_revision: u64,
    pub root_agent_id: String,
    pub parent_agent_id: Option<String>,
    pub agent_id: String,
    pub provider: String,
    pub profile: String,
    pub role: String,
    pub runtime_state: RuntimeState,
    pub waiting_on: Option<String>,
    pub environment: String,
    pub evidence_class: String,
    pub actor: String,
    pub evidence_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RuntimeObservationResult {
    pub outcome: DecisionOutcome,
    pub reason: String,
    pub record: Option<AgentRuntimeRecord>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RuntimeProjectionRequest {
    pub task_id: String,
    pub run_id: String,
    pub agent_id: String,
    pub consumer_schema_version: u32,
    pub redacted_fields: Vec<RuntimeProjectionField>,
    pub omitted_fields: Vec<RuntimeProjectionField>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RuntimeProjectionMetadata {
    pub schema_version: u32,
    pub source_schema_version: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub downgraded_from_schema_version: Option<u32>,
    pub event_seq: u64,
    pub emitter_identity: String,
    pub provenance: ProjectionProvenance,
    pub environment: ProjectionValue<String>,
    pub evidence_class: ProjectionValue<String>,
    pub projection_of_authority_revision: u64,
    pub authority_scope: ProjectionAuthorityScope,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RuntimeProjection {
    pub metadata: RuntimeProjectionMetadata,
    pub root_agent_id: ProjectionValue<String>,
    pub parent_agent_id: ProjectionValue<Option<String>>,
    pub agent_id: ProjectionValue<String>,
    pub task_id: ProjectionValue<String>,
    pub run_id: ProjectionValue<String>,
    pub provider: ProjectionValue<String>,
    pub profile: ProjectionValue<String>,
    pub role: ProjectionValue<String>,
    pub runtime_state: ProjectionValue<RuntimeState>,
    pub lifecycle_state: ProjectionValue<LifecycleState>,
    pub lease_generation: ProjectionValue<u64>,
    pub waiting_on: ProjectionValue<Option<String>>,
    pub last_activity: ProjectionValue<OffsetDateTime>,
    pub last_transition: ProjectionValue<TransitionKind>,
    pub authority_revision: ProjectionValue<u64>,
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
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub active_completion_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub active_handoff_checkpoint_id: Option<String>,
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
    #[serde(default)]
    completions: BTreeMap<String, CompletionBarrierRecord>,
    #[serde(default)]
    runtime_records: Vec<AgentRuntimeRecord>,
    #[serde(default)]
    approval_grants: BTreeMap<String, ApprovalGrant>,
    #[serde(default)]
    approval_consumptions: BTreeMap<String, ApprovalGrantConsumption>,
    #[serde(default)]
    context_checkpoints: BTreeMap<String, ContextCheckpoint>,
    #[serde(default)]
    handoff_checkpoints: BTreeMap<String, HandoffCheckpoint>,
    decisions: Vec<DecisionRecord>,
    next_decision_seq: u64,
}

impl Default for StateFile {
    fn default() -> Self {
        Self {
            schema: SCHEMA.to_owned(),
            tasks: BTreeMap::new(),
            blockers: BTreeMap::new(),
            completions: BTreeMap::new(),
            runtime_records: Vec::new(),
            approval_grants: BTreeMap::new(),
            approval_consumptions: BTreeMap::new(),
            context_checkpoints: BTreeMap::new(),
            handoff_checkpoints: BTreeMap::new(),
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
            active_completion_id: None,
            active_handoff_checkpoint_id: None,
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

    pub fn context_checkpoint(
        &self,
        checkpoint_id: &str,
    ) -> Result<Option<ContextCheckpoint>, GovernanceError> {
        validate_identity("checkpoint_id", checkpoint_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state.context_checkpoints.get(checkpoint_id).cloned())
    }

    pub fn handoff_checkpoint(
        &self,
        checkpoint_id: &str,
    ) -> Result<Option<HandoffCheckpoint>, GovernanceError> {
        validate_identity("checkpoint_id", checkpoint_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state.handoff_checkpoints.get(checkpoint_id).cloned())
    }

    pub fn rotate_context(
        &self,
        request: ContextRotationRequest,
        port: &dyn MemoryPort,
        verifier: &dyn MemoryDurabilityEvidenceVerifier,
    ) -> Result<ContextRotationResult, GovernanceError> {
        validate_context_rotation_request(&request)?;
        let authority = {
            let state = self.state.lock().expect("governance state poisoned");
            state.tasks.get(&request.task_id).cloned()
        };
        if let Some((outcome, reason)) = context_rotation_authority_failure(
            authority.as_ref(),
            &request,
            request.expected_authority_revision,
        ) {
            return self.record_context_rotation_rejection(&request, outcome, reason, Vec::new());
        }

        let retain_probe = port.probe("memory.retain_durable");
        validate_memory_port_probe(&retain_probe)?;
        if let Some((outcome, reason)) =
            memory_probe_failure(&retain_probe, "memory.retain_durable")
        {
            return self.record_context_rotation_rejection(
                &request,
                outcome,
                reason,
                retain_probe.evidence_refs,
            );
        }
        let hydrate_probe = port.probe("memory.hydrate");
        validate_memory_port_probe(&hydrate_probe)?;
        if let Some((outcome, reason)) = memory_probe_failure(&hydrate_probe, "memory.hydrate") {
            return self.record_context_rotation_rejection(
                &request,
                outcome,
                reason,
                hydrate_probe.evidence_refs,
            );
        }
        if !same_memory_provider(&retain_probe.capability, &hydrate_probe.capability) {
            return self.record_context_rotation_rejection(
                &request,
                DecisionOutcome::Deny,
                "memory_provider_binding_mismatch",
                hydrate_probe.evidence_refs,
            );
        }

        let retain_request = MemoryRetainRequest {
            retain_request_id: request.retain_request_id.clone(),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            lineage_id: request.lineage_id.clone(),
            authority_revision: request.expected_authority_revision,
            content_digest: request.memory_content_digest.clone(),
            fencing_identity: request.fencing_identity.clone(),
            evidence_refs: request.selected_evidence_refs.clone(),
        };
        let retained = evaluate_memory_retain(
            &retain_probe.capability,
            &retain_request,
            port.retain(&retain_request),
            verifier,
        )?;
        if !retained.is_durable {
            return self.record_context_rotation_rejection(
                &request,
                DecisionOutcome::Defer,
                &retained.reason,
                retain_probe.evidence_refs,
            );
        }

        let now = OffsetDateTime::now_utc();
        let checkpoint_id = new_context_checkpoint_id();
        let authority_summary = authority_summary(authority.as_ref().unwrap());
        let checkpoint = ContextCheckpoint {
            checkpoint_id: checkpoint_id.clone(),
            rotation_id: request.rotation_id.clone(),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            lineage_id: request.lineage_id.clone(),
            authority_summary: authority_summary.clone(),
            kanban_history_ref: request.kanban_history_ref.clone(),
            old_context_id: request.old_context_id.clone(),
            new_context_id: request.new_context_id.clone(),
            retain_request_id: request.retain_request_id.clone(),
            retain_operation_id: retained.operation_id.clone().unwrap(),
            capability_status: retained.capability_status,
            retain_capability: Some(retain_probe.capability.clone()),
            hydrate_capability: Some(hydrate_probe.capability.clone()),
            phase: ContextLifecyclePhase::ContextCheckpoint,
            phase_trace: vec![
                ContextLifecyclePhase::PreCompact,
                ContextLifecyclePhase::RetainDurable,
                ContextLifecyclePhase::ContextCheckpoint,
            ],
            memory_evidence_refs: retained.provider_evidence_refs.clone(),
            selected_evidence_refs: request.selected_evidence_refs.clone(),
            created_at: now,
            verified_at: None,
        };
        {
            let mut state = self.state.lock().expect("governance state poisoned");
            let snapshot = state.clone();
            let current = state.tasks.get(&request.task_id);
            if let Some((outcome, reason)) = context_rotation_authority_failure(
                current,
                &request,
                request.expected_authority_revision,
            ) {
                drop(state);
                return self.record_context_rotation_rejection(
                    &request,
                    outcome,
                    reason,
                    retained.provider_evidence_refs,
                );
            }
            state
                .context_checkpoints
                .insert(checkpoint_id.clone(), checkpoint);
            if let Err(error) = save(&self.path, &state) {
                *state = snapshot;
                return Err(error);
            }
        }

        let hydrate_request = MemoryHydrateRequest {
            hydrate_request_id: format!("hydrate-{}", request.rotation_id),
            checkpoint_id: checkpoint_id.clone(),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            lineage_id: request.lineage_id.clone(),
            authority_summary,
            new_context_id: request.new_context_id.clone(),
            memory_query: request.memory_query.clone(),
            selected_evidence_refs: request.selected_evidence_refs.clone(),
        };
        let hydration = port.hydrate(&hydrate_request);
        let (hydration_valid, hydrate_failure) = match validate_memory_hydrate_result(&hydration) {
            Ok(()) => (true, memory_hydrate_failure(&hydrate_request, &hydration)),
            Err(_) => (
                false,
                Some((DecisionOutcome::Deny, "memory_hydrate_invalid")),
            ),
        };
        let completed_at = OffsetDateTime::now_utc();

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let authority_failure = context_rotation_authority_failure(
            authority.as_ref(),
            &request,
            request.expected_authority_revision,
        );
        let failure = authority_failure.or(hydrate_failure);
        let (outcome, reason, performed_at) = match failure {
            Some((outcome, reason)) => (outcome, reason, None),
            None => (
                DecisionOutcome::Allow,
                "context_rotated",
                Some(completed_at),
            ),
        };
        let checkpoint = state.context_checkpoints.get_mut(&checkpoint_id).unwrap();
        if outcome == DecisionOutcome::Allow {
            checkpoint.phase = ContextLifecyclePhase::PostCompactVerify;
            checkpoint.phase_trace.extend([
                ContextLifecyclePhase::NewContext,
                ContextLifecyclePhase::TypedHydrate,
                ContextLifecyclePhase::PostCompactVerify,
            ]);
            checkpoint.verified_at = Some(completed_at);
        } else {
            checkpoint.phase = ContextLifecyclePhase::Failed;
            checkpoint.phase_trace.push(ContextLifecyclePhase::Failed);
        }
        let checkpoint = checkpoint.clone();
        let mut evidence_refs = request.selected_evidence_refs.clone();
        evidence_refs.extend(retain_probe.evidence_refs);
        evidence_refs.extend(hydrate_probe.evidence_refs);
        evidence_refs.extend(retained.provider_evidence_refs);
        if hydration_valid {
            evidence_refs.extend(hydration.evidence_refs.clone());
        }
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.agent_id,
            requested_transition: TransitionKind::RotateContext,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after: authority_before,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
            actor: request.actor,
            evaluated_at: completed_at,
            performed_at,
            fencing_identity: Some(request.fencing_identity),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ContextRotationResult {
            decision,
            checkpoint: Some(checkpoint),
            hydration: (outcome == DecisionOutcome::Allow).then_some(hydration),
        })
    }

    fn record_context_rotation_rejection(
        &self,
        request: &ContextRotationRequest,
        outcome: DecisionOutcome,
        reason: &str,
        mut evidence_refs: Vec<String>,
    ) -> Result<ContextRotationResult, GovernanceError> {
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority_before = state
            .tasks
            .get(&request.task_id)
            .map_or(0, |authority| authority.authority_revision);
        evidence_refs.extend(request.selected_evidence_refs.clone());
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            agent_id: request.agent_id.clone(),
            requested_transition: TransitionKind::RotateContext,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after: authority_before,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
            actor: request.actor.clone(),
            evaluated_at: OffsetDateTime::now_utc(),
            performed_at: None,
            fencing_identity: Some(request.fencing_identity.clone()),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ContextRotationResult {
            decision,
            checkpoint: None,
            hydration: None,
        })
    }

    pub fn begin_handoff(
        &self,
        request: BeginHandoffRequest,
    ) -> Result<HandoffTransitionResult, GovernanceError> {
        validate_begin_handoff_request(&request)?;
        let contract_digest = request.contract.digest();
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let capability_failure = handoff_capability_failure(&request.handoff_capability);
        let mutation_failure = handoff_mutation_failure(
            &request.mutation_receipts,
            request.expected_authority_revision,
            &request.fencing_identity,
        );
        let referenced_context_checkpoint = state
            .context_checkpoints
            .get(&request.context_checkpoint_id)
            .cloned();
        let context_checkpoint = handoff_context_checkpoint(
            &state,
            &request.context_checkpoint_id,
            (&request.task_id, &request.run_id),
            &request.lineage_id,
            request.expected_authority_revision,
            (&request.old_owner_agent_id, &request.fencing_identity),
            &contract_digest,
        )
        .cloned();
        let memory_checkpoint = request
            .contract
            .memory_durability_required
            .then(|| {
                verified_memory_checkpoint(
                    &state,
                    Some(&request.context_checkpoint_id),
                    (&request.task_id, &request.run_id),
                    request.expected_authority_revision,
                    (&request.old_owner_agent_id, &request.fencing_identity),
                    &contract_digest,
                )
                .cloned()
            })
            .flatten();
        let blocker_baseline_matches = authority.as_ref().is_some_and(|authority| match authority
            .active_blocker_id
            .as_deref()
        {
            Some(blocker_id) => state.blockers.get(blocker_id).is_some_and(|blocker| {
                blocker.evidence_baseline == request.blocker_evidence_baseline
            }),
            None => request.blocker_evidence_baseline.is_empty(),
        });
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut checkpoint = None;
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
                (DecisionOutcome::Deny, "handoff_not_available")
            }
            Some(authority)
                if authority.owner_agent_id.as_deref()
                    != Some(request.old_owner_agent_id.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            Some(authority)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            Some(authority) if authority.acceptance_contract_digest != contract_digest => {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            Some(authority) if authority.active_handoff_checkpoint_id.is_some() => {
                (DecisionOutcome::Deny, "handoff_already_active")
            }
            Some(_) if !blocker_baseline_matches => {
                (DecisionOutcome::Defer, "blocker_evidence_baseline_mismatch")
            }
            Some(_) if capability_failure.is_some() => capability_failure.unwrap(),
            Some(_) if mutation_failure.is_some() => mutation_failure.unwrap(),
            Some(_) if referenced_context_checkpoint.is_none() => {
                (DecisionOutcome::Defer, "context_checkpoint_required")
            }
            Some(_) if context_checkpoint.is_none() => {
                (DecisionOutcome::Deny, "context_checkpoint_binding_mismatch")
            }
            Some(_)
                if request.contract.memory_durability_required && memory_checkpoint.is_none() =>
            {
                (DecisionOutcome::Defer, "memory_checkpoint_required")
            }
            Some(authority) => {
                let checkpoint_id = new_handoff_checkpoint_id();
                let old_ownership_generation =
                    lease_generation(&state, &request.task_id, &request.run_id);
                let mut evidence_refs = request.evidence_refs.clone();
                evidence_refs.extend(request.handoff_capability.evidence_refs.clone());
                let context_checkpoint = context_checkpoint.as_ref().unwrap();
                evidence_refs.extend(context_checkpoint.memory_evidence_refs.clone());
                evidence_refs.extend(context_checkpoint.selected_evidence_refs.clone());
                evidence_refs.sort();
                evidence_refs.dedup();
                let active_blocker_id = authority.active_blocker_id.clone();
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Suspending;
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority.active_handoff_checkpoint_id = Some(checkpoint_id.clone());
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                let created = HandoffCheckpoint {
                    checkpoint_id: checkpoint_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    lineage_id: request.lineage_id.clone(),
                    root_agent_id: request.root_agent_id.clone(),
                    parent_agent_id: request.parent_agent_id.clone(),
                    old_owner_agent_id: request.old_owner_agent_id.clone(),
                    replacement_agent_id: request.replacement_agent_id.clone(),
                    acceptance_contract_digest: contract_digest.clone(),
                    memory_durability_required: request.contract.memory_durability_required,
                    source_authority_revision: request.expected_authority_revision,
                    suspending_authority_revision: authority_after,
                    suspended_authority_revision: None,
                    resuming_authority_revision: None,
                    resumed_authority_revision: None,
                    old_ownership_generation,
                    new_ownership_generation: None,
                    old_fencing_identity: request.fencing_identity.clone(),
                    new_fencing_identity: None,
                    active_blocker_id,
                    blocker_evidence_baseline: request.blocker_evidence_baseline.clone(),
                    pending_consequential_mutation_ids: request
                        .pending_consequential_mutation_ids
                        .clone(),
                    mutation_receipts: request.mutation_receipts.clone(),
                    context_checkpoint_id: request.context_checkpoint_id.clone(),
                    memory_checkpoint_id: request
                        .contract
                        .memory_durability_required
                        .then(|| request.context_checkpoint_id.clone()),
                    handoff_capability: request.handoff_capability.clone(),
                    evidence_refs,
                    state: HandoffCheckpointState::Suspending,
                    created_at: now,
                    released_at: None,
                    resumed_at: None,
                };
                state
                    .handoff_checkpoints
                    .insert(checkpoint_id, created.clone());
                checkpoint = Some(created);
                (DecisionOutcome::Allow, "handoff_suspending")
            }
        };
        let mut evidence_refs = request.evidence_refs;
        evidence_refs.extend(request.handoff_capability.evidence_refs);
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.old_owner_agent_id,
            requested_transition: TransitionKind::BeginHandoff,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
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
        Ok(HandoffTransitionResult {
            decision,
            checkpoint,
        })
    }

    pub fn suspend_handoff(
        &self,
        request: SuspendHandoffRequest,
    ) -> Result<HandoffTransitionResult, GovernanceError> {
        validate_suspend_handoff_request(&request)?;
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let checkpoint = state
            .handoff_checkpoints
            .get(&request.checkpoint_id)
            .cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let capability_failure = handoff_capability_failure(&request.handoff_capability);
        let context_checkpoint_verified = checkpoint.as_ref().is_some_and(|checkpoint| {
            handoff_context_checkpoint(
                &state,
                &checkpoint.context_checkpoint_id,
                (&request.task_id, &request.run_id),
                &checkpoint.lineage_id,
                checkpoint.source_authority_revision,
                (
                    &checkpoint.old_owner_agent_id,
                    &checkpoint.old_fencing_identity,
                ),
                &checkpoint.acceptance_contract_digest,
            )
            .is_some()
        });
        let memory_checkpoint_verified = checkpoint.as_ref().is_some_and(|checkpoint| {
            !checkpoint.memory_durability_required
                || checkpoint
                    .memory_checkpoint_id
                    .as_deref()
                    .is_some_and(|checkpoint_id| {
                        verified_memory_checkpoint(
                            &state,
                            Some(checkpoint_id),
                            (&request.task_id, &request.run_id),
                            checkpoint.source_authority_revision,
                            (
                                &checkpoint.old_owner_agent_id,
                                &checkpoint.old_fencing_identity,
                            ),
                            &checkpoint.acceptance_contract_digest,
                        )
                        .is_some()
                    })
        });
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut resulting_checkpoint = checkpoint.clone();
        let (outcome, reason) = match (authority.as_ref(), checkpoint.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Suspending => {
                (DecisionOutcome::Deny, "suspend_not_available")
            }
            (Some(authority), _)
                if authority.active_handoff_checkpoint_id.as_deref()
                    != Some(request.checkpoint_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_handoff_checkpoint")
            }
            (_, None) => (DecisionOutcome::Defer, "stale_handoff_checkpoint"),
            (_, Some(checkpoint))
                if checkpoint.task_id != request.task_id || checkpoint.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "handoff_scope_mismatch")
            }
            (_, Some(checkpoint))
                if checkpoint.state != HandoffCheckpointState::Suspending
                    || checkpoint.suspending_authority_revision
                        != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_handoff_checkpoint")
            }
            (_, Some(_)) if capability_failure.is_some() => capability_failure.unwrap(),
            (_, Some(checkpoint))
                if !same_capability_source(
                    &checkpoint.handoff_capability,
                    &request.handoff_capability,
                ) =>
            {
                (DecisionOutcome::Deny, "handoff_capability_binding_mismatch")
            }
            (Some(authority), Some(checkpoint))
                if authority.acceptance_contract_digest
                    != checkpoint.acceptance_contract_digest =>
            {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            (Some(authority), Some(checkpoint))
                if authority.owner_agent_id.as_deref()
                    != Some(request.old_owner_agent_id.as_str())
                    || checkpoint.old_owner_agent_id != request.old_owner_agent_id =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            (Some(authority), Some(checkpoint))
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str())
                    || checkpoint.old_fencing_identity != request.fencing_identity =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            (_, Some(checkpoint))
                if handoff_capability_failure(&checkpoint.handoff_capability).is_some() =>
            {
                handoff_capability_failure(&checkpoint.handoff_capability).unwrap()
            }
            _ if !context_checkpoint_verified => {
                (DecisionOutcome::Deny, "context_checkpoint_binding_mismatch")
            }
            _ if !memory_checkpoint_verified => {
                (DecisionOutcome::Defer, "memory_checkpoint_required")
            }
            _ => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Suspended;
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority.owner_agent_id = None;
                authority.fencing_identity = None;
                authority_after = authority.authority_revision;
                let checkpoint = state
                    .handoff_checkpoints
                    .get_mut(&request.checkpoint_id)
                    .unwrap();
                checkpoint.state = HandoffCheckpointState::Suspended;
                checkpoint.suspended_authority_revision = Some(authority_after);
                checkpoint.released_at = Some(now);
                resulting_checkpoint = Some(checkpoint.clone());
                performed_at = Some(now);
                (DecisionOutcome::Allow, "handoff_suspended")
            }
        };
        let mut evidence_refs = request.evidence_refs;
        evidence_refs.extend(request.handoff_capability.evidence_refs);
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.old_owner_agent_id,
            requested_transition: TransitionKind::SuspendHandoff,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
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
        Ok(HandoffTransitionResult {
            decision,
            checkpoint: resulting_checkpoint,
        })
    }

    pub fn acquire_handoff(
        &self,
        request: AcquireHandoffRequest,
    ) -> Result<AcquireHandoffResult, GovernanceError> {
        validate_acquire_handoff_request(&request)?;
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let checkpoint = state
            .handoff_checkpoints
            .get(&request.checkpoint_id)
            .cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let current_lease_generation = lease_generation(&state, &request.task_id, &request.run_id);
        let capability_failure = handoff_capability_failure(&request.handoff_capability);
        let context_checkpoint_verified = checkpoint.as_ref().is_some_and(|checkpoint| {
            handoff_context_checkpoint(
                &state,
                &checkpoint.context_checkpoint_id,
                (&request.task_id, &request.run_id),
                &checkpoint.lineage_id,
                checkpoint.source_authority_revision,
                (
                    &checkpoint.old_owner_agent_id,
                    &checkpoint.old_fencing_identity,
                ),
                &checkpoint.acceptance_contract_digest,
            )
            .is_some()
        });
        let memory_checkpoint_verified = checkpoint.as_ref().is_some_and(|checkpoint| {
            !checkpoint.memory_durability_required
                || checkpoint
                    .memory_checkpoint_id
                    .as_deref()
                    .is_some_and(|checkpoint_id| {
                        verified_memory_checkpoint(
                            &state,
                            Some(checkpoint_id),
                            (&request.task_id, &request.run_id),
                            checkpoint.source_authority_revision,
                            (
                                &checkpoint.old_owner_agent_id,
                                &checkpoint.old_fencing_identity,
                            ),
                            &checkpoint.acceptance_contract_digest,
                        )
                        .is_some()
                    })
        });
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut resulting_checkpoint = checkpoint.clone();
        let mut resulting_fence = None;
        let (outcome, reason) = match (authority.as_ref(), checkpoint.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Suspended => {
                (DecisionOutcome::Deny, "acquire_handoff_not_available")
            }
            (Some(authority), _)
                if authority.owner_agent_id.is_some() || authority.fencing_identity.is_some() =>
            {
                (DecisionOutcome::Deny, "old_lease_not_released")
            }
            (Some(authority), _)
                if authority.active_handoff_checkpoint_id.as_deref()
                    != Some(request.checkpoint_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_handoff_checkpoint")
            }
            (_, None) => (DecisionOutcome::Defer, "stale_handoff_checkpoint"),
            (_, Some(checkpoint))
                if checkpoint.task_id != request.task_id || checkpoint.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "handoff_scope_mismatch")
            }
            (_, Some(checkpoint))
                if checkpoint.state != HandoffCheckpointState::Suspended
                    || checkpoint.suspended_authority_revision
                        != Some(request.expected_authority_revision)
                    || checkpoint.released_at.is_none() =>
            {
                (DecisionOutcome::Defer, "stale_handoff_checkpoint")
            }
            (Some(authority), Some(checkpoint))
                if authority.acceptance_contract_digest
                    != checkpoint.acceptance_contract_digest =>
            {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            (_, Some(checkpoint))
                if checkpoint.replacement_agent_id != request.replacement_agent_id =>
            {
                (DecisionOutcome::Deny, "replacement_mismatch")
            }
            (_, Some(checkpoint))
                if checkpoint.old_ownership_generation != current_lease_generation =>
            {
                (DecisionOutcome::Defer, "lease_generation_mismatch")
            }
            (_, Some(_)) if capability_failure.is_some() => capability_failure.unwrap(),
            (_, Some(checkpoint))
                if !same_capability_source(
                    &checkpoint.handoff_capability,
                    &request.handoff_capability,
                ) =>
            {
                (DecisionOutcome::Deny, "handoff_capability_binding_mismatch")
            }
            (_, Some(checkpoint))
                if handoff_capability_failure(&checkpoint.handoff_capability).is_some() =>
            {
                handoff_capability_failure(&checkpoint.handoff_capability).unwrap()
            }
            _ if !context_checkpoint_verified => {
                (DecisionOutcome::Deny, "context_checkpoint_binding_mismatch")
            }
            _ if !memory_checkpoint_verified => {
                (DecisionOutcome::Defer, "memory_checkpoint_required")
            }
            _ => {
                let fence = new_fencing_identity();
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Resuming;
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority.owner_agent_id = Some(request.replacement_agent_id.clone());
                authority.fencing_identity = Some(fence.clone());
                authority_after = authority.authority_revision;
                let checkpoint = state
                    .handoff_checkpoints
                    .get_mut(&request.checkpoint_id)
                    .unwrap();
                checkpoint.state = HandoffCheckpointState::Resuming;
                checkpoint.resuming_authority_revision = Some(authority_after);
                checkpoint.new_ownership_generation =
                    Some(current_lease_generation.saturating_add(1));
                checkpoint.new_fencing_identity = Some(fence.clone());
                resulting_checkpoint = Some(checkpoint.clone());
                resulting_fence = Some(fence);
                performed_at = Some(now);
                (DecisionOutcome::Allow, "handoff_acquired")
            }
        };
        let mut evidence_refs = request.evidence_refs;
        evidence_refs.extend(request.handoff_capability.evidence_refs);
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.replacement_agent_id,
            requested_transition: TransitionKind::AcquireHandoff,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
            actor: request.actor,
            evaluated_at: now,
            performed_at,
            fencing_identity: resulting_fence.clone(),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(AcquireHandoffResult {
            decision,
            checkpoint: resulting_checkpoint,
            fencing_identity: resulting_fence,
        })
    }

    pub fn resume_handoff(
        &self,
        request: ResumeHandoffRequest,
        port: &dyn MemoryPort,
    ) -> Result<ResumeHandoffResult, GovernanceError> {
        validate_resume_handoff_request(&request)?;
        let (authority, checkpoint, context_checkpoint) = {
            let state = self.state.lock().expect("governance state poisoned");
            if let Some((outcome, reason)) = resume_handoff_state_failure(&state, &request) {
                drop(state);
                return self.record_resume_handoff_rejection(&request, outcome, reason, Vec::new());
            }
            let authority = state.tasks.get(&request.task_id).cloned().unwrap();
            let checkpoint = state
                .handoff_checkpoints
                .get(&request.checkpoint_id)
                .cloned()
                .unwrap();
            let context_checkpoint = state
                .context_checkpoints
                .get(&checkpoint.context_checkpoint_id)
                .cloned()
                .unwrap();
            (authority, checkpoint, context_checkpoint)
        };

        let hydrate_probe = port.probe("memory.hydrate");
        validate_memory_port_probe(&hydrate_probe)?;
        if let Some((outcome, reason)) = memory_probe_failure(&hydrate_probe, "memory.hydrate") {
            return self.record_resume_handoff_rejection(
                &request,
                outcome,
                reason,
                hydrate_probe.evidence_refs,
            );
        }
        let expected_hydrate_capability = context_checkpoint.hydrate_capability.as_ref().unwrap();
        if !same_capability_source(expected_hydrate_capability, &hydrate_probe.capability) {
            let mut evidence_refs = hydrate_probe.evidence_refs;
            evidence_refs.extend(hydrate_probe.capability.evidence_refs);
            evidence_refs.extend(expected_hydrate_capability.evidence_refs.clone());
            return self.record_resume_handoff_rejection(
                &request,
                DecisionOutcome::Deny,
                "memory_provider_binding_mismatch",
                evidence_refs,
            );
        }

        let hydrate_request = MemoryHydrateRequest {
            hydrate_request_id: format!("handoff-hydrate-{}", checkpoint.checkpoint_id),
            checkpoint_id: context_checkpoint.checkpoint_id,
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            lineage_id: checkpoint.lineage_id,
            authority_summary: authority_summary(&authority),
            new_context_id: request.new_context_id.clone(),
            memory_query: request.memory_query.clone(),
            selected_evidence_refs: context_checkpoint.selected_evidence_refs,
        };
        let hydration = port.hydrate(&hydrate_request);
        let (hydration_valid, hydrate_failure) = match validate_memory_hydrate_result(&hydration) {
            Ok(()) => (true, memory_hydrate_failure(&hydrate_request, &hydration)),
            Err(_) => (
                false,
                Some((DecisionOutcome::Deny, "memory_hydrate_invalid")),
            ),
        };
        let completed_at = OffsetDateTime::now_utc();

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority_before = state
            .tasks
            .get(&request.task_id)
            .map_or(0, |authority| authority.authority_revision);
        let failure = resume_handoff_state_failure(&state, &request).or(hydrate_failure);
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut resulting_checkpoint = state
            .handoff_checkpoints
            .get(&request.checkpoint_id)
            .cloned();
        let (outcome, reason) = match failure {
            Some((outcome, reason)) => (outcome, reason),
            None => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Running;
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority.active_handoff_checkpoint_id = None;
                authority_after = authority.authority_revision;
                let checkpoint = state
                    .handoff_checkpoints
                    .get_mut(&request.checkpoint_id)
                    .unwrap();
                checkpoint.state = HandoffCheckpointState::Resumed;
                checkpoint.resumed_authority_revision = Some(authority_after);
                checkpoint.resumed_at = Some(completed_at);
                resulting_checkpoint = Some(checkpoint.clone());
                performed_at = Some(completed_at);
                (DecisionOutcome::Allow, "handoff_resumed")
            }
        };
        let mut evidence_refs = request.evidence_refs;
        evidence_refs.extend(request.handoff_capability.evidence_refs);
        evidence_refs.extend(hydrate_probe.evidence_refs);
        evidence_refs.extend(hydrate_probe.capability.evidence_refs);
        if hydration_valid {
            evidence_refs.extend(hydration.evidence_refs.clone());
        }
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.replacement_agent_id,
            requested_transition: TransitionKind::ResumeHandoff,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
            actor: request.actor,
            evaluated_at: completed_at,
            performed_at,
            fencing_identity: Some(request.fencing_identity),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ResumeHandoffResult {
            decision,
            checkpoint: resulting_checkpoint,
            hydration: (outcome == DecisionOutcome::Allow).then_some(hydration),
        })
    }

    fn record_resume_handoff_rejection(
        &self,
        request: &ResumeHandoffRequest,
        outcome: DecisionOutcome,
        reason: &str,
        mut evidence_refs: Vec<String>,
    ) -> Result<ResumeHandoffResult, GovernanceError> {
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority_before = state
            .tasks
            .get(&request.task_id)
            .map_or(0, |authority| authority.authority_revision);
        let (outcome, reason) =
            resume_handoff_state_failure(&state, request).unwrap_or((outcome, reason));
        evidence_refs.extend(request.evidence_refs.clone());
        evidence_refs.extend(request.handoff_capability.evidence_refs.clone());
        evidence_refs.sort();
        evidence_refs.dedup();
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            agent_id: request.replacement_agent_id.clone(),
            requested_transition: TransitionKind::ResumeHandoff,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after: authority_before,
            policy_version: BASE_POLICY_VERSION.to_owned(),
            evaluator_version: BASE_EVALUATOR_VERSION.to_owned(),
            evidence_refs,
            actor: request.actor.clone(),
            evaluated_at: OffsetDateTime::now_utc(),
            performed_at: None,
            fencing_identity: Some(request.fencing_identity.clone()),
            supersedes_decision_id: None,
        };
        state.decisions.push(decision.clone());
        let checkpoint = state
            .handoff_checkpoints
            .get(&request.checkpoint_id)
            .cloned();
        if let Err(error) = save(&self.path, &state) {
            *state = snapshot;
            return Err(error);
        }
        Ok(ResumeHandoffResult {
            decision,
            checkpoint,
            hydration: None,
        })
    }

    pub fn approval_grant(
        &self,
        approval_id: &str,
    ) -> Result<Option<ApprovalGrant>, GovernanceError> {
        validate_identity("approval_id", approval_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state.approval_grants.get(approval_id).cloned())
    }

    pub fn approval_grant_consumptions(
        &self,
        approval_id: &str,
    ) -> Result<Vec<ApprovalGrantConsumption>, GovernanceError> {
        validate_identity("approval_id", approval_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        Ok(state
            .approval_consumptions
            .values()
            .filter(|consumption| consumption.approval_id == approval_id)
            .cloned()
            .collect())
    }

    pub fn issue_approval_grant(
        &self,
        request: IssueApprovalGrantRequest,
    ) -> Result<IssueApprovalGrantResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("approved_actor", &request.approved_actor)?;
        validate_identity("issued_by", &request.issued_by)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_policy_evaluation_request(&request.evaluation)?;
        if request.max_uses == 0 {
            return Err(GovernanceError::InvalidIdentity("max_uses"));
        }

        let evaluation = evaluate_policy(request.evaluation);
        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let decision_id = next_decision_id(&mut state);
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut grant = None;
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
            Some(_) if evaluation.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            Some(authority) if authority.lifecycle_state != LifecycleState::Running => {
                (DecisionOutcome::Deny, "approval_not_available")
            }
            Some(authority)
                if authority.owner_agent_id.as_deref() != Some(request.approved_actor.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            Some(authority)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            Some(_) if request.expires_at <= now => (DecisionOutcome::Deny, "approval_expired"),
            Some(_) if evaluation.outcome == ApprovalOutcome::Deny => {
                (DecisionOutcome::Deny, evaluation.reason.as_str())
            }
            Some(_) if evaluation.outcome == ApprovalOutcome::RequireUserApproval => {
                (DecisionOutcome::RequireApproval, evaluation.reason.as_str())
            }
            Some(_)
                if matches!(
                    evaluation.outcome,
                    ApprovalOutcome::Timeout | ApprovalOutcome::Abort
                ) =>
            {
                (DecisionOutcome::Defer, evaluation.reason.as_str())
            }
            Some(_)
                if matches!(
                    evaluation.governing_layer,
                    Some(PolicyLayer::UserPreference | PolicyLayer::AgentIntent)
                ) =>
            {
                (DecisionOutcome::Deny, "approval_authority_not_policy")
            }
            Some(_) if evaluation.exception_id.is_none() => {
                (DecisionOutcome::Deny, "approval_exception_required")
            }
            Some(_) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                let approval = ApprovalGrant {
                    approval_id: new_approval_id(),
                    actor: request.approved_actor.clone(),
                    policy_layer: evaluation.governing_layer.unwrap(),
                    policy_id: evaluation.governing_policy_id.clone().unwrap(),
                    exception_id: evaluation.exception_id.clone().unwrap(),
                    permitted_action: evaluation.requested_action.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    target_scope: evaluation.target_scope.clone(),
                    authority_revision: authority_after,
                    issued_at: now,
                    expires_at: request.expires_at,
                    max_uses: request.max_uses,
                    consumed_uses: 0,
                    revoked_at: None,
                    fencing_identity: request.fencing_identity.clone(),
                    policy_version: evaluation.policy_version.clone(),
                    evaluator_version: evaluation.evaluator_version.clone(),
                    evidence_refs: evaluation.evidence_refs.clone(),
                    issuance_decision_id: decision_id.clone(),
                };
                state
                    .approval_grants
                    .insert(approval.approval_id.clone(), approval.clone());
                grant = Some(approval);
                (DecisionOutcome::Allow, "approval_grant_issued")
            }
        };
        let decision = DecisionRecord {
            decision_id,
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.approved_actor,
            requested_transition: TransitionKind::IssueApprovalGrant,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: evaluation.policy_version,
            evaluator_version: evaluation.evaluator_version,
            evidence_refs: evaluation.evidence_refs,
            actor: request.issued_by,
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
        Ok(IssueApprovalGrantResult { decision, grant })
    }

    pub fn consume_approval_grant(
        &self,
        request: ConsumeApprovalGrantRequest,
    ) -> Result<ConsumeApprovalGrantResult, GovernanceError> {
        self.consume_approval_grant_at(request, OffsetDateTime::now_utc())
    }

    fn consume_approval_grant_at(
        &self,
        request: ConsumeApprovalGrantRequest,
        now: OffsetDateTime,
    ) -> Result<ConsumeApprovalGrantResult, GovernanceError> {
        validate_identity("approval_id", &request.approval_id)?;
        validate_identity("consumption_id", &request.consumption_id)?;
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("permitted_action", &request.permitted_action)?;
        validate_identity("target_scope", &request.target_scope)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let approval = state.approval_grants.get(&request.approval_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let decision_id = next_decision_id(&mut state);
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut grant = approval.clone();
        let mut consumption = None;
        let (outcome, reason) = match (authority.as_ref(), approval.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Running => {
                (DecisionOutcome::Deny, "approval_not_available")
            }
            (Some(authority), _)
                if authority.owner_agent_id.as_deref() != Some(request.actor.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            (Some(authority), _)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            (Some(_), _)
                if state
                    .approval_consumptions
                    .contains_key(&request.consumption_id) =>
            {
                (DecisionOutcome::Deny, "approval_replayed")
            }
            (Some(_), None) => (DecisionOutcome::Deny, "approval_not_found"),
            (Some(_), Some(approval))
                if approval.task_id != request.task_id || approval.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "approval_task_scope_mismatch")
            }
            (Some(_), Some(approval)) if approval.actor != request.actor => {
                (DecisionOutcome::Deny, "approval_actor_mismatch")
            }
            (Some(_), Some(approval)) if approval.permitted_action != request.permitted_action => {
                (DecisionOutcome::Deny, "approval_action_mismatch")
            }
            (Some(_), Some(approval)) if approval.target_scope != request.target_scope => {
                (DecisionOutcome::Deny, "approval_scope_mismatch")
            }
            (Some(_), Some(approval))
                if approval.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "approval_revision_mismatch")
            }
            (Some(_), Some(approval)) if approval.fencing_identity != request.fencing_identity => {
                (DecisionOutcome::Deny, "approval_fencing_mismatch")
            }
            (Some(_), Some(approval)) if approval.revoked_at.is_some() => {
                (DecisionOutcome::Deny, "approval_revoked")
            }
            (Some(_), Some(approval)) if approval.expires_at <= now => {
                (DecisionOutcome::Deny, "approval_expired")
            }
            (Some(_), Some(approval)) if approval.consumed_uses >= approval.max_uses => {
                (DecisionOutcome::Deny, "approval_exhausted")
            }
            (Some(_), Some(_)) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority_after = authority.authority_revision;
                let approval = state.approval_grants.get_mut(&request.approval_id).unwrap();
                approval.consumed_uses = approval.consumed_uses.saturating_add(1);
                approval.authority_revision = authority_after;
                grant = Some(approval.clone());
                let consumed = ApprovalGrantConsumption {
                    consumption_id: request.consumption_id.clone(),
                    approval_id: request.approval_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    actor: request.actor.clone(),
                    permitted_action: request.permitted_action.clone(),
                    target_scope: request.target_scope.clone(),
                    authority_revision: authority_after,
                    fencing_identity: request.fencing_identity.clone(),
                    evidence_refs: request.evidence_refs.clone(),
                    consumed_at: now,
                    decision_id: decision_id.clone(),
                };
                state
                    .approval_consumptions
                    .insert(consumed.consumption_id.clone(), consumed.clone());
                consumption = Some(consumed);
                performed_at = Some(now);
                (DecisionOutcome::Allow, "approval_grant_consumed")
            }
        };
        let decision = DecisionRecord {
            decision_id,
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.actor.clone(),
            requested_transition: TransitionKind::ConsumeApprovalGrant,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: approval
                .as_ref()
                .map_or(BASE_POLICY_VERSION, |grant| grant.policy_version.as_str())
                .to_owned(),
            evaluator_version: approval
                .as_ref()
                .map_or(BASE_EVALUATOR_VERSION, |grant| {
                    grant.evaluator_version.as_str()
                })
                .to_owned(),
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
        Ok(ConsumeApprovalGrantResult {
            decision,
            grant,
            consumption,
        })
    }

    pub fn revoke_approval_grant(
        &self,
        request: RevokeApprovalGrantRequest,
    ) -> Result<RevokeApprovalGrantResult, GovernanceError> {
        validate_identity("approval_id", &request.approval_id)?;
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let approval = state.approval_grants.get(&request.approval_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut grant = approval.clone();
        let (outcome, reason) = match (authority.as_ref(), approval.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Running => {
                (DecisionOutcome::Deny, "approval_not_available")
            }
            (Some(authority), _)
                if authority.owner_agent_id.as_deref() != Some(request.actor.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            (Some(authority), _)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            (Some(_), None) => (DecisionOutcome::Deny, "approval_not_found"),
            (Some(_), Some(approval))
                if approval.task_id != request.task_id || approval.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "approval_task_scope_mismatch")
            }
            (Some(_), Some(approval)) if approval.actor != request.actor => {
                (DecisionOutcome::Deny, "approval_actor_mismatch")
            }
            (Some(_), Some(approval))
                if approval.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "approval_revision_mismatch")
            }
            (Some(_), Some(approval)) if approval.fencing_identity != request.fencing_identity => {
                (DecisionOutcome::Deny, "approval_fencing_mismatch")
            }
            (Some(_), Some(approval)) if approval.revoked_at.is_some() => {
                (DecisionOutcome::Deny, "approval_already_revoked")
            }
            (Some(_), Some(_)) => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.authority_revision = authority.authority_revision.saturating_add(1);
                authority_after = authority.authority_revision;
                let approval = state.approval_grants.get_mut(&request.approval_id).unwrap();
                approval.revoked_at = Some(now);
                approval.authority_revision = authority_after;
                grant = Some(approval.clone());
                performed_at = Some(now);
                (DecisionOutcome::Allow, "approval_grant_revoked")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.actor.clone(),
            requested_transition: TransitionKind::RevokeApprovalGrant,
            outcome,
            reason: reason.to_owned(),
            authority_before,
            authority_after,
            policy_version: approval
                .as_ref()
                .map_or(BASE_POLICY_VERSION, |grant| grant.policy_version.as_str())
                .to_owned(),
            evaluator_version: approval
                .as_ref()
                .map_or(BASE_EVALUATOR_VERSION, |grant| {
                    grant.evaluator_version.as_str()
                })
                .to_owned(),
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
        Ok(RevokeApprovalGrantResult { decision, grant })
    }

    pub fn record_runtime_observation(
        &self,
        request: RuntimeObservationRequest,
    ) -> Result<RuntimeObservationResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("root_agent_id", &request.root_agent_id)?;
        if let Some(parent_agent_id) = &request.parent_agent_id {
            validate_identity("parent_agent_id", parent_agent_id)?;
        }
        validate_identity("agent_id", &request.agent_id)?;
        validate_identity("provider", &request.provider)?;
        validate_identity("profile", &request.profile)?;
        validate_identity("role", &request.role)?;
        if let Some(waiting_on) = &request.waiting_on {
            validate_identity("waiting_on", waiting_on)?;
        }
        validate_identity("environment", &request.environment)?;
        validate_identity("evidence_class", &request.evidence_class)?;
        validate_identity("actor", &request.actor)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let mut record = None;
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
                if request.runtime_state == RuntimeState::Waiting
                    && request.waiting_on.is_none() =>
            {
                (DecisionOutcome::Deny, "waiting_reason_required")
            }
            Some(_)
                if request.runtime_state != RuntimeState::Waiting
                    && request.waiting_on.is_some() =>
            {
                (DecisionOutcome::Deny, "waiting_reason_not_allowed")
            }
            Some(_) => {
                let now = OffsetDateTime::now_utc();
                let existing = state.runtime_records.iter().position(|candidate| {
                    candidate.task_id == request.task_id
                        && candidate.run_id == request.run_id
                        && candidate.agent_id == request.agent_id
                });
                match existing {
                    Some(index) => {
                        let candidate = &state.runtime_records[index];
                        if candidate.root_agent_id != request.root_agent_id
                            || candidate.parent_agent_id != request.parent_agent_id
                            || candidate.provider != request.provider
                            || candidate.profile != request.profile
                            || candidate.role != request.role
                        {
                            (DecisionOutcome::Deny, "runtime_identity_mismatch")
                        } else {
                            let candidate = &mut state.runtime_records[index];
                            candidate.runtime_state = request.runtime_state;
                            candidate.waiting_on = request.waiting_on.clone();
                            candidate.observed_authority_revision =
                                request.expected_authority_revision;
                            candidate.runtime_event_seq =
                                candidate.runtime_event_seq.saturating_add(1);
                            candidate.environment = request.environment.clone();
                            candidate.evidence_class = request.evidence_class.clone();
                            candidate.actor = request.actor.clone();
                            candidate.evidence_refs = request.evidence_refs.clone();
                            candidate.observed_at = now;
                            record = Some(candidate.clone());
                            (DecisionOutcome::Allow, "runtime_observed")
                        }
                    }
                    None => {
                        let created = AgentRuntimeRecord {
                            task_id: request.task_id.clone(),
                            run_id: request.run_id.clone(),
                            root_agent_id: request.root_agent_id.clone(),
                            parent_agent_id: request.parent_agent_id.clone(),
                            agent_id: request.agent_id.clone(),
                            provider: request.provider.clone(),
                            profile: request.profile.clone(),
                            role: request.role.clone(),
                            runtime_state: request.runtime_state,
                            waiting_on: request.waiting_on.clone(),
                            observed_authority_revision: request.expected_authority_revision,
                            runtime_event_seq: 1,
                            environment: request.environment.clone(),
                            evidence_class: request.evidence_class.clone(),
                            actor: request.actor.clone(),
                            evidence_refs: request.evidence_refs.clone(),
                            observed_at: now,
                        };
                        state.runtime_records.push(created.clone());
                        record = Some(created);
                        (DecisionOutcome::Allow, "runtime_observed")
                    }
                }
            }
        };
        if outcome == DecisionOutcome::Allow
            && let Err(error) = save(&self.path, &state)
        {
            *state = snapshot;
            return Err(error);
        }
        Ok(RuntimeObservationResult {
            outcome,
            reason: reason.to_owned(),
            record,
        })
    }

    pub fn runtime_projection(
        &self,
        request: RuntimeProjectionRequest,
    ) -> Result<Option<RuntimeProjection>, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("agent_id", &request.agent_id)?;
        if request.consumer_schema_version > 1 {
            return Err(GovernanceError::InvalidIdentity("consumer_schema_version"));
        }

        let state = self.state.lock().expect("governance state poisoned");
        let Some(authority) = state.tasks.get(&request.task_id) else {
            return Ok(None);
        };
        if authority.active_run_id != request.run_id {
            return Ok(None);
        }
        let record = state.runtime_records.iter().find(|record| {
            record.task_id == request.task_id
                && record.run_id == request.run_id
                && record.agent_id == request.agent_id
        });
        let task_decisions = state
            .decisions
            .iter()
            .filter(|decision| {
                decision.task_id == request.task_id && decision.run_id == request.run_id
            })
            .collect::<Vec<_>>();
        let lease_generation = lease_generation(&state, &request.task_id, &request.run_id);
        let last_transition = task_decisions.iter().rev().find_map(|decision| {
            (decision.performed_at.is_some()
                && decision.authority_after > decision.authority_before)
                .then_some(decision.requested_transition)
        });
        let last_decision_activity = task_decisions
            .iter()
            .map(|decision| decision.evaluated_at)
            .max();
        let last_activity = match (
            last_decision_activity,
            record.map(|record| record.observed_at),
        ) {
            (Some(left), Some(right)) => Some(left.max(right)),
            (Some(value), None) | (None, Some(value)) => Some(value),
            (None, None) => None,
        };
        let runtime_event_seq = state
            .runtime_records
            .iter()
            .filter(|record| record.task_id == request.task_id && record.run_id == request.run_id)
            .fold(0_u64, |total, record| {
                total.saturating_add(record.runtime_event_seq)
            });
        let event_seq = (task_decisions.len() as u64).saturating_add(runtime_event_seq);

        let schema_version = request.consumer_schema_version;
        Ok(Some(RuntimeProjection {
            metadata: RuntimeProjectionMetadata {
                schema_version,
                source_schema_version: 1,
                downgraded_from_schema_version: (schema_version < 1).then_some(1),
                event_seq,
                emitter_identity: "m365-ai-gateway/acp-governance".to_owned(),
                provenance: ProjectionProvenance::AcpCanonicalState,
                environment: reduce_runtime_projection_value(
                    &request,
                    RuntimeProjectionField::Environment,
                    runtime_projection_value(record, authority.authority_revision, |record| {
                        record.environment.clone()
                    }),
                ),
                evidence_class: reduce_runtime_projection_value(
                    &request,
                    RuntimeProjectionField::EvidenceClass,
                    runtime_projection_value(record, authority.authority_revision, |record| {
                        record.evidence_class.clone()
                    }),
                ),
                projection_of_authority_revision: authority.authority_revision,
                authority_scope: ProjectionAuthorityScope::ObserveOnly,
            },
            root_agent_id: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::RootAgentId,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.root_agent_id.clone()
                }),
            ),
            parent_agent_id: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::ParentAgentId,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.parent_agent_id.clone()
                }),
            ),
            agent_id: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::AgentId,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.agent_id.clone()
                }),
            ),
            task_id: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::TaskId,
                ProjectionValue::Value(authority.task_id.clone()),
            ),
            run_id: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::RunId,
                ProjectionValue::Value(authority.active_run_id.clone()),
            ),
            provider: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::Provider,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.provider.clone()
                }),
            ),
            profile: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::Profile,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.profile.clone()
                }),
            ),
            role: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::Role,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.role.clone()
                }),
            ),
            runtime_state: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::RuntimeState,
                runtime_projection_value(record, authority.authority_revision, |record| {
                    record.runtime_state
                }),
            ),
            lifecycle_state: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::LifecycleState,
                ProjectionValue::Value(authority.lifecycle_state),
            ),
            lease_generation: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::LeaseGeneration,
                ProjectionValue::Value(lease_generation),
            ),
            waiting_on: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::WaitingOn,
                handoff_waiting_on(authority.lifecycle_state).map_or_else(
                    || {
                        runtime_projection_value(record, authority.authority_revision, |record| {
                            record.waiting_on.clone()
                        })
                    },
                    |waiting_on| ProjectionValue::Value(Some(waiting_on.to_owned())),
                ),
            ),
            last_activity: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::LastActivity,
                last_activity
                    .map(ProjectionValue::Value)
                    .unwrap_or(ProjectionValue::SourceAbsent),
            ),
            last_transition: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::LastTransition,
                last_transition
                    .map(ProjectionValue::Value)
                    .unwrap_or(ProjectionValue::SourceAbsent),
            ),
            authority_revision: reduce_runtime_projection_value(
                &request,
                RuntimeProjectionField::AuthorityRevision,
                ProjectionValue::Value(authority.authority_revision),
            ),
        }))
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

    pub fn completion(
        &self,
        task_id: &str,
    ) -> Result<Option<CompletionBarrierRecord>, GovernanceError> {
        validate_identity("task_id", task_id)?;
        let state = self.state.lock().expect("governance state poisoned");
        let Some(completion_id) = state
            .tasks
            .get(task_id)
            .and_then(|authority| authority.active_completion_id.as_deref())
        else {
            return Ok(None);
        };
        Ok(state.completions.get(completion_id).cloned())
    }

    pub fn begin_completion(
        &self,
        request: BeginCompletionRequest,
    ) -> Result<BeginCompletionResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("agent_id", &request.agent_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_completion_contract(&request.contract)?;
        validate_completion_observation(&request.observation)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let contract_digest = request.contract.digest();
        let memory_checkpoint_id = verified_memory_checkpoint(
            &state,
            None,
            (&request.task_id, &request.run_id),
            request.expected_authority_revision,
            (&request.agent_id, &request.fencing_identity),
            &contract_digest,
        )
        .map(|checkpoint| checkpoint.checkpoint_id.clone());
        let completion_gate_failure = completion_gate_failure(
            &request.contract,
            &request.observation,
            request.expected_authority_revision,
            request.expected_authority_revision,
            request.expected_authority_revision,
            &request.fencing_identity,
            !request.contract.memory_durability_required || memory_checkpoint_id.is_some(),
        );
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut barrier = None;
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
            Some(authority) if authority.active_blocker_id.is_some() => {
                (DecisionOutcome::Deny, "unresolved_blocker")
            }
            Some(authority) if authority.lifecycle_state != LifecycleState::Running => {
                (DecisionOutcome::Deny, "completion_not_available")
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
            Some(authority) if authority.active_completion_id.is_some() => {
                (DecisionOutcome::Deny, "completion_already_active")
            }
            Some(authority) if authority.acceptance_contract_digest != contract_digest => {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            Some(_) if completion_gate_failure.is_some() => completion_gate_failure.unwrap(),
            Some(_) => {
                let completion_id = new_completion_id();
                let evidence_digest = completion_observation_digest(&request.observation);
                let mutation_receipts_digest =
                    completion_required_receipts_digest(&request.contract, &request.observation);
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Completing;
                authority.authority_revision += 1;
                authority.active_completion_id = Some(completion_id.clone());
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                let created = CompletionBarrierRecord {
                    completion_id: completion_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    source: request.source,
                    contract_digest,
                    evidence_digest,
                    mutation_receipts_digest,
                    evidence_authority_revision: request.observation.observed_authority_revision,
                    began_at_authority_revision: authority_after,
                    owner_agent_id: request.agent_id.clone(),
                    fencing_identity: request.fencing_identity.clone(),
                    memory_checkpoint_id,
                    state: CompletionBarrierState::Active,
                    completed_at_authority_revision: None,
                };
                state.completions.insert(completion_id, created.clone());
                barrier = Some(created);
                (DecisionOutcome::Allow, "completing")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.agent_id,
            requested_transition: TransitionKind::BeginCompletion,
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
        Ok(BeginCompletionResult { decision, barrier })
    }

    pub fn finish_completion(
        &self,
        request: FinishCompletionRequest,
    ) -> Result<FinishCompletionResult, GovernanceError> {
        validate_identity("task_id", &request.task_id)?;
        validate_identity("run_id", &request.run_id)?;
        validate_identity("completion_id", &request.completion_id)?;
        validate_identity("agent_id", &request.agent_id)?;
        validate_identity("actor", &request.actor)?;
        validate_identity("fencing_identity", &request.fencing_identity)?;
        validate_completion_contract(&request.contract)?;
        validate_completion_observation(&request.observation)?;
        validate_evidence_refs(&request.evidence_refs)?;

        let mut state = self.state.lock().expect("governance state poisoned");
        let snapshot = state.clone();
        let authority = state.tasks.get(&request.task_id).cloned();
        let barrier = state.completions.get(&request.completion_id).cloned();
        let authority_before = authority
            .as_ref()
            .map_or(0, |authority| authority.authority_revision);
        let now = OffsetDateTime::now_utc();
        let contract_digest = request.contract.digest();
        let mutation_receipts_digest =
            completion_required_receipts_digest(&request.contract, &request.observation);
        let completion_gate_failure = barrier.as_ref().and_then(|barrier| {
            let memory_durability_verified = !request.contract.memory_durability_required
                || verified_memory_checkpoint(
                    &state,
                    barrier.memory_checkpoint_id.as_deref(),
                    (&request.task_id, &request.run_id),
                    barrier.evidence_authority_revision,
                    (&request.agent_id, &request.fencing_identity),
                    &contract_digest,
                )
                .is_some();
            completion_gate_failure(
                &request.contract,
                &request.observation,
                request.expected_authority_revision,
                barrier.evidence_authority_revision,
                request.expected_authority_revision,
                &request.fencing_identity,
                memory_durability_verified,
            )
        });
        let mut authority_after = authority_before;
        let mut performed_at = None;
        let mut resulting_barrier = barrier.clone();
        let (outcome, reason) = match (authority.as_ref(), barrier.as_ref()) {
            (None, _) => (DecisionOutcome::Deny, "task_not_found"),
            (Some(authority), _) if authority.active_run_id != request.run_id => {
                (DecisionOutcome::Deny, "run_not_found")
            }
            (Some(authority), _)
                if authority.authority_revision != request.expected_authority_revision =>
            {
                (DecisionOutcome::Defer, "stale_authority")
            }
            (Some(_), _) if request.evidence_refs.is_empty() => {
                (DecisionOutcome::Defer, "evidence_required")
            }
            (Some(authority), _) if authority.lifecycle_state != LifecycleState::Completing => {
                (DecisionOutcome::Deny, "completion_not_available")
            }
            (Some(authority), _)
                if authority.owner_agent_id.as_deref() != Some(request.agent_id.as_str()) =>
            {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            (Some(authority), _)
                if authority.fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
            {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            (Some(authority), _)
                if authority.active_completion_id.as_deref()
                    != Some(request.completion_id.as_str()) =>
            {
                (DecisionOutcome::Defer, "stale_completion")
            }
            (_, None) => (DecisionOutcome::Defer, "stale_completion"),
            (_, Some(barrier))
                if barrier.task_id != request.task_id || barrier.run_id != request.run_id =>
            {
                (DecisionOutcome::Deny, "completion_scope_mismatch")
            }
            (_, Some(barrier)) if barrier.state != CompletionBarrierState::Active => {
                (DecisionOutcome::Deny, "completion_not_active")
            }
            (_, Some(barrier)) if barrier.owner_agent_id != request.agent_id => {
                (DecisionOutcome::Deny, "owner_mismatch")
            }
            (_, Some(barrier)) if barrier.fencing_identity != request.fencing_identity => {
                (DecisionOutcome::Deny, "fencing_mismatch")
            }
            (Some(authority), _) if authority.acceptance_contract_digest != contract_digest => {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            (_, Some(barrier)) if barrier.contract_digest != contract_digest => {
                (DecisionOutcome::Deny, "acceptance_contract_mismatch")
            }
            _ if completion_gate_failure.is_some() => completion_gate_failure.unwrap(),
            (_, Some(barrier)) if barrier.mutation_receipts_digest != mutation_receipts_digest => {
                (DecisionOutcome::Defer, "mutation_receipt_changed")
            }
            _ => {
                let authority = state.tasks.get_mut(&request.task_id).unwrap();
                authority.lifecycle_state = LifecycleState::Completed;
                authority.authority_revision += 1;
                authority.owner_agent_id = None;
                authority.fencing_identity = None;
                authority.active_completion_id = None;
                authority_after = authority.authority_revision;
                performed_at = Some(now);
                let barrier = state.completions.get_mut(&request.completion_id).unwrap();
                barrier.state = CompletionBarrierState::Completed;
                barrier.completed_at_authority_revision = Some(authority_after);
                resulting_barrier = Some(barrier.clone());
                (DecisionOutcome::Allow, "completed")
            }
        };
        let decision = DecisionRecord {
            decision_id: next_decision_id(&mut state),
            task_id: request.task_id,
            run_id: request.run_id,
            agent_id: request.agent_id,
            requested_transition: TransitionKind::Complete,
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
        Ok(FinishCompletionResult {
            decision,
            barrier: resulting_barrier,
        })
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

fn validate_policy_evaluation_request(
    request: &PolicyEvaluationRequest,
) -> Result<(), GovernanceError> {
    validate_identity("requested_action", &request.requested_action)?;
    validate_identity("target_scope", &request.target_scope)?;
    validate_identity("policy_version", &request.policy_version)?;
    validate_identity("evaluator_version", &request.evaluator_version)?;
    validate_evidence_refs(&request.evidence_refs)?;
    for rule in &request.rules {
        validate_identity("policy_id", &rule.policy_id)?;
        if let Some(exception_id) = &rule.exception_id {
            validate_identity("exception_id", exception_id)?;
        }
        validate_identity("requested_action", &rule.requested_action)?;
        validate_identity("target_scope", &rule.target_scope)?;
    }
    Ok(())
}

fn validate_memory_retain_request(request: &MemoryRetainRequest) -> Result<(), GovernanceError> {
    validate_identity("retain_request_id", &request.retain_request_id)?;
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("lineage_id", &request.lineage_id)?;
    validate_identity("content_digest", &request.content_digest)?;
    validate_identity("fencing_identity", &request.fencing_identity)?;
    validate_evidence_refs(&request.evidence_refs)
}

fn validate_memory_durability_evidence(
    evidence: &MemoryDurabilityEvidence,
) -> Result<(), GovernanceError> {
    validate_identity("adapter_id", &evidence.adapter_id)?;
    validate_identity("adapter_version", &evidence.adapter_version)?;
    validate_identity("upstream_id", &evidence.upstream_id)?;
    validate_identity("upstream_version", &evidence.upstream_version)?;
    validate_identity("retain_request_id", &evidence.retain_request_id)?;
    validate_identity("task_id", &evidence.task_id)?;
    validate_identity("run_id", &evidence.run_id)?;
    validate_identity("lineage_id", &evidence.lineage_id)?;
    validate_identity("content_digest", &evidence.content_digest)?;
    validate_identity("fencing_identity", &evidence.fencing_identity)?;
    validate_identity("operation_id", &evidence.operation_id)?;
    validate_evidence_refs(&evidence.evidence_refs)
}

fn validate_context_rotation_request(
    request: &ContextRotationRequest,
) -> Result<(), GovernanceError> {
    validate_identity("rotation_id", &request.rotation_id)?;
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("agent_id", &request.agent_id)?;
    validate_identity("actor", &request.actor)?;
    validate_identity("fencing_identity", &request.fencing_identity)?;
    validate_identity("lineage_id", &request.lineage_id)?;
    validate_identity("kanban_history_ref", &request.kanban_history_ref)?;
    validate_identity("old_context_id", &request.old_context_id)?;
    validate_identity("new_context_id", &request.new_context_id)?;
    validate_identity("retain_request_id", &request.retain_request_id)?;
    validate_identity("memory_content_digest", &request.memory_content_digest)?;
    validate_identity("memory_query", &request.memory_query)?;
    validate_evidence_refs(&request.selected_evidence_refs)?;
    if request.old_context_id == request.new_context_id {
        return Err(GovernanceError::InvalidIdentity("new_context_id"));
    }
    Ok(())
}

fn validate_begin_handoff_request(request: &BeginHandoffRequest) -> Result<(), GovernanceError> {
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("lineage_id", &request.lineage_id)?;
    validate_identity("root_agent_id", &request.root_agent_id)?;
    validate_identity("parent_agent_id", &request.parent_agent_id)?;
    validate_identity("old_owner_agent_id", &request.old_owner_agent_id)?;
    validate_identity("replacement_agent_id", &request.replacement_agent_id)?;
    validate_identity("actor", &request.actor)?;
    validate_identity("fencing_identity", &request.fencing_identity)?;
    validate_identity("context_checkpoint_id", &request.context_checkpoint_id)?;
    validate_completion_contract(&request.contract)?;
    validate_capability_probe_result(&request.handoff_capability)?;
    validate_evidence_observations(&request.blocker_evidence_baseline)?;
    validate_handoff_mutations(
        &request.pending_consequential_mutation_ids,
        &request.mutation_receipts,
    )?;
    validate_evidence_refs(&request.evidence_refs)?;
    if request.old_owner_agent_id == request.replacement_agent_id {
        return Err(GovernanceError::InvalidIdentity("replacement_agent_id"));
    }
    Ok(())
}

fn validate_suspend_handoff_request(
    request: &SuspendHandoffRequest,
) -> Result<(), GovernanceError> {
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("checkpoint_id", &request.checkpoint_id)?;
    validate_identity("old_owner_agent_id", &request.old_owner_agent_id)?;
    validate_identity("actor", &request.actor)?;
    validate_identity("fencing_identity", &request.fencing_identity)?;
    validate_capability_probe_result(&request.handoff_capability)?;
    validate_evidence_refs(&request.evidence_refs)
}

fn validate_acquire_handoff_request(
    request: &AcquireHandoffRequest,
) -> Result<(), GovernanceError> {
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("checkpoint_id", &request.checkpoint_id)?;
    validate_identity("replacement_agent_id", &request.replacement_agent_id)?;
    validate_identity("actor", &request.actor)?;
    validate_capability_probe_result(&request.handoff_capability)?;
    validate_evidence_refs(&request.evidence_refs)
}

fn validate_resume_handoff_request(request: &ResumeHandoffRequest) -> Result<(), GovernanceError> {
    validate_identity("task_id", &request.task_id)?;
    validate_identity("run_id", &request.run_id)?;
    validate_identity("checkpoint_id", &request.checkpoint_id)?;
    validate_identity("replacement_agent_id", &request.replacement_agent_id)?;
    validate_identity("actor", &request.actor)?;
    validate_identity("fencing_identity", &request.fencing_identity)?;
    validate_identity("new_context_id", &request.new_context_id)?;
    validate_identity("memory_query", &request.memory_query)?;
    validate_capability_probe_result(&request.handoff_capability)?;
    validate_evidence_refs(&request.evidence_refs)
}

fn validate_handoff_mutations(
    pending_mutation_ids: &[String],
    receipts: &[MutationReceipt],
) -> Result<(), GovernanceError> {
    let mut pending = std::collections::BTreeSet::new();
    for mutation_id in pending_mutation_ids {
        validate_identity("pending_mutation_id", mutation_id)?;
        if !pending.insert(mutation_id.trim().to_owned()) {
            return Err(GovernanceError::InvalidIdentity("pending_mutation_id"));
        }
    }
    let mut mutation_ids = std::collections::BTreeSet::new();
    let mut receipt_ids = std::collections::BTreeSet::new();
    for receipt in receipts {
        validate_identity("mutation_id", &receipt.mutation_id)?;
        validate_identity("mutation_receipt_id", &receipt.receipt_id)?;
        validate_identity("mutation_receipt_fence", &receipt.fencing_identity)?;
        if !mutation_ids.insert(receipt.mutation_id.trim().to_owned())
            || !receipt_ids.insert(receipt.receipt_id.trim().to_owned())
        {
            return Err(GovernanceError::InvalidIdentity("mutation_receipt_id"));
        }
    }
    Ok(())
}

fn validate_capability_probe_result(
    capability: &CapabilityProbeResult,
) -> Result<(), GovernanceError> {
    validate_identity("adapter_id", &capability.adapter_id)?;
    validate_identity("adapter_version", &capability.adapter_version)?;
    validate_identity("upstream_id", &capability.upstream_id)?;
    validate_identity("upstream_version", &capability.upstream_version)?;
    validate_identity("requested_capability", &capability.requested_capability)?;
    for family in capability
        .required_field_families
        .iter()
        .chain(&capability.observed_field_families)
        .chain(&capability.missing_field_families)
    {
        validate_identity("capability_field_family", family)?;
    }
    for semantic in capability
        .required_semantics
        .iter()
        .chain(&capability.observed_semantics)
        .chain(&capability.missing_semantics)
    {
        validate_identity("capability_semantic", semantic)?;
    }
    validate_evidence_refs(&capability.evidence_refs)
}

fn validate_memory_port_probe(probe: &MemoryPortProbe) -> Result<(), GovernanceError> {
    validate_capability_probe_result(&probe.capability)?;
    validate_evidence_refs(&probe.evidence_refs)
}

fn validate_memory_hydrate_result(result: &MemoryHydrateResult) -> Result<(), GovernanceError> {
    validate_identity("hydrate_request_id", &result.hydrate_request_id)?;
    validate_identity("checkpoint_id", &result.checkpoint_id)?;
    validate_identity("task_id", &result.task_id)?;
    validate_identity("run_id", &result.run_id)?;
    validate_identity("lineage_id", &result.lineage_id)?;
    validate_identity("new_context_id", &result.new_context_id)?;
    validate_evidence_refs(&result.evidence_refs)?;
    for item in &result.items {
        validate_identity("memory_id", &item.memory_id)?;
        validate_identity("memory_evidence_ref", &item.evidence_ref)?;
        if item.content.trim().is_empty() {
            return Err(GovernanceError::InvalidIdentity("memory_content"));
        }
    }
    Ok(())
}

fn authority_summary(authority: &TaskAuthority) -> AuthoritySummary {
    AuthoritySummary {
        task_id: authority.task_id.clone(),
        run_id: authority.active_run_id.clone(),
        lifecycle_state: authority.lifecycle_state,
        authority_revision: authority.authority_revision,
        acceptance_contract_digest: authority.acceptance_contract_digest.clone(),
        owner_agent_id: authority.owner_agent_id.clone().unwrap_or_default(),
        fencing_identity: authority.fencing_identity.clone().unwrap_or_default(),
        active_blocker_id: authority.active_blocker_id.clone(),
    }
}

fn context_rotation_authority_failure<'a>(
    authority: Option<&TaskAuthority>,
    request: &ContextRotationRequest,
    expected_authority_revision: u64,
) -> Option<(DecisionOutcome, &'a str)> {
    match authority {
        None => Some((DecisionOutcome::Deny, "task_not_found")),
        Some(authority) if authority.active_run_id != request.run_id => {
            Some((DecisionOutcome::Deny, "run_not_found"))
        }
        Some(authority) if authority.authority_revision != expected_authority_revision => {
            Some((DecisionOutcome::Defer, "stale_authority"))
        }
        Some(_) if request.selected_evidence_refs.is_empty() => {
            Some((DecisionOutcome::Defer, "evidence_required"))
        }
        Some(authority) if authority.lifecycle_state != LifecycleState::Running => {
            Some((DecisionOutcome::Deny, "context_rotation_not_available"))
        }
        Some(authority)
            if authority.owner_agent_id.as_deref() != Some(request.agent_id.as_str()) =>
        {
            Some((DecisionOutcome::Deny, "owner_mismatch"))
        }
        Some(authority)
            if authority.fencing_identity.as_deref() != Some(request.fencing_identity.as_str()) =>
        {
            Some((DecisionOutcome::Deny, "fencing_mismatch"))
        }
        Some(_) => None,
    }
}

fn memory_health_reason(health: MemoryPortHealth) -> &'static str {
    match health {
        MemoryPortHealth::Healthy => "memory_healthy",
        MemoryPortHealth::Degraded => "memory_health_degraded",
        MemoryPortHealth::Unavailable => "memory_health_unavailable",
        MemoryPortHealth::Unknown => "memory_health_unknown",
    }
}

fn memory_probe_failure(
    probe: &MemoryPortProbe,
    expected_capability: &str,
) -> Option<(DecisionOutcome, &'static str)> {
    if probe.health != MemoryPortHealth::Healthy {
        return Some((DecisionOutcome::Defer, memory_health_reason(probe.health)));
    }
    if probe.evidence_refs.is_empty() {
        return Some((DecisionOutcome::Defer, "memory_health_evidence_required"));
    }
    if probe.capability.status != CapabilityStatus::Supported {
        return Some((
            DecisionOutcome::Defer,
            memory_capability_reason(probe.capability.status),
        ));
    }
    if probe.capability.evidence_refs.is_empty() {
        return Some((
            DecisionOutcome::Defer,
            "memory_capability_evidence_required",
        ));
    }
    if probe.capability.requested_capability != expected_capability {
        return Some((DecisionOutcome::Deny, "memory_capability_mismatch"));
    }
    None
}

fn same_memory_provider(left: &CapabilityProbeResult, right: &CapabilityProbeResult) -> bool {
    left.adapter_id == right.adapter_id
        && left.adapter_version == right.adapter_version
        && left.upstream_id == right.upstream_id
        && left.upstream_version == right.upstream_version
        && left.integration_seam == right.integration_seam
}

fn handoff_capability_failure(
    capability: &CapabilityProbeResult,
) -> Option<(DecisionOutcome, &'static str)> {
    if capability.requested_capability != "handoff.checkpoint" {
        return Some((DecisionOutcome::Deny, "handoff_capability_mismatch"));
    }
    let status_failure = match capability.status {
        CapabilityStatus::Supported => None,
        CapabilityStatus::Degraded => Some((DecisionOutcome::Defer, "handoff_capability_degraded")),
        CapabilityStatus::Unsupported => {
            Some((DecisionOutcome::Defer, "handoff_capability_unsupported"))
        }
        CapabilityStatus::Incompatible => {
            Some((DecisionOutcome::Defer, "handoff_capability_incompatible"))
        }
        CapabilityStatus::Unknown => Some((DecisionOutcome::Defer, "handoff_capability_unknown")),
    };
    if status_failure.is_some() {
        return status_failure;
    }
    if capability.evidence_refs.is_empty() {
        return Some((
            DecisionOutcome::Defer,
            "handoff_capability_evidence_required",
        ));
    }
    if capability.schema_version != 1
        || capability.evidence_schema_version != Some(1)
        || capability.surface_present != Some(true)
        || capability.version_compatible != Some(true)
        || capability.required_field_families.is_empty()
        || capability.required_semantics.is_empty()
        || !capability.missing_field_families.is_empty()
        || !capability.missing_semantics.is_empty()
        || capability
            .required_field_families
            .iter()
            .any(|required| !capability.observed_field_families.contains(required))
        || capability
            .required_semantics
            .iter()
            .any(|required| !capability.observed_semantics.contains(required))
    {
        return Some((DecisionOutcome::Defer, "handoff_capability_unknown"));
    }
    None
}

fn same_capability_source(left: &CapabilityProbeResult, right: &CapabilityProbeResult) -> bool {
    left.adapter_id == right.adapter_id
        && left.adapter_version == right.adapter_version
        && left.upstream_id == right.upstream_id
        && left.upstream_version == right.upstream_version
        && left.integration_seam == right.integration_seam
        && left.requested_capability == right.requested_capability
        && normalized_capability_requirements(&left.required_field_families)
            == normalized_capability_requirements(&right.required_field_families)
        && normalized_capability_requirements(&left.required_semantics)
            == normalized_capability_requirements(&right.required_semantics)
}

fn normalized_capability_requirements(values: &[String]) -> std::collections::BTreeSet<&str> {
    values.iter().map(|value| value.trim()).collect()
}

fn handoff_mutation_failure(
    receipts: &[MutationReceipt],
    expected_authority_revision: u64,
    expected_fencing_identity: &str,
) -> Option<(DecisionOutcome, &'static str)> {
    receipts
        .iter()
        .any(|receipt| {
            receipt.authority_revision != expected_authority_revision
                || receipt.fencing_identity != expected_fencing_identity
        })
        .then_some((DecisionOutcome::Defer, "stale_mutation_receipt"))
}

fn resume_handoff_state_failure<'a>(
    state: &StateFile,
    request: &ResumeHandoffRequest,
) -> Option<(DecisionOutcome, &'a str)> {
    let authority = state.tasks.get(&request.task_id);
    let checkpoint = state.handoff_checkpoints.get(&request.checkpoint_id);
    match (authority, checkpoint) {
        (None, _) => Some((DecisionOutcome::Deny, "task_not_found")),
        (Some(authority), _) if authority.active_run_id != request.run_id => {
            Some((DecisionOutcome::Deny, "run_not_found"))
        }
        (Some(authority), _)
            if authority.authority_revision != request.expected_authority_revision =>
        {
            Some((DecisionOutcome::Defer, "stale_authority"))
        }
        (Some(_), _) if request.evidence_refs.is_empty() => {
            Some((DecisionOutcome::Defer, "evidence_required"))
        }
        (Some(authority), _) if authority.lifecycle_state != LifecycleState::Resuming => {
            Some((DecisionOutcome::Deny, "resume_handoff_not_available"))
        }
        (Some(authority), _)
            if authority.active_handoff_checkpoint_id.as_deref()
                != Some(request.checkpoint_id.as_str()) =>
        {
            Some((DecisionOutcome::Defer, "stale_handoff_checkpoint"))
        }
        (_, None) => Some((DecisionOutcome::Defer, "stale_handoff_checkpoint")),
        (_, Some(checkpoint))
            if checkpoint.task_id != request.task_id || checkpoint.run_id != request.run_id =>
        {
            Some((DecisionOutcome::Deny, "handoff_scope_mismatch"))
        }
        (_, Some(checkpoint))
            if checkpoint.state != HandoffCheckpointState::Resuming
                || checkpoint.resuming_authority_revision
                    != Some(request.expected_authority_revision) =>
        {
            Some((DecisionOutcome::Defer, "stale_handoff_checkpoint"))
        }
        (Some(authority), Some(checkpoint))
            if authority.acceptance_contract_digest != checkpoint.acceptance_contract_digest =>
        {
            Some((DecisionOutcome::Deny, "acceptance_contract_mismatch"))
        }
        (Some(authority), Some(checkpoint))
            if authority.owner_agent_id.as_deref()
                != Some(request.replacement_agent_id.as_str())
                || checkpoint.replacement_agent_id != request.replacement_agent_id =>
        {
            Some((DecisionOutcome::Deny, "replacement_mismatch"))
        }
        (Some(authority), Some(checkpoint))
            if authority.fencing_identity.as_deref() != Some(request.fencing_identity.as_str())
                || checkpoint.new_fencing_identity.as_deref()
                    != Some(request.fencing_identity.as_str()) =>
        {
            Some((DecisionOutcome::Deny, "fencing_mismatch"))
        }
        (_, Some(_)) if handoff_capability_failure(&request.handoff_capability).is_some() => {
            handoff_capability_failure(&request.handoff_capability)
        }
        (_, Some(checkpoint))
            if !same_capability_source(
                &checkpoint.handoff_capability,
                &request.handoff_capability,
            ) =>
        {
            Some((DecisionOutcome::Deny, "handoff_capability_binding_mismatch"))
        }
        (_, Some(checkpoint))
            if handoff_capability_failure(&checkpoint.handoff_capability).is_some() =>
        {
            handoff_capability_failure(&checkpoint.handoff_capability)
        }
        (_, Some(checkpoint))
            if handoff_context_checkpoint(
                state,
                &checkpoint.context_checkpoint_id,
                (&request.task_id, &request.run_id),
                &checkpoint.lineage_id,
                checkpoint.source_authority_revision,
                (
                    &checkpoint.old_owner_agent_id,
                    &checkpoint.old_fencing_identity,
                ),
                &checkpoint.acceptance_contract_digest,
            )
            .is_none() =>
        {
            Some((DecisionOutcome::Deny, "context_checkpoint_binding_mismatch"))
        }
        (_, Some(checkpoint))
            if checkpoint.memory_durability_required
                && (checkpoint.memory_checkpoint_id.as_deref()
                    != Some(checkpoint.context_checkpoint_id.as_str())
                    || verified_memory_checkpoint(
                        state,
                        checkpoint.memory_checkpoint_id.as_deref(),
                        (&request.task_id, &request.run_id),
                        checkpoint.source_authority_revision,
                        (
                            &checkpoint.old_owner_agent_id,
                            &checkpoint.old_fencing_identity,
                        ),
                        &checkpoint.acceptance_contract_digest,
                    )
                    .is_none()) =>
        {
            Some((DecisionOutcome::Defer, "memory_checkpoint_required"))
        }
        (_, Some(checkpoint))
            if state
                .context_checkpoints
                .get(&checkpoint.context_checkpoint_id)
                .and_then(|context_checkpoint| context_checkpoint.hydrate_capability.as_ref())
                .is_none_or(|capability| {
                    capability.requested_capability != "memory.hydrate"
                        || capability.status != CapabilityStatus::Supported
                        || capability.evidence_refs.is_empty()
                }) =>
        {
            Some((DecisionOutcome::Defer, "memory_provider_binding_required"))
        }
        _ => None,
    }
}

fn memory_capability_reason(status: CapabilityStatus) -> &'static str {
    match status {
        CapabilityStatus::Supported => "memory_capability_supported",
        CapabilityStatus::Degraded => "memory_capability_degraded",
        CapabilityStatus::Unsupported => "memory_capability_unsupported",
        CapabilityStatus::Incompatible => "memory_capability_incompatible",
        CapabilityStatus::Unknown => "memory_capability_unknown",
    }
}

fn memory_hydrate_failure<'a>(
    request: &MemoryHydrateRequest,
    result: &MemoryHydrateResult,
) -> Option<(DecisionOutcome, &'a str)> {
    if result.hydrate_request_id != request.hydrate_request_id
        || result.checkpoint_id != request.checkpoint_id
        || result.task_id != request.task_id
        || result.run_id != request.run_id
        || result.lineage_id != request.lineage_id
        || result.authority_revision != request.authority_summary.authority_revision
        || result.new_context_id != request.new_context_id
    {
        return Some((DecisionOutcome::Deny, "memory_hydrate_binding_mismatch"));
    }
    let status_failure = match result.status {
        MemoryHydrateStatus::Hydrated => None,
        MemoryHydrateStatus::Degraded => Some((DecisionOutcome::Defer, "memory_hydrate_degraded")),
        MemoryHydrateStatus::Unsupported => {
            Some((DecisionOutcome::Defer, "memory_hydrate_unsupported"))
        }
        MemoryHydrateStatus::Failed => Some((DecisionOutcome::Defer, "memory_hydrate_failed")),
        MemoryHydrateStatus::Timeout => Some((DecisionOutcome::Defer, "memory_hydrate_timeout")),
        MemoryHydrateStatus::Unknown => Some((DecisionOutcome::Defer, "memory_hydrate_unknown")),
    };
    if status_failure.is_some() {
        return status_failure;
    }
    if result.evidence_refs.is_empty() {
        return Some((DecisionOutcome::Defer, "memory_hydrate_evidence_required"));
    }
    if result
        .items
        .iter()
        .any(|item| item.layer != ContextLayer::LongTermMemory)
    {
        return Some((DecisionOutcome::Deny, "memory_hydrate_layer_forbidden"));
    }
    None
}

fn lease_generation(state: &StateFile, task_id: &str, run_id: &str) -> u64 {
    state
        .decisions
        .iter()
        .filter(|decision| {
            decision.task_id == task_id
                && decision.run_id == run_id
                && matches!(
                    decision.requested_transition,
                    TransitionKind::Claim | TransitionKind::AcquireHandoff
                )
                && decision.performed_at.is_some()
        })
        .count() as u64
}

fn handoff_waiting_on(lifecycle_state: LifecycleState) -> Option<&'static str> {
    match lifecycle_state {
        LifecycleState::Suspending => Some("handoff:suspending"),
        LifecycleState::Suspended => Some("handoff:suspended"),
        LifecycleState::Resuming => Some("handoff:resuming"),
        _ => None,
    }
}

fn runtime_projection_value<T: Clone>(
    record: Option<&AgentRuntimeRecord>,
    authority_revision: u64,
    project: impl FnOnce(&AgentRuntimeRecord) -> T,
) -> ProjectionValue<T> {
    let Some(record) = record else {
        return ProjectionValue::SourceAbsent;
    };
    let value = project(record);
    if record.observed_authority_revision == authority_revision {
        ProjectionValue::Value(value)
    } else {
        ProjectionValue::SourceStale {
            value,
            observed_authority_revision: record.observed_authority_revision,
        }
    }
}

fn reduce_runtime_projection_value<T>(
    request: &RuntimeProjectionRequest,
    field: RuntimeProjectionField,
    value: ProjectionValue<T>,
) -> ProjectionValue<T> {
    if request.redacted_fields.contains(&field) {
        return ProjectionValue::Redacted;
    }
    if request.omitted_fields.contains(&field) {
        return ProjectionValue::ProjectionOmission;
    }
    if !runtime_projection_field_supported(request.consumer_schema_version, field) {
        return ProjectionValue::SchemaDowngrade;
    }
    value
}

fn runtime_projection_field_supported(schema_version: u32, field: RuntimeProjectionField) -> bool {
    match schema_version {
        0 => matches!(
            field,
            RuntimeProjectionField::AgentId
                | RuntimeProjectionField::TaskId
                | RuntimeProjectionField::RunId
                | RuntimeProjectionField::RuntimeState
                | RuntimeProjectionField::LifecycleState
                | RuntimeProjectionField::AuthorityRevision
        ),
        1 => true,
        _ => false,
    }
}

fn validate_completion_contract(contract: &CompletionContract) -> Result<(), GovernanceError> {
    let mut mutations = std::collections::BTreeSet::new();
    for mutation_id in &contract.required_mutation_ids {
        validate_identity("required_mutation_id", mutation_id)?;
        if !mutations.insert(mutation_id.trim().to_owned()) {
            return Err(GovernanceError::InvalidIdentity("required_mutation_id"));
        }
    }
    let mut artifacts = std::collections::BTreeSet::new();
    for artifact in &contract.required_artifacts {
        validate_identity("required_artifact_id", &artifact.artifact_id)?;
        validate_identity("artifact_after_mutation_id", &artifact.after_mutation_id)?;
        if !mutations.contains(artifact.after_mutation_id.trim())
            || !artifacts.insert(artifact.artifact_id.trim().to_owned())
        {
            return Err(GovernanceError::InvalidIdentity("required_artifact_id"));
        }
    }
    Ok(())
}

fn validate_completion_observation(
    observation: &CompletionObservation,
) -> Result<(), GovernanceError> {
    validate_identity("completion_fencing_identity", &observation.fencing_identity)?;
    for child_id in &observation.active_child_ids {
        validate_identity("active_child_id", child_id)?;
    }
    for mutation_id in &observation.pending_consequential_mutation_ids {
        validate_identity("pending_mutation_id", mutation_id)?;
    }
    for receipt in &observation.mutation_receipts {
        validate_identity("mutation_id", &receipt.mutation_id)?;
        validate_identity("mutation_receipt_id", &receipt.receipt_id)?;
        validate_identity("mutation_receipt_fence", &receipt.fencing_identity)?;
    }
    for verification in &observation.artifact_verifications {
        validate_identity("artifact_id", &verification.artifact_id)?;
        validate_identity("artifact_identity", &verification.identity)?;
        validate_identity(
            "artifact_verification_fence",
            &verification.fencing_identity,
        )?;
        validate_identity(
            "artifact_after_mutation_receipt_id",
            &verification.after_mutation_receipt_id,
        )?;
    }
    Ok(())
}

fn completion_contract_digest(contract: &CompletionContract) -> String {
    let mut normalized = contract.clone();
    normalized.required_mutation_ids = normalized
        .required_mutation_ids
        .into_iter()
        .map(|value| value.trim().to_owned())
        .collect();
    normalized.required_mutation_ids.sort();
    for artifact in &mut normalized.required_artifacts {
        artifact.artifact_id = artifact.artifact_id.trim().to_owned();
        artifact.after_mutation_id = artifact.after_mutation_id.trim().to_owned();
    }
    normalized.required_artifacts.sort_by(|left, right| {
        (&left.artifact_id, &left.after_mutation_id)
            .cmp(&(&right.artifact_id, &right.after_mutation_id))
    });
    let bytes = serde_json::to_vec(&normalized).expect("completion contract serializes");
    let mut digest = Sha256::new();
    digest.update(b"m365/agent-governance/completion-contract/v1\0");
    digest.update(bytes);
    format!("{:x}", digest.finalize())
}

fn completion_observation_digest(observation: &CompletionObservation) -> String {
    let mut normalized = observation.clone();
    normalized.fencing_identity = normalized.fencing_identity.trim().to_owned();
    normalized.active_child_ids = normalized
        .active_child_ids
        .into_iter()
        .map(|value| value.trim().to_owned())
        .collect();
    normalized.active_child_ids.sort();
    normalized.pending_consequential_mutation_ids = normalized
        .pending_consequential_mutation_ids
        .into_iter()
        .map(|value| value.trim().to_owned())
        .collect();
    normalized.pending_consequential_mutation_ids.sort();
    for receipt in &mut normalized.mutation_receipts {
        receipt.mutation_id = receipt.mutation_id.trim().to_owned();
        receipt.receipt_id = receipt.receipt_id.trim().to_owned();
        receipt.fencing_identity = receipt.fencing_identity.trim().to_owned();
    }
    normalized.mutation_receipts.sort_by(|left, right| {
        (&left.mutation_id, &left.receipt_id).cmp(&(&right.mutation_id, &right.receipt_id))
    });
    for verification in &mut normalized.artifact_verifications {
        verification.artifact_id = verification.artifact_id.trim().to_owned();
        verification.identity = verification.identity.trim().to_owned();
        verification.fencing_identity = verification.fencing_identity.trim().to_owned();
        verification.after_mutation_receipt_id =
            verification.after_mutation_receipt_id.trim().to_owned();
    }
    normalized.artifact_verifications.sort_by(|left, right| {
        (&left.artifact_id, &left.after_mutation_receipt_id)
            .cmp(&(&right.artifact_id, &right.after_mutation_receipt_id))
    });
    let bytes = serde_json::to_vec(&normalized).expect("completion observation serializes");
    let mut digest = Sha256::new();
    digest.update(b"m365/agent-governance/completion-evidence/v1\0");
    digest.update(bytes);
    format!("{:x}", digest.finalize())
}

fn completion_mutation_receipt_failure(
    contract: &CompletionContract,
    observation: &CompletionObservation,
    expected_authority_revision: u64,
    expected_fencing_identity: &str,
) -> Option<&'static str> {
    for required in &contract.required_mutation_ids {
        let matches = observation
            .mutation_receipts
            .iter()
            .filter(|receipt| receipt.mutation_id.trim() == required.trim())
            .collect::<Vec<_>>();
        let receipt = match matches.as_slice() {
            [] => return Some("mutation_receipt_missing"),
            [receipt] => *receipt,
            _ => return Some("mutation_receipt_ambiguous"),
        };
        if receipt.authority_revision != expected_authority_revision
            || receipt.fencing_identity.trim() != expected_fencing_identity.trim()
        {
            return Some("stale_mutation_receipt");
        }
        if receipt.durability != MutationDurability::Durable {
            return Some("mutation_receipt_not_durable");
        }
    }
    None
}

fn completion_artifact_verification_failure(
    contract: &CompletionContract,
    observation: &CompletionObservation,
    expected_authority_revision: u64,
    expected_fencing_identity: &str,
) -> Option<&'static str> {
    for required in &contract.required_artifacts {
        let verifications = observation
            .artifact_verifications
            .iter()
            .filter(|verification| verification.artifact_id.trim() == required.artifact_id.trim())
            .collect::<Vec<_>>();
        let verification = match verifications.as_slice() {
            [] => return Some("artifact_verification_missing"),
            [verification] => *verification,
            _ => return Some("artifact_verification_ambiguous"),
        };
        let receipt = observation
            .mutation_receipts
            .iter()
            .find(|receipt| receipt.mutation_id.trim() == required.after_mutation_id.trim());
        let Some(receipt) = receipt else {
            return Some("artifact_verification_stale");
        };
        if verification.authority_revision != expected_authority_revision
            || verification.fencing_identity.trim() != expected_fencing_identity.trim()
            || verification.after_mutation_receipt_id.trim() != receipt.receipt_id.trim()
        {
            return Some("artifact_verification_stale");
        }
    }
    None
}

fn completion_gate_failure(
    contract: &CompletionContract,
    observation: &CompletionObservation,
    expected_observation_authority_revision: u64,
    expected_receipt_authority_revision: u64,
    expected_artifact_authority_revision: u64,
    expected_fencing_identity: &str,
    memory_durability_verified: bool,
) -> Option<(DecisionOutcome, &'static str)> {
    if observation.observed_authority_revision != expected_observation_authority_revision {
        return Some((DecisionOutcome::Defer, "stale_completion_observation"));
    }
    if observation.fencing_identity.trim() != expected_fencing_identity.trim() {
        return Some((DecisionOutcome::Deny, "observation_fencing_mismatch"));
    }
    if !observation.acceptance_satisfied {
        return Some((DecisionOutcome::Deny, "acceptance_not_satisfied"));
    }
    if !observation.active_child_ids.is_empty() {
        return Some((DecisionOutcome::Defer, "active_children"));
    }
    if !observation.pending_consequential_mutation_ids.is_empty() {
        return Some((DecisionOutcome::Defer, "pending_consequential_mutation"));
    }
    if observation.policy_state != CompletionGateState::Allow {
        return Some((DecisionOutcome::Deny, "policy_not_allowed"));
    }
    if contract.approval_required && observation.approval_state != ApprovalState::Allow {
        return Some((DecisionOutcome::Defer, "approval_not_satisfied"));
    }
    if contract.memory_durability_required
        && observation.memory_state != MemoryDurabilityState::Durable
    {
        return Some((DecisionOutcome::Defer, "memory_not_durable"));
    }
    if contract.memory_durability_required && !memory_durability_verified {
        return Some((DecisionOutcome::Defer, "memory_checkpoint_required"));
    }
    if let Some(reason) = completion_mutation_receipt_failure(
        contract,
        observation,
        expected_receipt_authority_revision,
        expected_fencing_identity,
    ) {
        return Some((DecisionOutcome::Defer, reason));
    }
    if let Some(reason) = completion_artifact_verification_failure(
        contract,
        observation,
        expected_artifact_authority_revision,
        expected_fencing_identity,
    ) {
        return Some((DecisionOutcome::Defer, reason));
    }
    None
}

fn handoff_context_checkpoint<'a>(
    state: &'a StateFile,
    checkpoint_id: &str,
    scope: (&str, &str),
    lineage_id: &str,
    authority_revision: u64,
    ownership: (&str, &str),
    acceptance_contract_digest: &str,
) -> Option<&'a ContextCheckpoint> {
    let (task_id, run_id) = scope;
    let (owner_agent_id, fencing_identity) = ownership;
    state
        .context_checkpoints
        .get(checkpoint_id)
        .filter(|checkpoint| {
            checkpoint.task_id == task_id
                && checkpoint.run_id == run_id
                && checkpoint.lineage_id == lineage_id
                && checkpoint.authority_summary.task_id == task_id
                && checkpoint.authority_summary.run_id == run_id
                && checkpoint.authority_summary.lifecycle_state == LifecycleState::Running
                && checkpoint.authority_summary.authority_revision == authority_revision
                && checkpoint.authority_summary.owner_agent_id == owner_agent_id
                && checkpoint.authority_summary.fencing_identity == fencing_identity
                && checkpoint.authority_summary.acceptance_contract_digest
                    == acceptance_contract_digest
        })
}

fn verified_memory_checkpoint<'a>(
    state: &'a StateFile,
    checkpoint_id: Option<&str>,
    scope: (&str, &str),
    authority_revision: u64,
    ownership: (&str, &str),
    acceptance_contract_digest: &str,
) -> Option<&'a ContextCheckpoint> {
    let (task_id, run_id) = scope;
    let (owner_agent_id, fencing_identity) = ownership;
    state
        .context_checkpoints
        .values()
        .filter(|checkpoint| {
            checkpoint_id.is_none_or(|id| checkpoint.checkpoint_id == id)
                && checkpoint.task_id == task_id
                && checkpoint.run_id == run_id
                && checkpoint.authority_summary.task_id == task_id
                && checkpoint.authority_summary.run_id == run_id
                && checkpoint.authority_summary.lifecycle_state == LifecycleState::Running
                && checkpoint.authority_summary.authority_revision == authority_revision
                && checkpoint.authority_summary.owner_agent_id == owner_agent_id
                && checkpoint.authority_summary.fencing_identity == fencing_identity
                && checkpoint.authority_summary.acceptance_contract_digest
                    == acceptance_contract_digest
                && checkpoint.capability_status == CapabilityStatus::Supported
                && checkpoint
                    .retain_capability
                    .as_ref()
                    .is_some_and(|capability| {
                        capability.requested_capability == "memory.retain_durable"
                            && capability.status == CapabilityStatus::Supported
                            && !capability.evidence_refs.is_empty()
                    })
                && checkpoint
                    .hydrate_capability
                    .as_ref()
                    .is_some_and(|capability| {
                        capability.requested_capability == "memory.hydrate"
                            && capability.status == CapabilityStatus::Supported
                            && !capability.evidence_refs.is_empty()
                    })
                && checkpoint
                    .retain_capability
                    .as_ref()
                    .zip(checkpoint.hydrate_capability.as_ref())
                    .is_some_and(|(retain, hydrate)| same_memory_provider(retain, hydrate))
                && checkpoint.phase == ContextLifecyclePhase::PostCompactVerify
                && checkpoint.verified_at.is_some()
                && !checkpoint.memory_evidence_refs.is_empty()
                && !checkpoint.selected_evidence_refs.is_empty()
        })
        .max_by_key(|checkpoint| checkpoint.created_at)
}

fn completion_required_receipts_digest(
    contract: &CompletionContract,
    observation: &CompletionObservation,
) -> String {
    let mut receipts = observation
        .mutation_receipts
        .iter()
        .filter(|receipt| {
            contract
                .required_mutation_ids
                .iter()
                .any(|required| required.trim() == receipt.mutation_id.trim())
        })
        .cloned()
        .collect::<Vec<_>>();
    for receipt in &mut receipts {
        receipt.mutation_id = receipt.mutation_id.trim().to_owned();
        receipt.receipt_id = receipt.receipt_id.trim().to_owned();
        receipt.fencing_identity = receipt.fencing_identity.trim().to_owned();
    }
    receipts.sort_by(|left, right| {
        (&left.mutation_id, &left.receipt_id).cmp(&(&right.mutation_id, &right.receipt_id))
    });
    let bytes = serde_json::to_vec(&receipts).expect("completion receipts serialize");
    let mut digest = Sha256::new();
    digest.update(b"m365/agent-governance/completion-receipts/v1\0");
    digest.update(bytes);
    format!("{:x}", digest.finalize())
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

fn new_completion_id() -> String {
    format!("completion-{:032x}", rand::random::<u128>())
}

fn new_approval_id() -> String {
    format!("approval-{:032x}", rand::random::<u128>())
}

fn new_context_checkpoint_id() -> String {
    format!("context-{:032x}", rand::random::<u128>())
}

fn new_handoff_checkpoint_id() -> String {
    format!("handoff-{:032x}", rand::random::<u128>())
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

    #[test]
    fn model_final_is_only_intent_and_valid_evidence_enters_completing_barrier() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        store
            .create_task(CreateTask {
                task_id: "task-complete-intent".to_owned(),
                run_id: "run-complete-intent".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-complete-intent".to_owned(),
                run_id: "run-complete-intent".to_owned(),
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
                task_id: "task-complete-intent".to_owned(),
                run_id: "run-complete-intent".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();

        let direct = store
            .transition(TransitionRequest {
                task_id: "task-complete-intent".to_owned(),
                run_id: "run-complete-intent".to_owned(),
                expected_authority_revision: 3,
                requested_transition: TransitionKind::Complete,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["transport:final".to_owned()],
            })
            .unwrap();
        assert_eq!(direct.outcome, DecisionOutcome::Defer);
        assert_eq!(direct.reason, "transition_not_implemented");
        assert_eq!(
            store
                .authority("task-complete-intent")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Running
        );

        let begun = store
            .begin_completion(BeginCompletionRequest {
                task_id: "task-complete-intent".to_owned(),
                run_id: "run-complete-intent".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                source: CompletionIntentSource::ModelFinal,
                contract: contract.clone(),
                observation: CompletionObservation {
                    observed_authority_revision: 3,
                    fencing_identity: fence.clone(),
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: Vec::new(),
                    artifact_verifications: Vec::new(),
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::NotRequired,
                    memory_state: MemoryDurabilityState::NotRequired,
                },
                evidence_refs: vec!["acceptance:verified".to_owned()],
            })
            .unwrap();
        assert_eq!(begun.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(
            begun.decision.requested_transition,
            TransitionKind::BeginCompletion
        );
        assert_eq!(begun.decision.authority_before, 3);
        assert_eq!(begun.decision.authority_after, 4);
        assert!(begun.decision.performed_at.is_some());
        let barrier = begun.barrier.unwrap();
        assert_eq!(barrier.state, CompletionBarrierState::Active);
        assert_eq!(barrier.evidence_authority_revision, 3);

        let authority = store.authority("task-complete-intent").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Completing);
        assert_eq!(authority.authority_revision, 4);
        assert_eq!(authority.owner_agent_id.as_deref(), Some("worker-a"));
        assert_eq!(authority.fencing_identity.as_deref(), Some(fence.as_str()));
        assert_eq!(
            authority.active_completion_id.as_deref(),
            Some(barrier.completion_id.as_str())
        );
        assert_eq!(
            authority.acceptance_contract_digest,
            barrier.contract_digest
        );
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(
            reopened
                .completion("task-complete-intent")
                .unwrap()
                .unwrap(),
            barrier
        );
    }

    #[test]
    fn required_mutation_receipt_must_be_durable_current_and_fenced() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: vec!["deploy".to_owned()],
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        store
            .create_task(CreateTask {
                task_id: "task-receipt".to_owned(),
                run_id: "run-receipt".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-receipt".to_owned(),
                run_id: "run-receipt".to_owned(),
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
                task_id: "task-receipt".to_owned(),
                run_id: "run-receipt".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();

        let request_with = |receipt: MutationReceipt| BeginCompletionRequest {
            task_id: "task-receipt".to_owned(),
            run_id: "run-receipt".to_owned(),
            expected_authority_revision: 3,
            agent_id: "worker-a".to_owned(),
            actor: "worker-a".to_owned(),
            fencing_identity: fence.clone(),
            source: CompletionIntentSource::AgentIntent,
            contract: contract.clone(),
            observation: CompletionObservation {
                observed_authority_revision: 3,
                fencing_identity: fence.clone(),
                acceptance_satisfied: true,
                active_child_ids: Vec::new(),
                pending_consequential_mutation_ids: Vec::new(),
                mutation_receipts: vec![receipt],
                artifact_verifications: Vec::new(),
                policy_state: CompletionGateState::Allow,
                approval_state: ApprovalState::NotRequired,
                memory_state: MemoryDurabilityState::NotRequired,
            },
            evidence_refs: vec!["receipt:deploy".to_owned()],
        };

        let accepted_only = store
            .begin_completion(request_with(MutationReceipt {
                mutation_id: "deploy".to_owned(),
                receipt_id: "receipt-accepted".to_owned(),
                authority_revision: 3,
                fencing_identity: fence.clone(),
                durability: MutationDurability::Accepted,
            }))
            .unwrap();
        assert_eq!(accepted_only.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(
            accepted_only.decision.reason,
            "mutation_receipt_not_durable"
        );

        let stale_revision = store
            .begin_completion(request_with(MutationReceipt {
                mutation_id: "deploy".to_owned(),
                receipt_id: "receipt-old-revision".to_owned(),
                authority_revision: 2,
                fencing_identity: fence.clone(),
                durability: MutationDurability::Durable,
            }))
            .unwrap();
        assert_eq!(stale_revision.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(stale_revision.decision.reason, "stale_mutation_receipt");

        let stale_revision_replay = store
            .begin_completion(request_with(MutationReceipt {
                mutation_id: "deploy".to_owned(),
                receipt_id: "receipt-old-revision".to_owned(),
                authority_revision: 2,
                fencing_identity: fence.clone(),
                durability: MutationDurability::Durable,
            }))
            .unwrap();
        assert_eq!(
            stale_revision_replay.decision.outcome,
            DecisionOutcome::Defer
        );
        assert_eq!(
            stale_revision_replay.decision.reason,
            "stale_mutation_receipt"
        );
        assert_eq!(
            store
                .authority("task-receipt")
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );

        let stale_fence = store
            .begin_completion(request_with(MutationReceipt {
                mutation_id: "deploy".to_owned(),
                receipt_id: "receipt-old-fence".to_owned(),
                authority_revision: 3,
                fencing_identity: "fence-stale".to_owned(),
                durability: MutationDurability::Durable,
            }))
            .unwrap();
        assert_eq!(stale_fence.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(stale_fence.decision.reason, "stale_mutation_receipt");

        assert_eq!(
            store
                .authority("task-receipt")
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );
        let current = store
            .begin_completion(request_with(MutationReceipt {
                mutation_id: "deploy".to_owned(),
                receipt_id: "receipt-current".to_owned(),
                authority_revision: 3,
                fencing_identity: fence.clone(),
                durability: MutationDurability::Durable,
            }))
            .unwrap();
        assert_eq!(current.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(current.decision.authority_before, 3);
        assert_eq!(current.decision.authority_after, 4);
        assert_eq!(
            store
                .authority("task-receipt")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Completing
        );
    }

    #[test]
    fn final_artifact_verification_must_follow_current_required_mutation_receipt() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: vec!["deploy".to_owned()],
            required_artifacts: vec![ArtifactRequirement {
                artifact_id: "report".to_owned(),
                after_mutation_id: "deploy".to_owned(),
            }],
            approval_required: false,
            memory_durability_required: false,
        };
        store
            .create_task(CreateTask {
                task_id: "task-artifact".to_owned(),
                run_id: "run-artifact".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-artifact".to_owned(),
                run_id: "run-artifact".to_owned(),
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
                task_id: "task-artifact".to_owned(),
                run_id: "run-artifact".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let receipt = MutationReceipt {
            mutation_id: "deploy".to_owned(),
            receipt_id: "receipt-current".to_owned(),
            authority_revision: 3,
            fencing_identity: fence.clone(),
            durability: MutationDurability::Durable,
        };
        let request_with = |verification: ArtifactVerification| BeginCompletionRequest {
            task_id: "task-artifact".to_owned(),
            run_id: "run-artifact".to_owned(),
            expected_authority_revision: 3,
            agent_id: "worker-a".to_owned(),
            actor: "worker-a".to_owned(),
            fencing_identity: fence.clone(),
            source: CompletionIntentSource::AgentIntent,
            contract: contract.clone(),
            observation: CompletionObservation {
                observed_authority_revision: 3,
                fencing_identity: fence.clone(),
                acceptance_satisfied: true,
                active_child_ids: Vec::new(),
                pending_consequential_mutation_ids: Vec::new(),
                mutation_receipts: vec![receipt.clone()],
                artifact_verifications: vec![verification],
                policy_state: CompletionGateState::Allow,
                approval_state: ApprovalState::NotRequired,
                memory_state: MemoryDurabilityState::NotRequired,
            },
            evidence_refs: vec!["receipt:deploy".to_owned(), "artifact:report".to_owned()],
        };

        for stale in [
            ArtifactVerification {
                artifact_id: "report".to_owned(),
                identity: "sha256:old".to_owned(),
                authority_revision: 3,
                fencing_identity: fence.clone(),
                after_mutation_receipt_id: "receipt-before-deploy".to_owned(),
            },
            ArtifactVerification {
                artifact_id: "report".to_owned(),
                identity: "sha256:current".to_owned(),
                authority_revision: 2,
                fencing_identity: fence.clone(),
                after_mutation_receipt_id: "receipt-current".to_owned(),
            },
            ArtifactVerification {
                artifact_id: "report".to_owned(),
                identity: "sha256:current".to_owned(),
                authority_revision: 3,
                fencing_identity: "fence-stale".to_owned(),
                after_mutation_receipt_id: "receipt-current".to_owned(),
            },
        ] {
            let result = store.begin_completion(request_with(stale)).unwrap();
            assert_eq!(result.decision.outcome, DecisionOutcome::Defer);
            assert_eq!(result.decision.reason, "artifact_verification_stale");
            assert!(result.decision.performed_at.is_none());
        }
        assert_eq!(
            store
                .authority("task-artifact")
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );

        let current = store
            .begin_completion(request_with(ArtifactVerification {
                artifact_id: "report".to_owned(),
                identity: "sha256:current".to_owned(),
                authority_revision: 3,
                fencing_identity: fence.clone(),
                after_mutation_receipt_id: "receipt-current".to_owned(),
            }))
            .unwrap();
        assert_eq!(current.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(
            store
                .authority("task-artifact")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Completing
        );
    }

    #[test]
    fn completing_barrier_requires_current_owner_fence_and_revision_before_completed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        store
            .create_task(CreateTask {
                task_id: "task-finish".to_owned(),
                run_id: "run-finish".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-finish".to_owned(),
                run_id: "run-finish".to_owned(),
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
                task_id: "task-finish".to_owned(),
                run_id: "run-finish".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let begun = store
            .begin_completion(BeginCompletionRequest {
                task_id: "task-finish".to_owned(),
                run_id: "run-finish".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                source: CompletionIntentSource::AgentIntent,
                contract: contract.clone(),
                observation: CompletionObservation {
                    observed_authority_revision: 3,
                    fencing_identity: fence.clone(),
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: Vec::new(),
                    artifact_verifications: Vec::new(),
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::NotRequired,
                    memory_state: MemoryDurabilityState::NotRequired,
                },
                evidence_refs: vec!["evidence:completion-ready".to_owned()],
            })
            .unwrap();
        let barrier = begun.barrier.unwrap();
        assert_eq!(barrier.state, CompletionBarrierState::Active);
        assert_eq!(
            store
                .authority("task-finish")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Completing
        );

        let request =
            |revision: u64, agent_id: &str, fencing_identity: &str| FinishCompletionRequest {
                task_id: "task-finish".to_owned(),
                run_id: "run-finish".to_owned(),
                expected_authority_revision: revision,
                completion_id: barrier.completion_id.clone(),
                agent_id: agent_id.to_owned(),
                actor: agent_id.to_owned(),
                fencing_identity: fencing_identity.to_owned(),
                contract: contract.clone(),
                observation: CompletionObservation {
                    observed_authority_revision: revision,
                    fencing_identity: fencing_identity.to_owned(),
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: Vec::new(),
                    artifact_verifications: Vec::new(),
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::NotRequired,
                    memory_state: MemoryDurabilityState::NotRequired,
                },
                evidence_refs: vec!["evidence:barrier-readback".to_owned()],
            };

        let stale = store
            .finish_completion(request(3, "worker-a", &fence))
            .unwrap();
        assert_eq!(stale.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(stale.decision.reason, "stale_authority");
        let wrong_owner = store
            .finish_completion(request(4, "worker-b", &fence))
            .unwrap();
        assert_eq!(wrong_owner.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(wrong_owner.decision.reason, "owner_mismatch");
        let wrong_fence = store
            .finish_completion(request(4, "worker-a", "fence-stale"))
            .unwrap();
        assert_eq!(wrong_fence.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(wrong_fence.decision.reason, "fencing_mismatch");

        let completed = store
            .finish_completion(request(4, "worker-a", &fence))
            .unwrap();
        assert_eq!(completed.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(completed.decision.reason, "completed");
        assert_eq!(completed.decision.authority_before, 4);
        assert_eq!(completed.decision.authority_after, 5);
        assert!(completed.decision.performed_at.is_some());
        let barrier = completed.barrier.unwrap();
        assert_eq!(barrier.state, CompletionBarrierState::Completed);
        assert_eq!(barrier.completed_at_authority_revision, Some(5));

        let authority = store.authority("task-finish").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Completed);
        assert_eq!(authority.authority_revision, 5);
        assert!(authority.owner_agent_id.is_none());
        assert!(authority.fencing_identity.is_none());
        assert!(authority.active_completion_id.is_none());
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let authority = reopened.authority("task-finish").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Completed);
        assert_eq!(authority.authority_revision, 5);
    }

    #[test]
    fn unresolved_blocker_prevents_completion_and_records_explainable_decision() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        store
            .create_task(CreateTask {
                task_id: "task-blocked-complete".to_owned(),
                run_id: "run-blocked-complete".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-blocked-complete".to_owned(),
                run_id: "run-blocked-complete".to_owned(),
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
                task_id: "task-blocked-complete".to_owned(),
                run_id: "run-blocked-complete".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        store
            .block_task(BlockTaskRequest {
                task_id: "task-blocked-complete".to_owned(),
                run_id: "run-blocked-complete".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "dependency-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("dependency".to_owned(), "database".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "database".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "database".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:database-down".to_owned()],
            })
            .unwrap();

        let decision = store
            .begin_completion(BeginCompletionRequest {
                task_id: "task-blocked-complete".to_owned(),
                run_id: "run-blocked-complete".to_owned(),
                expected_authority_revision: 4,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                source: CompletionIntentSource::ModelFinal,
                contract,
                observation: CompletionObservation {
                    observed_authority_revision: 4,
                    fencing_identity: fence,
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: Vec::new(),
                    artifact_verifications: Vec::new(),
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::NotRequired,
                    memory_state: MemoryDurabilityState::NotRequired,
                },
                evidence_refs: vec!["transport:final".to_owned()],
            })
            .unwrap()
            .decision;
        assert_eq!(decision.outcome, DecisionOutcome::Deny);
        assert_eq!(decision.reason, "unresolved_blocker");
        assert!(decision.performed_at.is_none());

        let authority = store.authority("task-blocked-complete").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Blocked);
        assert_eq!(authority.authority_revision, 4);
        let ledger = store.decisions("task-blocked-complete").unwrap();
        assert_eq!(ledger.last().unwrap(), &decision);
    }

    #[test]
    fn premature_completion_conditions_and_contract_weakening_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: true,
            memory_durability_required: true,
        };
        store
            .create_task(CreateTask {
                task_id: "task-premature".to_owned(),
                run_id: "run-premature".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-premature".to_owned(),
                run_id: "run-premature".to_owned(),
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
                task_id: "task-premature".to_owned(),
                run_id: "run-premature".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();

        let base_observation = CompletionObservation {
            observed_authority_revision: 3,
            fencing_identity: fence.clone(),
            acceptance_satisfied: true,
            active_child_ids: Vec::new(),
            pending_consequential_mutation_ids: Vec::new(),
            mutation_receipts: Vec::new(),
            artifact_verifications: Vec::new(),
            policy_state: CompletionGateState::Allow,
            approval_state: ApprovalState::Allow,
            memory_state: MemoryDurabilityState::Durable,
        };
        let evaluate = |contract: CompletionContract, observation: CompletionObservation| {
            store
                .begin_completion(BeginCompletionRequest {
                    task_id: "task-premature".to_owned(),
                    run_id: "run-premature".to_owned(),
                    expected_authority_revision: 3,
                    agent_id: "worker-a".to_owned(),
                    actor: "worker-a".to_owned(),
                    fencing_identity: fence.clone(),
                    source: CompletionIntentSource::TransportFinal,
                    contract,
                    observation,
                    evidence_refs: vec!["transport:final".to_owned()],
                })
                .unwrap()
                .decision
        };

        let cases = [
            (
                "acceptance_not_satisfied",
                CompletionObservation {
                    acceptance_satisfied: false,
                    ..base_observation.clone()
                },
            ),
            (
                "active_children",
                CompletionObservation {
                    active_child_ids: vec!["child-1".to_owned()],
                    ..base_observation.clone()
                },
            ),
            (
                "pending_consequential_mutation",
                CompletionObservation {
                    pending_consequential_mutation_ids: vec!["mutation-1".to_owned()],
                    ..base_observation.clone()
                },
            ),
            (
                "policy_not_allowed",
                CompletionObservation {
                    policy_state: CompletionGateState::Pending,
                    ..base_observation.clone()
                },
            ),
            (
                "approval_not_satisfied",
                CompletionObservation {
                    approval_state: ApprovalState::Pending,
                    ..base_observation.clone()
                },
            ),
            (
                "memory_not_durable",
                CompletionObservation {
                    memory_state: MemoryDurabilityState::Queued,
                    ..base_observation.clone()
                },
            ),
        ];
        for (reason, observation) in cases {
            let decision = evaluate(contract.clone(), observation);
            assert_ne!(decision.outcome, DecisionOutcome::Allow);
            assert_eq!(decision.reason, reason);
            assert!(decision.performed_at.is_none());
            let authority = store.authority("task-premature").unwrap().unwrap();
            assert_eq!(authority.lifecycle_state, LifecycleState::Running);
            assert_eq!(authority.authority_revision, 3);
        }

        let weakened = CompletionContract {
            approval_required: false,
            memory_durability_required: false,
            ..contract
        };
        let decision = evaluate(weakened, base_observation);
        assert_eq!(decision.outcome, DecisionOutcome::Deny);
        assert_eq!(decision.reason, "acceptance_contract_mismatch");
        assert!(decision.performed_at.is_none());
        let authority = store.authority("task-premature").unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Running);
        assert_eq!(authority.authority_revision, 3);
    }

    #[test]
    fn completion_requires_verified_memory_checkpoint_not_caller_claim() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let suffix = "memory-completion";
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: true,
        };
        let (task_id, run_id, fence) = running_task(&store, suffix, contract.digest());
        let request = || BeginCompletionRequest {
            task_id: task_id.clone(),
            run_id: run_id.clone(),
            expected_authority_revision: 3,
            agent_id: format!("agent-{suffix}"),
            actor: format!("agent-{suffix}"),
            fencing_identity: fence.clone(),
            source: CompletionIntentSource::AgentIntent,
            contract: contract.clone(),
            observation: CompletionObservation {
                observed_authority_revision: 3,
                fencing_identity: fence.clone(),
                acceptance_satisfied: true,
                active_child_ids: Vec::new(),
                pending_consequential_mutation_ids: Vec::new(),
                mutation_receipts: Vec::new(),
                artifact_verifications: Vec::new(),
                policy_state: CompletionGateState::Allow,
                approval_state: ApprovalState::NotRequired,
                memory_state: MemoryDurabilityState::Durable,
            },
            evidence_refs: vec!["evidence:completion".to_owned()],
        };

        let unverified = store.begin_completion(request()).unwrap();
        assert_eq!(unverified.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(unverified.decision.reason, "memory_checkpoint_required");
        assert!(unverified.barrier.is_none());

        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        let rotated = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap();
        let checkpoint_id = rotated.checkpoint.unwrap().checkpoint_id;

        let verified = store.begin_completion(request()).unwrap();
        assert_eq!(verified.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(
            verified.barrier.unwrap().memory_checkpoint_id,
            Some(checkpoint_id)
        );
    }

    #[test]
    fn finish_completion_revalidates_fresh_state_and_artifact_after_barrier() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: vec!["deploy".to_owned()],
            required_artifacts: vec![ArtifactRequirement {
                artifact_id: "report".to_owned(),
                after_mutation_id: "deploy".to_owned(),
            }],
            approval_required: true,
            memory_durability_required: true,
        };
        store
            .create_task(CreateTask {
                task_id: "task-finish-recheck".to_owned(),
                run_id: "run-finish-recheck".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: contract.digest(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-finish-recheck".to_owned(),
                run_id: "run-finish-recheck".to_owned(),
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
                task_id: "task-finish-recheck".to_owned(),
                run_id: "run-finish-recheck".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();
        let lineage_id = "lineage-finish-recheck";
        let retain_request_id = "retain-finish-recheck";
        let memory_port = verified_test_memory_port(
            "task-finish-recheck",
            "run-finish-recheck",
            lineage_id,
            &fence,
            retain_request_id,
            "sha256:precompact-memory",
        );
        let rotated = store
            .rotate_context(
                context_rotation_request_for_agent(
                    "finish-recheck",
                    "task-finish-recheck",
                    "run-finish-recheck",
                    &fence,
                    lineage_id,
                    retain_request_id,
                    "worker-a",
                ),
                &memory_port,
                &AcceptMemoryEvidence,
            )
            .unwrap();
        assert_eq!(rotated.decision.outcome, DecisionOutcome::Allow);
        let receipt = MutationReceipt {
            mutation_id: "deploy".to_owned(),
            receipt_id: "receipt-deploy".to_owned(),
            authority_revision: 3,
            fencing_identity: fence.clone(),
            durability: MutationDurability::Durable,
        };
        let begun = store
            .begin_completion(BeginCompletionRequest {
                task_id: "task-finish-recheck".to_owned(),
                run_id: "run-finish-recheck".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                source: CompletionIntentSource::AgentIntent,
                contract: contract.clone(),
                observation: CompletionObservation {
                    observed_authority_revision: 3,
                    fencing_identity: fence.clone(),
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: vec![receipt.clone()],
                    artifact_verifications: vec![ArtifactVerification {
                        artifact_id: "report".to_owned(),
                        identity: "sha256:begin".to_owned(),
                        authority_revision: 3,
                        fencing_identity: fence.clone(),
                        after_mutation_receipt_id: receipt.receipt_id.clone(),
                    }],
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::Allow,
                    memory_state: MemoryDurabilityState::Durable,
                },
                evidence_refs: vec!["receipt:deploy".to_owned(), "artifact:report".to_owned()],
            })
            .unwrap();
        let barrier = begun.barrier.unwrap();

        let finish =
            |active_child_ids: Vec<String>, artifact_revision: u64| FinishCompletionRequest {
                task_id: "task-finish-recheck".to_owned(),
                run_id: "run-finish-recheck".to_owned(),
                expected_authority_revision: 4,
                completion_id: barrier.completion_id.clone(),
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence.clone(),
                contract: contract.clone(),
                observation: CompletionObservation {
                    observed_authority_revision: 4,
                    fencing_identity: fence.clone(),
                    acceptance_satisfied: true,
                    active_child_ids,
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: vec![receipt.clone()],
                    artifact_verifications: vec![ArtifactVerification {
                        artifact_id: "report".to_owned(),
                        identity: "sha256:finish".to_owned(),
                        authority_revision: artifact_revision,
                        fencing_identity: fence.clone(),
                        after_mutation_receipt_id: receipt.receipt_id.clone(),
                    }],
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::Allow,
                    memory_state: MemoryDurabilityState::Durable,
                },
                evidence_refs: vec!["receipt:deploy".to_owned(), "artifact:report".to_owned()],
            };

        let child_drift = store
            .finish_completion(finish(vec!["child-late".to_owned()], 4))
            .unwrap();
        assert_eq!(child_drift.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(child_drift.decision.reason, "active_children");
        assert_eq!(
            store
                .authority("task-finish-recheck")
                .unwrap()
                .unwrap()
                .authority_revision,
            4
        );

        let stale_artifact = store.finish_completion(finish(Vec::new(), 3)).unwrap();
        assert_eq!(stale_artifact.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(
            stale_artifact.decision.reason,
            "artifact_verification_stale"
        );

        let completed = store.finish_completion(finish(Vec::new(), 4)).unwrap();
        assert_eq!(completed.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(completed.decision.reason, "completed");
        assert_eq!(
            store
                .authority("task-finish-recheck")
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Completed
        );
    }

    #[test]
    fn runtime_projection_keeps_runtime_liveness_distinct_from_lifecycle_authority() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-projection".to_owned(),
                run_id: "run-projection".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:projection:v1".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-projection".to_owned(),
                run_id: "run-projection".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:worker-ready".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.clone().unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-projection".to_owned(),
                run_id: "run-projection".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "worker-a".to_owned(),
                actor: "dispatcher-a".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:owner-ready".to_owned()],
            })
            .unwrap();

        let observed = store
            .record_runtime_observation(RuntimeObservationRequest {
                task_id: "task-projection".to_owned(),
                run_id: "run-projection".to_owned(),
                expected_authority_revision: 3,
                root_agent_id: "root-a".to_owned(),
                parent_agent_id: None,
                agent_id: "worker-a".to_owned(),
                provider: "m365".to_owned(),
                profile: "default".to_owned(),
                role: "worker".to_owned(),
                runtime_state: RuntimeState::Running,
                waiting_on: None,
                environment: "test".to_owned(),
                evidence_class: "direct-runtime".to_owned(),
                actor: "runtime-adapter".to_owned(),
                evidence_refs: vec!["runtime:worker-a-live".to_owned()],
            })
            .unwrap();
        assert_eq!(observed.outcome, DecisionOutcome::Allow);
        assert_eq!(observed.reason, "runtime_observed");
        assert_eq!(observed.record.as_ref().unwrap().runtime_event_seq, 1);

        let request = RuntimeProjectionRequest {
            task_id: "task-projection".to_owned(),
            run_id: "run-projection".to_owned(),
            agent_id: "worker-a".to_owned(),
            consumer_schema_version: 1,
            redacted_fields: Vec::new(),
            omitted_fields: Vec::new(),
        };
        let running = store.runtime_projection(request.clone()).unwrap().unwrap();
        assert_eq!(
            running.runtime_state,
            ProjectionValue::Value(RuntimeState::Running)
        );
        assert_eq!(
            running.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Running)
        );
        assert_eq!(running.lease_generation, ProjectionValue::Value(1));
        assert_eq!(running.authority_revision, ProjectionValue::Value(3));
        assert_eq!(
            running.root_agent_id,
            ProjectionValue::Value("root-a".to_owned())
        );
        assert_eq!(running.parent_agent_id, ProjectionValue::Value(None));
        assert_eq!(
            running.agent_id,
            ProjectionValue::Value("worker-a".to_owned())
        );
        assert_eq!(running.provider, ProjectionValue::Value("m365".to_owned()));
        assert_eq!(
            running.profile,
            ProjectionValue::Value("default".to_owned())
        );
        assert_eq!(running.role, ProjectionValue::Value("worker".to_owned()));
        assert_eq!(running.waiting_on, ProjectionValue::Value(None));
        assert_eq!(
            running.last_transition,
            ProjectionValue::Value(TransitionKind::Start)
        );
        assert_eq!(running.metadata.schema_version, 1);
        assert_eq!(running.metadata.source_schema_version, 1);
        assert_eq!(running.metadata.projection_of_authority_revision, 3);
        assert_eq!(
            running.metadata.authority_scope,
            ProjectionAuthorityScope::ObserveOnly
        );
        assert_eq!(
            running.metadata.emitter_identity,
            "m365-ai-gateway/acp-governance"
        );
        assert_eq!(
            running.metadata.provenance,
            ProjectionProvenance::AcpCanonicalState
        );
        assert_eq!(
            running.metadata.environment,
            ProjectionValue::Value("test".to_owned())
        );
        assert_eq!(
            running.metadata.evidence_class,
            ProjectionValue::Value("direct-runtime".to_owned())
        );
        assert!(running.metadata.event_seq >= 4);

        store
            .block_task(BlockTaskRequest {
                task_id: "task-projection".to_owned(),
                run_id: "run-projection".to_owned(),
                expected_authority_revision: 3,
                agent_id: "worker-a".to_owned(),
                actor: "worker-a".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "db-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("status".to_owned(), "down".to_owned())]),
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

        let blocked = store.runtime_projection(request).unwrap().unwrap();
        assert_eq!(
            blocked.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Blocked)
        );
        assert_eq!(
            blocked.runtime_state,
            ProjectionValue::SourceStale {
                value: RuntimeState::Running,
                observed_authority_revision: 3,
            }
        );
        assert_eq!(blocked.authority_revision, ProjectionValue::Value(4));
        assert_eq!(blocked.metadata.projection_of_authority_revision, 4);
        assert_eq!(
            blocked.metadata.authority_scope,
            ProjectionAuthorityScope::ObserveOnly
        );
    }

    #[test]
    fn runtime_projection_reductions_and_schema_downgrade_are_explicit_and_observe_only() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-projection-reduction".to_owned(),
                run_id: "run-projection-reduction".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:projection-reduction:v1".to_owned(),
            })
            .unwrap();
        store
            .record_runtime_observation(RuntimeObservationRequest {
                task_id: "task-projection-reduction".to_owned(),
                run_id: "run-projection-reduction".to_owned(),
                expected_authority_revision: 1,
                root_agent_id: "root-a".to_owned(),
                parent_agent_id: None,
                agent_id: "worker-a".to_owned(),
                provider: "m365".to_owned(),
                profile: "default".to_owned(),
                role: "worker".to_owned(),
                runtime_state: RuntimeState::Idle,
                waiting_on: None,
                environment: "test".to_owned(),
                evidence_class: "direct-runtime".to_owned(),
                actor: "runtime-adapter".to_owned(),
                evidence_refs: vec!["runtime:worker-a-idle".to_owned()],
            })
            .unwrap();

        let reduced = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: "task-projection-reduction".to_owned(),
                run_id: "run-projection-reduction".to_owned(),
                agent_id: "worker-a".to_owned(),
                consumer_schema_version: 1,
                redacted_fields: vec![RuntimeProjectionField::Provider],
                omitted_fields: vec![RuntimeProjectionField::Role],
            })
            .unwrap()
            .unwrap();
        assert_eq!(reduced.provider, ProjectionValue::Redacted);
        assert_eq!(reduced.role, ProjectionValue::ProjectionOmission);
        assert_eq!(reduced.parent_agent_id, ProjectionValue::Value(None));
        assert_eq!(reduced.waiting_on, ProjectionValue::Value(None));
        assert_eq!(
            reduced.metadata.authority_scope,
            ProjectionAuthorityScope::ObserveOnly
        );

        let absent = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: "task-projection-reduction".to_owned(),
                run_id: "run-projection-reduction".to_owned(),
                agent_id: "unknown-agent".to_owned(),
                consumer_schema_version: 1,
                redacted_fields: Vec::new(),
                omitted_fields: Vec::new(),
            })
            .unwrap()
            .unwrap();
        assert_eq!(absent.agent_id, ProjectionValue::SourceAbsent);
        assert_eq!(absent.parent_agent_id, ProjectionValue::SourceAbsent);
        assert_eq!(absent.runtime_state, ProjectionValue::SourceAbsent);
        assert_eq!(
            absent.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Ready)
        );
        assert_eq!(
            absent.metadata.authority_scope,
            ProjectionAuthorityScope::ObserveOnly
        );

        let legacy = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: "task-projection-reduction".to_owned(),
                run_id: "run-projection-reduction".to_owned(),
                agent_id: "worker-a".to_owned(),
                consumer_schema_version: 0,
                redacted_fields: Vec::new(),
                omitted_fields: Vec::new(),
            })
            .unwrap()
            .unwrap();
        assert_eq!(legacy.metadata.schema_version, 0);
        assert_eq!(legacy.metadata.source_schema_version, 1);
        assert_eq!(legacy.metadata.downgraded_from_schema_version, Some(1));
        assert_eq!(
            legacy.metadata.authority_scope,
            ProjectionAuthorityScope::ObserveOnly
        );
        assert_eq!(
            legacy.task_id,
            ProjectionValue::Value("task-projection-reduction".to_owned())
        );
        assert_eq!(
            legacy.run_id,
            ProjectionValue::Value("run-projection-reduction".to_owned())
        );
        assert_eq!(
            legacy.agent_id,
            ProjectionValue::Value("worker-a".to_owned())
        );
        assert_eq!(
            legacy.runtime_state,
            ProjectionValue::Value(RuntimeState::Idle)
        );
        assert_eq!(
            legacy.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Ready)
        );
        assert_eq!(legacy.authority_revision, ProjectionValue::Value(1));
        assert_eq!(legacy.root_agent_id, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.parent_agent_id, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.provider, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.profile, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.role, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.lease_generation, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.waiting_on, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.last_activity, ProjectionValue::SchemaDowngrade);
        assert_eq!(legacy.last_transition, ProjectionValue::SchemaDowngrade);
        assert_eq!(
            legacy.metadata.environment,
            ProjectionValue::SchemaDowngrade
        );
        assert_eq!(
            legacy.metadata.evidence_class,
            ProjectionValue::SchemaDowngrade
        );

        let serialized = serde_json::to_string(&legacy).unwrap();
        assert!(serialized.contains("SCHEMA_DOWNGRADE"));
        assert!(!serialized.contains("ALLOW"));
    }

    #[test]
    fn runtime_projection_sequence_advances_on_runtime_activity_and_survives_restart() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-projection-seq".to_owned(),
                run_id: "run-projection-seq".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:projection-seq:v1".to_owned(),
            })
            .unwrap();
        let observe =
            |runtime_state: RuntimeState, waiting_on: Option<&str>| RuntimeObservationRequest {
                task_id: "task-projection-seq".to_owned(),
                run_id: "run-projection-seq".to_owned(),
                expected_authority_revision: 1,
                root_agent_id: "root-a".to_owned(),
                parent_agent_id: Some("manager-a".to_owned()),
                agent_id: "worker-a".to_owned(),
                provider: "m365".to_owned(),
                profile: "default".to_owned(),
                role: "worker".to_owned(),
                runtime_state,
                waiting_on: waiting_on.map(str::to_owned),
                environment: "test".to_owned(),
                evidence_class: "direct-runtime".to_owned(),
                actor: "runtime-adapter".to_owned(),
                evidence_refs: vec!["runtime:worker-a".to_owned()],
            };
        let projection_request = RuntimeProjectionRequest {
            task_id: "task-projection-seq".to_owned(),
            run_id: "run-projection-seq".to_owned(),
            agent_id: "worker-a".to_owned(),
            consumer_schema_version: 1,
            redacted_fields: Vec::new(),
            omitted_fields: Vec::new(),
        };

        store
            .record_runtime_observation(observe(RuntimeState::Running, None))
            .unwrap();
        let first = store
            .runtime_projection(projection_request.clone())
            .unwrap()
            .unwrap();
        assert_eq!(first.metadata.projection_of_authority_revision, 1);
        assert_eq!(
            first.runtime_state,
            ProjectionValue::Value(RuntimeState::Running)
        );
        assert_eq!(first.waiting_on, ProjectionValue::Value(None));

        store
            .record_runtime_observation(observe(RuntimeState::Waiting, Some("tool:database")))
            .unwrap();
        let waiting = store
            .runtime_projection(projection_request.clone())
            .unwrap()
            .unwrap();
        assert_eq!(waiting.metadata.projection_of_authority_revision, 1);
        assert!(waiting.metadata.event_seq > first.metadata.event_seq);
        assert_eq!(
            waiting.runtime_state,
            ProjectionValue::Value(RuntimeState::Waiting)
        );
        assert_eq!(
            waiting.waiting_on,
            ProjectionValue::Value(Some("tool:database".to_owned()))
        );
        assert_eq!(
            waiting.parent_agent_id,
            ProjectionValue::Value(Some("manager-a".to_owned()))
        );
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        let after_restart = reopened
            .runtime_projection(projection_request)
            .unwrap()
            .unwrap();
        assert_eq!(after_restart, waiting);
    }

    #[test]
    fn runtime_observation_fails_closed_on_stale_authority_identity_rewrite_and_waiting_mismatch() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-runtime-guard".to_owned(),
                run_id: "run-runtime-guard".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:runtime-guard:v1".to_owned(),
            })
            .unwrap();
        let base = |revision: u64, runtime_state: RuntimeState, waiting_on: Option<&str>| {
            RuntimeObservationRequest {
                task_id: "task-runtime-guard".to_owned(),
                run_id: "run-runtime-guard".to_owned(),
                expected_authority_revision: revision,
                root_agent_id: "root-a".to_owned(),
                parent_agent_id: Some("manager-a".to_owned()),
                agent_id: "worker-a".to_owned(),
                provider: "m365".to_owned(),
                profile: "default".to_owned(),
                role: "worker".to_owned(),
                runtime_state,
                waiting_on: waiting_on.map(str::to_owned),
                environment: "test".to_owned(),
                evidence_class: "direct-runtime".to_owned(),
                actor: "runtime-adapter".to_owned(),
                evidence_refs: vec!["runtime:worker-a".to_owned()],
            }
        };
        store
            .record_runtime_observation(base(1, RuntimeState::Running, None))
            .unwrap();
        let projection_request = RuntimeProjectionRequest {
            task_id: "task-runtime-guard".to_owned(),
            run_id: "run-runtime-guard".to_owned(),
            agent_id: "worker-a".to_owned(),
            consumer_schema_version: 1,
            redacted_fields: Vec::new(),
            omitted_fields: Vec::new(),
        };
        let initial = store
            .runtime_projection(projection_request.clone())
            .unwrap()
            .unwrap();

        let stale = store
            .record_runtime_observation(base(0, RuntimeState::Stopped, None))
            .unwrap();
        assert_eq!(stale.outcome, DecisionOutcome::Defer);
        assert_eq!(stale.reason, "stale_authority");
        assert_eq!(
            store
                .runtime_projection(projection_request.clone())
                .unwrap()
                .unwrap(),
            initial
        );

        let mut rewritten = base(1, RuntimeState::Running, None);
        rewritten.parent_agent_id = Some("other-manager".to_owned());
        let identity_rewrite = store.record_runtime_observation(rewritten).unwrap();
        assert_eq!(identity_rewrite.outcome, DecisionOutcome::Deny);
        assert_eq!(identity_rewrite.reason, "runtime_identity_mismatch");
        assert_eq!(
            store
                .runtime_projection(projection_request.clone())
                .unwrap()
                .unwrap(),
            initial
        );

        let waiting_without_reason = store
            .record_runtime_observation(base(1, RuntimeState::Waiting, None))
            .unwrap();
        assert_eq!(waiting_without_reason.outcome, DecisionOutcome::Deny);
        assert_eq!(waiting_without_reason.reason, "waiting_reason_required");

        let running_with_wait = store
            .record_runtime_observation(base(1, RuntimeState::Running, Some("tool:db")))
            .unwrap();
        assert_eq!(running_with_wait.outcome, DecisionOutcome::Deny);
        assert_eq!(running_with_wait.reason, "waiting_reason_not_allowed");
        assert_eq!(
            store
                .runtime_projection(projection_request.clone())
                .unwrap()
                .unwrap(),
            initial
        );

        let valid_wait = store
            .record_runtime_observation(base(1, RuntimeState::Waiting, Some("tool:db")))
            .unwrap();
        assert_eq!(valid_wait.outcome, DecisionOutcome::Allow);
        assert_eq!(valid_wait.record.as_ref().unwrap().runtime_event_seq, 2);
        let waiting = store
            .runtime_projection(projection_request)
            .unwrap()
            .unwrap();
        assert!(waiting.metadata.event_seq > initial.metadata.event_seq);
        assert_eq!(
            waiting.runtime_state,
            ProjectionValue::Value(RuntimeState::Waiting)
        );
        assert_eq!(
            waiting.waiting_on,
            ProjectionValue::Value(Some("tool:db".to_owned()))
        );
    }

    fn capability_probe_request() -> CapabilityProbeRequest {
        CapabilityProbeRequest {
            adapter_id: "hindsight-adapter".to_owned(),
            adapter_version: "1.0.0".to_owned(),
            upstream_id: "hindsight".to_owned(),
            upstream_version: "0.9.1".to_owned(),
            requested_capability: "memory.retain_durable".to_owned(),
            integration_seam: CapabilityIntegrationSeam::Adapter,
            required_field_families: vec!["operation_state".to_owned()],
            required_semantics: vec!["durable_terminal".to_owned()],
            evidence: None,
        }
    }

    fn with_capability_evidence(
        mut request: CapabilityProbeRequest,
        surface_present: Option<bool>,
        version_compatible: Option<bool>,
        observed_field_families: &[&str],
        observed_semantics: &[&str],
        evidence_refs: &[&str],
    ) -> CapabilityProbeRequest {
        request.evidence = Some(CapabilityProbeEvidence {
            schema_version: 1,
            adapter_id: request.adapter_id.clone(),
            adapter_version: request.adapter_version.clone(),
            upstream_id: request.upstream_id.clone(),
            upstream_version: request.upstream_version.clone(),
            requested_capability: request.requested_capability.clone(),
            integration_seam: request.integration_seam,
            surface_present,
            version_compatible,
            observed_field_families: observed_field_families
                .iter()
                .map(|value| (*value).to_owned())
                .collect(),
            observed_semantics: observed_semantics
                .iter()
                .map(|value| (*value).to_owned())
                .collect(),
            evidence_refs: evidence_refs
                .iter()
                .map(|value| (*value).to_owned())
                .collect(),
        });
        request
    }

    #[derive(Default)]
    struct TestCapabilityProbeEvidenceVerifier {
        verified: Vec<CapabilityProbeEvidence>,
    }

    impl CapabilityProbeEvidenceVerifier for TestCapabilityProbeEvidenceVerifier {
        fn verifies(&self, evidence: &CapabilityProbeEvidence) -> bool {
            self.verified.contains(evidence)
        }
    }

    struct RejectMemoryEvidence;

    impl MemoryDurabilityEvidenceVerifier for RejectMemoryEvidence {
        fn verifies(&self, _evidence: &MemoryDurabilityEvidence) -> bool {
            false
        }
    }

    struct AcceptMemoryEvidence;

    impl MemoryDurabilityEvidenceVerifier for AcceptMemoryEvidence {
        fn verifies(&self, _evidence: &MemoryDurabilityEvidence) -> bool {
            true
        }
    }

    struct TestMemoryPort {
        retain_probe: MemoryPortProbe,
        hydrate_probe: MemoryPortProbe,
        retain: MemoryRetainResult,
        hydrate_status: MemoryHydrateStatus,
        hydrate_layer: ContextLayer,
        hydrate_authority_revision: Option<u64>,
        hydrate_content: String,
        hydrate_observed_at: Option<std::sync::Arc<Mutex<Option<OffsetDateTime>>>>,
    }

    impl MemoryPort for TestMemoryPort {
        fn probe(&self, requested_capability: &str) -> MemoryPortProbe {
            match requested_capability {
                "memory.retain_durable" => self.retain_probe.clone(),
                "memory.hydrate" => self.hydrate_probe.clone(),
                _ => panic!("unexpected capability probe: {requested_capability}"),
            }
        }

        fn retain(&self, _request: &MemoryRetainRequest) -> MemoryRetainResult {
            self.retain.clone()
        }

        fn hydrate(&self, request: &MemoryHydrateRequest) -> MemoryHydrateResult {
            if let Some(observed_at) = &self.hydrate_observed_at {
                *observed_at.lock().unwrap() = Some(OffsetDateTime::now_utc());
            }
            MemoryHydrateResult {
                hydrate_request_id: request.hydrate_request_id.clone(),
                checkpoint_id: request.checkpoint_id.clone(),
                task_id: request.task_id.clone(),
                run_id: request.run_id.clone(),
                lineage_id: request.lineage_id.clone(),
                authority_revision: self
                    .hydrate_authority_revision
                    .unwrap_or(request.authority_summary.authority_revision),
                new_context_id: request.new_context_id.clone(),
                status: self.hydrate_status,
                items: vec![HydratedContextItem {
                    memory_id: "memory-observation-1".to_owned(),
                    layer: self.hydrate_layer,
                    content: self.hydrate_content.clone(),
                    evidence_ref: "evidence:memory-observation-1".to_owned(),
                }],
                evidence_refs: vec!["evidence:hindsight-recall-1".to_owned()],
            }
        }
    }

    struct BlockingMemoryPort {
        inner: TestMemoryPort,
        hydrate_entered: std::sync::Arc<std::sync::Barrier>,
        hydrate_release: std::sync::Arc<std::sync::Barrier>,
    }

    impl MemoryPort for BlockingMemoryPort {
        fn probe(&self, requested_capability: &str) -> MemoryPortProbe {
            self.inner.probe(requested_capability)
        }

        fn retain(&self, request: &MemoryRetainRequest) -> MemoryRetainResult {
            self.inner.retain(request)
        }

        fn hydrate(&self, request: &MemoryHydrateRequest) -> MemoryHydrateResult {
            self.hydrate_entered.wait();
            self.hydrate_release.wait();
            self.inner.hydrate(request)
        }
    }

    fn verified_test_memory_port(
        task_id: &str,
        run_id: &str,
        lineage_id: &str,
        fence: &str,
        retain_request_id: &str,
        content_digest: &str,
    ) -> TestMemoryPort {
        let capability = evaluate_verified_capability_probe(with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:hindsight-capability"],
        ))
        .unwrap();
        let mut hydrate_request = capability_probe_request();
        hydrate_request.requested_capability = "memory.hydrate".to_owned();
        hydrate_request.required_field_families = vec!["context_identity".to_owned()];
        hydrate_request.required_semantics = vec!["typed_hydrate".to_owned()];
        let hydrate_capability = evaluate_verified_capability_probe(with_capability_evidence(
            hydrate_request,
            Some(true),
            Some(true),
            &["context_identity"],
            &["typed_hydrate"],
            &["evidence:hindsight-hydrate-capability"],
        ))
        .unwrap();
        let operation_id = format!("hindsight-operation-{retain_request_id}");
        TestMemoryPort {
            retain_probe: MemoryPortProbe {
                capability: capability.clone(),
                health: MemoryPortHealth::Healthy,
                evidence_refs: vec!["evidence:hindsight-health".to_owned()],
            },
            hydrate_probe: MemoryPortProbe {
                capability: hydrate_capability,
                health: MemoryPortHealth::Healthy,
                evidence_refs: vec!["evidence:hindsight-hydrate-health".to_owned()],
            },
            retain: MemoryRetainResult {
                retain_request_id: retain_request_id.to_owned(),
                task_id: task_id.to_owned(),
                run_id: run_id.to_owned(),
                lineage_id: lineage_id.to_owned(),
                authority_revision: 3,
                content_digest: content_digest.to_owned(),
                fencing_identity: fence.to_owned(),
                operation_id: operation_id.clone(),
                durability: MemoryDurabilityState::Durable,
                evidence: Some(MemoryDurabilityEvidence {
                    schema_version: 1,
                    adapter_id: capability.adapter_id,
                    adapter_version: capability.adapter_version,
                    upstream_id: capability.upstream_id,
                    upstream_version: capability.upstream_version,
                    retain_request_id: retain_request_id.to_owned(),
                    task_id: task_id.to_owned(),
                    run_id: run_id.to_owned(),
                    lineage_id: lineage_id.to_owned(),
                    authority_revision: 3,
                    content_digest: content_digest.to_owned(),
                    fencing_identity: fence.to_owned(),
                    operation_id,
                    durable_at: OffsetDateTime::now_utc(),
                    evidence_refs: vec!["evidence:hmac-retain-completed".to_owned()],
                }),
                evidence_refs: vec!["provider:retain-completed".to_owned()],
            },
            hydrate_status: MemoryHydrateStatus::Hydrated,
            hydrate_layer: ContextLayer::LongTermMemory,
            hydrate_authority_revision: None,
            hydrate_content: "selected durable memory".to_owned(),
            hydrate_observed_at: None,
        }
    }

    fn context_rotation_request(
        suffix: &str,
        task_id: &str,
        run_id: &str,
        fence: &str,
        lineage_id: &str,
        retain_request_id: &str,
    ) -> ContextRotationRequest {
        context_rotation_request_for_agent(
            suffix,
            task_id,
            run_id,
            fence,
            lineage_id,
            retain_request_id,
            &format!("agent-{suffix}"),
        )
    }

    fn context_rotation_request_for_agent(
        suffix: &str,
        task_id: &str,
        run_id: &str,
        fence: &str,
        lineage_id: &str,
        retain_request_id: &str,
        agent_id: &str,
    ) -> ContextRotationRequest {
        ContextRotationRequest {
            rotation_id: format!("rotation-{suffix}"),
            task_id: task_id.to_owned(),
            run_id: run_id.to_owned(),
            expected_authority_revision: 3,
            agent_id: agent_id.to_owned(),
            actor: agent_id.to_owned(),
            fencing_identity: fence.to_owned(),
            lineage_id: lineage_id.to_owned(),
            kanban_history_ref: format!("kanban:task-{suffix}"),
            old_context_id: format!("context-old-{suffix}"),
            new_context_id: format!("context-new-{suffix}"),
            retain_request_id: retain_request_id.to_owned(),
            memory_content_digest: "sha256:precompact-memory".to_owned(),
            memory_query: "current run requirements".to_owned(),
            selected_evidence_refs: vec!["evidence:current-run".to_owned()],
        }
    }

    fn evaluate_verified_capability_probe(
        request: CapabilityProbeRequest,
    ) -> Result<CapabilityProbeResult, GovernanceError> {
        let verifier = TestCapabilityProbeEvidenceVerifier {
            verified: request.evidence.iter().cloned().collect(),
        };
        evaluate_capability_probe(request, &verifier)
    }

    #[test]
    fn capability_probe_unverified_semantic_assertion_is_unknown() {
        let request = with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:caller-assertion"],
        );
        let result =
            evaluate_capability_probe(request, &TestCapabilityProbeEvidenceVerifier::default())
                .unwrap();

        assert_eq!(result.status, CapabilityStatus::Unknown);
        assert!(result.evidence_refs.is_empty());
    }

    #[test]
    fn capability_probe_surface_presence_without_evidence_is_unknown() {
        let request = with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &[],
        );
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.schema_version, 1);
        assert_eq!(result.status, CapabilityStatus::Unknown);
        assert_eq!(result.adapter_id, "hindsight-adapter");
        assert_eq!(result.upstream_version, "0.9.1");
        assert_eq!(result.requested_capability, "memory.retain_durable");
    }

    #[test]
    fn capability_probe_empty_semantic_contract_is_unknown() {
        let mut request = capability_probe_request();
        request.required_field_families.clear();
        request.required_semantics.clear();
        let request = with_capability_evidence(
            request,
            Some(true),
            Some(true),
            &[],
            &[],
            &["evidence:surface-exists"],
        );

        assert_eq!(
            evaluate_verified_capability_probe(request).unwrap().status,
            CapabilityStatus::Unknown
        );
    }

    #[test]
    fn capability_probe_full_evidenced_semantics_is_supported() {
        let request = with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:hindsight-operation-terminal"],
        );
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.evidence_schema_version, Some(1));
        assert_eq!(result.status, CapabilityStatus::Supported);
        assert!(result.missing_field_families.is_empty());
        assert!(result.missing_semantics.is_empty());
        assert_eq!(
            result.evidence_refs,
            vec!["evidence:hindsight-operation-terminal".to_owned()]
        );
    }

    #[test]
    fn capability_probe_incomplete_semantics_is_explicitly_degraded() {
        let mut request = capability_probe_request();
        request.required_field_families = vec![
            "receipt_identity".to_owned(),
            "operation_state".to_owned(),
            "receipt_identity".to_owned(),
        ];
        request.required_semantics =
            vec!["recall_visible".to_owned(), "durable_terminal".to_owned()];
        let request = with_capability_evidence(
            request,
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:hindsight-partial-contract"],
        );
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.status, CapabilityStatus::Degraded);
        assert_eq!(
            result.required_field_families,
            vec!["operation_state".to_owned(), "receipt_identity".to_owned()]
        );
        assert_eq!(
            result.missing_field_families,
            vec!["receipt_identity".to_owned()]
        );
        assert_eq!(result.missing_semantics, vec!["recall_visible".to_owned()]);
    }

    #[test]
    fn capability_probe_disappeared_surface_is_unsupported() {
        let mut request = capability_probe_request();
        request.adapter_id = "hermes-adapter".to_owned();
        request.upstream_id = "hermes".to_owned();
        request.upstream_version = "0.20.5".to_owned();
        request.requested_capability = "handoff.checkpoint".to_owned();
        request.integration_seam = CapabilityIntegrationSeam::Hook;
        request.required_field_families = vec!["checkpoint_identity".to_owned()];
        request.required_semantics = vec!["durable_handoff".to_owned()];
        let request = with_capability_evidence(
            request,
            Some(false),
            Some(true),
            &[],
            &[],
            &["evidence:hermes-surface-probe"],
        );
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.status, CapabilityStatus::Unsupported);
        assert_eq!(
            result.missing_field_families,
            vec!["checkpoint_identity".to_owned()]
        );
        assert_eq!(result.missing_semantics, vec!["durable_handoff".to_owned()]);
    }

    #[test]
    fn capability_probe_version_drift_is_incompatible_even_when_surface_exists() {
        let mut request = capability_probe_request();
        request.adapter_id = "semantica-adapter".to_owned();
        request.upstream_id = "semantica".to_owned();
        request.upstream_version = "0.7.0".to_owned();
        request.requested_capability = "decision.query".to_owned();
        request.required_field_families = vec!["decision_identity".to_owned()];
        request.required_semantics = vec!["provenance_bound_query".to_owned()];
        let request = with_capability_evidence(
            request,
            Some(true),
            Some(false),
            &["decision_identity"],
            &["provenance_bound_query"],
            &["evidence:semantica-version-drift"],
        );
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.status, CapabilityStatus::Incompatible);
        assert!(result.missing_field_families.is_empty());
        assert!(result.missing_semantics.is_empty());
    }

    #[test]
    fn capability_probe_mismatched_evidence_binding_is_unknown() {
        let mut request = with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:unrelated-capability"],
        );
        request.evidence.as_mut().unwrap().requested_capability = "memory.delete".to_owned();
        let result = evaluate_verified_capability_probe(request).unwrap();

        assert_eq!(result.status, CapabilityStatus::Unknown);
        assert_eq!(result.evidence_schema_version, None);
        assert!(result.evidence_refs.is_empty());
    }

    #[test]
    fn capability_probe_contract_rejects_undocumented_private_upstream_seams() {
        for integration_seam in [
            "UNDOCUMENTED_PRIVATE_DB",
            "PRIVATE_FUNCTION",
            "INTERNAL_CACHE",
        ] {
            let encoded = serde_json::json!({
                "adapterId": "hermes-adapter",
                "adapterVersion": "1.0.0",
                "upstreamId": "hermes",
                "upstreamVersion": "0.20.5",
                "requestedCapability": "handoff.checkpoint",
                "integrationSeam": integration_seam,
                "requiredFieldFamilies": ["checkpoint_identity"],
                "requiredSemantics": ["durable_handoff"],
                "evidence": {
                    "schemaVersion": 1,
                    "adapterId": "hermes-adapter",
                    "adapterVersion": "1.0.0",
                    "upstreamId": "hermes",
                    "upstreamVersion": "0.20.5",
                    "requestedCapability": "handoff.checkpoint",
                    "integrationSeam": integration_seam,
                    "surfacePresent": true,
                    "versionCompatible": true,
                    "observedFieldFamilies": ["checkpoint_identity"],
                    "observedSemantics": ["durable_handoff"],
                    "evidenceRefs": ["evidence:private-upstream-state"]
                }
            });

            assert!(serde_json::from_value::<CapabilityProbeRequest>(encoded).is_err());
        }
    }

    fn running_task(
        store: &GovernanceStore,
        suffix: &str,
        acceptance_contract_digest: String,
    ) -> (String, String, String) {
        let task_id = format!("task-{suffix}");
        let run_id = format!("run-{suffix}");
        let agent_id = format!("agent-{suffix}");
        store
            .create_task(CreateTask {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                actor: "operator".to_owned(),
                acceptance_contract_digest,
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: agent_id.clone(),
                actor: "scheduler".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:claim".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id,
                actor: "scheduler".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:start".to_owned()],
            })
            .unwrap();
        (task_id, run_id, fence)
    }

    fn running_approval_task(store: &GovernanceStore, suffix: &str) -> (String, String, String) {
        running_task(store, suffix, format!("contract-{suffix}"))
    }

    fn issue_test_approval(
        store: &GovernanceStore,
        suffix: &str,
        task_id: &str,
        run_id: &str,
        fence: &str,
        expires_at: OffsetDateTime,
        max_uses: u32,
    ) -> ApprovalGrant {
        store
            .issue_approval_grant(IssueApprovalGrantRequest {
                task_id: task_id.to_owned(),
                run_id: run_id.to_owned(),
                expected_authority_revision: 3,
                approved_actor: format!("agent-{suffix}"),
                issued_by: "operator".to_owned(),
                fencing_identity: fence.to_owned(),
                evaluation: PolicyEvaluationRequest {
                    requested_action: "deploy.production".to_owned(),
                    target_scope: "gateway:production".to_owned(),
                    policy_version: "policy-v1".to_owned(),
                    evaluator_version: "evaluator-v1".to_owned(),
                    evaluator_status: PolicyEvaluatorStatus::Resolved,
                    evidence_refs: vec!["evidence:approved-exception".to_owned()],
                    rules: vec![PolicyRule {
                        layer: PolicyLayer::ServicePolicy,
                        policy_id: "service-deploy".to_owned(),
                        exception_id: Some("exception-emergency-deploy".to_owned()),
                        requested_action: "deploy.production".to_owned(),
                        target_scope: "gateway:production".to_owned(),
                        outcome: ApprovalOutcome::Allow,
                    }],
                },
                expires_at,
                max_uses,
            })
            .unwrap()
            .grant
            .unwrap()
    }

    fn approval_consumption_request(
        grant: &ApprovalGrant,
        consumption_id: &str,
        expected_authority_revision: u64,
        actor: &str,
        target_scope: &str,
        fence: &str,
    ) -> ConsumeApprovalGrantRequest {
        ConsumeApprovalGrantRequest {
            approval_id: grant.approval_id.clone(),
            consumption_id: consumption_id.to_owned(),
            task_id: grant.task_id.clone(),
            run_id: grant.run_id.clone(),
            expected_authority_revision,
            actor: actor.to_owned(),
            permitted_action: grant.permitted_action.clone(),
            target_scope: target_scope.to_owned(),
            fencing_identity: fence.to_owned(),
            evidence_refs: vec![format!("evidence:{consumption_id}")],
        }
    }

    fn consume_test_approval(
        store: &GovernanceStore,
        grant: &ApprovalGrant,
        consumption_id: &str,
        expected_authority_revision: u64,
        actor: &str,
        target_scope: &str,
        fence: &str,
    ) -> ConsumeApprovalGrantResult {
        store
            .consume_approval_grant(approval_consumption_request(
                grant,
                consumption_id,
                expected_authority_revision,
                actor,
                target_scope,
                fence,
            ))
            .unwrap()
    }

    #[test]
    fn policy_precedence_rejects_lower_layer_relaxation() {
        let result = evaluate_policy(PolicyEvaluationRequest {
            requested_action: "deploy.production".to_owned(),
            target_scope: "gateway:production".to_owned(),
            policy_version: "policy-v1".to_owned(),
            evaluator_version: "evaluator-v1".to_owned(),
            evaluator_status: PolicyEvaluatorStatus::Resolved,
            evidence_refs: vec!["evidence:policy-v1".to_owned()],
            rules: vec![
                PolicyRule {
                    layer: PolicyLayer::CompanyRequirements,
                    policy_id: "company-production".to_owned(),
                    exception_id: None,
                    requested_action: "deploy.production".to_owned(),
                    target_scope: "gateway:production".to_owned(),
                    outcome: ApprovalOutcome::RequireUserApproval,
                },
                PolicyRule {
                    layer: PolicyLayer::TaskPolicy,
                    policy_id: "task-shortcut".to_owned(),
                    exception_id: Some("exception-task-shortcut".to_owned()),
                    requested_action: "deploy.production".to_owned(),
                    target_scope: "gateway:production".to_owned(),
                    outcome: ApprovalOutcome::Allow,
                },
            ],
        });

        assert_eq!(result.outcome, ApprovalOutcome::Deny);
        assert_eq!(result.reason, "lower_layer_relaxation");
        assert_eq!(
            result.governing_policy_id.as_deref(),
            Some("company-production")
        );
        assert_eq!(
            result.governing_layer,
            Some(PolicyLayer::CompanyRequirements)
        );
    }

    #[test]
    fn policy_precedence_allows_lower_layer_tightening() {
        let result = evaluate_policy(PolicyEvaluationRequest {
            requested_action: "tool.execute".to_owned(),
            target_scope: "terminal:workspace".to_owned(),
            policy_version: "policy-v1".to_owned(),
            evaluator_version: "evaluator-v1".to_owned(),
            evaluator_status: PolicyEvaluatorStatus::Resolved,
            evidence_refs: vec!["evidence:policy-v1".to_owned()],
            rules: vec![
                PolicyRule {
                    layer: PolicyLayer::ProviderRequirements,
                    policy_id: "provider-tools".to_owned(),
                    exception_id: None,
                    requested_action: "tool.execute".to_owned(),
                    target_scope: "terminal:workspace".to_owned(),
                    outcome: ApprovalOutcome::RequireUserApproval,
                },
                PolicyRule {
                    layer: PolicyLayer::CompanyRequirements,
                    policy_id: "company-tools".to_owned(),
                    exception_id: None,
                    requested_action: "tool.execute".to_owned(),
                    target_scope: "terminal:workspace".to_owned(),
                    outcome: ApprovalOutcome::Allow,
                },
            ],
        });

        assert_eq!(result.outcome, ApprovalOutcome::RequireUserApproval);
        assert_eq!(result.reason, "policy_resolved");
        assert_eq!(
            result.governing_policy_id.as_deref(),
            Some("provider-tools")
        );
        assert_eq!(
            result.governing_layer,
            Some(PolicyLayer::ProviderRequirements)
        );
    }

    #[test]
    fn evaluator_failures_are_typed_and_never_allow() {
        let base = PolicyEvaluationRequest {
            requested_action: "artifact.publish".to_owned(),
            target_scope: "repository:main".to_owned(),
            policy_version: "policy-v1".to_owned(),
            evaluator_version: "evaluator-v1".to_owned(),
            evaluator_status: PolicyEvaluatorStatus::Resolved,
            evidence_refs: vec!["evidence:policy-v1".to_owned()],
            rules: vec![PolicyRule {
                layer: PolicyLayer::CompanyRequirements,
                policy_id: "company-publish".to_owned(),
                exception_id: Some("exception-publish".to_owned()),
                requested_action: "artifact.publish".to_owned(),
                target_scope: "repository:main".to_owned(),
                outcome: ApprovalOutcome::Allow,
            }],
        };

        for (status, expected, reason) in [
            (
                PolicyEvaluatorStatus::Timeout,
                ApprovalOutcome::Timeout,
                "evaluator_timeout",
            ),
            (
                PolicyEvaluatorStatus::Aborted,
                ApprovalOutcome::Abort,
                "evaluator_aborted",
            ),
            (
                PolicyEvaluatorStatus::ParseFailure,
                ApprovalOutcome::Abort,
                "evaluator_parse_failure",
            ),
            (
                PolicyEvaluatorStatus::Unavailable,
                ApprovalOutcome::Abort,
                "evaluator_unavailable",
            ),
            (
                PolicyEvaluatorStatus::Unknown,
                ApprovalOutcome::Abort,
                "evaluator_unknown",
            ),
        ] {
            let mut request = base.clone();
            request.evaluator_status = status;
            let result = evaluate_policy(request);

            assert_eq!(result.outcome, expected);
            assert_eq!(result.reason, reason);
            assert_ne!(result.outcome, ApprovalOutcome::Allow);
        }
    }

    #[test]
    fn exceptional_allow_issues_and_consumes_a_durable_bound_grant() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        store
            .create_task(CreateTask {
                task_id: "task-grant".to_owned(),
                run_id: "run-grant".to_owned(),
                actor: "operator".to_owned(),
                acceptance_contract_digest: "contract-grant".to_owned(),
            })
            .unwrap();
        let claim = store
            .transition(TransitionRequest {
                task_id: "task-grant".to_owned(),
                run_id: "run-grant".to_owned(),
                expected_authority_revision: 1,
                requested_transition: TransitionKind::Claim,
                agent_id: "agent-grant".to_owned(),
                actor: "scheduler".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:claim".to_owned()],
            })
            .unwrap();
        let fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: "task-grant".to_owned(),
                run_id: "run-grant".to_owned(),
                expected_authority_revision: 2,
                requested_transition: TransitionKind::Start,
                agent_id: "agent-grant".to_owned(),
                actor: "scheduler".to_owned(),
                fencing_identity: Some(fence.clone()),
                evidence_refs: vec!["evidence:start".to_owned()],
            })
            .unwrap();

        let issued = store
            .issue_approval_grant(IssueApprovalGrantRequest {
                task_id: "task-grant".to_owned(),
                run_id: "run-grant".to_owned(),
                expected_authority_revision: 3,
                approved_actor: "agent-grant".to_owned(),
                issued_by: "operator".to_owned(),
                fencing_identity: fence.clone(),
                evaluation: PolicyEvaluationRequest {
                    requested_action: "deploy.production".to_owned(),
                    target_scope: "gateway:production".to_owned(),
                    policy_version: "policy-v1".to_owned(),
                    evaluator_version: "evaluator-v1".to_owned(),
                    evaluator_status: PolicyEvaluatorStatus::Resolved,
                    evidence_refs: vec!["evidence:approved-exception".to_owned()],
                    rules: vec![PolicyRule {
                        layer: PolicyLayer::ServicePolicy,
                        policy_id: "service-deploy".to_owned(),
                        exception_id: Some("exception-emergency-deploy".to_owned()),
                        requested_action: "deploy.production".to_owned(),
                        target_scope: "gateway:production".to_owned(),
                        outcome: ApprovalOutcome::Allow,
                    }],
                },
                expires_at: OffsetDateTime::now_utc() + time::Duration::hours(1),
                max_uses: 1,
            })
            .unwrap();
        assert_eq!(issued.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(issued.decision.reason, "approval_grant_issued");
        let grant = issued.grant.unwrap();
        assert_eq!(grant.actor, "agent-grant");
        assert_eq!(grant.policy_id, "service-deploy");
        assert_eq!(grant.exception_id, "exception-emergency-deploy");
        assert_eq!(grant.permitted_action, "deploy.production");
        assert_eq!(grant.target_scope, "gateway:production");
        assert_eq!(grant.authority_revision, 4);
        assert_eq!(grant.max_uses, 1);
        assert_eq!(grant.consumed_uses, 0);
        assert!(grant.revoked_at.is_none());

        drop(store);
        let store = GovernanceStore::open(&path).unwrap();
        assert_eq!(
            store.approval_grant(&grant.approval_id).unwrap(),
            Some(grant.clone())
        );
        let consumed = store
            .consume_approval_grant(ConsumeApprovalGrantRequest {
                approval_id: grant.approval_id.clone(),
                consumption_id: "consume-grant-1".to_owned(),
                task_id: "task-grant".to_owned(),
                run_id: "run-grant".to_owned(),
                expected_authority_revision: 4,
                actor: "agent-grant".to_owned(),
                permitted_action: "deploy.production".to_owned(),
                target_scope: "gateway:production".to_owned(),
                fencing_identity: fence.clone(),
                evidence_refs: vec!["evidence:deployment-readback".to_owned()],
            })
            .unwrap();
        assert_eq!(consumed.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(consumed.decision.reason, "approval_grant_consumed");
        assert_eq!(consumed.grant.unwrap().consumed_uses, 1);
        let consumption = consumed.consumption.unwrap();
        assert_eq!(consumption.approval_id, grant.approval_id);
        assert_eq!(consumption.consumption_id, "consume-grant-1");
        assert_eq!(consumption.authority_revision, 5);
        assert_eq!(
            store
                .authority("task-grant")
                .unwrap()
                .unwrap()
                .authority_revision,
            5
        );

        drop(store);
        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(
            reopened
                .approval_grant(&grant.approval_id)
                .unwrap()
                .unwrap()
                .consumed_uses,
            1
        );
        assert_eq!(
            reopened
                .approval_grant_consumptions(&grant.approval_id)
                .unwrap(),
            vec![consumption]
        );
        let ledger = reopened.decisions("task-grant").unwrap();
        assert_eq!(
            ledger
                .iter()
                .filter(|decision| {
                    matches!(
                        decision.requested_transition,
                        TransitionKind::IssueApprovalGrant | TransitionKind::ConsumeApprovalGrant
                    ) && decision.performed_at.is_some()
                })
                .count(),
            2
        );
    }

    #[test]
    fn revoked_approval_grant_cannot_be_consumed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "revoked");
        let grant = issue_test_approval(
            &store,
            "revoked",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            1,
        );

        let revoked = store
            .revoke_approval_grant(RevokeApprovalGrantRequest {
                approval_id: grant.approval_id.clone(),
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                actor: "agent-revoked".to_owned(),
                fencing_identity: fence.clone(),
                evidence_refs: vec!["evidence:revocation".to_owned()],
            })
            .unwrap();
        assert_eq!(revoked.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(revoked.decision.reason, "approval_grant_revoked");
        assert_eq!(revoked.grant.unwrap().authority_revision, 5);

        let rejected = store
            .consume_approval_grant(ConsumeApprovalGrantRequest {
                approval_id: grant.approval_id,
                consumption_id: "consume-revoked".to_owned(),
                task_id,
                run_id,
                expected_authority_revision: 5,
                actor: "agent-revoked".to_owned(),
                permitted_action: "deploy.production".to_owned(),
                target_scope: "gateway:production".to_owned(),
                fencing_identity: fence,
                evidence_refs: vec!["evidence:attempt".to_owned()],
            })
            .unwrap();
        assert_eq!(rejected.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(rejected.decision.reason, "approval_revoked");
        assert!(rejected.decision.performed_at.is_none());
        assert!(rejected.consumption.is_none());
    }

    #[test]
    fn user_preference_cannot_issue_permanent_authority() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "preference");

        let result = store
            .issue_approval_grant(IssueApprovalGrantRequest {
                task_id: task_id.clone(),
                run_id,
                expected_authority_revision: 3,
                approved_actor: "agent-preference".to_owned(),
                issued_by: "operator".to_owned(),
                fencing_identity: fence,
                evaluation: PolicyEvaluationRequest {
                    requested_action: "deploy.production".to_owned(),
                    target_scope: "gateway:production".to_owned(),
                    policy_version: "policy-v1".to_owned(),
                    evaluator_version: "evaluator-v1".to_owned(),
                    evaluator_status: PolicyEvaluatorStatus::Resolved,
                    evidence_refs: vec!["evidence:user-preference".to_owned()],
                    rules: vec![PolicyRule {
                        layer: PolicyLayer::UserPreference,
                        policy_id: "user-default".to_owned(),
                        exception_id: Some("preference-remember-me".to_owned()),
                        requested_action: "deploy.production".to_owned(),
                        target_scope: "gateway:production".to_owned(),
                        outcome: ApprovalOutcome::Allow,
                    }],
                },
                expires_at: OffsetDateTime::now_utc() + time::Duration::hours(1),
                max_uses: 10,
            })
            .unwrap();

        assert_eq!(result.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(result.decision.reason, "approval_authority_not_policy");
        assert!(result.decision.performed_at.is_none());
        assert!(result.grant.is_none());
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );
    }

    #[test]
    fn expanded_scope_and_fencing_mismatch_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "binding");
        let grant = issue_test_approval(
            &store,
            "binding",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            1,
        );

        let expanded = consume_test_approval(
            &store,
            &grant,
            "consume-expanded",
            4,
            "agent-binding",
            "gateway:*",
            &fence,
        );
        assert_eq!(expanded.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(expanded.decision.reason, "approval_scope_mismatch");
        assert!(expanded.consumption.is_none());

        let wrong_fence = consume_test_approval(
            &store,
            &grant,
            "consume-wrong-fence",
            4,
            "agent-binding",
            "gateway:production",
            "fence-wrong",
        );
        assert_eq!(wrong_fence.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(wrong_fence.decision.reason, "fencing_mismatch");
        assert!(wrong_fence.consumption.is_none());
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            4
        );
    }

    #[test]
    fn expired_and_exhausted_approval_grants_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "expired");
        let expiring = issue_test_approval(
            &store,
            "expired",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            1,
        );
        let expired = store
            .consume_approval_grant_at(
                approval_consumption_request(
                    &expiring,
                    "consume-expired",
                    4,
                    "agent-expired",
                    "gateway:production",
                    &fence,
                ),
                expiring.expires_at,
            )
            .unwrap();
        assert_eq!(expired.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(expired.decision.reason, "approval_expired");

        let (task_id, run_id, fence) = running_approval_task(&store, "exhausted");
        let single_use = issue_test_approval(
            &store,
            "exhausted",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            1,
        );
        let consumed = consume_test_approval(
            &store,
            &single_use,
            "consume-first",
            4,
            "agent-exhausted",
            "gateway:production",
            &fence,
        );
        assert_eq!(consumed.decision.outcome, DecisionOutcome::Allow);
        let exhausted = consume_test_approval(
            &store,
            &single_use,
            "consume-second",
            5,
            "agent-exhausted",
            "gateway:production",
            &fence,
        );
        assert_eq!(exhausted.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(exhausted.decision.reason, "approval_exhausted");
        assert!(exhausted.consumption.is_none());
    }

    #[test]
    fn approval_consumption_id_replay_is_rejected() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "replay");
        let grant = issue_test_approval(
            &store,
            "replay",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            2,
        );
        let first = consume_test_approval(
            &store,
            &grant,
            "consume-replay",
            4,
            "agent-replay",
            "gateway:production",
            &fence,
        );
        assert_eq!(first.decision.outcome, DecisionOutcome::Allow);
        let replayed = consume_test_approval(
            &store,
            &grant,
            "consume-replay",
            5,
            "agent-replay",
            "gateway:production",
            &fence,
        );
        assert_eq!(replayed.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(replayed.decision.reason, "approval_replayed");
        assert!(replayed.consumption.is_none());
        assert_eq!(
            store
                .approval_grant(&grant.approval_id)
                .unwrap()
                .unwrap()
                .consumed_uses,
            1
        );
    }

    #[test]
    fn approval_bound_to_an_older_authority_revision_is_rejected() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "revision");
        let grant = issue_test_approval(
            &store,
            "revision",
            &task_id,
            &run_id,
            &fence,
            OffsetDateTime::now_utc() + time::Duration::hours(1),
            1,
        );
        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                agent_id: "agent-revision".to_owned(),
                actor: "agent-revision".to_owned(),
                fencing_identity: fence,
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "upstream-unavailable".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("upstream".to_owned(), "memory".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "memory".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "memory".to_owned(),
                    identity: "unavailable".to_owned(),
                }],
                evidence_refs: vec!["evidence:memory-unavailable".to_owned()],
            })
            .unwrap();
        let blocker = blocked.blocker.unwrap();
        let resumed = store
            .resume_blocker(ResumeBlockerRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 5,
                blocker_id: blocker.blocker_id,
                expected_blocker_generation: blocker.generation,
                actor: "scheduler".to_owned(),
                evidence: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "memory".to_owned(),
                    identity: "available".to_owned(),
                }],
                evidence_refs: vec!["evidence:memory-available".to_owned()],
            })
            .unwrap();
        assert_eq!(resumed.status, ResumeStatus::Resumed);
        let claim = store
            .transition(TransitionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 6,
                requested_transition: TransitionKind::Claim,
                agent_id: "agent-revision".to_owned(),
                actor: "scheduler".to_owned(),
                fencing_identity: None,
                evidence_refs: vec!["evidence:reclaim".to_owned()],
            })
            .unwrap();
        let new_fence = claim.fencing_identity.unwrap();
        store
            .transition(TransitionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 7,
                requested_transition: TransitionKind::Start,
                agent_id: "agent-revision".to_owned(),
                actor: "scheduler".to_owned(),
                fencing_identity: Some(new_fence.clone()),
                evidence_refs: vec!["evidence:restart".to_owned()],
            })
            .unwrap();

        let rejected = consume_test_approval(
            &store,
            &grant,
            "consume-old-revision",
            8,
            "agent-revision",
            "gateway:production",
            &new_fence,
        );
        assert_eq!(rejected.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(rejected.decision.reason, "approval_revision_mismatch");
        assert!(rejected.consumption.is_none());
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            8
        );
    }

    #[test]
    fn policy_rule_binding_mismatch_and_unresolved_rule_outcome_abort() {
        let mut request = PolicyEvaluationRequest {
            requested_action: "deploy.production".to_owned(),
            target_scope: "gateway:production".to_owned(),
            policy_version: "policy-v1".to_owned(),
            evaluator_version: "evaluator-v1".to_owned(),
            evaluator_status: PolicyEvaluatorStatus::Resolved,
            evidence_refs: vec!["evidence:policy".to_owned()],
            rules: vec![PolicyRule {
                layer: PolicyLayer::CompanyRequirements,
                policy_id: "company-deploy".to_owned(),
                exception_id: None,
                requested_action: "deploy.staging".to_owned(),
                target_scope: "gateway:production".to_owned(),
                outcome: ApprovalOutcome::Allow,
            }],
        };
        let mismatched = evaluate_policy(request.clone());
        assert_eq!(mismatched.outcome, ApprovalOutcome::Abort);
        assert_eq!(mismatched.reason, "policy_rule_binding_mismatch");

        request.rules[0].requested_action = request.requested_action.clone();
        request.rules[0].outcome = ApprovalOutcome::Timeout;
        let unresolved = evaluate_policy(request);
        assert_eq!(unresolved.outcome, ApprovalOutcome::Abort);
        assert_eq!(unresolved.reason, "invalid_policy_outcome");
    }

    #[test]
    fn duplicate_rules_at_same_policy_layer_abort_regardless_of_order() {
        let rule_a = PolicyRule {
            layer: PolicyLayer::CompanyRequirements,
            policy_id: "company-a".to_owned(),
            exception_id: Some("exception-a".to_owned()),
            requested_action: "deploy.production".to_owned(),
            target_scope: "gateway:production".to_owned(),
            outcome: ApprovalOutcome::Allow,
        };
        let rule_b = PolicyRule {
            policy_id: "company-b".to_owned(),
            exception_id: Some("exception-b".to_owned()),
            ..rule_a.clone()
        };
        for rules in [
            vec![rule_a.clone(), rule_b.clone()],
            vec![rule_b.clone(), rule_a.clone()],
        ] {
            let result = evaluate_policy(PolicyEvaluationRequest {
                requested_action: "deploy.production".to_owned(),
                target_scope: "gateway:production".to_owned(),
                policy_version: "policy-v1".to_owned(),
                evaluator_version: "evaluator-v1".to_owned(),
                evaluator_status: PolicyEvaluatorStatus::Resolved,
                evidence_refs: vec!["evidence:duplicate-layer".to_owned()],
                rules,
            });
            assert_eq!(result.outcome, ApprovalOutcome::Abort);
            assert_eq!(result.reason, "duplicate_policy_layer");
            assert!(result.governing_policy_id.is_none());
            assert!(result.exception_id.is_none());
        }
    }

    #[test]
    fn memory_transport_acceptance_is_never_durable_without_provider_evidence() {
        let capability = evaluate_verified_capability_probe(with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:memory-port-capability"],
        ))
        .unwrap();
        let request = MemoryRetainRequest {
            retain_request_id: "retain-1".to_owned(),
            task_id: "task-memory".to_owned(),
            run_id: "run-memory".to_owned(),
            lineage_id: "lineage-memory".to_owned(),
            authority_revision: 3,
            content_digest: "sha256:memory-input".to_owned(),
            fencing_identity: "fence-memory".to_owned(),
            evidence_refs: vec!["evidence:pre-compact".to_owned()],
        };

        for durability in [
            MemoryDurabilityState::Accepted,
            MemoryDurabilityState::Queued,
            MemoryDurabilityState::Claimed,
            MemoryDurabilityState::Processing,
        ] {
            let evaluated = evaluate_memory_retain(
                &capability,
                &request,
                MemoryRetainResult {
                    retain_request_id: request.retain_request_id.clone(),
                    task_id: request.task_id.clone(),
                    run_id: request.run_id.clone(),
                    lineage_id: request.lineage_id.clone(),
                    authority_revision: request.authority_revision,
                    content_digest: request.content_digest.clone(),
                    fencing_identity: request.fencing_identity.clone(),
                    operation_id: "hindsight-operation-1".to_owned(),
                    durability,
                    evidence: None,
                    evidence_refs: vec!["provider:http-200".to_owned()],
                },
                &RejectMemoryEvidence,
            )
            .unwrap();
            assert_eq!(evaluated.durability, durability);
            assert!(!evaluated.is_durable);
            assert_eq!(evaluated.reason, "memory_not_durable");
            assert!(evaluated.provider_evidence_refs.is_empty());
        }
    }

    #[test]
    fn verified_memory_durability_preserves_typed_capability_loss() {
        let capability = evaluate_verified_capability_probe(with_capability_evidence(
            capability_probe_request(),
            Some(true),
            Some(true),
            &["operation_state"],
            &["durable_terminal"],
            &["evidence:memory-port-capability"],
        ))
        .unwrap();
        let request = MemoryRetainRequest {
            retain_request_id: "retain-verified".to_owned(),
            task_id: "task-memory".to_owned(),
            run_id: "run-memory".to_owned(),
            lineage_id: "lineage-memory".to_owned(),
            authority_revision: 3,
            content_digest: "sha256:memory-input".to_owned(),
            fencing_identity: "fence-memory".to_owned(),
            evidence_refs: vec!["evidence:pre-compact".to_owned()],
        };
        let result = MemoryRetainResult {
            retain_request_id: request.retain_request_id.clone(),
            task_id: request.task_id.clone(),
            run_id: request.run_id.clone(),
            lineage_id: request.lineage_id.clone(),
            authority_revision: request.authority_revision,
            content_digest: request.content_digest.clone(),
            fencing_identity: request.fencing_identity.clone(),
            operation_id: "hindsight-operation-verified".to_owned(),
            durability: MemoryDurabilityState::Durable,
            evidence: Some(MemoryDurabilityEvidence {
                schema_version: 1,
                adapter_id: capability.adapter_id.clone(),
                adapter_version: capability.adapter_version.clone(),
                upstream_id: capability.upstream_id.clone(),
                upstream_version: capability.upstream_version.clone(),
                retain_request_id: request.retain_request_id.clone(),
                task_id: request.task_id.clone(),
                run_id: request.run_id.clone(),
                lineage_id: request.lineage_id.clone(),
                authority_revision: request.authority_revision,
                content_digest: request.content_digest.clone(),
                fencing_identity: request.fencing_identity.clone(),
                operation_id: "hindsight-operation-verified".to_owned(),
                durable_at: OffsetDateTime::now_utc(),
                evidence_refs: vec!["evidence:hmac-retain-completed".to_owned()],
            }),
            evidence_refs: vec!["provider:retain-completed".to_owned()],
        };
        let durable =
            evaluate_memory_retain(&capability, &request, result.clone(), &AcceptMemoryEvidence)
                .unwrap();
        assert!(durable.is_durable);
        assert_eq!(durable.durability, MemoryDurabilityState::Durable);
        assert_eq!(
            durable.provider_evidence_refs,
            vec!["evidence:hmac-retain-completed".to_owned()]
        );

        let mut empty_evidence = result.clone();
        empty_evidence
            .evidence
            .as_mut()
            .unwrap()
            .evidence_refs
            .clear();
        let rejected =
            evaluate_memory_retain(&capability, &request, empty_evidence, &AcceptMemoryEvidence)
                .unwrap();
        assert!(!rejected.is_durable);
        assert_eq!(rejected.durability, MemoryDurabilityState::Failed);
        assert_eq!(rejected.reason, "memory_durability_evidence_required");
        assert!(rejected.provider_evidence_refs.is_empty());

        for (status, expected_durability, reason) in [
            (
                CapabilityStatus::Degraded,
                MemoryDurabilityState::Degraded,
                "memory_capability_degraded",
            ),
            (
                CapabilityStatus::Unsupported,
                MemoryDurabilityState::Unsupported,
                "memory_capability_unsupported",
            ),
            (
                CapabilityStatus::Incompatible,
                MemoryDurabilityState::Unsupported,
                "memory_capability_incompatible",
            ),
            (
                CapabilityStatus::Unknown,
                MemoryDurabilityState::Unknown,
                "memory_capability_unknown",
            ),
        ] {
            let mut unavailable = capability.clone();
            unavailable.status = status;
            let evaluated = evaluate_memory_retain(
                &unavailable,
                &request,
                result.clone(),
                &AcceptMemoryEvidence,
            )
            .unwrap();
            assert_eq!(evaluated.capability_status, status);
            assert_eq!(evaluated.durability, expected_durability);
            assert_eq!(evaluated.reason, reason);
            assert!(!evaluated.is_durable);
            assert!(evaluated.provider_evidence_refs.is_empty());
        }
    }

    #[test]
    fn context_rotation_persists_checkpoint_and_preserves_authority_identity() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let (task_id, run_id, fence) = running_approval_task(&store, "context");
        let retain_request_id = "retain-context-1".to_owned();
        let lineage_id = "lineage-context-1".to_owned();
        let hydrate_observed_at = std::sync::Arc::new(Mutex::new(None));
        let mut port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        port.hydrate_observed_at = Some(hydrate_observed_at.clone());

        let rotated = store
            .rotate_context(
                context_rotation_request(
                    "context",
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap();

        assert_eq!(rotated.decision.outcome, DecisionOutcome::Allow);
        assert_eq!(rotated.decision.reason, "context_rotated");
        assert_eq!(rotated.decision.authority_before, 3);
        assert_eq!(rotated.decision.authority_after, 3);
        let checkpoint = rotated.checkpoint.unwrap();
        assert_eq!(
            checkpoint.phase_trace,
            vec![
                ContextLifecyclePhase::PreCompact,
                ContextLifecyclePhase::RetainDurable,
                ContextLifecyclePhase::ContextCheckpoint,
                ContextLifecyclePhase::NewContext,
                ContextLifecyclePhase::TypedHydrate,
                ContextLifecyclePhase::PostCompactVerify,
            ]
        );
        assert_eq!(checkpoint.phase, ContextLifecyclePhase::PostCompactVerify);
        assert_eq!(checkpoint.authority_summary.task_id, task_id);
        assert_eq!(checkpoint.authority_summary.run_id, run_id);
        assert_eq!(checkpoint.authority_summary.authority_revision, 3);
        assert_eq!(checkpoint.authority_summary.fencing_identity, fence);
        assert_eq!(checkpoint.kanban_history_ref, "kanban:task-context");
        assert!(checkpoint.verified_at.is_some());
        assert!(checkpoint.verified_at.unwrap() >= hydrate_observed_at.lock().unwrap().unwrap());
        let hydration = rotated.hydration.unwrap();
        assert_eq!(hydration.status, MemoryHydrateStatus::Hydrated);
        assert!(
            hydration
                .items
                .iter()
                .all(|item| item.layer == ContextLayer::LongTermMemory)
        );
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );

        drop(store);
        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(
            reopened
                .context_checkpoint(&checkpoint.checkpoint_id)
                .unwrap(),
            Some(checkpoint)
        );
    }

    #[test]
    fn context_rotation_requires_exact_hydrate_capability_from_same_provider() {
        for (index, status, reason) in [
            (0, CapabilityStatus::Degraded, "memory_capability_degraded"),
            (
                1,
                CapabilityStatus::Unsupported,
                "memory_capability_unsupported",
            ),
            (
                2,
                CapabilityStatus::Incompatible,
                "memory_capability_incompatible",
            ),
            (3, CapabilityStatus::Unknown, "memory_capability_unknown"),
        ] {
            let root = tempfile::tempdir().unwrap();
            let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
            let suffix = format!("hydrate-capability-{index}");
            let (task_id, run_id, fence) = running_approval_task(&store, &suffix);
            let lineage_id = format!("lineage-{suffix}");
            let retain_request_id = format!("retain-{suffix}");
            let mut port = verified_test_memory_port(
                &task_id,
                &run_id,
                &lineage_id,
                &fence,
                &retain_request_id,
                "sha256:precompact-memory",
            );
            port.hydrate_probe.capability.status = status;

            let rotated = store
                .rotate_context(
                    context_rotation_request(
                        &suffix,
                        &task_id,
                        &run_id,
                        &fence,
                        &lineage_id,
                        &retain_request_id,
                    ),
                    &port,
                    &AcceptMemoryEvidence,
                )
                .unwrap();

            assert_eq!(rotated.decision.outcome, DecisionOutcome::Defer);
            assert_eq!(rotated.decision.reason, reason);
            assert!(rotated.checkpoint.is_none());
            assert!(rotated.hydration.is_none());
        }

        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let suffix = "hydrate-provider-mismatch";
        let (task_id, run_id, fence) = running_approval_task(&store, suffix);
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let mut port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        port.hydrate_probe.capability.adapter_version = "2.0.0".to_owned();
        let rotated = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap();
        assert_eq!(rotated.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(rotated.decision.reason, "memory_provider_binding_mismatch");
        assert!(rotated.checkpoint.is_none());
        assert!(rotated.hydration.is_none());
    }

    #[test]
    fn malformed_hydrate_marks_durable_checkpoint_failed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let suffix = "hydrate-invalid";
        let (task_id, run_id, fence) = running_approval_task(&store, suffix);
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let mut port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        port.hydrate_content.clear();

        let rotated = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap();

        assert_eq!(rotated.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(rotated.decision.reason, "memory_hydrate_invalid");
        assert!(rotated.hydration.is_none());
        let checkpoint = rotated.checkpoint.unwrap();
        assert_eq!(checkpoint.phase, ContextLifecyclePhase::Failed);
        assert!(checkpoint.verified_at.is_none());
        assert_eq!(
            store.decisions(&task_id).unwrap().last(),
            Some(&rotated.decision)
        );
        drop(store);
        assert_eq!(
            GovernanceStore::open(&path)
                .unwrap()
                .context_checkpoint(&checkpoint.checkpoint_id)
                .unwrap(),
            Some(checkpoint)
        );
    }

    #[test]
    fn authority_change_during_hydrate_never_returns_memory_payload() {
        let root = tempfile::tempdir().unwrap();
        let store = std::sync::Arc::new(
            GovernanceStore::open(root.path().join("agent-governance.json")).unwrap(),
        );
        let suffix = "hydrate-authority-race";
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        let (task_id, run_id, fence) = running_task(&store, suffix, contract.digest());
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let hydrate_entered = std::sync::Arc::new(std::sync::Barrier::new(2));
        let hydrate_release = std::sync::Arc::new(std::sync::Barrier::new(2));
        let port = BlockingMemoryPort {
            inner: verified_test_memory_port(
                &task_id,
                &run_id,
                &lineage_id,
                &fence,
                &retain_request_id,
                "sha256:precompact-memory",
            ),
            hydrate_entered: hydrate_entered.clone(),
            hydrate_release: hydrate_release.clone(),
        };
        let rotation_request = context_rotation_request(
            suffix,
            &task_id,
            &run_id,
            &fence,
            &lineage_id,
            &retain_request_id,
        );
        let rotating_store = store.clone();
        let rotating = std::thread::spawn(move || {
            rotating_store
                .rotate_context(rotation_request, &port, &AcceptMemoryEvidence)
                .unwrap()
        });
        hydrate_entered.wait();

        let begun = store
            .begin_completion(BeginCompletionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 3,
                agent_id: format!("agent-{suffix}"),
                actor: format!("agent-{suffix}"),
                fencing_identity: fence.clone(),
                source: CompletionIntentSource::AgentIntent,
                contract,
                observation: CompletionObservation {
                    observed_authority_revision: 3,
                    fencing_identity: fence,
                    acceptance_satisfied: true,
                    active_child_ids: Vec::new(),
                    pending_consequential_mutation_ids: Vec::new(),
                    mutation_receipts: Vec::new(),
                    artifact_verifications: Vec::new(),
                    policy_state: CompletionGateState::Allow,
                    approval_state: ApprovalState::NotRequired,
                    memory_state: MemoryDurabilityState::NotRequired,
                },
                evidence_refs: vec!["evidence:completion".to_owned()],
            })
            .unwrap();
        assert_eq!(begun.decision.outcome, DecisionOutcome::Allow);
        hydrate_release.wait();

        let rotated = rotating.join().unwrap();
        assert_eq!(rotated.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(rotated.decision.reason, "stale_authority");
        assert!(rotated.hydration.is_none());
        assert_eq!(
            rotated.checkpoint.unwrap().phase,
            ContextLifecyclePhase::Failed
        );
    }

    #[test]
    fn hydrate_cannot_reconstruct_kanban_or_acp_authority() {
        for (index, layer) in [
            ContextLayer::KanbanDurableHistory,
            ContextLayer::LiveModelContext,
            ContextLayer::AcpAuthority,
        ]
        .into_iter()
        .enumerate()
        {
            let root = tempfile::tempdir().unwrap();
            let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
            let suffix = format!("forbidden-hydrate-{index}");
            let (task_id, run_id, fence) = running_approval_task(&store, &suffix);
            let lineage_id = format!("lineage-{suffix}");
            let retain_request_id = format!("retain-{suffix}");
            let mut port = verified_test_memory_port(
                &task_id,
                &run_id,
                &lineage_id,
                &fence,
                &retain_request_id,
                "sha256:precompact-memory",
            );
            port.hydrate_layer = layer;

            let rotated = store
                .rotate_context(
                    context_rotation_request(
                        &suffix,
                        &task_id,
                        &run_id,
                        &fence,
                        &lineage_id,
                        &retain_request_id,
                    ),
                    &port,
                    &AcceptMemoryEvidence,
                )
                .unwrap();

            assert_eq!(rotated.decision.outcome, DecisionOutcome::Deny);
            assert_eq!(rotated.decision.reason, "memory_hydrate_layer_forbidden");
            let checkpoint = rotated.checkpoint.unwrap();
            assert_eq!(checkpoint.phase, ContextLifecyclePhase::Failed);
            assert!(checkpoint.verified_at.is_none());
            assert_eq!(
                store
                    .authority(&task_id)
                    .unwrap()
                    .unwrap()
                    .authority_revision,
                3
            );
        }

        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let suffix = "hydrate-authority-mismatch";
        let (task_id, run_id, fence) = running_approval_task(&store, suffix);
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let mut port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        port.hydrate_authority_revision = Some(99);

        let rotated = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap();

        assert_eq!(rotated.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(rotated.decision.reason, "memory_hydrate_binding_mismatch");
        assert_eq!(
            rotated.checkpoint.unwrap().phase,
            ContextLifecyclePhase::Failed
        );
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            3
        );
    }

    fn handoff_capability() -> CapabilityProbeResult {
        let mut request = capability_probe_request();
        request.adapter_id = "hermes-adapter".to_owned();
        request.upstream_id = "hermes".to_owned();
        request.upstream_version = "0.20.5".to_owned();
        request.requested_capability = "handoff.checkpoint".to_owned();
        request.integration_seam = CapabilityIntegrationSeam::Hook;
        request.required_field_families = vec!["checkpoint_identity".to_owned()];
        request.required_semantics =
            vec!["durable_handoff".to_owned(), "fenced_release".to_owned()];
        evaluate_verified_capability_probe(with_capability_evidence(
            request,
            Some(true),
            Some(true),
            &["checkpoint_identity"],
            &["durable_handoff", "fenced_release"],
            &["evidence:hermes-handoff-capability"],
        ))
        .unwrap()
    }

    fn weakened_handoff_capability() -> CapabilityProbeResult {
        let mut capability = handoff_capability();
        capability
            .required_semantics
            .retain(|semantic| semantic != "fenced_release");
        capability
    }

    fn prepared_handoff(
        store: &GovernanceStore,
        suffix: &str,
    ) -> (
        CompletionContract,
        String,
        String,
        String,
        ContextCheckpoint,
        TestMemoryPort,
    ) {
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: true,
        };
        let (task_id, run_id, fence) = running_task(store, suffix, contract.digest());
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        let checkpoint = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap()
            .checkpoint
            .unwrap();
        (contract, task_id, run_id, fence, checkpoint, port)
    }

    fn prepared_begin_handoff_request(
        suffix: &str,
        contract: CompletionContract,
        task_id: &str,
        run_id: &str,
        fence: &str,
        context_checkpoint_id: &str,
    ) -> BeginHandoffRequest {
        BeginHandoffRequest {
            task_id: task_id.to_owned(),
            run_id: run_id.to_owned(),
            expected_authority_revision: 3,
            lineage_id: format!("lineage-{suffix}"),
            root_agent_id: "agent-root".to_owned(),
            parent_agent_id: "agent-parent".to_owned(),
            old_owner_agent_id: format!("agent-{suffix}"),
            replacement_agent_id: format!("replacement-{suffix}"),
            actor: format!("agent-{suffix}"),
            fencing_identity: fence.to_owned(),
            contract,
            handoff_capability: handoff_capability(),
            blocker_evidence_baseline: Vec::new(),
            pending_consequential_mutation_ids: Vec::new(),
            mutation_receipts: Vec::new(),
            context_checkpoint_id: context_checkpoint_id.to_owned(),
            evidence_refs: vec!["evidence:handoff-requested".to_owned()],
        }
    }

    #[test]
    fn handoff_lifecycle_is_durable_fenced_and_never_completes_the_task() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: vec!["mutation-before-handoff".to_owned()],
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: true,
        };
        let contract_digest = contract.digest();
        let (task_id, run_id, old_fence) = running_task(&store, "handoff", contract_digest.clone());
        let lineage_id = "lineage-handoff";
        let retain_request_id = "retain-handoff";
        let port = verified_test_memory_port(
            &task_id,
            &run_id,
            lineage_id,
            &old_fence,
            retain_request_id,
            "sha256:precompact-memory",
        );
        let context = store
            .rotate_context(
                context_rotation_request(
                    "handoff",
                    &task_id,
                    &run_id,
                    &old_fence,
                    lineage_id,
                    retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap()
            .checkpoint
            .unwrap();

        let beginning = store
            .begin_handoff(BeginHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 3,
                lineage_id: lineage_id.to_owned(),
                root_agent_id: "agent-root".to_owned(),
                parent_agent_id: "agent-parent".to_owned(),
                old_owner_agent_id: "agent-handoff".to_owned(),
                replacement_agent_id: "agent-replacement".to_owned(),
                actor: "agent-handoff".to_owned(),
                fencing_identity: old_fence.clone(),
                contract,
                handoff_capability: handoff_capability(),
                blocker_evidence_baseline: Vec::new(),
                pending_consequential_mutation_ids: vec!["mutation-pending".to_owned()],
                mutation_receipts: vec![MutationReceipt {
                    mutation_id: "mutation-before-handoff".to_owned(),
                    receipt_id: "receipt-before-handoff".to_owned(),
                    authority_revision: 3,
                    fencing_identity: old_fence.clone(),
                    durability: MutationDurability::Durable,
                }],
                context_checkpoint_id: context.checkpoint_id.clone(),
                evidence_refs: vec!["evidence:handoff-requested".to_owned()],
            })
            .unwrap();
        assert_eq!(beginning.decision.outcome, DecisionOutcome::Allow);
        let checkpoint = beginning.checkpoint.unwrap();
        assert_eq!(checkpoint.state, HandoffCheckpointState::Suspending);
        assert_eq!(checkpoint.old_ownership_generation, 1);
        assert_eq!(checkpoint.source_authority_revision, 3);
        assert_eq!(checkpoint.suspending_authority_revision, 4);
        let suspending = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(suspending.lifecycle_state, LifecycleState::Suspending);
        assert_eq!(suspending.owner_agent_id.as_deref(), Some("agent-handoff"));
        assert_eq!(
            suspending.fencing_identity.as_deref(),
            Some(old_fence.as_str())
        );
        assert_eq!(suspending.acceptance_contract_digest, contract_digest);
        let projection = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                agent_id: "agent-handoff".to_owned(),
                consumer_schema_version: 1,
                redacted_fields: Vec::new(),
                omitted_fields: Vec::new(),
            })
            .unwrap()
            .unwrap();
        assert_eq!(
            projection.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Suspending)
        );
        assert_eq!(projection.lease_generation, ProjectionValue::Value(1));
        assert_eq!(
            projection.waiting_on,
            ProjectionValue::Value(Some("handoff:suspending".to_owned()))
        );

        let suspended = store
            .suspend_handoff(SuspendHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                checkpoint_id: checkpoint.checkpoint_id.clone(),
                old_owner_agent_id: "agent-handoff".to_owned(),
                actor: "agent-handoff".to_owned(),
                fencing_identity: old_fence.clone(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:state-and-ledger-flushed".to_owned()],
            })
            .unwrap();
        assert_eq!(suspended.decision.outcome, DecisionOutcome::Allow);
        let checkpoint = suspended.checkpoint.unwrap();
        assert_eq!(checkpoint.state, HandoffCheckpointState::Suspended);
        assert_eq!(checkpoint.suspended_authority_revision, Some(5));
        assert!(checkpoint.released_at.is_some());
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Suspended);
        assert!(authority.owner_agent_id.is_none());
        assert!(authority.fencing_identity.is_none());
        let projection = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                agent_id: "agent-handoff".to_owned(),
                consumer_schema_version: 1,
                redacted_fields: Vec::new(),
                omitted_fields: Vec::new(),
            })
            .unwrap()
            .unwrap();
        assert_eq!(
            projection.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Suspended)
        );
        assert_eq!(projection.lease_generation, ProjectionValue::Value(1));
        assert_eq!(
            projection.waiting_on,
            ProjectionValue::Value(Some("handoff:suspended".to_owned()))
        );
        drop(store);

        let store = GovernanceStore::open(&path).unwrap();
        let durable_checkpoint = store
            .handoff_checkpoint(&checkpoint.checkpoint_id)
            .unwrap()
            .unwrap();
        assert_eq!(durable_checkpoint, checkpoint);
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Suspended
        );

        let acquired = store
            .acquire_handoff(AcquireHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 5,
                checkpoint_id: checkpoint.checkpoint_id.clone(),
                replacement_agent_id: "agent-replacement".to_owned(),
                actor: "scheduler".to_owned(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:replacement-ready".to_owned()],
            })
            .unwrap();
        assert_eq!(acquired.decision.outcome, DecisionOutcome::Allow);
        let new_fence = acquired.fencing_identity.unwrap();
        assert_ne!(new_fence, old_fence);
        let checkpoint = acquired.checkpoint.unwrap();
        assert_eq!(checkpoint.state, HandoffCheckpointState::Resuming);
        assert_eq!(checkpoint.resuming_authority_revision, Some(6));
        assert_eq!(checkpoint.new_ownership_generation, Some(2));
        assert_eq!(
            checkpoint.new_fencing_identity.as_deref(),
            Some(new_fence.as_str())
        );
        assert_eq!(checkpoint.lineage_id, lineage_id);
        assert_eq!(checkpoint.root_agent_id, "agent-root");
        assert_eq!(checkpoint.parent_agent_id, "agent-parent");
        assert_eq!(
            checkpoint.pending_consequential_mutation_ids,
            vec!["mutation-pending".to_owned()]
        );
        assert_eq!(checkpoint.mutation_receipts.len(), 1);
        assert_eq!(
            checkpoint.memory_checkpoint_id.as_deref(),
            Some(context.checkpoint_id.as_str())
        );

        let projection = store
            .runtime_projection(RuntimeProjectionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                agent_id: "agent-replacement".to_owned(),
                consumer_schema_version: 1,
                redacted_fields: Vec::new(),
                omitted_fields: Vec::new(),
            })
            .unwrap()
            .unwrap();
        assert_eq!(
            projection.lifecycle_state,
            ProjectionValue::Value(LifecycleState::Resuming)
        );
        assert_eq!(projection.lease_generation, ProjectionValue::Value(2));
        assert_eq!(
            projection.waiting_on,
            ProjectionValue::Value(Some("handoff:resuming".to_owned()))
        );

        let resumed = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id: run_id.clone(),
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id.clone(),
                    replacement_agent_id: "agent-replacement".to_owned(),
                    actor: "agent-replacement".to_owned(),
                    fencing_identity: new_fence.clone(),
                    new_context_id: "context-resumed-handoff".to_owned(),
                    memory_query: "resume the exact handoff".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:typed-hydrate-requested".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert!(resumed.hydration.is_some());
        assert_eq!(
            resumed.checkpoint.unwrap().state,
            HandoffCheckpointState::Resumed
        );
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Running);
        assert_eq!(authority.authority_revision, 7);
        assert_eq!(
            authority.owner_agent_id.as_deref(),
            Some("agent-replacement")
        );
        assert_eq!(
            authority.fencing_identity.as_deref(),
            Some(new_fence.as_str())
        );
        assert_eq!(authority.acceptance_contract_digest, contract_digest);
        assert!(authority.active_handoff_checkpoint_id.is_none());
        assert!(authority.active_completion_id.is_none());
        drop(store);

        let reopened = GovernanceStore::open(&path).unwrap();
        assert_eq!(
            reopened
                .handoff_checkpoint(&checkpoint.checkpoint_id)
                .unwrap()
                .unwrap()
                .state,
            HandoffCheckpointState::Resumed
        );
        assert_eq!(
            reopened
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .lifecycle_state,
            LifecycleState::Running
        );
    }

    #[test]
    fn handoff_capability_loss_is_typed_and_never_weakens_the_requirement() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, fence, context, _) =
            prepared_handoff(&store, "handoff-capability");

        for (status, reason) in [
            (CapabilityStatus::Degraded, "handoff_capability_degraded"),
            (
                CapabilityStatus::Unsupported,
                "handoff_capability_unsupported",
            ),
            (
                CapabilityStatus::Incompatible,
                "handoff_capability_incompatible",
            ),
            (CapabilityStatus::Unknown, "handoff_capability_unknown"),
        ] {
            let mut request = prepared_begin_handoff_request(
                "handoff-capability",
                contract.clone(),
                &task_id,
                &run_id,
                &fence,
                &context.checkpoint_id,
            );
            request.handoff_capability.status = status;
            let result = store.begin_handoff(request).unwrap();

            assert_eq!(result.decision.outcome, DecisionOutcome::Defer);
            assert_eq!(result.decision.reason, reason);
            assert!(result.decision.performed_at.is_none());
            assert!(result.checkpoint.is_none());
            let authority = store.authority(&task_id).unwrap().unwrap();
            assert_eq!(authority.lifecycle_state, LifecycleState::Running);
            assert_eq!(authority.authority_revision, 3);
            assert!(authority.active_handoff_checkpoint_id.is_none());
        }
    }

    #[test]
    fn capability_loss_at_each_handoff_stage_fails_closed_and_is_retryable() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, old_fence, context, mut port) =
            prepared_handoff(&store, "handoff-stage-capability");
        let checkpoint = store
            .begin_handoff(prepared_begin_handoff_request(
                "handoff-stage-capability",
                contract,
                &task_id,
                &run_id,
                &old_fence,
                &context.checkpoint_id,
            ))
            .unwrap()
            .checkpoint
            .unwrap();

        let mut suspend_capability = handoff_capability();
        suspend_capability.status = CapabilityStatus::Incompatible;
        let suspend_request = SuspendHandoffRequest {
            task_id: task_id.clone(),
            run_id: run_id.clone(),
            expected_authority_revision: 4,
            checkpoint_id: checkpoint.checkpoint_id,
            old_owner_agent_id: "agent-handoff-stage-capability".to_owned(),
            actor: "agent-handoff-stage-capability".to_owned(),
            fencing_identity: old_fence.clone(),
            handoff_capability: suspend_capability,
            evidence_refs: vec!["evidence:suspend-probe".to_owned()],
        };
        let weakened_suspend = store
            .suspend_handoff(SuspendHandoffRequest {
                handoff_capability: weakened_handoff_capability(),
                ..suspend_request.clone()
            })
            .unwrap();
        assert_eq!(weakened_suspend.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            weakened_suspend.decision.reason,
            "handoff_capability_binding_mismatch"
        );
        let failed_suspend = store.suspend_handoff(suspend_request.clone()).unwrap();
        assert_eq!(failed_suspend.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(
            failed_suspend.decision.reason,
            "handoff_capability_incompatible"
        );
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Suspending
        );
        let checkpoint = store
            .suspend_handoff(SuspendHandoffRequest {
                handoff_capability: handoff_capability(),
                ..suspend_request
            })
            .unwrap()
            .checkpoint
            .unwrap();

        let mut acquire_capability = handoff_capability();
        acquire_capability.status = CapabilityStatus::Unknown;
        let acquire_request = AcquireHandoffRequest {
            task_id: task_id.clone(),
            run_id: run_id.clone(),
            expected_authority_revision: 5,
            checkpoint_id: checkpoint.checkpoint_id,
            replacement_agent_id: "replacement-handoff-stage-capability".to_owned(),
            actor: "scheduler".to_owned(),
            handoff_capability: acquire_capability,
            evidence_refs: vec!["evidence:acquire-probe".to_owned()],
        };
        let weakened_acquire = store
            .acquire_handoff(AcquireHandoffRequest {
                handoff_capability: weakened_handoff_capability(),
                ..acquire_request.clone()
            })
            .unwrap();
        assert_eq!(weakened_acquire.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            weakened_acquire.decision.reason,
            "handoff_capability_binding_mismatch"
        );
        let failed_acquire = store.acquire_handoff(acquire_request.clone()).unwrap();
        assert_eq!(failed_acquire.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(failed_acquire.decision.reason, "handoff_capability_unknown");
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Suspended
        );
        let acquired = store
            .acquire_handoff(AcquireHandoffRequest {
                handoff_capability: handoff_capability(),
                ..acquire_request
            })
            .unwrap();
        let checkpoint = acquired.checkpoint.unwrap();
        let new_fence = acquired.fencing_identity.unwrap();

        let hydrate_observed = std::sync::Arc::new(Mutex::new(None));
        port.hydrate_observed_at = Some(hydrate_observed.clone());
        let mut resume_capability = handoff_capability();
        resume_capability.status = CapabilityStatus::Degraded;
        let resume_request = ResumeHandoffRequest {
            task_id: task_id.clone(),
            run_id: run_id.clone(),
            expected_authority_revision: 6,
            checkpoint_id: checkpoint.checkpoint_id,
            replacement_agent_id: "replacement-handoff-stage-capability".to_owned(),
            actor: "replacement-handoff-stage-capability".to_owned(),
            fencing_identity: new_fence,
            new_context_id: "context-stage-capability".to_owned(),
            memory_query: "resume".to_owned(),
            handoff_capability: resume_capability,
            evidence_refs: vec!["evidence:resume-probe".to_owned()],
        };
        let weakened_resume = store
            .resume_handoff(
                ResumeHandoffRequest {
                    handoff_capability: weakened_handoff_capability(),
                    ..resume_request.clone()
                },
                &port,
            )
            .unwrap();
        assert_eq!(weakened_resume.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            weakened_resume.decision.reason,
            "handoff_capability_binding_mismatch"
        );
        assert!(weakened_resume.hydration.is_none());
        let failed_resume = store.resume_handoff(resume_request.clone(), &port).unwrap();
        assert_eq!(failed_resume.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(failed_resume.decision.reason, "handoff_capability_degraded");
        assert!(failed_resume.hydration.is_none());
        assert!(hydrate_observed.lock().unwrap().is_none());
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Resuming
        );

        let resumed = store
            .resume_handoff(
                ResumeHandoffRequest {
                    handoff_capability: handoff_capability(),
                    ..resume_request
                },
                &port,
            )
            .unwrap();
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert!(resumed.hydration.is_some());
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Running
        );
    }

    #[test]
    fn handoff_requires_a_verified_memory_checkpoint_before_suspending() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let (contract, task_id, run_id, fence, context, _) =
            prepared_handoff(&store, "handoff-no-memory");
        {
            let mut state = store.state.lock().unwrap();
            let context = state
                .context_checkpoints
                .get_mut(&context.checkpoint_id)
                .unwrap();
            context.capability_status = CapabilityStatus::Unknown;
            context.memory_evidence_refs.clear();
            context.verified_at = None;
            save(&path, &state).unwrap();
        }
        let result = store
            .begin_handoff(prepared_begin_handoff_request(
                "handoff-no-memory",
                contract,
                &task_id,
                &run_id,
                &fence,
                &context.checkpoint_id,
            ))
            .unwrap();

        assert_eq!(result.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(result.decision.reason, "memory_checkpoint_required");
        assert!(result.checkpoint.is_none());
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Running);
        assert_eq!(authority.authority_revision, 3);
        assert_eq!(
            authority.owner_agent_id.as_deref(),
            Some("agent-handoff-no-memory")
        );
        assert_eq!(authority.fencing_identity.as_deref(), Some(fence.as_str()));
    }

    #[test]
    fn handoff_rejects_a_context_checkpoint_from_another_lineage() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, fence, context, _) =
            prepared_handoff(&store, "handoff-lineage");
        let mut request = prepared_begin_handoff_request(
            "handoff-lineage",
            contract,
            &task_id,
            &run_id,
            &fence,
            &context.checkpoint_id,
        );
        request.lineage_id = "lineage-other".to_owned();

        let result = store.begin_handoff(request).unwrap();
        assert_eq!(result.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            result.decision.reason,
            "context_checkpoint_binding_mismatch"
        );
        assert!(result.checkpoint.is_none());
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Running);
        assert_eq!(authority.authority_revision, 3);
    }

    #[test]
    fn policy_without_memory_durability_can_handoff_from_a_bound_context_checkpoint() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("agent-governance.json");
        let store = GovernanceStore::open(&path).unwrap();
        let contract = CompletionContract {
            required_mutation_ids: Vec::new(),
            required_artifacts: Vec::new(),
            approval_required: false,
            memory_durability_required: false,
        };
        let suffix = "handoff-memory-optional";
        let (task_id, run_id, old_fence) = running_task(&store, suffix, contract.digest());
        let lineage_id = format!("lineage-{suffix}");
        let retain_request_id = format!("retain-{suffix}");
        let port = verified_test_memory_port(
            &task_id,
            &run_id,
            &lineage_id,
            &old_fence,
            &retain_request_id,
            "sha256:precompact-memory",
        );
        let context = store
            .rotate_context(
                context_rotation_request(
                    suffix,
                    &task_id,
                    &run_id,
                    &old_fence,
                    &lineage_id,
                    &retain_request_id,
                ),
                &port,
                &AcceptMemoryEvidence,
            )
            .unwrap()
            .checkpoint
            .unwrap();
        {
            let mut state = store.state.lock().unwrap();
            let context = state
                .context_checkpoints
                .get_mut(&context.checkpoint_id)
                .unwrap();
            context.capability_status = CapabilityStatus::Unknown;
            context.phase = ContextLifecyclePhase::ContextCheckpoint;
            context.memory_evidence_refs.clear();
            context.verified_at = None;
            save(&path, &state).unwrap();
        }

        let checkpoint = store
            .begin_handoff(prepared_begin_handoff_request(
                suffix,
                contract,
                &task_id,
                &run_id,
                &old_fence,
                &context.checkpoint_id,
            ))
            .unwrap()
            .checkpoint
            .unwrap();
        assert!(!checkpoint.memory_durability_required);
        assert!(checkpoint.memory_checkpoint_id.is_none());
        let checkpoint = store
            .suspend_handoff(SuspendHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                checkpoint_id: checkpoint.checkpoint_id,
                old_owner_agent_id: format!("agent-{suffix}"),
                actor: format!("agent-{suffix}"),
                fencing_identity: old_fence,
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:flushed".to_owned()],
            })
            .unwrap()
            .checkpoint
            .unwrap();
        let acquired = store
            .acquire_handoff(AcquireHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 5,
                checkpoint_id: checkpoint.checkpoint_id,
                replacement_agent_id: format!("replacement-{suffix}"),
                actor: "scheduler".to_owned(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:replacement-ready".to_owned()],
            })
            .unwrap();
        let checkpoint = acquired.checkpoint.unwrap();
        let new_fence = acquired.fencing_identity.unwrap();
        let resumed = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id,
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id,
                    replacement_agent_id: format!("replacement-{suffix}"),
                    actor: format!("replacement-{suffix}"),
                    fencing_identity: new_fence,
                    new_context_id: "context-memory-optional-resumed".to_owned(),
                    memory_query: "resume".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:typed-hydrate".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert!(resumed.hydration.is_some());
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Running
        );
    }

    #[test]
    fn simultaneous_handoff_acquisition_has_one_generation_and_one_fence() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, old_fence, context, _) =
            prepared_handoff(&store, "handoff-race");
        let checkpoint = store
            .begin_handoff(prepared_begin_handoff_request(
                "handoff-race",
                contract,
                &task_id,
                &run_id,
                &old_fence,
                &context.checkpoint_id,
            ))
            .unwrap()
            .checkpoint
            .unwrap();
        let checkpoint = store
            .suspend_handoff(SuspendHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                checkpoint_id: checkpoint.checkpoint_id,
                old_owner_agent_id: "agent-handoff-race".to_owned(),
                actor: "agent-handoff-race".to_owned(),
                fencing_identity: old_fence,
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:flushed".to_owned()],
            })
            .unwrap()
            .checkpoint
            .unwrap();
        let store = std::sync::Arc::new(store);
        let request = AcquireHandoffRequest {
            task_id: task_id.clone(),
            run_id: run_id.clone(),
            expected_authority_revision: 5,
            checkpoint_id: checkpoint.checkpoint_id.clone(),
            replacement_agent_id: "replacement-handoff-race".to_owned(),
            actor: "scheduler".to_owned(),
            handoff_capability: handoff_capability(),
            evidence_refs: vec!["evidence:replacement-ready".to_owned()],
        };
        let first_store = store.clone();
        let first_request = request.clone();
        let first = std::thread::spawn(move || first_store.acquire_handoff(first_request).unwrap());
        let second_store = store.clone();
        let second = std::thread::spawn(move || second_store.acquire_handoff(request).unwrap());
        let results = [first.join().unwrap(), second.join().unwrap()];

        assert_eq!(
            results
                .iter()
                .filter(|result| result.decision.outcome == DecisionOutcome::Allow)
                .count(),
            1
        );
        assert_eq!(
            results
                .iter()
                .filter(|result| result.decision.outcome == DecisionOutcome::Defer)
                .count(),
            1
        );
        let winner = results
            .iter()
            .find(|result| result.decision.outcome == DecisionOutcome::Allow)
            .unwrap();
        let winner_fence = winner.fencing_identity.as_deref().unwrap();
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Resuming);
        assert_eq!(authority.authority_revision, 6);
        assert_eq!(
            authority.owner_agent_id.as_deref(),
            Some("replacement-handoff-race")
        );
        assert_eq!(authority.fencing_identity.as_deref(), Some(winner_fence));
        assert_eq!(
            winner.checkpoint.as_ref().unwrap().new_ownership_generation,
            Some(2)
        );
        assert_eq!(
            store
                .decisions(&task_id)
                .unwrap()
                .iter()
                .filter(|decision| {
                    decision.requested_transition == TransitionKind::AcquireHandoff
                        && decision.performed_at.is_some()
                })
                .count(),
            1
        );
    }

    #[test]
    fn resuming_rejects_old_lease_and_work_until_typed_hydrate_succeeds() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, old_fence, context, mut port) =
            prepared_handoff(&store, "handoff-guard");
        let checkpoint = store
            .begin_handoff(prepared_begin_handoff_request(
                "handoff-guard",
                contract,
                &task_id,
                &run_id,
                &old_fence,
                &context.checkpoint_id,
            ))
            .unwrap()
            .checkpoint
            .unwrap();
        let checkpoint = store
            .suspend_handoff(SuspendHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                checkpoint_id: checkpoint.checkpoint_id,
                old_owner_agent_id: "agent-handoff-guard".to_owned(),
                actor: "agent-handoff-guard".to_owned(),
                fencing_identity: old_fence.clone(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:flushed".to_owned()],
            })
            .unwrap()
            .checkpoint
            .unwrap();
        let acquired = store
            .acquire_handoff(AcquireHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 5,
                checkpoint_id: checkpoint.checkpoint_id,
                replacement_agent_id: "replacement-handoff-guard".to_owned(),
                actor: "scheduler".to_owned(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:replacement-ready".to_owned()],
            })
            .unwrap();
        let checkpoint = acquired.checkpoint.unwrap();
        let new_fence = acquired.fencing_identity.unwrap();
        let hydrate_observed = std::sync::Arc::new(Mutex::new(None));
        port.hydrate_observed_at = Some(hydrate_observed.clone());

        let stale_owner = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id: run_id.clone(),
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id.clone(),
                    replacement_agent_id: "replacement-handoff-guard".to_owned(),
                    actor: "agent-handoff-guard".to_owned(),
                    fencing_identity: old_fence,
                    new_context_id: "context-resumed-guard".to_owned(),
                    memory_query: "resume".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:old-owner-attempt".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(stale_owner.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(stale_owner.decision.reason, "fencing_mismatch");
        assert!(stale_owner.hydration.is_none());
        assert!(hydrate_observed.lock().unwrap().is_none());

        port.hydrate_probe.capability.adapter_id = "other-memory-adapter".to_owned();
        let provider_mismatch = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id: run_id.clone(),
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id.clone(),
                    replacement_agent_id: "replacement-handoff-guard".to_owned(),
                    actor: "replacement-handoff-guard".to_owned(),
                    fencing_identity: new_fence.clone(),
                    new_context_id: "context-resumed-guard".to_owned(),
                    memory_query: "resume".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:provider-mismatch".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(provider_mismatch.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            provider_mismatch.decision.reason,
            "memory_provider_binding_mismatch"
        );
        assert!(provider_mismatch.hydration.is_none());
        assert!(hydrate_observed.lock().unwrap().is_none());
        port.hydrate_probe.capability.adapter_id = "hindsight-adapter".to_owned();

        let blocked = store
            .block_task(BlockTaskRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 6,
                agent_id: "replacement-handoff-guard".to_owned(),
                actor: "replacement-handoff-guard".to_owned(),
                fencing_identity: new_fence.clone(),
                blocker_kind: "dependency".to_owned(),
                cause: StructuredCause {
                    cause_id: "work-before-running".to_owned(),
                    schema_version: 1,
                    fields: BTreeMap::from([("service".to_owned(), "db".to_owned())]),
                },
                required_resume_evidence: vec![EvidenceRequirement {
                    kind: EvidenceKind::DependencyState,
                    subject: "db".to_owned(),
                }],
                evidence_baseline: vec![EvidenceObservation {
                    kind: EvidenceKind::DependencyState,
                    subject: "db".to_owned(),
                    identity: "down".to_owned(),
                }],
                evidence_refs: vec!["evidence:work-attempt".to_owned()],
            })
            .unwrap();
        assert_eq!(blocked.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(blocked.decision.reason, "block_not_available");
        assert_eq!(
            store
                .authority(&task_id)
                .unwrap()
                .unwrap()
                .authority_revision,
            6
        );

        port.hydrate_layer = ContextLayer::KanbanDurableHistory;
        let invalid_hydrate = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id: run_id.clone(),
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id.clone(),
                    replacement_agent_id: "replacement-handoff-guard".to_owned(),
                    actor: "replacement-handoff-guard".to_owned(),
                    fencing_identity: new_fence.clone(),
                    new_context_id: "context-resumed-guard".to_owned(),
                    memory_query: "resume".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:typed-hydrate".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(invalid_hydrate.decision.outcome, DecisionOutcome::Deny);
        assert_eq!(
            invalid_hydrate.decision.reason,
            "memory_hydrate_layer_forbidden"
        );
        assert!(invalid_hydrate.hydration.is_none());
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Resuming
        );
        assert_eq!(
            store
                .handoff_checkpoint(&checkpoint.checkpoint_id)
                .unwrap()
                .unwrap()
                .state,
            HandoffCheckpointState::Resuming
        );

        port.hydrate_layer = ContextLayer::LongTermMemory;
        let resumed = store
            .resume_handoff(
                ResumeHandoffRequest {
                    task_id: task_id.clone(),
                    run_id,
                    expected_authority_revision: 6,
                    checkpoint_id: checkpoint.checkpoint_id,
                    replacement_agent_id: "replacement-handoff-guard".to_owned(),
                    actor: "replacement-handoff-guard".to_owned(),
                    fencing_identity: new_fence,
                    new_context_id: "context-resumed-guard".to_owned(),
                    memory_query: "resume".to_owned(),
                    handoff_capability: handoff_capability(),
                    evidence_refs: vec!["evidence:typed-hydrate-retry".to_owned()],
                },
                &port,
            )
            .unwrap();
        assert_eq!(resumed.decision.outcome, DecisionOutcome::Allow);
        assert!(resumed.hydration.is_some());
        assert_eq!(
            store.authority(&task_id).unwrap().unwrap().lifecycle_state,
            LifecycleState::Running
        );
    }

    #[test]
    fn authority_drift_during_hydrate_returns_no_stale_handoff_payload() {
        let root = tempfile::tempdir().unwrap();
        let store = GovernanceStore::open(root.path().join("agent-governance.json")).unwrap();
        let (contract, task_id, run_id, old_fence, context, port) =
            prepared_handoff(&store, "handoff-hydrate-race");
        let checkpoint = store
            .begin_handoff(prepared_begin_handoff_request(
                "handoff-hydrate-race",
                contract,
                &task_id,
                &run_id,
                &old_fence,
                &context.checkpoint_id,
            ))
            .unwrap()
            .checkpoint
            .unwrap();
        let checkpoint = store
            .suspend_handoff(SuspendHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 4,
                checkpoint_id: checkpoint.checkpoint_id,
                old_owner_agent_id: "agent-handoff-hydrate-race".to_owned(),
                actor: "agent-handoff-hydrate-race".to_owned(),
                fencing_identity: old_fence,
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:flushed".to_owned()],
            })
            .unwrap()
            .checkpoint
            .unwrap();
        let acquired = store
            .acquire_handoff(AcquireHandoffRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 5,
                checkpoint_id: checkpoint.checkpoint_id,
                replacement_agent_id: "replacement-handoff-hydrate-race".to_owned(),
                actor: "scheduler".to_owned(),
                handoff_capability: handoff_capability(),
                evidence_refs: vec!["evidence:replacement-ready".to_owned()],
            })
            .unwrap();
        let checkpoint = acquired.checkpoint.unwrap();
        let new_fence = acquired.fencing_identity.unwrap();
        let hydrate_entered = std::sync::Arc::new(std::sync::Barrier::new(2));
        let hydrate_release = std::sync::Arc::new(std::sync::Barrier::new(2));
        let blocking_port = BlockingMemoryPort {
            inner: port,
            hydrate_entered: hydrate_entered.clone(),
            hydrate_release: hydrate_release.clone(),
        };
        let store = std::sync::Arc::new(store);
        let resume_store = store.clone();
        let resume_task_id = task_id.clone();
        let resume_run_id = run_id.clone();
        let resume_checkpoint_id = checkpoint.checkpoint_id.clone();
        let resume_fence = new_fence.clone();
        let resume = std::thread::spawn(move || {
            resume_store
                .resume_handoff(
                    ResumeHandoffRequest {
                        task_id: resume_task_id,
                        run_id: resume_run_id,
                        expected_authority_revision: 6,
                        checkpoint_id: resume_checkpoint_id,
                        replacement_agent_id: "replacement-handoff-hydrate-race".to_owned(),
                        actor: "replacement-handoff-hydrate-race".to_owned(),
                        fencing_identity: resume_fence,
                        new_context_id: "context-resumed-race".to_owned(),
                        memory_query: "resume".to_owned(),
                        handoff_capability: handoff_capability(),
                        evidence_refs: vec!["evidence:hydrate-race".to_owned()],
                    },
                    &blocking_port,
                )
                .unwrap()
        });
        hydrate_entered.wait();
        let correction = store
            .correct_decision(CorrectionRequest {
                task_id: task_id.clone(),
                run_id: run_id.clone(),
                expected_authority_revision: 6,
                supersedes_decision_id: acquired.decision.decision_id,
                actor: "operator".to_owned(),
                reason: "authority_changed_during_hydrate".to_owned(),
                evidence_refs: vec!["evidence:authority-correction".to_owned()],
            })
            .unwrap();
        assert_eq!(correction.outcome, DecisionOutcome::Allow);
        hydrate_release.wait();
        let resumed = resume.join().unwrap();

        assert_eq!(resumed.decision.outcome, DecisionOutcome::Defer);
        assert_eq!(resumed.decision.reason, "stale_authority");
        assert!(resumed.decision.performed_at.is_none());
        assert!(resumed.hydration.is_none());
        let authority = store.authority(&task_id).unwrap().unwrap();
        assert_eq!(authority.lifecycle_state, LifecycleState::Resuming);
        assert_eq!(authority.authority_revision, 7);
        assert_eq!(
            authority.owner_agent_id.as_deref(),
            Some("replacement-handoff-hydrate-race")
        );
        assert_eq!(
            authority.fencing_identity.as_deref(),
            Some(new_fence.as_str())
        );
        assert_eq!(
            store
                .handoff_checkpoint(&checkpoint.checkpoint_id)
                .unwrap()
                .unwrap()
                .state,
            HandoffCheckpointState::Resuming
        );
    }
}
