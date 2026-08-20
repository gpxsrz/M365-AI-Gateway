use std::{convert::Infallible, sync::Arc};

use axum::{
    Json,
    body::{Body, Bytes, to_bytes},
    extract::{Request, State},
    http::{HeaderValue, StatusCode, header},
    response::{IntoResponse, Response},
};
use futures_util::stream;
use rand::Rng;
use serde::Deserialize;
use serde_json::{Map, Value, json};
use time::OffsetDateTime;

use crate::{
    chathub::Tool,
    error::openai_error,
    protocol::{ChatCompletionRequest, OpenAiMessage, execute_chat_request},
    web::{ApiKeyOwner, Gateway},
};

const BODY_LIMIT: usize = 16 * 1024 * 1024;
const CUSTOM_EXEC_INSTRUCTION: &str = "You are operating through the caller's local OpenCode execution bridge. Use the caller-provided exec tool only for local filesystem and command execution. Do not use Microsoft 365 native execution or file-mutation tools for those operations. Microsoft 365 native Bing web search, citations, grounding, and read-only information retrieval remain allowed. The executor already starts in the caller-selected project workspace. Use relative paths only; never guess, cd to, or write under /root, /workspace, /tmp, or any other absolute project path. Inspect pwd and ls before changes. Do not create files outside the current working directory. Never claim a file was created, modified, or verified until custom exec returns a successful result. After every execution, use custom exec to verify the result.";

#[derive(Default, Deserialize)]
struct ResponsesRequest {
    #[serde(default)]
    model: String,
    #[serde(default)]
    instructions: String,
    #[serde(default)]
    input: Value,
    #[serde(default)]
    tools: Vec<Value>,
    #[serde(default)]
    tool_choice: Value,
    #[serde(default)]
    parallel_tool_calls: Option<bool>,
    #[serde(default)]
    stream: bool,
    #[serde(default)]
    previous_response_id: String,
    #[serde(default)]
    conversation: String,
    #[serde(default)]
    new_conversation: bool,
    #[serde(default)]
    reasoning: Reasoning,
}

#[derive(Default, Deserialize)]
struct Reasoning {
    #[serde(default)]
    effort: String,
}

#[derive(Default, Deserialize)]
struct AnthropicRequest {
    #[serde(default)]
    model: String,
    #[serde(default)]
    system: Value,
    #[serde(default)]
    messages: Vec<AnthropicMessage>,
    #[serde(default)]
    tools: Vec<AnthropicTool>,
    #[serde(default)]
    tool_choice: Value,
    #[serde(default)]
    stream: bool,
    #[serde(default)]
    max_tokens: usize,
}

#[derive(Default, Deserialize)]
struct AnthropicMessage {
    #[serde(default)]
    role: String,
    #[serde(default)]
    content: Value,
}

#[derive(Default, Deserialize)]
struct AnthropicTool {
    #[serde(default)]
    name: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    input_schema: Value,
    #[serde(default)]
    annotations: Value,
}

pub async fn responses(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let owner = api_key_owner(&request);
    let artifact_origin = crate::web::artifact_origin(&request);
    let body = match read::<ResponsesRequest>(request).await {
        Ok(body) => body,
        Err(response) => return response,
    };
    let stream = body.stream;
    let response_id = format!("resp_{}", random_id());
    let model = if body.model.trim().is_empty() {
        "m365-copilot".to_owned()
    } else {
        body.model.clone()
    };
    let chat = match responses_chat(body, &response_id) {
        Ok(chat) => chat,
        Err(message) => return invalid(message),
    };
    let response = execute_chat_request(
        gateway,
        "/v1/responses".to_owned(),
        owner,
        artifact_origin,
        chat,
    )
    .await;
    let (status, value) = match response_json(response).await {
        Ok(output) => output,
        Err(response) => return response,
    };
    if !status.is_success() {
        return (status, Json(value)).into_response();
    }
    project_responses(&response_id, &model, stream, value)
}

pub async fn anthropic_messages(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let owner = api_key_owner(&request);
    let artifact_origin = crate::web::artifact_origin(&request);
    let body = match read::<AnthropicRequest>(request).await {
        Ok(body) => body,
        Err(response) => return anthropic_error_response(response).await,
    };
    let stream = body.stream;
    let max_tokens = body.max_tokens;
    let model = if body.model.trim().is_empty() {
        "claude-sonnet".to_owned()
    } else {
        body.model.clone()
    };
    let chat = match anthropic_chat(body) {
        Ok(chat) => chat,
        Err(message) => {
            return anthropic_headers(
                anthropic_error_value(
                    StatusCode::BAD_REQUEST,
                    json!({"error":{"type":"invalid_request_error","code":"invalid_request","message":message}}),
                ),
                stream,
                max_tokens,
            );
        }
    };
    let response = execute_chat_request(
        gateway,
        "/v1/messages".to_owned(),
        owner,
        artifact_origin,
        chat,
    )
    .await;
    let (status, value) = match response_json(response).await {
        Ok(output) => output,
        Err(response) => {
            return anthropic_headers(anthropic_error_response(response).await, stream, max_tokens);
        }
    };
    if !status.is_success() {
        return anthropic_headers(anthropic_error_value(status, value), stream, max_tokens);
    }
    anthropic_headers(project_anthropic(&model, stream, value), stream, max_tokens)
}

