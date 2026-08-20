use std::{
    collections::HashMap,
    convert::Infallible,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use axum::{
    Json,
    body::{Body, Bytes, to_bytes},
    extract::{ConnectInfo, Request, State},
    http::{HeaderMap, HeaderValue, Method, StatusCode, header},
    response::{IntoResponse, Response},
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use futures_util::stream;
use rand::Rng;
use serde::Deserialize;
use serde_json::{Value, json};
use tokio::sync::mpsc;

use crate::{
    admin::validate_origin,
    web::{ApiKeyOwner, Gateway},
};

const LATEST_PROTOCOL: &str = "2025-11-25";
const SESSION_HEADER: &str = "mcp-session-id";
const PROTOCOL_HEADER: &str = "mcp-protocol-version";
const MAX_BODY: usize = 8 << 20;
const MAX_SESSIONS: usize = 128;
const SESSION_TTL: Duration = Duration::from_secs(30 * 60);

#[derive(Default)]
pub(crate) struct Server {
    sessions: Mutex<HashMap<String, Session>>,
}

struct Session {
    owner: String,
    protocol: String,
    initialized: bool,
    legacy: bool,
    last_used: Instant,
    sender: Option<mpsc::Sender<Vec<u8>>>,
}

#[derive(Deserialize)]
struct RpcRequest {
    jsonrpc: String,
    #[serde(default)]
    id: Option<Value>,
    method: String,
    #[serde(default)]
    params: Value,
}

pub(crate) async fn streamable(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let origin = match validate_origin_and_owner(&gateway, &request) {
        Ok(origin) => origin,
        Err(response) => return response,
    };
    let owner = owner(&request);
    let response = match *request.method() {
        Method::HEAD => empty(StatusCode::OK),
        Method::POST => modern_post(&gateway.mcp, owner, request).await,
        Method::DELETE => modern_delete(&gateway.mcp, &owner, &request),
        _ => method_not_allowed(
            "HEAD, POST, DELETE",
            "server-initiated streams are not supported",
        ),
    };
    with_cors(response, origin)
}

pub(crate) async fn legacy_sse(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let origin = match validate_origin_and_owner(&gateway, &request) {
        Ok(origin) => origin,
        Err(response) => return response,
    };
    if request.method() != Method::GET {
        return method_not_allowed("GET", "method not allowed");
    }
    let owner = owner(&request);
    let (session_id, receiver) = match gateway.mcp.create_session(owner, String::new(), true) {
        Ok(value) => value,
        Err(message) => return http_error(StatusCode::SERVICE_UNAVAILABLE, message),
    };
    let initial = format!("event: endpoint\ndata: /v1/mcp/message?sessionId={session_id}\n\n");
    let output = stream::unfold(
        (Some(Bytes::from(initial)), receiver),
        |(first, mut receiver)| async move {
            if let Some(first) = first {
                return Some((Ok::<_, Infallible>(first), (None, receiver)));
            }
            receiver.recv().await.map(|message| {
                (
                    Ok(Bytes::from(format!(
                        "event: message\ndata: {}\n\n",
                        String::from_utf8_lossy(&message)
                    ))),
                    (None, receiver),
                )
            })
        },
    );
    let mut response = Body::from_stream(output).into_response();
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/event-stream"),
    );
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
    response.headers_mut().insert(
        "x-content-type-options",
        HeaderValue::from_static("nosniff"),
    );
    with_cors(response, origin)
}

pub(crate) async fn legacy_message(
    State(gateway): State<Arc<Gateway>>,
    request: Request,
) -> Response {
    let origin = match validate_origin_and_owner(&gateway, &request) {
        Ok(origin) => origin,
        Err(response) => return response,
    };
    if !json_content_type(request.headers()) {
        return http_error(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "Content-Type must be application/json",
        );
    }
    let session_id = request
        .uri()
        .query()
        .map(|query| {
            url::form_urlencoded::parse(query.as_bytes())
                .filter(|(name, _)| name == "sessionId")
                .map(|(_, value)| value.into_owned())
                .collect::<Vec<_>>()
        })
        .filter(|values| values.len() == 1)
        .and_then(|mut values| values.pop())
        .filter(|value| !value.trim().is_empty());
    let Some(session_id) = session_id else {
        return http_error(StatusCode::BAD_REQUEST, "sessionId required");
    };
    let owner = owner(&request);
    if !gateway.mcp.session_matches(&session_id, &owner, true) {
        return http_error(StatusCode::NOT_FOUND, "MCP session not found");
    }
    let rpc = match read_rpc(request).await {
        Ok(rpc) => rpc,
        Err(response) => return response,
    };
    let response = gateway.mcp.dispatch(&session_id, &owner, rpc);
    let Some(response) = response else {
        return empty(StatusCode::ACCEPTED);
    };
    let body = serde_json::to_vec(&response).expect("JSON-RPC response serializes");
    let response = match gateway.mcp.legacy_sender(&session_id, &owner) {
        Some(sender) if sender.try_send(body).is_ok() => empty(StatusCode::ACCEPTED),
        Some(_) => http_error(StatusCode::SERVICE_UNAVAILABLE, "MCP response queue full"),
        None => http_error(StatusCode::NOT_FOUND, "MCP session not found"),
    };
    with_cors(response, origin)
}

async fn modern_post(server: &Server, owner: String, request: Request) -> Response {
    let accepts = request
        .headers()
        .get_all(header::ACCEPT)
        .iter()
        .filter_map(|value| value.to_str().ok())
        .collect::<Vec<_>>()
        .join(",");
    if !accepts_type(&accepts, "application/json") || !accepts_type(&accepts, "text/event-stream") {
        return http_error(
            StatusCode::NOT_ACCEPTABLE,
            "Accept must include application/json and text/event-stream",
        );
    }
    if !json_content_type(request.headers()) {
        return http_error(
            StatusCode::UNSUPPORTED_MEDIA_TYPE,
            "Content-Type must be application/json",
        );
    }
    let session_header = single_header(request.headers(), SESSION_HEADER);
    let protocol_header = single_header(request.headers(), PROTOCOL_HEADER);
    let rpc = match read_rpc(request).await {
        Ok(rpc) => rpc,
        Err(response) => return response,
    };
    if rpc.method == "initialize" {
        if session_header.is_some() {
            return rpc_error_response(
                StatusCode::BAD_REQUEST,
                rpc.id.unwrap_or(Value::Null),
                -32600,
                "initialize must not reuse a session",
            );
        }
        let protocol = match negotiated_protocol(&rpc.params) {
            Some(protocol) => protocol,
            None => {
                return rpc_error_response(
                    StatusCode::BAD_REQUEST,
                    rpc.id.unwrap_or(Value::Null),
                    -32602,
                    "invalid initialize params",
                );
            }
        };
        let (session_id, _) = match server.create_session(owner.clone(), protocol, false) {
            Ok(value) => value,
            Err(message) => return http_error(StatusCode::SERVICE_UNAVAILABLE, message),
        };
        let response = server
            .dispatch(&session_id, &owner, rpc)
            .unwrap_or_else(|| rpc_error(Value::Null, -32603, "response encoding failed"));
        let mut response = (StatusCode::OK, Json(response)).into_response();
        response.headers_mut().insert(
            SESSION_HEADER,
            HeaderValue::from_str(&session_id).expect("session ID is a header value"),
        );
        return response;
    }
    let Some(session_id) = session_header else {
        return http_error(StatusCode::BAD_REQUEST, "MCP session header required");
    };
    if !server.session_matches(&session_id, &owner, false) {
        return http_error(StatusCode::NOT_FOUND, "MCP session not found");
    }
    let Some(protocol) = protocol_header else {
        return http_error(StatusCode::BAD_REQUEST, "MCP protocol header mismatch");
    };
    if !server.protocol_matches(&session_id, &owner, &protocol) {
        return http_error(StatusCode::BAD_REQUEST, "MCP protocol header mismatch");
    }
    match server.dispatch(&session_id, &owner, rpc) {
        Some(response) => (StatusCode::OK, Json(response)).into_response(),
        None => empty(StatusCode::ACCEPTED),
    }
}

fn modern_delete(server: &Server, owner: &str, request: &Request) -> Response {
    let Some(session_id) = single_header(request.headers(), SESSION_HEADER) else {
        return http_error(StatusCode::BAD_REQUEST, "MCP session header required");
    };
    if !server.session_matches(&session_id, owner, false) {
        return http_error(StatusCode::NOT_FOUND, "MCP session not found");
    }
    let Some(protocol) = single_header(request.headers(), PROTOCOL_HEADER) else {
        return http_error(StatusCode::BAD_REQUEST, "MCP protocol header mismatch");
    };
    if !server.protocol_matches(&session_id, owner, &protocol) {
        return http_error(StatusCode::BAD_REQUEST, "MCP protocol header mismatch");
    }
    server.remove_session(&session_id, owner);
    empty(StatusCode::NO_CONTENT)
}

impl Server {
    fn create_session(
        &self,
        owner: String,
        protocol: String,
        legacy: bool,
    ) -> Result<(String, mpsc::Receiver<Vec<u8>>), &'static str> {
        let mut bytes = [0_u8; 32];
        rand::rng().fill(&mut bytes);
        let id = URL_SAFE_NO_PAD.encode(bytes);
        let (sender, receiver) = mpsc::channel(64);
        let now = Instant::now();
        let mut sessions = self.sessions.lock().expect("MCP sessions poisoned");
        sessions.retain(|_, session| now.duration_since(session.last_used) < SESSION_TTL);
        if sessions.len() >= MAX_SESSIONS {
            return Err("MCP session capacity reached");
        }
        sessions.insert(
            id.clone(),
            Session {
                owner,
                protocol,
                initialized: false,
                legacy,
                last_used: now,
                sender: legacy.then_some(sender),
            },
        );
        Ok((id, receiver))
    }

    fn session_matches(&self, id: &str, owner: &str, legacy: bool) -> bool {
        let mut sessions = self.sessions.lock().expect("MCP sessions poisoned");
        let Some(session) = sessions.get_mut(id) else {
            return false;
        };
        if session.owner != owner || session.legacy != legacy {
            return false;
        }
        session.last_used = Instant::now();
        true
    }

    fn protocol_matches(&self, id: &str, owner: &str, protocol: &str) -> bool {
        self.sessions
            .lock()
            .expect("MCP sessions poisoned")
            .get(id)
            .is_some_and(|session| session.owner == owner && session.protocol == protocol)
    }

    fn legacy_sender(&self, id: &str, owner: &str) -> Option<mpsc::Sender<Vec<u8>>> {
        self.sessions
            .lock()
            .expect("MCP sessions poisoned")
            .get(id)
            .filter(|session| session.owner == owner && session.legacy)
            .and_then(|session| session.sender.clone())
    }

    fn remove_session(&self, id: &str, owner: &str) {
        let mut sessions = self.sessions.lock().expect("MCP sessions poisoned");
        if sessions
            .get(id)
            .is_some_and(|session| session.owner == owner)
        {
            sessions.remove(id);
        }
    }

    fn dispatch(&self, id: &str, owner: &str, request: RpcRequest) -> Option<Value> {
        let response_id = request.id.clone().unwrap_or(Value::Null);
        if request.jsonrpc != "2.0"
            || request.method.trim().is_empty()
            || request.id.as_ref().is_some_and(|id| !valid_id(id))
        {
            return Some(rpc_error(response_id, -32600, "invalid request"));
        }
        if request.method == "initialize" {
            let protocol = negotiated_protocol(&request.params)?;
            let mut sessions = self.sessions.lock().expect("MCP sessions poisoned");
            let session = sessions.get_mut(id)?;
            if session.owner != owner
                || (!session.protocol.is_empty() && session.protocol != protocol)
                || (session.legacy && !session.protocol.is_empty())
            {
                return Some(rpc_error(
                    response_id,
                    -32600,
                    "session already initialized",
                ));
            }
            session.protocol = protocol.clone();
            return Some(rpc_result(
                response_id,
                json!({
                    "protocolVersion": protocol,
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "m365-copilot2api", "version": "wp6"}
                }),
            ));
        }
        if request.method == "notifications/initialized" {
            if request.id.is_some() {
                return Some(rpc_error(
                    response_id,
                    -32600,
                    "initialized must be a notification",
                ));
            }
            if let Some(session) = self
                .sessions
                .lock()
                .expect("MCP sessions poisoned")
                .get_mut(id)
            {
                session.initialized = !session.protocol.is_empty();
            }
            return None;
        }
        if request.method == "notifications/cancelled" || request.id.is_none() {
            return None;
        }
        let initialized = self
            .sessions
            .lock()
            .expect("MCP sessions poisoned")
            .get(id)
            .is_some_and(|session| session.owner == owner && session.initialized);
        if !initialized {
            return Some(rpc_error(response_id, -32600, "session not initialized"));
        }
        match request.method.as_str() {
            "ping" => Some(rpc_result(response_id, json!({}))),
            "tools/list" => Some(rpc_result(response_id, json!({"tools": [echo_tool()]}))),
            "tools/call" => Some(call_echo(response_id, &request.params)),
            _ => Some(rpc_error(response_id, -32601, "method not found")),
        }
    }
}

