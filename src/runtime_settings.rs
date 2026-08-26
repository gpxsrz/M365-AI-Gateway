use std::{
    collections::{BTreeMap, HashSet},
    path::{Path, PathBuf},
    sync::{Arc, RwLock},
};

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{config::Config, error::GatewayError, private_file};

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ModelMapping {
    pub public_model: String,
    pub upstream_tone: String,
    pub display_name: String,
    pub default_reasoning_level: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(default, rename_all = "camelCase")]
pub struct RuntimeSettings {
    pub chat_mode: String,
    pub hermes_compatibility_enabled: bool,
    pub memory_compatibility_enabled: bool,
    pub interactive_max_concurrent: usize,
    pub interactive_queue_timeout_seconds: u64,
    pub memory_max_concurrent: usize,
    pub memory_queue_timeout_seconds: u64,
    pub interactive_priority_holdoff_seconds: u64,
    pub memory_backoff_initial_seconds: u64,
    pub memory_backoff_max_seconds: u64,
    pub text_input_limit_utf16: usize,
    pub max_tool_calls_per_turn: usize,
    pub max_tool_rounds: usize,
    pub hermes_max_tool_rounds: usize,
    pub context_window: usize,
    pub max_output_tokens: usize,
    pub chat_timeout_seconds: u64,
    pub image_timeout_seconds: u64,
    pub log_level: String,
    pub debug_log_path: String,
    pub listen_address: String,
    pub config_path: String,
    pub token_cache_path: String,
    pub session_cache_path: String,
    pub outbound_proxy: String,
    pub client_id: String,
    pub authority: String,
    pub redirect_uri: String,
    pub scope: String,
    pub model_mappings: Vec<ModelMapping>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub optional_model_capabilities: Vec<Value>,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub web_request_capability_evidence: Value,
    pub tool_planning_mode: String,
}

impl Default for RuntimeSettings {
    fn default() -> Self {
        Self {
            chat_mode: "private".to_owned(),
            hermes_compatibility_enabled: true,
            memory_compatibility_enabled: false,
            interactive_max_concurrent: 2,
            interactive_queue_timeout_seconds: 120,
            memory_max_concurrent: 2,
            memory_queue_timeout_seconds: 120,
            interactive_priority_holdoff_seconds: 30,
            memory_backoff_initial_seconds: 5,
            memory_backoff_max_seconds: 60,
            text_input_limit_utf16: 128_000,
            max_tool_calls_per_turn: 2,
            max_tool_rounds: 16,
            hermes_max_tool_rounds: 128,
            context_window: 128_000,
            max_output_tokens: 16_384,
            chat_timeout_seconds: 120,
            image_timeout_seconds: 150,
            log_level: "info".to_owned(),
            debug_log_path: String::new(),
            listen_address: String::new(),
            config_path: String::new(),
            token_cache_path: String::new(),
            session_cache_path: String::new(),
            outbound_proxy: String::new(),
            client_id: String::new(),
            authority: String::new(),
            redirect_uri: String::new(),
            scope: String::new(),
            model_mappings: vec![
                mapping("gpt-5.6-sol", "Gpt_5_6_Reasoning", "low"),
                mapping("gpt-5.6-terra", "Gpt_5_6_Reasoning", "medium"),
                mapping("gpt-5.6-luna", "Gpt_5_6_Reasoning", "medium"),
            ],
            optional_model_capabilities: Vec::new(),
            web_request_capability_evidence: Value::Null,
            tool_planning_mode: "native".to_owned(),
        }
    }
}

fn mapping(id: &str, tone: &str, effort: &str) -> ModelMapping {
    ModelMapping {
        public_model: id.to_owned(),
        upstream_tone: tone.to_owned(),
        display_name: id.to_owned(),
        default_reasoning_level: effort.to_owned(),
    }
}

#[derive(Clone)]
pub struct Store {
    path: PathBuf,
    state: Arc<RwLock<State>>,
}

struct State {
    settings: RuntimeSettings,
    persisted: bool,
}

