use std::collections::{HashMap, HashSet};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::{protocol::OpenAiMessage, tool_calls::DetectedToolCall};

pub(crate) const UNCONFIRMED_TOOL_OUTCOME: &str = "I cannot confirm completion because no matching tool results were returned. No external action has been verified.";

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum ToolResultStatus {
    #[default]
    Unknown,
    Success,
    Failed,
}

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum ToolOperation {
    #[default]
    Unknown,
    Read,
    Deploy,
    Install,
    Start,
    Stop,
    Create,
    Delete,
    Write,
    Execute,
    Verify,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum CompletionPolicyDisposition {
    NotApplicable,
    Allowed,
    Rewritten,
    Suppressed,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum CompletionPolicyReason {
    PolicyDisabled,
    ToolCalls,
    NoExternalClaim,
    MatchingEvidence,
    PendingEvidence,
    MissingEvidence,
    NoMatchingOperation,
    AmbiguousClaim,
    AmbiguousEvidence,
    FailedOrUnknownEvidence,
    MissingTargetBinding,
    TargetMismatch,
    CompletedCallSuppressed,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) struct CompletionPolicyDecision {
    pub(crate) allowed: bool,
    pub(crate) disposition: CompletionPolicyDisposition,
    pub(crate) reason: CompletionPolicyReason,
}

impl CompletionPolicyDisposition {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::NotApplicable => "not_applicable",
            Self::Allowed => "allowed",
            Self::Rewritten => "rewritten",
            Self::Suppressed => "suppressed",
        }
    }
}

impl CompletionPolicyReason {
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::PolicyDisabled => "policy_disabled",
            Self::ToolCalls => "tool_calls",
            Self::NoExternalClaim => "no_external_claim",
            Self::MatchingEvidence => "matching_evidence",
            Self::PendingEvidence => "pending_evidence",
            Self::MissingEvidence => "missing_evidence",
            Self::NoMatchingOperation => "no_matching_operation",
            Self::AmbiguousClaim => "ambiguous_claim",
            Self::AmbiguousEvidence => "ambiguous_evidence",
            Self::FailedOrUnknownEvidence => "failed_or_unknown_evidence",
            Self::MissingTargetBinding => "missing_target_binding",
            Self::TargetMismatch => "target_mismatch",
            Self::CompletedCallSuppressed => "completed_call_suppressed",
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub(crate) struct ToolEvidence {
    id: String,
    name: String,
    arguments_digest: String,
    result_length: usize,
    result_digest: String,
    failed: bool,
    has_result: bool,
    #[serde(default)]
    result_status: ToolResultStatus,
    #[serde(default)]
    operation: ToolOperation,
    #[serde(default)]
    target_digest: String,
    #[serde(default)]
    target_claim_digest: String,
}

impl ToolEvidence {
    fn result_status(&self) -> ToolResultStatus {
        if self.result_status != ToolResultStatus::Unknown {
            self.result_status
        } else if self.failed {
            ToolResultStatus::Failed
        } else {
            ToolResultStatus::Unknown
        }
    }