fn anthropic_headers(mut response: Response, stream: bool, max_tokens: usize) -> Response {
    if stream {
        response.headers_mut().insert(
            "x-m365-streaming-semantics",
            HeaderValue::from_static("posthoc-adapter"),
        );
    }
    if max_tokens > 0 {
        response.headers_mut().insert(
            "x-m365-ignored-parameters",
            HeaderValue::from_static("max_tokens"),
        );
    }
    response
}

fn responses_chat(
    body: ResponsesRequest,
    response_id: &str,
) -> Result<ChatCompletionRequest, &'static str> {
    if !body.previous_response_id.is_empty() && !body.conversation.is_empty() {
        return Err("previous_response_id 與 conversation 不能同時使用");
    }
    let mut messages = Vec::new();
    if !body.instructions.trim().is_empty() {
        messages.push(OpenAiMessage::text("system", body.instructions));
    }
    match body.input {
        Value::String(text) if !text.is_empty() => messages.push(OpenAiMessage::text("user", text)),
        Value::Array(items) => {
            let mut same_turn_calls = Vec::new();
            let flush_calls = |messages: &mut Vec<OpenAiMessage>, calls: &mut Vec<Value>| {
                if calls.is_empty() {
                    return;
                }
                messages.push(OpenAiMessage {
                    role: "assistant".to_owned(),
                    content: Value::Null,
                    tool_calls: std::mem::take(calls),
                    ..OpenAiMessage::default()
                });
            };
            for item in items {
                let Some(object) = item.as_object() else {
                    flush_calls(&mut messages, &mut same_turn_calls);
                    continue;
                };
                match object
                    .get("type")
                    .and_then(Value::as_str)
                    .unwrap_or("message")
                {
                    "function_call_progress" => {
                        flush_calls(&mut messages, &mut same_turn_calls);
                        if object
                            .get("call_id")
                            .and_then(Value::as_str)
                            .is_none_or(|value| value.trim().is_empty())
                            || object
                                .get("message")
                                .and_then(Value::as_str)
                                .is_none_or(|value| value.trim().is_empty())
                        {
                            return Err("invalid function_call_progress");
                        }
                    }
                    "function_call_output" | "custom_tool_call_output" => {
                        flush_calls(&mut messages, &mut same_turn_calls);
                        messages.push(OpenAiMessage {
                            role: "tool".to_owned(),
                            content: object.get("output").cloned().unwrap_or(Value::Null),
                            tool_call_id: object
                                .get("call_id")
                                .and_then(Value::as_str)
                                .unwrap_or_default()
                                .to_owned(),
                            ..OpenAiMessage::default()
                        });
                    }
                    "function_call" | "custom_tool_call" => {
                        let name = object
                            .get("name")
                            .and_then(Value::as_str)
                            .unwrap_or_default();
                        let arguments = object
                            .get("arguments")
                            .cloned()
                            .or_else(|| {
                                object
                                    .get("input")
                                    .cloned()
                                    .map(|input| json!({"input": input}))
                            })
                            .unwrap_or_else(|| json!({}));
                        same_turn_calls.push(json!({
                            "id": object.get("call_id").and_then(Value::as_str).unwrap_or_default(),
                            "type": if object.get("type").and_then(Value::as_str) == Some("custom_tool_call") { "custom" } else { "function" },
                            "function": {"name": name, "arguments": arguments_string(arguments)}
                        }));
                    }
                    "message" | "" => {
                        flush_calls(&mut messages, &mut same_turn_calls);
                        messages.push(OpenAiMessage {
                            role: object
                                .get("role")
                                .and_then(Value::as_str)
                                .unwrap_or("user")
                                .to_owned(),
                            content: object.get("content").cloned().unwrap_or(Value::Null),
                            ..OpenAiMessage::default()
                        });
                    }
                    _ => flush_calls(&mut messages, &mut same_turn_calls),
                }
            }
            flush_calls(&mut messages, &mut same_turn_calls);
        }
        _ => return Err("input 必須是文字或陣列"),
    }
    if messages.is_empty() {
        return Err("input required");
    }
    let has_custom_exec = body.tools.iter().any(|tool| {
        tool.get("type").and_then(Value::as_str) == Some("custom")
            && tool.get("name").and_then(Value::as_str) == Some("exec")
    });
    let tools = body
        .tools
        .iter()
        .filter_map(|tool| response_tool(tool, has_custom_exec))
        .collect();
    if has_custom_exec {
        messages.insert(0, OpenAiMessage::text("system", CUSTOM_EXEC_INSTRUCTION));
    }
    let (checkpoint_mode, checkpoint_parent, session_key, checkpoint_force_new) = if body
        .new_conversation
        || (body.previous_response_id.is_empty() && body.conversation.is_empty())
    {
        ("full", String::new(), String::new(), true)
    } else if !body.previous_response_id.is_empty() {
        ("parent", body.previous_response_id, String::new(), false)
    } else {
        ("append", String::new(), body.conversation, false)
    };
    Ok(ChatCompletionRequest {
        model: body.model,
        messages,
        stream: false,
        session_key,
        tools,
        tool_choice: body.tool_choice,
        parallel_tool_calls: body.parallel_tool_calls,
        reasoning_effort: body.reasoning.effort,
        checkpoint_mode: checkpoint_mode.to_owned(),
        checkpoint_namespace: "responses".to_owned(),
        checkpoint_parent,
        checkpoint_response_id: response_id.to_owned(),
        checkpoint_force_new,
        ..ChatCompletionRequest::default()
    })
}