impl Store {
    pub fn open(data_dir: &Path, config: &Config) -> Result<Self, GatewayError> {
        let path = data_dir.join("settings.json");
        let persisted = private_file::read_json::<RuntimeSettings>(&path)?;
        let mut settings = persisted.clone().unwrap_or_default();
        if persisted.is_none() {
            settings.text_input_limit_utf16 = config.text_input_limit_utf16;
            settings.max_tool_calls_per_turn = config.max_tool_calls_per_turn;
            settings.max_tool_rounds = config.max_tool_rounds;
            settings.hermes_max_tool_rounds = config.hermes_max_tool_rounds;
            settings.chat_timeout_seconds = config.chat_timeout.as_secs();
            settings.image_timeout_seconds = config.image_timeout.as_secs();
        }
        validate(&settings).map_err(GatewayError::Configuration)?;
        Ok(Self {
            path,
            state: Arc::new(RwLock::new(State {
                settings,
                persisted: persisted.is_some(),
            })),
        })
    }

    pub fn current(&self) -> RuntimeSettings {
        self.state
            .read()
            .expect("runtime settings poisoned")
            .settings
            .clone()
    }

    pub fn save(&self, settings: RuntimeSettings) -> Result<(), GatewayError> {
        validate(&settings).map_err(GatewayError::InvalidRequest)?;
        private_file::write_json(&self.path, &settings)?;
        let mut state = self.state.write().expect("runtime settings poisoned");
        state.settings = settings;
        state.persisted = true;
        Ok(())
    }

    pub fn setting_status(&self) -> BTreeMap<String, Value> {
        let state = self.state.read().expect("runtime settings poisoned");
        let source = if state.persisted { "file" } else { "default" };
        let value = serde_json::to_value(&state.settings).unwrap_or(Value::Null);
        let mut status = value
            .as_object()
            .into_iter()
            .flat_map(|object| object.iter())
            .map(|(name, configured)| {
                (
                    name.clone(),
                    json!({
                        "configured": configured,
                        "effective": configured,
                        "source": source,
                        "locked": false,
                        "restartRequired": restart_required(name),
                    }),
                )
            })
            .collect::<BTreeMap<_, _>>();
        for (field, environment, effective) in [
            (
                "maxToolCallsPerTurn",
                "M365_MAX_TOOL_CALLS_PER_TURN",
                configured_tool_call_limit(&state.settings),
            ),
            (
                "maxToolRounds",
                "M365_MAX_TOOL_ROUNDS",
                configured_max_tool_rounds(&state.settings),
            ),
            (
                "hermesMaxToolRounds",
                "M365_HERMES_MAX_TOOL_ROUNDS",
                configured_hermes_max_tool_rounds(&state.settings),
            ),
        ] {
            if std::env::var_os(environment).is_some() {
                let configured = serde_json::to_value(&state.settings)
                    .ok()
                    .and_then(|value| value.get(field).cloned())
                    .unwrap_or(Value::Null);
                status.insert(
                    field.to_owned(),
                    json!({
                        "configured":configured,
                        "effective":effective,
                        "source":"env",
                        "locked":true,
                        "restartRequired":false,
                    }),
                );
            }
        }
        status
    }
}

pub fn configured_tool_call_limit(settings: &RuntimeSettings) -> usize {
    direct_override(
        "M365_MAX_TOOL_CALLS_PER_TURN",
        settings.max_tool_calls_per_turn,
        1,
        1,
        64,
    )
}

pub fn configured_max_tool_rounds(settings: &RuntimeSettings) -> usize {
    direct_override("M365_MAX_TOOL_ROUNDS", settings.max_tool_rounds, 16, 1, 512)
}

pub fn configured_hermes_max_tool_rounds(settings: &RuntimeSettings) -> usize {
    direct_override(
        "M365_HERMES_MAX_TOOL_ROUNDS",
        settings.hermes_max_tool_rounds,
        128,
        1,
        512,
    )
}

fn direct_override(
    name: &str,
    configured: usize,
    invalid_default: usize,
    minimum: usize,
    maximum: usize,
) -> usize {
    let value = std::env::var(name).ok();
    direct_override_value(
        value.as_deref(),
        configured,
        invalid_default,
        minimum,
        maximum,
    )
}

fn direct_override_value(
    value: Option<&str>,
    configured: usize,
    invalid_default: usize,
    minimum: usize,
    maximum: usize,
) -> usize {
    match value {
        Some(value) => value
            .trim()
            .parse::<usize>()
            .ok()
            .filter(|value| (minimum..=maximum).contains(value))
            .unwrap_or(invalid_default),
        None => configured.clamp(minimum, maximum),
    }
}

