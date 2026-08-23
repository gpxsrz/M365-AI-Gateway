use std::{
    collections::HashMap,
    env,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::Instant,
};

use axum::{
    Json, Router,
    body::{Body, to_bytes},
    extract::{ConnectInfo, Query, Request, State, connect_info::MockConnectInfo},
    http::{HeaderMap, HeaderValue, Method, StatusCode, header},
    middleware::{self, Next},
    response::{Html, IntoResponse, Response},
    routing::{get, post},
};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;
use url::Url;

use crate::{
    admin::{
        AdminError, AdminRequestInfo, AdminSecurityPolicy, AdminState, management_security_bypass,
        validate_origin,
    },
    api_keys::ApiKeyStore,
    auth::{
        AccountToken, DEFAULT_AUTHORITY, DEFAULT_CLIENT_ID, DEFAULT_REDIRECT_URI, DEFAULT_SCOPE,
        OAuthConfig, TokenSet, TokenStore,
    },
    chathub::{Attachment, ChatHubTransport, LiveChatHub, Tool},
    checkpoint::{CheckpointStore, ClearThenError},
    config::Config,
    error::{GatewayError, openai_error},
    oauth_flow::{AccountView, PkceError, PkceManager, PkceStart},
    traffic::TrafficController,
};

const VERSION: &str = match option_env!("M365_BUILD_VERSION") {
    Some(value) => value,
    None => env!("CARGO_PKG_VERSION"),
};
const COMMIT: &str = match option_env!("M365_BUILD_COMMIT") {
    Some(value) => value,
    None => "dev",
};
const BUILD_TIME: &str = match option_env!("M365_BUILD_TIME") {
    Some(value) => value,
    None => "unknown",
};
const ADMIN_COOKIE: &str = "m365_admin_session";
const SESSION_MAX_AGE_SECONDS: i64 = 24 * 60 * 60;
const OAUTH_COMPLETION_PAGE: &str = r#"<!doctype html><html lang="zh-TW"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>M365 AI Gateway 授權完成</title><style>body{font:16px system-ui;text-align:center;padding:15vh 20px;color:#242424}main{max-width:520px;margin:auto}h1{font-size:26px}</style></head><body><main><h1>授權完成</h1><p>Microsoft 帳號已登入，可以關閉此頁面。</p><script>if(window.opener){window.opener.postMessage({type:"m365-auth-complete"},window.location.origin);setTimeout(()=>window.close(),300)}</script></main></body></html>"#;

pub struct Gateway {
    pub(crate) started_at: Instant,
    pub(crate) admin: AdminState,
    pub(crate) admin_security: AdminSecurityPolicy,
    pub(crate) api_keys: ApiKeyStore,
    pub(crate) tokens: TokenStore,
    pub(crate) pkce: PkceManager,
    pub(crate) browser_pkce_active: AtomicBool,
    pub(crate) browser_pkce: Arc<dyn crate::browser_pkce::Runner>,
    pub(crate) oauth_profiles: crate::oauth_profiles::Store,
    pub(crate) chat: Arc<dyn ChatHubTransport>,
    pub(crate) traffic: Arc<TrafficController>,
    pub(crate) settings: crate::runtime_settings::Store,
    pub(crate) settings_lifecycle: Mutex<()>,
    pub(crate) checkpoints: Arc<CheckpointStore>,
    pub(crate) hindsight_webhook_secret: String,
    pub(crate) mcp: crate::mcp::Server,
    pub(crate) artifacts: crate::artifact::Store,
    pub(crate) deployments: crate::deployments::Store,
    pub(crate) debug: crate::debug::Store,
    pub(crate) governance: crate::governance::GovernanceStore,
}

#[derive(Clone, Debug)]
pub(crate) struct ApiKeyOwner(pub String);

impl Gateway {
    pub fn open(config: Config) -> Result<Self, GatewayError> {
        std::fs::create_dir_all(&config.data_dir).map_err(|error| {
            GatewayError::Storage(format!("{}: {error}", config.data_dir.display()))
        })?;
        let admin = AdminState::from_env(&config.data_dir)?;
        let admin_security = AdminSecurityPolicy::from_env()?;
        let api_keys = ApiKeyStore::open(api_keys_path(&config.data_dir))?;
        let oauth = OAuthConfig::from_env()
            .map_err(|error| GatewayError::Configuration(error.to_string()))?;
        let tokens = TokenStore::open(token_cache_path(&config.data_dir), oauth)
            .map_err(|error| GatewayError::Storage(error.to_string()))?;
        let oauth_profiles = crate::oauth_profiles::Store::open(tokens.path())?;
        let checkpoints = CheckpointStore::open(config.data_dir.join("transport-checkpoints.json"))
            .map_err(|error| GatewayError::Storage(error.to_string()))?;
        let artifacts = crate::artifact::Store::open(config.data_dir.join("artifacts"))?;
        let deployments = crate::deployments::Store::open(&config.data_dir)?;
        let settings = crate::runtime_settings::Store::open(&config.data_dir, &config)?;
        let governance =
            crate::governance::GovernanceStore::open(config.data_dir.join("agent-governance.json"))
                .map_err(|error| GatewayError::Storage(error.to_string()))?;
        Ok(Self {
            started_at: Instant::now(),
            admin,
            admin_security,
            api_keys,
            tokens,
            pkce: PkceManager::default(),
            browser_pkce_active: AtomicBool::new(false),
            browser_pkce: Arc::new(crate::browser_pkce::LiveRunner),
            oauth_profiles,
            chat: Arc::new(LiveChatHub::new(settings.clone())),
            traffic: TrafficController::new(),
            settings,
            settings_lifecycle: Mutex::new(()),
            checkpoints,
            hindsight_webhook_secret: env::var("M365_HINDSIGHT_WEBHOOK_SECRET")
                .unwrap_or_default()
                .trim()
                .to_owned(),
            mcp: crate::mcp::Server::default(),
            artifacts,
            deployments,
            debug: crate::debug::Store::default(),
            governance,
        })
    }

    pub fn router(gateway: Arc<Self>) -> Router {
        Router::new()
            .route("/api/admin/login", post(Self::admin_login))
            .route("/api/admin/logout", post(Self::admin_logout))
            .route(
                "/api/admin/session",
                get(Self::admin_session).head(Self::admin_session),
            )
            .route(
                "/api/admin/change-password",
                post(Self::admin_change_password),
            )
            .route(
                "/api/admin/keys",
                get(Self::admin_keys)
                    .post(Self::admin_key_create)
                    .delete(Self::admin_key_revoke),
            )
            .route(
                "/api/admin/traffic/recovery",
                post(Self::admin_traffic_recovery),
            )
            .route(
                "/api/admin/settings",
                get(Self::admin_settings).put(Self::admin_settings),
            )
            .route(
                "/api/admin/deployments",
                get(crate::deployments::list_or_create).post(crate::deployments::list_or_create),
            )
            .route(
                "/api/admin/deployment",
                axum::routing::put(crate::deployments::action),
            )
            .route(
                "/api/admin/deployment/check",
                axum::routing::put(crate::deployments::check),
            )
            .route("/api/admin/debug/logs", get(crate::debug::list))
            .route("/api/admin/debug/detail", get(crate::debug::detail))
            .route(
                "/api/admin/debug/session",
                post(crate::debug::start_session).delete(crate::debug::clear_session),
            )
            .route("/api/admin/debug/export", post(crate::debug::export))
            .route("/api/admin/traffic", get(Self::admin_traffic))
            .route(
                "/api/admin/governance/runtime",
                get(Self::admin_governance_runtime),
            )
            .route("/api/health", get(Self::health))
            .route("/api/version", get(Self::version))
            .route("/api/update", get(Self::update))
            .route("/api/chat", post(Self::legacy_chat))
            .route("/api/chat/stream", post(Self::legacy_chat_stream))
            .route("/v1/models", get(crate::protocol::models))
            .route("/hermes/v1/models", get(crate::protocol::models))
            .route("/memory/v1/models", get(crate::protocol::models))
            .route(
                "/v1/chat/completions",
                post(crate::protocol::chat_completions),
            )
            .route(
                "/hermes/v1/chat/completions",
                post(crate::protocol::chat_completions),
            )
            .route(
                "/memory/v1/chat/completions",
                post(crate::protocol::chat_completions),
            )
            .route("/v1/responses", post(crate::compat::responses))
            .route("/v1/messages", post(crate::compat::anthropic_messages))
            .route("/v1/images/generations", post(crate::images::generations))
            .route(
                "/v1/mcp",
                get(crate::mcp::streamable)
                    .head(crate::mcp::streamable)
                    .post(crate::mcp::streamable)
                    .delete(crate::mcp::streamable),
            )
            .route("/v1/mcp/sse", get(crate::mcp::legacy_sse))
            .route("/v1/mcp/message", post(crate::mcp::legacy_message))
            .route(
                "/v1/artifacts/{capability}/content",
                get(crate::artifact::content).head(crate::artifact::content),
            )
            .route("/api/account", get(Self::account_status))
            .route("/api/account/refresh", post(Self::account_refresh))
            .route("/api/account/logout", post(Self::account_logout))
            .route("/api/conversations", get(Self::conversations))
            .route("/api/conversations/delete", post(Self::delete_conversation))
            .route("/api/auth/start", post(Self::auth_start))
            .route(
                "/api/auth/status",
                get(Self::auth_status).head(Self::auth_status),
            )
            .route(
                "/api/auth/callback",
                get(Self::auth_callback).post(Self::auth_callback),
            )
            .route("/api/auth/browser/start", post(Self::auth_browser_start))
            .route("/api/auth/candidate/chat", post(Self::auth_candidate_chat))
            .route(
                "/api/auth/browser/default/start",
                post(Self::auth_browser_default_start),
            )
            .route(
                "/internal/hindsight/webhook",
                post(crate::hindsight::webhook),
            )
            .route("/debug", get(Self::debug_page).head(Self::debug_page))
            .route("/", get(Self::root_page).head(Self::root_page))
            .fallback(Self::not_found)
            .layer(middleware::from_fn_with_state(
                gateway.clone(),
                crate::debug::record,
            ))
            .layer(middleware::from_fn_with_state(
                gateway.clone(),
                Self::request_security,
            ))
            .layer(MockConnectInfo(SocketAddr::new(
                IpAddr::V4(Ipv4Addr::LOCALHOST),
                0,
            )))
            .with_state(gateway)
    }

    async fn request_security(
        State(gateway): State<Arc<Self>>,
        ConnectInfo(peer): ConnectInfo<SocketAddr>,
        mut request: Request,
        next: Next,
    ) -> Response {
        let path = request.uri().path().to_owned();
        if !management_security_bypass(&path) {
            let info = match gateway
                .admin_security
                .inspect(request.headers(), request.uri(), peer)
            {
                Ok(info) => info,
                Err(error) => {
                    return openai_error(
                        StatusCode::FORBIDDEN,
                        "management_security_error",
                        "management_security_error",
                        &error.to_string(),
                    );
                }
            };
            if !info.local_console && gateway.admin.must_change_password() {
                return openai_error(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "management_security_error",
                    "management_security_error",
                    "使用一次性 bootstrap secret 時，非 loopback 管理介面無法使用",
                );
            }
            if let Err(error) = validate_origin(request.method(), request.headers(), &info) {
                return openai_error(
                    StatusCode::FORBIDDEN,
                    "csrf_error",
                    "csrf_error",
                    &error.to_string(),
                );
            }
            request.extensions_mut().insert(info);
        }

        if admin_auth_exempt(&path) {
            return next.run(request).await;
        }
        if protocol_path(&path) {
            if artifact_capability_token(&path) {
                return next.run(request).await;
            }
            if let Some(owner) = authenticate_api_key(&gateway.api_keys, request.headers()) {
                request.extensions_mut().insert(ApiKeyOwner(owner));
                return next.run(request).await;
            }
            return openai_error(
                StatusCode::UNAUTHORIZED,
                "auth_error",
                "auth_error",
                "valid API key required",
            );
        }
        if !gateway.admin.credential_available() {
            return openai_error(
                StatusCode::SERVICE_UNAVAILABLE,
                "configuration_error",
                "configuration_error",
                "管理員憑證無法使用",
            );
        }
        let authenticated = cookie(request.headers(), ADMIN_COOKIE).is_some_and(|token| {
            gateway
                .admin
                .valid_session(token, OffsetDateTime::now_utc())
        });
        if !authenticated {
            return openai_error(
                StatusCode::UNAUTHORIZED,
                "auth_error",
                "auth_error",
                "需要先以管理員身分登入",
            );
        }
        if gateway.admin.must_change_password()
            && path != "/api/admin/change-password"
            && path != "/api/admin/logout"
        {
            return openai_error(
                StatusCode::FORBIDDEN,
                "password_change_required",
                "password_change_required",
                "使用管理主控台前必須先變更管理員密碼",
            );
        }
        next.run(request).await
    }