fn response_tool(value: &Value, custom_exec_only: bool) -> Option<Tool> {
    let kind = value
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let name = value.get("name").and_then(Value::as_str)?.trim();
    if name.is_empty()
        || !matches!(kind, "function" | "custom")
        || (custom_exec_only && !(kind == "custom" && name == "exec"))
    {
        return None;
    }
    let parameters = if kind == "custom" && name == "exec" {
        json!({
            "type":"object",
            "properties":{"input":{"type":"string"}},
            "required":["input"],
            "additionalProperties":false
        })
    } else {
        value
            .get("parameters")
            .cloned()
            .unwrap_or_else(|| json!({}))
    };
    Some(Tool {
        kind: kind.to_owned(),
        function: json!({
            "name": name,
            "description": value.get("description").and_then(Value::as_str).unwrap_or_default(),
            "parameters": parameters
        }),
    })
}

fn anthropic_chat(body: AnthropicRequest) -> Result<ChatCompletionRequest, &'static str> {
    let mut messages = Vec::new();
    if !body.system.is_null() {
        messages.push(OpenAiMessage {
            role: "system".to_owned(),
            content: anthropic_text_content(&body.system)?,
            ..OpenAiMessage::default()
        });
    }
    for message in body.messages {
        match message.content {
            Value::String(text) => messages.push(OpenAiMessage::text(&message.role, text)),
            Value::Array(blocks) => {
                let mut content = Vec::new();
                let mut calls = Vec::new();
                for block in blocks {
                    let kind = block
                        .get("type")
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    match kind {
                        "text" => content.push(json!({
                            "type":"text",
                            "text": block.get("text").and_then(Value::as_str).unwrap_or_default()
                        })),
                        "tool_use" => calls.push(json!({
                            "id": block.get("id").and_then(Value::as_str).unwrap_or_default(),
                            "type":"function",
                            "function": {
                                "name": block.get("name").and_then(Value::as_str).unwrap_or_default(),
                                "arguments": arguments_string(block.get("input").cloned().unwrap_or_else(|| json!({})))
                            }
                        })),
                        "tool_result" => messages.push(OpenAiMessage {
                            role: "tool".to_owned(),
                            content: block.get("content").cloned().unwrap_or(Value::Null),
                            tool_call_id: block
                                .get("tool_use_id")
                                .and_then(Value::as_str)
                                .unwrap_or_default()
                                .to_owned(),
                            tool_result_is_error: block
                                .get("is_error")
                                .and_then(Value::as_bool)
                                .unwrap_or(false),
                            ..OpenAiMessage::default()
                        }),
                        "image" => content.push(anthropic_image(block)?),
                        _ => {}
                    }
                }
                if !content.is_empty() || !calls.is_empty() {
                    messages.push(OpenAiMessage {
                        role: message.role,
                        content: Value::Array(content),
                        tool_calls: calls,
                        ..OpenAiMessage::default()
                    });
                }
            }
            _ => return Err("invalid anthropic content"),
        }
    }
    if messages.is_empty() {
        return Err("messages required");
    }
    let parallel_tool_calls = body
        .tool_choice
        .get("disable_parallel_tool_use")
        .and_then(Value::as_bool)
        .map(|disabled| !disabled);
    let tools = body
        .tools
        .into_iter()
        .filter(|tool| !tool.name.trim().is_empty())
        .map(|tool| {
            let mut function = json!({
                "name":tool.name,
                "description":tool.description,
                "parameters":tool.input_schema
            });
            if !tool.annotations.is_null() {
                function["annotations"] = tool.annotations;
            }
            Tool {
                kind: "function".to_owned(),
                function,
            }
        })
        .collect::<Vec<_>>();
    Ok(ChatCompletionRequest {
        model: body.model,
        messages,
        stream: false,
        tools,
        tool_choice: anthropic_tool_choice(&body.tool_choice),
        parallel_tool_calls,
        checkpoint_mode: "full".to_owned(),
        checkpoint_namespace: "anthropic".to_owned(),
        ..ChatCompletionRequest::default()
    })
}