    fn operation(&self) -> ToolOperation {
        if self.operation != ToolOperation::Unknown {
            self.operation
        } else {
            tool_operation(&self.name, "")
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub(crate) struct AgentLedger {
    pub(crate) completed: Vec<ToolEvidence>,
    pub(crate) pending: Vec<ToolEvidence>,
    pub(crate) tool_rounds: usize,
    pub(crate) repeated_call: bool,
    pub(crate) repeated_failure: bool,
}

impl AgentLedger {
    pub(crate) fn can_continue(&self, limit: usize) -> Result<(), String> {
        let limit = if limit == 0 { 16 } else { limit };
        if self.tool_rounds >= limit {
            return Err(format!("tool round limit reached: {limit}"));
        }
        if !self.pending.is_empty() {
            return Err("pending tool results must be returned before another turn".to_owned());
        }
        Ok(())
    }

    fn pending_ids(&self) -> impl Iterator<Item = &str> {
        self.pending.iter().map(|evidence| evidence.id.as_str())
    }

    fn all_ids(&self) -> impl Iterator<Item = &str> {
        self.completed
            .iter()
            .chain(&self.pending)
            .map(|evidence| evidence.id.as_str())
    }

    pub(crate) fn filter_known_calls(
        &self,
        calls: Vec<DetectedToolCall>,
    ) -> (Vec<DetectedToolCall>, bool) {
        let mut batch = HashSet::new();
        let mut suppressed = false;
        let calls = calls
            .into_iter()
            .filter(|call| {
                let name = call
                    .function
                    .get("name")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let arguments = call
                    .function
                    .get("arguments")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let identity = format!("{name}\0{}", arguments_digest(arguments));
                let duplicate = !batch.insert(identity)
                    || self.completed.iter().chain(&self.pending).any(|evidence| {
                        evidence.name == name
                            && evidence.arguments_digest == arguments_digest(arguments)
                    });
                suppressed |= duplicate;
                !duplicate
            })
            .collect();
        (calls, suppressed)
    }

    pub(crate) fn router_context(&self) -> String {
        let evidence = serde_json::json!({
            "completed": self.completed,
            "pending": self.pending,
            "repeated_call": self.repeated_call,
        });
        format!(
            "Use only this compact evidence. Completed calls are final evidence. Pending calls have unknown outcomes because no matching tool result was returned. Do not automatically issue the same name and arguments as any completed or pending call. Report pending outcomes as unconfirmed unless independent evidence resolves them.\nEVIDENCE_LEDGER: {evidence}"
        )
    }
}

#[cfg(test)]
pub(crate) fn validate_tool_conversation(messages: &[OpenAiMessage]) -> Result<(), String> {
    validate_tool_conversation_with_prior(messages, &AgentLedger::default())
}

pub(crate) fn validate_tool_conversation_with_prior(
    messages: &[OpenAiMessage],
    prior: &AgentLedger,
) -> Result<(), String> {
    let mut pending = prior
        .pending_ids()
        .map(str::to_owned)
        .collect::<HashSet<_>>();
    let mut seen = prior.all_ids().map(str::to_owned).collect::<HashSet<_>>();
    for (index, message) in messages.iter().enumerate() {
        match message.role.as_str() {
            "assistant" => {
                if !pending.is_empty() {
                    return Err(format!(
                        "tool results missing before assistant message at index {index}"
                    ));
                }
                for call in &message.tool_calls {
                    let id = call.get("id").and_then(Value::as_str).unwrap_or_default();
                    if id.is_empty() {
                        return Err(format!("assistant tool call missing id at index {index}"));
                    }
                    if !seen.insert(id.to_owned()) {
                        return Err(format!("duplicate tool call id: {id}"));
                    }
                    pending.insert(id.to_owned());
                }
            }
            "tool" => {
                if message.tool_call_id.is_empty() {
                    return Err(format!("tool_call_id required at index {index}"));
                }
                if !pending.remove(&message.tool_call_id) {
                    return Err(format!("unexpected tool result: {}", message.tool_call_id));
                }
            }
            "user" if message.is_execution_user_boundary() => pending.clear(),
            _ => {}
        }
    }
    if let Some(id) = pending.into_iter().next() {
        return Err(format!("missing tool result for tool_call_id: {id}"));
    }
    Ok(())
}

pub(crate) fn build(messages: &[OpenAiMessage]) -> AgentLedger {
    build_with_prior(messages, AgentLedger::default())
}

pub(crate) fn execution_ledger(prior: &AgentLedger, messages: &[OpenAiMessage]) -> AgentLedger {
    let Some(last_user) = messages
        .iter()
        .rposition(OpenAiMessage::is_execution_user_boundary)
    else {
        return build_with_prior(messages, prior.clone());
    };

    let pending = if last_user == 0 {
        prior.pending.clone()
    } else {
        build_with_prior(&messages[..last_user], prior.clone()).pending
    };
    build_with_prior(
        &messages[last_user..],
        AgentLedger {
            pending,
            ..AgentLedger::default()
        },
    )
}

pub(crate) fn build_with_prior(messages: &[OpenAiMessage], prior: AgentLedger) -> AgentLedger {
    let mut calls = HashMap::<String, ToolEvidence>::new();
    let mut order = Vec::new();
    for evidence in prior.completed.iter().chain(&prior.pending) {
        if calls
            .insert(evidence.id.clone(), evidence.clone())
            .is_none()
        {
            order.push(evidence.id.clone());
        }
    }
    let mut tool_rounds = prior.tool_rounds;
    for message in messages {
        if message.role == "assistant" {
            let mut added_round = false;
            for call in &message.tool_calls {
                let id = call.get("id").and_then(Value::as_str).unwrap_or_default();
                let function = call.get("function").and_then(Value::as_object);
                let name = function
                    .and_then(|function| function.get("name"))
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let arguments = function
                    .and_then(|function| function.get("arguments"))
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if !id.is_empty() && !calls.contains_key(id) {
                    calls.insert(
                        id.to_owned(),
                        ToolEvidence {
                            id: id.to_owned(),
                            name: name.to_owned(),
                            arguments_digest: arguments_digest(arguments),
                            operation: tool_operation(name, arguments),
                            target_digest: tool_target_digest(arguments),
                            target_claim_digest: tool_target_claim_digest(arguments),
                            ..ToolEvidence::default()
                        },
                    );
                    order.push(id.to_owned());
                    added_round = true;
                }
            }
            tool_rounds += usize::from(added_round);
        }
        if message.role == "tool"
            && let Some(evidence) = calls.get_mut(&message.tool_call_id)
        {
            let result = content_string(&message.content);
            evidence.result_length = result.len();
            evidence.result_digest = digest(result.as_bytes());
            evidence.result_status =
                tool_result_status(message.tool_result_is_error, &evidence.name, &result);
            evidence.failed = evidence.result_status == ToolResultStatus::Failed;
            evidence.has_result = true;
        }
    }

    let mut ledger = AgentLedger {
        tool_rounds,
        repeated_call: prior.repeated_call,
        repeated_failure: prior.repeated_failure,
        ..AgentLedger::default()
    };
    let mut calls_seen = HashMap::<(String, String), usize>::new();
    let mut failures_seen = HashMap::<(String, String, String), usize>::new();
    for id in order {
        let Some(evidence) = calls.remove(&id) else {
            continue;
        };
        let call_count = calls_seen
            .entry((evidence.name.clone(), evidence.arguments_digest.clone()))
            .or_default();
        *call_count += 1;
        ledger.repeated_call |= *call_count >= 2;
        if evidence.has_result {
            if evidence.result_status() == ToolResultStatus::Failed {
                let failure_count = failures_seen
                    .entry((
                        evidence.name.clone(),
                        evidence.arguments_digest.clone(),
                        evidence.result_digest.clone(),
                    ))
                    .or_default();
                *failure_count += 1;
                ledger.repeated_failure |= *failure_count >= 2;
            }
            ledger.completed.push(evidence);
        } else {
            ledger.pending.push(evidence);
        }
    }
    ledger
}

pub(crate) fn active_messages(messages: &[OpenAiMessage]) -> &[OpenAiMessage] {
    let last_user = messages
        .iter()
        .rposition(OpenAiMessage::is_execution_user_boundary);
    last_user.map_or(messages, |index| &messages[index..])
}

#[cfg(test)]
pub(crate) fn completion_evidence_allows(answer: &str, ledger: &AgentLedger) -> bool {
    completion_evidence_decision(answer, ledger).allowed
}

pub(crate) fn completion_evidence_decision(
    answer: &str,
    ledger: &AgentLedger,
) -> CompletionPolicyDecision {
    let Some(claim) = completion_claim(answer) else {
        return CompletionPolicyDecision {
            allowed: true,
            disposition: CompletionPolicyDisposition::Allowed,
            reason: CompletionPolicyReason::NoExternalClaim,
        };
    };
    if matches!(claim, CompletionClaim::AmbiguousOperation) {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::AmbiguousClaim,
        };
    }
    if !ledger.pending.is_empty() {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::PendingEvidence,
        };
    }

    let mut matching = match claim {
        CompletionClaim::Operation(operation) => ledger
            .completed
            .iter()
            .filter(|evidence| evidence.operation() == operation)
            .collect::<Vec<_>>(),
        CompletionClaim::AmbiguousOperation => unreachable!("ambiguous claim handled above"),
        CompletionClaim::Unqualified => ledger
            .completed
            .iter()
            .filter(|evidence| {
                !matches!(
                    evidence.operation(),
                    ToolOperation::Unknown | ToolOperation::Read
                )
            })
            .collect::<Vec<_>>(),
    };
    if matching.is_empty() {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: if ledger.completed.is_empty() {
                CompletionPolicyReason::MissingEvidence
            } else {
                CompletionPolicyReason::NoMatchingOperation
            },
        };
    }
    let claimed_targets = completion_target_digests(answer);
    if !claimed_targets.is_empty() {
        matching.retain(|evidence| claimed_targets.contains(&evidence.target_claim_digest));
        if matching.is_empty() {
            return CompletionPolicyDecision {
                allowed: false,
                disposition: CompletionPolicyDisposition::Rewritten,
                reason: CompletionPolicyReason::TargetMismatch,
            };
        }
        let evidence_targets = matching
            .iter()
            .map(|evidence| evidence.target_claim_digest.as_str())
            .collect::<std::collections::BTreeSet<_>>();
        if evidence_targets.len() != claimed_targets.len()
            || claimed_targets
                .iter()
                .any(|target| !evidence_targets.contains(target.as_str()))
        {
            return CompletionPolicyDecision {
                allowed: false,
                disposition: CompletionPolicyDisposition::Rewritten,
                reason: CompletionPolicyReason::TargetMismatch,
            };
        }
    }
    if matching
        .iter()
        .any(|evidence| evidence.result_status() != ToolResultStatus::Success)
    {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::FailedOrUnknownEvidence,
        };
    }
    if matches!(claim, CompletionClaim::Unqualified) {
        let mut operations = matching.iter().map(|evidence| evidence.operation());
        let first = operations.next();
        if first.is_none() || operations.any(|operation| Some(operation) != first) {
            return CompletionPolicyDecision {
                allowed: false,
                disposition: CompletionPolicyDisposition::Rewritten,
                reason: CompletionPolicyReason::NoMatchingOperation,
            };
        }
    }
    if matching.iter().any(|evidence| {
        evidence.target_digest.is_empty() || evidence.target_claim_digest.is_empty()
    }) {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::MissingTargetBinding,
        };
    }
    let mut target_contexts_by_claim = HashMap::<&str, HashSet<&str>>::new();
    for evidence in &matching {
        target_contexts_by_claim
            .entry(evidence.target_claim_digest.as_str())
            .or_default()
            .insert(evidence.target_digest.as_str());
    }
    if target_contexts_by_claim
        .values()
        .any(|contexts| contexts.len() > 1)
    {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::AmbiguousEvidence,
        };
    }
    let target_digests = matching
        .iter()
        .map(|evidence| evidence.target_digest.as_str())
        .filter(|digest| !digest.is_empty())
        .collect::<std::collections::BTreeSet<_>>();
    if target_digests.len() > 1 && claimed_targets.is_empty() {
        return CompletionPolicyDecision {
            allowed: false,
            disposition: CompletionPolicyDisposition::Rewritten,
            reason: CompletionPolicyReason::AmbiguousEvidence,
        };
    }
    CompletionPolicyDecision {
        allowed: true,
        disposition: CompletionPolicyDisposition::Allowed,
        reason: CompletionPolicyReason::MatchingEvidence,
    }
}

