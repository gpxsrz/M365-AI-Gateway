use std::{sync::Arc, time::SystemTime};

use axum::{
    body::to_bytes,
    extract::{Request, State},
    http::StatusCode,
    response::{IntoResponse, Response},
};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{error::openai_error, web::Gateway};

const MAX_BODY: usize = 64 << 10;

#[derive(Deserialize)]
struct Event {
    event: String,
    operation_id: String,
    status: String,
    timestamp: String,
}

pub(crate) async fn webhook(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    if gateway.hindsight_webhook_secret.is_empty() {
        return error(
            StatusCode::SERVICE_UNAVAILABLE,
            "configuration_error",
            "Hindsight webhook secret is not configured",
        );
    }
    let event_header = request
        .headers()
        .get("x-hindsight-event")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .trim()
        .to_owned();
    let signature = request
        .headers()
        .get("x-hindsight-signature")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .trim()
        .to_owned();
    let body = match to_bytes(request.into_body(), MAX_BODY).await {
        Ok(body) => body,
        Err(_) => {
            return error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "invalid_request_error",
                "Hindsight webhook payload is too large",
            );
        }
    };
    if !valid_signature(&gateway.hindsight_webhook_secret, &signature, &body) {
        return error(
            StatusCode::UNAUTHORIZED,
            "auth_error",
            "invalid Hindsight webhook signature",
        );
    }
    let event: Event = match serde_json::from_slice(&body) {
        Ok(event) => event,
        Err(_) => {
            return error(
                StatusCode::BAD_REQUEST,
                "invalid_request_error",
                "invalid Hindsight webhook payload",
            );
        }
    };
    let kind = event.event.trim();
    if !event_header.is_empty() && event_header != kind {
        return error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "Hindsight webhook event header does not match payload",
        );
    }
    if !matches!(kind, "retain.completed" | "consolidation.completed") {
        return error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "unsupported Hindsight webhook event",
        );
    }
    let timestamp = match OffsetDateTime::parse(event.timestamp.trim(), &Rfc3339) {
        Ok(timestamp) if !event.operation_id.trim().is_empty() => timestamp,
        _ => {
            return error(
                StatusCode::BAD_REQUEST,
                "invalid_request_error",
                "Hindsight webhook operation_id and timestamp are required",
            );
        }
    };
    gateway.traffic.observe_hindsight_event(
        kind,
        event.operation_id.trim(),
        event.status.trim() == "completed",
        SystemTime::from(timestamp),
    );
    StatusCode::NO_CONTENT.into_response()
}

fn error(status: StatusCode, kind: &str, message: &str) -> Response {
    openai_error(status, kind, kind, message)
}

fn valid_signature(secret: &str, provided: &str, payload: &[u8]) -> bool {
    let expected = format!("sha256={}", hex(&hmac_sha256(secret.as_bytes(), payload)));
    provided.len() == expected.len()
        && provided
            .bytes()
            .zip(expected.bytes())
            .fold(0_u8, |difference, (left, right)| {
                difference | (left ^ right)
            })
            == 0
}

fn hmac_sha256(key: &[u8], payload: &[u8]) -> [u8; 32] {
    let mut normalized = [0_u8; 64];
    if key.len() > normalized.len() {
        normalized[..32].copy_from_slice(&Sha256::digest(key));
    } else {
        normalized[..key.len()].copy_from_slice(key);
    }
    let mut inner_pad = [0x36_u8; 64];
    let mut outer_pad = [0x5c_u8; 64];
    for index in 0..normalized.len() {
        inner_pad[index] ^= normalized[index];
        outer_pad[index] ^= normalized[index];
    }
    let inner = Sha256::new()
        .chain_update(inner_pad)
        .chain_update(payload)
        .finalize();
    Sha256::new()
        .chain_update(outer_pad)
        .chain_update(inner)
        .finalize()
        .into()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}
