use std::{collections::HashSet, future::Future, pin::Pin, time::Duration};

use base64::{Engine, engine::general_purpose::STANDARD};
use futures_util::{SinkExt, StreamExt};
use rand::Rng;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio_tungstenite::{
    connect_async,
    tungstenite::{
        Error as WebSocketError, Message,
        client::IntoClientRequest,
        http::{HeaderValue, StatusCode},
    },
};
use url::Url;

use crate::{attachment, runtime_settings};

const RECORD_SEPARATOR: char = '\x1e';
const WS_BASE: &str = "wss://substrate.office.com/m365Copilot/Chathub";
const DEFAULT_TONE: &str = "magic";
const STREAMING_MODE: &str = "ConciseWithPadding";
const VARIANTS: &str = "EnableMcpServerWidgets,feature.EnableMcpServerWidgets,feature.EnableImageGenInsufficientTokensThrottled,feature.EnableImageGenSystemCapacityThrottled,feature.EnableLuForChatCIQ,feature.enableChatCIQPlugin,EnableRequestPlugins,feature.EnableSensitivityLabels,EnableUnsupportedUrlDetector,feature.IsCustomEngineCopilotEnabled,feature.bizchatfluxv3,feature.enablechatpages,feature.enableCodeCanvas,feature.turnOnDARecommendation,feature.IsStreamingModeInChatRequestEnabled,IncludeSourceAttributionsConcise,SkipPublishEmptyMessage,feature.EnableDeduplicatingSourceAttributions,feature.IsCitationsReferencesOutputEnabled,feature.enableDeltaStreamingForReferences,feature.enableIncludeReferencesInDeltaResponse,feature.enablereferencesforagents,feature.EnableCodeInterpreterConversion,agt_module_attr_enableReferencesForCodeInterpreter,agt_module_enableCodeInterpreterHallucinatedUrlFilter,Enable3PActionProgressMessages,feature.enableClientWebRtc,feature.EnableMeetingRecapOfSeriesMeetingWithCiq,feature.EnableReferencesListCompleteSignal,feature.StorageMessageSplitDisabled,SingletonEnvOn,cdxenablefccinmainline,EnableComposeWidget,-agt_researcheragent_enableMemoryRead,feature.cwcallowedos,feature.EnableMergingPureDeltas,feature.disabledisallowedmsgs,feature.enableCitationsForSynthesisData,feature.EnableConversationShareApis,feature.EnableConversationShareApisForMsa,feature.enableGenerateGraphicArtOptionsSet,cdximagen,feature.EnableUpdatedUXForConfirmationDialog,feature.EnableContentApiandDocTypeHtmlInRichAnswers,cdxgrounding_api_v2_rich_web_answers_reference_bottom_force,cdxenablerenderforisocomp,feature.EnableClientFileURLSupportForOfficeWebPaidCopilot,feature.EnableDesignEditorImageGrounding,feature.EnableDesignerEditor,feature.EnableSkipRehydrationForSpeCIdImages,feature.EnablePersonalization,rich_responses,feature.EnableBase64DataInMessageAnnotations,feature.EnableSkipEmittingMessageOnFlush,feature.EnableRemoveEmptySourceAttributions,feature.EnableRemoveStreamingMode,feature.OfficeWebToHelix,feature.OfficeDesktopToHelix,feature.M365TeamsHubToHelix,feature.OwaHubToHelix,feature.MonarchHubToHelix,feature.Win32OutlookHubToHelix,feature.MacOutlookHubToHelix,Agt_bizchat_enableGpt5ForHelix";

const OPTIONS: &[&str] = &[
    "search_result_progress_messages_with_search_queries",
    "update_textdoc_response_after_streaming",
    "deepleo_networking_timeout_10minutes_canmore",
    "cwc_flux_image",
    "cwc_code_interpreter",
    "cwc_code_interpreter_amsfix",
    "cwcfluxgptv",
    "flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch",
    "gptvnorm2048",
    "cwc_code_interpreter_citation_fix",
    "code_interpreter_interactive_charts",
    "cwc_code_interpreter_interactive_charts_inline_image",
    "code_interpreter_matplotlib_patching",
    "cwc_fileupload_odb",
    "update_memory_plugin",
    "add_custom_instructions",
    "cwc_flux_v3",
    "flux_v3_progress_messages",
    "enable_batch_token_processing",
    "enable_gg_gpt",
    "cwc_table_context",
    "flux_v3_references",
    "flux_v3_references_entities",
    "flux_v3_references_ci",
    "add_filestore_filetype",
    "cwc_code_interpreter_citation_sourceannotations",
    "cdxcwc_code_interpreter_hallucinated_url_filter",
    "flux_v3_image_gen_enable_dimensions",
    "flux_v3_image_gen_enable_non_watermarked_storage",
    "flux_v3_image_gen_enable_icon_dimensions",
    "flux_v3_image_gen_enable_system_text_with_params",
    "flux_v3_image_gen_enable_designer_dimensions_meta_prompting_in_system_prompts",
    "flux_v3_image_gen_enable_story",
    "rich_responses",
];

const ALLOWED_MESSAGE_TYPES: &[&str] = &[
    "Chat",
    "Suggestion",
    "InternalSearchQuery",
    "Disengaged",
    "InternalLoaderMessage",
    "Progress",
    "GeneratedCode",
    "RenderCardRequest",
    "AdsQuery",
    "SemanticSerp",
    "GenerateContentQuery",
    "GenerateGraphicArt",
    "SearchQuery",
    "ConfirmationCard",
    "AuthError",
    "DeveloperLogs",
    "TriggerPlugin",
    "HintInvocation",
    "MemoryUpdate",
    "EndOfRequest",
    "TriggerConfirmation",
    "ResumeInvokeAction",
    "ResumeUserInputRequest",
    "TriggerUserInputRequest",
    "EscapeHatch",
    "TriggerPluginAuth",
    "ResumePluginAuth",
    "SideBySide",
    "ReferencesListComplete",
    "SwitchRespondingEndpoint",
];

pub fn request_capability_baseline() -> Value {
    json!({
        "streamingMode":STREAMING_MODE,
        "optionsSets":OPTIONS,
        "allowedMessageTypes":ALLOWED_MESSAGE_TYPES,
    })
}