fn anthropic_text_content(value: &Value) -> Result<Value, &'static str> {
    match value {
        Value::String(_) => Ok(value.clone()),
        Value::Array(blocks) => Ok(Value::Array(
            blocks
                .iter()
                .filter(|block| block.get("type").and_then(Value::as_str) == Some("text"))
                .cloned()
                .collect(),
        )),
        _ => Err("invalid anthropic system content"),
    }
}

fn anthropic_image(block: Value) -> Result<Value, &'static str> {
    let source = block
        .get("source")
        .and_then(Value::as_object)
        .ok_or("invalid image source")?;
    if source.get("type").and_then(Value::as_str) == Some("url") {
        return Ok(json!({
            "type":"input_image",
            "image_url":source.get("url").and_then(Value::as_str).unwrap_or_default()
        }));
    }
    let data = source
        .get("data")
        .and_then(Value::as_str)
        .ok_or("image data required")?;
    let media = source
        .get("media_type")
        .and_then(Value::as_str)
        .unwrap_or("application/octet-stream");
    Ok(json!({"type":"input_image","image_url":format!("data:{media};base64,{data}")}))
}

fn anthropic_tool_choice(value: &Value) -> Value {
    match value.get("type").and_then(Value::as_str) {
        Some("any") => Value::String("required".to_owned()),
        Some("none") => Value::String("none".to_owned()),
        Some("tool") => {
            json!({"type":"function","function":{"name":value.get("name").and_then(Value::as_str).unwrap_or_default()}})
        }
        _ => Value::String("auto".to_owned()),
    }
}