pub fn validate(settings: &RuntimeSettings) -> Result<(), String> {
    if !matches!(settings.chat_mode.as_str(), "private" | "normal") {
        return Err("聊天模式必須為 private 或 normal".to_owned());
    }
    bounded(
        settings.interactive_max_concurrent,
        1,
        16,
        "互動流量同時請求上限",
    )?;
    bounded(
        settings.memory_max_concurrent,
        1,
        16,
        "背景 Memory 同時請求上限",
    )?;
    bounded(
        settings.interactive_queue_timeout_seconds as usize,
        1,
        600,
        "互動流量排隊逾時",
    )?;
    bounded(
        settings.memory_queue_timeout_seconds as usize,
        1,
        600,
        "背景 Memory 排隊逾時",
    )?;
    bounded(
        settings.text_input_limit_utf16,
        1,
        4_000_000,
        "文字輸入上限",
    )?;
    bounded(settings.max_tool_calls_per_turn, 1, 64, "每輪工具呼叫數")?;
    bounded(settings.max_tool_rounds, 1, 512, "一般工具輪次")?;
    bounded(settings.hermes_max_tool_rounds, 1, 512, "Hermes 工具輪次")?;
    bounded(settings.chat_timeout_seconds as usize, 5, 3_600, "聊天逾時")?;
    bounded(
        settings.image_timeout_seconds as usize,
        5,
        3_600,
        "圖片逾時",
    )?;
    if settings.context_window < 1_024
        || settings.max_output_tokens == 0
        || settings.max_output_tokens >= settings.context_window
    {
        return Err("最大輸出必須大於 0、小於內容視窗；內容視窗不得小於 1024".to_owned());
    }
    if !matches!(
        settings.log_level.as_str(),
        "silent" | "error" | "warn" | "info" | "debug"
    ) {
        return Err("日誌等級必須為 silent、error、warn、info 或 debug".to_owned());
    }
    if !matches!(settings.tool_planning_mode.as_str(), "router" | "native") {
        return Err("工具規劃模式必須為 router 或 native".to_owned());
    }
    if settings.memory_backoff_initial_seconds == 0
        || settings.memory_backoff_initial_seconds > 300
        || settings.memory_backoff_max_seconds < settings.memory_backoff_initial_seconds
        || settings.memory_backoff_max_seconds > 3_600
        || settings.interactive_priority_holdoff_seconds > 300
    {
        return Err("Memory 退避或互動優先保留時間超出安全範圍".to_owned());
    }
    crate::catalog::validate_optional_capabilities(&settings.optional_model_capabilities)?;
    let optional_models = settings
        .optional_model_capabilities
        .iter()
        .filter_map(|value| value.get("publicModel").and_then(Value::as_str))
        .map(|value| value.trim().to_ascii_lowercase())
        .collect::<HashSet<_>>();
    let optional_tones = settings
        .optional_model_capabilities
        .iter()
        .filter_map(|value| value.get("upstreamTone").and_then(Value::as_str))
        .map(|value| value.trim().to_ascii_lowercase())
        .collect::<HashSet<_>>();
    let mut models = HashSet::new();
    for mapping in &settings.model_mappings {
        let id = mapping.public_model.trim();
        let tone = mapping.upstream_tone.trim();
        if id.is_empty()
            || id.len() > 128
            || !id
                .bytes()
                .all(|byte| byte.is_ascii_alphanumeric() || b"._-".contains(&byte))
            || !models.insert(id.to_ascii_lowercase())
            || optional_models.contains(&id.to_ascii_lowercase())
            || optional_tones.contains(&tone.to_ascii_lowercase())
            || !crate::catalog::valid_mapping_tone(settings, tone)
            || mapping.display_name.trim().is_empty()
            || !valid_effort(&mapping.default_reasoning_level)
        {
            return Err("模型對應格式無效或公開模型 ID 重複".to_owned());
        }
    }
    validate_web_request_capability_evidence(settings)?;
    Ok(())
}