#[derive(Clone, Debug)]
pub struct Account {
    pub access_token: String,
    pub graph_access_token: String,
    pub oid: String,
    pub tid: String,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct Attachment {
    #[serde(rename = "type")]
    pub kind: String,
    pub url: String,
    pub name: String,
    pub mime_type: String,
    pub detail: String,
    #[serde(skip)]
    pub doc_id: String,
    #[serde(skip)]
    pub file_type: String,
    #[serde(skip)]
    pub uploaded_conversation_id: String,
    #[serde(skip)]
    pub transport_name: String,
    #[serde(skip)]
    pub reference_url: String,
    #[serde(skip)]
    pub generated_oversize_text: bool,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct Tool {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Value::is_null")]
    pub function: Value,
}

#[derive(Clone, Debug, Default)]
pub struct ChatRequest {
    pub text: String,
    pub tone: String,
    pub conversation_id: String,
    pub session_id: String,
    pub started: bool,
    pub attachments: Vec<Attachment>,
    pub tools: Vec<Tool>,
    pub tool_choice: Value,
    pub tool_call_limit: usize,
    pub mcp_server_url: String,
    pub disable_built_in_search: bool,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StreamEvent {
    pub kind: String,
    pub text: String,
    pub message_type: String,
    pub content_type: String,
    pub tool_name: String,
    pub arguments: Value,
}

#[derive(Clone, Debug, Default)]
pub struct ChatResult {
    pub text: String,
    pub final_text: String,
    pub streamed_text: String,
    pub text_relation: String,
    pub text_source: String,
    pub conversation_id: String,
    pub session_id: String,
    pub request_id: String,
    pub throttling: Option<Value>,
    pub raw_result: String,
    pub events: Vec<Value>,
    pub images: Vec<String>,
    pub artifacts: Vec<Artifact>,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct Artifact {
    pub reference_id: String,
    pub filename: String,
    pub upstream_url: String,
    pub kind: String,
    pub public_url: String,
}

#[derive(Debug, thiserror::Error)]
pub enum ChatError {
    #[error("missing access token / oid / tid")]
    MissingIdentity,
    #[error("empty prompt")]
    EmptyPrompt,
    #[error("ChatHub rate limited")]
    RateLimited {
        retry_after: Option<String>,
        soft: bool,
    },
    #[error("ChatHub terminal {kind}: {message}")]
    Terminal { kind: String, message: String },
    #[error("ChatHub transport: {0}")]
    Transport(String),
    #[error("attachment transport: {message}")]
    Attachment {
        generated_oversize_text: bool,
        message: String,
    },
    #[error("ChatHub protocol: {0}")]
    Protocol(String),
}

pub trait EventSink {
    fn send(&mut self, event: StreamEvent) -> Result<(), ChatError>;
}

impl<F> EventSink for F
where
    F: FnMut(StreamEvent) -> Result<(), ChatError>,
{
    fn send(&mut self, event: StreamEvent) -> Result<(), ChatError> {
        self(event)
    }
}

pub type ChatFuture<'a> = Pin<Box<dyn Future<Output = Result<ChatResult, ChatError>> + Send + 'a>>;

pub trait ChatHubTransport: Send + Sync {
    fn chat<'a>(
        &'a self,
        account: Account,
        request: ChatRequest,
        events: &'a mut (dyn EventSink + Send),
    ) -> ChatFuture<'a>;
}

pub struct LiveChatHub {
    settings: runtime_settings::Store,
}

impl LiveChatHub {
    pub fn new(settings: runtime_settings::Store) -> Self {
        Self { settings }
    }
}

impl ChatHubTransport for LiveChatHub {
    fn chat<'a>(
        &'a self,
        account: Account,
        request: ChatRequest,
        events: &'a mut (dyn EventSink + Send),
    ) -> ChatFuture<'a> {
        let private_mode = self.settings.current().chat_mode != "normal";
        Box::pin(async move { live_chat(account, request, private_mode, events).await })
    }
}

async fn live_chat(
    account: Account,
    mut request: ChatRequest,
    private_mode: bool,
    events: &mut (dyn EventSink + Send),
) -> Result<ChatResult, ChatError> {
    if account.access_token.is_empty() || account.oid.is_empty() || account.tid.is_empty() {
        return Err(ChatError::MissingIdentity);
    }
    if request.text.trim().is_empty() {
        return Err(ChatError::EmptyPrompt);
    }
    if request.tone.is_empty() {
        request.tone = DEFAULT_TONE.to_owned();
    }
    if request.session_id.is_empty() {
        request.session_id = uuid_v4();
    }
    if request.conversation_id.is_empty() {
        request.conversation_id = uuid_v4();
    }
    attachment::prepare(&account, &request.conversation_id, &mut request.attachments).await?;
    let request_id = uuid_v4();
    let url = websocket_url(&account, &request, &request_id, private_mode)?;
    let payload = chat_payload(&request, &request_id)?;

    let mut socket = None;
    for attempt in 0..2 {
        let mut upgrade = url
            .as_str()
            .into_client_request()
            .map_err(|error| ChatError::Transport(error.to_string()))?;
        upgrade.headers_mut().insert(
            "origin",
            HeaderValue::from_static("https://m365.cloud.microsoft"),
        );
        upgrade.headers_mut().insert(
            "user-agent",
            HeaderValue::from_static(
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
            ),
        );
        match connect_async(upgrade).await {
            Ok((stream, _)) => {
                socket = Some(stream);
                break;
            }
            Err(error) => {
                if let WebSocketError::Http(response) = &error {
                    if response.status() == StatusCode::TOO_MANY_REQUESTS {
                        return Err(ChatError::RateLimited {
                            retry_after: response
                                .headers()
                                .get("retry-after")
                                .and_then(|value| value.to_str().ok())
                                .and_then(normalize_retry_after),
                            soft: false,
                        });
                    }
                    if attempt == 0 && response.status().is_server_error() {
                        tokio::time::sleep(Duration::from_millis(100)).await;
                        continue;
                    }
                } else if attempt == 0 {
                    tokio::time::sleep(Duration::from_millis(100)).await;
                    continue;
                }
                return Err(ChatError::Transport(error.to_string()));
            }
        }
    }
    let mut socket = socket.ok_or_else(|| ChatError::Transport("dial failed".to_owned()))?;
    socket
        .send(Message::Text(
            format!("{{\"protocol\":\"json\",\"version\":1}}{RECORD_SEPARATOR}").into(),
        ))
        .await
        .map_err(transport)?;
    socket
        .next()
        .await
        .ok_or_else(|| ChatError::Protocol("handshake ended early".to_owned()))?
        .map_err(transport)?;
    socket
        .send(Message::Text(payload.into()))
        .await
        .map_err(transport)?;

    let mut collector =
        SignalRCollector::new(request.conversation_id, request.session_id, request_id);
    while let Some(message) = socket.next().await {
        let message = message.map_err(transport)?;
        match message {
            Message::Text(text) => {
                if let Some(result) = collector.ingest(text.as_str(), events)? {
                    return Ok(result);
                }
                if collector.ping_seen {
                    collector.ping_seen = false;
                    socket
                        .send(Message::Text(
                            format!("{{\"type\":6}}{RECORD_SEPARATOR}").into(),
                        ))
                        .await
                        .map_err(transport)?;
                }
            }
            Message::Binary(bytes) => {
                let text = String::from_utf8(bytes.to_vec())
                    .map_err(|_| ChatError::Protocol("non-UTF-8 frame".to_owned()))?;
                if let Some(result) = collector.ingest(&text, events)? {
                    return Ok(result);
                }
            }
            Message::Ping(value) => socket.send(Message::Pong(value)).await.map_err(transport)?,
            Message::Close(_) => {
                return Err(ChatError::Protocol(
                    "socket closed before completion".to_owned(),
                ));
            }
            _ => {}
        }
    }
    Err(ChatError::Protocol(
        "socket ended before completion".to_owned(),
    ))
}

struct SignalRCollector {
    streamed_text: String,
    final_text: String,
    throttling: Option<Value>,
    soft_throttle: bool,
    raw_result: String,
    events: Vec<Value>,
    conversation_id: String,
    session_id: String,
    request_id: String,
    ping_seen: bool,
}

impl SignalRCollector {
    fn new(conversation_id: String, session_id: String, request_id: String) -> Self {
        Self {
            streamed_text: String::new(),
            final_text: String::new(),
            throttling: None,
            soft_throttle: false,
            raw_result: String::new(),
            events: Vec::new(),
            conversation_id,
            session_id,
            request_id,
            ping_seen: false,
        }
    }