fn project_responses(response_id: &str, model: &str, streaming: bool, source: Value) -> Response {
    let message = source
        .pointer("/choices/0/message")
        .cloned()
        .unwrap_or_else(|| json!({}));
    let mut output = Vec::new();
    if let Some(reasoning) = message
        .get("reasoning_content")
        .and_then(Value::as_str)
        .filter(|value| !value.trim().is_empty())
    {
        output.push(responses_reasoning_item(
            &format!("rs_{}", random_id()),
            reasoning,
            "completed",
        ));
    }
    let content = responses_content_blocks(message.get("content").unwrap_or(&Value::Null));
    if !content.is_empty() {
        output.push(json!({
            "type":"message",
            "id":format!("msg_{}", random_id()),
            "role":"assistant",
            "status":"completed",
            "content":content
        }));
    }
    if let Some(calls) = message.get("tool_calls").and_then(Value::as_array) {
        for call in calls {
            let function = call.get("function").cloned().unwrap_or_else(|| json!({}));
            let custom = call.get("type").and_then(Value::as_str) == Some("custom");
            output.push(if custom {
                json!({
                    "type":"custom_tool_call",
                    "id":format!("ctc_{}", random_id()),
                    "call_id":call.get("id").cloned().unwrap_or(Value::Null),
                    "name":function.get("name").cloned().unwrap_or(Value::Null),
                    "input":custom_input(function.get("arguments")),
                    "status":"completed"
                })
            } else {
                json!({
                    "type":"function_call",
                    "id":format!("fc_{}", random_id()),
                    "call_id":call.get("id").cloned().unwrap_or(Value::Null),
                    "name":function.get("name").cloned().unwrap_or(Value::Null),
                    "arguments":function.get("arguments").cloned().unwrap_or(Value::String("{}".to_owned())),
                    "status":"completed"
                })
            });
        }
    }
    let usage = responses_usage(&source);
    let m365 = responses_metadata(&source);
    let response = json!({
        "id":response_id,
        "object":"response",
        "created_at":OffsetDateTime::now_utc().unix_timestamp(),
        "status":"completed",
        "model":model,
        "output":output,
        "usage":usage,
        "m365":m365
    });
    if !streaming {
        return Json(response).into_response();
    }
    let mut events = Vec::new();
    let created = json!({
        "type":"response.created",
        "response":{"id":response_id,"object":"response","status":"in_progress","model":model,"output":[]}
    });
    events.push(("response.created", created));
    for (index, item) in output.iter().enumerate() {
        let kind = item.get("type").and_then(Value::as_str).unwrap_or_default();
        let mut added = item.clone();
        if kind == "reasoning" {
            added = responses_reasoning_item(
                item.get("id").and_then(Value::as_str).unwrap_or_default(),
                "",
                "in_progress",
            );
        } else if kind == "function_call" {
            added["arguments"] = Value::String(String::new());
            added["status"] = Value::String("in_progress".to_owned());
        }
        events.push((
            "response.output_item.added",
            json!({
                "type":"response.output_item.added","output_index":index,"item":added
            }),
        ));
        match kind {
            "reasoning" => {
                if let Some(text) = item
                    .pointer("/summary/0/text")
                    .and_then(Value::as_str)
                    .filter(|value| !value.is_empty())
                {
                    let item_id = item.get("id").cloned().unwrap_or(Value::Null);
                    events.push((
                        "response.reasoning_summary_part.added",
                        json!({"type":"response.reasoning_summary_part.added","output_index":index,"item_id":item_id,"summary_index":0,"part":{"type":"summary_text","text":""}}),
                    ));
                    events.push((
                        "response.reasoning_summary_text.delta",
                        json!({"type":"response.reasoning_summary_text.delta","output_index":index,"item_id":item_id,"summary_index":0,"delta":text}),
                    ));
                    events.push((
                        "response.reasoning_summary_text.done",
                        json!({"type":"response.reasoning_summary_text.done","output_index":index,"item_id":item_id,"summary_index":0,"text":text}),
                    ));
                    events.push((
                        "response.reasoning_summary_part.done",
                        json!({"type":"response.reasoning_summary_part.done","output_index":index,"item_id":item_id,"summary_index":0,"part":{"type":"summary_text","text":text}}),
                    ));
                }
            }
            "message" => {
                if let Some(blocks) = item.get("content").and_then(Value::as_array) {
                    for (content_index, block) in blocks.iter().enumerate() {
                        if block.get("type").and_then(Value::as_str) != Some("output_text") {
                            continue;
                        }
                        events.push((
                            "response.output_text.delta",
                            json!({
                                "type":"response.output_text.delta","output_index":index,"content_index":content_index,
                                "item_id":item.get("id"),"delta":block.get("text").and_then(Value::as_str).unwrap_or_default()
                            }),
                        ));
                    }
                }
            }
            "function_call" => {
                let arguments = item
                    .get("arguments")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                events.push((
                    "response.function_call_arguments.delta",
                    json!({"type":"response.function_call_arguments.delta","output_index":index,"item_id":item.get("id"),"delta":arguments}),
                ));
                events.push((
                    "response.function_call_arguments.done",
                    json!({"type":"response.function_call_arguments.done","output_index":index,"item_id":item.get("id"),"arguments":arguments}),
                ));
            }
            "custom_tool_call" => {
                let input = item
                    .get("input")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                events.push((
                    "response.custom_tool_call_input.delta",
                    json!({"type":"response.custom_tool_call_input.delta","output_index":index,"item_id":item.get("id"),"delta":input}),
                ));
                events.push((
                    "response.custom_tool_call_input.done",
                    json!({"type":"response.custom_tool_call_input.done","output_index":index,"item_id":item.get("id"),"input":input}),
                ));
            }
            _ => {}
        }
        events.push((
            "response.output_item.done",
            json!({
                "type":"response.output_item.done","output_index":index,"item":item
            }),
        ));
    }
    events.push((
        "response.completed",
        json!({"type":"response.completed","response":response}),
    ));
    sse(events)
}

fn responses_content_blocks(content: &Value) -> Vec<Value> {
    fn push_text(output: &mut Vec<Value>, text: &str) {
        if !text.is_empty() {
            output.push(json!({"type":"output_text","text":text,"annotations":[]}));
        }
    }

    let mut output = Vec::new();
    match content {
        Value::String(text) => push_text(&mut output, text),
        Value::Array(blocks) => {
            for block in blocks {
                match block.get("type").and_then(Value::as_str) {
                    Some("text" | "output_text") => {
                        push_text(
                            &mut output,
                            block
                                .get("text")
                                .and_then(Value::as_str)
                                .unwrap_or_default(),
                        );
                    }
                    Some("image_url" | "output_image") => {
                        let direct = block
                            .get("image_url")
                            .and_then(Value::as_str)
                            .or_else(|| block.pointer("/image_url/url").and_then(Value::as_str));
                        if let Some(url) = direct.filter(|url| crate::chathub::is_image_url(url)) {
                            output.push(json!({"type":"output_image","image_url":url.trim()}));
                        }
                    }
                    _ => {}
                }
            }
        }
        _ => {}
    }
    output
}

fn responses_reasoning_item(id: &str, text: &str, status: &str) -> Value {
    let summary = if text.is_empty() {
        Vec::new()
    } else {
        vec![json!({"type":"summary_text","text":text})]
    };
    json!({"type":"reasoning","id":id,"status":status,"summary":summary})
}

fn responses_usage(source: &Value) -> Value {
    let input = usage_value(source, "input_tokens", "prompt_tokens");
    let output = usage_value(source, "output_tokens", "completion_tokens");
    json!({"input_tokens":input,"output_tokens":output,"total_tokens":input + output})
}

