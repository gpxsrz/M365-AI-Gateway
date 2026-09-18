use rand::Rng;
use serde::Serialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

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
    pub diagnostic: Option<ToolDiagnostic>,
    pub rejection: Option<ToolRejection>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ToolRejectionClass {
    StrictJsonValid,
    LiteralControlCharInsideJsonString,
    MalformedJsonStructure,
    UnclosedString,
    IllegalEscape,
    ExtraProseBeforeCall,
    ExtraProseAfterValidCall,
    MalformedFence,
    DuplicateOrAmbiguous,
    MoreCallsThanAllowed,
    KnownToolDisallowedByToolChoice,
    UnknownToolFence,
    Other,
}

impl ToolRejectionClass {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::StrictJsonValid => "strict_json_valid",
            Self::LiteralControlCharInsideJsonString => "literal_control_char_inside_json_string",
            Self::MalformedJsonStructure => "malformed_json_structure",
            Self::UnclosedString => "unclosed_string",
            Self::IllegalEscape => "illegal_escape",
            Self::ExtraProseBeforeCall => "extra_prose_before_call",
            Self::ExtraProseAfterValidCall => "extra_prose_after_valid_call",
            Self::MalformedFence => "malformed_fence",
            Self::DuplicateOrAmbiguous => "duplicate_or_ambiguous",
            Self::MoreCallsThanAllowed => "more_calls_than_allowed",
            Self::KnownToolDisallowedByToolChoice => "known_tool_disallowed_by_tool_choice",
            Self::UnknownToolFence => "unknown_tool_fence",
            Self::Other => "other",
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ToolDiagnostic {
    pub class: ToolRejectionClass,
    pub candidate_sha256: String,
    pub candidate_bytes: usize,
    pub candidate_chars: usize,
    pub candidate_lines: usize,
    pub fence_count: usize,
    pub matching_known_tool_fence_count: usize,
    pub parse_error_offset: Option<usize>,
}

pub type ToolRejection = ToolDiagnostic;

impl ToolDiagnostic {
    fn from_shape(
        class: ToolRejectionClass,
        candidate: &str,
        full_text: &str,
        tools: &[Tool],
        parse_error_offset: Option<usize>,
    ) -> Self {
        Self {
            class,
            candidate_sha256: format!("{:x}", Sha256::digest(candidate.as_bytes())),
            candidate_bytes: candidate.len(),
            candidate_chars: candidate.chars().count(),
            candidate_lines: candidate.lines().count(),
            fence_count: fence_count(full_text),
            matching_known_tool_fence_count: matching_known_tool_fence_count(full_text, tools),
            parse_error_offset,
        }
    }
}

pub fn project(text: &str, tools: &[Tool], choice: &Value, limit: usize) -> ToolProjection {
    if tools.is_empty() {
        return ToolProjection {
            content: text.to_owned(),
            calls: Vec::new(),
            overflowed: false,
            rejected: false,
            diagnostic: None,
            rejection: None,
        };
    }
    let mut output = ToolProjection::default();
    let lines = text.lines().collect::<Vec<_>>();
    let mut cursor = 0;
    let limit = limit.max(1);
    let mut pre_call_content = false;
    while cursor < lines.len() {
        let line = lines[cursor].trim();
        let Some(name) = line.strip_prefix("```").map(str::trim) else {
            append_projection_content(
                &mut output,
                lines[cursor],
                &mut pre_call_content,
                text,
                tools,
            );
            cursor += 1;
            continue;
        };
        if name.is_empty() || name.contains(char::is_whitespace) {
            append_projection_content(
                &mut output,
                lines[cursor],
                &mut pre_call_content,
                text,
                tools,
            );
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
                match escaped_closing_fence_arguments(&lines[cursor + 1..]) {
                    Some(raw) => match parse_object_arguments(raw) {
                        ArgumentParseOutcome::Success { value, class } => {
                            set_diagnostic(&mut output, class, raw, text, tools, None);
                            append_call(&mut output, tool, name, value, limit);
                        }
                        ArgumentParseOutcome::Failure { class, offset } => {
                            reject(&mut output, class, raw, text, tools, offset);
                        }
                    },
                    None => reject(
                        &mut output,
                        ToolRejectionClass::MalformedFence,
                        &lines[cursor..].join("\n"),
                        text,
                        tools,
                        None,
                    ),
                }
                cursor = lines.len();
            } else if choice_allows_name && has_matching_tool {
                reject(
                    &mut output,
                    ToolRejectionClass::DuplicateOrAmbiguous,
                    lines[cursor],
                    text,
                    tools,
                    None,
                );
                cursor = lines.len();
            } else if has_matching_tool {
                set_diagnostic(
                    &mut output,
                    ToolRejectionClass::KnownToolDisallowedByToolChoice,
                    lines[cursor],
                    text,
                    tools,
                    None,
                );
                append_projection_content(
                    &mut output,
                    lines[cursor],
                    &mut pre_call_content,
                    text,
                    tools,
                );
                cursor += 1;
            } else {
                // Unknown or disallowed fences are caller-visible Markdown, not
                // executable candidates. Matching malformed candidates fail closed.
                set_diagnostic(
                    &mut output,
                    ToolRejectionClass::UnknownToolFence,
                    lines[cursor],
                    text,
                    tools,
                    None,
                );
                append_projection_content(
                    &mut output,
                    lines[cursor],
                    &mut pre_call_content,
                    text,
                    tools,
                );
                cursor += 1;
            }
            continue;
        };
        let end = cursor + 1 + relative_end;
        let raw_arguments = lines[cursor + 1..end].join("\n");
        if let Some(tool) = allowed_tool {
            match parse_object_arguments(&raw_arguments) {
                ArgumentParseOutcome::Success { value, class } => {
                    set_diagnostic(&mut output, class, &raw_arguments, text, tools, None);
                    append_call(&mut output, tool, name, value, limit);
                }
                ArgumentParseOutcome::Failure { class, offset } => {
                    reject(&mut output, class, &raw_arguments, text, tools, offset);
                }
            }
        } else if choice_allows_name && has_matching_tool {
            reject(
                &mut output,
                ToolRejectionClass::DuplicateOrAmbiguous,
                &raw_arguments,
                text,
                tools,
                None,
            );
        } else if has_matching_tool {
            set_diagnostic(
                &mut output,
                ToolRejectionClass::KnownToolDisallowedByToolChoice,
                &raw_arguments,
                text,
                tools,
                None,
            );
            for original in &lines[cursor..=end] {
                append_projection_content(
                    &mut output,
                    original,
                    &mut pre_call_content,
                    text,
                    tools,
                );
            }
        } else {
            // A known tool definition is required before textual Markdown can
            // become a caller-tool projection.
            set_diagnostic(
                &mut output,
                ToolRejectionClass::UnknownToolFence,
                &raw_arguments,
                text,
                tools,
                None,
            );
            for original in &lines[cursor..=end] {
                append_projection_content(
                    &mut output,
                    original,
                    &mut pre_call_content,
                    text,
                    tools,
                );
            }
        }
        cursor = end + 1;
    }
    output.content = if choice.as_str() == Some("none") {
        text.to_owned()
    } else {
        output.content.trim().to_owned()
    };
    if output.rejected {
        output.calls.clear();
    } else if output.overflowed {
        let diagnostic = ToolRejection::from_shape(
            ToolRejectionClass::MoreCallsThanAllowed,
            text,
            text,
            tools,
            None,
        );
        output.diagnostic = Some(diagnostic.clone());
        output.rejection = Some(diagnostic);
    } else if !output.calls.is_empty() && pre_call_content {
        output.diagnostic = Some(ToolDiagnostic::from_shape(
            ToolRejectionClass::ExtraProseBeforeCall,
            text,
            text,
            tools,
            None,
        ));
    }
    output
}