    fn ingest(
        &mut self,
        frame: &str,
        sink: &mut (dyn EventSink + Send),
    ) -> Result<Option<ChatResult>, ChatError> {
        for part in frame.split(RECORD_SEPARATOR).map(str::trim) {
            if part.is_empty() {
                continue;
            }
            let value: Value = match serde_json::from_str(part) {
                Ok(value) => value,
                Err(_) => continue,
            };
            self.events.push(value.clone());
            let kind = value
                .get("type")
                .and_then(Value::as_i64)
                .unwrap_or_default();
            if kind == 6 {
                self.ping_seen = true;
                continue;
            }
            if kind == 1 && value.get("target").and_then(Value::as_str) == Some("update") {
                self.update(&value, sink)?;
                continue;
            }
            if kind == 2 {
                if let Some(item) = value.get("item") {
                    if let Some(throttling) = item.get("throttling") {
                        self.throttling = Some(throttling.clone());
                    }
                    self.soft_throttle |= soft_throttle_message(item);
                    if let Some(result) = item.get("result") {
                        self.raw_result = result
                            .get("value")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned();
                        self.final_text = result
                            .get("message")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned();
                    }
                }
                continue;
            }
            if kind == 3 {
                if let Some(error) = provider_error(value.get("error")) {
                    return Err(ChatError::Terminal {
                        kind: "error".to_owned(),
                        message: error,
                    });
                }
                if self.soft_throttle {
                    return Err(ChatError::RateLimited {
                        retry_after: None,
                        soft: true,
                    });
                }
                return Ok(Some(self.result()?));
            }
            if kind == 7 {
                return Err(ChatError::Terminal {
                    kind: "close".to_owned(),
                    message: provider_error(value.get("error")).unwrap_or_default(),
                });
            }
        }
        Ok(None)
    }

    fn update(
        &mut self,
        value: &Value,
        sink: &mut (dyn EventSink + Send),
    ) -> Result<(), ChatError> {
        let Some(arguments) = value.get("arguments").and_then(Value::as_array) else {
            return Ok(());
        };
        for argument in arguments {
            if let Some(throttling) = argument.get("throttling") {
                self.throttling = Some(throttling.clone());
            }
            self.soft_throttle |= soft_throttle_message(argument);
            let messages = argument
                .get("messages")
                .and_then(Value::as_array)
                .map(Vec::as_slice)
                .unwrap_or_default();
            let tool_frame = messages.iter().any(|message| {
                matches!(
                    message.get("messageType").and_then(Value::as_str),
                    Some("Progress")
                ) || matches!(
                    message.get("contentType").and_then(Value::as_str),
                    Some("SearchResults" | "Code" | "ToolCall")
                )
            });
            if !tool_frame && let Some(text) = argument.get("writeAtCursor").and_then(Value::as_str)
            {
                self.emit_text(text, false, sink)?;
            }
            for message in messages {
                let message_type = message
                    .get("messageType")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let content_type = message
                    .get("contentType")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let text = message
                    .get("text")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let generated_artifact = message
                    .get("messageType")
                    .and_then(Value::as_str)
                    .is_some_and(|value| value.eq_ignore_ascii_case("GeneratedCode"))
                    && message
                        .get("contentOrigin")
                        .and_then(Value::as_str)
                        .is_some_and(|value| value.eq_ignore_ascii_case("CodeInterpreter"));
                if generated_artifact || contains_protected_artifact_reference(text) {
                    continue;
                }
                if message.get("author").and_then(Value::as_str) == Some("bot")
                    && message_type.is_empty()
                    && !text.is_empty()
                {
                    self.emit_text(text, true, sink)?;
                } else if !message_type.is_empty() || !content_type.is_empty() {
                    sink.send(StreamEvent {
                        kind: if message_type == "Progress" {
                            "tool.progress".to_owned()
                        } else {
                            "message".to_owned()
                        },
                        text: text.to_owned(),
                        message_type: message_type.to_owned(),
                        content_type: content_type.to_owned(),
                        tool_name: String::new(),
                        arguments: Value::Null,
                    })?;
                }
            }
        }
        Ok(())
    }

    fn emit_text(
        &mut self,
        update: &str,
        cumulative: bool,
        sink: &mut (dyn EventSink + Send),
    ) -> Result<(), ChatError> {
        let (next, delta) = fold_stream_text(&self.streamed_text, update, cumulative);
        if delta.is_empty() {
            return Ok(());
        }
        self.streamed_text = next;
        sink.send(StreamEvent {
            kind: "text".to_owned(),
            text: delta,
            message_type: String::new(),
            content_type: String::new(),
            tool_name: String::new(),
            arguments: Value::Null,
        })
    }

