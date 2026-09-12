use std::{
    collections::VecDeque,
    path::PathBuf,
    sync::{Arc, Mutex},
    time::Instant,
};

use axum::{
    Json,
    extract::{Query, Request, State},
    http::StatusCode,
    middleware::Next,
    response::{IntoResponse, Response},
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use serde_json::json;
use time::{Duration, OffsetDateTime};

use crate::{error::openai_error, private_file, web::Gateway};

const MAX_RECORDS: usize = 1_000;
const COMPACT_EVERY: usize = 100;
const MAX_LOG_BYTES: u64 = 16 * 1024 * 1024;
const MAX_RECORDED_UTF16: usize = 1_000_000;
const MAX_RECORDED_BYTES: usize = 64 * 1024 * 1024;
const SURFACE_ID: &str = "m365-privacy-telemetry/v1";

#[derive(Clone, Copy)]
pub(crate) struct TracedResponse;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum ProvenanceClass {
    None,
    AuthenticatedEphemeralRecall,
    RejectedUntrusted,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum RequestClass {
    Unclassified,
    ManagementOrAuxiliary,
    ExternalUser,
    Autonomous,
    ControlPlane,
    AsyncCompletion,
    Memory,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum AdmissionResult {
    NotReached,
    Admitted,
    UpstreamThrottle,
    InteractiveCapacityBusy,
    MemoryCapacityDeferred,
    OtherDenied,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum BreakerProjection {
    NotReached,
    Pending,
    Admitted,
    RecoveryProbe,
    Throttled,
    QueueDenied,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum SpillDecision {
    None,
    Eligible,
    Performed,
    Denied,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum SpillReason {
    NotEvaluated,
    NotApplicable,
    NotRequired,
    BelowLimit,
    RecalledSourceMaterial,
    SafeBulkCandidate,
    MemorySpillDisabled,
    AttachmentSlotsFull,
    NoSafeCandidate,
    CannotFitInline,
    GeneratedFileTooLarge,
    ProjectionFailed,
    FullContextDocument,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum UpstreamAttempt {
    None,
    Initial,
    Retried,
    Followup,
    FollowupRetried,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum UpstreamResult {
    NotAttempted,
    Success,
    ResponseFormatInvalid,
    Timeout,
    MissingIdentity,
    EmptyPrompt,
    EmptyResponse,
    RateLimited429,
    ServiceUnavailable503,
    AttachmentError,
    TerminalError,
    TransportError,
    ProtocolError,
    ContextLength,
    JsonDecode,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum CallerDelivery {
    NotEvaluated,
    /// The internal JSON/SSE response producer accepted the body/frame.
    /// This is not an acknowledgement that the network client received it.
    Sent,
    Failed,
    Cancelled,
}

macro_rules! telemetry_names {
    ($type:ty, {$($variant:path => $name:literal),+ $(,)?}) => {
        impl $type {
            pub(crate) fn as_str(self) -> &'static str {
                match self {
                    $($variant => $name),+
                }
            }
        }
    };
}

telemetry_names!(ProvenanceClass, {
    ProvenanceClass::None => "none",
    ProvenanceClass::AuthenticatedEphemeralRecall => "authenticated_ephemeral_recall",
    ProvenanceClass::RejectedUntrusted => "rejected_untrusted",
});
telemetry_names!(RequestClass, {
    RequestClass::Unclassified => "unclassified",
    RequestClass::ManagementOrAuxiliary => "management_or_auxiliary",
    RequestClass::ExternalUser => "external_user",
    RequestClass::Autonomous => "autonomous",
    RequestClass::ControlPlane => "control_plane",
    RequestClass::AsyncCompletion => "async_completion",
    RequestClass::Memory => "memory",
});
telemetry_names!(AdmissionResult, {
    AdmissionResult::NotReached => "not_reached",
    AdmissionResult::Admitted => "admitted",
    AdmissionResult::UpstreamThrottle => "upstream_throttle",
    AdmissionResult::InteractiveCapacityBusy => "interactive_capacity_busy",
    AdmissionResult::MemoryCapacityDeferred => "memory_capacity_deferred",
    AdmissionResult::OtherDenied => "other_denied",
});
telemetry_names!(BreakerProjection, {
    BreakerProjection::NotReached => "not_reached",
    BreakerProjection::Pending => "pending",
    BreakerProjection::Admitted => "admitted",
    BreakerProjection::RecoveryProbe => "recovery_probe",
    BreakerProjection::Throttled => "throttled",
    BreakerProjection::QueueDenied => "queue_denied",
});
telemetry_names!(SpillDecision, {
    SpillDecision::None => "none",
    SpillDecision::Eligible => "eligible",
    SpillDecision::Performed => "performed",
    SpillDecision::Denied => "denied",
});
telemetry_names!(SpillReason, {
    SpillReason::NotEvaluated => "not_evaluated",
    SpillReason::NotApplicable => "not_applicable",
    SpillReason::NotRequired => "not_required",
    SpillReason::BelowLimit => "below_limit",
    SpillReason::RecalledSourceMaterial => "recalled_source_material",
    SpillReason::SafeBulkCandidate => "safe_bulk_candidate",
    SpillReason::MemorySpillDisabled => "memory_spill_disabled",
    SpillReason::AttachmentSlotsFull => "attachment_slots_full",
    SpillReason::NoSafeCandidate => "no_safe_candidate",
    SpillReason::CannotFitInline => "cannot_fit_inline",
    SpillReason::GeneratedFileTooLarge => "generated_file_too_large",
    SpillReason::ProjectionFailed => "projection_failed",
    SpillReason::FullContextDocument => "full_context_document",
});
telemetry_names!(UpstreamAttempt, {
    UpstreamAttempt::None => "none",
    UpstreamAttempt::Initial => "initial",
    UpstreamAttempt::Retried => "retried",
    UpstreamAttempt::Followup => "followup",
    UpstreamAttempt::FollowupRetried => "followup_retried",
});
telemetry_names!(UpstreamResult, {
    UpstreamResult::NotAttempted => "not_attempted",
    UpstreamResult::Success => "success",
    UpstreamResult::ResponseFormatInvalid => "response_format_invalid",
    UpstreamResult::Timeout => "timeout",
    UpstreamResult::MissingIdentity => "missing_identity",
    UpstreamResult::EmptyPrompt => "empty_prompt",
    UpstreamResult::EmptyResponse => "empty_response",
    UpstreamResult::RateLimited429 => "rate_limited_429",
    UpstreamResult::ServiceUnavailable503 => "service_unavailable_503",
    UpstreamResult::AttachmentError => "attachment_error",
    UpstreamResult::TerminalError => "terminal_error",
    UpstreamResult::TransportError => "transport_error",
    UpstreamResult::ProtocolError => "protocol_error",
    UpstreamResult::ContextLength => "context_length",
    UpstreamResult::JsonDecode => "json_decode",
});
telemetry_names!(CallerDelivery, {
    CallerDelivery::NotEvaluated => "not_evaluated",
    CallerDelivery::Sent => "sent",
    CallerDelivery::Failed => "failed",
    CallerDelivery::Cancelled => "cancelled",
});

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Record {
    schema: String,
    id: String,
    correlation_id: String,
    at: String,
    level: String,
    protocol: String,
    route: String,
    method: String,
    path: String,
    status: u16,
    duration_ms: u64,
    request_class: String,
    admission_result: String,
    breaker_state: String,
    breaker_projection: String,
    spill_decision: String,
    spill_reason: String,
    utf16_before: usize,
    utf16_after: usize,
    utf16_before_class: String,
    utf16_after_class: String,
    provenance_class: String,
    upstream_attempt_class: String,
    upstream_result_class: String,
    // These fields are a live projection. Keeping them out of the v1 JSONL
    // record preserves readback by an older rollback binary.
    #[serde(
        skip_serializing,
        default = "default_not_evaluated",
        deserialize_with = "deserialize_not_evaluated"
    )]
    post_policy_disposition: String,
    #[serde(
        skip_serializing,
        default = "default_not_evaluated",
        deserialize_with = "deserialize_not_evaluated"
    )]
    post_policy_reason: String,
    #[serde(
        skip_serializing,
        default = "default_not_evaluated",
        deserialize_with = "deserialize_not_evaluated"
    )]
    caller_delivery: String,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_false")]
    tool_call_suppressed: bool,
    // Transport details are a bounded live projection. They are deliberately
    // absent from the v1 JSONL record so an older rollback reader can still
    // read the authoritative durable surface.
    #[serde(
        skip_serializing,
        default = "default_not_evaluated",
        deserialize_with = "deserialize_not_evaluated"
    )]
    transport_projection: String,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    wire_before_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    inline_core_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    message_text_before_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    preliminary_message_text_after_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    message_text_after_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    wire_after_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    preliminary_wire_after_utf16: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    generated_document_bytes: usize,
    #[serde(skip_serializing, default, deserialize_with = "deserialize_zero_usize")]
    generated_document_message_count: usize,
    #[serde(
        skip_serializing,
        default = "default_not_evaluated",
        deserialize_with = "deserialize_not_evaluated"
    )]
    generated_document_state: String,
    // This field already existed in the v1 Record shape. It is now written
    // durably so a generated-document transport incident can be classified
    // after restart. Older rollback readers already know this v1 field and
    // their existing deserializer resets it conservatively.
    #[serde(default = "default_not_evaluated")]
    fallback_failure: String,
    request_id: String,
    error_code: String,
    input_tokens: usize,
    output_tokens: usize,
    message_count: usize,
    tool_count: usize,
    attachment_count: usize,
    event_count: usize,
    snapshot_available: bool,
    snapshot_expires_at: Option<String>,
}