    async fn admin_login(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let info = request.extensions().get::<AdminRequestInfo>().cloned();
        let secure = info.as_ref().is_some_and(|info| info.secure);
        let ip = info
            .map(|info| info.client_ip.to_string())
            .unwrap_or_else(|| Ipv4Addr::LOCALHOST.to_string());
        let body = match read_json::<LoginRequest>(request, 4_096).await {
            Ok(body) if !body.password.is_empty() => body,
            _ => return admin_error(AdminError::InvalidCredential),
        };
        match gateway
            .admin
            .login(&body.password, &ip, OffsetDateTime::now_utc())
        {
            Ok(login) => {
                let mut response = Json(LoginResponse {
                    status: "authenticated",
                    must_change_password: login.must_change_password,
                })
                .into_response();
                set_cookie(
                    response.headers_mut(),
                    &login.token,
                    secure,
                    SESSION_MAX_AGE_SECONDS,
                );
                response
            }
            Err(error) => admin_error(error),
        }
    }

    async fn admin_logout(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        if let Some(token) = cookie(request.headers(), ADMIN_COOKIE) {
            gateway.admin.logout(token);
        }
        let secure = request
            .extensions()
            .get::<AdminRequestInfo>()
            .is_some_and(|info| info.secure);
        let mut response = Json(StatusResponse {
            status: "logged_out",
        })
        .into_response();
        set_cookie(response.headers_mut(), "", secure, -1);
        response
    }

    async fn admin_session(
        State(gateway): State<Arc<Self>>,
        method: Method,
        headers: HeaderMap,
    ) -> Response {
        let authenticated = cookie(&headers, ADMIN_COOKIE).is_some_and(|token| {
            gateway
                .admin
                .valid_session(token, OffsetDateTime::now_utc())
        });
        let mut response = Json(SessionResponse {
            authenticated,
            must_change_password: authenticated && gateway.admin.must_change_password(),
        })
        .into_response();
        if method == Method::HEAD {
            *response.body_mut() = Body::empty();
        }
        response
    }

    async fn admin_change_password(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let token = cookie(request.headers(), ADMIN_COOKIE)
            .map(str::to_owned)
            .unwrap_or_default();
        let secure = request
            .extensions()
            .get::<AdminRequestInfo>()
            .is_some_and(|info| info.secure);
        let body = match read_json::<ChangePasswordRequest>(request, 4_096).await {
            Ok(body) => body,
            Err(response) => return response,
        };
        match gateway.admin.change_password(
            &token,
            &body.current_password,
            &body.new_password,
            OffsetDateTime::now_utc(),
        ) {
            Ok(()) => {
                let mut response = Json(ChangePasswordResponse {
                    status: "password_changed",
                    reauthenticate: true,
                })
                .into_response();
                set_cookie(response.headers_mut(), "", secure, -1);
                response
            }
            Err(error) => admin_error(error),
        }
    }

    async fn admin_keys(State(gateway): State<Arc<Self>>) -> Response {
        Json(KeysResponse {
            keys: gateway.api_keys.list(),
        })
        .into_response()
    }