fn responses_metadata(source: &Value) -> Value {
    let mut metadata = source
        .get("m365")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    metadata.insert("usage_source".to_owned(), json!("utf16_estimate"));
    metadata.insert("usage_values_are_estimates".to_owned(), json!(true));
    metadata.insert(
        "usage_estimate_scope".to_owned(),
        json!("visible_request_and_completion"),
    );
    Value::Object(metadata)
}

fn usage_value(source: &Value, primary: &str, fallback: &str) -> u64 {
    source
        .get("usage")
        .and_then(|usage| usage.get(primary).or_else(|| usage.get(fallback)))
        .and_then(Value::as_u64)
        .unwrap_or_default()
}

fn project_anthropic(model: &str, streaming: bool, source: Value) -> Response {
    let id = format!("msg_{}", random_id());
    let message = source
        .pointer("/choices/0/message")
        .cloned()
        .unwrap_or_else(|| json!({}));
    let mut content = Vec::new();
    if let Some(text) = message.get("content").and_then(Value::as_str)
        && !text.is_empty()
    {
        content.push(json!({"type":"text","text":text}));
    }
    if let Some(calls) = message.get("tool_calls").and_then(Value::as_array) {
        for call in calls {
            let function = call.get("function").cloned().unwrap_or_else(|| json!({}));
            let input = function
                .get("arguments")
                .and_then(Value::as_str)
                .and_then(|raw| serde_json::from_str::<Value>(raw).ok())
                .unwrap_or_else(|| json!({}));
            content.push(json!({
                "type":"tool_use",
                "id":call.get("id").cloned().unwrap_or(Value::Null),
                "name":function.get("name").cloned().unwrap_or(Value::Null),
                "input":input
            }));
        }
    }
    let stop = if content
        .iter()
        .any(|block| block.get("type").and_then(Value::as_str) == Some("tool_use"))
    {
        "tool_use"
    } else {
        "end_turn"
    };
    let m365 = anthropic_metadata(&source);
    let output = json!({
        "id":id,"type":"message","role":"assistant","model":model,"content":content,
        "stop_reason":stop,"stop_sequence":Value::Null,
        "usage":{"input_tokens":0,"output_tokens":0},
        "m365":m365
    });
    if !streaming {
        return Json(output).into_response();
    }
    let mut events = vec![(
        "message_start",
        json!({
            "type":"message_start","message":{"id":id,"type":"message","role":"assistant","model":model,"content":[],"stop_reason":Value::Null,"usage":{"input_tokens":0,"output_tokens":0},"m365":m365}
        }),
    )];
    for (index, block) in content.iter().enumerate() {
        let start = if block.get("type").and_then(Value::as_str) == Some("tool_use") {
            json!({"type":"tool_use","id":block.get("id"),"name":block.get("name"),"input":{}})
        } else {
            block.clone()
        };
        events.push((
            "content_block_start",
            json!({"type":"content_block_start","index":index,"content_block":start}),
        ));
        let delta = if block.get("type").and_then(Value::as_str) == Some("tool_use") {
            json!({"type":"input_json_delta","partial_json":arguments_string(block.get("input").cloned().unwrap_or_else(|| json!({})))})
        } else {
            json!({"type":"text_delta","text":block.get("text").and_then(Value::as_str).unwrap_or_default()})
        };
        events.push((
            "content_block_delta",
            json!({"type":"content_block_delta","index":index,"delta":delta}),
        ));
        events.push((
            "content_block_stop",
            json!({"type":"content_block_stop","index":index}),
        ));
    }
    events.push(("message_delta", json!({"type":"message_delta","delta":{"stop_reason":stop,"stop_sequence":Value::Null},"usage":{"output_tokens":0}})));
    events.push((
        "message_stop",
        json!({"type":"message_stop","model":model,"m365":m365}),
    ));
    sse(events)
}

fn anthropic_metadata(source: &Value) -> Value {
    let mut metadata = source
        .get("m365")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    metadata.insert("usage_source".to_owned(), json!("unavailable_from_chathub"));
    metadata.insert("usage_values_are_placeholders".to_owned(), json!(true));
    Value::Object(metadata)
}

async fn read<T: for<'de> Deserialize<'de>>(request: Request) -> Result<T, Response> {
    let bytes = to_bytes(request.into_body(), BODY_LIMIT)
        .await
        .map_err(|_| {
            openai_error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "invalid_request_error",
                "request_too_large",
                "request body is too large",
            )
        })?;
    serde_json::from_slice(&bytes).map_err(|_| invalid("bad json"))
}