fn echo_tool() -> Value {
    json!({
        "name": "wp6_echo",
        "description": "Returns the supplied value unchanged to verify MCP interoperability.",
        "inputSchema": {
            "type": "object",
            "properties": {"value": {"type": "string"}},
            "required": ["value"]
        },
        "outputSchema": {
            "type": "object",
            "properties": {"value": {"type": "string"}},
            "required": ["value"]
        },
        "annotations": {
            "readOnlyHint": true,
            "destructiveHint": false,
            "idempotentHint": true,
            "openWorldHint": false
        }
    })
}

fn call_echo(id: Value, params: &Value) -> Value {
    let name = params
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let arguments = params.get("arguments").and_then(Value::as_object);
    let value = arguments
        .and_then(|arguments| arguments.get("value"))
        .and_then(Value::as_str);
    if name != "wp6_echo" {
        return rpc_error(id, -32602, "unknown tool");
    }
    let Some(value) = value else {
        return rpc_error(id, -32602, "tool arguments do not match input schema");
    };
    rpc_result(
        id,
        json!({
            "content": [{"type": "text", "text": format!("WP6_ECHO:{value}")}],
            "structuredContent": {"value": value}
        }),
    )
}

async fn read_rpc(request: Request) -> Result<RpcRequest, Response> {
    let body = to_bytes(request.into_body(), MAX_BODY).await.map_err(|_| {
        rpc_error_response(
            StatusCode::PAYLOAD_TOO_LARGE,
            Value::Null,
            -32000,
            "MCP message too large",
        )
    })?;
    serde_json::from_slice(&body).map_err(|_| {
        rpc_error_response(StatusCode::BAD_REQUEST, Value::Null, -32700, "parse error")
    })
}