fn validate_web_request_capability_evidence(settings: &RuntimeSettings) -> Result<(), String> {
    let evidence = &settings.web_request_capability_evidence;
    if evidence.is_null() || evidence.as_object().is_some_and(serde_json::Map::is_empty) {
        return Ok(());
    }
    let object = evidence
        .as_object()
        .ok_or_else(|| "Web request capability evidence 格式無效".to_owned())?;
    let field = |name| {
        object
            .get(name)
            .and_then(Value::as_str)
            .map(str::trim)
            .unwrap_or_default()
    };
    if field("schema") != "m365-web-request-capability-evidence/v1" {
        return Err("Web request capability evidence schema 不支援".to_owned());
    }
    if OffsetDateTime::parse(field("capturedAt"), &Rfc3339).is_err() {
        return Err("Web request capability evidence capturedAt 無效".to_owned());
    }
    if !crate::catalog::observed_tone(settings, field("tone")) {
        return Err("Web request capability evidence tone 尚未被接受".to_owned());
    }
    if !valid_capability_name(field("streamingMode")) || !valid_digest(field("observationSha256")) {
        return Err("Web request capability evidence identity 無效".to_owned());
    }
    if object.get("temporaryChat").and_then(Value::as_bool) != Some(true)
        || object.get("disableMemoryObserved").and_then(Value::as_bool) != Some(true)
    {
        return Err(
            "Web request capability evidence 缺少 Temporary Chat / disableMemory 驗證".to_owned(),
        );
    }
    validate_capability_names(object.get("optionsSets"), "optionsSets")?;
    validate_capability_names(object.get("allowedMessageTypes"), "allowedMessageTypes")
}

fn validate_capability_names(value: Option<&Value>, label: &str) -> Result<(), String> {
    let values = value
        .and_then(Value::as_array)
        .ok_or_else(|| format!("Web request capability evidence {label} 格式無效"))?;
    if values.is_empty() || values.len() > 256 {
        return Err(format!(
            "Web request capability evidence {label} 數量必須為 1-256"
        ));
    }
    let mut seen = HashSet::new();
    for value in values {
        let value = value.as_str().map(str::trim).unwrap_or_default();
        if !valid_capability_name(value) || !seen.insert(value) {
            return Err(format!(
                "Web request capability evidence {label} 包含無效或重複名稱"
            ));
        }
    }
    Ok(())
}

fn valid_capability_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._:-".contains(&byte))
}

fn valid_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

pub fn request_capability_drift(settings: &RuntimeSettings) -> Value {
    let baseline = crate::chathub::request_capability_baseline();
    let evidence = settings.web_request_capability_evidence.as_object();
    let Some(evidence) = evidence.filter(|value| !value.is_empty()) else {
        return json!({
            "observed":false,
            "projectionPolicy":"observe_only",
            "sidecarStreamingMode":baseline["streamingMode"],
        });
    };
    let drift = |name: &str| {
        let observed = string_set(evidence.get(name));
        let sidecar = string_set(baseline.get(name));
        let mut web_only = observed.difference(&sidecar).cloned().collect::<Vec<_>>();
        let mut sidecar_only = sidecar.difference(&observed).cloned().collect::<Vec<_>>();
        let mut common = observed.intersection(&sidecar).cloned().collect::<Vec<_>>();
        web_only.sort();
        sidecar_only.sort();
        common.sort();
        json!({"webOnly":web_only,"sidecarOnly":sidecar_only,"common":common})
    };
    json!({
        "observed":true,
        "capturedAt":evidence.get("capturedAt").cloned().unwrap_or(Value::Null),
        "tone":evidence.get("tone").cloned().unwrap_or(Value::Null),
        "observationSha256":evidence.get("observationSha256").cloned().unwrap_or(Value::Null),
        "projectionPolicy":"observe_only",
        "streamingMode":evidence.get("streamingMode").cloned().unwrap_or(Value::Null),
        "sidecarStreamingMode":baseline["streamingMode"],
        "streamingModeMatch":evidence.get("streamingMode") == baseline.get("streamingMode"),
        "optionsSets":drift("optionsSets"),
        "allowedMessageTypes":drift("allowedMessageTypes"),
    })
}

fn string_set(value: Option<&Value>) -> HashSet<String> {
    value
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .map(str::to_owned)
        .collect()
}