    fn result(&self) -> Result<ChatResult, ChatError> {
        let (text, relation, source) = reconcile_text(&self.final_text, &self.streamed_text);
        let images = image_urls(&self.events, &self.raw_result);
        let artifacts = generated_artifacts(&self.events, &self.raw_result)
            .map_err(|message| ChatError::Protocol(message.to_owned()))?;
        Ok(ChatResult {
            text,
            final_text: self.final_text.clone(),
            streamed_text: self.streamed_text.clone(),
            text_relation: relation,
            text_source: source,
            conversation_id: self.conversation_id.clone(),
            session_id: self.session_id.clone(),
            request_id: self.request_id.clone(),
            throttling: self.throttling.clone(),
            raw_result: self.raw_result.clone(),
            events: self.events.clone(),
            images,
            artifacts,
        })
    }
}

fn generated_artifacts(events: &[Value], raw_result: &str) -> Result<Vec<Artifact>, &'static str> {
    struct Collector {
        values: Vec<Artifact>,
        by_reference: std::collections::HashMap<String, usize>,
        nodes: usize,
    }

    impl Collector {
        fn visit(&mut self, depth: usize) -> Result<(), &'static str> {
            self.nodes += 1;
            if depth > 32 || self.nodes > 64 * 1024 {
                Err("invalid generated artifact metadata")
            } else {
                Ok(())
            }
        }

        fn typed(&mut self, value: &Value, depth: usize) -> Result<(), &'static str> {
            self.visit(depth)?;
            match value {
                Value::Array(values) => {
                    for value in values {
                        self.typed(value, depth + 1)?;
                    }
                }
                Value::Object(values) => {
                    let generated = values
                        .get("messageType")
                        .and_then(Value::as_str)
                        .is_some_and(|value| value.eq_ignore_ascii_case("GeneratedCode"))
                        && values
                            .get("contentOrigin")
                            .and_then(Value::as_str)
                            .is_some_and(|value| value.eq_ignore_ascii_case("CodeInterpreter"));
                    if generated {
                        self.output_files(value, depth + 1)?;
                        if let Some(text) = values.get("text").and_then(Value::as_str)
                            && text.len() <= 1 << 20
                            && let Ok(value) = serde_json::from_str::<Value>(text)
                        {
                            self.output_files(&value, depth + 1)?;
                        }
                    } else {
                        let mut keys = values.keys().collect::<Vec<_>>();
                        keys.sort();
                        for key in keys {
                            self.typed(&values[key], depth + 1)?;
                        }
                    }
                }
                _ => {}
            }
            Ok(())
        }

        fn output_files(&mut self, value: &Value, depth: usize) -> Result<(), &'static str> {
            self.visit(depth)?;
            match value {
                Value::Array(values) => {
                    for value in values {
                        self.output_files(value, depth + 1)?;
                    }
                }
                Value::Object(values) => {
                    let mut keys = values.keys().collect::<Vec<_>>();
                    keys.sort();
                    for key in keys {
                        if key == "outputFiles" {
                            let files = values[key]
                                .as_array()
                                .ok_or("invalid generated artifact metadata")?;
                            for file in files {
                                self.add(
                                    file.as_object()
                                        .ok_or("invalid generated artifact metadata")?,
                                )?;
                            }
                        } else {
                            self.output_files(&values[key], depth + 1)?;
                        }
                    }
                }
                _ => {}
            }
            Ok(())
        }

        fn add(&mut self, value: &serde_json::Map<String, Value>) -> Result<(), &'static str> {
            let reference_id = value
                .get("reference_id")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .trim();
            let file_url = value
                .get("codeResultFileUrl")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .trim();
            let image_url = value
                .get("codeResultImageUrl")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .trim();
            let filename = value
                .get("filename")
                .or_else(|| value.get("fileName"))
                .and_then(Value::as_str)
                .unwrap_or_default();
            if reference_id.is_empty()
                || (file_url.is_empty() == image_url.is_empty())
                || reference_id.len() > 512
                || file_url.len() > 16 << 10
                || image_url.len() > 16 << 10
                || filename.len() > 1024
                || self.values.len() >= 32
            {
                return Err("invalid generated artifact metadata");
            }
            let artifact = Artifact {
                reference_id: reference_id.to_owned(),
                filename: filename.to_owned(),
                upstream_url: if file_url.is_empty() {
                    image_url.to_owned()
                } else {
                    file_url.to_owned()
                },
                kind: if file_url.is_empty() { "image" } else { "file" }.to_owned(),
                public_url: String::new(),
            };
            if let Some(index) = self.by_reference.get(reference_id).copied() {
                let known = &mut self.values[index];
                if known.upstream_url != artifact.upstream_url
                    || (!known.filename.is_empty()
                        && !artifact.filename.is_empty()
                        && known.filename != artifact.filename)
                {
                    return Err("invalid generated artifact metadata");
                }
                if known.filename.is_empty() {
                    known.filename = artifact.filename;
                }
                return Ok(());
            }
            self.by_reference
                .insert(reference_id.to_owned(), self.values.len());
            self.values.push(artifact);
            Ok(())
        }
    }

    let mut collector = Collector {
        values: Vec::new(),
        by_reference: std::collections::HashMap::new(),
        nodes: 0,
    };
    for event in events {
        collector.nodes = 0;
        collector.typed(event, 0)?;
    }
    if let Ok(value) = serde_json::from_str::<Value>(raw_result) {
        collector.nodes = 0;
        collector.output_files(&value, 0)?;
    }
    Ok(collector.values)
}

fn websocket_url(
    account: &Account,
    request: &ChatRequest,
    request_id: &str,
    private_mode: bool,
) -> Result<Url, ChatError> {
    let mut url = Url::parse(&format!("{WS_BASE}/{}@{}", account.oid, account.tid))
        .map_err(|error| ChatError::Transport(error.to_string()))?;
    url.query_pairs_mut()
        .append_pair("chatsessionid", request_id)
        .append_pair("clientrequestid", request_id)
        .append_pair("XRoutingParameterSessionKey", request_id)
        .append_pair("X-SessionId", &request.session_id)
        .append_pair("ConversationId", &request.conversation_id)
        .append_pair("access_token", &account.access_token)
        .append_pair("variants", VARIANTS)
        .append_pair("source", "\"officeweb\"")
        .append_pair("product", "Office")
        .append_pair("agentHost", "Bizchat.FullScreen")
        .append_pair("licenseType", "Starter")
        .append_pair("agent", "web")
        .append_pair("scenario", "OfficeWebIncludedCopilot")
        .append_pair("developerMode", "Basic")
        .append_pair("isEdu", "false");
    if private_mode {
        url.query_pairs_mut().append_pair("disableMemory", "1");
    }
    Ok(url)
}