    async fn admin_key_create(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let body = match read_json::<CreateKeyRequest>(request, 4_096).await {
            Ok(body) => body,
            Err(response) => return response,
        };
        let name = if body.name.trim().is_empty() {
            "API key".to_owned()
        } else {
            body.name
        };
        match gateway.api_keys.create(name) {
            Ok((record, key)) => Json(CreateKeyResponse { key, record }).into_response(),
            Err(_) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "storage_error",
                "無法建立 API Key",
            ),
        }
    }

    async fn admin_key_revoke(
        State(gateway): State<Arc<Self>>,
        Query(query): Query<HashMap<String, String>>,
    ) -> Response {
        match gateway
            .api_keys
            .revoke(query.get("id").map(String::as_str).unwrap_or_default())
        {
            Ok(true) => Json(StatusResponse { status: "revoked" }).into_response(),
            Ok(false) => openai_error(
                StatusCode::NOT_FOUND,
                "not_found",
                "not_found",
                "找不到 API Key",
            ),
            Err(_) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "storage_error",
                "無法撤銷 API Key",
            ),
        }
    }

    async fn admin_traffic_recovery(
        State(gateway): State<Arc<Self>>,
        request: Request,
    ) -> Response {
        let body = match read_json::<RecoveryRequest>(request, 4_096).await {
            Ok(body) if body.action.trim() == "complete" => body,
            _ => {
                return openai_error(
                    StatusCode::BAD_REQUEST,
                    "invalid_request_error",
                    "invalid_recovery_action",
                    "action must be complete",
                );
            }
        };
        debug_assert_eq!(body.action.trim(), "complete");
        match gateway.traffic.complete_recovery() {
            Ok(()) => Json(serde_json::json!({
                "ok": true,
                "compatibilityTraffic": gateway.traffic.snapshot(),
            }))
            .into_response(),
            Err(message) => openai_error(
                StatusCode::CONFLICT,
                "invalid_state_error",
                "recovery_not_ready",
                message,
            ),
        }
    }

    async fn admin_traffic(State(gateway): State<Arc<Self>>) -> Response {
        Json(serde_json::json!({
            "compatibilityTraffic": gateway.traffic.snapshot(),
        }))
        .into_response()
    }

    async fn admin_governance_runtime(
        State(gateway): State<Arc<Self>>,
        Query(query): Query<GovernanceRuntimeQuery>,
    ) -> Response {
        let request = crate::governance::RuntimeProjectionRequest {
            task_id: query.task_id,
            run_id: query.run_id,
            agent_id: query.agent_id,
            consumer_schema_version: query.schema_version,
            redacted_fields: Vec::new(),
            omitted_fields: Vec::new(),
        };
        match gateway.governance.runtime_projection(request) {
            Ok(Some(projection)) => Json(projection).into_response(),
            Ok(None) => (
                StatusCode::NOT_FOUND,
                Json(serde_json::json!({"error": "governance_projection_not_found"})),
            )
                .into_response(),
            Err(crate::governance::GovernanceError::InvalidIdentity(_)) => (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({"error": "invalid_governance_projection_request"})),
            )
                .into_response(),
            Err(_) => (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(serde_json::json!({"error": "governance_projection_unavailable"})),
            )
                .into_response(),
        }
    }

    async fn admin_settings(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        if request.method() == Method::GET {
            let settings = gateway.settings.current();
            let generic_rounds = crate::runtime_settings::configured_max_tool_rounds(&settings);
            let hermes_rounds =
                crate::runtime_settings::configured_hermes_max_tool_rounds(&settings);
            return Json(serde_json::json!({
                "settings": settings,
                "settingStatus": gateway.settings.setting_status(),
                "compatibilityTraffic": gateway.traffic.snapshot(),
                "checkpointPersistence": {
                    "recordCount": gateway.checkpoints.list().map_or(0, |items| items.len()),
                    "generationSwitchCount": 0,
                    "lastGenerationReusedRecordCount": 0,
                    "lastGenerationWrittenRecordCount": 0,
                    "lastGenerationDurationMs": 0,
                },
                "toolRoundPolicy": {
                    "generic": generic_rounds,
                    "hermes": hermes_rounds,
                    "memory": generic_rounds,
                },
                "codexModels": crate::protocol::model_ids(&gateway),
                "upstreamTones": crate::protocol::upstream_tones(&gateway),
                "chatHubRequestCapabilityBaseline": crate::chathub::request_capability_baseline(),
                "webRequestCapabilityDrift": crate::runtime_settings::request_capability_drift(&settings),
                "restartRequiredFields": [
                    "listenAddress", "configPath", "tokenCachePath", "sessionCachePath",
                    "outboundProxy", "clientId", "authority", "redirectUri", "scope",
                    "debugLogPath"
                ],
            }))
            .into_response();
        }
        let patch = match read_json::<serde_json::Value>(request, 16 * 1024 * 1024).await {
            Ok(patch) if patch.is_object() => patch,
            _ => {
                return openai_error(
                    StatusCode::BAD_REQUEST,
                    "invalid_request_error",
                    "invalid_request",
                    "JSON 格式錯誤",
                );
            }
        };
        let _lifecycle = gateway
            .settings_lifecycle
            .lock()
            .expect("settings lifecycle poisoned");
        let current = gateway.settings.current();
        let mut merged = serde_json::to_value(&current).expect("runtime settings serialize");
        merged
            .as_object_mut()
            .expect("runtime settings are an object")
            .extend(patch.as_object().expect("validated settings patch").clone());
        let mut settings =
            match serde_json::from_value::<crate::runtime_settings::RuntimeSettings>(merged) {
                Ok(settings) => settings,
                Err(_) => {
                    return openai_error(
                        StatusCode::BAD_REQUEST,
                        "invalid_request_error",
                        "invalid_request",
                        "JSON 格式錯誤",
                    );
                }
            };
        if settings.tool_planning_mode.trim().is_empty() {
            settings.tool_planning_mode = current.tool_planning_mode.clone();
        }
        let result = if current.chat_mode == settings.chat_mode {
            gateway.settings.save(settings.clone()).map_err(Ok)
        } else {
            gateway
                .checkpoints
                .clear_then(|| gateway.settings.save(settings.clone()))
                .map_err(Err)
        };
        match result {
            Ok(()) => Json(serde_json::json!({"ok": true, "settings": settings})).into_response(),
            Err(Ok(error)) => settings_error(&error),
            Err(Err(ClearThenError::Clear)) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "transport_checkpoint_clear_failed",
                "無法安全更新聊天模式的連線狀態",
            ),
            Err(Err(ClearThenError::Change(error))) => settings_error(&error),
            Err(Err(ClearThenError::Restore { change, restore })) => {
                let _ = restore;
                settings_error(&change)
            }
        }
    }

    async fn update() -> Response {
        let stable = !VERSION.trim().is_empty() && VERSION != "dev";
        Json(serde_json::json!({
            "current": VERSION,
            "channel": if stable { "stable" } else { "development" },
            "updateAvailable": false,
            "recommendUpdate": false,
            "message": if stable {
                "目前為穩定版，可檢查穩定版更新"
            } else {
                "目前為開發版，不建議更新"
            },
        }))
        .into_response()
    }

    async fn legacy_chat(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        Self::execute_legacy_chat(gateway, request, false).await
    }

    async fn legacy_chat_stream(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        Self::execute_legacy_chat(gateway, request, true).await
    }

    async fn execute_legacy_chat(
        gateway: Arc<Self>,
        request: Request,
        stream_response: bool,
    ) -> Response {
        let artifact_origin = artifact_origin(&request);
        let body = match read_json::<LegacyChatRequest>(request, 16 * 1024 * 1024).await {
            Ok(body) => body,
            Err(response) => return response,
        };
        let text = if body.message.trim().is_empty() {
            body.prompt.trim()
        } else {
            body.message.trim()
        };
        if text.is_empty() && body.attachments.is_empty() {
            return openai_error(
                StatusCode::BAD_REQUEST,
                "invalid_request_error",
                "messages_required",
                "message or attachment required",
            );
        }
        let mut chat = crate::protocol::ChatCompletionRequest {
            model: body.model,
            messages: vec![crate::protocol::OpenAiMessage::text("user", text)],
            conversation_id: body.conversation_id,
            session_id: body.session_id,
            session_key: body.session_key,
            reasoning_effort: body.reasoning_effort,
            tools: body.tools,
            tool_choice: body.tool_choice,
            legacy_attachments: body.attachments,
            ..Default::default()
        };
        chat.stream = false;
        let response = crate::protocol::execute_chat_request(
            gateway,
            "/hermes/v1/chat/completions".to_owned(),
            "legacy-console".to_owned(),
            artifact_origin,
            chat,
        )
        .await;
        if !response.status().is_success() {
            return response;
        }
        let value = match response_value(response).await {
            Ok(value) => value,
            Err(response) => return response,
        };
        let m365 = &value["m365"];
        let text = value["choices"][0]["message"]["content"]
            .as_str()
            .unwrap_or_default();
        let output = serde_json::json!({
            "status": "ok",
            "model": value["model"],
            "text": text,
            "conversationId": m365["conversationId"],
            "sessionId": m365["sessionId"],
            "requestId": m365["requestId"],
            "throttling": m365["throttling"],
            "semanticEvents": m365["semanticEvents"],
            "images": m365["images"],
            "m365": m365,
        });
        if !stream_response {
            return Json(output).into_response();
        }
        let mut response = Body::from(format!("event: done\ndata: {output}\n\n")).into_response();
        response.headers_mut().insert(
            header::CONTENT_TYPE,
            HeaderValue::from_static("text/event-stream"),
        );
        response
            .headers_mut()
            .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
        response
    }

    async fn account_status(State(gateway): State<Arc<Self>>) -> Response {
        Json(AccountResponse {
            account: gateway.tokens.first().as_ref().map(AccountView::from),
        })
        .into_response()
    }

    async fn account_refresh(State(gateway): State<Arc<Self>>) -> Response {
        let Some(account) = gateway.tokens.first() else {
            return openai_error(
                StatusCode::NOT_FOUND,
                "account_not_found",
                "account_not_found",
                "尚未登入 Microsoft 帳號",
            );
        };
        match gateway.tokens.ensure_valid(&account.id).await {
            Ok(account) => Json(RefreshAccountResponse {
                status: "refreshed",
                account: AccountView::from(&account),
            })
            .into_response(),
            Err(_) => openai_error(
                StatusCode::BAD_GATEWAY,
                "token_refresh_error",
                "token_refresh_error",
                "無法重新整理帳號權杖",
            ),
        }
    }

    async fn conversations(State(gateway): State<Arc<Self>>) -> Response {
        match gateway.checkpoints.list() {
            Ok(conversations) => Json(ConversationsResponse { conversations }).into_response(),
            Err(_) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "storage_error",
                "無法讀取對話",
            ),
        }
    }

    async fn delete_conversation(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let body = match read_json::<DeleteConversationRequest>(request, 4_096).await {
            Ok(body) if !body.id.trim().is_empty() => body,
            _ => {
                return openai_error(
                    StatusCode::BAD_REQUEST,
                    "invalid_request_error",
                    "invalid_request",
                    "JSON 格式錯誤",
                );
            }
        };
        match gateway.checkpoints.delete(body.id.trim()) {
            Ok(true) => Json(StatusResponse { status: "deleted" }).into_response(),
            Ok(false) => openai_error(
                StatusCode::NOT_FOUND,
                "not_found",
                "not_found",
                "找不到對話",
            ),
            Err(_) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "storage_error",
                "無法刪除對話",
            ),
        }
    }

    async fn account_logout(State(gateway): State<Arc<Self>>) -> Response {
        let account = gateway.tokens.first();
        let result = gateway.checkpoints.clear_then(|| match account {
            Some(account) => gateway.tokens.delete(&account.id),
            None => Ok(false),
        });
        match result {
            Ok(_) => Json(StatusResponse {
                status: "logged_out",
            })
            .into_response(),
            Err(ClearThenError::Clear) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "transport_checkpoint_clear_failed",
                "無法安全清除帳號的聊天連線狀態",
            ),
            Err(ClearThenError::Change(_) | ClearThenError::Restore { .. }) => openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "storage_error",
                "storage_error",
                "無法刪除帳號",
            ),
        }
    }

    async fn auth_start(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let request = match validate_optional_json_body::<PkceStartRequest>(request).await {
            Ok(request) => request.unwrap_or_default(),
            Err(response) => return response,
        };
        if !request.profile_id.trim().is_empty() && request.stage_active {
            return oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_profile_target_conflict",
                "OAuth 授權目標只能指定一種 profile 模式",
            );
        }
        let now = OffsetDateTime::now_utc();
        discard_abandoned_oauth(&gateway, now);
        let target = if request.stage_active {
            match gateway.oauth_profiles.stage_from_active(&gateway.tokens) {
                Ok((manifest, _)) => Some(manifest),
                Err(_) => {
                    return oauth_error(
                        StatusCode::INTERNAL_SERVER_ERROR,
                        "oauth_candidate_stage_failed",
                        "無法建立候選 OAuth profile",
                    );
                }
            }
        } else if !request.profile_id.trim().is_empty() {
            match gateway.oauth_profiles.open_store(request.profile_id.trim()) {
                Ok((manifest, _)) => Some(manifest),
                Err(_) => {
                    return oauth_error(
                        StatusCode::BAD_REQUEST,
                        "oauth_profile_target_invalid",
                        "無法開啟指定的 OAuth profile",
                    );
                }
            }
        } else {
            None
        };
        let result = match target.as_ref() {
            Some(target) => gateway.pkce.start_target_owned(
                target.oauth.clone(),
                &target.profile_id,
                &target.kind,
                true,
                request.stage_active,
                now,
            ),
            None => gateway.pkce.start(gateway.tokens.config().clone(), now),
        };
        match result {
            Ok(started) => Json(started).into_response(),
            Err(error) => {
                if request.stage_active
                    && let Some(target) = target
                {
                    gateway.oauth_profiles.discard(&target.profile_id);
                }
                pkce_error(error)
            }
        }
    }

    async fn auth_browser_default_start(State(gateway): State<Arc<Self>>) -> Response {
        if let Err(response) = claim_browser_session(&gateway) {
            return *response;
        }
        let now = OffsetDateTime::now_utc();
        discard_abandoned_oauth(&gateway, now);
        let oauth = OAuthConfig {
            client_id: DEFAULT_CLIENT_ID.to_owned(),
            authority: DEFAULT_AUTHORITY.to_owned(),
            redirect_uri: DEFAULT_REDIRECT_URI.to_owned(),
            scope: DEFAULT_SCOPE.to_owned(),
            authorize_endpoint: format!("{DEFAULT_AUTHORITY}/oauth2/v2.0/authorize"),
            token_endpoint: format!("{DEFAULT_AUTHORITY}/oauth2/v2.0/token"),
        };
        let (manifest, _) = match gateway.oauth_profiles.stage(oauth) {
            Ok(value) => value,
            Err(_) => {
                gateway.browser_pkce_active.store(false, Ordering::Release);
                return oauth_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "oauth_candidate_stage_failed",
                    "無法建立候選 OAuth profile",
                );
            }
        };
        match gateway.pkce.start_target_owned(
            manifest.oauth.clone(),
            &manifest.profile_id,
            &manifest.kind,
            true,
            true,
            now,
        ) {
            Ok(started) => {
                launch_browser_capture(
                    Arc::clone(&gateway),
                    &started,
                    Some(manifest.profile_id.clone()),
                );
                Json(serde_json::json!({
                    "status": "browser_pkce_started",
                    "state": started.state,
                    "oauthProfileKind": manifest.kind,
                    "staged": true,
                }))
                .into_response()
            }
            Err(error) => {
                gateway.oauth_profiles.discard(&manifest.profile_id);
                gateway.browser_pkce_active.store(false, Ordering::Release);
                pkce_error(error)
            }
        }
    }

    async fn auth_browser_start(State(gateway): State<Arc<Self>>) -> Response {
        if gateway.tokens.config().redirect_uri != DEFAULT_REDIRECT_URI {
            return oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_browser_redirect_unsupported",
                "自動登入只支援內建 Microsoft 回呼；請改用目前瀏覽器登入",
            );
        }
        if let Err(response) = claim_browser_session(&gateway) {
            return *response;
        }
        let started = match gateway
            .pkce
            .start(gateway.tokens.config().clone(), OffsetDateTime::now_utc())
        {
            Ok(started) => started,
            Err(error) => {
                gateway.browser_pkce_active.store(false, Ordering::Release);
                return pkce_error(error);
            }
        };
        launch_browser_capture(Arc::clone(&gateway), &started, None);
        Json(serde_json::json!({
            "status": "browser_pkce_started",
            "state": started.state,
            "oauthProfileKind": "active",
            "staged": false,
        }))
        .into_response()
    }

    async fn auth_candidate_chat(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let input = match read_json::<CandidateChatRequest>(request, 4_096).await {
            Ok(input) if !input.profile_id.trim().is_empty() => input,
            _ => {
                return openai_error(
                    StatusCode::BAD_REQUEST,
                    "invalid_request_error",
                    "invalid_request",
                    "需要有效的候選 OAuth profile",
                );
            }
        };
        let (manifest, store) = match gateway.oauth_profiles.open_store(input.profile_id.trim()) {
            Ok(value) => value,
            Err(_) => {
                return openai_error(
                    StatusCode::BAD_REQUEST,
                    "oauth_profile_error",
                    "oauth_profile_error",
                    "指定的候選 OAuth profile 無法使用",
                );
            }
        };
        let Some(account) = store.first() else {
            return openai_error(
                StatusCode::BAD_REQUEST,
                "account_not_found",
                "account_not_found",
                "候選 OAuth profile 尚未完成登入",
            );
        };
        let account = match store.ensure_valid(&account.id).await {
            Ok(account) => account,
            Err(_) => {
                return openai_error(
                    StatusCode::BAD_GATEWAY,
                    "token_refresh_error",
                    "token_refresh_error",
                    "候選帳號權杖無法使用",
                );
            }
        };
        if account.oid.is_empty() || account.tid.is_empty() {
            return openai_error(
                StatusCode::BAD_REQUEST,
                "account_identity_error",
                "account_identity_error",
                "候選帳號缺少必要身分資訊",
            );
        }
        let mut sink = |_: crate::chathub::StreamEvent| Ok(());
        let result = tokio::time::timeout(
            std::time::Duration::from_secs(gateway.settings.current().chat_timeout_seconds),
            gateway.chat.chat(
                crate::chathub::Account {
                    access_token: account.access_token,
                    graph_access_token: String::new(),
                    oid: account.oid,
                    tid: account.tid,
                },
                crate::chathub::ChatRequest {
                    text: "Reply with OK only.".to_owned(),
                    tone: "Magic".to_owned(),
                    ..Default::default()
                },
                &mut sink,
            ),
        )
        .await;
        if !matches!(result, Ok(Ok(ref output)) if !output.text.trim().is_empty()) {
            return openai_error(
                StatusCode::BAD_GATEWAY,
                "upstream_error",
                "upstream_error",
                "候選 ChatHub 驗證失敗",
            );
        }
        if gateway
            .oauth_profiles
            .record_chathub(&manifest.profile_id)
            .is_err()
        {
            return openai_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "oauth_profile_error",
                "oauth_profile_error",
                "無法記錄候選 ChatHub 驗證",
            );
        }
        Json(serde_json::json!({"status": "validated"})).into_response()
    }

    async fn auth_status(
        State(gateway): State<Arc<Self>>,
        method: Method,
        Query(query): Query<HashMap<String, String>>,
    ) -> Response {
        let Some(state) = query.get("state").filter(|state| !state.trim().is_empty()) else {
            return openai_error(
                StatusCode::BAD_REQUEST,
                "invalid_request_error",
                "invalid_request",
                "缺少 state",
            );
        };
        let mut response =
            Json(gateway.pkce.status(state, OffsetDateTime::now_utc())).into_response();
        no_store(response.headers_mut());
        if method == Method::HEAD {
            *response.body_mut() = Body::empty();
        }
        response
    }

    async fn auth_callback(State(gateway): State<Arc<Self>>, request: Request) -> Response {
        let method = request.method().clone();
        let input = match parse_callback(&gateway.pkce, request).await {
            Ok(input) => input,
            Err(response) => return response,
        };
        let claimed = match gateway.pkce.claim(&input.state, OffsetDateTime::now_utc()) {
            Ok(claimed) => claimed,
            Err(error) => {
                if let Some(profile_id) = gateway.pkce.take_discard_target(&input.state) {
                    gateway.oauth_profiles.discard(&profile_id);
                }
                return pkce_error(error);
            }
        };
        if !input.error.is_empty() {
            let cancelled = matches!(input.error.as_str(), "access_denied" | "user_cancelled");
            if cancelled {
                gateway.pkce.cancelled(&input.state);
                discard_failed_oauth(&gateway, &claimed);
                return oauth_error(
                    StatusCode::BAD_REQUEST,
                    "oauth_authorization_cancelled",
                    "Microsoft 授權已取消，請在需要時重新開始授權",
                );
            }
            gateway.pkce.failed(
                &input.state,
                "oauth_authorization_failed",
                "Microsoft 授權失敗，請重新開始授權",
            );
            discard_failed_oauth(&gateway, &claimed);
            return oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_authorization_failed",
                "Microsoft 授權失敗，請重新開始授權",
            );
        }
        let candidate_store = if claimed.staged {
            match gateway.oauth_profiles.open_store(&claimed.profile_id) {
                Ok((_, store)) => Some(store),
                Err(_) => {
                    gateway.pkce.failed(
                        &input.state,
                        "oauth_profile_target_invalid",
                        "候選 OAuth profile 無法使用",
                    );
                    discard_failed_oauth(&gateway, &claimed);
                    return oauth_error(
                        StatusCode::BAD_REQUEST,
                        "oauth_profile_target_invalid",
                        "候選 OAuth profile 無法使用",
                    );
                }
            }
        } else {
            None
        };
        let token_store = candidate_store.as_ref().unwrap_or(&gateway.tokens);
        let token = match token_store
            .exchange_code(&input.code, &claimed.verifier, &claimed.redirect_uri)
            .await
        {
            Ok(token) => token,
            Err(_) => {
                gateway.pkce.failed(
                    &input.state,
                    "oauth_token_exchange_failed",
                    "OAuth 授權碼交換失敗，請重新開始授權",
                );
                discard_failed_oauth(&gateway, &claimed);
                return oauth_error(
                    StatusCode::BAD_GATEWAY,
                    "oauth_token_exchange_failed",
                    "OAuth 授權碼交換失敗，請重新開始授權",
                );
            }
        };
        let account = match store_oauth_token(&gateway, token_store, token, !claimed.staged) {
            Ok(account) => account,
            Err(OAuthTokenStoreError::Checkpoint) => {
                gateway.pkce.failed(
                    &input.state,
                    "transport_checkpoint_clear_failed",
                    "無法安全更新 Microsoft 帳號的聊天連線狀態",
                );
                discard_failed_oauth(&gateway, &claimed);
                return oauth_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "transport_checkpoint_clear_failed",
                    "無法安全更新 Microsoft 帳號的聊天連線狀態",
                );
            }
            Err(OAuthTokenStoreError::Token) => {
                gateway.pkce.failed(
                    &input.state,
                    "oauth_token_store_failed",
                    "無法安全儲存帳號權杖，請檢查資料目錄權限後重試",
                );
                discard_failed_oauth(&gateway, &claimed);
                return oauth_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "oauth_token_store_failed",
                    "無法安全儲存帳號權杖，請檢查資料目錄權限後重試",
                );
            }
        };
        let account_view = AccountView::from(&account);
        gateway
            .pkce
            .authenticated(&input.state, account_view.clone());
        if method == Method::GET {
            let mut response = Html(OAUTH_COMPLETION_PAGE).into_response();
            no_store(response.headers_mut());
            return response;
        }
        let mut response = Json(CallbackResponse {
            status: "authenticated",
            account: account_view,
            oauth_profile_id: if claimed.staged {
                &claimed.profile_id
            } else {
                "legacy"
            },
            oauth_profile_kind: if claimed.staged { "staged" } else { "legacy" },
            staged: claimed.staged,
        })
        .into_response();
        no_store(response.headers_mut());
        response
    }

    async fn health(State(gateway): State<Arc<Self>>) -> Response {
        let account_connected = gateway.tokens.first().is_some();
        Json(HealthResponse {
            status: "ok",
            auth: ["pkce"],
            chat: "chathub",
            client_id: &gateway.tokens.config().client_id,
            scope: &gateway.tokens.config().scope,
            token_cache: gateway.tokens.path().display().to_string(),
            account_connected,
        })
        .into_response()
    }

    async fn version(State(gateway): State<Arc<Self>>) -> Response {
        Json(VersionResponse {
            version: VERSION,
            commit: COMMIT,
            build_time: BUILD_TIME,
            rust: env!("CARGO_PKG_RUST_VERSION"),
            uptime_seconds: gateway.started_at.elapsed().as_secs(),
            account_connected: gateway.tokens.first().is_some(),
        })
        .into_response()
    }

    async fn root_page(
        State(gateway): State<Arc<Self>>,
        method: Method,
        headers: HeaderMap,
    ) -> Response {
        let authenticated = cookie(&headers, ADMIN_COOKIE).is_some_and(|token| {
            gateway
                .admin
                .valid_session(token, OffsetDateTime::now_utc())
        });
        if authenticated && !gateway.admin.must_change_password() {
            static_page(method, include_str!("../web/index.html"))
        } else {
            static_page(method, include_str!("../web/login.html"))
        }
    }

    async fn debug_page(method: Method) -> Response {
        static_page(method, include_str!("../web/debug.html"))
    }

    async fn not_found() -> Response {
        (StatusCode::NOT_FOUND, "not found").into_response()
    }
}