fn bounded(value: usize, minimum: usize, maximum: usize, name: &str) -> Result<(), String> {
    if (minimum..=maximum).contains(&value) {
        Ok(())
    } else {
        Err(format!("{name}必須為 {minimum}-{maximum}"))
    }
}

fn valid_effort(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "none" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max" | "ultra"
    )
}

fn restart_required(name: &str) -> bool {
    matches!(
        name,
        "listenAddress"
            | "configPath"
            | "tokenCachePath"
            | "sessionCachePath"
            | "outboundProxy"
            | "clientId"
            | "authority"
            | "redirectUri"
            | "scope"
            | "debugLogPath"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_are_private_persistent_and_validated() {
        let root = tempfile::tempdir().unwrap();
        let config = Config::for_test(root.path().to_path_buf());
        let store = Store::open(root.path(), &config).unwrap();
        let mut settings = store.current();
        settings.chat_timeout_seconds = 321;
        store.save(settings).unwrap();
        assert_eq!(
            Store::open(root.path(), &config)
                .unwrap()
                .current()
                .chat_timeout_seconds,
            321
        );
        let mut invalid = store.current();
        invalid.max_output_tokens = invalid.context_window;
        assert!(store.save(invalid).is_err());
    }

    #[test]
    fn request_capability_evidence_is_validated_and_observe_only() {
        let digest = "4".repeat(64);
        let mut settings = RuntimeSettings {
            optional_model_capabilities: vec![json!({
                "publicModel":"future-model",
                "upstreamTone":"Future_Tone",
                "webLabel":"Future",
                "displayName":"Future model",
                "defaultReasoningLevel":"medium",
                "enabled":false,
                "evidence":{
                    "schema":"m365-web-model-capability-evidence/v1",
                    "selectorChoiceId":"Future_Tone",
                    "wireTone":"Future_Tone",
                    "capturedAt":"2026-08-20T00:00:00Z",
                    "temporaryChat":true,
                    "usabilityVerified":true,
                    "selectorObservationSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                    "usabilityObservationSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
                    "wireObservationSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
                }
            })],
            web_request_capability_evidence: json!({
                "schema":"m365-web-request-capability-evidence/v1",
                "capturedAt":"2026-08-20T00:00:00Z",
                "tone":"Future_Tone",
                "streamingMode":"ConciseV2",
                "optionsSets":["observed-option"],
                "allowedMessageTypes":["Chat"],
                "observationSha256":digest,
                "temporaryChat":true,
                "disableMemoryObserved":true
            }),
            ..RuntimeSettings::default()
        };
        validate(&settings).unwrap();
        assert!(crate::catalog::resolve(&settings, "future-model", "").is_none());
        let drift = request_capability_drift(&settings);
        assert_eq!(drift["observed"], true);
        assert_eq!(drift["projectionPolicy"], "observe_only");
        assert_eq!(drift["streamingModeMatch"], false);

        settings.web_request_capability_evidence["optionsSets"] = json!(["duplicate", "duplicate"]);
        assert!(validate(&settings).is_err());
    }

    #[test]
    fn direct_tool_overrides_fail_to_the_profile_safe_default() {
        assert_eq!(direct_override_value(Some("7"), 16, 16, 1, 512), 7);
        assert_eq!(direct_override_value(Some("9999"), 32, 16, 1, 512), 16);
        assert_eq!(direct_override_value(Some("bad"), 32, 16, 1, 512), 16);
        assert_eq!(direct_override_value(None, 32, 16, 1, 512), 32);
    }

    #[test]
    fn queue_timeout_defaults_match_the_live_scheduler_baseline() {
        let settings = RuntimeSettings::default();
        assert_eq!(settings.interactive_queue_timeout_seconds, 120);
        assert_eq!(settings.memory_queue_timeout_seconds, 120);
    }

    #[test]
    fn queue_timeout_validation_keeps_the_documented_bounds() {
        let mut settings = RuntimeSettings {
            interactive_queue_timeout_seconds: 1,
            memory_queue_timeout_seconds: 600,
            ..RuntimeSettings::default()
        };
        validate(&settings).unwrap();

        settings.interactive_queue_timeout_seconds = 0;
        assert!(validate(&settings).is_err());
        settings.interactive_queue_timeout_seconds = 120;
        settings.memory_queue_timeout_seconds = 601;
        assert!(validate(&settings).is_err());
    }
}
