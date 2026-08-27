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
    RateLimited429,
    ServiceUnavailable503,
    AttachmentError,
    TerminalError,
    TransportError,
    ProtocolError,
    ContextLength,
    JsonDecode,
}

macro_rules! telemetry_names {
    ($type:ty, {$($variant:path => $name:literal),+ $(,)?}) => {
        impl $type {
            fn as_str(self) -> &'static str {
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
    UpstreamResult::RateLimited429 => "rate_limited_429",
    UpstreamResult::ServiceUnavailable503 => "service_unavailable_503",
    UpstreamResult::AttachmentError => "attachment_error",
    UpstreamResult::TerminalError => "terminal_error",
    UpstreamResult::TransportError => "transport_error",
    UpstreamResult::ProtocolError => "protocol_error",
    UpstreamResult::ContextLength => "context_length",
    UpstreamResult::JsonDecode => "json_decode",
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
                    | "rate_limited_429"
                    | "service_unavailable_503"
                    | "attachment_error"
                    | "terminal_error"
                    | "transport_error"
                    | "protocol_error"
                    | "context_length"
                    | "json_decode"
            )
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

    pub(crate) fn upstream_attempt(&self, class: UpstreamAttempt) {
        self.update(|record| record.upstream_attempt_class = class.as_str().to_owned());
    }

    pub(crate) fn upstream_result(&self, class: UpstreamResult) {
        self.update(|record| record.upstream_result_class = class.as_str().to_owned());
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
        "records": inner.records,
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
        "spillDecision": record.spill_decision,
        "spillReason": record.spill_reason,
        "utf16Before": record.utf16_before,
        "utf16After": record.utf16_after,
        "utf16BeforeClass": record.utf16_before_class,
        "utf16AfterClass": record.utf16_after_class,
        "provenanceClass": record.provenance_class,
        "upstreamAttemptClass": record.upstream_attempt_class,
        "upstreamResultClass": record.upstream_result_class,
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
        "records": inner.records,
        "audit": inner.audit,
    }))
    .into_response()
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