fn chat_payload(request: &ChatRequest, request_id: &str) -> Result<String, ChatError> {
    let text = tool_protocol_prompt(
        &request.text,
        &request.tools,
        &request.tool_choice,
        request.tool_call_limit,
    );
    let client_info = json!({
        "clientPlatform": "mcmcopilot-web",
        "clientAppName": "Office",
        "clientEntrypoint": "mcmcopilot-officeweb",
        "clientSessionId": request.session_id,
        "ProductCategory": "Chat",
        "clientAppType": "Web",
        "productEntryPoint": "ChatPanel",
        "deviceOS": "macOS",
        "deviceType": "Desktop",
        "clientPlatformVersion": "10.15.7",
    });
    let mut message = json!({
        "author": "user",
        "inputMethod": "Keyboard",
        "text": text,
        "entityAnnotationTypes": ["People", "File", "Event", "Email", "TeamsMessage"],
        "requestId": request_id,
        "locationInfo": {"timeZoneOffset": 8, "timeZone": "Asia/Taipei"},
        "locale": "zh-tw",
        "messageType": "Chat",
        "experienceType": "Default",
        "adaptiveCards": [],
        "clientPreferences": {},
        "clientInfo": client_info.clone(),
        "connectedFederatedConnections": ["dummyId"],
    });
    let annotations = request
        .attachments
        .iter()
        .filter_map(|attachment| {
            if attachment.doc_id.is_empty()
                || attachment.uploaded_conversation_id != request.conversation_id
            {
                return None;
            }
            match attachment.kind.as_str() {
                "file"
                    if !attachment.transport_name.is_empty()
                        && !attachment.reference_url.is_empty() =>
                {
                    Some(json!({
                        "id":attachment.doc_id,
                        "text":attachment.transport_name,
                        "url":attachment.reference_url,
                        "messageAnnotationType":"LocalFile"
                    }))
                }
                "image" => Some(json!({
                    "id":attachment.doc_id,
                    "messageAnnotationMetadata": {
                        "@type":"File",
                        "annotationType":"File",
                        "fileType":attachment.file_type,
                        "fileName":attachment.name,
                    },
                    "messageAnnotationType":"ImageFile"
                })),
                _ => None,
            }
        })
        .collect::<Vec<_>>();
    if !annotations.is_empty() {
        message["messageAnnotations"] = Value::Array(annotations);
    }
    let mut argument = json!({
            "source": "officeweb",
            "clientCorrelationId": request_id,
            "sessionId": request.session_id,
            "optionsSets": OPTIONS,
            "options": {},
            "allowedMessageTypes": ALLOWED_MESSAGE_TYPES,
            "sliceIds": [],
            "threadLevelGptId": {},
            "traceId": request_id,
            "isStartOfSession": request.started,
            "clientInfo": client_info,
            "tone": request.tone,
            "streamingMode": STREAMING_MODE,
            "message": message,
            "disconnectBehavior": "continue",
            "extraExtensionParameters": {},
            "isSbsSupported": true,
            "renderReferencesBehindEOS": true,
            "plugins": plugins(request),
    });
    if !request.tools.is_empty()
        || request
            .tool_choice
            .as_str()
            .is_some_and(|value| value != "auto")
    {
        argument["toolChoice"] = request.tool_choice.clone();
    }
    let chat = json!({
        "arguments": [argument],
        "invocationId": "0",
        "target": "chat",
        "type": 4,
    });
    let metrics = json!({
        "arguments": [{"Timestamps": {
            "ConnectionStart": "", "UserInputStart": "",
            "ConnectionEstablished": "", "UserInputSubmit": ""
        }}],
        "target": "Metrics",
        "type": 1,
    });
    Ok(format!(
        "{}{RECORD_SEPARATOR}{}{RECORD_SEPARATOR}",
        serde_json::to_string(&chat).map_err(|error| ChatError::Protocol(error.to_string()))?,
        serde_json::to_string(&metrics).map_err(|error| ChatError::Protocol(error.to_string()))?
    ))
}

fn plugins(request: &ChatRequest) -> Vec<Value> {
    if request.tool_choice.as_str() == Some("none") {
        return if request.disable_built_in_search {
            Vec::new()
        } else {
            vec![json!({"Id": "BingWebSearch", "Source": "BuiltIn"})]
        };
    }
    let mut plugins = Vec::new();
    if !request.disable_built_in_search {
        plugins.push(json!({"Id": "BingWebSearch", "Source": "BuiltIn"}));
    }
    if !request.mcp_server_url.is_empty() {
        plugins.push(json!({
            "Id": "mcp-gateway", "Source": "MCPServer", "Description": "MCP Gateway tools",
            "Transport": "mcp", "TransportUrl": request.mcp_server_url,
            "TransportProtocol": "https://copilot.microsoft.com/schemas/plugins/local/transport/1.0"
        }));
    }
    for tool in &request.tools {
        let Some(name) = tool.function.get("name").and_then(Value::as_str) else {
            continue;
        };
        plugins.push(json!({
            "Id": name,
            "Source": "Client",
            "Description": tool.function.get("description").and_then(Value::as_str).unwrap_or_default(),
            "Parameters": tool.function.get("parameters").cloned().unwrap_or_else(|| json!({})),
        }));
    }
    plugins
}

