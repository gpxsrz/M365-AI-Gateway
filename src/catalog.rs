use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::runtime_settings::RuntimeSettings;

#[derive(Clone, Debug)]
pub struct Route {
    pub id: String,
    pub canonical_route: String,
    pub tone: String,
    pub web_label: String,
    pub kind: String,
    pub owner: String,
    pub display_name: String,
    pub default_reasoning_level: String,
    pub visibility: String,
    pub mapping_evidence: String,
    pub identity_status: String,
    pub compatibility_required: bool,
    pub experimental: bool,
    pub configured_mapping: bool,
    pub optional_capability: bool,
    pub locked_effort: bool,
    pub evidence: Option<Value>,
}

impl Route {
    pub fn metadata(&self, requested: &str, effort_ignored: bool) -> Value {
        json!({
            "requested_model": requested,
            "response_model": requested,
            "canonical_route": self.canonical_route,
            "resolved_tone": self.tone,
            "web_label": self.web_label,
            "route_kind": self.kind,
            "operational_status": "enabled",
            "mapping_evidence": self.mapping_evidence,
            "identity_status": self.identity_status,
            "catalog_visibility": self.visibility,
            "alias_used": matches!(self.kind.as_str(), "alias" | "preset"),
            "compatibility_required": self.compatibility_required,
            "experimental": self.experimental,
            "fallback_used": false,
            "reasoning_effort_ignored": effort_ignored,
            "configured_mapping": self.configured_mapping,
        })
    }
}

pub struct Resolution {
    pub route: Route,
    pub requested_model: String,
    pub resolved_tone: String,
    pub effort_ignored: bool,
}

pub fn resolve(settings: &RuntimeSettings, requested: &str, effort: &str) -> Option<Resolution> {
    let requested = if requested.trim().is_empty() {
        "m365-copilot"
    } else {
        requested.trim()
    };
    let route = routes(settings)
        .into_iter()
        .find(|route| route.id.eq_ignore_ascii_case(requested))?;
    let effort_ignored = route.locked_effort && !effort.is_empty();
    let resolved_tone = if route.locked_effort
        || effort.is_empty()
        || matches!(effort, "none" | "minimal" | "low")
        || route.id.to_ascii_lowercase().contains("reasoning")
    {
        route.tone.clone()
    } else {
        match route.id.to_ascii_lowercase().as_str() {
            "claude" | "claude-sonnet" => "Claude_Sonnet_Reasoning".to_owned(),
            "gpt-5.2" => "Gpt_5_2_Reasoning".to_owned(),
            "gpt-5.3" | "gpt-5.3-think-deeper" => "Gpt_5_3_Reasoning".to_owned(),
            "gpt-5.4" | "gpt-5.4-quick" => "Gpt_5_4_Reasoning".to_owned(),
            "gpt-5.5" => "Gpt_5_5_Reasoning".to_owned(),
            _ => "Gpt_Reasoning".to_owned(),
        }
    };
    Some(Resolution {
        route,
        requested_model: requested.to_owned(),
        resolved_tone,
        effort_ignored,
    })
}

pub fn routes(settings: &RuntimeSettings) -> Vec<Route> {
    let mut routes = built_in_routes();
    for mapping in &settings.model_mappings {
        let id = mapping.public_model.trim();
        if id.is_empty() {
            continue;
        }
        let configured = Route {
            id: id.to_owned(),
            canonical_route: id.to_owned(),
            tone: mapping.upstream_tone.trim().to_owned(),
            web_label: String::new(),
            kind: "configured_mapping".to_owned(),
            owner: owner_for_tone(&mapping.upstream_tone).to_owned(),
            display_name: mapping.display_name.trim().to_owned(),
            default_reasoning_level: mapping.default_reasoning_level.trim().to_owned(),
            visibility: "compatibility".to_owned(),
            mapping_evidence: "unverified".to_owned(),
            identity_status: "accepted_unverified".to_owned(),
            compatibility_required: true,
            experimental: false,
            configured_mapping: true,
            optional_capability: false,
            locked_effort: true,
            evidence: None,
        };
        if let Some(index) = routes
            .iter()
            .position(|route| route.id.eq_ignore_ascii_case(id))
        {
            let protected = routes[index].id.to_ascii_lowercase().starts_with("m365-")
                || matches!(routes[index].kind.as_str(), "web_mode" | "alias" | "preset");
            if !protected {
                routes[index] = configured;
            }
        } else {
            routes.push(configured);
        }
    }
    for value in &settings.optional_model_capabilities {
        let Some(route) = optional_route(value) else {
            continue;
        };
        if !routes
            .iter()
            .any(|existing| existing.id.eq_ignore_ascii_case(&route.id))
        {
            routes.push(route);
        }
    }
    routes
}