fn default_not_evaluated() -> String {
    "not_evaluated".to_owned()
}

fn deserialize_not_evaluated<'de, D>(deserializer: D) -> Result<String, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let _ = serde::de::IgnoredAny::deserialize(deserializer)?;
    Ok(default_not_evaluated())
}

fn deserialize_false<'de, D>(deserializer: D) -> Result<bool, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let _ = serde::de::IgnoredAny::deserialize(deserializer)?;
    Ok(false)
}

fn deserialize_zero_usize<'de, D>(deserializer: D) -> Result<usize, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let _ = serde::de::IgnoredAny::deserialize(deserializer)?;
    Ok(0)
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct Audit {
    at: String,
    action: String,
    result: String,
    session_id: String,
    record_id: String,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
struct Session {
    active: bool,
    id: String,
    expires_at: Option<String>,
}

struct Inner {
    records: VecDeque<Record>,
    audit: VecDeque<Audit>,
    session: Session,
    surface_id: String,
    path_class: String,
    reader_state: String,
    writer_state: String,
    writes_since_compaction: usize,
}

#[derive(Clone)]
pub(crate) struct Store {
    inner: Arc<Mutex<Inner>>,
    path: Option<Arc<PathBuf>>,
}

impl Store {
    pub(crate) fn open(
        path: PathBuf,
        path_class: &str,
    ) -> Result<Self, crate::error::GatewayError> {
        if path.extension().and_then(|value| value.to_str()) != Some("jsonl") {
            return Err(crate::error::GatewayError::Configuration(
                "privacy telemetry path must use the .jsonl extension; legacy log.db is not an authoritative source"
                    .to_owned(),
            ));
        }
        private_file::prepare_private_file(&path)?;
        if !path.exists() {
            private_file::write_text(&path, "")?;
        }
        let metadata = std::fs::metadata(&path).map_err(|error| {
            crate::error::GatewayError::Storage(format!("{}: {error}", path.display()))
        })?;
        if !metadata.is_file() || metadata.len() > MAX_LOG_BYTES {
            return Err(crate::error::GatewayError::Storage(format!(
                "unsafe privacy telemetry file: {}",
                path.display()
            )));
        }
        let raw = std::fs::read_to_string(&path).map_err(|error| {
            crate::error::GatewayError::Storage(format!("{}: {error}", path.display()))
        })?;
        let lines = raw.lines().collect::<Vec<_>>();
        let mut records = VecDeque::new();
        let mut reader_state = "ok";
        let mut repair = None;
        for (index, line) in lines.iter().enumerate() {
            if line.trim().is_empty() {
                continue;
            }
            match serde_json::from_str::<Record>(line) {
                Ok(record) if record.valid() => {
                    records.push_front(record);
                    records.truncate(MAX_RECORDS);
                    if index + 1 == lines.len() && !raw.ends_with('\n') {
                        repair = Some(format!("{raw}\n"));
                        reader_state = "trailing_newline_repaired";
                    }
                }
                Err(_)
                    if index + 1 == lines.len()
                        && !raw.ends_with('\n')
                        && serde_json::from_str::<serde_json::Value>(line).is_err() =>
                {
                    let valid_bytes = raw.rfind('\n').map_or(0, |offset| offset + 1);
                    repair = Some(raw[..valid_bytes].to_owned());
                    reader_state = "trailing_partial_repaired";
                }
                _ => {
                    return Err(crate::error::GatewayError::Storage(format!(
                        "invalid privacy telemetry file: {}",
                        path.display()
                    )));
                }
            }
        }
        if let Some(repaired) = repair {
            private_file::write_text(&path, &repaired)?;
        }
        Ok(Self {
            inner: Arc::new(Mutex::new(Inner {
                records,
                audit: VecDeque::new(),
                session: Session::default(),
                surface_id: SURFACE_ID.to_owned(),
                path_class: path_class.to_owned(),
                reader_state: reader_state.to_owned(),
                writer_state: "ok".to_owned(),
                writes_since_compaction: 0,
            })),
            path: Some(Arc::new(path)),
        })
    }

    fn push(&self, record: Record) {
        let mut inner = self.inner.lock().expect("debug store poisoned");
        expire(&mut inner);
        if !record.valid() {
            inner.writer_state = "invalid_record_rejected".to_owned();
            return;
        }
        let encoded = serde_json::to_string(&record).expect("typed telemetry is serializable");
        if let Some(path) = self.path.as_deref() {
            if inner.writer_state == "error" {
                return;
            }
            if private_file::append_line(path, &encoded).is_err() {
                inner.writer_state = "error".to_owned();
                return;
            }
            inner.writer_state = "ok".to_owned();
            inner.writes_since_compaction += 1;
        }
        inner.records.push_front(record);
        inner.records.truncate(MAX_RECORDS);
        let Some(path) = self.path.as_deref() else {
            return;
        };
        if inner.records.len() == MAX_RECORDS && inner.writes_since_compaction >= COMPACT_EVERY {
            let compacted = inner
                .records
                .iter()
                .rev()
                .map(|record| {
                    serde_json::to_string(record).expect("typed telemetry is serializable")
                })
                .collect::<Vec<_>>()
                .join("\n");
            match private_file::write_text(path, &format!("{compacted}\n")) {
                Ok(()) => {
                    inner.writer_state = "ok".to_owned();
                    inner.writes_since_compaction = 0;
                }
                Err(_) => inner.writer_state = "error".to_owned(),
            }
        }
    }

    pub(crate) fn start_request(&self, method: &str, path: &str) -> Trace {
        Trace {
            inner: Arc::new(TraceInner {
                store: self.clone(),
                started: Instant::now(),
                record: Mutex::new(Record::new(method, path)),
            }),
        }
    }
}

impl Default for Store {
    fn default() -> Self {
        Self {
            inner: Arc::new(Mutex::new(Inner {
                records: VecDeque::new(),
                audit: VecDeque::new(),
                session: Session::default(),
                surface_id: SURFACE_ID.to_owned(),
                path_class: "memory_only_test".to_owned(),
                reader_state: "memory_only".to_owned(),
                writer_state: "memory_only".to_owned(),
                writes_since_compaction: 0,
            })),
            path: None,
        }
    }
}

impl Record {
    fn new(method: &str, path: &str) -> Self {
        let correlation_id = random_id();
        Self {
            schema: SURFACE_ID.to_owned(),
            id: correlation_id.clone(),
            correlation_id,
            at: now(),
            level: "info".to_owned(),
            protocol: protocol(path).to_owned(),
            route: route(path).to_owned(),
            method: method.to_owned(),
            path: route_template(path).to_owned(),
            status: 0,
            duration_ms: 0,
            request_class: RequestClass::Unclassified.as_str().to_owned(),
            admission_result: AdmissionResult::NotReached.as_str().to_owned(),
            breaker_state: "unknown".to_owned(),
            breaker_projection: BreakerProjection::NotReached.as_str().to_owned(),
            spill_decision: SpillDecision::None.as_str().to_owned(),
            spill_reason: SpillReason::NotEvaluated.as_str().to_owned(),
            utf16_before: 0,
            utf16_after: 0,
            utf16_before_class: "unknown".to_owned(),
            utf16_after_class: "unknown".to_owned(),
            provenance_class: ProvenanceClass::None.as_str().to_owned(),
            upstream_attempt_class: UpstreamAttempt::None.as_str().to_owned(),
            upstream_result_class: UpstreamResult::NotAttempted.as_str().to_owned(),
            post_policy_disposition: "not_evaluated".to_owned(),
            post_policy_reason: "not_evaluated".to_owned(),
            caller_delivery: CallerDelivery::NotEvaluated.as_str().to_owned(),
            tool_call_suppressed: false,
            transport_projection: "not_evaluated".to_owned(),
            wire_before_utf16: 0,
            inline_core_utf16: 0,
            message_text_before_utf16: 0,
            preliminary_message_text_after_utf16: 0,
            message_text_after_utf16: 0,
            wire_after_utf16: 0,
            preliminary_wire_after_utf16: 0,
            generated_document_bytes: 0,
            generated_document_message_count: 0,
            generated_document_state: "not_evaluated".to_owned(),
            fallback_failure: "not_evaluated".to_owned(),
            request_id: String::new(),
            error_code: String::new(),
            input_tokens: 0,
            output_tokens: 0,
            message_count: 0,
            tool_count: 0,
            attachment_count: 0,
            event_count: 0,
            snapshot_available: false,
            snapshot_expires_at: None,
        }
    }

    fn valid(&self) -> bool {
        let expected_level = if self.status >= 500 {
            "error"
        } else if self.status >= 400 {
            "warn"
        } else {
            "info"
        };
        self.schema == SURFACE_ID
            && self.id == self.correlation_id
            && valid_correlation_id(&self.correlation_id)
            && OffsetDateTime::parse(&self.at, &time::format_description::well_known::Rfc3339)
                .is_ok()
            && self.level == expected_level
            && matches!(
                self.protocol.as_str(),
                "responses" | "anthropic" | "openai" | "management"
            )
            && matches!(
                self.route.as_str(),
                "hermes" | "memory" | "auxiliary" | "management"
            )
            && matches!(
                self.method.as_str(),
                "GET" | "POST" | "PUT" | "DELETE" | "HEAD" | "PATCH" | "OPTIONS"
            )
            && route_template(&self.path) == self.path
            && protocol(&self.path) == self.protocol
            && route(&self.path) == self.route
            && (self.status == 0 || (100..=599).contains(&self.status))
            && matches!(
                self.request_class.as_str(),
                "unclassified"
                    | "management_or_auxiliary"
                    | "external_user"
                    | "autonomous"
                    | "control_plane"
                    | "async_completion"
                    | "memory"
            )
            && matches!(
                self.admission_result.as_str(),
                "not_reached"
                    | "admitted"
                    | "upstream_throttle"
                    | "interactive_capacity_busy"
                    | "memory_capacity_deferred"
                    | "other_denied"
            )
            && matches!(
                self.breaker_state.as_str(),
                "unknown" | "CLOSED" | "OPEN" | "HALF_OPEN_READY" | "PROBE_IN_FLIGHT" | "RECOVERY"
            )
            && matches!(
                self.breaker_projection.as_str(),
                "not_reached"
                    | "pending"
                    | "admitted"
                    | "recovery_probe"
                    | "throttled"
                    | "queue_denied"
            )
            && matches!(
                self.spill_decision.as_str(),
                "none" | "eligible" | "performed" | "denied"
            )
            && matches!(
                self.spill_reason.as_str(),
                "not_evaluated"
                    | "not_applicable"
                    | "not_required"
                    | "below_limit"
                    | "recalled_source_material"
                    | "safe_bulk_candidate"
                    | "memory_spill_disabled"
                    | "attachment_slots_full"
                    | "no_safe_candidate"
                    | "cannot_fit_inline"
                    | "generated_file_too_large"
                    | "projection_failed"
                    | "full_context_document"
            )
            && valid_utf16_class(self.utf16_before, &self.utf16_before_class)
            && valid_utf16_class(self.utf16_after, &self.utf16_after_class)
            && matches!(
                self.provenance_class.as_str(),
                "none" | "authenticated_ephemeral_recall" | "rejected_untrusted"
            )
            && matches!(
                self.upstream_attempt_class.as_str(),
                "none" | "initial" | "retried" | "followup" | "followup_retried"
            )
            && matches!(
                self.upstream_result_class.as_str(),
                "not_attempted"
                    | "success"
                    | "response_format_invalid"
                    | "timeout"
                    | "missing_identity"
                    | "empty_prompt"
                    | "empty_response"
                    | "rate_limited_429"
                    | "service_unavailable_503"
                    | "attachment_error"
                    | "terminal_error"
                    | "transport_error"
                    | "protocol_error"
                    | "context_length"
                    | "json_decode"
            )
            && self.post_policy_disposition == "not_evaluated"
            && self.post_policy_reason == "not_evaluated"
            && matches!(
                self.caller_delivery.as_str(),
                "not_evaluated" | "sent" | "failed" | "cancelled"
            )
            && valid_transport_projection(&self.transport_projection)
            && self.wire_before_utf16 <= MAX_RECORDED_UTF16
            && self.inline_core_utf16 <= MAX_RECORDED_UTF16
            && self.message_text_before_utf16 <= MAX_RECORDED_UTF16
            && self.preliminary_message_text_after_utf16 <= MAX_RECORDED_UTF16
            && self.message_text_after_utf16 <= MAX_RECORDED_UTF16
            && self.wire_after_utf16 <= MAX_RECORDED_UTF16
            && self.preliminary_wire_after_utf16 <= MAX_RECORDED_UTF16
            && self.generated_document_bytes <= MAX_RECORDED_BYTES
            && self.generated_document_message_count <= MAX_RECORDED_UTF16
            && valid_generated_document_state(&self.generated_document_state)
            && valid_fallback_failure(&self.fallback_failure)
            && valid_spill_relation(
                &self.route,
                &self.provenance_class,
                &self.spill_decision,
                &self.spill_reason,
                self.utf16_before,
                self.utf16_after,
            )
            && (self.upstream_result_class == "not_attempted"
                || self.upstream_attempt_class != "none")
            && self.request_id.is_empty()
            && self.error_code.is_empty()
            && self.input_tokens == 0
            && self.output_tokens == 0
            && self.message_count == 0
            && self.tool_count == 0
            && self.attachment_count == 0
            && self.event_count == 0
            && !self.snapshot_available
            && self.snapshot_expires_at.is_none()
    }
}

fn valid_spill_relation(
    route: &str,
    provenance: &str,
    decision: &str,
    reason: &str,
    before: usize,
    after: usize,
) -> bool {
    let authenticated_recall = route == "hermes" && provenance == "authenticated_ephemeral_recall";
    if provenance == "authenticated_ephemeral_recall" && route != "hermes" {
        return false;
    }
    match decision {
        "none" => {
            matches!(reason, "not_evaluated" | "not_applicable" | "not_required") && before == after
        }
        "eligible" => reason == "below_limit" && authenticated_recall && before == after,
        "performed" => {
            if reason == "full_context_document" {
                // Durable spill measurements are canonical message.text units;
                // complete serialized-payload observations remain live only.
                return route != "memory" && before > after;
            }
            ((reason == "recalled_source_material" && authenticated_recall)
                || reason == "safe_bulk_candidate")
                && before > after
        }
        "denied" => {
            matches!(
                reason,
                "memory_spill_disabled"
                    | "attachment_slots_full"
                    | "no_safe_candidate"
                    | "cannot_fit_inline"
                    | "generated_file_too_large"
                    | "projection_failed"
                    | "full_context_document"
            ) && before == after
                && ((reason == "memory_spill_disabled" && route == "memory")
                    || (reason != "memory_spill_disabled" && route != "memory"))
        }
        _ => false,
    }
}

fn valid_correlation_id(value: &str) -> bool {
    value.len() == 24
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
}

fn valid_transport_projection(value: &str) -> bool {
    matches!(
        value,
        "not_evaluated" | "inline" | "bulk_spill" | "full_context_document" | "overflow"
    )
}

fn valid_generated_document_state(value: &str) -> bool {
    matches!(
        value,
        "not_evaluated" | "not_applicable" | "created" | "reused" | "failed" | "unknown"
    )
}

fn valid_fallback_failure(value: &str) -> bool {
    matches!(
        value,
        "not_evaluated"
            | "not_applicable"
            | "memory_spill_disabled"
            | "attachment_slots_full"
            | "no_safe_candidate"
            | "cannot_fit_inline"
            | "generated_file_too_large"
            | "projection_failed"
            | "graph_authorization_unavailable"
            | "document_upload_failed"
            | "attachment_upload_failed"
            | "local_spool_failure"
            | "attachment_metadata_invalid"
            | "graph_upload_session_transport"
            | "graph_upload_session_http_408"
            | "graph_upload_session_http_429"
            | "graph_upload_session_http_5xx"
            | "graph_upload_session_http_4xx"
            | "graph_upload_session_invalid_json"
            | "untrusted_upload_url"
            | "sharepoint_upload_transport_unknown"
            | "sharepoint_upload_http_408"
            | "sharepoint_upload_http_429"
            | "sharepoint_upload_http_5xx"
            | "sharepoint_upload_http_4xx"
            | "drive_item_invalid_json"
            | "drive_item_incomplete"
            | "reference_validation_failed"
            | "unknown_attachment_transport"
    )
}

fn valid_utf16_class(value: usize, class: &str) -> bool {
    value <= MAX_RECORDED_UTF16
        && ((value == 0 && class == "unknown") || class == utf16_class(value))
}

struct TraceInner {
    store: Store,
    started: Instant,
    record: Mutex<Record>,
}

impl Drop for TraceInner {
    fn drop(&mut self) {
        let mut record = self.record.lock().expect("debug trace poisoned").clone();
        record.duration_ms = self.started.elapsed().as_millis().min(u64::MAX as u128) as u64;
        self.store.push(record);
    }
}

#[derive(Clone)]
pub(crate) struct Trace {
    inner: Arc<TraceInner>,
}

impl Trace {
    fn update(&self, update: impl FnOnce(&mut Record)) {
        update(&mut self.inner.record.lock().expect("debug trace poisoned"));
    }

    #[cfg(test)]
    pub(crate) fn correlation_id(&self) -> String {
        self.inner
            .record
            .lock()
            .expect("debug trace poisoned")
            .correlation_id
            .clone()
    }

    pub(crate) fn request(
        &self,
        request_class: crate::traffic::WorkloadClass,
        provenance_class: ProvenanceClass,
    ) {
        self.update(|record| {
            record.request_class = workload_class(request_class).as_str().to_owned();
            record.provenance_class = provenance_class.as_str().to_owned();
        });
    }

    pub(crate) fn breaker(
        &self,
        state: crate::traffic::CircuitState,
        projection: BreakerProjection,
    ) {
        self.update(|record| {
            record.breaker_state = circuit_state(state).to_owned();
            record.breaker_projection = projection.as_str().to_owned();
        });
    }

    pub(crate) fn breaker_projection(&self, projection: BreakerProjection) {
        self.update(|record| record.breaker_projection = projection.as_str().to_owned());
    }

    pub(crate) fn admission(&self, result: AdmissionResult) {
        self.update(|record| record.admission_result = result.as_str().to_owned());
    }

    pub(crate) fn spill(
        &self,
        decision: SpillDecision,
        reason: SpillReason,
        utf16_before: usize,
        utf16_after: usize,
    ) {
        self.update(|record| {
            record.spill_decision = decision.as_str().to_owned();
            record.spill_reason = reason.as_str().to_owned();
            record.utf16_before = utf16_before.min(MAX_RECORDED_UTF16);
            record.utf16_after = utf16_after.min(MAX_RECORDED_UTF16);
            record.utf16_before_class = utf16_class(utf16_before).to_owned();
            record.utf16_after_class = utf16_class(utf16_after).to_owned();
        });
    }

    #[cfg(test)]
    #[allow(clippy::too_many_arguments)]
    pub(crate) fn transport(
        &self,
        projection: &str,
        wire_before_utf16: usize,
        inline_core_utf16: usize,
        message_text_before_utf16: usize,
        preliminary_message_text_after_utf16: usize,
        message_text_after_utf16: usize,
        wire_after_utf16: usize,
        generated_document_bytes: usize,
        generated_document_message_count: usize,
        generated_document_state: &str,
        fallback_failure: &str,
    ) {
        self.update(|record| {
            record.transport_projection = projection.to_owned();
            record.wire_before_utf16 = wire_before_utf16.min(MAX_RECORDED_UTF16);
            record.inline_core_utf16 = inline_core_utf16.min(MAX_RECORDED_UTF16);
            record.message_text_before_utf16 = message_text_before_utf16.min(MAX_RECORDED_UTF16);
            record.preliminary_message_text_after_utf16 =
                preliminary_message_text_after_utf16.min(MAX_RECORDED_UTF16);
            record.message_text_after_utf16 = message_text_after_utf16.min(MAX_RECORDED_UTF16);
            record.wire_after_utf16 = wire_after_utf16.min(MAX_RECORDED_UTF16);
            record.generated_document_bytes = generated_document_bytes.min(MAX_RECORDED_BYTES);
            record.generated_document_message_count =
                generated_document_message_count.min(MAX_RECORDED_UTF16);
            record.generated_document_state = generated_document_state.to_owned();
            record.fallback_failure = fallback_failure.to_owned();
        });
    }

    #[allow(clippy::too_many_arguments)]
    pub(crate) fn transport_preliminary(
        &self,
        projection: &str,
        wire_before_utf16: usize,
        inline_core_utf16: usize,
        preliminary_wire_after_utf16: usize,
        generated_document_bytes: usize,
        generated_document_message_count: usize,
        generated_document_state: &str,
        fallback_failure: &str,
    ) {
        self.update(|record| {
            record.transport_projection = projection.to_owned();
            record.wire_before_utf16 = wire_before_utf16.min(MAX_RECORDED_UTF16);
            record.inline_core_utf16 = inline_core_utf16.min(MAX_RECORDED_UTF16);
            record.preliminary_wire_after_utf16 =
                preliminary_wire_after_utf16.min(MAX_RECORDED_UTF16);
            record.generated_document_bytes = generated_document_bytes.min(MAX_RECORDED_BYTES);
            record.generated_document_message_count =
                generated_document_message_count.min(MAX_RECORDED_UTF16);
            record.generated_document_state = generated_document_state.to_owned();
            record.fallback_failure = fallback_failure.to_owned();
        });
    }

    pub(crate) fn transport_message_text_preliminary(&self, before: usize, after: usize) {
        self.update(|record| {
            record.message_text_before_utf16 = before.min(MAX_RECORDED_UTF16);
            record.preliminary_message_text_after_utf16 = after.min(MAX_RECORDED_UTF16);
        });
    }

    pub(crate) fn transport_message_text_preliminary_failed(
        &self,
        preliminary_message_text_after_utf16: usize,
        fallback_failure: &str,
    ) {
        self.update(|record| {
            record.transport_projection = "overflow".to_owned();
            record.preliminary_message_text_after_utf16 =
                preliminary_message_text_after_utf16.min(MAX_RECORDED_UTF16);
            record.fallback_failure = fallback_failure.to_owned();
        });
    }

    pub(crate) fn transport_message_text_failed(
        &self,
        message_text_after_utf16: usize,
        fallback_failure: &str,
    ) {
        self.update(|record| {
            record.transport_projection = "overflow".to_owned();
            record.message_text_after_utf16 = message_text_after_utf16.min(MAX_RECORDED_UTF16);
            record.fallback_failure = fallback_failure.to_owned();
        });
    }

    pub(crate) fn transport_message_text_final(&self, message_text_after_utf16: usize) {
        self.update(|record| {
            record.message_text_after_utf16 = message_text_after_utf16.min(MAX_RECORDED_UTF16);
        });
    }

    pub(crate) fn transport_final_wire(&self, wire_after_utf16: usize) {
        self.update(|record| {
            record.wire_after_utf16 = wire_after_utf16.min(MAX_RECORDED_UTF16);
        });
    }

    pub(crate) fn generated_document_reused(&self) {
        self.update(|record| {
            if record.generated_document_state == "created" {
                record.generated_document_state = "reused".to_owned();
            }
        });
    }

    pub(crate) fn generated_document_failed(&self, failure: &str) {
        self.update(|record| {
            if matches!(
                record.generated_document_state.as_str(),
                "created" | "reused"
            ) {
                record.generated_document_state = "failed".to_owned();
                record.fallback_failure = failure.to_owned();
            }
        });
    }

    pub(crate) fn upstream_attempt(&self, class: UpstreamAttempt) {
        self.update(|record| record.upstream_attempt_class = class.as_str().to_owned());
    }

    pub(crate) fn upstream_result(&self, class: UpstreamResult) {
        self.update(|record| record.upstream_result_class = class.as_str().to_owned());
    }

    pub(crate) fn caller_delivery(&self, delivery: CallerDelivery) {
        self.update(|record| record.caller_delivery = delivery.as_str().to_owned());
    }

    pub(crate) fn tool_call_suppressed(&self) {
        self.update(|record| record.tool_call_suppressed = true);
    }

    pub(crate) fn http_status(&self, status: StatusCode) {
        self.update(|record| {
            record.status = status.as_u16();
            record.level = if status.is_server_error() {
                "error"
            } else if status.is_client_error() {
                "warn"
            } else {
                "info"
            }
            .to_owned();
        });
    }
}

#[cfg(test)]
impl Store {
    pub(crate) fn path_for_test(&self) -> Option<PathBuf> {
        self.path.as_deref().cloned()
    }

    pub(crate) fn records_for_test(&self) -> Vec<serde_json::Value> {
        let inner = self.inner.lock().expect("debug store poisoned");
        inner.records.iter().rev().map(public_record).collect()
    }
}

pub(crate) async fn record(
    State(gateway): State<Arc<Gateway>>,
    request: Request,
    next: Next,
) -> Response {
    let method = request.method().to_string();
    let path = request.uri().path().to_owned();
    let started = Instant::now();
    let response = next.run(request).await;
    let status = response.status();
    if !path.starts_with("/api/admin/debug/")
        && response.extensions().get::<TracedResponse>().is_none()
    {
        let mut record = Record::new(&method, &path);
        record.status = status.as_u16();
        record.duration_ms = started.elapsed().as_millis().min(u64::MAX as u128) as u64;
        record.level = if status.is_server_error() {
            "error"
        } else if status.is_client_error() {
            "warn"
        } else {
            "info"
        }
        .to_owned();
        record.request_class = RequestClass::ManagementOrAuxiliary.as_str().to_owned();
        record.spill_reason = SpillReason::NotApplicable.as_str().to_owned();
        gateway.debug.push(record);
    }
    response
}

pub(crate) async fn list(State(gateway): State<Arc<Gateway>>) -> Response {
    let mut inner = gateway.debug.inner.lock().expect("debug store poisoned");
    expire(&mut inner);
    Json(json!({
        "schema": SURFACE_ID,
        "source": {
            "surfaceId": inner.surface_id,
            "kind": "authoritative_jsonl",
            "pathClass": inner.path_class,
            "readerState": inner.reader_state,
            "writerState": inner.writer_state,
        },
        "records": inner.records.iter().map(public_record).collect::<Vec<_>>(),
        "audit": inner.audit,
        "session": inner.session,
    }))
    .into_response()
}

pub(crate) async fn detail(
    State(gateway): State<Arc<Gateway>>,
    Query(query): Query<std::collections::HashMap<String, String>>,
) -> Response {
    let id = query.get("id").map(String::as_str).unwrap_or_default();
    let inner = gateway.debug.inner.lock().expect("debug store poisoned");
    let Some(record) = inner.records.iter().find(|record| record.id == id) else {
        return openai_error(
            StatusCode::NOT_FOUND,
            "not_found",
            "not_found",
            "找不到診斷摘要",
        );
    };
    Json(json!({
        "id": record.id,
        "at": record.at,
        "protocol": record.protocol,
        "route": record.route,
        "method": record.method,
        "path": record.path,
        "status": record.status,
        "durationMs": record.duration_ms,
        "correlationId": record.correlation_id,
        "requestClass": record.request_class,
        "admissionResult": record.admission_result,
        "breakerState": record.breaker_state,
        "breakerProjection": record.breaker_projection,
        "throttleKind": throttle_kind(record),
        "spillDecision": public_spill_decision(record),
        "spillReason": public_spill_reason(record),
        "utf16Before": record.utf16_before,
        "utf16After": record.utf16_after,
        "utf16BeforeClass": record.utf16_before_class,
        "utf16AfterClass": record.utf16_after_class,
        "provenanceClass": record.provenance_class,
        "upstreamAttemptClass": record.upstream_attempt_class,
        "upstreamResultClass": record.upstream_result_class,
        "callerDelivery": record.caller_delivery,
        "toolCallSuppressed": record.tool_call_suppressed,
        "transportProjection": record.transport_projection,
        "wireBeforeUtf16": record.wire_before_utf16,
        "inlineCoreUtf16": record.inline_core_utf16,
        "messageTextBeforeUtf16": record.message_text_before_utf16,
        "preliminaryMessageTextAfterUtf16": record.preliminary_message_text_after_utf16,
        "messageTextAfterUtf16": record.message_text_after_utf16,
        "wireAfterUtf16": record.wire_after_utf16,
        "preliminaryWireAfterUtf16": record.preliminary_wire_after_utf16,
        "generatedDocumentBytes": record.generated_document_bytes,
        "generatedDocumentMessageCount": record.generated_document_message_count,
        "generatedDocumentState": record.generated_document_state,
        "fallbackFailure": record.fallback_failure,
        "requestId": record.request_id,
        "errorCode": record.error_code,
        "inputTokens": record.input_tokens,
        "outputTokens": record.output_tokens,
        "eventCount": record.event_count,
        "snapshotAvailable": false,
        "snapshot": null,
    }))
    .into_response()
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct SessionRequest {
    ttl_seconds: Option<i64>,
}

pub(crate) async fn start_session(
    State(gateway): State<Arc<Gateway>>,
    Json(input): Json<SessionRequest>,
) -> Response {
    let ttl = input.ttl_seconds.unwrap_or(900).clamp(60, 3_600);
    let mut inner = gateway.debug.inner.lock().expect("debug store poisoned");
    let id = random_id();
    inner.session = Session {
        active: true,
        id: id.clone(),
        expires_at: Some(format_time(
            OffsetDateTime::now_utc() + Duration::seconds(ttl),
        )),
    };
    inner.audit.push_front(Audit {
        at: now(),
        action: "session.start".to_owned(),
        result: "ok".to_owned(),
        session_id: id,
        record_id: String::new(),
    });
    Json(&inner.session).into_response()
}

pub(crate) async fn clear_session(State(gateway): State<Arc<Gateway>>) -> Response {
    let mut inner = gateway.debug.inner.lock().expect("debug store poisoned");
    let id = inner.session.id.clone();
    inner.session = Session::default();
    inner.audit.push_front(Audit {
        at: now(),
        action: "session.clear".to_owned(),
        result: "ok".to_owned(),
        session_id: id,
        record_id: String::new(),
    });
    Json(&inner.session).into_response()
}

pub(crate) async fn export(State(gateway): State<Arc<Gateway>>) -> Response {
    let inner = gateway.debug.inner.lock().expect("debug store poisoned");
    Json(json!({
        "schema": SURFACE_ID,
        "source": {
            "surfaceId": inner.surface_id,
            "kind": "authoritative_jsonl",
            "pathClass": inner.path_class,
            "readerState": inner.reader_state,
            "writerState": inner.writer_state,
        },
        "exportedAt": now(),
        "records": inner.records.iter().map(public_record).collect::<Vec<_>>(),
        "audit": inner.audit,
    }))
    .into_response()
}

fn public_record(record: &Record) -> serde_json::Value {
    let mut value = serde_json::to_value(record).expect("typed telemetry is serializable");
    value["spillDecision"] = serde_json::Value::String(public_spill_decision(record).to_owned());
    value["spillReason"] = serde_json::Value::String(public_spill_reason(record).to_owned());
    value["callerDelivery"] = serde_json::Value::String(record.caller_delivery.clone());
    value["toolCallSuppressed"] = serde_json::Value::Bool(record.tool_call_suppressed);
    value["throttleKind"] = serde_json::Value::String(throttle_kind(record).to_owned());
    value["transportProjection"] = serde_json::Value::String(record.transport_projection.clone());
    value["wireBeforeUtf16"] = serde_json::Value::from(record.wire_before_utf16);
    value["inlineCoreUtf16"] = serde_json::Value::from(record.inline_core_utf16);
    value["messageTextBeforeUtf16"] = serde_json::Value::from(record.message_text_before_utf16);
    value["preliminaryMessageTextAfterUtf16"] =
        serde_json::Value::from(record.preliminary_message_text_after_utf16);
    value["messageTextAfterUtf16"] = serde_json::Value::from(record.message_text_after_utf16);
    value["wireAfterUtf16"] = serde_json::Value::from(record.wire_after_utf16);
    value["preliminaryWireAfterUtf16"] =
        serde_json::Value::from(record.preliminary_wire_after_utf16);
    value["generatedDocumentBytes"] = serde_json::Value::from(record.generated_document_bytes);
    value["generatedDocumentMessageCount"] =
        serde_json::Value::from(record.generated_document_message_count);
    value["generatedDocumentState"] =
        serde_json::Value::String(record.generated_document_state.clone());
    value["fallbackFailure"] = serde_json::Value::String(record.fallback_failure.clone());
    value
}

fn public_spill_decision(record: &Record) -> &str {
    if record.transport_projection == "full_context_document" {
        SpillDecision::Performed.as_str()
    } else {
        &record.spill_decision
    }
}

fn public_spill_reason(record: &Record) -> &str {
    if record.transport_projection == "full_context_document" {
        SpillReason::FullContextDocument.as_str()
    } else {
        &record.spill_reason
    }
}

fn throttle_kind(record: &Record) -> &'static str {
    if record.admission_result == "upstream_throttle"
        && record.upstream_result_class == "not_attempted"
    {
        "projected_breaker"
    } else if record.upstream_result_class == "rate_limited_429" {
        if record.breaker_projection == "throttled" {
            "hard_http_429"
        } else {
            "soft_bot_notice"
        }
    } else {
        "none"
    }
}

fn expire(inner: &mut Inner) {
    let expired = inner
        .session
        .expires_at
        .as_deref()
        .and_then(|value| {
            OffsetDateTime::parse(value, &time::format_description::well_known::Rfc3339).ok()
        })
        .is_some_and(|expires| expires <= OffsetDateTime::now_utc());
    if expired {
        inner.session = Session::default();
    }
}

fn protocol(path: &str) -> &'static str {
    if path == "/v1/responses" {
        "responses"
    } else if path == "/v1/messages" {
        "anthropic"
    } else if path.starts_with("/v1/")
        || path.starts_with("/hermes/")
        || path.starts_with("/memory/")
    {
        "openai"
    } else {
        "management"
    }
}