fn tool_result_status(explicit: bool, _name: &str, result: &str) -> ToolResultStatus {
    if explicit {
        return ToolResultStatus::Failed;
    }
    if let Ok(object) = serde_json::from_str::<serde_json::Map<String, Value>>(result.trim())
        && object.contains_key("output")
        && let Some(exit_code) = object.get("exit_code").and_then(Value::as_i64)
    {
        let failed = exit_code != 0
            || object.get("error").is_some_and(|error| {
                !error.is_null() && error.as_str().is_none_or(|s| !s.trim().is_empty())
            })
            || object
                .get("status")
                .and_then(Value::as_str)
                .is_some_and(|status| {
                    matches!(
                        status.trim().to_ascii_lowercase().as_str(),
                        "error" | "failed" | "failure"
                    )
                });
        return if failed {
            ToolResultStatus::Failed
        } else {
            ToolResultStatus::Success
        };
    }
    if let Ok(value) = serde_json::from_str::<Value>(result.trim()) {
        let Some(object) = value.as_object() else {
            return ToolResultStatus::Unknown;
        };
        if object
            .get("success")
            .and_then(Value::as_bool)
            .is_some_and(|success| !success)
            || object
                .get("ok")
                .and_then(Value::as_bool)
                .is_some_and(|ok| !ok)
        {
            return ToolResultStatus::Failed;
        }
        if object.get("error").is_some_and(|error| {
            !error.is_null() && error.as_str().is_none_or(|text| !text.trim().is_empty())
        }) {
            return ToolResultStatus::Failed;
        }
        return match object
            .get("status")
            .and_then(Value::as_str)
            .map(|status| status.trim().to_ascii_lowercase())
            .as_deref()
        {
            Some("error" | "failed" | "failure") => ToolResultStatus::Failed,
            _ => ToolResultStatus::Unknown,
        };
    }
    ToolResultStatus::Unknown
}

fn tool_operation(name: &str, arguments: &str) -> ToolOperation {
    let name = name.trim().to_ascii_lowercase();
    if matches!(name.as_str(), "terminal" | "shell" | "exec" | "run_command") {
        let command = serde_json::from_str::<Value>(arguments.trim())
            .ok()
            .and_then(|value| {
                value
                    .get("command")
                    .and_then(Value::as_str)
                    .map(str::to_owned)
            })
            .unwrap_or_default();
        return operation_from_tool_name(command.split_whitespace().next().unwrap_or(""))
            .unwrap_or(ToolOperation::Execute);
    }
    operation_from_tool_name(&name).unwrap_or(ToolOperation::Unknown)
}