pub fn ids(settings: &RuntimeSettings) -> Vec<String> {
    routes(settings)
        .into_iter()
        .filter(|route| route.visibility != "hidden")
        .map(|route| route.id)
        .collect()
}

pub fn tones(settings: &RuntimeSettings) -> Vec<String> {
    let mut tones = routes(settings)
        .into_iter()
        .map(|route| route.tone)
        .collect::<Vec<_>>();
    tones.sort();
    tones.dedup();
    tones
}

pub fn catalog(settings: &RuntimeSettings) -> Vec<Value> {
    let context_window = settings.context_window;
    let max_output = settings
        .max_output_tokens
        .min(context_window.saturating_sub(1));
    let max_input = context_window.saturating_sub(max_output);
    let efforts = json!([
        {"effort":"none","description":"Disable additional reasoning."},
        {"effort":"minimal","description":"Fast responses with minimal reasoning."},
        {"effort":"low","description":"Fast responses with lighter reasoning."},
        {"effort":"medium","description":"Balances speed and reasoning depth for everyday tasks."},
        {"effort":"high","description":"Greater reasoning depth for complex problems."},
        {"effort":"xhigh","description":"Extra high reasoning depth for complex problems."}
    ]);
    routes(settings)
        .into_iter()
        .filter(|route| route.visibility != "hidden")
        .map(|route| {
            let features = json!(["tools", "function_calling", "streaming", "reasoning", "vision"]);
            let modalities = json!(["text", "image"]);
            let mut entry = json!({
                "id": route.id,
                "slug": route.id,
                "display_name": route.display_name,
                "description": "Microsoft 365 gateway model route.",
                "canonical_route": route.canonical_route,
                "resolved_tone": route.tone,
                "route_kind": route.kind,
                "operational_status": "enabled",
                "mapping_evidence": route.mapping_evidence,
                "identity_status": route.identity_status,
                "catalog_visibility": route.visibility,
                "alias_used": matches!(route.kind.as_str(), "alias" | "preset"),
                "compatibility_required": route.compatibility_required,
                "configured_mapping": route.configured_mapping,
                "experimental": route.experimental,
                "deprecated": false,
                "default_reasoning_level": if route.default_reasoning_level.is_empty() { "medium" } else { &route.default_reasoning_level },
                "object": "model",
                "owned_by": route.owner,
                "supported_in_api": true,
                "visibility": "list",
                "priority": 1,
                "supports_parallel_tool_calls": true,
                "supports_image_detail_original": true,
                "supports_reasoning_summaries": true,
                "support_verbosity": true,
                "context_window": context_window,
                "max_context_window": context_window,
                "max_input_tokens": max_input,
                "max_output_tokens": max_output,
                "supports_tools": true,
                "tool_calls": true,
                "function_calling": true,
                "supports_function_calling": true,
                "supports_vision": true,
                "vision": true,
                "modalities": modalities,
                "input_modalities": modalities,
                "output_modalities": ["text"],
                "supported_features": features,
                "supported_reasoning_levels": efforts,
                "capabilities": {
                    "chat_completions": true,
                    "responses": true,
                    "streaming": true,
                    "tools": true,
                    "reasoning": true,
                    "vision": true,
                    "reasoning_efforts": efforts,
                    "modalities": modalities,
                    "input_modalities": modalities,
                    "output_modalities": ["text"],
                    "supported_features": features
                },
                "x_m365_route_source": "registry_config",
                "x_m365_mapping_source": if route.optional_capability { "operator_attested_web_observation" } else { "registry_config" },
                "x_m365_evidence_source": if route.optional_capability { "operator_attested_web_observation" } else { "none" },
            });
            if route.optional_capability {
                entry["x_m365_optional_capability"] = Value::Bool(true);
                if let Some(evidence) = route.evidence {
                    entry["x_m365_evidence_captured_at"] = evidence["capturedAt"].clone();
                    entry["x_m365_selector_observation_sha256"] = evidence["selectorObservationSha256"].clone();
                    entry["x_m365_usability_observation_sha256"] = evidence["usabilityObservationSha256"].clone();
                    entry["x_m365_wire_observation_sha256"] = evidence["wireObservationSha256"].clone();
                }
            }
            entry
        })
        .collect()
}

