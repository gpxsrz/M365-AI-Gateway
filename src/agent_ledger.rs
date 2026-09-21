use std::collections::{HashMap, HashSet};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::{protocol::OpenAiMessage, tool_calls::DetectedToolCall};

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum ToolResultStatus {
    #[default]
    Unknown,
    Success,
    Failed,
}

#[derive(Clone, Debug, Default, Serialize)]
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
    #[serde(skip)]
    status_was_present: bool,
}

#[derive(Deserialize)]
struct PersistedToolEvidence {
    id: String,
    name: String,
    arguments_digest: String,
    result_length: usize,
    result_digest: String,
    failed: bool,
    has_result: bool,
    #[serde(default)]
    result_status: Option<ToolResultStatus>,
}

impl<'de> Deserialize<'de> for ToolEvidence {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let persisted = PersistedToolEvidence::deserialize(deserializer)?;
        let status_was_present = persisted.result_status.is_some();
        Ok(Self {
            id: persisted.id,
            name: persisted.name,
            arguments_digest: persisted.arguments_digest,
            result_length: persisted.result_length,
            result_digest: persisted.result_digest,
            failed: persisted.failed,
            has_result: persisted.has_result,
            result_status: persisted.result_status.unwrap_or_default(),
            status_was_present,
        })
    }
}

impl ToolEvidence {
    fn result_status(&self) -> ToolResultStatus {
        if !self.has_result || (self.failed && self.result_status == ToolResultStatus::Success) {
            ToolResultStatus::Unknown
        } else if self.result_status != ToolResultStatus::Unknown {
            self.result_status
        } else if self.failed {
            ToolResultStatus::Failed
        } else {
            ToolResultStatus::Unknown
        }
    }

