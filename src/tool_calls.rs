use rand::Rng;
use serde::Serialize;
use serde_json::{Value, json};

use crate::chathub::Tool;

#[derive(Clone, Debug, Serialize)]
pub struct DetectedToolCall {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    pub function: Value,
}

#[derive(Clone, Debug, Default)]
pub struct ToolProjection {
    pub content: String,
    pub calls: Vec<DetectedToolCall>,
    pub overflowed: bool,
}

pub fn project(text: &str, tools: &[Tool], choice: &Value, limit: usize) -> ToolProjection {
    if tools.is_empty() || choice.as_str() == Some("none") {
        return ToolProjection {
            content: text.to_owned(),
            calls: Vec::new(),
            overflowed: false,
        };
    }
    let mut output = ToolProjection::default();
    let lines = text.lines().collect::<Vec<_>>();
    let mut cursor = 0;
    let limit = limit.max(1);
    while cursor < lines.len() {
        let line = lines[cursor].trim();
        let Some(name) = line.strip_prefix("```").map(str::trim) else {
            append_line(&mut output.content, lines[cursor]);
            cursor += 1;
            continue;
        };
        if name.is_empty() || name.contains(char::is_whitespace) {
            append_line(&mut output.content, lines[cursor]);
            cursor += 1;
            continue;
        }
        let Some(relative_end) = lines[cursor + 1..]
            .iter()
            .position(|candidate| candidate.trim() == "```")
        else {
            append_line(&mut output.content, lines[cursor]);
            cursor += 1;
            continue;
        };
        let end = cursor + 1 + relative_end;
        let raw_arguments = lines[cursor + 1..end].join("\n");
        let recognized = tool(tools, name)
            .filter(|_| choice_allows(choice, name))
            .zip(
                serde_json::from_str::<Value>(raw_arguments.trim())
                    .ok()
                    .filter(Value::is_object),
            );
        if let Some((tool, arguments)) = recognized {
            if output.calls.len() < limit {
                output.calls.push(DetectedToolCall {
                    id: random_call_id(),
                    kind: if tool.kind == "custom" {
                        "custom".to_owned()
                    } else {
                        "function".to_owned()
                    },
                    function: json!({
                        "name": name,
                        "arguments": serde_json::to_string(&arguments).unwrap_or_else(|_| "{}".to_owned())
                    }),
                });
            } else {
                output.overflowed = true;
            }
        } else {
            for original in &lines[cursor..=end] {
                append_line(&mut output.content, original);
            }
        }
        cursor = end + 1;
    }
    output.content = output.content.trim().to_owned();
    output
}

fn tool<'a>(tools: &'a [Tool], name: &str) -> Option<&'a Tool> {
    tools.iter().find(|tool| {
        tool.function
            .get("name")
            .and_then(Value::as_str)
            .is_some_and(|candidate| candidate == name)
    })
}

fn choice_allows(choice: &Value, name: &str) -> bool {
    match choice {
        Value::Null => true,
        Value::String(mode) => !mode.eq_ignore_ascii_case("none"),
        Value::Object(object) => object
            .get("function")
            .and_then(|function| function.get("name"))
            .and_then(Value::as_str)
            .or_else(|| object.get("name").and_then(Value::as_str))
            .is_none_or(|candidate| candidate == name),
        _ => false,
    }
}

fn append_line(output: &mut String, line: &str) {
    if !output.is_empty() {
        output.push('\n');
    }
    output.push_str(line);
}

fn random_call_id() -> String {
    let mut bytes = [0_u8; 16];
    rand::rng().fill(&mut bytes);
    let suffix = bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    format!("call_{suffix}")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tools() -> Vec<Tool> {
        vec![Tool {
            kind: "function".to_owned(),
            function: json!({"name":"read_file","parameters":{"type":"object"}}),
        }]
    }

    #[test]
    fn recognized_fence_becomes_a_call_and_is_removed_from_text() {
        let output = project(
            "checking\n```read_file\n{\"path\":\"README.md\"}\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert_eq!(output.content, "checking");
        assert_eq!(output.calls.len(), 1);
        assert_eq!(output.calls[0].function["name"], "read_file");
        assert_eq!(
            output.calls[0].function["arguments"],
            r#"{"path":"README.md"}"#
        );
    }

    #[test]
    fn unknown_or_invalid_fence_remains_visible_text() {
        let output = project(
            "```delete_everything\n{}\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert!(output.calls.is_empty());
        assert!(output.content.contains("delete_everything"));
    }

    #[test]
    fn valid_calls_beyond_the_limit_fail_closed_instead_of_truncating() {
        let output = project(
            "```read_file\n{\"path\":\"a\"}\n```\n```read_file\n{\"path\":\"b\"}\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert_eq!(output.calls.len(), 1);
        assert!(output.overflowed);
    }
}