fn operation_from_tool_name(name: &str) -> Option<ToolOperation> {
    let name = name.trim().trim_start_matches("./").to_ascii_lowercase();
    let exact =
        |variants: &[&str], operation| variants.contains(&name.as_str()).then_some(operation);
    exact(
        &[
            "read",
            "read_file",
            "search",
            "search_files",
            "list",
            "list_files",
            "inspect",
            "get",
            "fetch",
            "retrieve",
            "kanban_show",
        ],
        ToolOperation::Read,
    )
    .or_else(|| {
        exact(
            &["deploy", "deployment", "publish", "release"],
            ToolOperation::Deploy,
        )
    })
    .or_else(|| {
        exact(
            &["install", "installation", "upgrade"],
            ToolOperation::Install,
        )
    })
    .or_else(|| exact(&["start", "restart", "launch"], ToolOperation::Start))
    .or_else(|| {
        exact(
            &["stop", "shutdown", "terminate", "kill"],
            ToolOperation::Stop,
        )
    })
    .or_else(|| exact(&["create", "provision"], ToolOperation::Create))
    .or_else(|| exact(&["delete", "remove", "destroy"], ToolOperation::Delete))
    .or_else(|| {
        exact(
            &[
                "write", "update", "modify", "edit", "patch", "commit", "push",
            ],
            ToolOperation::Write,
        )
    })
    .or_else(|| exact(&["execute", "run", "apply"], ToolOperation::Execute))
    .or_else(|| exact(&["verify", "validate"], ToolOperation::Verify))
}

fn operations_from_text(text: &str) -> Vec<ToolOperation> {
    let mut operations = Vec::new();
    for (operation, words) in [
        (
            ToolOperation::Read,
            &[
                "read",
                "reading",
                "inspect",
                "inspection",
                "retrieve",
                "query",
                "queried",
                "查詢",
                "檢查",
                "讀取",
                "讀檔",
            ][..],
        ),
        (
            ToolOperation::Deploy,
            &[
                "deploy",
                "deployment",
                "publish",
                "release",
                "部署",
                "發布",
                "发布",
            ][..],
        ),
        (
            ToolOperation::Install,
            &[
                "install",
                "installation",
                "upgrade",
                "安裝",
                "安装",
                "升級",
                "升级",
            ][..],
        ),
        (
            ToolOperation::Start,
            &[
                "start", "started", "restart", "launch", "running", "啟動", "启动", "重啟", "重启",
            ][..],
        ),
        (
            ToolOperation::Stop,
            &[
                "stop",
                "stopped",
                "shutdown",
                "terminate",
                "kill",
                "停止",
                "關閉",
                "关闭",
            ][..],
        ),
        (
            ToolOperation::Create,
            &["create", "created", "provision", "建立", "创建", "新增"][..],
        ),
        (
            ToolOperation::Delete,
            &[
                "delete", "deleted", "remove", "removed", "destroy", "刪除", "删除",
            ][..],
        ),
        (
            ToolOperation::Write,
            &[
                "write", "written", "update", "updated", "modify", "modified", "edit", "edited",
                "patch", "commit", "push", "寫入", "更新",
            ][..],
        ),
        (
            ToolOperation::Execute,
            &[
                "execute", "executed", "run", "ran", "apply", "applied", "執行", "执行", "套用",
            ][..],
        ),
        (
            ToolOperation::Verify,
            &[
                "verify",
                "verified",
                "validate",
                "validated",
                "驗證",
                "验证",
            ][..],
        ),
    ] {
        if words.iter().any(|word| contains_token(text, word)) {
            operations.push(operation);
        }
    }
    operations
}