enum ArgumentParseOutcome {
    Success {
        value: Value,
        class: ToolRejectionClass,
    },
    Failure {
        class: ToolRejectionClass,
        offset: Option<usize>,
    },
}

struct JsonStringScan {
    repaired: String,
    changed: bool,
    unclosed_string: bool,
    illegal_escape_offset: Option<usize>,
}

fn parse_object_arguments(raw: &str) -> ArgumentParseOutcome {
    let raw = raw.trim();
    match serde_json::from_str::<Value>(raw) {
        Ok(value) if value.is_object() => ArgumentParseOutcome::Success {
            value,
            class: ToolRejectionClass::StrictJsonValid,
        },
        Ok(_) => ArgumentParseOutcome::Failure {
            class: ToolRejectionClass::Other,
            offset: None,
        },
        Err(error) => {
            let scan = scan_json_string(raw);
            if scan.changed {
                match serde_json::from_str::<Value>(&scan.repaired) {
                    Ok(value) if value.is_object() => ArgumentParseOutcome::Success {
                        value,
                        class: ToolRejectionClass::LiteralControlCharInsideJsonString,
                    },
                    Ok(_) => ArgumentParseOutcome::Failure {
                        class: ToolRejectionClass::Other,
                        offset: Some(parse_error_offset(raw, &error)),
                    },
                    Err(repaired_error) => ArgumentParseOutcome::Failure {
                        class: scan_failure_class(&scan),
                        offset: scan
                            .illegal_escape_offset
                            .or_else(|| Some(parse_error_offset(raw, &repaired_error))),
                    },
                }
            } else {
                ArgumentParseOutcome::Failure {
                    class: scan_failure_class(&scan),
                    offset: scan
                        .illegal_escape_offset
                        .or_else(|| Some(parse_error_offset(raw, &error))),
                }
            }
        }
    }
}

