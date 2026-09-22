//! Public checkpoint lifecycle regression. All inputs and files are synthetic.
//! The cross-language Hermes/SDK regression is a separate qualification gate.

use m365_ai_gateway::checkpoint::{Binding, CheckpointError, CheckpointMessage, CheckpointStore};
use serde_json::{Value, json};

fn message(role: &str, content: &str) -> CheckpointMessage {
    CheckpointMessage {
        role: role.to_owned(),
        content: Value::String(content.to_owned()),
        empty_recovery_synthetic: false,
        name: String::new(),
        tool_call_id: String::new(),
        tool_calls: Vec::new(),
        tool_result_is_error: false,
    }
}

fn call(arguments: &str) -> CheckpointMessage {
    let mut message = message("assistant", "");
    message.content = Value::Null;
    message.tool_calls = vec![json!({
        "id": "call_unicode_fixture", "type": "function",
        "function": {"name": "todo_list", "arguments": arguments}
    })];
    message
}

fn assert_continuation(original: &str, incoming: &str, accepted: bool) {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("checkpoints.json");
    let store = CheckpointStore::open(&path).unwrap();
    let user = message("user", "Synthetic argument identity test.");
    let binding = Binding {
        conversation_id: "fixture".to_owned(),
        session_id: "upstream".to_owned(),
    };
    store
        .begin_full("hermes", "owner", "key", std::slice::from_ref(&user), false)
        .unwrap()
        .accept(binding, &[call(original)])
        .unwrap();
    let before = std::fs::read(&path).unwrap();
    let mut result = message("tool", r#"{"ok":true}"#);
    result.tool_call_id = "call_unicode_fixture".to_owned();
    let continuation = store.begin_full(
        "hermes",
        "owner",
        "key",
        &[user, call(incoming), result],
        false,
    );
    assert_eq!(
        continuation.is_ok(),
        accepted,
        "{original} -> {incoming}: {:?}",
        continuation.as_ref().err()
    );
    if !accepted {
        assert!(matches!(
            continuation,
            Err(CheckpointError::ConversationDrift | CheckpointError::InvalidArguments)
        ));
        assert_eq!(
            std::fs::read(&path).unwrap(),
            before,
            "denial must not rewrite durable evidence"
        );
    }
}

#[test]
fn equivalent_arguments_use_one_strict_lossless_identity() {
    for (original, incoming) in [
        (
            r#"{"x":"中文😀"}"#,
            r#"{ "\u0078" : "\u4E2D\u6587\uD83D\uDE00" }"#,
        ),
        (
            r#"{"a":[{"x":"a/b"},true,null],"b":2}"#,
            r#"{"b":2,"a":[{"x":"a\/b"},true,null]}"#,
        ),
        (
            r#"{"x":18446744073709551616001}"#,
            r#"{ "x" : 18446744073709551616001 }"#,
        ),
        (r#"{"x":1.2300e3}"#, r#"{"x":1230.0}"#),
        (
            r#"{"x":1.0e999999999999999999999999}"#,
            r#"{"x":10.0e999999999999999999999998}"#,
        ),
        (
            r#"{"x":1.0e-999999999999999999999999}"#,
            r#"{"x":10.0e-1000000000000000000000000}"#,
        ),
        (r#"{"x":-0.0}"#, r#"{"x":-0e200}"#),
    ] {
        assert_continuation(original, incoming, true);
    }
}

#[test]
fn real_value_type_string_or_array_changes_are_rejected() {
    for (original, changed) in [
        (r#"{"x":"中"}"#, r#"{"x":"文"}"#),
        (r#"{"x":"中"}"#, r#"{"x":"\\u4e2d"}"#),
        (r#"{"x":"\n"}"#, r#"{"x":"\\n"}"#),
        (r#"{"x":"é"}"#, r#"{"x":"e\u0301"}"#),
        (r#"{"x":"Ａ"}"#, r#"{"x":"A"}"#),
        (r#"{"x":[1,2]}"#, r#"{"x":[2,1]}"#),
        (r#"{"x":true}"#, r#"{"x":1}"#),
        (r#"{"x":1}"#, r#"{"x":"1"}"#),
        (r#"{"x":1}"#, r#"{"x":1.0}"#),
        (r#"{"x":null}"#, r#"{"x":""}"#),
        (r#"{"x":9007199254740992}"#, r#"{"x":9007199254740993}"#),
        (
            r#"{"x":18446744073709551616001}"#,
            r#"{"x":18446744073709551616002}"#,
        ),
        (
            r#"{"x":1.0000000000000000000001}"#,
            r#"{"x":1.0000000000000000000002}"#,
        ),
        (r#"{"x":-0.0}"#, r#"{"x":0.0}"#),
    ] {
        assert_continuation(original, changed, false);
    }
}

#[test]
fn ambiguous_and_invalid_arguments_have_no_comparison_identity() {
    for invalid in [
        r#"{"x":1,"x":1}"#,
        r#"{"x":1,"\u0078":1}"#,
        r#"{"a":{"x":1,"x":2}}"#,
        r#"{"x":1,}"#,
        r#"{"x":"\q"}"#,
        r#"{"x":"\ud800"}"#,
        r#"{"x":"\ude00"}"#,
        r#"{"x":NaN}"#,
        r#"{"x":01}"#,
        r#"{"x":1} trailing"#,
        r#"[]"#,
        r#"null"#,
        "{\"x\":\"literal\nnewline\"}",
    ] {
        assert_continuation(r#"{"x":1}"#, invalid, false);
    }
}

#[test]
fn call_id_name_and_role_remain_part_of_message_identity() {
    for field in ["id", "name", "role"] {
        let root = tempfile::tempdir().unwrap();
        let store = CheckpointStore::open(root.path().join("checkpoints.json")).unwrap();
        let user = message("user", "Synthetic identity test.");
        let original = call(r#"{"x":"中"}"#);
        store
            .begin_full("hermes", "owner", "key", std::slice::from_ref(&user), false)
            .unwrap()
            .accept(
                Binding {
                    conversation_id: "c".into(),
                    session_id: "s".into(),
                },
                std::slice::from_ref(&original),
            )
            .unwrap();
        let mut changed = original;
        match field {
            "id" => changed.tool_calls[0]["id"] = json!("other-call"),
            "name" => changed.tool_calls[0]["function"]["name"] = json!("other_tool"),
            _ => changed.role = "user".to_owned(),
        }
        assert!(matches!(
            store.begin_full("hermes", "owner", "key", &[user, changed], false),
            Err(CheckpointError::ConversationDrift)
        ));
    }
}

#[test]
fn unicode_argument_reserialization_resumes_after_restart_without_rewriting_history() {
    let root = tempfile::tempdir().unwrap();
    let path = root.path().join("checkpoints.json");
    let store = CheckpointStore::open(&path).unwrap();
    let user = message("user", "Update the synthetic checklist.");
    let original = call(r#"{"text":"中文😀","done":false}"#);
    let binding = Binding {
        conversation_id: "fixture-conversation".to_owned(),
        session_id: "fixture-upstream-session".to_owned(),
    };
    store
        .begin_full(
            "hermes",
            "fixture-owner",
            "fixture-session",
            std::slice::from_ref(&user),
            false,
        )
        .unwrap()
        .accept(binding.clone(), &[original])
        .unwrap();
    let before: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    let accepted_raw = before["records"][0]["messageDigests"]
        .as_array()
        .unwrap()
        .clone();
    drop(store);

    let reopened = CheckpointStore::open(&path).unwrap();
    let reserialized = call(r#"{"done":false,"text":"\u4e2d\u6587\ud83d\ude00"}"#);
    let mut result = message("tool", r#"{"ok":true}"#);
    result.tool_call_id = "call_unicode_fixture".to_owned();
    let continuation = reopened.begin_full(
        "hermes",
        "fixture-owner",
        "fixture-session",
        &[user, reserialized, result],
        false,
    );
    assert!(
        continuation.is_ok(),
        "legal JSON reserialization must resume: {:?}",
        continuation.err()
    );
    let continuation = continuation.unwrap();
    assert_eq!(
        continuation.binding.conversation_id,
        binding.conversation_id
    );
    assert_eq!(
        continuation.outbound.len(),
        1,
        "accepted tool call must not replay"
    );
    assert_eq!(continuation.outbound[0].role, "tool");
    continuation
        .accept(binding, &[message("assistant", "Done.")])
        .unwrap();
    let after: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
    assert_eq!(
        &after["records"][0]["messageDigests"].as_array().unwrap()[..accepted_raw.len()],
        accepted_raw
    );
}