async fn response_json(response: Response) -> Result<(StatusCode, Value), Response> {
    let status = response.status();
    let bytes = to_bytes(response.into_body(), BODY_LIMIT)
        .await
        .map_err(|_| {
            openai_error(
                StatusCode::BAD_GATEWAY,
                "upstream_error",
                "invalid_inner_response",
                "gateway response is too large",
            )
        })?;
    serde_json::from_slice(&bytes)
        .map(|value| (status, value))
        .map_err(|_| {
            openai_error(
                StatusCode::BAD_GATEWAY,
                "upstream_error",
                "invalid_inner_response",
                "gateway returned invalid JSON",
            )
        })
}

async fn anthropic_error_response(response: Response) -> Response {
    let status = response.status();
    match to_bytes(response.into_body(), BODY_LIMIT).await {
        Ok(body) => anthropic_error_value(
            status,
            serde_json::from_slice(&body).unwrap_or_else(
                |_| json!({"error":{"type":"api_error","message":"gateway returned invalid JSON"}}),
            ),
        ),
        Err(_) => anthropic_error_value(
            StatusCode::BAD_GATEWAY,
            json!({"error":{"type":"api_error","message":"gateway response is too large"}}),
        ),
    }
}

fn anthropic_error_value(status: StatusCode, value: Value) -> Response {
    if value.get("type").and_then(Value::as_str) == Some("error")
        && value.get("error").is_some_and(Value::is_object)
    {
        return (status, Json(value)).into_response();
    }
    let mut error = value
        .get("error")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_else(|| {
            Map::from_iter([
                ("type".to_owned(), json!("api_error")),
                ("message".to_owned(), json!("upstream protocol error")),
            ])
        });
    if !error.get("type").is_some_and(Value::is_string) {
        error.insert("type".to_owned(), json!("api_error"));
    }
    if !error.get("message").is_some_and(Value::is_string) {
        error.insert("message".to_owned(), json!("upstream protocol error"));
    }
    (status, Json(json!({"type":"error","error":error}))).into_response()
}

fn sse(events: Vec<(&'static str, Value)>) -> Response {
    let chunks = events.into_iter().map(|(event, value)| {
        Ok::<_, Infallible>(Bytes::from(format!("event: {event}\ndata: {value}\n\n")))
    });
    let mut response = Body::from_stream(stream::iter(chunks)).into_response();
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/event-stream"),
    );
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
    response
        .headers_mut()
        .insert("x-accel-buffering", HeaderValue::from_static("no"));
    response
}

fn invalid(message: &'static str) -> Response {
    openai_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "invalid_request",
        message,
    )
}

fn api_key_owner(request: &Request) -> String {
    request
        .extensions()
        .get::<ApiKeyOwner>()
        .map(|owner| owner.0.clone())
        .unwrap_or_default()
}

fn arguments_string(value: Value) -> String {
    match value {
        Value::String(raw) if serde_json::from_str::<Value>(&raw).is_ok() => raw,
        value => serde_json::to_string(&value).unwrap_or_else(|_| "{}".to_owned()),
    }
}

fn custom_input(arguments: Option<&Value>) -> String {
    arguments
        .and_then(Value::as_str)
        .and_then(|raw| serde_json::from_str::<Map<String, Value>>(raw).ok())
        .and_then(|object| {
            object
                .get("input")
                .and_then(Value::as_str)
                .map(str::to_owned)
        })
        .unwrap_or_default()
}

