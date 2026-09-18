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
    pub rejected: bool,
}

pub fn project(text: &str, tools: &[Tool], choice: &Value, limit: usize) -> ToolProjection {
    if tools.is_empty() || choice.as_str() == Some("none") {
        return ToolProjection {
            content: text.to_owned(),
            calls: Vec::new(),
            overflowed: false,
            rejected: false,
        };
    }
    let mut output = ToolProjection::default();
    let lines = text.lines().collect::<Vec<_>>();
    let mut cursor = 0;
    let limit = limit.max(1);
    while cursor < lines.len() {
        let line = lines[cursor].trim();
        let Some(name) = line.strip_prefix("```").map(str::trim) else {
            append_projection_content(&mut output, lines[cursor]);
            cursor += 1;
            continue;
        };
        if name.is_empty() || name.contains(char::is_whitespace) {
            append_projection_content(&mut output, lines[cursor]);
            cursor += 1;
            continue;
        }
        let choice_allows_name = choice_allows(choice, name);
        let has_matching_tool = tools.iter().any(|tool| {
            tool.function
                .get("name")
                .and_then(Value::as_str)
                .is_some_and(|candidate| candidate == name)
        });
        let allowed_tool = tool(tools, name).filter(|_| choice_allows_name);
        let Some(relative_end) = lines[cursor + 1..]
            .iter()
            .position(|candidate| candidate.trim() == "```")
        else {
            if let Some(tool) = allowed_tool {
                if let Some(arguments) = escaped_closing_fence_arguments(&lines[cursor + 1..])
                    .and_then(parse_object_arguments)
                {
                    append_call(&mut output, tool, name, arguments, limit);
                } else {
                    output.rejected = true;
                }
                cursor = lines.len();
            } else if choice_allows_name && has_matching_tool {
                output.rejected = true;
                cursor = lines.len();
            } else {
                // Unknown or disallowed fences are caller-visible Markdown, not
                // executable candidates. Matching malformed candidates fail closed.
                append_projection_content(&mut output, lines[cursor]);
                cursor += 1;
            }
            continue;
        };
        let end = cursor + 1 + relative_end;
        let raw_arguments = lines[cursor + 1..end].join("\n");
        if let Some(tool) = allowed_tool {
            if let Some(arguments) = parse_object_arguments(&raw_arguments) {
                append_call(&mut output, tool, name, arguments, limit);
            } else {
                output.rejected = true;
            }
        } else if choice_allows_name && has_matching_tool {
            output.rejected = true;
        } else {
            // A known tool definition is required before textual Markdown can
            // become a caller-tool projection.
            for original in &lines[cursor..=end] {
                append_projection_content(&mut output, original);
            }
        }
        cursor = end + 1;
    }
    output.content = output.content.trim().to_owned();
    if output.rejected {
        output.calls.clear();
    }
    output
}

fn parse_object_arguments(raw: &str) -> Option<Value> {
    let raw = raw.trim();
    serde_json::from_str::<Value>(raw)
        .ok()
        .filter(Value::is_object)
        .or_else(|| {
            let repaired = escape_json_string_control_chars(raw)?;
            serde_json::from_str::<Value>(&repaired)
                .ok()
                .filter(Value::is_object)
        })
}

fn escape_json_string_control_chars(raw: &str) -> Option<String> {
    let mut repaired = String::with_capacity(raw.len());
    let mut in_string = false;
    let mut escaped = false;
    let mut changed = false;
    for ch in raw.chars() {
        if !in_string {
            if ch == '"' {
                in_string = true;
            }
            repaired.push(ch);
            continue;
        }
        if escaped {
            repaired.push(ch);
            escaped = false;
            continue;
        }
        match ch {
            '\\' => {
                repaired.push(ch);
                escaped = true;
            }
            '"' => {
                repaired.push(ch);
                in_string = false;
            }
            '\n' => {
                repaired.push_str("\\n");
                changed = true;
            }
            '\r' => {
                repaired.push_str("\\r");
                changed = true;
            }
            '\t' => {
                repaired.push_str("\\t");
                changed = true;
            }
            '\u{0000}'..='\u{001f}' => {
                repaired.push_str(&format!("\\u{:04x}", ch as u32));
                changed = true;
            }
            _ => repaired.push(ch),
        }
    }
    changed.then_some(repaired)
}

fn escaped_closing_fence_arguments<'a>(lines: &[&'a str]) -> Option<&'a str> {
    (lines.len() == 1)
        .then(|| lines[0])
        .and_then(|raw| raw.strip_suffix(r"\n```").map(str::trim))
}