fn route(path: &str) -> &'static str {
    if path.starts_with("/hermes/") {
        "hermes"
    } else if path.starts_with("/memory/") {
        "memory"
    } else if path.starts_with("/v1/") {
        "auxiliary"
    } else {
        "management"
    }
}

fn route_template(path: &str) -> &str {
    match path {
        "/v1/chat/completions"
        | "/hermes/v1/chat/completions"
        | "/memory/v1/chat/completions"
        | "/v1/responses"
        | "/v1/messages"
        | "/v1/images/generations"
        | "/v1/models"
        | "/hermes/v1/models"
        | "/memory/v1/models"
        | "/v1/mcp"
        | "/v1/mcp/sse"
        | "/v1/mcp/message"
        | "/api/health"
        | "/api/version"
        | "/api/update"
        | "/api/chat"
        | "/api/chat/stream"
        | "/api/account"
        | "/api/account/refresh"
        | "/api/account/logout"
        | "/api/conversations"
        | "/api/conversations/delete"
        | "/api/auth/start"
        | "/api/auth/status"
        | "/api/auth/callback"
        | "/api/auth/browser/start"
        | "/api/auth/candidate/chat"
        | "/api/auth/browser/default/start"
        | "/internal/hindsight/webhook"
        | "/debug"
        | "/" => path,
        _ if path.starts_with("/v1/artifacts/") => "/v1/artifacts/{capability}/content",
        _ if path.starts_with("/api/admin/") => "/api/admin/{operation}",
        _ if path.starts_with("/v1/") => "/v1/{unknown}",
        _ if path.starts_with("/hermes/") => "/hermes/{unknown}",
        _ if path.starts_with("/memory/") => "/memory/{unknown}",
        _ if path.starts_with("/api/") => "/api/{unknown}",
        _ => "/{unknown}",
    }
}