fn random_id() -> String {
    let mut bytes = [0_u8; 16];
    rand::rng().fill(&mut bytes);
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    async fn body_json(response: Response) -> Value {
        let body = to_bytes(response.into_body(), BODY_LIMIT).await.unwrap();
        serde_json::from_slice(&body).unwrap()
    }

    #[test]
    fn responses_tool_output_keeps_call_identity() {
        let chat = responses_chat(
            ResponsesRequest {
                model: "gpt-5.6-sol".to_owned(),
                input: json!([{"type":"function_call_output","call_id":"call_1","output":"ok"}]),
                ..ResponsesRequest::default()
            },
            "resp_test",
        )
        .unwrap();
        assert_eq!(chat.messages[0].role, "tool");
        assert_eq!(chat.messages[0].tool_call_id, "call_1");
    }

    #[test]
    fn responses_groups_parallel_calls_and_validates_progress() {
        let chat = responses_chat(
            ResponsesRequest {
                input: json!([
                    {"type":"function_call","call_id":"call_1","name":"first","arguments":"{}"},
                    {"type":"function_call","call_id":"call_2","name":"second","arguments":"{}"},
                    {"type":"function_call_progress","call_id":"call_2","message":"working"},
                    {"type":"function_call_output","call_id":"call_2","output":"ok"}
                ]),
                parallel_tool_calls: Some(true),
                ..ResponsesRequest::default()
            },
            "resp_test",
        )
        .unwrap();
        assert_eq!(chat.messages.len(), 2);
        assert_eq!(chat.messages[0].tool_calls.len(), 2);
        assert_eq!(chat.messages[1].tool_call_id, "call_2");
        assert_eq!(chat.parallel_tool_calls, Some(true));

        assert!(
            responses_chat(
                ResponsesRequest {
                    input: json!([{"type":"function_call_progress","call_id":"call_1"}]),
                    ..ResponsesRequest::default()
                },
                "resp_test",
            )
            .is_err()
        );
    }

    #[tokio::test]
    async fn responses_projection_keeps_usage_media_reasoning_and_tool_events() {
        let source = json!({
            "choices":[{"message":{
                "reasoning_content":"checked",
                "content":[
                    {"type":"output_text","text":"done"},
                    {"type":"output_image","image_url":"https://cdn.example.com/image/result.png"}
                ],
                "tool_calls":[
                    {"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Taipei\"}"}},
                    {"id":"call_2","type":"custom","function":{"name":"exec","arguments":"{\"input\":\"pwd\"}"}}
                ]
            }}],
            "usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15},
            "m365":{"requestId":"request_1"}
        });
        let value = body_json(project_responses(
            "resp_test",
            "gpt-test",
            false,
            source.clone(),
        ))
        .await;
        assert_eq!(value["usage"]["input_tokens"], 12);
        assert_eq!(value["usage"]["output_tokens"], 3);
        assert_eq!(value["m365"]["usage_source"], "utf16_estimate");
        assert_eq!(value["output"][0]["type"], "reasoning");
        assert_eq!(value["output"][1]["content"][1]["type"], "output_image");

        let body = to_bytes(
            project_responses("resp_test", "gpt-test", true, source).into_body(),
            BODY_LIMIT,
        )
        .await
        .unwrap();
        let body = String::from_utf8(body.to_vec()).unwrap();
        for event in [
            "response.reasoning_summary_text.delta",
            "response.function_call_arguments.delta",
            "response.function_call_arguments.done",
            "response.custom_tool_call_input.delta",
            "response.custom_tool_call_input.done",
            "response.completed",
        ] {
            assert!(body.contains(&format!("event: {event}")), "missing {event}");
        }
        assert!(body.contains("\"arguments\":\"\",\"call_id\":\"call_1\""));
    }

    #[test]
    fn anthropic_tool_round_trip_keeps_identity() {
        let chat = anthropic_chat(AnthropicRequest {
            model: "claude-sonnet".to_owned(),
            messages: vec![AnthropicMessage {
                role: "assistant".to_owned(),
                content: json!([{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Taipei"}}]),
            }],
            ..AnthropicRequest::default()
        })
        .unwrap();
        assert_eq!(chat.messages[0].tool_calls[0]["id"], "call_1");
    }

    #[test]
    fn anthropic_preserves_tool_annotations_and_parallel_policy() {
        let chat = anthropic_chat(AnthropicRequest {
            messages: vec![AnthropicMessage {
                role: "user".to_owned(),
                content: json!("hello"),
            }],
            tools: vec![AnthropicTool {
                name: "lookup".to_owned(),
                annotations: json!({"readOnlyHint":true,"destructiveHint":false}),
                ..AnthropicTool::default()
            }],
            tool_choice: json!({"type":"auto","disable_parallel_tool_use":true}),
            ..AnthropicRequest::default()
        })
        .unwrap();
        assert_eq!(chat.parallel_tool_calls, Some(false));
        assert_eq!(chat.tools[0].function["annotations"]["readOnlyHint"], true);
    }

    #[tokio::test]
    async fn anthropic_projection_and_errors_use_current_envelopes() {
        let source = json!({
            "choices":[{"message":{"content":"done"}}],
            "m365":{"requestId":"request_1"}
        });
        let value = body_json(project_anthropic("claude-test", false, source)).await;
        assert_eq!(value["usage"]["input_tokens"], 0);
        assert_eq!(value["m365"]["usage_source"], "unavailable_from_chathub");
        assert_eq!(value["m365"]["usage_values_are_placeholders"], true);

        let error = body_json(anthropic_error_value(
            StatusCode::BAD_REQUEST,
            json!({"error":{"type":"invalid_request_error","code":"bad","message":"bad json"}}),
        ))
        .await;
        assert_eq!(error["type"], "error");
        assert_eq!(error["error"]["type"], "invalid_request_error");
        assert_eq!(error["error"]["code"], "bad");
        assert_eq!(error["error"]["message"], "bad json");

        let response = anthropic_headers(
            anthropic_error_value(
                StatusCode::BAD_REQUEST,
                json!({"error":{"type":"invalid_request_error","message":"bad request"}}),
            ),
            true,
            64,
        );
        assert_eq!(
            response.headers()["x-m365-streaming-semantics"],
            "posthoc-adapter"
        );
        assert_eq!(
            response.headers()["x-m365-ignored-parameters"],
            "max_tokens"
        );
    }
}