fn tool_protocol_prompt(text: &str, tools: &[Tool], choice: &Value, limit: usize) -> String {
    if tools.is_empty() || choice.as_str() == Some("none") {
        return text.to_owned();
    }
    let definitions = tools
        .iter()
        .filter_map(|tool| {
            let name = tool.function.get("name")?.as_str()?.trim();
            if name.is_empty() {
                return None;
            }
            let description = tool
                .function
                .get("description")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let parameters = tool
                .function
                .get("parameters")
                .cloned()
                .unwrap_or_else(|| json!({}));
            Some(format!(
                "{name} — {description}\n```{name}\n{parameters}\n```"
            ))
        })
        .collect::<Vec<_>>();
    if definitions.is_empty() {
        return text.to_owned();
    }
    let limit = limit.max(1);
    format!(
        "You are an execution agent. The tools below are real tools exposed by the caller, not hypothetical M365 plugins.\nCaller execution tools are separate from Microsoft native Bing web search, citations, grounding, and read-only information retrieval. Native Bing and those native read-only capabilities remain allowed when caller tools are registered. When a turn needs both native grounding and a caller tool, use the native capability and still emit the caller decision in the required fenced format.\nWhen the user's request requires caller-side tools, emit at most {limit} fenced tool blocks. Each block's info string must be the exact tool name and its body must be a JSON object of arguments. Multiple blocks are allowed only for mutually independent, clearly read-only operations. Commands, mutations, dependent operations, and uncertain operations must be emitted one at a time. Do not say that the tool is unavailable. Do not wrap calls in XML or explanatory prose. Wait for every emitted tool result before claiming completion.\n\n<tools>\n{}\n</tools>\n\nUser request:\n{text}",
        definitions.join("\n\n")
    )
}

fn fold_stream_text(current: &str, update: &str, cumulative: bool) -> (String, String) {
    if update.is_empty() {
        return (current.to_owned(), String::new());
    }
    if !cumulative {
        return (format!("{current}{update}"), update.to_owned());
    }
    if current.is_empty() {
        return (update.to_owned(), update.to_owned());
    }
    if let Some(delta) = update.strip_prefix(current) {
        return (update.to_owned(), delta.to_owned());
    }
    if current.starts_with(update) {
        return (current.to_owned(), String::new());
    }
    (current.to_owned(), String::new())
}

fn reconcile_text(final_text: &str, streamed_text: &str) -> (String, String, String) {
    match (final_text.is_empty(), streamed_text.is_empty()) {
        (true, true) => (String::new(), "empty".to_owned(), String::new()),
        (true, false) => (
            streamed_text.to_owned(),
            "stream_only".to_owned(),
            "stream".to_owned(),
        ),
        (false, true) => (
            final_text.to_owned(),
            "final_only".to_owned(),
            "final".to_owned(),
        ),
        _ if final_text == streamed_text => (
            final_text.to_owned(),
            "equal".to_owned(),
            "final".to_owned(),
        ),
        _ if streamed_text.starts_with(final_text) => (
            streamed_text.to_owned(),
            "final_prefix_of_stream".to_owned(),
            "stream".to_owned(),
        ),
        _ if final_text.starts_with(streamed_text) => (
            final_text.to_owned(),
            "stream_prefix_of_final".to_owned(),
            "final".to_owned(),
        ),
        _ => (
            final_text.to_owned(),
            "divergent".to_owned(),
            "final".to_owned(),
        ),
    }
}

fn soft_throttle_message(container: &Value) -> bool {
    container
        .get("messages")
        .and_then(Value::as_array)
        .is_some_and(|messages| {
            messages.iter().any(|message| {
                let text = message
                    .get("text")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                message.get("author").and_then(Value::as_str) == Some("bot")
                    && message.get("contentOrigin").and_then(Value::as_str) == Some("BotConnection")
                    && message
                        .get("messageType")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .is_empty()
                    && ((text.contains("暫時無法回應") && text.contains("請稍後再試"))
                        || (text.contains("暂时无法响应") && text.contains("请稍后重试")))
            })
        })
}

fn provider_error(value: Option<&Value>) -> Option<String> {
    let value = value?;
    if value.is_null() {
        return None;
    }
    Some(
        value
            .as_str()
            .map(str::trim)
            .map(str::to_owned)
            .unwrap_or_else(|| value.to_string()),
    )
}

fn image_urls(events: &[Value], raw_result: &str) -> Vec<String> {
    let mut seen = HashSet::new();
    let mut images = Vec::new();
    let mut nodes = 0_usize;
    for event in events {
        collect_image_urls(event, 0, &mut nodes, &mut seen, &mut images);
    }
    if let Ok(value) = serde_json::from_str::<Value>(raw_result) {
        collect_image_urls(&value, 0, &mut nodes, &mut seen, &mut images);
    }
    images
}

fn collect_image_urls(
    value: &Value,
    depth: usize,
    nodes: &mut usize,
    seen: &mut HashSet<String>,
    images: &mut Vec<String>,
) {
    *nodes += 1;
    if depth > 32 || *nodes > 65_536 || images.len() >= 32 {
        return;
    }
    match value {
        Value::Array(values) => {
            for value in values {
                collect_image_urls(value, depth + 1, nodes, seen, images);
            }
        }
        Value::Object(object) => {
            let artifact_message = object
                .get("messageType")
                .and_then(Value::as_str)
                .is_some_and(|kind| kind.eq_ignore_ascii_case("GeneratedCode"))
                && object
                    .get("contentOrigin")
                    .and_then(Value::as_str)
                    .is_some_and(|origin| origin.eq_ignore_ascii_case("CodeInterpreter"));
            if artifact_message {
                return;
            }
            for (key, child) in object {
                if matches!(
                    key.to_ascii_lowercase().as_str(),
                    "outputfiles" | "coderesultfileurl" | "coderesultimageurl"
                ) {
                    continue;
                }
                let candidate_field = matches!(
                    key.to_ascii_lowercase().as_str(),
                    "url" | "imageurl" | "thumbnailurl" | "downloadurl" | "src" | "value" | "data"
                );
                if candidate_field
                    && let Some(candidate) = child.as_str()
                    && is_image_url(candidate)
                    && !contains_protected_artifact_reference(candidate)
                    && seen.insert(candidate.to_owned())
                {
                    images.push(candidate.to_owned());
                } else {
                    collect_image_urls(child, depth + 1, nodes, seen, images);
                }
            }
        }
        _ => {}
    }
}

pub(crate) fn is_image_url(raw: &str) -> bool {
    let raw = raw.trim();
    if let Some(encoded) = raw
        .strip_prefix("data:image/")
        .and_then(|value| value.split_once(',').map(|(_, encoded)| encoded))
    {
        return !encoded.is_empty() && STANDARD.decode(encoded).is_ok();
    }
    let Ok(url) = Url::parse(raw) else {
        return false;
    };
    if url.scheme() != "https" || url.host_str().is_none() {
        return false;
    }
    let path = url.path().to_ascii_lowercase();
    path.contains("image")
        || [".png", ".jpg", ".jpeg", ".webp", ".gif"]
            .iter()
            .any(|suffix| path.ends_with(suffix))
}

