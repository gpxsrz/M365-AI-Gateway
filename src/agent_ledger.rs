use std::collections::{HashMap, HashSet};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::{protocol::OpenAiMessage, tool_calls::DetectedToolCall};

pub(crate) const UNCONFIRMED_TOOL_OUTCOME: &str = "I cannot confirm completion because no matching tool results were returned. No external action has been verified.";

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub(crate) struct ToolEvidence {
    id: String,
    name: String,
    arguments_digest: String,
    result_length: usize,
    result_digest: String,
    failed: bool,
    has_result: bool,
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

    pub(crate) fn has_failed_completed_evidence(&self) -> bool {
        self.completed.iter().any(|evidence| evidence.failed)
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
            "user" => pending.clear(),
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
    let Some(last_user) = messages.iter().rposition(|message| message.role == "user") else {
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
            evidence.failed =
                tool_result_failed(message.tool_result_is_error, &evidence.name, &result);
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
            if evidence.failed {
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
    let last_user = messages.iter().rposition(|message| message.role == "user");
    last_user.map_or(messages, |index| &messages[index..])
}

pub(crate) fn completion_evidence_allows(answer: &str, ledger: &AgentLedger) -> bool {
    let claims_success = claims_unsupported_success(answer);
    if !ledger.pending.is_empty() {
        return !claims_success;
    }
    if !ledger.completed.is_empty() {
        return !claims_success
            || ledger
                .completed
                .iter()
                .any(|evidence| evidence.result_length > 0 && !evidence.failed);
    }
    !claims_success
}

fn tool_result_failed(explicit: bool, name: &str, result: &str) -> bool {
    if explicit {
        return true;
    }
    if name == "terminal"
        && let Ok(object) = serde_json::from_str::<serde_json::Map<String, Value>>(result.trim())
        && object.contains_key("output")
        && let Some(exit_code) = object.get("exit_code").and_then(Value::as_i64)
    {
        return exit_code != 0
            || object.get("error").is_some_and(|error| {
                !error.is_null() && error.as_str().is_none_or(|s| !s.trim().is_empty())
            });
    }
    if let Ok(value) = serde_json::from_str::<Value>(result.trim()) {
        let Some(object) = value.as_object() else {
            return false;
        };
        if let Some(success) = object.get("success").and_then(Value::as_bool) {
            return !success;
        }
        if let Some(ok) = object.get("ok").and_then(Value::as_bool) {
            return !ok;
        }
        if object.get("error").is_some_and(|error| {
            !error.is_null() && error.as_str().is_none_or(|text| !text.trim().is_empty())
        }) {
            return true;
        }
        return object
            .get("status")
            .and_then(Value::as_str)
            .is_some_and(|status| {
                matches!(
                    status.trim().to_ascii_lowercase().as_str(),
                    "error" | "failed" | "failure"
                )
            });
    }
    let lower = result.to_ascii_lowercase();
    [
        "error",
        "failed",
        "failure",
        "exception",
        "traceback",
        "timed out",
        "timeout",
        "permission denied",
        "not found",
        "refused",
        "exit code 1",
        "exit status 1",
    ]
    .iter()
    .any(|needle| lower.contains(needle))
}

fn claims_unsupported_success(answer: &str) -> bool {
    let lower = answer.to_ascii_lowercase();
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
    success_words.iter().any(|word| contains_word(&lower, word))
        || [
            "service is running",
            "server is active",
            "app is now running",
        ]
        .iter()
        .any(|phrase| lower.contains(phrase))
        || [
            "issue is fixed",
            "bug was resolved",
            "problem has been fixed",
        ]
        .iter()
        .any(|phrase| lower.contains(phrase))
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
    fn structured_tool_success_ignores_null_error_fields() {
        for (content, failed) in [
            (
                r#"{"success":true,"job":{"last_delivery_error":null,"last_fire_error":null}}"#,
                false,
            ),
            (r#"{"ok":true,"error":null}"#, false),
            (r#"{"success":false,"error":"update rejected"}"#, true),
            (r#"{"ok":false,"error":"not found"}"#, true),
        ] {
            let ledger = build(&[call("c1", "cronjob", "{}"), result("c1", content)]);
            assert_eq!(ledger.completed[0].failed, failed, "content={content}");
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
            call("c1", "terminal", "{}"),
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
    fn persisted_pending_call_can_be_resolved_by_an_append_only_result() {
        let prior = build(&[call("pending", "deploy", "{}")]);
        let appended = vec![result("pending", "ok")];
        validate_tool_conversation_with_prior(&appended, &prior).unwrap();
        let ledger = execution_ledger(&prior, &appended);
        assert!(ledger.pending.is_empty());
        assert_eq!(ledger.completed.len(), 1);
        assert!(completion_evidence_allows(
            "Deployment completed successfully.",
            &ledger
        ));
    }
}