pub fn validate_optional_capabilities(values: &[Value]) -> Result<(), String> {
    let mut models = std::collections::HashSet::new();
    let mut tones = std::collections::HashSet::new();
    for value in values {
        let (route, _) = validated_optional_route(value)
            .ok_or_else(|| "可選 Web 模型缺少完整且可驗證的 capability evidence".to_owned())?;
        if !models.insert(route.id.to_ascii_lowercase())
            || !tones.insert(route.tone.to_ascii_lowercase())
            || built_in_routes().iter().any(|built_in| {
                built_in.id.eq_ignore_ascii_case(&route.id)
                    || built_in.tone.eq_ignore_ascii_case(&route.tone)
            })
        {
            return Err("可選 Web 模型或 tone 重複／與內建 route 衝突".to_owned());
        }
    }
    Ok(())
}

fn optional_route(value: &Value) -> Option<Route> {
    let (route, enabled) = validated_optional_route(value)?;
    enabled.then_some(route)
}

fn validated_optional_route(value: &Value) -> Option<(Route, bool)> {
    let object = value.as_object()?;
    let id = field(object, "publicModel")?;
    let tone = field(object, "upstreamTone")?;
    let web_label = field(object, "webLabel")?;
    let display_name = field(object, "displayName")?;
    let effort = field(object, "defaultReasoningLevel")?;
    let enabled = object.get("enabled").and_then(Value::as_bool)?;
    if !valid_id(id)
        || !valid_id(tone)
        || !matches!(
            effort,
            "none" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max" | "ultra"
        )
    {
        return None;
    }
    let evidence = object.get("evidence")?.as_object()?;
    if field(evidence, "schema")? != "m365-web-model-capability-evidence/v1"
        || field(evidence, "selectorChoiceId")? != tone
        || field(evidence, "wireTone")? != tone
        || !object_bool(evidence, "temporaryChat")
        || !object_bool(evidence, "usabilityVerified")
        || OffsetDateTime::parse(field(evidence, "capturedAt")?, &Rfc3339).is_err()
        || [
            "selectorObservationSha256",
            "usabilityObservationSha256",
            "wireObservationSha256",
        ]
        .iter()
        .any(|name| !valid_digest(field(evidence, name).unwrap_or_default()))
    {
        return None;
    }
    Some((
        Route {
            id: id.to_owned(),
            canonical_route: id.to_owned(),
            tone: tone.to_owned(),
            web_label: web_label.to_owned(),
            kind: "web_model_route".to_owned(),
            owner: "microsoft-365".to_owned(),
            display_name: display_name.to_owned(),
            default_reasoning_level: effort.to_owned(),
            visibility: "public".to_owned(),
            mapping_evidence: "web_payload_verified".to_owned(),
            identity_status: "accepted_unverified".to_owned(),
            compatibility_required: false,
            experimental: true,
            configured_mapping: false,
            optional_capability: true,
            locked_effort: true,
            evidence: object.get("evidence").cloned(),
        },
        enabled,
    ))
}

pub(crate) fn valid_mapping_tone(settings: &RuntimeSettings, tone: &str) -> bool {
    let tone = tone.trim();
    built_in_routes().iter().any(|route| route.tone == tone)
        || settings
            .optional_model_capabilities
            .iter()
            .filter_map(optional_route)
            .any(|route| route.tone == tone)
}

pub(crate) fn observed_tone(settings: &RuntimeSettings, tone: &str) -> bool {
    let tone = tone.trim();
    built_in_routes().iter().any(|route| route.tone == tone)
        || settings
            .optional_model_capabilities
            .iter()
            .filter_map(validated_optional_route)
            .any(|(route, _)| route.tone == tone)
}

fn field<'a>(object: &'a serde_json::Map<String, Value>, name: &str) -> Option<&'a str> {
    object
        .get(name)?
        .as_str()
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

fn object_bool(object: &serde_json::Map<String, Value>, name: &str) -> bool {
    object.get(name).and_then(Value::as_bool) == Some(true)
}

fn valid_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._-".contains(&byte))
}

fn valid_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn owner_for_tone(tone: &str) -> &'static str {
    if tone.trim().to_ascii_lowercase().starts_with("claude_") {
        "anthropic-via-microsoft-365"
    } else {
        "microsoft-365"
    }
}