pub(crate) fn contains_protected_artifact_reference(raw: &str) -> bool {
    let raw = raw.to_ascii_lowercase();
    raw.contains("coderesultfileurl")
        || raw.contains("coderesultimageurl")
        || raw.contains("asyncgw.teams.microsoft.com")
        || raw.contains("blob:")
}

pub(crate) fn semantic_events(events: &[Value]) -> Vec<Value> {
    let mut projected = Vec::new();
    for event in events {
        if event.get("target").and_then(Value::as_str) != Some("update") {
            continue;
        }
        let Some(arguments) = event.get("arguments").and_then(Value::as_array) else {
            continue;
        };
        for argument in arguments {
            let Some(messages) = argument.get("messages").and_then(Value::as_array) else {
                continue;
            };
            for message in messages {
                let message_type = message
                    .get("messageType")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let content_type = message
                    .get("contentType")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let content_origin = message
                    .get("contentOrigin")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let text = message
                    .get("text")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                let queries = message
                    .get("searchQueries")
                    .and_then(Value::as_array)
                    .map(|values| {
                        values
                            .iter()
                            .filter_map(Value::as_str)
                            .map(str::to_owned)
                            .collect::<Vec<_>>()
                    })
                    .unwrap_or_default();
                let generated_artifact = message_type.eq_ignore_ascii_case("GeneratedCode")
                    && content_origin.eq_ignore_ascii_case("CodeInterpreter");
                if generated_artifact
                    || contains_protected_artifact_reference(text)
                    || queries
                        .iter()
                        .any(|query| contains_protected_artifact_reference(query))
                {
                    continue;
                }
                let add_to_chain_of_thought = message
                    .get("addToChainOfThought")
                    .and_then(Value::as_bool)
                    .unwrap_or(false);
                let kind = if message_type == "Progress"
                    && !text.trim().is_empty()
                    && (content_origin == "ChainOfThoughtSummary" || add_to_chain_of_thought)
                {
                    "reasoning.summary"
                } else if content_type == "SearchResults" {
                    "search.progress"
                } else if content_type == "Code" {
                    "code.progress"
                } else if message_type == "Progress" {
                    "tool.progress"
                } else {
                    "message"
                };
                let mut value = serde_json::Map::from_iter([(
                    "kind".to_owned(),
                    Value::String(kind.to_owned()),
                )]);
                for (key, field) in [
                    ("contentType", content_type),
                    ("messageType", message_type),
                    ("contentOrigin", content_origin),
                    ("text", text),
                ] {
                    if !field.is_empty() {
                        value.insert(key.to_owned(), Value::String(field.to_owned()));
                    }
                }
                if add_to_chain_of_thought {
                    value.insert("addToChainOfThought".to_owned(), Value::Bool(true));
                }
                if !queries.is_empty() {
                    value.insert(
                        "queries".to_owned(),
                        Value::Array(queries.into_iter().map(Value::String).collect()),
                    );
                }
                projected.push(Value::Object(value));
            }
        }
    }
    projected
}

fn normalize_retry_after(raw: &str) -> Option<String> {
    let raw = raw.trim();
    if raw.parse::<u64>().is_ok() {
        return Some(raw.to_owned());
    }
    httpdate::parse_http_date(raw)
        .ok()
        .map(httpdate::fmt_http_date)
}

fn transport(error: WebSocketError) -> ChatError {
    ChatError::Transport(error.to_string())
}