fn scan_failure_class(scan: &JsonStringScan) -> ToolRejectionClass {
    if scan.illegal_escape_offset.is_some() {
        ToolRejectionClass::IllegalEscape
    } else if scan.unclosed_string {
        ToolRejectionClass::UnclosedString
    } else {
        ToolRejectionClass::MalformedJsonStructure
    }
}

fn parse_error_offset(raw: &str, error: &serde_json::Error) -> usize {
    let line = error.line().saturating_sub(1);
    let column = error.column().saturating_sub(1);
    let mut line_start = 0;
    for _ in 0..line {
        let Some(relative) = raw[line_start..].find('\n') else {
            return raw.len();
        };
        line_start = line_start.saturating_add(relative + 1);
    }
    line_start.saturating_add(column).min(raw.len())
}

fn set_diagnostic(
    output: &mut ToolProjection,
    class: ToolRejectionClass,
    candidate: &str,
    full_text: &str,
    tools: &[Tool],
    parse_error_offset: Option<usize>,
) {
    if output.diagnostic.is_none() {
        output.diagnostic = Some(ToolDiagnostic::from_shape(
            class,
            candidate,
            full_text,
            tools,
            parse_error_offset,
        ));
    }
}

fn reject(
    output: &mut ToolProjection,
    class: ToolRejectionClass,
    candidate: &str,
    full_text: &str,
    tools: &[Tool],
    parse_error_offset: Option<usize>,
) {
    output.rejected = true;
    if output.rejection.is_none() {
        let diagnostic =
            ToolRejection::from_shape(class, candidate, full_text, tools, parse_error_offset);
        output.diagnostic = Some(diagnostic.clone());
        output.rejection = Some(diagnostic);
    }
}

