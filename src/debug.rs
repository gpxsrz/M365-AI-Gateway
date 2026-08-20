use std::{
    collections::VecDeque,
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

use crate::{error::openai_error, web::Gateway};

const MAX_RECORDS: usize = 1_000;

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct Record {
    id: String,
    at: String,
    level: &'static str,
    protocol: &'static str,
    route: &'static str,
    method: String,
    path: String,
    status: u16,
    duration_ms: u64,
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

#[derive(Default)]
struct Inner {
    records: VecDeque<Record>,
    audit: VecDeque<Audit>,
    session: Session,
}

#[derive(Clone, Default)]
pub(crate) struct Store {
    inner: Arc<Mutex<Inner>>,
}

impl Store {
    fn push(&self, record: Record) {
        let mut inner = self.inner.lock().expect("debug store poisoned");
        expire(&mut inner);
        inner.records.push_front(record);
        inner.records.truncate(MAX_RECORDS);
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
    if !path.starts_with("/api/admin/debug/") {
        gateway.debug.push(Record {
            id: random_id(),
            at: now(),
            level: if status.is_server_error() {
                "error"
            } else if status.is_client_error() {
                "warn"
            } else {
                "info"
            },
            protocol: protocol(&path),
            route: route(&path),
            method,
            path,
            status: status.as_u16(),
            duration_ms: started.elapsed().as_millis().min(u64::MAX as u128) as u64,
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
        });
    }
    response
}

pub(crate) async fn list(State(gateway): State<Arc<Gateway>>) -> Response {
    let mut inner = gateway.debug.inner.lock().expect("debug store poisoned");
    expire(&mut inner);
    Json(json!({
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
        "schema": "m365-debug-redacted/rust-v1",
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