fn uuid_v4() -> String {
    let mut bytes = [0_u8; 16];
    rand::rng().fill(&mut bytes);
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    format!(
        "{:02x}{:02x}{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}{:02x}{:02x}{:02x}{:02x}",
        bytes[0],
        bytes[1],
        bytes[2],
        bytes[3],
        bytes[4],
        bytes[5],
        bytes[6],
        bytes[7],
        bytes[8],
        bytes[9],
        bytes[10],
        bytes[11],
        bytes[12],
        bytes[13],
        bytes[14],
        bytes[15]
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_artifacts_require_structured_code_interpreter_metadata() {
        let protected =
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.csv";
        let structured = json!({
            "type": 1,
            "target": "update",
            "arguments": [{"messages": [{
                "messageType": "GeneratedCode",
                "contentOrigin": "CodeInterpreter",
                "text": format!(r#"{{"outputFiles":[{{"reference_id":"turn1file1","codeResultFileUrl":"{protected}","filename":"report.csv"}}]}}"#)
            }]}]
        });
        let artifacts = generated_artifacts(&[structured], "").unwrap();
        assert_eq!(artifacts.len(), 1);
        assert_eq!(artifacts[0].reference_id, "turn1file1");
        assert_eq!(artifacts[0].filename, "report.csv");
        assert_eq!(artifacts[0].upstream_url, protected);

        let prose = json!({"text": format!("codeResultFileUrl: {protected}")});
        assert!(generated_artifacts(&[prose], "").unwrap().is_empty());
    }

    #[test]
    fn protected_artifact_metadata_never_becomes_a_stream_event() {
        let protected =
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.csv";
        let frame = json!({
            "type": 1,
            "target": "update",
            "arguments": [{"messages": [
                {"author":"bot","text":"分析完成"},
                {
                    "messageType": "GeneratedCode",
                    "contentOrigin": "CodeInterpreter",
                    "text": format!(r#"{{"outputFiles":[{{"reference_id":"turn1file1","codeResultFileUrl":"{protected}","filename":"report.csv"}}]}}"#)
                }
            ]}]
        });
        let mut collector = SignalRCollector::new(
            "conversation".to_owned(),
            "session".to_owned(),
            "request".to_owned(),
        );
        let mut emitted = Vec::new();
        collector
            .ingest(&format!("{}{}", frame, RECORD_SEPARATOR), &mut |event| {
                emitted.push(event);
                Ok(())
            })
            .unwrap();
        assert!(emitted.iter().any(|event| event.text == "分析完成"));
        assert!(emitted.iter().all(|event| {
            !event.text.contains("codeResultFileUrl") && !event.text.contains(protected)
        }));
    }

    #[test]
    fn semantic_events_omit_artifacts_and_keep_safe_progress() {
        let protected =
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.csv";
        let frame = json!({
            "type": 1,
            "target": "update",
            "arguments": [{"messages": [
                {
                    "messageType": "Progress",
                    "contentType": "SearchResults",
                    "text": "Found one source",
                    "searchQueries": ["safe query"]
                },
                {
                    "messageType": "GeneratedCode",
                    "contentOrigin": "CodeInterpreter",
                    "text": format!(r#"{{"codeResultFileUrl":"{protected}"}}"#)
                }
            ]}]
        });

        let projected = semantic_events(&[frame]);
        assert_eq!(projected.len(), 1);
        assert_eq!(projected[0]["kind"], "search.progress");
        let encoded = serde_json::to_string(&projected).unwrap();
        assert!(!contains_protected_artifact_reference(&encoded));
    }

    #[test]
    fn cumulative_stream_frames_do_not_duplicate_text() {
        assert_eq!(
            fold_stream_text("Hello", "Hello world", true),
            ("Hello world".to_owned(), " world".to_owned())
        );
        assert_eq!(
            fold_stream_text("Hello world", "Hello", true),
            ("Hello world".to_owned(), String::new())
        );
    }

    #[test]
    fn ordinary_throttling_metadata_is_not_a_soft_throttle() {
        let mut collector = SignalRCollector::new("c".into(), "s".into(), "r".into());
        let mut deltas = Vec::new();
        let mut sink = |event: StreamEvent| {
            deltas.push(event);
            Ok(())
        };
        let frame = concat!(
            r#"{"type":2,"item":{"throttling":{"remaining":1},"result":{"message":"OK"}}}"#,
            "\u{1e}",
            r#"{"type":3}"#,
            "\u{1e}"
        );
        let result = collector.ingest(frame, &mut sink).unwrap().unwrap();
        assert_eq!(result.text, "OK");
        assert!(result.throttling.is_some());
    }

    #[test]
    fn recognized_provider_notice_is_a_soft_throttle() {
        let mut collector = SignalRCollector::new("c".into(), "s".into(), "r".into());
        let mut sink = |_: StreamEvent| Ok(());
        let frame = concat!(
            r#"{"type":1,"target":"update","arguments":[{"messages":[{"author":"bot","contentOrigin":"BotConnection","messageType":"","text":"暫時無法回應，請稍後再試"}]}]}"#,
            "\u{1e}",
            r#"{"type":3}"#,
            "\u{1e}"
        );
        assert!(matches!(
            collector.ingest(frame, &mut sink),
            Err(ChatError::RateLimited { soft: true, .. })
        ));
    }

    #[test]
    fn payload_keeps_private_mode_and_current_capabilities() {
        let request = ChatRequest {
            text: "hello".to_owned(),
            tone: DEFAULT_TONE.to_owned(),
            conversation_id: "conversation".to_owned(),
            session_id: "session".to_owned(),
            started: false,
            ..ChatRequest::default()
        };
        let account = Account {
            access_token: "secret".to_owned(),
            graph_access_token: String::new(),
            oid: "oid".to_owned(),
            tid: "tid".to_owned(),
        };
        let url = websocket_url(&account, &request, "request", true).unwrap();
        let query = url
            .query_pairs()
            .into_owned()
            .collect::<std::collections::HashMap<_, _>>();
        assert_eq!(query["chatsessionid"], "request");
        assert_eq!(query["clientrequestid"], "request");
        assert_eq!(query["XRoutingParameterSessionKey"], "request");
        assert_eq!(query["developerMode"], "Basic");
        assert_eq!(query["isEdu"], "false");
        assert_eq!(query["disableMemory"], "1");
        let normal_url = websocket_url(&account, &request, "request", false).unwrap();
        assert!(
            !normal_url
                .query_pairs()
                .any(|(key, _)| key == "disableMemory")
        );
        let payload = chat_payload(&request, "request").unwrap();
        let chat: Value =
            serde_json::from_str(payload.split(RECORD_SEPARATOR).next().unwrap()).unwrap();
        let argument = &chat["arguments"][0];
        assert_eq!(argument["clientCorrelationId"], "request");
        assert_eq!(argument["traceId"], "request");
        assert_eq!(argument["message"]["requestId"], "request");
        assert_eq!(argument["isStartOfSession"], false);
        assert_eq!(argument["message"]["locale"], "zh-tw");
        assert_eq!(
            argument["message"]["locationInfo"]["timeZone"],
            "Asia/Taipei"
        );
        assert_eq!(argument["clientInfo"], argument["message"]["clientInfo"]);
        assert_eq!(argument["clientInfo"]["deviceOS"], "macOS");
        assert_eq!(argument["clientInfo"]["clientPlatformVersion"], "10.15.7");
        assert_eq!(argument["disconnectBehavior"], "continue");
        assert_eq!(
            argument["message"]["connectedFederatedConnections"],
            json!(["dummyId"])
        );
        assert!(argument.get("conversationId").is_none());
        assert!(argument.get("productThreadType").is_none());
        assert!(argument.get("toolChoice").is_none());
        assert_eq!(argument["isSbsSupported"], true);
        assert_eq!(argument["renderReferencesBehindEOS"], true);
        assert!(
            argument["optionsSets"]
                .as_array()
                .unwrap()
                .iter()
                .any(|option| option == "add_filestore_filetype")
        );
        assert!(
            VARIANTS
                .split(',')
                .any(|variant| variant == "feature.EnableCodeInterpreterConversion")
        );
        assert!(payload.contains(STREAMING_MODE));
        assert!(payload.contains("BingWebSearch"));
    }

    #[test]
    fn payload_uses_ready_annotations_without_leaking_attachment_sources() {
        let request = ChatRequest {
            text: "read".to_owned(),
            tone: DEFAULT_TONE.to_owned(),
            conversation_id: "conversation".to_owned(),
            session_id: "session".to_owned(),
            attachments: vec![Attachment {
                kind: "file".to_owned(),
                url: "data:text/plain;base64,c2VjcmV0".to_owned(),
                name: "report.txt".to_owned(),
                doc_id: "SPO_ready".to_owned(),
                transport_name: "report-random.txt".to_owned(),
                reference_url: "https://tenant.sharepoint.com/report".to_owned(),
                uploaded_conversation_id: "conversation".to_owned(),
                ..Attachment::default()
            }],
            ..ChatRequest::default()
        };
        let payload = chat_payload(&request, "request").unwrap();
        assert!(payload.contains("LocalFile"));
        assert!(payload.contains("SPO_ready"));
        assert!(!payload.contains("c2VjcmV0"));
    }
}