    fn is_valid_persisted_with_legacy_statusless(&self, allow_legacy_statusless: bool) -> bool {
        if !self.status_was_present && !allow_legacy_statusless {
            return false;
        }
        if self.status_was_present && !is_digest(&self.arguments_digest) {
            return false;
        }
        if !self.has_result {
            return !self.failed
                && self.result_status == ToolResultStatus::Unknown
                && self.result_length == 0
                && self.result_digest.is_empty();
        }
        match self.result_status {
            ToolResultStatus::Success => {
                !self.failed && self.result_length > 0 && is_digest(&self.result_digest)
            }
            ToolResultStatus::Failed => {
                self.failed
                    && self.has_result
                    && ((self.result_length == 0 && self.result_digest.is_empty())
                        || (self.result_length > 0 && is_digest(&self.result_digest)))
            }
            // A pre-status ledger is accepted only for migration. A current
            // explicit unknown result with bytes is malformed.
            ToolResultStatus::Unknown => {
                (allow_legacy_statusless && !self.status_was_present && self.has_result)
                    || (!self.failed && self.result_length == 0 && self.result_digest.is_empty())
            }
        }
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum SuppressionReason {
    SameBatchDuplicate,
    PendingSameCall,
    CompletedNotAuthorizedReadback,
    CompletedResultNotVerified,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub(crate) struct SuppressionDetail {
    pub(crate) registered_tool_name: String,
    pub(crate) candidate_ordinal: usize,
    pub(crate) arguments_digest: String,
    pub(crate) matched_prior_call_id: Option<String>,
    pub(crate) result_received: bool,
    pub(crate) result_classification: ToolResultStatus,
    pub(crate) blocking_reason: SuppressionReason,
    pub(crate) candidate_not_dispatched: bool,
}

#[derive(Clone, Debug, Default)]
pub(crate) struct KnownCallFilterResult {
    pub(crate) calls: Vec<DetectedToolCall>,
    pub(crate) suppression_details: Vec<SuppressionDetail>,
}

impl KnownCallFilterResult {
    pub(crate) fn suppressed(&self) -> bool {
        !self.suppression_details.is_empty()
    }
}

#[derive(Clone, Debug)]
pub(crate) struct ReplayFeedback {
    text: String,
}

impl ReplayFeedback {
    pub(crate) fn as_str(&self) -> &str {
        &self.text
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

    pub(crate) fn filter_known_calls<F>(
        &self,
        calls: Vec<DetectedToolCall>,
        allow_completed_reissue: F,
    ) -> KnownCallFilterResult
    where
        F: Fn(&str) -> bool,
    {
        let mut batch = HashSet::new();
        let mut filtered = Vec::new();
        let mut suppression_details = Vec::new();
        for (candidate_ordinal, call) in calls.into_iter().enumerate() {
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
            let argument_digest = arguments_digest(arguments);
            let identity = format!("{name}\0{argument_digest}");
            let duplicate_in_batch = !batch.insert(identity);
            // A pending effect is never safe to replay. A completed read-only
            // effect may be a new observation, but only its caller contract
            // can authorize that distinction.
            let duplicate_pending = self.pending.iter().any(|evidence| {
                is_digest(&evidence.arguments_digest)
                    && evidence.name == name
                    && evidence.arguments_digest == argument_digest
            });
            let duplicate_completed = self.completed.iter().any(|evidence| {
                is_digest(&evidence.arguments_digest)
                    && evidence.name == name
                    && evidence.arguments_digest == argument_digest
            });
            let completed_contract_allows = duplicate_completed && allow_completed_reissue(name);
            let completed_result_verified = duplicate_completed
                && self
                    .completed
                    .iter()
                    .filter(|evidence| {
                        is_digest(&evidence.arguments_digest)
                            && evidence.name == name
                            && evidence.arguments_digest == argument_digest
                    })
                    .all(|evidence| evidence.result_status() == ToolResultStatus::Success);
            let completed_reissue_allowed = completed_contract_allows && completed_result_verified;
            let duplicate = duplicate_in_batch
                || duplicate_pending
                || (duplicate_completed && !completed_reissue_allowed);
            if !duplicate {
                filtered.push(call);
                continue;
            }

            let blocking_reason = if duplicate_in_batch {
                SuppressionReason::SameBatchDuplicate
            } else if duplicate_pending {
                SuppressionReason::PendingSameCall
            } else if !completed_contract_allows {
                SuppressionReason::CompletedNotAuthorizedReadback
            } else {
                debug_assert!(!completed_result_verified);
                SuppressionReason::CompletedResultNotVerified
            };
            let matched = if duplicate_pending {
                self.pending.iter().find(|evidence| {
                    is_digest(&evidence.arguments_digest)
                        && evidence.name == name
                        && evidence.arguments_digest == argument_digest
                })
            } else {
                self.completed
                    .iter()
                    .filter(|evidence| {
                        is_digest(&evidence.arguments_digest)
                            && evidence.name == name
                            && evidence.arguments_digest == argument_digest
                    })
                    .find(|evidence| {
                        blocking_reason == SuppressionReason::CompletedResultNotVerified
                            && evidence.result_status() != ToolResultStatus::Success
                    })
                    .or_else(|| {
                        self.completed.iter().find(|evidence| {
                            is_digest(&evidence.arguments_digest)
                                && evidence.name == name
                                && evidence.arguments_digest == argument_digest
                        })
                    })
            };
            suppression_details.push(SuppressionDetail {
                registered_tool_name: name.to_owned(),
                candidate_ordinal: candidate_ordinal + 1,
                arguments_digest: argument_digest.clone(),
                matched_prior_call_id: matched.map(|evidence| evidence.id.clone()),
                result_received: matched.is_some_and(|evidence| evidence.has_result),
                result_classification: matched
                    .map_or(ToolResultStatus::Unknown, ToolEvidence::result_status),
                blocking_reason,
                candidate_not_dispatched: true,
            });
        }
        KnownCallFilterResult {
            calls: filtered,
            suppression_details,
        }
    }

    pub(crate) fn replay_feedback(
        &self,
        suppression_details: &[SuppressionDetail],
    ) -> ReplayFeedback {
        let evidence = serde_json::json!({
            "completed": self.completed,
            "pending": self.pending,
            "repeated_call": self.repeated_call,
        });
        let suppressed =
            serde_json::to_string(suppression_details).unwrap_or_else(|_| "[]".to_owned());
        ReplayFeedback {
            text: format!(
                "The original request, attached documents, and tool outputs remain task evidence according to their content and provenance. This transport summary is only for preventing unsafe re-execution; it is not a replacement for that evidence and does not decide task completion. A returned result whose success was not certified is not a missing result. The rejected candidate(s) below were not dispatched to the caller. Use an earlier applicable result when it is available; do not repeat an already satisfied prerequisite solely to obtain the same information. Do not change arguments, spelling, paths, or tools merely to evade replay protection. Continue only with a genuinely necessary operation permitted by the existing tool contract, or give an evidence-based answer when tool_choice allows it. If needed evidence is unavailable, state the limitation without inventing it or claiming success.\nEVIDENCE_LEDGER: {evidence}\nSUPPRESSED_CANDIDATES: {suppressed}"
            ),
        }
    }

    pub(crate) fn is_valid_persisted(&self) -> bool {
        self.completed
            .iter()
            .chain(&self.pending)
            .all(|evidence| evidence.is_valid_persisted_with_legacy_statusless(false))
    }

    pub(crate) fn is_valid_persisted_legacy(&self) -> bool {
        self.completed
            .iter()
            .chain(&self.pending)
            .all(|evidence| evidence.is_valid_persisted_with_legacy_statusless(true))
    }

    pub(crate) fn demote_unverified(&mut self) -> bool {
        let completed_before = self.completed.len();
        let pending_before = self.pending.len();
        self.completed
            .retain(|evidence| is_digest(&evidence.arguments_digest));
        self.pending
            .retain(|evidence| is_digest(&evidence.arguments_digest));
        let mut demoted =
            self.completed.len() != completed_before || self.pending.len() != pending_before;
        for evidence in self.completed.iter_mut().chain(self.pending.iter_mut()) {
            if !evidence.has_result {
                continue;
            }
            evidence.result_length = 0;
            evidence.result_digest.clear();
            evidence.failed = false;
            evidence.result_status = ToolResultStatus::Unknown;
            evidence.status_was_present = true;
            demoted = true;
        }
        demoted
    }

    pub(crate) fn normalize_unknown_results(&mut self) -> bool {
        let mut normalized = false;
        for evidence in self.completed.iter_mut().chain(self.pending.iter_mut()) {
            if evidence.has_result
                && evidence.result_status == ToolResultStatus::Unknown
                && evidence.result_length > 0
            {
                evidence.result_length = 0;
                evidence.result_digest.clear();
                evidence.status_was_present = true;
                normalized = true;
            }
        }
        normalized
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
            if evidence.result_status == ToolResultStatus::Unknown {
                // v1 readers have no result_status field and would otherwise
                // mistake a non-empty opaque result for a successful action.
                evidence.result_length = 0;
                evidence.result_digest.clear();
            }
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

fn tool_result_status(explicit: bool, name: &str, result: &str) -> ToolResultStatus {
    if explicit {
        return ToolResultStatus::Failed;
    }
    if name == "read_file" {
        let Ok(object) = serde_json::from_str::<serde_json::Map<String, Value>>(result.trim())
        else {
            return ToolResultStatus::Unknown;
        };
        if is_successful_read_file_result(&object) {
            return ToolResultStatus::Success;
        }
        if object.get("error").is_some_and(|error| {
            !error.is_null() && error.as_str().is_none_or(|text| !text.trim().is_empty())
        }) {
            return ToolResultStatus::Failed;
        }
        return ToolResultStatus::Unknown;
    }
    if let Ok(object) = serde_json::from_str::<serde_json::Map<String, Value>>(result.trim())
        && object.contains_key("output")
        && let Some(exit_code) = object.get("exit_code").and_then(Value::as_i64)
    {
        if ["success", "ok"]
            .iter()
            .any(|field| object.get(*field).is_some_and(|value| !value.is_boolean()))
        {
            return ToolResultStatus::Unknown;
        }
        if ["partial", "cancelled", "canceled", "incomplete"]
            .iter()
            .any(|field| match object.get(*field) {
                Some(Value::Bool(value)) => *value,
                Some(_) => true,
                None => false,
            })
            || match object.get("complete") {
                Some(Value::Bool(value)) => !value,
                Some(_) => true,
                None => false,
            }
        {
            return ToolResultStatus::Unknown;
        }
        let status = match object.get("status") {
            None => None,
            Some(Value::String(status)) => Some(status.trim().to_ascii_lowercase()),
            Some(_) => return ToolResultStatus::Unknown,
        };
        let nonempty_output = object
            .get("output")
            .and_then(Value::as_str)
            .is_some_and(|output| !output.trim().is_empty());
        let terminal_positive = object
            .get("success")
            .and_then(Value::as_bool)
            .is_some_and(|success| success)
            || object
                .get("ok")
                .and_then(Value::as_bool)
                .is_some_and(|ok| ok)
            || matches!(object.get("complete"), Some(Value::Bool(true)))
            || status.as_deref().is_some_and(|status| {
                matches!(
                    status,
                    "ok" | "success" | "succeeded" | "complete" | "completed"
                )
            });
        let failed = exit_code != 0
            || object
                .get("success")
                .and_then(Value::as_bool)
                .is_some_and(|success| !success)
            || object
                .get("ok")
                .and_then(Value::as_bool)
                .is_some_and(|ok| !ok)
            || object.get("error").is_some_and(|error| {
                !error.is_null() && error.as_str().is_none_or(|s| !s.trim().is_empty())
            })
            || status
                .as_deref()
                .is_some_and(|status| matches!(status, "error" | "failed" | "failure"));
        let incomplete = status.as_deref().is_some_and(|status| {
            !matches!(
                status,
                "ok" | "success"
                    | "succeeded"
                    | "complete"
                    | "completed"
                    | "error"
                    | "failed"
                    | "failure"
            )
        });
        return if failed {
            ToolResultStatus::Failed
        } else if incomplete || !nonempty_output || !terminal_positive {
            ToolResultStatus::Unknown
        } else {
            ToolResultStatus::Success
        };
    }
    if let Ok(value) = serde_json::from_str::<Value>(result.trim()) {
        let Some(object) = value.as_object() else {
            return ToolResultStatus::Unknown;
        };
        if ["success", "ok"]
            .iter()
            .any(|field| object.get(*field).is_some_and(|value| !value.is_boolean()))
        {
            return ToolResultStatus::Unknown;
        }
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
        return match object.get("status") {
            Some(Value::String(status))
                if matches!(
                    status.trim().to_ascii_lowercase().as_str(),
                    "error" | "failed" | "failure"
                ) =>
            {
                ToolResultStatus::Failed
            }
            _ => ToolResultStatus::Unknown,
        };
    }
    ToolResultStatus::Unknown
}

fn is_successful_read_file_result(object: &serde_json::Map<String, Value>) -> bool {
    object.get("content").is_some_and(Value::is_string)
        && object.get("file_size").and_then(Value::as_u64).is_some()
        && object.get("total_lines").and_then(Value::as_u64).is_some()
        && object.get("is_binary").is_some_and(Value::is_boolean)
        && object.get("is_image").is_some_and(Value::is_boolean)
        && object.get("truncated").is_some_and(Value::is_boolean)
        && object.get("error").is_none_or(Value::is_null)
}

fn arguments_digest(arguments: &str) -> String {
    let canonical = serde_json::from_str::<Value>(arguments.trim())
        .ok()
        .and_then(|value| serde_json::to_string(&value).ok())
        .unwrap_or_else(|| arguments.trim().to_owned());
    digest(canonical.as_bytes())
}

fn is_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
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
                "id": id,
                "type": "function",
                "function": {"name": name, "arguments": arguments}
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
            "id": "b",
            "type": "function",
            "function": {"name": "read", "arguments": "{\"path\":\"b\"}"}
        }));
        let ledger = build(&[parallel, result("a", "A"), result("b", "B")]);
        assert_eq!(ledger.tool_rounds, 1);
        let candidate = DetectedToolCall {
            id: "new".to_owned(),
            kind: "function".to_owned(),
            function: json!({"name": "read", "arguments": " { \"path\" : \"a\" } "}),
        };
        let filtered = ledger.filter_known_calls(vec![candidate], |_| false);
        assert!(filtered.calls.is_empty());
        assert!(filtered.suppressed());
    }

    #[test]
    fn suppression_details_preserve_replay_predicate_and_result_classification() {
        let successful = build(&[
            call("skill-call", "skill_view", r#"{"name":"m365-document"}"#),
            result("skill-call", "saved reading answer"),
        ]);
        let candidate = DetectedToolCall {
            id: "candidate".to_owned(),
            kind: "function".to_owned(),
            function: json!({
                "name": "skill_view",
                "arguments": r#"{"name":"m365-document"}"#,
            }),
        };
        let filtered = successful.filter_known_calls(vec![candidate.clone()], |_| false);
        assert!(filtered.calls.is_empty());
        assert_eq!(filtered.suppression_details.len(), 1);
        assert_eq!(
            filtered.suppression_details[0].registered_tool_name,
            "skill_view"
        );
        assert_eq!(filtered.suppression_details[0].candidate_ordinal, 1);
        assert_eq!(
            filtered.suppression_details[0].arguments_digest,
            arguments_digest(r#"{"name":"m365-document"}"#)
        );
        assert_eq!(
            filtered.suppression_details[0].blocking_reason,
            SuppressionReason::CompletedNotAuthorizedReadback
        );
        assert_eq!(
            filtered.suppression_details[0]
                .matched_prior_call_id
                .as_deref(),
            Some("skill-call")
        );
        assert!(filtered.suppression_details[0].result_received);
        assert_eq!(
            filtered.suppression_details[0].result_classification,
            ToolResultStatus::Unknown
        );
        assert!(filtered.suppression_details[0].candidate_not_dispatched);

        let read_only = build(&[
            call("read-call", "read_file", r#"{"path":"report.txt"}"#),
            result(
                "read-call",
                r#"{"content":"report","file_size":6,"is_binary":false,"is_image":false,"total_lines":1,"truncated":false}"#,
            ),
        ]);
        let accepted = read_only
            .filter_known_calls(vec![candidate_for("read_file")], |name| name == "read_file");
        assert_eq!(accepted.calls.len(), 1);
        assert!(accepted.suppression_details.is_empty());

        let unknown = build(&[
            call("unknown-call", "read_file", r#"{"path":"report.txt"}"#),
            result("unknown-call", "untyped result"),
        ]);
        let filtered = unknown
            .filter_known_calls(vec![candidate_for("read_file")], |name| name == "read_file");
        assert!(filtered.calls.is_empty());
        assert_eq!(
            filtered.suppression_details[0].blocking_reason,
            SuppressionReason::CompletedResultNotVerified
        );
        assert!(filtered.suppression_details[0].result_received);
        assert_eq!(
            filtered.suppression_details[0].result_classification,
            ToolResultStatus::Unknown
        );

        let failed = build(&[
            call("failed-call", "read_file", r#"{"path":"report.txt"}"#),
            result(
                "failed-call",
                r#"{"content":"partial","file_size":7,"is_binary":false,"is_image":false,"total_lines":1,"truncated":false,"error":"denied"}"#,
            ),
        ]);
        let filtered =
            failed.filter_known_calls(vec![candidate_for("read_file")], |name| name == "read_file");
        assert_eq!(
            filtered.suppression_details[0].blocking_reason,
            SuppressionReason::CompletedResultNotVerified
        );
        assert_eq!(
            filtered.suppression_details[0].result_classification,
            ToolResultStatus::Failed
        );
    }

    #[test]
    fn replay_feedback_keeps_task_evidence_and_exact_rejected_candidate() {
        let ledger = build(&[
            call(
                "skill-routing",
                "skill_view",
                r#"{"name":"m365-document-attachment-routing"}"#,
            ),
            result("skill-routing", "saved reading answer"),
            call(
                "skill-governance",
                "skill_view",
                r#"{"name":"hermes-single-agent-governance"}"#,
            ),
            result(
                "skill-governance",
                "verification result: gateway-only authority",
            ),
        ]);
        let candidate = candidate_for_args(
            "skill_view",
            r#"{"name":"m365-document-attachment-routing"}"#,
        );
        let filtered = ledger.filter_known_calls(vec![candidate], |_| false);
        let feedback = ledger.replay_feedback(&filtered.suppression_details);
        assert!(feedback.as_str().contains(
            "The original request, attached documents, and tool outputs remain task evidence"
        ));
        assert!(feedback.as_str().contains("skill_view"));
        assert!(feedback.as_str().contains("skill-routing"));
        assert!(feedback.as_str().contains("skill-governance"));
        assert!(feedback.as_str().contains("candidate_not_dispatched"));
        assert!(feedback.as_str().contains("arguments_digest"));
        assert!(
            !feedback
                .as_str()
                .contains("Use only this compact transport evidence")
        );
    }

    fn candidate_for(name: &str) -> DetectedToolCall {
        let arguments = if name == "skill_view" {
            r#"{"name":"m365-document"}"#
        } else {
            r#"{"path":"report.txt"}"#
        };
        candidate_for_args(name, arguments)
    }

    fn candidate_for_args(name: &str, arguments: &str) -> DetectedToolCall {
        DetectedToolCall {
            id: "candidate".to_owned(),
            kind: "function".to_owned(),
            function: json!({"name": name, "arguments": arguments}),
        }
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
    fn untyped_success_flags_never_authorize_a_transport_success() {
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
    fn typed_success_requires_nonempty_output() {
        let success = build(&[
            call("c1", "terminal", "{}"),
            result(
                "c1",
                r#"{"output":"ok","exit_code":0,"status":"completed"}"#,
            ),
        ]);
        assert_eq!(
            success.completed[0].result_status(),
            ToolResultStatus::Success
        );
        assert!(success.completed[0].result_length > 0);
        assert!(is_digest(&success.completed[0].result_digest));

        let incomplete = build(&[
            call("c2", "terminal", "{}"),
            result("c2", r#"{"output":"","exit_code":0,"status":"completed"}"#),
        ]);
        assert_eq!(
            incomplete.completed[0].result_status(),
            ToolResultStatus::Unknown
        );
        assert_eq!(incomplete.completed[0].result_length, 0);
        assert!(incomplete.completed[0].result_digest.is_empty());
    }

    #[test]
    fn actual_hermes_read_file_result_is_success_only_for_complete_shape() {
        let success = build(&[
            call("c1", "read_file", r#"{"path":"report.txt"}"#),
            result(
                "c1",
                r#"{"content":"new-bytes\n","file_size":10,"is_binary":false,"is_image":false,"total_lines":1,"truncated":false}"#,
            ),
        ]);
        assert_eq!(
            success.completed[0].result_status(),
            ToolResultStatus::Success
        );

        for (content, expected) in [
            (
                r#"{"content":"new-bytes\n","file_size":10,"total_lines":1,"truncated":false}"#,
                ToolResultStatus::Unknown,
            ),
            (
                r#"{"content":"new-bytes\n","file_size":10,"is_binary":false,"is_image":false,"total_lines":1,"truncated":false,"error":"write failed"}"#,
                ToolResultStatus::Failed,
            ),
            (
                r#"{"success":true,"content":"new-bytes\n"}"#,
                ToolResultStatus::Unknown,
            ),
            (
                r#"{"output":"new-bytes","exit_code":0,"status":"completed"}"#,
                ToolResultStatus::Unknown,
            ),
        ] {
            let ledger = build(&[
                call("c2", "read_file", r#"{"path":"report.txt"}"#),
                result("c2", content),
            ]);
            assert_eq!(
                ledger.completed[0].result_status(),
                expected,
                "content={content}"
            );
        }
    }

    #[test]
    fn persisted_success_requires_nonempty_valid_result_digest() {
        for result_digest in [String::new(), "malformed".to_owned()] {
            let ledger: AgentLedger = serde_json::from_value(json!({
                "completed": [{
                    "id": "success",
                    "name": "terminal",
                    "arguments_digest": digest(b"{}"),
                    "result_length": 2,
                    "result_digest": result_digest,
                    "failed": false,
                    "has_result": true,
                    "result_status": "success"
                }],
                "pending": [],
                "tool_rounds": 1,
                "repeated_call": false,
                "repeated_failure": false
            }))
            .unwrap();
            assert!(!ledger.is_valid_persisted());
        }

        let ledger: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "success",
                "name": "terminal",
                "arguments_digest": digest(b"{}"),
                "result_length": 2,
                "result_digest": digest(b"ok"),
                "failed": false,
                "has_result": true,
                "result_status": "success"
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        assert!(ledger.is_valid_persisted());
    }

    #[test]
    fn persisted_non_success_requires_transport_integrity_fields() {
        let malformed_arguments = serde_json::from_value::<AgentLedger>(json!({
            "completed": [{
                "id": "failed",
                "name": "terminal",
                "arguments_digest": "malformed",
                "result_length": 2,
                "result_digest": digest(b"no"),
                "failed": true,
                "has_result": true,
                "result_status": "failed"
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        assert!(!malformed_arguments.is_valid_persisted());

        for (result_status, result_digest) in [("failed", "malformed"), ("unknown", "malformed")] {
            let ledger: AgentLedger = serde_json::from_value(json!({
                "completed": [{
                    "id": "result",
                    "name": "terminal",
                    "arguments_digest": digest(b"{}"),
                    "result_length": 0,
                    "result_digest": result_digest,
                    "failed": result_status == "failed",
                    "has_result": true,
                    "result_status": result_status
                }],
                "pending": [],
                "tool_rounds": 1,
                "repeated_call": false,
                "repeated_failure": false
            }))
            .unwrap();
            assert!(!ledger.is_valid_persisted(), "status={result_status}");
        }
    }

    #[test]
    fn legacy_result_is_accepted_only_for_conservative_migration() {
        let mut legacy: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "legacy",
                "name": "deploy",
                "arguments_digest": digest(b"{}"),
                "result_length": 2,
                "result_digest": "old",
                "failed": false,
                "has_result": true
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        assert!(legacy.is_valid_persisted_legacy());
        assert!(legacy.demote_unverified());
        assert_eq!(
            legacy.completed[0].result_status(),
            ToolResultStatus::Unknown
        );
        assert_eq!(legacy.completed[0].result_length, 0);
        assert!(legacy.completed[0].result_digest.is_empty());
    }

    #[test]
    fn legacy_malformed_argument_identity_is_dropped_before_v2_save() {
        let mut legacy: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "legacy",
                "name": "deploy",
                "arguments_digest": "old",
                "result_length": 0,
                "result_digest": "",
                "failed": false,
                "has_result": false
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        assert!(legacy.is_valid_persisted_legacy());
        assert!(legacy.demote_unverified());
        assert!(legacy.completed.is_empty());
        assert!(legacy.pending.is_empty());
        assert!(legacy.is_valid_persisted());
    }

    #[test]
    fn current_schema_cannot_bypass_statusless_ledger_validation() {
        let statusless: AgentLedger = serde_json::from_value(json!({
            "completed": [{
                "id": "statusless",
                "name": "deploy",
                "arguments_digest": "old",
                "result_length": 2,
                "result_digest": "old",
                "failed": false,
                "has_result": true
            }],
            "pending": [],
            "tool_rounds": 1,
            "repeated_call": false,
            "repeated_failure": false
        }))
        .unwrap();
        assert!(!statusless.is_valid_persisted());
        assert!(statusless.is_valid_persisted_legacy());
    }

    #[test]
    fn current_unknown_result_is_not_success_for_legacy_readers() {
        let current = build(&[
            call("c1", "terminal", "{}"),
            result("c1", r#"{"status":"success","output":"ok"}"#),
        ]);
        assert_eq!(
            current.completed[0].result_status(),
            ToolResultStatus::Unknown
        );
        let encoded = serde_json::to_value(current).unwrap();
        assert_eq!(encoded["completed"][0]["result_length"], 0);
        assert_eq!(encoded["completed"][0]["result_digest"], "");
        assert_eq!(encoded["completed"][0]["failed"], false);
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
            function: json!({"name": "deploy", "arguments": "{}"}),
        };
        let filtered = ledger.filter_known_calls(vec![candidate], |_| false);
        assert!(filtered.calls.is_empty());
        assert!(filtered.suppressed());
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
                "name": "kanban_show",
                "arguments": "{\"task_id\":\"t_c3de88aa\"}"
            }),
        };
        let filtered = ledger.filter_known_calls(vec![candidate], |_| false);
        assert_eq!(filtered.calls.len(), 1);
        assert!(!filtered.suppressed());
    }

    #[test]
    fn read_only_completed_call_can_be_reissued_but_pending_and_batch_duplicates_cannot() {
        let completed = execution_ledger(
            &AgentLedger::default(),
            &[
                OpenAiMessage::text("user", "Read the current file."),
                call("c1", "read_file", r#"{"path":"report.txt"}"#),
                result(
                    "c1",
                    r#"{"content":"report-v1","file_size":9,"is_binary":false,"is_image":false,"total_lines":1,"truncated":false}"#,
                ),
            ],
        );
        let candidate = DetectedToolCall {
            id: "c2".to_owned(),
            kind: "function".to_owned(),
            function: json!({
                "name": "read_file",
                "arguments": "{\"path\":\"report.txt\"}"
            }),
        };
        let filtered =
            completed.filter_known_calls(vec![candidate.clone()], |name| name == "read_file");
        assert_eq!(filtered.calls.len(), 1);
        assert!(!filtered.suppressed());

        let pending = build(&[call("c1", "read_file", r#"{"path":"report.txt"}"#)]);
        let filtered =
            pending.filter_known_calls(vec![candidate.clone()], |name| name == "read_file");
        assert!(filtered.calls.is_empty());
        assert!(filtered.suppressed());
        assert_eq!(
            filtered.suppression_details[0].blocking_reason,
            SuppressionReason::PendingSameCall
        );
        assert_eq!(
            filtered.suppression_details[0]
                .matched_prior_call_id
                .as_deref(),
            Some("c1")
        );
        assert!(!filtered.suppression_details[0].result_received);
        assert_eq!(
            filtered.suppression_details[0].result_classification,
            ToolResultStatus::Unknown
        );

        let filtered = completed.filter_known_calls(vec![candidate.clone(), candidate], |name| {
            name == "read_file"
        });
        assert_eq!(filtered.calls.len(), 1);
        assert!(filtered.suppressed());
        assert_eq!(filtered.suppression_details.len(), 1);
        assert_eq!(
            filtered.suppression_details[0].blocking_reason,
            SuppressionReason::SameBatchDuplicate
        );
        assert_eq!(
            filtered.suppression_details[0]
                .matched_prior_call_id
                .as_deref(),
            Some("c1")
        );
        assert!(filtered.suppression_details[0].result_received);
        assert_eq!(
            filtered.suppression_details[0].result_classification,
            ToolResultStatus::Success
        );
    }

    #[test]
    fn unknown_read_only_result_cannot_authorize_a_reissue() {
        let ledger = execution_ledger(
            &AgentLedger::default(),
            &[
                OpenAiMessage::text("user", "Read the current file."),
                call("c1", "read_file", r#"{"path":"report.txt"}"#),
                result(
                    "c1",
                    r#"{"path":"report.txt","sha256":"opaque","status":"completed"}"#,
                ),
            ],
        );
        assert_eq!(ledger.completed.len(), 1);
        assert_eq!(
            ledger.completed[0].result_status(),
            ToolResultStatus::Unknown
        );

        let candidate = DetectedToolCall {
            id: "c2".to_owned(),
            kind: "function".to_owned(),
            function: json!({
                "name": "read_file",
                "arguments": "{\"path\":\"report.txt\"}"
            }),
        };
        let filtered = ledger.filter_known_calls(vec![candidate], |name| name == "read_file");
        assert!(filtered.calls.is_empty());
        assert!(filtered.suppressed());
    }

    #[test]
    fn synthetic_recovery_messages_keep_completed_transport_evidence() {
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
            result(
                "c1",
                r#"{"output":"ok","exit_code":0,"error":null,"status":"completed"}"#,
            ),
            synthetic_empty,
            synthetic_recovery,
        ];
        let ledger = execution_ledger(&AgentLedger::default(), &messages);
        assert_eq!(ledger.completed.len(), 1);
        assert_eq!(
            ledger.completed[0].result_status(),
            ToolResultStatus::Success
        );
    }

    #[test]
    fn persisted_pending_call_can_be_resolved_by_an_append_only_result() {
        let prior = build(&[call("pending", "deploy", r#"{"target":"service-a"}"#)]);
        let appended = vec![result(
            "pending",
            r#"{"output":"ok","exit_code":0,"status":"completed"}"#,
        )];
        validate_tool_conversation_with_prior(&appended, &prior).unwrap();
        let ledger = execution_ledger(&prior, &appended);
        assert!(ledger.pending.is_empty());
        assert_eq!(ledger.completed.len(), 1);
        assert_eq!(
            ledger.completed[0].result_status(),
            ToolResultStatus::Success
        );
    }
}