fn negotiated_protocol(params: &Value) -> Option<String> {
    let protocol = params.get("protocolVersion")?.as_str()?.trim();
    let capabilities = params.get("capabilities")?.as_object()?;
    let client = params.get("clientInfo")?.as_object()?;
    if capabilities.len() > 1_000
        || client.get("name")?.as_str()?.trim().is_empty()
        || client.get("version")?.as_str()?.trim().is_empty()
    {
        return None;
    }
    Some(
        if matches!(
            protocol,
            "2024-11-05" | "2025-03-26" | "2025-06-18" | LATEST_PROTOCOL
        ) {
            protocol
        } else {
            LATEST_PROTOCOL
        }
        .to_owned(),
    )
}

// Returning the response directly keeps the three HTTP entry points small and
// avoids an allocation on the successful path.
#[allow(clippy::result_large_err)]
fn validate_origin_and_owner(
    gateway: &Gateway,
    request: &Request,
) -> Result<Option<HeaderValue>, Response> {
    if owner(request).is_empty() {
        return Err(http_error(
            StatusCode::UNAUTHORIZED,
            "valid API key required",
        ));
    }
    if request
        .headers()
        .get_all(header::ORIGIN)
        .iter()
        .next()
        .is_none()
    {
        return Ok(None);
    }
    let peer = request
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .map(|value| value.0)
        .unwrap_or_else(|| SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 0));
    let info = match gateway
        .admin_security
        .inspect(request.headers(), request.uri(), peer)
    {
        Ok(info) => info,
        Err(_) => return Err(http_error(StatusCode::FORBIDDEN, "invalid Origin")),
    };
    if validate_origin(request.method(), request.headers(), &info).is_err() {
        return Err(http_error(StatusCode::FORBIDDEN, "invalid Origin"));
    }
    request
        .headers()
        .get(header::ORIGIN)
        .cloned()
        .map(Some)
        .ok_or_else(|| http_error(StatusCode::FORBIDDEN, "invalid Origin"))
}

