use std::sync::Arc;

use axum::{
    Json,
    body::to_bytes,
    extract::{Request, State},
    http::{HeaderValue, StatusCode, header},
    response::{IntoResponse, Response},
};
use serde::Deserialize;
use serde_json::{Value, json};
use time::OffsetDateTime;

use crate::{
    chathub::{Account, ChatError, ChatRequest, StreamEvent},
    error::openai_error,
    web::Gateway,
};

#[derive(Deserialize)]
struct ImageRequest {
    prompt: String,
    #[serde(default)]
    n: usize,
    #[serde(default)]
    size: String,
    #[serde(default)]
    response_format: String,
}

pub(crate) async fn generations(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let body = match to_bytes(request.into_body(), 16 * 1024 * 1024).await {
        Ok(body) => body,
        Err(_) => return invalid("request body is too large"),
    };
    let body = match serde_json::from_slice::<ImageRequest>(&body) {
        Ok(body) if !body.prompt.trim().is_empty() => body,
        _ => return invalid("prompt is required"),
    };
    if body.prompt.encode_utf16().count() > gateway.settings.current().text_input_limit_utf16 {
        return openai_error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "text_policy_exceeded",
            "輸入文字超過目前上限",
        );
    }
    let count = body.n.max(1);
    if count > 4 {
        return invalid("n must be between 1 and 4");
    }
    if !body.response_format.is_empty()
        && !matches!(
            body.response_format.to_ascii_lowercase().as_str(),
            "url" | "b64_json"
        )
    {
        return invalid("response_format must be url or b64_json");
    }
    let Some(stored) = gateway.tokens.first() else {
        return openai_error(
            StatusCode::BAD_REQUEST,
            "account_not_found",
            "account_not_found",
            "尚未登入 Microsoft 帳號",
        );
    };
    let stored = match gateway.tokens.ensure_valid(&stored.id).await {
        Ok(account) => account,
        Err(_) => {
            return openai_error(
                StatusCode::BAD_GATEWAY,
                "token_refresh_error",
                "token_refresh_error",
                "Microsoft 帳號權杖無法使用",
            );
        }
    };
    if stored.oid.is_empty() || stored.tid.is_empty() {
        return openai_error(
            StatusCode::BAD_REQUEST,
            "account_identity_error",
            "account_identity_error",
            "Microsoft 帳號缺少必要身分資訊",
        );
    }
    let size = if body.size.is_empty() {
        "1024x1024"
    } else {
        body.size.as_str()
    };
    let chat = ChatRequest {
        text: format!(
            "Generate an image with the Flux model. Size: {size}. Description: {}. Return the image URL directly.",
            body.prompt
        ),
        tone: "magic".to_owned(),
        ..ChatRequest::default()
    };
    let account = Account {
        access_token: stored.access_token,
        graph_access_token: String::new(),
        oid: stored.oid,
        tid: stored.tid,
    };
    let mut sink = |_: StreamEvent| Ok(());
    let result = match tokio::time::timeout(
        std::time::Duration::from_secs(gateway.settings.current().image_timeout_seconds),
        gateway.chat.chat(account, chat, &mut sink),
    )
    .await
    {
        Ok(Ok(result)) => result,
        Ok(Err(error)) => return chat_error(error),
        Err(_) => {
            return openai_error(
                StatusCode::GATEWAY_TIMEOUT,
                "upstream_error",
                "upstream_timeout",
                "image generation timed out",
            );
        }
    };
    let mut images = result.images;
    if images.is_empty() {
        images = extract_image_urls(&result.raw_result);
    }
    if images.is_empty() {
        images = extract_image_urls(&result.text);
    }
    if images.is_empty() {
        return openai_error(
            StatusCode::BAD_GATEWAY,
            "upstream_error",
            "no_image_resource",
            "upstream returned no image resource",
        );
    }
    images.truncate(count);
    let data = if body.response_format.eq_ignore_ascii_case("b64_json") {
        let Some(encoded) = images
            .iter()
            .map(|image| {
                image
                    .strip_prefix("data:image/")
                    .and_then(|value| value.split_once(','))
                    .map(|(_, data)| data)
            })
            .collect::<Option<Vec<_>>>()
        else {
            return openai_error(
                StatusCode::BAD_GATEWAY,
                "unsupported_response_format",
                "unsupported_response_format",
                "upstream returned URL, not b64_json",
            );
        };
        encoded
            .into_iter()
            .map(|value| json!({"b64_json": value}))
            .collect::<Vec<_>>()
    } else {
        images
            .iter()
            .map(|value| json!({"url": value}))
            .collect::<Vec<_>>()
    };
    Json(json!({
        "created": OffsetDateTime::now_utc().unix_timestamp(),
        "data": data,
        "m365": {
            "conversationId": result.conversation_id,
            "sessionId": result.session_id,
            "images": images,
        }
    }))
    .into_response()
}

fn invalid(message: &str) -> Response {
    openai_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "invalid_request_error",
        message,
    )
}

fn chat_error(error: ChatError) -> Response {
    match error {
        ChatError::RateLimited { retry_after, .. } => {
            let mut response = openai_error(
                StatusCode::TOO_MANY_REQUESTS,
                "rate_limit_error",
                "upstream_throttle",
                "ChatHub rate limited",
            );
            if let Some(retry_after) = retry_after
                && let Ok(value) = HeaderValue::from_str(&retry_after)
            {
                response.headers_mut().insert(header::RETRY_AFTER, value);
            }
            response
        }
        error => openai_error(
            StatusCode::BAD_GATEWAY,
            "upstream_error",
            "upstream_error",
            &error.to_string(),
        ),
    }
}

fn extract_image_urls(raw: &str) -> Vec<String> {
    fn walk(value: &Value, images: &mut Vec<String>) {
        match value {
            Value::Array(values) => values.iter().for_each(|value| walk(value, images)),
            Value::Object(values) => values.iter().for_each(|(key, value)| {
                if matches!(
                    key.to_ascii_lowercase().as_str(),
                    "url" | "imageurl" | "thumbnailurl" | "downloadurl" | "src" | "value" | "data"
                ) && let Some(url) = value.as_str()
                    && url.starts_with("https://")
                    && !url.contains("/v1/artifacts/")
                    && (url.to_ascii_lowercase().contains("image")
                        || [".png", ".jpg", ".jpeg", ".webp", ".gif"]
                            .iter()
                            .any(|suffix| url.to_ascii_lowercase().ends_with(suffix)))
                    && !images.iter().any(|known| known == url)
                {
                    images.push(url.to_owned());
                } else {
                    walk(value, images);
                }
            }),
            _ => {}
        }
    }

    let mut images = Vec::new();
    if let Ok(value) = serde_json::from_str(raw) {
        walk(&value, &mut images);
    }
    images
}