#[derive(Default, Deserialize)]
struct LoginRequest {
    #[serde(default)]
    password: String,
}

#[derive(Serialize)]
struct LoginResponse<'a> {
    status: &'a str,
    must_change_password: bool,
}

#[derive(Serialize)]
struct SessionResponse {
    authenticated: bool,
    must_change_password: bool,
}

#[derive(Default, Deserialize)]
struct ChangePasswordRequest {
    #[serde(default)]
    current_password: String,
    #[serde(default)]
    new_password: String,
}

#[derive(Serialize)]
struct ChangePasswordResponse<'a> {
    status: &'a str,
    reauthenticate: bool,
}

#[derive(Serialize)]
struct StatusResponse<'a> {
    status: &'a str,
}

#[derive(Serialize)]
struct KeysResponse {
    keys: Vec<crate::api_keys::ApiKeyRecord>,
}

#[derive(Deserialize)]
struct CreateKeyRequest {
    #[serde(default)]
    name: String,
}

#[derive(Serialize)]
struct CreateKeyResponse {
    key: String,
    record: crate::api_keys::ApiKeyRecord,
}

#[derive(Default, Deserialize)]
struct RecoveryRequest {
    #[serde(default)]
    action: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct GovernanceRuntimeQuery {
    task_id: String,
    run_id: String,
    agent_id: String,
    schema_version: u32,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct LegacyChatRequest {
    #[serde(default)]
    model: String,
    #[serde(default)]
    message: String,
    #[serde(default)]
    prompt: String,
    #[serde(default)]
    conversation_id: String,
    #[serde(default)]
    session_id: String,
    #[serde(default)]
    session_key: String,
    #[serde(default)]
    attachments: Vec<Attachment>,
    #[serde(default)]
    tools: Vec<Tool>,
    #[serde(default)]
    tool_choice: serde_json::Value,
    #[serde(default)]
    reasoning_effort: String,
}

#[derive(Serialize)]
struct AccountResponse {
    account: Option<AccountView>,
}

#[derive(Serialize)]
struct ConversationsResponse {
    conversations: Vec<crate::checkpoint::CheckpointView>,
}

#[derive(Default, Deserialize)]
struct DeleteConversationRequest {
    #[serde(default)]
    id: String,
}

#[derive(Serialize)]
struct RefreshAccountResponse<'a> {
    status: &'a str,
    account: AccountView,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct PkceStartRequest {
    #[serde(default)]
    profile_id: String,
    #[serde(default)]
    stage_active: bool,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct CandidateChatRequest {
    #[serde(default)]
    profile_id: String,
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct OAuthCallbackInput {
    #[serde(default)]
    callback_url: String,
    #[serde(default)]
    code: String,
    #[serde(default)]
    state: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_description: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct CallbackResponse<'a> {
    status: &'a str,
    account: AccountView,
    oauth_profile_id: &'a str,
    oauth_profile_kind: &'a str,
    staged: bool,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct HealthResponse<'a> {
    status: &'a str,
    auth: [&'a str; 1],
    chat: &'a str,
    client_id: &'a str,
    scope: &'a str,
    token_cache: String,
    account_connected: bool,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct VersionResponse<'a> {
    version: &'a str,
    commit: &'a str,
    build_time: &'a str,
    rust: &'a str,
    uptime_seconds: u64,
    account_connected: bool,
}

async fn read_json<T: for<'de> Deserialize<'de>>(
    request: Request,
    limit: usize,
) -> Result<T, Response> {
    let bytes = to_bytes(request.into_body(), limit).await.map_err(|_| {
        openai_error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "invalid_request",
            "JSON 格式錯誤",
        )
    })?;
    serde_json::from_slice(&bytes).map_err(|_| {
        openai_error(
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "invalid_request",
            "JSON 格式錯誤",
        )
    })
}

async fn response_value(response: Response) -> Result<serde_json::Value, Response> {
    let bytes = to_bytes(response.into_body(), 16 * 1024 * 1024)
        .await
        .map_err(|_| {
            openai_error(
                StatusCode::BAD_GATEWAY,
                "upstream_error",
                "invalid_inner_response",
                "gateway response is too large",
            )
        })?;
    serde_json::from_slice(&bytes).map_err(|_| {
        openai_error(
            StatusCode::BAD_GATEWAY,
            "upstream_error",
            "invalid_inner_response",
            "gateway returned invalid JSON",
        )
    })
}

async fn validate_optional_json_body<T: for<'de> Deserialize<'de>>(
    request: Request,
) -> Result<Option<T>, Response> {
    let content_type = request
        .headers()
        .get(header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .map(|value| {
            value
                .split(';')
                .next()
                .unwrap_or_default()
                .trim()
                .to_owned()
        });
    let bytes = to_bytes(request.into_body(), 16 * 1024)
        .await
        .map_err(|_| {
            oauth_error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "oauth_start_body_too_large",
                "OAuth start body 超過允許大小",
            )
        })?;
    if bytes.is_empty() {
        return Ok(None);
    }
    if content_type.as_deref() != Some("application/json") {
        return Err(oauth_error(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "oauth_start_content_type",
            "OAuth start body 必須使用 application/json",
        ));
    }
    let decoded: T = serde_json::from_slice(&bytes).map_err(|_| {
        oauth_error(
            StatusCode::BAD_REQUEST,
            "oauth_start_invalid_json",
            "OAuth start JSON 格式錯誤或包含未允許欄位",
        )
    })?;
    Ok(Some(decoded))
}

async fn parse_callback(
    pkce: &PkceManager,
    request: Request,
) -> Result<OAuthCallbackInput, Response> {
    let method = request.method().clone();
    let headers = request.headers().clone();
    let uri = request.uri().clone();
    let info = request.extensions().get::<AdminRequestInfo>().cloned();
    let mut input = if method == Method::GET {
        callback_from_url(&format!("http://callback.invalid{}", uri)).map_err(|_| {
            oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_callback_invalid_query",
                "OAuth callback query 格式錯誤",
            )
        })?
    } else {
        if uri.query().is_some() {
            return Err(oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_callback_query_forbidden",
                "手動備援不得把 callback 資料放在 URL query",
            ));
        }
        let content_type = headers
            .get(header::CONTENT_TYPE)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.split(';').next())
            .map(str::trim);
        if content_type != Some("application/json") {
            return Err(oauth_error(
                StatusCode::UNSUPPORTED_MEDIA_TYPE,
                "oauth_callback_content_type",
                "手動備援必須使用 application/json",
            ));
        }
        let bytes = to_bytes(request.into_body(), 16 * 1024)
            .await
            .map_err(|_| {
                oauth_error(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "oauth_callback_body_too_large",
                    "callback 資料超過允許大小",
                )
            })?;
        serde_json::from_slice::<OAuthCallbackInput>(&bytes).map_err(|_| {
            oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_callback_invalid_json",
                "callback JSON 格式錯誤或包含未允許欄位",
            )
        })?
    };
    trim_callback(&mut input);
    if !input.callback_url.is_empty() {
        let from_url = callback_from_url(&input.callback_url).map_err(|_| {
            oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_callback_invalid_url",
                "callback_url 格式錯誤",
            )
        })?;
        merge_callback(&mut input, from_url).map_err(oauth_input_error)?;
        trim_callback(&mut input);
    }
    validate_callback_material(&input).map_err(oauth_input_error)?;
    let redirect = pkce.redirect_uri(&input.state).ok_or_else(|| {
        oauth_error(
            StatusCode::BAD_REQUEST,
            "oauth_state_mismatch",
            "OAuth state 不符合目前的授權工作階段",
        )
    })?;
    if method == Method::GET {
        if !registered_loopback_redirect(&redirect)
            || !loopback_request_matches(&headers, &uri, info.as_ref(), &redirect)
        {
            return Err(oauth_error(
                StatusCode::BAD_REQUEST,
                "oauth_callback_redirect_mismatch",
                "loopback callback 的 scheme、host、port 或 path 不符",
            ));
        }
    } else if !input.callback_url.is_empty()
        && url_identity(&input.callback_url) != url_identity(&redirect)
    {
        return Err(oauth_error(
            StatusCode::BAD_REQUEST,
            "oauth_callback_redirect_mismatch",
            "callback_url 與本次授權設定的 redirect URI 不符",
        ));
    }
    Ok(input)
}

