use axum::{
    Json,
    http::StatusCode,
    response::{IntoResponse, Response},
};
use serde::Serialize;

#[derive(Debug, thiserror::Error)]
pub enum GatewayError {
    #[error("{0}")]
    Configuration(String),
    #[error("{0}")]
    Storage(String),
    #[error("{0}")]
    InvalidRequest(String),
}
#[derive(Serialize)]
struct ErrorEnvelope<'a> {
    error: ErrorBody<'a>,
}

#[derive(Serialize)]
struct ErrorBody<'a> {
    message: &'a str,
    #[serde(rename = "type")]
    kind: &'a str,
    code: &'a str,
}

impl IntoResponse for GatewayError {
    fn into_response(self) -> Response {
        let (status, kind, code) = match self {
            Self::InvalidRequest(_) => (
                StatusCode::BAD_REQUEST,
                "invalid_request_error",
                "invalid_request",
            ),
            Self::Configuration(_) | Self::Storage(_) => (
                StatusCode::INTERNAL_SERVER_ERROR,
                "server_error",
                "internal_error",
            ),
        };
        let message = self.to_string();
        openai_error(status, kind, code, &message)
    }
}

pub fn openai_error(status: StatusCode, kind: &str, code: &str, message: &str) -> Response {
    (
        status,
        Json(ErrorEnvelope {
            error: ErrorBody {
                message,
                kind,
                code,
            },
        }),
    )
        .into_response()
}