fn scan_json_string(raw: &str) -> JsonStringScan {
    let mut repaired = String::with_capacity(raw.len());
    let mut in_string = false;
    let mut escaped = false;
    let mut unicode_remaining = 0;
    let mut changed = false;
    let mut illegal_escape_offset = None;
    for (offset, ch) in raw.char_indices() {
        if !in_string {
            if ch == '"' {
                in_string = true;
            }
            repaired.push(ch);
            continue;
        }
        if escaped {
            if ch == 'u' {
                unicode_remaining = 4;
            } else if !matches!(ch, '"' | '\\' | '/' | 'b' | 'f' | 'n' | 'r' | 't')
                && illegal_escape_offset.is_none()
            {
                illegal_escape_offset = Some(offset);
            }
            repaired.push(ch);
            escaped = false;
            continue;
        }
        if unicode_remaining > 0 {
            if !ch.is_ascii_hexdigit() && illegal_escape_offset.is_none() {
                illegal_escape_offset = Some(offset);
            }
            unicode_remaining -= 1;
            repaired.push(ch);
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
            ch if (ch as u32) <= 0x1f => {
                repaired.push_str(&format!("\\u{:04x}", ch as u32));
                changed = true;
            }
            _ => repaired.push(ch),
        }
    }
    JsonStringScan {
        repaired,
        changed,
        unclosed_string: in_string,
        illegal_escape_offset,
    }
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

fn fence_count(text: &str) -> usize {
    text.lines()
        .filter(|line| line.trim_start().as_bytes().starts_with(&[96, 96, 96]))
        .count()
}

fn matching_known_tool_fence_count(text: &str, tools: &[Tool]) -> usize {
    text.lines()
        .filter_map(|line| {
            let trimmed = line.trim();
            let bytes = trimmed.as_bytes();
            bytes.starts_with(&[96, 96, 96]).then(|| &trimmed[3..])
        })
        .filter(|name| {
            !name.is_empty()
                && !name.contains(char::is_whitespace)
                && tools.iter().any(|tool| {
                    tool.function
                        .get("name")
                        .and_then(Value::as_str)
                        .is_some_and(|candidate| candidate == *name)
                })
        })
        .count()
}

fn append_projection_content(
    output: &mut ToolProjection,
    line: &str,
    pre_call_content: &mut bool,
    full_text: &str,
    tools: &[Tool],
) {
    if output.calls.is_empty() && !line.trim().is_empty() {
        *pre_call_content = true;
    }
    // A structured call must be the complete executable projection. Any
    // non-whitespace material after it makes the candidate ambiguous (for
    // example, a second Markdown/code example), so the caller must fail
    // closed instead of executing only the first block.
    if !output.calls.is_empty() && !line.trim().is_empty() {
        let class = if line.trim_start().starts_with("```") {
            ToolRejectionClass::DuplicateOrAmbiguous
        } else {
            ToolRejectionClass::ExtraProseAfterValidCall
        };
        reject(output, class, line, full_text, tools, None);
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

    #[test]
    fn rejected_projection_reports_only_a_bounded_shape_class() {
        let mut duplicate = tools();
        duplicate.push(duplicate[0].clone());
        let cases = [
            (
                "```read_file\n{\"path\":\n```",
                tools(),
                ToolRejectionClass::MalformedJsonStructure,
            ),
            (
                "```read_file\n{\"path\":\"README.md\"}\n```\nexplanation",
                tools(),
                ToolRejectionClass::ExtraProseAfterValidCall,
            ),
            (
                "```read_file\n{\"path\":\"README.md\"}",
                tools(),
                ToolRejectionClass::MalformedFence,
            ),
            (
                "```read_file\n{\"path\":\"README.md\"}\n```",
                duplicate,
                ToolRejectionClass::DuplicateOrAmbiguous,
            ),
            (
                "```read_file\n{\"path\":\"a\"}\n```\n```read_file\n{\"path\":\"b\"}\n```",
                tools(),
                ToolRejectionClass::MoreCallsThanAllowed,
            ),
            ("```read_file\n[1]\n```", tools(), ToolRejectionClass::Other),
        ];
        for (text, available_tools, expected) in cases {
            let output = project(text, &available_tools, &Value::String("auto".to_owned()), 1);
            let rejection = output.rejection.expect("diagnostic rejection shape");
            assert_eq!(rejection.class, expected, "text={text}");
            assert!(rejection.candidate_bytes <= text.len());
            assert!(rejection.candidate_lines <= text.lines().count());
        }
    }

    #[test]
    fn projection_diagnostic_distinguishes_safe_rejection_taxonomy_and_shape() {
        let mut duplicate = tools();
        duplicate.push(duplicate[0].clone());
        let cases = [
            (
                "```read_file\n{\"path\":\"ok\"}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::StrictJsonValid,
            ),
            (
                "```read_file\n{\"path\":\"line-one\nline-two\"}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::LiteralControlCharInsideJsonString,
            ),
            (
                "```read_file\n{\"path\":\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::MalformedJsonStructure,
            ),
            (
                "```read_file\n{\"path\":\"unterminated}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::UnclosedString,
            ),
            (
                "```read_file\n{\"path\":\"bad\\q\"}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::IllegalEscape,
            ),
            (
                "prefix\n```read_file\n{\"path\":\"ok\"}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::ExtraProseBeforeCall,
            ),
            (
                "```read_file\n{\"path\":\"ok\"}\n```\nexplanation",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::ExtraProseAfterValidCall,
            ),
            (
                "```read_file\n{\"path\":\"ok\"}",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::MalformedFence,
            ),
            (
                "```read_file\n{\"path\":\"ok\"}\n```",
                duplicate,
                Value::String("auto".to_owned()),
                ToolRejectionClass::DuplicateOrAmbiguous,
            ),
            (
                "```read_file\n{\"path\":\"a\"}\n```\n```read_file\n{\"path\":\"b\"}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::MoreCallsThanAllowed,
            ),
            (
                "```read_file\n{\"path\":\"ok\"}\n```",
                tools(),
                Value::String("none".to_owned()),
                ToolRejectionClass::KnownToolDisallowedByToolChoice,
            ),
            (
                "```unknown_tool\n{}\n```",
                tools(),
                Value::String("auto".to_owned()),
                ToolRejectionClass::UnknownToolFence,
            ),
        ];
        for (text, available_tools, choice, expected) in cases {
            let output = project(text, &available_tools, &choice, 1);
            let diagnostic = output.diagnostic.as_ref().expect("diagnostic shape");
            assert_eq!(diagnostic.class, expected, "text={text}");
            assert_eq!(diagnostic.candidate_sha256.len(), 64);
            assert!(diagnostic.candidate_bytes <= text.len());
            assert!(diagnostic.candidate_chars <= text.chars().count());
            assert!(diagnostic.fence_count <= text.lines().count());
            assert!(diagnostic.matching_known_tool_fence_count <= diagnostic.fence_count);
            if matches!(
                expected,
                ToolRejectionClass::MalformedJsonStructure
                    | ToolRejectionClass::UnclosedString
                    | ToolRejectionClass::IllegalEscape
                    | ToolRejectionClass::MalformedFence
                    | ToolRejectionClass::DuplicateOrAmbiguous
                    | ToolRejectionClass::ExtraProseAfterValidCall
            ) {
                assert!(output.rejected, "text={text}");
                assert!(output.calls.is_empty(), "text={text}");
            }
            if expected == ToolRejectionClass::MoreCallsThanAllowed {
                assert!(output.overflowed, "text={text}");
            }
            if matches!(
                expected,
                ToolRejectionClass::MalformedJsonStructure
                    | ToolRejectionClass::UnclosedString
                    | ToolRejectionClass::IllegalEscape
            ) {
                assert!(diagnostic.parse_error_offset.is_some(), "text={text}");
            }
        }
    }
}