fn callback_from_url(raw: &str) -> Result<OAuthCallbackInput, ()> {
    let url = Url::parse(raw).map_err(|_| ())?;
    if !url.has_host()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
    {
        return Err(());
    }
    let mut input = OAuthCallbackInput::default();
    let mut seen = std::collections::HashSet::new();
    for (name, value) in url.query_pairs() {
        if !matches!(
            name.as_ref(),
            "code" | "state" | "error" | "error_description"
        ) {
            continue;
        }
        if !seen.insert(name.to_string()) {
            return Err(());
        }
        match name.as_ref() {
            "code" => input.code = value.into_owned(),
            "state" => input.state = value.into_owned(),
            "error" => input.error = value.into_owned(),
            "error_description" => input.error_description = value.into_owned(),
            _ => {}
        }
    }
    Ok(input)
}

fn merge_callback(
    destination: &mut OAuthCallbackInput,
    source: OAuthCallbackInput,
) -> Result<(), OAuthInputError> {
    for (target, value) in [
        (&mut destination.code, source.code),
        (&mut destination.state, source.state),
        (&mut destination.error, source.error),
        (&mut destination.error_description, source.error_description),
    ] {
        if value.is_empty() {
            continue;
        }
        if !target.is_empty() && *target != value {
            return Err(OAuthInputError {
                status: StatusCode::BAD_REQUEST,
                code: "oauth_callback_material_conflict",
                message: "callback_url 與 JSON 欄位內容不一致",
            });
        }
        *target = value;
    }
    Ok(())
}

fn trim_callback(input: &mut OAuthCallbackInput) {
    input.callback_url = input.callback_url.trim().to_owned();
    input.code = input.code.trim().to_owned();
    input.state = input.state.trim().to_owned();
    input.error = input.error.trim().to_owned();
    input.error_description = input.error_description.trim().to_owned();
}

fn validate_callback_material(input: &OAuthCallbackInput) -> Result<(), OAuthInputError> {
    if input.state.is_empty() {
        return Err(OAuthInputError {
            status: StatusCode::BAD_REQUEST,
            code: "oauth_state_required",
            message: "callback 缺少 OAuth state",
        });
    }
    if !input.code.is_empty() && !input.error.is_empty() {
        return Err(OAuthInputError {
            status: StatusCode::BAD_REQUEST,
            code: "oauth_callback_material_conflict",
            message: "callback 不得同時包含 code 與 error",
        });
    }
    if input.code.is_empty() && input.error.is_empty() {
        return Err(OAuthInputError {
            status: StatusCode::BAD_REQUEST,
            code: "oauth_code_required",
            message: "callback 缺少授權碼或錯誤狀態",
        });
    }
    Ok(())
}

fn registered_loopback_redirect(raw: &str) -> bool {
    Url::parse(raw).is_ok_and(|url| {
        url.scheme() == "http"
            && url.username().is_empty()
            && url.password().is_none()
            && url.query().is_none()
            && url.fragment().is_none()
            && matches!(url.host_str(), Some("127.0.0.1" | "localhost"))
    })
}

fn loopback_request_matches(
    headers: &HeaderMap,
    uri: &axum::http::Uri,
    info: Option<&AdminRequestInfo>,
    configured: &str,
) -> bool {
    let scheme = info.map_or_else(|| uri.scheme_str().unwrap_or("http"), |info| &info.scheme);
    let host = headers
        .get(header::HOST)
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default();
    let path = uri.path();
    url_identity(&format!("{scheme}://{host}{path}")) == url_identity(configured)
}

fn url_identity(raw: &str) -> String {
    let Ok(url) = Url::parse(raw) else {
        return String::new();
    };
    if !url.has_host() || !url.username().is_empty() || url.password().is_some() {
        return String::new();
    }
    let host = url.host_str().unwrap_or_default().to_ascii_lowercase();
    let authority = match url.port() {
        Some(port) if host.contains(':') => format!("[{host}]:{port}"),
        Some(port) => format!("{host}:{port}"),
        None if host.contains(':') => format!("[{host}]"),
        None => host,
    };
    format!(
        "{}://{}{}",
        url.scheme().to_ascii_lowercase(),
        authority,
        url.path()
    )
}

fn pkce_error(error: PkceError) -> Response {
    oauth_error(
        StatusCode::from_u16(error.status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR),
        error.code,
        error.message,
    )
}

#[derive(Clone, Copy)]
struct OAuthInputError {
    status: StatusCode,
    code: &'static str,
    message: &'static str,
}

fn oauth_input_error(error: OAuthInputError) -> Response {
    oauth_error(error.status, error.code, error.message)
}

fn oauth_error(status: StatusCode, code: &str, message: &str) -> Response {
    let mut response = openai_error(status, "oauth_callback_error", code, message);
    no_store(response.headers_mut());
    response
}

fn discard_failed_oauth(gateway: &Gateway, claimed: &crate::oauth_flow::ClaimedPkce) {
    if claimed.staged && claimed.discard_on_failure {
        gateway.oauth_profiles.discard(&claimed.profile_id);
    }
}

#[derive(Debug)]
enum OAuthTokenStoreError {
    Checkpoint,
    Token,
}

fn store_oauth_token(
    gateway: &Gateway,
    store: &TokenStore,
    token: TokenSet,
    active: bool,
) -> Result<AccountToken, OAuthTokenStoreError> {
    if !active {
        return store.upsert(token).map_err(|_| OAuthTokenStoreError::Token);
    }
    match gateway.checkpoints.clear_then(|| store.upsert(token)) {
        Ok(account) => Ok(account),
        Err(ClearThenError::Clear) => Err(OAuthTokenStoreError::Checkpoint),
        Err(ClearThenError::Change(_) | ClearThenError::Restore { .. }) => {
            Err(OAuthTokenStoreError::Token)
        }
    }
}

fn claim_browser_session(gateway: &Gateway) -> Result<(), Box<Response>> {
    gateway
        .browser_pkce_active
        .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
        .map(|_| ())
        .map_err(|_| {
            Box::new(oauth_error(
                StatusCode::CONFLICT,
                "oauth_browser_session_active",
                "已有瀏覽器授權工作階段正在進行",
            ))
        })
}

fn launch_browser_capture(
    gateway: Arc<Gateway>,
    started: &PkceStart,
    discard_profile: Option<String>,
) {
    let state = started.state.clone();
    let authorization_url = started.url.clone();
    let redirect_uri = started.redirect_uri.clone();
    let profile_dir = gateway
        .tokens
        .path()
        .parent()
        .unwrap_or_else(|| std::path::Path::new("."))
        .join("browser-profile");
    tokio::spawn(async move {
        let capture = gateway
            .browser_pkce
            .capture(authorization_url, redirect_uri, state.clone(), profile_dir)
            .await;
        match capture {
            Ok(capture) => {
                let body = serde_json::json!({
                    "code": capture.code,
                    "state": capture.state,
                    "error": capture.error,
                });
                let request = Request::post("/api/auth/callback")
                    .header(header::CONTENT_TYPE, "application/json")
                    .body(Body::from(body.to_string()))
                    .expect("internal OAuth callback request");
                let response = Gateway::auth_callback(State(Arc::clone(&gateway)), request).await;
                if !response.status().is_success()
                    && let Some(profile_id) = discard_profile.as_deref()
                {
                    gateway.oauth_profiles.discard(profile_id);
                }
            }
            Err(error) => {
                let timeout = error.contains("timed out");
                gateway.pkce.fail_pending(
                    &state,
                    if timeout {
                        "oauth_browser_capture_timeout"
                    } else {
                        "oauth_browser_capture_failed"
                    },
                    if timeout {
                        "瀏覽器授權等待逾時，請重新開始授權"
                    } else {
                        "瀏覽器未能擷取 Microsoft 授權回呼，請重新開始授權"
                    },
                );
                if let Some(profile_id) = discard_profile.as_deref() {
                    gateway.oauth_profiles.discard(profile_id);
                }
            }
        }
        gateway.browser_pkce_active.store(false, Ordering::Release);
    });
}

fn discard_abandoned_oauth(gateway: &Gateway, now: OffsetDateTime) {
    for profile_id in gateway.pkce.prune_discard_targets(now) {
        gateway.oauth_profiles.discard(&profile_id);
    }
}

fn settings_error(error: &GatewayError) -> Response {
    openai_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "invalid_settings",
        &error.to_string(),
    )
}