fn built_in_routes() -> Vec<Route> {
    [
        (
            "m365-auto",
            "m365-auto",
            "Magic",
            "Auto",
            "web_mode",
            "public",
            "M365 Auto",
            "none",
            false,
            false,
        ),
        (
            "m365-gpt-5.6-think-deeper",
            "m365-gpt-5.6-think-deeper",
            "Gpt_5_6_Reasoning",
            "GPT 5.6 — Think deeper",
            "web_model_route",
            "public",
            "M365 GPT 5.6 — Think deeper",
            "medium",
            false,
            false,
        ),
        (
            "m365-gpt-5.5-quick-response",
            "m365-gpt-5.5-quick-response",
            "Gpt_5_5_Chat",
            "GPT 5.5 — Quick response",
            "web_model_route",
            "public",
            "M365 GPT 5.5 — Quick response",
            "low",
            false,
            false,
        ),
        (
            "m365-copilot",
            "m365-auto",
            "Magic",
            "Auto",
            "alias",
            "compatibility",
            "M365 Copilot (compatibility alias)",
            "none",
            true,
            false,
        ),
        (
            "gpt-5.6-reasoning",
            "m365-gpt-5.6-think-deeper",
            "Gpt_5_6_Reasoning",
            "GPT 5.6 — Think deeper",
            "alias",
            "compatibility",
            "GPT 5.6 Reasoning (compatibility alias)",
            "medium",
            true,
            false,
        ),
        (
            "gpt-5.5",
            "m365-gpt-5.5-quick-response",
            "Gpt_5_5_Chat",
            "GPT 5.5 — Quick response",
            "alias",
            "compatibility",
            "GPT 5.5 (compatibility alias)",
            "low",
            true,
            false,
        ),
        (
            "gpt-5.2",
            "gpt-5.2",
            "Gpt_5_2_Chat",
            "",
            "legacy_direct",
            "public",
            "gpt-5.2",
            "low",
            false,
            true,
        ),
        (
            "gpt-5.2-reasoning",
            "gpt-5.2-reasoning",
            "Gpt_5_2_Reasoning",
            "",
            "legacy_direct",
            "public",
            "gpt-5.2-reasoning",
            "medium",
            false,
            true,
        ),
        (
            "gpt-5.3",
            "gpt-5.3",
            "Gpt_5_3_Chat",
            "",
            "legacy_direct",
            "public",
            "gpt-5.3",
            "low",
            false,
            true,
        ),
        (
            "gpt-5.4",
            "gpt-5.4",
            "Gpt_5_4_Chat",
            "",
            "legacy_direct",
            "public",
            "gpt-5.4",
            "low",
            false,
            true,
        ),
        (
            "gpt-5.4-reasoning",
            "gpt-5.4-reasoning",
            "Gpt_5_4_Reasoning",
            "",
            "legacy_direct",
            "public",
            "gpt-5.4-reasoning",
            "medium",
            false,
            true,
        ),
        (
            "gpt-5.5-reasoning",
            "gpt-5.5-reasoning",
            "Gpt_5_5_Reasoning",
            "",
            "legacy_direct",
            "public",
            "gpt-5.5-reasoning",
            "medium",
            false,
            true,
        ),
        (
            "claude-sonnet",
            "claude-sonnet",
            "Claude_Sonnet",
            "",
            "legacy_direct",
            "public",
            "Claude Sonnet",
            "low",
            false,
            true,
        ),
        (
            "claude-sonnet-reasoning",
            "claude-sonnet-reasoning",
            "Claude_Sonnet_Reasoning",
            "",
            "legacy_direct",
            "public",
            "Claude Sonnet Reasoning",
            "medium",
            false,
            true,
        ),
        (
            "gpt-5.6-sol",
            "m365-gpt-5.6-think-deeper",
            "Gpt_5_6_Reasoning",
            "",
            "preset",
            "compatibility",
            "GPT-5.6-Sol (compatibility preset)",
            "low",
            true,
            false,
        ),
        (
            "gpt-5.6-terra",
            "m365-gpt-5.6-think-deeper",
            "Gpt_5_6_Reasoning",
            "",
            "preset",
            "compatibility",
            "GPT-5.6-Terra (compatibility preset)",
            "medium",
            true,
            false,
        ),
        (
            "gpt-5.6-luna",
            "m365-gpt-5.6-think-deeper",
            "Gpt_5_6_Reasoning",
            "",
            "preset",
            "compatibility",
            "GPT-5.6-Luna (compatibility preset)",
            "medium",
            true,
            false,
        ),
        (
            "claude",
            "claude-sonnet",
            "Claude_Sonnet",
            "",
            "alias",
            "hidden",
            "Claude",
            "low",
            true,
            false,
        ),
        (
            "gpt-5.4-quick",
            "gpt-5.4",
            "Gpt_5_4_Chat",
            "",
            "alias",
            "hidden",
            "GPT 5.4 Quick",
            "low",
            true,
            false,
        ),
        (
            "gpt-5.3-think-deeper",
            "gpt-5.3",
            "Gpt_5_3_Chat",
            "",
            "alias",
            "hidden",
            "GPT 5.3 Think Deeper",
            "medium",
            true,
            false,
        ),
        (
            "quick",
            "quick",
            "Chat",
            "Quick response",
            "web_mode",
            "hidden",
            "Quick response",
            "none",
            false,
            false,
        ),
        (
            "think-deeper",
            "think-deeper",
            "Reasoning",
            "Think deeper",
            "web_mode",
            "hidden",
            "Think deeper",
            "medium",
            false,
            false,
        ),
    ]
    .into_iter()
    .map(
        |(
            id,
            canonical,
            tone,
            label,
            kind,
            visibility,
            display,
            effort,
            compatibility,
            experimental,
        )| Route {
            id: id.to_owned(),
            canonical_route: canonical.to_owned(),
            tone: tone.to_owned(),
            web_label: label.to_owned(),
            kind: kind.to_owned(),
            owner: owner_for_tone(tone).to_owned(),
            display_name: display.to_owned(),
            default_reasoning_level: effort.to_owned(),
            visibility: visibility.to_owned(),
            mapping_evidence: "api_tone_accepted".to_owned(),
            identity_status: if id == "m365-auto" || id == "m365-copilot" {
                "dynamic_unidentified"
            } else {
                "accepted_unverified"
            }
            .to_owned(),
            compatibility_required: compatibility,
            experimental,
            configured_mapping: false,
            optional_capability: false,
            locked_effort: id.starts_with("m365-")
                || matches!(kind, "web_mode" | "preset")
                || id == "gpt-5.6-reasoning",
            evidence: None,
        },
    )
    .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn configured_mappings_override_only_legacy_routes() {
        let mut settings = RuntimeSettings::default();
        settings
            .model_mappings
            .push(crate::runtime_settings::ModelMapping {
                public_model: "gpt-5.2".to_owned(),
                upstream_tone: "Custom_Tone".to_owned(),
                display_name: "Custom".to_owned(),
                default_reasoning_level: "low".to_owned(),
            });
        settings
            .model_mappings
            .push(crate::runtime_settings::ModelMapping {
                public_model: "m365-auto".to_owned(),
                upstream_tone: "Unsafe".to_owned(),
                display_name: "Unsafe".to_owned(),
                default_reasoning_level: "low".to_owned(),
            });
        assert_eq!(
            resolve(&settings, "gpt-5.2", "").unwrap().resolved_tone,
            "Custom_Tone"
        );
        assert_eq!(
            resolve(&settings, "m365-auto", "").unwrap().resolved_tone,
            "Magic"
        );
    }

    #[test]
    fn optional_capabilities_require_bound_evidence_and_project_identity() {
        let digest = "a".repeat(64);
        let capability = json!({
            "publicModel":"future-model",
            "upstreamTone":"Future_Tone",
            "webLabel":"Future",
            "displayName":"Future model",
            "defaultReasoningLevel":"medium",
            "enabled":true,
            "evidence":{
                "schema":"m365-web-model-capability-evidence/v1",
                "selectorChoiceId":"Future_Tone",
                "wireTone":"Future_Tone",
                "capturedAt":"2026-08-20T00:00:00Z",
                "temporaryChat":true,
                "usabilityVerified":true,
                "selectorObservationSha256":digest,
                "usabilityObservationSha256":digest,
                "wireObservationSha256":digest
            }
        });
        let settings = RuntimeSettings {
            optional_model_capabilities: vec![capability.clone()],
            ..RuntimeSettings::default()
        };
        validate_optional_capabilities(&settings.optional_model_capabilities).unwrap();
        let entry = catalog(&settings)
            .into_iter()
            .find(|entry| entry["id"] == "future-model")
            .unwrap();
        assert_eq!(entry["resolved_tone"], "Future_Tone");
        assert_eq!(entry["x_m365_optional_capability"], true);
        assert_eq!(
            entry["x_m365_mapping_source"],
            "operator_attested_web_observation"
        );
        assert_eq!(entry["x_m365_selector_observation_sha256"], digest);

        let mut mismatched = capability;
        mismatched["evidence"]["wireTone"] = json!("Different_Tone");
        assert!(validate_optional_capabilities(&[mismatched]).is_err());
    }
}
