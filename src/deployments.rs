use std::{
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    time::Instant,
};

use axum::{
    Json,
    body::to_bytes,
    extract::{Query, Request, State},
    http::StatusCode,
    response::{IntoResponse, Response},
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use serde_json::json;
use time::OffsetDateTime;
use url::Url;

use crate::{error::openai_error, private_file, web::Gateway};

const API_BASE: &str = "https://api.cloudflare.com/client/v4";

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct Deployment {
    id: String,
    provider: String,
    name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    account_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    default_url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    custom_url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    active_url: String,
    status: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    latency_ms: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    last_checked_at: Option<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    last_error: String,
}

#[derive(Default, Deserialize, Serialize)]
struct File {
    items: Vec<Deployment>,
}

#[derive(Clone)]
pub(crate) struct Store {
    path: PathBuf,
    items: Arc<Mutex<Vec<Deployment>>>,
}

impl Store {
    pub(crate) fn open(data_dir: &Path) -> Result<Self, crate::error::GatewayError> {
        let path = data_dir.join("deployments.json");
        let items = private_file::read_json::<File>(&path)?
            .unwrap_or_default()
            .items;
        Ok(Self {
            path,
            items: Arc::new(Mutex::new(items)),
        })
    }

    fn save(&self) -> Result<(), crate::error::GatewayError> {
        let items = self
            .items
            .lock()
            .expect("deployment store poisoned")
            .clone();
        private_file::write_json(&self.path, &File { items })
    }
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct CreateRequest {
    provider: String,
    account_id: String,
    name: String,
    token: String,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct UpdateRequest {
    custom_url: String,
}

pub(crate) async fn list_or_create(
    State(gateway): State<Arc<Gateway>>,
    request: Request,
) -> Response {
    if request.method() == axum::http::Method::GET {
        let items = gateway
            .deployments
            .items
            .lock()
            .expect("deployment store poisoned")
            .clone();
        return Json(json!({"items": items})).into_response();
    }
    let input = match read::<CreateRequest>(request, 64 * 1024).await {
        Ok(input) => input,
        Err(response) => return response,
    };
    if input.provider != "cloudflare" {
        return invalid("目前僅支援 Cloudflare");
    }
    let deployment = match deploy_cloudflare(input).await {
        Ok(deployment) => deployment,
        Err(message) => {
            return openai_error(
                StatusCode::BAD_REQUEST,
                "deployment_error",
                "deployment_error",
                &message,
            );
        }
    };
    gateway
        .deployments
        .items
        .lock()
        .expect("deployment store poisoned")
        .push(deployment.clone());
    if let Err(error) = gateway.deployments.save() {
        return storage(&error.to_string());
    }
    Json(json!({"ok": true, "deployment": deployment})).into_response()
}

pub(crate) async fn action(
    State(gateway): State<Arc<Gateway>>,
    Query(query): Query<std::collections::HashMap<String, String>>,
    request: Request,
) -> Response {
    let id = query.get("id").map(String::as_str).unwrap_or_default();
    let input = match read::<UpdateRequest>(request, 64 * 1024).await {
        Ok(input) => input,
        Err(response) => return response,
    };
    let custom_url = input.custom_url.trim().trim_end_matches('/').to_owned();
    if !custom_url.is_empty() && !valid_target(&custom_url) {
        return invalid("自訂網域必須是安全的 HTTP(S) URL");
    }
    let output = {
        let mut items = gateway
            .deployments
            .items
            .lock()
            .expect("deployment store poisoned");
        let Some(item) = items.iter_mut().find(|item| item.id == id) else {
            return not_found();
        };
        item.custom_url = custom_url;
        item.active_url = if item.custom_url.is_empty() {
            item.default_url.clone()
        } else {
            item.custom_url.clone()
        };
        item.clone()
    };
    if let Err(error) = gateway.deployments.save() {
        return storage(&error.to_string());
    }
    Json(json!({"ok": true, "deployment": output})).into_response()
}

pub(crate) async fn check(
    State(gateway): State<Arc<Gateway>>,
    Query(query): Query<std::collections::HashMap<String, String>>,
) -> Response {
    let id = query.get("id").map(String::as_str).unwrap_or_default();
    let target = {
        let items = gateway
            .deployments
            .items
            .lock()
            .expect("deployment store poisoned");
        let Some(item) = items.iter().find(|item| item.id == id) else {
            return not_found();
        };
        if item.active_url.is_empty() {
            item.default_url.clone()
        } else {
            item.active_url.clone()
        }
    };
    if !valid_target(&target) {
        return invalid("部署節點 URL 無效");
    }
    let started = Instant::now();
    let result = reqwest::Client::new()
        .get(format!("{}/health", target.trim_end_matches('/')))
        .timeout(std::time::Duration::from_secs(15))
        .send()
        .await;
    let output = {
        let mut items = gateway
            .deployments
            .items
            .lock()
            .expect("deployment store poisoned");
        let Some(item) = items.iter_mut().find(|item| item.id == id) else {
            return not_found();
        };
        item.latency_ms = started.elapsed().as_millis().min(u64::MAX as u128) as u64;
        item.last_checked_at = Some(
            OffsetDateTime::now_utc()
                .format(&time::format_description::well_known::Rfc3339)
                .unwrap_or_default(),
        );
        match result {
            Ok(response) if response.status() == StatusCode::OK => {
                item.status = "healthy".to_owned();
                item.last_error.clear();
            }
            Ok(response) => {
                item.status = "unhealthy".to_owned();
                item.last_error = format!("健康檢查回傳 {}", response.status());
            }
            Err(_) => {
                item.status = "unhealthy".to_owned();
                item.last_error = "健康檢查無法連線".to_owned();
            }
        }
        item.clone()
    };
    let _ = gateway.deployments.save();
    Json(json!({"ok": output.status == "healthy", "deployment": output})).into_response()
}

async fn deploy_cloudflare(input: CreateRequest) -> Result<Deployment, String> {
    if input.account_id.trim().is_empty()
        || input.token.trim().is_empty()
        || !valid_worker_name(&input.name)
    {
        return Err("Account ID、有效 Worker 名稱與 API Token 皆不得為空".to_owned());
    }
    let client = reqwest::Client::new();
    let account = percent(&input.account_id);
    let name = percent(&input.name);
    let response = client
        .put(format!(
            "{API_BASE}/accounts/{account}/workers/scripts/{name}"
        ))
        .bearer_auth(&input.token)
        .header("content-type", "application/javascript")
        .body("export default {async fetch(request){if(new URL(request.url).pathname==='/health')return new Response('ok');return new Response('m365-native worker relay is configured')}}")
        .send()
        .await
        .map_err(|_| "Cloudflare 部署無法連線".to_owned())?;
    if !response.status().is_success() {
        return Err(format!("Cloudflare 部署失敗（HTTP {}）", response.status()));
    }
    let response = client
        .get(format!("{API_BASE}/accounts/{account}/workers/subdomain"))
        .bearer_auth(&input.token)
        .send()
        .await
        .map_err(|_| "Cloudflare 子網域查詢無法連線".to_owned())?;
    if !response.status().is_success() {
        return Err(format!(
            "Cloudflare 子網域查詢失敗（HTTP {}）",
            response.status()
        ));
    }
    let value: serde_json::Value = response
        .json()
        .await
        .map_err(|_| "Cloudflare 子網域回應格式錯誤".to_owned())?;
    let subdomain = value["result"]["subdomain"]
        .as_str()
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| "Cloudflare 未回傳 workers.dev 子網域".to_owned())?;
    let default_url = format!("https://{}.{}.workers.dev", input.name, subdomain);
    Ok(Deployment {
        id: format!("cf-{}", random_id()),
        provider: input.provider,
        name: input.name,
        account_id: input.account_id,
        default_url: default_url.clone(),
        custom_url: String::new(),
        active_url: default_url,
        status: "deployed".to_owned(),
        latency_ms: 0,
        last_checked_at: None,
        last_error: String::new(),
    })
}

async fn read<T: for<'de> Deserialize<'de>>(request: Request, limit: usize) -> Result<T, Response> {
    let bytes = to_bytes(request.into_body(), limit)
        .await
        .map_err(|_| invalid("JSON 格式錯誤"))?;
    serde_json::from_slice(&bytes).map_err(|_| invalid("JSON 格式錯誤"))
}

fn valid_worker_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 64
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
}

fn valid_target(value: &str) -> bool {
    Url::parse(value).is_ok_and(|url| {
        matches!(url.scheme(), "http" | "https")
            && url.has_host()
            && url.username().is_empty()
            && url.password().is_none()
            && url.query().is_none()
            && url.fragment().is_none()
    })
}

fn percent(value: &str) -> String {
    url::form_urlencoded::byte_serialize(value.trim().as_bytes()).collect()
}

fn random_id() -> String {
    let mut bytes = [0_u8; 24];
    rand::rng().fill_bytes(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

fn invalid(message: &str) -> Response {
    openai_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "invalid_request",
        message,
    )
}

fn not_found() -> Response {
    openai_error(
        StatusCode::NOT_FOUND,
        "not_found",
        "not_found",
        "找不到部署項目",
    )
}

fn storage(message: &str) -> Response {
    openai_error(
        StatusCode::INTERNAL_SERVER_ERROR,
        "storage_error",
        "storage_error",
        message,
    )
}

fn is_zero(value: &u64) -> bool {
    *value == 0
}