fn contains_token(text: &str, token: &str) -> bool {
    if token.is_ascii() {
        contains_word(text, token)
    } else {
        text.contains(token)
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum CompletionClaim {
    Operation(ToolOperation),
    AmbiguousOperation,
    Unqualified,
}

fn completion_claim(answer: &str) -> Option<CompletionClaim> {
    let lower = answer.to_ascii_lowercase();
    if answer.trim().is_empty() || is_non_claim_framing(answer, &lower) {
        return None;
    }
    let success_words = [
        "installed",
        "created",
        "written",
        "executed",
        "ran",
        "started",
        "deployed",
        "deleted",
        "verified",
        "completed",
        "succeeded",
        "success",
        "successful",
        "successfully",
        "done",
        "finished",
        "passed",
        "applied",
    ];
    let positive = success_words.iter().any(|word| contains_word(&lower, word))
        || [
            "service is running",
            "server is active",
            "app is now running",
        ]
        .iter()
        .any(|phrase| lower.contains(phrase))
        || ["完成", "成功", "已啟動", "已启动", "已完成"]
            .iter()
            .any(|phrase| answer.contains(phrase));
    if !positive {
        return None;
    }
    let (negated_positive, unnegated_positive) = positive_polarity(&lower, &success_words);
    if negated_positive && unnegated_positive {
        return Some(CompletionClaim::AmbiguousOperation);
    }
    if has_unnegated_failure_marker(&lower, answer) {
        return Some(CompletionClaim::AmbiguousOperation);
    }
    if (negated_positive && !unnegated_positive) || has_negation_marker(&lower) {
        return None;
    }
    match operations_from_text(&lower).as_slice() {
        [] => Some(CompletionClaim::Unqualified),
        [operation] => Some(CompletionClaim::Operation(*operation)),
        _ => Some(CompletionClaim::AmbiguousOperation),
    }
}

fn is_non_claim_framing(answer: &str, lower: &str) -> bool {
    let trimmed = answer.trim();
    let question = trimmed.ends_with(['?', '？'])
        || [
            "is ",
            "was ",
            "were ",
            "did ",
            "has ",
            "have ",
            "can ",
            "could ",
            "what ",
            "why ",
            "how ",
            "是否",
            "有沒有",
            "有沒有",
            "是否已",
        ]
        .iter()
        .any(|marker| lower.starts_with(marker));
    let explanatory = [
        "it means ",
        "this means ",
        "that means ",
        "which means ",
        "in other words",
        "the meaning is ",
        "意思是",
        "這表示",
        "这表示",
        "也就是",
    ]
    .iter()
    .any(|marker| lower.starts_with(marker));
    let framed = [
        "the word ",
        "the term ",
        "the phrase ",
        "the sentence ",
        "for example",
        "example:",
        "translation:",
        "translate:",
        "translated:",
        "quote:",
        "quotation:",
        "quoted",
        "historical",
        "history:",
        "previously",
        "in the past",
        "code:",
        "definition:",
    ]
    .iter()
    .any(|marker| lower.starts_with(marker));
    let fenced_code = trimmed.starts_with("```") && trimmed.ends_with("```");
    let inline_code = trimmed.starts_with('`') && trimmed.ends_with('`');
    let quoted = (trimmed.starts_with('"') && trimmed.ends_with('"'))
        || (trimmed.starts_with('“') && trimmed.ends_with('”'))
        || (trimmed.starts_with('‘') && trimmed.ends_with('’'));
    let definition = lower.starts_with("success is a noun")
        || lower.starts_with("success is the noun")
        || trimmed.starts_with("成功是主觀的")
        || trimmed.starts_with("成功是主观的");
    let (outside, delimited) = outside_delimited_text(answer);
    let delimited_framing = delimited
        && [
            "phrase",
            "sentence",
            "quote",
            "quotation",
            "example",
            "translation",
            "翻譯",
            "翻译",
            "引述",
            "引用",
        ]
        .iter()
        .any(|marker| outside.to_ascii_lowercase().contains(marker));
    question
        || framed
        || explanatory
        || fenced_code
        || inline_code
        || quoted
        || definition
        || delimited_framing
}

fn positive_polarity(answer: &str, success_words: &[&str]) -> (bool, bool) {
    let mut negated = false;
    let mut unnegated = false;
    for word in success_words {
        for (index, _) in answer.match_indices(word) {
            if answer[..index]
                .split_whitespace()
                .rev()
                .take(4)
                .any(|preceding| matches!(preceding, "not" | "never" | "no"))
            {
                negated = true;
            } else {
                unnegated = true;
            }
        }
    }
    (negated, unnegated)
}

fn has_negation_marker(answer: &str) -> bool {
    [
        "cannot confirm",
        "can't confirm",
        "unable to confirm",
        "unconfirmed",
        "無法確認",
        "无法确认",
        "未確認",
        "未确认",
        "未完成",
        "尚未完成",
        "沒有完成",
        "没有完成",
        "未成功",
        "沒有成功",
        "没有成功",
    ]
    .iter()
    .any(|marker| answer.contains(marker))
}

fn has_unnegated_failure_marker(lower: &str, original: &str) -> bool {
    let english = ["failed", "failure", "failures"].iter().any(|word| {
        lower.match_indices(word).any(|(index, _)| {
            !lower[..index]
                .split_whitespace()
                .rev()
                .take(4)
                .any(|preceding| matches!(preceding, "no" | "not" | "never" | "without"))
        })
    });
    if english {
        return true;
    }
    ["失敗", "失败"].iter().any(|marker| {
        original.contains(marker)
            && ![
                "未失敗",
                "未失败",
                "沒有失敗",
                "没有失败",
                "無失敗",
                "无失败",
            ]
            .iter()
            .any(|negated| original.contains(negated))
    })
}

fn outside_delimited_text(answer: &str) -> (String, bool) {
    let mut outside = String::with_capacity(answer.len());
    let mut delimiter = None;
    let mut had_delimiter = false;
    for character in answer.chars() {
        if let Some(active) = delimiter {
            if character == active {
                delimiter = None;
            }
            had_delimiter = true;
        } else if matches!(character, '`' | '"' | '“' | '”' | '‘' | '’') {
            delimiter = Some(match character {
                '“' => '”',
                '‘' => '’',
                _ => character,
            });
            had_delimiter = true;
        } else {
            outside.push(character);
        }
    }
    (outside, had_delimiter)
}

fn contains_word(text: &str, word: &str) -> bool {
    text.match_indices(word).any(|(index, _)| {
        let before = text[..index].chars().next_back();
        let after = text[index + word.len()..].chars().next();
        before.is_none_or(|c| !c.is_ascii_alphanumeric() && c != '_')
            && after.is_none_or(|c| !c.is_ascii_alphanumeric() && c != '_')
    })
}

fn arguments_digest(arguments: &str) -> String {
    let canonical = serde_json::from_str::<Value>(arguments.trim())
        .ok()
        .and_then(|value| serde_json::to_string(&value).ok())
        .unwrap_or_else(|| arguments.trim().to_owned());
    digest(canonical.as_bytes())
}

fn tool_target_digest(arguments: &str) -> String {
    let Some(Value::Object(object)) = serde_json::from_str(arguments.trim()).ok() else {
        return String::new();
    };
    let target = [
        "target",
        "target_id",
        "path",
        "service",
        "environment",
        "name",
        "resource",
        "resource_id",
        "task_id",
        "url",
    ]
    .into_iter()
    .filter_map(|key| {
        object
            .get(key)
            .filter(|value| target_value_is_credible(value))
            .cloned()
            .map(|value| (key.to_owned(), value))
    })
    .collect::<serde_json::Map<_, _>>();
    let binding = if target.is_empty() {
        let command = object
            .get("command")
            .and_then(Value::as_str)
            .unwrap_or_default();
        let Some(command_target) = terminal_command_target_parts(command) else {
            return String::new();
        };
        let mut binding = serde_json::Map::new();
        binding.insert(
            "_command_target".to_owned(),
            Value::String(command_target.join(" ")),
        );
        binding
    } else {
        target
    };
    serde_json::to_vec(&binding)
        .map(|value| digest(&value))
        .unwrap_or_default()
}

fn tool_target_claim_digest(arguments: &str) -> String {
    let Some(Value::Object(object)) = serde_json::from_str(arguments.trim()).ok() else {
        return String::new();
    };
    for key in [
        "target",
        "target_id",
        "path",
        "service",
        "environment",
        "name",
        "resource",
        "resource_id",
        "task_id",
        "url",
    ] {
        if let Some(value) = object
            .get(key)
            .filter(|value| target_value_is_credible(value))
        {
            return target_claim_digest_value(value);
        }
    }
    let command = object
        .get("command")
        .and_then(Value::as_str)
        .unwrap_or_default();
    terminal_command_target_parts(command)
        .and_then(|parts| parts.first().map(|target| target_claim_digest_text(target)))
        .unwrap_or_default()
}

fn terminal_command_target_parts(command: &str) -> Option<Vec<&str>> {
    let mut words = command.split_whitespace();
    words.next()?;
    let first = words.next()?;
    if first.starts_with('-') {
        return None;
    }
    Some(std::iter::once(first).chain(words).collect())
}

fn target_value_is_credible(value: &Value) -> bool {
    value.as_str().is_some_and(|value| !value.trim().is_empty()) || value.is_number()
}

fn target_claim_digest_value(value: &Value) -> String {
    value
        .as_str()
        .map(target_claim_digest_text)
        .unwrap_or_else(|| digest(value.to_string().as_bytes()))
}

fn target_claim_digest_text(value: &str) -> String {
    digest(value.trim().as_bytes())
}

fn completion_target_digests(answer: &str) -> Vec<String> {
    let mut targets = Vec::new();
    let words = answer.split_whitespace().collect::<Vec<_>>();
    let mut index = 0;
    while index < words.len() {
        let word = words[index];
        let marker = word
            .trim_matches(|character: char| !character.is_ascii_alphanumeric() && character != '_')
            .to_ascii_lowercase();
        if !matches!(marker.as_str(), "for" | "to" | "on" | "target") {
            index += 1;
            continue;
        }
        index += 1;
        let mut separated = true;
        while separated && index < words.len() {
            let raw = words[index];
            let target = raw.trim_matches(|character: char| {
                !character.is_ascii_alphanumeric() && !matches!(character, '_' | '-' | '/' | ':')
            });
            if target.is_empty()
                || matches!(
                    target.to_ascii_lowercase().as_str(),
                    "and"
                        | "or"
                        | "the"
                        | "a"
                        | "an"
                        | "success"
                        | "successful"
                        | "successfully"
                        | "completed"
                        | "done"
                )
            {
                break;
            }
            targets.push(target_claim_digest_text(target));
            let has_trailing_separator = raw.ends_with(',') || raw.ends_with(';');
            index += 1;
            if index >= words.len() {
                break;
            }
            let next = words[index]
                .trim_matches(|character: char| {
                    !character.is_ascii_alphanumeric()
                        && !matches!(character, '_' | '-' | '/' | ':')
                })
                .to_ascii_lowercase();
            separated = has_trailing_separator || matches!(next.as_str(), "and" | "or" | "&");
            if separated && matches!(next.as_str(), "and" | "or" | "&") {
                index += 1;
            }
        }
    }
    targets.sort();
    targets.dedup();
    targets
}

fn digest(value: &[u8]) -> String {
    let bytes = Sha256::digest(value);
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn content_string(value: &Value) -> String {
    value
        .as_str()
        .map(str::to_owned)
        .or_else(|| serde_json::to_string(value).ok())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn call(id: &str, name: &str, arguments: &str) -> OpenAiMessage {
        OpenAiMessage {
            role: "assistant".to_owned(),
            content: Value::Null,
            tool_calls: vec![json!({
                "id":id,
                "type":"function",
                "function":{"name":name,"arguments":arguments}
            })],
            ..OpenAiMessage::default()
        }
    }

    fn result(id: &str, content: &str) -> OpenAiMessage {
        OpenAiMessage {
            role: "tool".to_owned(),
            content: Value::String(content.to_owned()),
            tool_call_id: id.to_owned(),
            ..OpenAiMessage::default()
        }
    }

    #[test]
    fn parallel_calls_count_as_one_round_and_equivalent_json_deduplicates() {
        let mut parallel = call("a", "read", r#"{"path":"a"}"#);
        parallel.tool_calls.push(json!({
            "id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}
        }));
        let ledger = build(&[parallel, result("a", "A"), result("b", "B")]);
        assert_eq!(ledger.tool_rounds, 1);
        let candidate = DetectedToolCall {
            id: "new".to_owned(),
            kind: "function".to_owned(),
            function: json!({"name":"read","arguments":" { \"path\" : \"a\" } "}),
        };
        let (calls, suppressed) = ledger.filter_known_calls(vec![candidate]);
        assert!(calls.is_empty());
        assert!(suppressed);
    }

    #[test]
    fn structured_terminal_result_uses_explicit_failure_fields() {
        for (content, explicit, failed) in [
            (
                r#"{"output":"ERROR diagnostic","exit_code":0,"error":null}"#,
                false,
                false,
            ),
            (
                r#"{"output":"bad","exit_code":1,"error":null}"#,
                false,
                true,
            ),
            (
                r#"{"output":"bad","exit_code":0,"error":"permission denied"}"#,
                false,
                true,
            ),
            (r#"{"output":"ok","exit_code":0,"error":null}"#, true, true),
        ] {
            let mut output = result("c1", content);
            output.tool_result_is_error = explicit;
            let ledger = build(&[call("c1", "terminal", "{}"), output]);
            assert_eq!(ledger.completed[0].failed, failed, "content={content}");
        }
    }

    #[test]
    fn untyped_success_flags_never_authorize_but_explicit_false_fails_closed() {
        for (content, status) in [
            (
                r#"{"success":true,"job":{"last_delivery_error":null,"last_fire_error":null}}"#,
                ToolResultStatus::Unknown,
            ),
            (r#"{"ok":true,"error":null}"#, ToolResultStatus::Unknown),
            (
                r#"{"success":false,"error":"update rejected"}"#,
                ToolResultStatus::Failed,
            ),
            (
                r#"{"ok":false,"error":"not found"}"#,
                ToolResultStatus::Failed,
            ),
        ] {
            let ledger = build(&[call("c1", "cronjob", "{}"), result("c1", content)]);
            assert_eq!(
                ledger.completed[0].result_status(),
                status,
                "content={content}"
            );
        }
    }

    #[test]
    fn completion_guard_distinguishes_success_evidence_pending_and_failure() {
        let pending = build(&[call("p1", "deploy", "{}")]);
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &pending
        ));
        assert!(completion_evidence_allows(
            "The result remains unconfirmed.",
            &pending
        ));

        let succeeded = build(&[
            call("c1", "terminal", r#"{"command":"verify service-a"}"#),
            result("c1", r#"{"output":"ok","exit_code":0,"error":null}"#),
        ]);
        assert!(completion_evidence_allows(
            "Completed successfully.",
            &succeeded
        ));

        let failed = build(&[
            call("c1", "deploy", "{}"),
            result("c1", "exit code 1: failed"),
        ]);
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &failed
        ));
        assert!(completion_evidence_allows(
            "Deployment failed and remains incomplete.",
            &failed
        ));
    }

    #[test]
    fn ordinary_explanations_and_negated_actions_do_not_trigger_completion_guard() {
        let ledger = AgentLedger::default();

        assert!(completion_evidence_allows(
            "The word success is a noun.",
            &ledger
        ));
        assert!(completion_evidence_allows("成功是主觀的。", &ledger));
        assert!(completion_evidence_allows(
            "I have not deployed anything.",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "Translation: \"Deployment completed successfully.\"",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "Historical note: deployment completed successfully in 2020.",
            &ledger
        ));
        assert!(!completion_evidence_allows(
            "Deployment completed successfully, but verification failed.",
            &ledger
        ));
        assert!(!completion_evidence_allows(
            "Deployment completed successfully; see `status`.",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "```\nDeployment completed successfully.\n```",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "Was the deployment successful?",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "The phrase \"Deployment completed successfully.\" is an example.",
            &ledger
        ));
        assert!(completion_evidence_allows(
            "It means the deployment completed successfully.",
            &ledger
        ));
        assert!(completion_evidence_allows("這表示部署已成功。", &ledger));
        assert!(!completion_evidence_allows(
            "It was not successfully deployed, but the deployment was successful.",
            &ledger
        ));
    }

    #[test]
    fn completion_claim_requires_the_matching_operation_and_authoritative_result() {
        let read = build(&[
            call("read-1", "read_file", r#"{"path":"README.md"}"#),
            result(
                "read-1",
                "Documentation: error handling and timeout recovery",
            ),
        ]);
        assert!(!read.completed[0].failed);
        assert_eq!(
            read.completed[0].result_status(),
            ToolResultStatus::Unknown,
            "human-readable file content is not an execution status"
        );
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &read
        ));

        let mixed = build(&[
            call("read-1", "read_file", r#"{"path":"README.md"}"#),
            result("read-1", "documentation"),
            call("deploy-1", "terminal", r#"{"command":"deploy"}"#),
            result(
                "deploy-1",
                r#"{"output":"failed","exit_code":1,"error":null}"#,
            ),
        ]);
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &mixed
        ));

        let target_a = build(&[
            call("deploy-a", "deploy", r#"{"target":"service-a"}"#),
            result("deploy-a", r#"{"output":"ok","exit_code":0}"#),
            call("deploy-b", "deploy", r#"{"target":"service-b"}"#),
            result("deploy-b", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision =
            completion_evidence_decision("Deployment completed successfully.", &target_a);
        assert_eq!(decision.reason, CompletionPolicyReason::AmbiguousEvidence);
        assert!(!decision.allowed);

        let ambiguous_context = build(&[
            call(
                "deploy-prod",
                "deploy",
                r#"{"target":"service-a","environment":"production"}"#,
            ),
            result("deploy-prod", r#"{"output":"ok","exit_code":0}"#),
            call(
                "deploy-staging",
                "deploy",
                r#"{"target":"service-a","environment":"staging"}"#,
            ),
            result("deploy-staging", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision = completion_evidence_decision(
            "Deployment completed successfully for service-a.",
            &ambiguous_context,
        );
        assert_eq!(decision.reason, CompletionPolicyReason::AmbiguousEvidence);
        assert!(!decision.allowed);

        let successful_deploy = build(&[
            call("deploy-a", "deploy", r#"{"target":"service-a"}"#),
            result("deploy-a", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision = completion_evidence_decision(
            "Deployment completed successfully, but the deployment failed.",
            &successful_deploy,
        );
        assert_eq!(decision.reason, CompletionPolicyReason::AmbiguousClaim);
        assert!(!decision.allowed);

        let command_without_target = build(&[
            call(
                "deploy-force",
                "terminal",
                r#"{"command":"deploy --force"}"#,
            ),
            result("deploy-force", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision = completion_evidence_decision(
            "Deployment completed successfully.",
            &command_without_target,
        );
        assert_eq!(
            decision.reason,
            CompletionPolicyReason::MissingTargetBinding
        );
        assert!(!decision.allowed);

        assert!(completion_evidence_allows(
            "Deployments completed successfully for service-a and service-b.",
            &target_a
        ));

        let one_target_claim = completion_evidence_decision(
            "Deployment completed successfully for service-a.",
            &target_a,
        );
        assert!(one_target_claim.allowed);

        let unrecognized_targets = build(&[
            call(
                "deploy-a",
                "deploy",
                r#"{"destination":{"cluster":"service-a"}}"#,
            ),
            result("deploy-a", r#"{"output":"ok","exit_code":0}"#),
            call(
                "deploy-b",
                "deploy",
                r#"{"destination":{"cluster":"service-b"}}"#,
            ),
            result("deploy-b", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision = completion_evidence_decision(
            "Deployment completed successfully.",
            &unrecognized_targets,
        );
        assert_eq!(
            decision.reason,
            CompletionPolicyReason::MissingTargetBinding
        );
        assert!(!decision.allowed);

        let mixed_claim = build(&[
            call("write-1", "write", r#"{"target":"service-a"}"#),
            result("write-1", r#"{"output":"ok","exit_code":0}"#),
        ]);
        let decision = completion_evidence_decision(
            "Inspect and deploy completed successfully.",
            &mixed_claim,
        );
        assert_eq!(decision.reason, CompletionPolicyReason::AmbiguousClaim);
        assert!(!decision.allowed);

        let echo = build(&[
            call(
                "terminal-1",
                "terminal",
                r#"{"command":"echo deploy","target":"service-a"}"#,
            ),
            result("terminal-1", r#"{"output":"deploy","exit_code":0}"#),
        ]);
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &echo
        ));
    }

    #[test]
    fn legacy_ledger_evidence_is_compatible_but_never_upgraded_to_success() {
        let legacy: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id":"legacy",
                "name":"deploy",
                "arguments_digest":"old",
                "result_length":2,
                "result_digest":"old",
                "failed":false,
                "has_result":true
            }],
            "pending":[],
            "tool_rounds":1,
            "repeated_call":false,
            "repeated_failure":false
        }))
        .unwrap();
        let decision = completion_evidence_decision("Deployment completed successfully.", &legacy);
        assert_eq!(
            decision.reason,
            CompletionPolicyReason::FailedOrUnknownEvidence
        );
        assert!(!decision.allowed);
    }

    #[test]
    fn new_user_turn_resets_round_scope_but_full_ledger_keeps_pending_evidence() {
        let messages = vec![
            call("pending", "deploy", "{}"),
            OpenAiMessage::text("user", "Continue after interruption"),
        ];
        validate_tool_conversation(&messages).unwrap();
        assert_eq!(build(&messages).pending.len(), 1);
        assert_eq!(build(active_messages(&messages)).tool_rounds, 0);

        let ledger = execution_ledger(&AgentLedger::default(), &messages);
        assert_eq!(ledger.pending.len(), 1);
        let candidate = DetectedToolCall {
            id: "retry".to_owned(),
            kind: "function".to_owned(),
            function: json!({"name":"deploy","arguments":"{}"}),
        };
        let (calls, suppressed) = ledger.filter_known_calls(vec![candidate]);
        assert!(calls.is_empty());
        assert!(suppressed);
    }

    #[test]
    fn completed_call_from_previous_user_turn_can_be_reissued() {
        let messages = vec![
            OpenAiMessage::text("user", "Read the current task."),
            call("c1", "kanban_show", r#"{"task_id":"t_c3de88aa"}"#),
            result("c1", "task state"),
            OpenAiMessage::text("user", "Read the current task again."),
        ];
        let ledger = execution_ledger(&AgentLedger::default(), &messages);
        let candidate = DetectedToolCall {
            id: "c2".to_owned(),
            kind: "function".to_owned(),
            function: json!({
                "name":"kanban_show",
                "arguments":"{\"task_id\":\"t_c3de88aa\"}"
            }),
        };

        let (calls, suppressed) = ledger.filter_known_calls(vec![candidate]);

        assert_eq!(calls.len(), 1);
        assert!(!suppressed);
    }

    #[test]
    fn synthetic_empty_recovery_user_keeps_completed_tool_evidence() {
        let mut synthetic_empty = OpenAiMessage::text("assistant", "(empty)");
        synthetic_empty.empty_recovery_synthetic = true;
        let mut synthetic_recovery = OpenAiMessage::text(
            "user",
            "You just executed tool calls but returned an empty response. Please process the tool results above and continue with the task.",
        );
        synthetic_recovery.empty_recovery_synthetic = true;
        let messages = vec![
            OpenAiMessage::text("user", "Inspect the current state."),
            call("c1", "terminal", r#"{"command":"inspect service-a"}"#),
            result("c1", r#"{"output":"ok","exit_code":0,"error":null}"#),
            synthetic_empty,
            synthetic_recovery,
        ];

        let ledger = execution_ledger(&AgentLedger::default(), &messages);

        assert_eq!(
            ledger.completed.len(),
            1,
            "synthetic recovery must preserve completed evidence"
        );
        assert!(completion_evidence_allows(
            "Inspection completed successfully.",
            &ledger
        ));
    }

    #[test]
    fn persisted_pending_call_can_be_resolved_by_an_append_only_result() {
        let prior = build(&[call("pending", "deploy", r#"{"target":"service-a"}"#)]);
        let appended = vec![result("pending", r#"{"output":"ok","exit_code":0}"#)];
        validate_tool_conversation_with_prior(&appended, &prior).unwrap();
        let ledger = execution_ledger(&prior, &appended);
        assert!(ledger.pending.is_empty());
        assert_eq!(ledger.completed.len(), 1);
        assert!(completion_evidence_allows(
            "Deployment completed successfully.",
            &ledger
        ));
    }

    #[test]
    fn completion_guard_requires_a_bound_target_and_rejects_untrusted_success_text() {
        let no_target = build(&[
            call("deploy", "deploy", "{}"),
            result("deploy", r#"{"output":"ok","exit_code":0}"#),
        ]);
        assert_eq!(
            completion_evidence_decision("Deployment completed successfully.", &no_target).reason,
            CompletionPolicyReason::MissingTargetBinding
        );
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &no_target
        ));

        let target_a = build(&[
            call("deploy", "deploy", r#"{"target":"service-a"}"#),
            result("deploy", r#"{"output":"ok","exit_code":0}"#),
        ]);
        assert!(completion_evidence_allows(
            "Deployment completed successfully for service-a.",
            &target_a
        ));
        assert_eq!(
            completion_evidence_decision(
                "Deployment completed successfully for service-b.",
                &target_a
            )
            .reason,
            CompletionPolicyReason::TargetMismatch
        );
        assert!(!completion_evidence_allows(
            "Deployment completed successfully for service-b.",
            &target_a
        ));

        let untrusted = build(&[
            call("deploy", "deploy", r#"{"target":"service-a"}"#),
            result("deploy", r#"{"status":"success","output":"ok"}"#),
        ]);
        assert_eq!(
            untrusted.completed[0].result_status(),
            ToolResultStatus::Unknown
        );
        assert!(!completion_evidence_allows(
            "Deployment completed successfully.",
            &untrusted
        ));
    }
}