fn no_store(headers: &mut HeaderMap) {
    headers.insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    headers.insert("pragma", HeaderValue::from_static("no-cache"));
    headers.insert(
        header::REFERRER_POLICY,
        HeaderValue::from_static("no-referrer"),
    );
}

fn admin_error(error: AdminError) -> Response {
    let message = error.to_string();
    let (status, kind, code) = match error {
        AdminError::Unavailable => (
            StatusCode::SERVICE_UNAVAILABLE,
            "configuration_error",
            "configuration_error",
        ),
        AdminError::InvalidCredential
        | AdminError::Unauthenticated
        | AdminError::InvalidCurrentPassword => {
            (StatusCode::UNAUTHORIZED, "auth_error", "auth_error")
        }
        AdminError::RateLimited {
            retry_after_seconds,
        } => {
            let mut response = openai_error(
                StatusCode::TOO_MANY_REQUESTS,
                "rate_limit_error",
                "rate_limit_error",
                &message,
            );
            if let Ok(value) = HeaderValue::from_str(&retry_after_seconds.to_string()) {
                response.headers_mut().insert(header::RETRY_AFTER, value);
            }
            return response;
        }
        AdminError::SamePassword | AdminError::InvalidNewPassword(_) => (
            StatusCode::BAD_REQUEST,
            "invalid_request_error",
            "invalid_request",
        ),
        AdminError::Storage(_) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            "storage_error",
            "storage_error",
        ),
    };
    openai_error(status, kind, code, &message)
}

fn static_page(method: Method, page: &'static str) -> Response {
    if method != Method::GET && method != Method::HEAD {
        return GatewayError::InvalidRequest("不支援此 HTTP 方法".to_owned()).into_response();
    }
    let mut response = Html(page).into_response();
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    response.headers_mut().insert(
        header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    if method == Method::HEAD {
        *response.body_mut() = Body::empty();
    }
    response
}

fn admin_auth_exempt(path: &str) -> bool {
    matches!(
        path,
        "/internal/hindsight/webhook"
            | "/api/admin/login"
            | "/api/admin/session"
            | "/api/admin/change-password"
            | "/api/admin/logout"
            | "/"
    )
}

fn protocol_path(path: &str) -> bool {
    path.starts_with("/v1/") || path.starts_with("/hermes/v1/") || path.starts_with("/memory/v1/")
}

pub(crate) fn artifact_origin(request: &Request) -> String {
    if let Ok(origin) = env::var("M365_ARTIFACT_PUBLIC_ORIGIN")
        && !origin.trim().is_empty()
    {
        return origin.trim().trim_end_matches('/').to_owned();
    }
    let peer_is_loopback = request
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .is_some_and(|peer| peer.0.ip().is_loopback());
    let host = request
        .headers()
        .get(header::HOST)
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default();
    let url = Url::parse(&format!("http://{host}"));
    let host_is_loopback = url
        .as_ref()
        .ok()
        .and_then(Url::host_str)
        .is_some_and(|host| {
            host.eq_ignore_ascii_case("localhost")
                || host
                    .parse::<IpAddr>()
                    .is_ok_and(|address| address.is_loopback())
        });
    if peer_is_loopback && host_is_loopback {
        format!("http://{host}")
    } else {
        String::new()
    }
}

fn artifact_capability_token(path: &str) -> bool {
    let Some(token) = path
        .strip_prefix("/v1/artifacts/")
        .and_then(|value| value.strip_suffix("/content"))
    else {
        return false;
    };
    token.len() == 43
        && token
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
}

fn authenticate_api_key(store: &ApiKeyStore, headers: &HeaderMap) -> Option<String> {
    let raw = headers
        .get("x-api-key")
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .or_else(|| {
            headers
                .get(header::AUTHORIZATION)
                .and_then(|value| value.to_str().ok())
                .and_then(|value| {
                    value
                        .get(..7)
                        .filter(|prefix| prefix.eq_ignore_ascii_case("bearer "))
                        .map(|_| &value[7..])
                })
                .map(str::trim)
                .filter(|value| !value.is_empty())
        })?;
    store.authenticate(raw)
}

fn cookie<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|value| value.to_str().ok())
        .flat_map(|value| value.split(';'))
        .filter_map(|pair| pair.trim().split_once('='))
        .find_map(|(candidate, value)| (candidate == name).then_some(value))
}

fn set_cookie(headers: &mut HeaderMap, token: &str, secure: bool, max_age: i64) {
    let secure = if secure { "; Secure" } else { "" };
    let value = format!(
        "{ADMIN_COOKIE}={token}; Path=/; HttpOnly{secure}; SameSite=Lax; Max-Age={max_age}"
    );
    if let Ok(value) = HeaderValue::from_str(&value) {
        headers.insert(header::SET_COOKIE, value);
    }
}

fn api_keys_path(data_dir: &std::path::Path) -> PathBuf {
    env::var_os("M365_API_KEYS")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| data_dir.join("api-keys.json"))
}

fn token_cache_path(data_dir: &std::path::Path) -> PathBuf {
    if env::var_os("M365_DATA_DIR").is_some() {
        return data_dir.join("accounts.json");
    }
    ["M365_CONFIG", "M365_TOKEN_CACHE", "M365_TOKEN_FILE"]
        .into_iter()
        .find_map(|name| env::var_os(name).filter(|value| !value.is_empty()))
        .map(PathBuf::from)
        .unwrap_or_else(|| data_dir.join("accounts.json"))
}

#[cfg(test)]
mod tests {
    use axum::{body::Body, http::Request};
    use serde_json::Value;
    use tower::ServiceExt;

    use super::*;

    fn seed_checkpoint(gateway: &Arc<Gateway>) {
        let message = crate::checkpoint::CheckpointMessage {
            role: "user".to_owned(),
            content: Value::String("private conversation".to_owned()),
            name: String::new(),
            tool_call_id: String::new(),
            tool_calls: Vec::new(),
            tool_result_is_error: false,
        };
        let turn = gateway
            .checkpoints
            .begin_full("hermes", "owner", "session", &[message], false)
            .unwrap();
        turn.accept(
            crate::checkpoint::Binding {
                conversation_id: "conversation".to_owned(),
                session_id: "upstream-session".to_owned(),
            },
            &[],
        )
        .unwrap();
        assert_eq!(gateway.checkpoints.list().unwrap().len(), 1);
    }

    fn token_set(id: &str) -> crate::auth::TokenSet {
        crate::auth::TokenSet {
            access_token: format!("token-{id}"),
            refresh_token: format!("refresh-{id}"),
            id_token: String::new(),
            token_type: "Bearer".to_owned(),
            scope: DEFAULT_SCOPE.to_owned(),
            expires_in: 3600,
            expires_at: OffsetDateTime::now_utc() + time::Duration::hours(1),
            email: format!("{id}@example.test"),
            display_name: id.to_owned(),
            home_oid: format!("oid-{id}"),
            tenant_id: "tenant".to_owned(),
        }
    }

    fn gateway() -> Arc<Gateway> {
        gateway_with_redirect(DEFAULT_REDIRECT_URI)
    }

    fn gateway_with_redirect(redirect_uri: &str) -> Arc<Gateway> {
        let root = tempfile::tempdir().unwrap().keep();
        let admin_password = root.join("admin-password");
        std::fs::write(&admin_password, "correct-password\n").unwrap();
        let admin = AdminState::open_for_test(admin_password, None).unwrap();
        let oauth = OAuthConfig {
            client_id: crate::auth::DEFAULT_CLIENT_ID.to_owned(),
            authority: crate::auth::DEFAULT_AUTHORITY.to_owned(),
            redirect_uri: redirect_uri.to_owned(),
            scope: crate::auth::DEFAULT_SCOPE.to_owned(),
            authorize_endpoint: format!("{}/oauth2/v2.0/authorize", crate::auth::DEFAULT_AUTHORITY),
            token_endpoint: format!("{}/oauth2/v2.0/token", crate::auth::DEFAULT_AUTHORITY),
        };
        let settings =
            crate::runtime_settings::Store::open(&root, &Config::for_test(root.clone())).unwrap();
        Arc::new(Gateway {
            started_at: Instant::now(),
            admin,
            admin_security: AdminSecurityPolicy::default(),
            api_keys: ApiKeyStore::open(root.join("api-keys.json")).unwrap(),
            tokens: TokenStore::open(root.join("accounts.json"), oauth).unwrap(),
            pkce: PkceManager::default(),
            browser_pkce_active: AtomicBool::new(false),
            browser_pkce: Arc::new(crate::browser_pkce::DisabledRunner),
            oauth_profiles: crate::oauth_profiles::Store::open(
                root.join("accounts.json").as_path(),
            )
            .unwrap(),
            chat: Arc::new(LiveChatHub::new(settings.clone())),
            traffic: TrafficController::new(),
            settings,
            settings_lifecycle: Mutex::new(()),
            checkpoints: CheckpointStore::open(root.join("transport-checkpoints.json")).unwrap(),
            hindsight_webhook_secret: String::new(),
            mcp: crate::mcp::Server::default(),
            artifacts: crate::artifact::Store::open(root.join("artifacts")).unwrap(),
            deployments: crate::deployments::Store::open(&root).unwrap(),
            debug: crate::debug::Store::default(),
            governance: crate::governance::GovernanceStore::open(
                root.join("agent-governance.json"),
            )
            .unwrap(),
        })
    }

    fn authenticated_app() -> (Router, String) {
        let gateway = gateway();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        (Gateway::router(gateway), login.token)
    }