fn append_call(
    output: &mut ToolProjection,
    tool: &Tool,
    name: &str,
    arguments: Value,
    limit: usize,
) {
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
}

fn tool<'a>(tools: &'a [Tool], name: &str) -> Option<&'a Tool> {
    let mut matches = tools.iter().filter(|tool| {
        tool.function
            .get("name")
            .and_then(Value::as_str)
            .is_some_and(|candidate| candidate == name)
    });
    let first = matches.next()?;
    if matches.next().is_some() {
        None
    } else {
        Some(first)
    }
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

fn append_projection_content(output: &mut ToolProjection, line: &str) {
    // A structured call must be the complete executable projection. Any
    // non-whitespace material after it makes the candidate ambiguous (for
    // example, a second Markdown/code example), so the caller must fail
    // closed instead of executing only the first block.
    if !output.calls.is_empty() && !line.trim().is_empty() {
        output.rejected = true;
    }
    append_line(&mut output.content, line);
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
        assert!(!output.rejected);
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
        assert!(!output.rejected);
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

    #[test]
    fn a_call_followed_by_unknown_markdown_is_ambiguous_and_fails_closed() {
        let output = project(
            "```read_file\n{\"path\":\"README.md\"}\n```\n```python\nprint('example')\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert!(output.calls.is_empty());
        assert!(output.rejected);
    }

    #[test]
    fn escaped_closing_fence_is_repaired_only_at_the_exact_tail() {
        let output = project(
            "```read_file\n{\"path\":\"README.md\"}\\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert_eq!(output.calls.len(), 1);
        assert!(output.content.is_empty());
        assert!(!output.rejected);
    }

    #[test]
    fn literal_newlines_inside_json_string_are_repaired_without_changing_arguments() {
        let output = project(
            "```read_file\n{\"path\":\"line-one\nline-two\"}\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert_eq!(output.calls.len(), 1);
        assert!(!output.rejected);
        let arguments: Value =
            serde_json::from_str(output.calls[0].function["arguments"].as_str().unwrap()).unwrap();
        assert_eq!(arguments["path"], "line-one\nline-two");
    }

    #[test]
    fn malformed_or_ambiguous_matching_fence_is_rejected_without_a_call() {
        for text in [
            "```read_file\n{\"path\":\n```",
            "```read_file\n{\"path\":\"README.md\"}\nextra\n```",
            "```read_file\n{\"path\":\"README.md\"}\n```\nmore",
            "```read_file\n{\"path\":\"README.md\"}\\n```\nmore",
            "```read_file\n{\"path\":\"README.md\"}\\n``` ",
            "```read_file\n{\n\"path\":\"README.md\"}\n\\n```",
        ] {
            let output = project(text, &tools(), &Value::String("auto".to_owned()), 1);
            assert!(output.calls.is_empty(), "text={text}");
            assert!(output.rejected, "text={text}");
        }
    }

    #[test]
    fn choice_none_and_unknown_markdown_never_execute() {
        let none = project(
            "```read_file\n{\"path\":\"README.md\"}\\n```",
            &tools(),
            &Value::String("none".to_owned()),
            1,
        );
        assert!(none.calls.is_empty());
        assert!(!none.rejected);

        let unknown = project(
            "```terminal\n{\"command\":\"id\"}\n```",
            &tools(),
            &Value::String("auto".to_owned()),
            1,
        );
        assert!(unknown.calls.is_empty());
        assert!(!unknown.rejected);
        assert!(unknown.content.contains("terminal"));
    }

    #[test]
    fn required_and_specific_choices_never_accept_an_invalid_matching_candidate() {
        let text = "```read_file\n{\"path\":\n```";
        for choice in [
            Value::String("required".to_owned()),
            json!({"type":"function","function":{"name":"read_file"}}),
        ] {
            let output = project(text, &tools(), &choice, 1);
            assert!(output.calls.is_empty());
            assert!(output.rejected);
        }
        let other_choice = json!({"type":"function","function":{"name":"other"}});
        let output = project(text, &tools(), &other_choice, 1);
        assert!(output.calls.is_empty());
        assert!(!output.rejected);
    }

    #[test]
    fn duplicate_tool_definitions_are_not_a_unique_execution_target() {
        let mut duplicate = tools();
        duplicate.push(duplicate[0].clone());
        let output = project(
            "```read_file\n{\"path\":\"README.md\"}\n```",
            &duplicate,
            &Value::String("auto".to_owned()),
            1,
        );
        assert!(output.calls.is_empty());
        assert!(output.rejected);
    }
}