fn workload_class(class: crate::traffic::WorkloadClass) -> RequestClass {
    match class {
        crate::traffic::WorkloadClass::ExternalUser => RequestClass::ExternalUser,
        crate::traffic::WorkloadClass::Autonomous => RequestClass::Autonomous,
        crate::traffic::WorkloadClass::ControlPlane => RequestClass::ControlPlane,
        crate::traffic::WorkloadClass::AsyncCompletion => RequestClass::AsyncCompletion,
        crate::traffic::WorkloadClass::Memory => RequestClass::Memory,
    }
}

fn circuit_state(state: crate::traffic::CircuitState) -> &'static str {
    match state {
        crate::traffic::CircuitState::Closed => "CLOSED",
        crate::traffic::CircuitState::Open => "OPEN",
        crate::traffic::CircuitState::HalfOpenReady => "HALF_OPEN_READY",
        crate::traffic::CircuitState::ProbeInFlight => "PROBE_IN_FLIGHT",
        crate::traffic::CircuitState::Recovery => "RECOVERY",
    }
}

fn utf16_class(units: usize) -> &'static str {
    match units {
        0..=31_999 => "small",
        32_000..=95_999 => "medium",
        96_000..=127_999 => "near_limit",
        _ => "over_limit",
    }
}

fn random_id() -> String {
    let mut bytes = [0_u8; 18];
    rand::rng().fill_bytes(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

fn now() -> String {
    format_time(OffsetDateTime::now_utc())
}

fn format_time(value: OffsetDateTime) -> String {
    value
        .format(&time::format_description::well_known::Rfc3339)
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn throttle_kind_is_derived_without_changing_the_durable_record_schema() {
        let mut soft = Record::new("POST", "/hermes/v1/chat/completions");
        soft.upstream_attempt_class = "initial".to_owned();
        soft.upstream_result_class = "rate_limited_429".to_owned();
        soft.breaker_projection = "admitted".to_owned();
        assert_eq!(throttle_kind(&soft), "soft_bot_notice");
        assert_eq!(public_record(&soft)["throttleKind"], "soft_bot_notice");
        assert!(
            serde_json::to_value(&soft)
                .unwrap()
                .get("throttleKind")
                .is_none()
        );

        let mut hard = soft.clone();
        hard.breaker_projection = "throttled".to_owned();
        assert_eq!(throttle_kind(&hard), "hard_http_429");

        let mut projected = Record::new("POST", "/hermes/v1/chat/completions");
        projected.admission_result = "upstream_throttle".to_owned();
        projected.breaker_projection = "throttled".to_owned();
        assert_eq!(throttle_kind(&projected), "projected_breaker");
    }

    #[test]
    fn full_context_spill_relation_is_non_memory_and_reducing() {
        assert!(valid_spill_relation(
            "hermes",
            "none",
            "performed",
            "full_context_document",
            200,
            100,
        ));
        assert!(!valid_spill_relation(
            "memory",
            "none",
            "performed",
            "full_context_document",
            200,
            100,
        ));
        assert!(!valid_spill_relation(
            "hermes",
            "none",
            "performed",
            "full_context_document",
            100,
            100,
        ));
    }

    #[test]
    fn authoritative_telemetry_surface_is_durable_and_typed() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let store = Store::open(path.clone(), "data_dir_default").unwrap();
        let trace = store.start_request("POST", "/hermes/v1/chat/completions");
        trace.request(
            crate::traffic::WorkloadClass::ExternalUser,
            ProvenanceClass::AuthenticatedEphemeralRecall,
        );
        trace.breaker(
            crate::traffic::CircuitState::Closed,
            BreakerProjection::Admitted,
        );
        trace.admission(AdmissionResult::Admitted);
        trace.spill(
            SpillDecision::Performed,
            SpillReason::RecalledSourceMaterial,
            128_736,
            636,
        );
        trace.upstream_attempt(UpstreamAttempt::Initial);
        trace.upstream_result(UpstreamResult::Success);
        trace.http_status(StatusCode::OK);
        let correlation_id = trace.correlation_id();
        drop(trace);

        let raw = std::fs::read_to_string(&path).unwrap();
        assert!(raw.contains("m365-privacy-telemetry/v1"));
        assert!(!raw.contains("<memory-context>"));
        let reopened = Store::open(path, "data_dir_default").unwrap();
        let inner = reopened.inner.lock().unwrap();
        let record = inner.records.front().unwrap();
        assert_eq!(record.correlation_id, correlation_id);
        assert_eq!(record.request_class, "external_user");
        assert_eq!(record.spill_decision, "performed");
        assert_eq!(record.utf16_before, 128_736);
        assert_eq!(record.utf16_after, 636);
        assert_eq!(inner.surface_id, "m365-privacy-telemetry/v1");
        assert_eq!(inner.path_class, "data_dir_default");
        drop(inner);
        assert!(
            Store::open(root.path().join("log.db"), "legacy").is_err(),
            "the stale SQLite surface must never become current truth"
        );
    }

    #[test]
    fn telemetry_never_persists_dynamic_path_capabilities() {
        let capability = "REPLAYABLE-CAPABILITY-SENTINEL";
        let record = Record::new("GET", &format!("/v1/artifacts/{capability}/content"));
        let encoded = serde_json::to_string(&record).unwrap();
        assert_eq!(record.path, "/v1/artifacts/{capability}/content");
        assert!(!encoded.contains(capability));
    }

    #[test]
    fn live_outcome_projection_does_not_extend_the_v1_durable_record() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let store = Store::open(path.clone(), "test").unwrap();
        let trace = store.start_request("POST", "/hermes/v1/chat/completions");
        trace.caller_delivery(CallerDelivery::Failed);
        trace.tool_call_suppressed();
        drop(trace);
        drop(store);

        let raw = std::fs::read_to_string(&path).unwrap();
        let value: serde_json::Value = serde_json::from_str(raw.lines().next().unwrap()).unwrap();
        assert_eq!(value["schema"], SURFACE_ID);
        assert!(value.get("callerDelivery").is_none());
        assert!(value.get("toolCallSuppressed").is_none());

        // This is the exact v1 record shape an older rollback reader sees.
        let reopened = Store::open(path.clone(), "test").unwrap();
        let record = reopened
            .inner
            .lock()
            .unwrap()
            .records
            .front()
            .unwrap()
            .clone();
        assert_eq!(record.caller_delivery, "not_evaluated");
        assert!(!record.tool_call_suppressed);
    }

    #[test]
    fn transport_projection_is_visible_live_without_breaking_v1_jsonl() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let store = Store::open(path.clone(), "test").unwrap();
        let trace = store.start_request("POST", "/hermes/v1/chat/completions");
        trace.spill(
            SpillDecision::Performed,
            SpillReason::FullContextDocument,
            185_439,
            82_045,
        );
        trace.transport(
            "full_context_document",
            185_439,
            39_017,
            185_439,
            82_045,
            82_045,
            82_045,
            12_345,
            50,
            "created",
            "not_applicable",
        );
        assert!(
            store.records_for_test().is_empty(),
            "the trace is not durable until it is dropped"
        );
        drop(trace);

        let raw = std::fs::read_to_string(&path).unwrap();
        let durable: serde_json::Value = serde_json::from_str(raw.lines().next().unwrap()).unwrap();
        assert!(durable.get("transportProjection").is_none());
        assert!(durable.get("messageTextBeforeUtf16").is_none());
        assert!(durable.get("preliminaryMessageTextAfterUtf16").is_none());
        assert!(durable.get("messageTextAfterUtf16").is_none());
        assert!(durable.get("wireBeforeUtf16").is_none());
        assert_eq!(durable["spillDecision"], "performed");
        assert_eq!(durable["spillReason"], "full_context_document");
        assert_eq!(durable["utf16Before"], 185_439);
        assert_eq!(durable["utf16After"], 82_045);

        let live = store.records_for_test().pop().unwrap();
        assert_eq!(live["transportProjection"], "full_context_document");
        assert_eq!(live["wireBeforeUtf16"], 185_439);
        assert_eq!(live["inlineCoreUtf16"], 39_017);
        assert_eq!(live["messageTextBeforeUtf16"], 185_439);
        assert_eq!(live["preliminaryMessageTextAfterUtf16"], 82_045);
        assert_eq!(live["messageTextAfterUtf16"], 82_045);
        assert_eq!(live["wireAfterUtf16"], 82_045);
        assert_eq!(live["generatedDocumentBytes"], 12_345);
        assert_eq!(live["generatedDocumentMessageCount"], 50);
        assert_eq!(live["generatedDocumentState"], "created");

        let reopened = Store::open(path.clone(), "test").unwrap();
        let durable = reopened.inner.lock().unwrap();
        assert_eq!(
            durable.records.front().unwrap().spill_reason,
            "full_context_document"
        );
        drop(durable);
        let reopened = reopened.records_for_test().pop().unwrap();
        assert_eq!(reopened["transportProjection"], "not_evaluated");
        assert_eq!(reopened["wireBeforeUtf16"], 0);

        let failed_trace = store.start_request("POST", "/hermes/v1/chat/completions");
        failed_trace.transport(
            "full_context_document",
            1,
            1,
            1,
            1,
            1,
            1,
            1,
            1,
            "created",
            "not_applicable",
        );
        failed_trace.generated_document_failed("sharepoint_upload_http_5xx");
        drop(failed_trace);
        let failed = store.records_for_test().pop().unwrap();
        assert_eq!(failed["generatedDocumentState"], "failed");
        assert_eq!(failed["fallbackFailure"], "sharepoint_upload_http_5xx");
        let raw = std::fs::read_to_string(&path).unwrap();
        let durable: serde_json::Value = serde_json::from_str(raw.lines().last().unwrap()).unwrap();
        assert_eq!(durable["fallbackFailure"], "sharepoint_upload_http_5xx");
        let reopened = Store::open(path, "test").unwrap();
        let inner = reopened.inner.lock().unwrap();
        assert_eq!(
            inner.records.front().unwrap().fallback_failure,
            "sharepoint_upload_http_5xx"
        );
    }

    #[test]
    fn reader_accepts_frozen_v1_live_projection_fields_but_resets_them() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let mut legacy =
            serde_json::to_value(Record::new("POST", "/hermes/v1/chat/completions")).unwrap();
        legacy["postPolicyDisposition"] = serde_json::json!("allowed");
        legacy["postPolicyReason"] = serde_json::json!("matching_evidence");
        legacy["callerDelivery"] = serde_json::json!("sent");
        legacy["toolCallSuppressed"] = serde_json::json!(true);
        std::fs::write(
            &path,
            format!("{}\n", serde_json::to_string(&legacy).unwrap()),
        )
        .unwrap();

        let reopened = Store::open(path, "test").unwrap();
        let inner = reopened.inner.lock().unwrap();
        let record = inner.records.front().unwrap();
        assert_eq!(record.post_policy_disposition, "not_evaluated");
        assert_eq!(record.post_policy_reason, "not_evaluated");
        assert_eq!(record.caller_delivery, "not_evaluated");
        assert!(!record.tool_call_suppressed);
    }

    #[test]
    fn authoritative_reader_rejects_unknown_fields_and_taxonomy() {
        let root = tempfile::tempdir().unwrap();
        let baseline = serde_json::to_value(Record::new("GET", "/api/version")).unwrap();
        for (name, value) in [
            ("unknown-field", {
                let mut value = baseline.clone();
                value["prompt"] = serde_json::json!("SENSITIVE-SENTINEL");
                value
            }),
            ("unknown-taxonomy", {
                let mut value = baseline.clone();
                value["requestClass"] = serde_json::json!("attacker_defined");
                value
            }),
        ] {
            let path = root.path().join(format!("{name}.jsonl"));
            std::fs::write(
                &path,
                format!("{}\n", serde_json::to_string(&value).unwrap()),
            )
            .unwrap();
            assert!(Store::open(path, "test").is_err(), "accepted {name}");
        }
    }

    #[test]
    fn authoritative_reader_rejects_a_complete_unknown_record_without_newline() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let mut value = serde_json::to_value(Record::new("GET", "/api/version")).unwrap();
        value["prompt"] = serde_json::json!("SENSITIVE-SENTINEL");
        std::fs::write(&path, serde_json::to_string(&value).unwrap()).unwrap();

        assert!(Store::open(path, "test").is_err());
    }

    #[test]
    fn authoritative_reader_rejects_semantically_impossible_classifications() {
        let root = tempfile::tempdir().unwrap();
        let baseline = serde_json::to_value(Record::new("GET", "/api/version")).unwrap();
        for (name, value) in [
            ("performed-but-not-required", {
                let mut value = baseline.clone();
                value["spillDecision"] = serde_json::json!("performed");
                value["spillReason"] = serde_json::json!("not_required");
                value
            }),
            ("success-without-attempt", {
                let mut value = baseline.clone();
                value["upstreamResultClass"] = serde_json::json!("success");
                value
            }),
            ("recall-eligible-without-authenticated-provenance", {
                let mut value =
                    serde_json::to_value(Record::new("POST", "/hermes/v1/chat/completions"))
                        .unwrap();
                value["spillDecision"] = serde_json::json!("eligible");
                value["spillReason"] = serde_json::json!("below_limit");
                value["utf16Before"] = serde_json::json!(100);
                value["utf16After"] = serde_json::json!(100);
                value["utf16BeforeClass"] = serde_json::json!("small");
                value["utf16AfterClass"] = serde_json::json!("small");
                value
            }),
        ] {
            let path = root.path().join(format!("{name}.jsonl"));
            std::fs::write(
                &path,
                format!("{}\n", serde_json::to_string(&value).unwrap()),
            )
            .unwrap();
            assert!(Store::open(path, "test").is_err(), "accepted {name}");
        }
    }

    #[test]
    fn trailing_partial_is_repaired_before_the_next_append() {
        use std::io::Write as _;

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let store = Store::open(path.clone(), "test").unwrap();
        let trace = store.start_request("GET", "/api/version");
        trace.http_status(StatusCode::OK);
        drop(trace);
        drop(store);
        std::fs::OpenOptions::new()
            .append(true)
            .open(&path)
            .unwrap()
            .write_all(b"{\"partial\":")
            .unwrap();

        let reopened = Store::open(path.clone(), "test").unwrap();
        assert_eq!(
            reopened.inner.lock().unwrap().reader_state,
            "trailing_partial_repaired"
        );
        let trace = reopened.start_request("GET", "/api/health");
        trace.http_status(StatusCode::OK);
        drop(trace);
        drop(reopened);

        let final_store = Store::open(path, "test").unwrap();
        assert_eq!(final_store.inner.lock().unwrap().records.len(), 2);
    }

    #[test]
    fn failed_durable_append_never_advances_the_authoritative_projection() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug-telemetry.jsonl");
        let store = Store::open(path.clone(), "test").unwrap();
        std::fs::remove_file(&path).unwrap();
        std::fs::create_dir(&path).unwrap();

        for _ in 0..2 {
            let trace = store.start_request("GET", "/api/version");
            trace.http_status(StatusCode::OK);
            drop(trace);
        }

        let inner = store.inner.lock().unwrap();
        assert_eq!(inner.writer_state, "error");
        assert!(inner.records.is_empty());
    }
}