    #[tokio::test]
    async fn test_health_preserves_current_shape() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::get("/api/health")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = axum::body::to_bytes(response.into_body(), 64 * 1024)
            .await
            .unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["status"], "ok");
        assert_eq!(value["auth"], serde_json::json!(["pkce"]));
        assert_eq!(value["chat"], "chathub");
        assert_eq!(value["accountConnected"], false);
    }

    #[tokio::test]
    async fn test_admin_login_sets_session_cookie() {
        let app = Gateway::router(gateway());
        let response = app
            .oneshot(
                Request::post("/api/admin/login")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .body(Body::from(r#"{"password":"correct-password"}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert!(
            response
                .headers()
                .get(header::SET_COOKIE)
                .unwrap()
                .to_str()
                .unwrap()
                .starts_with("m365_admin_session=")
        );
    }

    #[tokio::test]
    async fn test_admin_traffic_recovery_route_is_preserved() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::post("/api/admin/traffic/recovery")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::from(r#"{"action":"complete"}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::CONFLICT);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["error"]["code"], "recovery_not_ready");
    }

    #[tokio::test]
    async fn test_admin_governance_runtime_exposes_acp_projection_for_observers() {
        use crate::governance::{CreateTask, RuntimeObservationRequest, RuntimeState};

        let gateway = gateway();
        gateway
            .governance
            .create_task(CreateTask {
                task_id: "task-observer".to_owned(),
                run_id: "run-observer".to_owned(),
                actor: "scheduler".to_owned(),
                acceptance_contract_digest: "contract:observer:v1".to_owned(),
            })
            .unwrap();
        gateway
            .governance
            .record_runtime_observation(RuntimeObservationRequest {
                task_id: "task-observer".to_owned(),
                run_id: "run-observer".to_owned(),
                expected_authority_revision: 1,
                root_agent_id: "root-a".to_owned(),
                parent_agent_id: Some("manager-a".to_owned()),
                agent_id: "worker-a".to_owned(),
                provider: "m365".to_owned(),
                profile: "default".to_owned(),
                role: "worker".to_owned(),
                runtime_state: RuntimeState::Waiting,
                waiting_on: Some("tool:database".to_owned()),
                environment: "test".to_owned(),
                evidence_class: "direct-runtime".to_owned(),
                actor: "runtime-adapter".to_owned(),
                evidence_refs: vec!["runtime:worker-a-waiting".to_owned()],
            })
            .unwrap();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let app = Gateway::router(gateway);

        let response = app
            .clone()
            .oneshot(
                Request::get(
                    "/api/admin/governance/runtime?taskId=task-observer&runId=run-observer&agentId=worker-a&schemaVersion=1",
                )
                .header(header::HOST, "127.0.0.1")
                .header(
                    header::COOKIE,
                    format!("{ADMIN_COOKIE}={}", login.token),
                )
                .body(Body::empty())
                .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["metadata"]["schemaVersion"], 1);
        assert_eq!(value["metadata"]["authorityScope"], "OBSERVE_ONLY");
        assert_eq!(value["metadata"]["projectionOfAuthorityRevision"], 1);
        assert_eq!(value["runtimeState"]["status"], "VALUE");
        assert_eq!(value["runtimeState"]["value"], "WAITING");
        assert_eq!(value["lifecycleState"]["status"], "VALUE");
        assert_eq!(value["lifecycleState"]["value"], "READY");
        assert_eq!(value["waitingOn"]["value"], "tool:database");

        let legacy = app
            .oneshot(
                Request::get(
                    "/api/admin/governance/runtime?taskId=task-observer&runId=run-observer&agentId=worker-a&schemaVersion=0",
                )
                .header(header::HOST, "127.0.0.1")
                .header(
                    header::COOKIE,
                    format!("{ADMIN_COOKIE}={}", login.token),
                )
                .body(Body::empty())
                .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(legacy.status(), StatusCode::OK);
        let body = to_bytes(legacy.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["metadata"]["schemaVersion"], 0);
        assert_eq!(value["metadata"]["authorityScope"], "OBSERVE_ONLY");
        assert_eq!(value["rootAgentId"]["status"], "SCHEMA_DOWNGRADE");
        assert_eq!(value["runtimeState"]["value"], "WAITING");
    }

    #[tokio::test]
    async fn test_admin_traffic_snapshot_exposes_recovery_telemetry() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::get("/api/admin/traffic")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        let traffic = &value["compatibilityTraffic"];
        assert_eq!(traffic["sharedCircuitState"], "CLOSED");
        assert_eq!(traffic["recoveryObservationSeconds"], 60);
        assert_eq!(traffic["recoveryObservationRemainingSeconds"], 0);
        assert!(traffic.get("lastRecoveryMode").is_some());
        assert!(traffic.get("lastRecoveryReason").is_some());
        assert!(traffic.get("lastRecoveryAt").is_some());
    }

    #[tokio::test]
    async fn test_head_page_has_headers_without_body() {
        let response = Gateway::router(gateway())
            .oneshot(
                Request::head("/")
                    .header(header::HOST, "127.0.0.1")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(
            response.headers().get(header::CACHE_CONTROL).unwrap(),
            "no-store"
        );
        let body = axum::body::to_bytes(response.into_body(), 1024)
            .await
            .unwrap();
        assert!(body.is_empty());
    }

    #[test]
    fn test_pkce_popup_opens_before_the_async_auth_request() {
        let html = include_str!("../web/index.html");
        let start = html.find("async function startPKCE()").unwrap();
        let end = html[start..]
            .find("async function submitCallback()")
            .map(|offset| start + offset)
            .unwrap();
        let function = &html[start..end];
        let popup = function.find("window.open(").unwrap();
        let request = function.find("await api('/api/auth/start'").unwrap();

        assert!(
            popup < request,
            "the popup must be created while the click still has browser user activation"
        );
        assert!(
            function[request..].contains("win.location.replace(d.url)"),
            "the authorization URL must be loaded after the API request succeeds"
        );
    }

    #[test]
    fn test_manual_pkce_fallback_explains_the_referrer_boundary() {
        let html = include_str!("../web/index.html");

        assert!(html.contains("已在目前 Chrome 登入？使用相容備援"));
        assert!(html.contains("callback 仍在錯誤頁的 <code>referrer</code>"));
        assert!(html.contains("不要把一次性網址貼到聊天、日誌或文件"));
        assert!(html.contains("Already signed in to current Chrome? Use compatibility fallback"));
        assert!(html.contains("$('manualCallbackFallback').open=true"));
        assert!(!html.contains("請立刻按瀏覽器的「上一頁」"));
    }

    #[test]
    fn test_primary_login_uses_the_managed_browser_active_route() {
        let html = include_str!("../web/index.html");
        let start = html.find("async function startBrowserLogin()").unwrap();
        let end = html[start..]
            .find("async function loadDeployments()")
            .map(|offset| start + offset)
            .unwrap();
        let function = &html[start..end];

        assert!(html.contains("onclick=\"startBrowserLogin()\""));
        assert!(function.contains("await api('/api/auth/browser/start'"));
        assert!(function.contains("st.status==='authenticated'&&!st.staged"));
        assert!(html.contains(
            "\"瀏覽器未能擷取 Microsoft 授權回呼，請重新開始授權\":\"The browser could not capture the Microsoft sign-in result. Start sign-in again.\""
        ));
        assert!(html.contains(
            "\"自動登入只支援內建 Microsoft 回呼；請改用目前瀏覽器登入\":\"Automatic sign-in supports the built-in Microsoft callback only. Use the current browser instead.\""
        ));
        assert!(!html.contains("startBrowserCandidate"));
    }

    #[tokio::test]
    async fn test_unknown_route_is_not_found_after_authentication() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::get("/not-a-real-route")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::NOT_FOUND);
    }

    #[tokio::test]
    async fn test_hindsight_webhook_is_a_public_signed_route() {
        let response = Gateway::router(gateway())
            .oneshot(
                Request::post("/internal/hindsight/webhook")
                    .body(Body::from("{}"))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["error"]["type"], "configuration_error");
    }

    #[tokio::test]
    async fn test_hindsight_retain_webhook_requires_hmac_and_releases_barrier() {
        let mut gateway = gateway();
        Arc::get_mut(&mut gateway).unwrap().hindsight_webhook_secret =
            "issue71-test-webhook-secret-with-enough-entropy".to_owned();
        gateway.traffic.arm_memory_yield();
        let payload = r#"{"event":"retain.completed","bank_id":"issue71-bank","operation_id":"op-durable-1","status":"completed","timestamp":"2026-08-19T00:00:00Z","data":{}}"#;
        let app = Gateway::router(gateway.clone());

        let bad = app
            .clone()
            .oneshot(
                Request::post("/internal/hindsight/webhook")
                    .header("x-hindsight-event", "retain.completed")
                    .header("x-hindsight-signature", "sha256=deadbeef")
                    .body(Body::from(payload))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(bad.status(), StatusCode::UNAUTHORIZED);
        assert!(gateway.traffic.snapshot().memory_yield_pending);

        let good = app
            .oneshot(
                Request::post("/internal/hindsight/webhook")
                    .header("x-hindsight-event", "retain.completed")
                    .header(
                        "x-hindsight-signature",
                        "sha256=8294375ea1a7a9737d0c2cf7c43a25f00827c59034e538a92094fb1f2538d85e",
                    )
                    .body(Body::from(payload))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(good.status(), StatusCode::NO_CONTENT);
        let snapshot = gateway.traffic.snapshot();
        assert!(!snapshot.memory_yield_pending);
        assert_eq!(snapshot.last_memory_yield_outcome, "retain_durable");
    }

    #[tokio::test]
    async fn test_protocol_route_still_requires_api_key_when_management_checks_are_bypassed() {
        let response = Gateway::router(gateway())
            .oneshot(Request::get("/v1/models").body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn test_image_generation_route_reaches_account_validation() {
        let gateway = gateway();
        let (_, key) = gateway.api_keys.create("images-test").unwrap();
        let response = Gateway::router(gateway)
            .oneshot(
                Request::post("/v1/images/generations")
                    .header(header::AUTHORIZATION, format!("Bearer {key}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .body(Body::from(r#"{"prompt":"畫一隻橘貓"}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["error"]["code"], "account_not_found");
    }

    #[tokio::test]
    async fn test_mcp_modern_initialize_creates_owner_bound_session() {
        let gateway = gateway();
        let (_, key) = gateway.api_keys.create("mcp-test").unwrap();
        let response = Gateway::router(gateway)
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::AUTHORIZATION, format!("Bearer {key}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Hermes","version":"0.20.0"}}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert!(response.headers().get("mcp-session-id").is_some());
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["result"]["protocolVersion"], "2025-11-25");
        assert_eq!(
            value["result"]["capabilities"]["tools"],
            serde_json::json!({})
        );
    }

    #[tokio::test]
    async fn test_mcp_session_cannot_cross_api_key_and_echo_round_trips() {
        let gateway = gateway();
        let (_, key_a) = gateway.api_keys.create("mcp-owner-a").unwrap();
        let (_, key_b) = gateway.api_keys.create("mcp-owner-b").unwrap();
        let app = Gateway::router(gateway);
        let initialized = app
            .clone()
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::AUTHORIZATION, format!("Bearer {key_a}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Hermes","version":"0.20.0"}}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        let session = initialized
            .headers()
            .get("mcp-session-id")
            .unwrap()
            .to_str()
            .unwrap()
            .to_owned();

        let hijack = app
            .clone()
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::AUTHORIZATION, format!("Bearer {key_b}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .header("mcp-session-id", &session)
                    .header("mcp-protocol-version", "2025-11-25")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(hijack.status(), StatusCode::NOT_FOUND);

        let notification = app
            .clone()
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::AUTHORIZATION, format!("Bearer {key_a}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .header("mcp-session-id", &session)
                    .header("mcp-protocol-version", "2025-11-25")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(notification.status(), StatusCode::ACCEPTED);

        let called = app
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::AUTHORIZATION, format!("Bearer {key_a}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .header("mcp-session-id", session)
                    .header("mcp-protocol-version", "2025-11-25")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"wp6_echo","arguments":{"value":"HERMES14_MARKER"}}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(called.status(), StatusCode::OK);
        let body = to_bytes(called.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(
            value["result"]["content"][0]["text"],
            "WP6_ECHO:HERMES14_MARKER"
        );
        assert_eq!(
            value["result"]["structuredContent"]["value"],
            "HERMES14_MARKER"
        );
    }

    #[tokio::test]
    async fn test_mcp_legacy_sse_rejects_head_instead_of_opening_session() {
        let gateway = gateway();
        let (_, key) = gateway.api_keys.create("mcp-legacy").unwrap();
        let response = Gateway::router(gateway)
            .oneshot(
                Request::head("/v1/mcp/sse")
                    .header(header::AUTHORIZATION, format!("Bearer {key}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::METHOD_NOT_ALLOWED);
        assert_eq!(response.headers().get(header::ALLOW).unwrap(), "GET");
    }

    #[tokio::test]
    async fn test_mcp_trusted_origin_is_echoed_after_exact_origin_validation() {
        let gateway = gateway();
        let (_, key) = gateway.api_keys.create("mcp-origin").unwrap();
        let response = Gateway::router(gateway)
            .oneshot(
                Request::post("/v1/mcp")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::AUTHORIZATION, format!("Bearer {key}"))
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::ACCEPT, "application/json, text/event-stream")
                    .body(Body::from(
                        r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Hermes","version":"0.20.0"}}}"#,
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(
            response
                .headers()
                .get(header::ACCESS_CONTROL_ALLOW_ORIGIN)
                .unwrap(),
            "http://127.0.0.1"
        );
        assert_eq!(response.headers().get(header::VARY).unwrap(), "Origin");
    }

    #[tokio::test]
    async fn test_artifact_capability_download_is_exact_and_needs_no_api_key() {
        let gateway = gateway();
        let record = gateway.artifacts.put("報告.csv", b"abc\0exact").unwrap();
        let response = Gateway::router(gateway)
            .oneshot(
                Request::get(format!("/v1/artifacts/{}/content", record.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(response.headers().get(header::CONTENT_LENGTH).unwrap(), "9");
        assert_eq!(
            response.headers().get(header::CACHE_CONTROL).unwrap(),
            "private, no-store"
        );
        assert!(
            response
                .headers()
                .get(header::CONTENT_DISPOSITION)
                .unwrap()
                .to_str()
                .unwrap()
                .contains("%E5%A0%B1%E5%91%8A.csv")
        );
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        assert_eq!(&body[..], b"abc\0exact");
    }

    #[tokio::test]
    async fn test_conversation_list_preserves_current_shape() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::get("/api/conversations")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = axum::body::to_bytes(response.into_body(), 64 * 1024)
            .await
            .unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["conversations"], serde_json::json!([]));
    }

    #[tokio::test]
    async fn test_update_is_read_only_and_reports_current_build_channel() {
        let (app, token) = authenticated_app();
        let response = app
            .oneshot(
                Request::get("/api/update")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["current"], VERSION);
        assert_eq!(value["updateAvailable"], false);
        assert_eq!(value["recommendUpdate"], false);
    }

    #[tokio::test]
    async fn test_admin_settings_round_trip_updates_live_policy() {
        let gateway = gateway();
        seed_checkpoint(&gateway);
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let app = Gateway::router(gateway.clone());
        let get = app
            .clone()
            .oneshot(
                Request::get("/api/admin/settings")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(get.status(), StatusCode::OK);
        let bytes = to_bytes(get.into_body(), 1024 * 1024).await.unwrap();
        let mut value: Value = serde_json::from_slice(&bytes).unwrap();
        value["settings"]["chatTimeoutSeconds"] = serde_json::json!(321);
        value["settings"]["chatMode"] = serde_json::json!("normal");
        let put = app
            .oneshot(
                Request::put("/api/admin/settings")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from(value["settings"].to_string()))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(put.status(), StatusCode::OK);
        assert_eq!(gateway.settings.current().chat_timeout_seconds, 321);
        assert!(gateway.checkpoints.list().unwrap().is_empty());
    }

    #[tokio::test]
    async fn test_admin_settings_partial_put_preserves_capability_evidence() {
        let gateway = gateway();
        let digest = "4".repeat(64);
        let mut settings = gateway.settings.current();
        settings.optional_model_capabilities = vec![serde_json::json!({
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
                "selectorObservationSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                "usabilityObservationSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
                "wireObservationSha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
            }
        })];
        settings.web_request_capability_evidence = serde_json::json!({
            "schema":"m365-web-request-capability-evidence/v1",
            "capturedAt":"2026-08-20T00:00:00Z",
            "tone":"Future_Tone",
            "streamingMode":"ConciseV2",
            "optionsSets":["observed-option"],
            "allowedMessageTypes":["Chat"],
            "observationSha256":digest,
            "temporaryChat":true,
            "disableMemoryObserved":true
        });
        gateway.settings.save(settings).unwrap();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let app = Gateway::router(gateway.clone());
        let put = app
            .clone()
            .oneshot(
                Request::put("/api/admin/settings")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from(r#"{"chatTimeoutSeconds":180}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(put.status(), StatusCode::OK);
        let current = gateway.settings.current();
        assert_eq!(current.chat_timeout_seconds, 180);
        assert_eq!(current.optional_model_capabilities.len(), 1);
        assert_eq!(
            current.web_request_capability_evidence["observationSha256"],
            digest
        );

        let get = app
            .oneshot(
                Request::get("/api/admin/settings")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        let value: Value =
            serde_json::from_slice(&to_bytes(get.into_body(), 1024 * 1024).await.unwrap()).unwrap();
        assert_eq!(value["webRequestCapabilityDrift"]["observed"], true);
        assert_eq!(
            value["webRequestCapabilityDrift"]["projectionPolicy"],
            "observe_only"
        );
        assert_eq!(
            value["chatHubRequestCapabilityBaseline"]["streamingMode"],
            "ConciseWithPadding"
        );
    }

    #[tokio::test]
    async fn test_admin_deployments_route_is_persistent_and_rejects_unknown_provider() {
        let (app, token) = authenticated_app();
        let listed = app
            .clone()
            .oneshot(
                Request::get("/api/admin/deployments")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(listed.status(), StatusCode::OK);
        let body = to_bytes(listed.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["items"], serde_json::json!([]));

        let rejected = app
            .oneshot(
                Request::post("/api/admin/deployments")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::from(r#"{"provider":"unknown"}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(rejected.status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn test_debug_routes_expose_only_structured_request_summaries() {
        let (app, token) = authenticated_app();
        let _ = app
            .clone()
            .oneshot(
                Request::get("/api/version")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        let response = app
            .oneshot(
                Request::get("/api/admin/debug/logs")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 1024 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        let records = value["records"].as_array().unwrap();
        assert!(
            records
                .iter()
                .any(|record| record["path"] == "/api/version")
        );
        assert!(records.iter().all(|record| record.get("body").is_none()));
    }

    #[tokio::test]
    async fn test_managed_browser_login_targets_the_active_account() {
        let gateway = gateway();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let response = Gateway::router(gateway.clone())
            .oneshot(
                Request::post("/api/auth/browser/start")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(value["staged"], false);
        assert_eq!(value["oauthProfileKind"], "active");
        assert!(gateway.tokens.first().is_none());

        let state = value["state"].as_str().unwrap();
        let mut status = gateway.pkce.status(state, OffsetDateTime::now_utc());
        for _ in 0..10 {
            if status.status != "pending" {
                break;
            }
            tokio::task::yield_now().await;
            status = gateway.pkce.status(state, OffsetDateTime::now_utc());
        }
        assert_eq!(status.status, "error");
        assert_eq!(status.error_code, "oauth_browser_capture_failed");
    }

    #[tokio::test]
    async fn test_managed_browser_login_rejects_an_unsupported_redirect_before_launch() {
        let gateway = gateway_with_redirect("http://127.0.0.1:4143/api/auth/callback");
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let response = Gateway::router(gateway.clone())
            .oneshot(
                Request::post("/api/auth/browser/start")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        assert!(!gateway.browser_pkce_active.load(Ordering::Acquire));
    }

    #[tokio::test]
    async fn test_candidate_oauth_routes_keep_the_active_store_isolated() {
        let gateway = gateway();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let app = Gateway::router(gateway.clone());
        let started = app
            .clone()
            .oneshot(
                Request::post("/api/auth/start")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from(r#"{"stageActive":true}"#))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(started.status(), StatusCode::OK);
        let body = to_bytes(started.into_body(), 64 * 1024).await.unwrap();
        let value: Value = serde_json::from_slice(&body).unwrap();
        let profile = value["oauthProfileId"].as_str().unwrap();
        assert_eq!(value["staged"], true);
        assert!(profile.starts_with("oauthp_"));
        assert!(gateway.tokens.first().is_none());

        let candidate = app
            .clone()
            .oneshot(
                Request::post("/api/auth/candidate/chat")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from(
                        serde_json::json!({"profileId": profile}).to_string(),
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(candidate.status(), StatusCode::BAD_REQUEST);

        let browser = app
            .oneshot(
                Request::post("/api/auth/browser/default/start")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(browser.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn test_expired_owned_oauth_candidate_is_discarded() {
        let gateway = gateway();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let (manifest, _) = gateway
            .oauth_profiles
            .stage_from_active(&gateway.tokens)
            .unwrap();
        let started = gateway
            .pkce
            .start_target_owned(
                manifest.oauth.clone(),
                &manifest.profile_id,
                &manifest.kind,
                true,
                true,
                OffsetDateTime::now_utc() - time::Duration::minutes(11),
            )
            .unwrap();

        let response = Gateway::router(gateway.clone())
            .oneshot(
                Request::post("/api/auth/callback")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from(
                        serde_json::json!({"state": started.state, "code": "expired-code"})
                            .to_string(),
                    ))
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::GONE);
        assert!(
            gateway
                .oauth_profiles
                .open_store(&manifest.profile_id)
                .is_err()
        );
    }

    #[tokio::test]
    async fn test_logout_and_active_oauth_replacement_clear_checkpoints() {
        let gateway = gateway();
        gateway.tokens.upsert(token_set("before")).unwrap();
        seed_checkpoint(&gateway);
        store_oauth_token(&gateway, &gateway.tokens, token_set("after"), true).unwrap();
        assert!(gateway.checkpoints.list().unwrap().is_empty());
        assert_eq!(gateway.tokens.first().unwrap().access_token, "token-after");

        seed_checkpoint(&gateway);
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let response = Gateway::router(gateway.clone())
            .oneshot(
                Request::post("/api/account/logout")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        assert!(gateway.tokens.first().is_none());
        assert!(gateway.checkpoints.list().unwrap().is_empty());
    }

    #[tokio::test]
    async fn test_new_oauth_start_prunes_only_abandoned_owned_candidates() {
        let gateway = gateway();
        let login = gateway
            .admin
            .login("correct-password", "127.0.0.1", OffsetDateTime::now_utc())
            .unwrap();
        let (abandoned, _) = gateway
            .oauth_profiles
            .stage_from_active(&gateway.tokens)
            .unwrap();
        let (completed, _) = gateway
            .oauth_profiles
            .stage_from_active(&gateway.tokens)
            .unwrap();
        let created_at = OffsetDateTime::now_utc() - time::Duration::minutes(31);
        gateway
            .pkce
            .start_target_owned(
                abandoned.oauth.clone(),
                &abandoned.profile_id,
                &abandoned.kind,
                true,
                true,
                created_at,
            )
            .unwrap();
        let completed_start = gateway
            .pkce
            .start_target_owned(
                completed.oauth.clone(),
                &completed.profile_id,
                &completed.kind,
                true,
                true,
                created_at,
            )
            .unwrap();
        gateway
            .pkce
            .claim(&completed_start.state, created_at)
            .unwrap();
        gateway.pkce.authenticated(
            &completed_start.state,
            AccountView {
                status: "active".to_owned(),
                expires_at: created_at + time::Duration::hours(1),
                updated_at: created_at,
            },
        );

        let response = Gateway::router(gateway.clone())
            .oneshot(
                Request::post("/api/auth/start")
                    .header(header::HOST, "127.0.0.1")
                    .header(header::ORIGIN, "http://127.0.0.1")
                    .header(header::CONTENT_TYPE, "application/json")
                    .header(header::COOKIE, format!("{ADMIN_COOKIE}={}", login.token))
                    .body(Body::from("{}"))
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::OK);
        assert!(
            gateway
                .oauth_profiles
                .open_store(&abandoned.profile_id)
                .is_err()
        );
        assert!(
            gateway
                .oauth_profiles
                .open_store(&completed.profile_id)
                .is_ok()
        );
    }

    #[tokio::test]
    async fn test_legacy_chat_routes_reuse_the_chat_execution_path() {
        let (app, token) = authenticated_app();
        for route in ["/api/chat", "/api/chat/stream"] {
            let response = app
                .clone()
                .oneshot(
                    Request::post(route)
                        .header(header::HOST, "127.0.0.1")
                        .header(header::ORIGIN, "http://127.0.0.1")
                        .header(header::CONTENT_TYPE, "application/json")
                        .header(header::COOKIE, format!("{ADMIN_COOKIE}={token}"))
                        .body(Body::from(r#"{"message":"hello","model":"m365-auto"}"#))
                        .unwrap(),
                )
                .await
                .unwrap();
            assert_eq!(response.status(), StatusCode::BAD_REQUEST, "{route}");
            let body = to_bytes(response.into_body(), 64 * 1024).await.unwrap();
            let value: Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(value["error"]["code"], "account_not_found", "{route}");
        }
    }
}