fn with_cors(mut response: Response, origin: Option<HeaderValue>) -> Response {
    if let Some(origin) = origin {
        response
            .headers_mut()
            .insert(header::ACCESS_CONTROL_ALLOW_ORIGIN, origin);
        response
            .headers_mut()
            .append(header::VARY, HeaderValue::from_static("Origin"));
    }
    response
}

fn owner(request: &Request) -> String {
    request
        .extensions()
        .get::<ApiKeyOwner>()
        .map(|owner| owner.0.clone())
        .unwrap_or_default()
}

fn single_header(headers: &HeaderMap, name: &str) -> Option<String> {
    let mut values = headers.get_all(name).iter();
    let value = values.next()?.to_str().ok()?.trim();
    if value.is_empty() || value.contains(',') || values.next().is_some() {
        None
    } else {
        Some(value.to_owned())
    }
}

fn json_content_type(headers: &HeaderMap) -> bool {
    headers
        .get(header::CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split(';').next())
        .is_some_and(|value| value.trim().eq_ignore_ascii_case("application/json"))
}

fn accepts_type(header: &str, expected: &str) -> bool {
    header.split(',').any(|value| {
        value
            .split(';')
            .next()
            .is_some_and(|value| matches!(value.trim(), "*/*") || value.trim() == expected)
    })
}

fn valid_id(id: &Value) -> bool {
    match id {
        Value::String(_) => true,
        Value::Number(number) => number.as_i64().is_some(),
        _ => false,
    }
}

fn rpc_result(id: Value, result: Value) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "result": result})
}

fn rpc_error(id: Value, code: i64, message: &str) -> Value {
    json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}})
}

fn rpc_error_response(status: StatusCode, id: Value, code: i64, message: &str) -> Response {
    (status, Json(rpc_error(id, code, message))).into_response()
}

fn http_error(status: StatusCode, message: &str) -> Response {
    rpc_error_response(status, Value::Null, -32000, message)
}

fn empty(status: StatusCode) -> Response {
    let mut response = status.into_response();
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    response
}

fn method_not_allowed(allow: &'static str, message: &str) -> Response {
    let mut response = http_error(StatusCode::METHOD_NOT_ALLOWED, message);
    response
        .headers_mut()
        .insert(header::ALLOW, HeaderValue::from_static(allow));
    response
}
