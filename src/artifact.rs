use std::{
    collections::HashMap,
    fs::{self, OpenOptions},
    path::{Path, PathBuf},
    sync::Mutex,
};

use axum::{
    body::{Body, Bytes},
    extract::{Path as AxumPath, Request, State as AxumState},
    http::{HeaderValue, Method, StatusCode, header},
    response::{IntoResponse, Response},
};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use futures_util::{StreamExt, stream};
use rand::Rng;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use time::OffsetDateTime;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

use crate::{error::GatewayError, private_file};

const INDEX_SCHEMA: &str = "m365-artifacts/v1";
const LIFETIME_SECONDS: i64 = 24 * 60 * 60;
const MAX_ENTRIES: usize = 1_024;
const MAX_BYTES: u64 = 4 << 30;
const MAX_FETCH_BYTES: u64 = 512 << 20;
const ARTIFACT_SCOPE: &str = "https://ic3.teams.office.com/.default openid profile offline_access";
const PROTECTED_STREAM_MARKERS: [&str; 5] = [
    "https://",
    "http://",
    "blob:",
    "coderesultfileurl",
    "asyncgw.teams.microsoft.com",
];

#[derive(Clone, Debug)]
pub(crate) struct Record {
    pub token: String,
    pub filename: String,
    pub size: u64,
    pub sha256: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct Entry {
    token_sha256: String,
    filename: String,
    size: u64,
    sha256: String,
    created_at: i64,
    expires_at: i64,
    blob_id: String,
}

#[derive(Deserialize, Serialize)]
struct Index {
    schema: String,
    entries: Vec<Entry>,
}

struct State {
    entries: HashMap<String, Entry>,
    total_bytes: u64,
    pending_entries: usize,
    pending_bytes: u64,
}

pub(crate) struct Store {
    blobs: PathBuf,
    index: PathBuf,
    state: Mutex<State>,
}

impl Store {
    pub(crate) fn open(root: impl Into<PathBuf>) -> Result<Self, GatewayError> {
        let root = root.into();
        let blobs = root.join("blobs");
        private_dir(&root)?;
        private_dir(&blobs)?;
        let index_path = root.join("index.json");
        let index = private_file::read_json::<Index>(&index_path)?.unwrap_or(Index {
            schema: INDEX_SCHEMA.to_owned(),
            entries: Vec::new(),
        });
        if index.schema != INDEX_SCHEMA {
            return Err(storage("unsupported artifact index schema"));
        }
        let now = OffsetDateTime::now_utc().unix_timestamp();
        let mut entries = HashMap::new();
        let mut total_bytes = 0_u64;
        for entry in index.entries {
            if entry.token_sha256.len() != 64
                || entry.blob_id.len() != 64
                || entry.size == 0
                || entry.expires_at <= now
                || entries.contains_key(&entry.token_sha256)
            {
                continue;
            }
            let blob = blobs.join(&entry.blob_id);
            if fs::metadata(&blob)
                .is_ok_and(|metadata| metadata.is_file() && metadata.len() == entry.size)
            {
                total_bytes = total_bytes.saturating_add(entry.size);
                entries.insert(entry.token_sha256.clone(), entry);
            }
        }
        let store = Self {
            blobs,
            index: index_path,
            state: Mutex::new(State {
                entries,
                total_bytes,
                pending_entries: 0,
                pending_bytes: 0,
            }),
        };
        store.persist()?;
        Ok(store)
    }

    #[cfg(test)]
    pub(crate) fn put(&self, filename: &str, bytes: &[u8]) -> Result<Record, GatewayError> {
        if bytes.is_empty() || bytes.len() as u64 > MAX_BYTES {
            return Err(storage("artifact bytes are empty or too large"));
        }
        let mut state = self.state.lock().expect("artifact store poisoned");
        self.cleanup_locked(&mut state);
        if state.entries.len() >= MAX_ENTRIES
            || bytes.len() as u64 > MAX_BYTES.saturating_sub(state.total_bytes)
        {
            return Err(storage("artifact store capacity reached"));
        }
        let token = random_id(32);
        let token_sha256 = digest(token.as_bytes());
        let blob_id = random_hex(32);
        let temporary = self.blobs.join(format!(".artifact-{blob_id}"));
        let blob = self.blobs.join(&blob_id);
        write_blob(&temporary, bytes)?;
        fs::rename(&temporary, &blob)
            .map_err(|error| storage(format!("store artifact bytes: {error}")))?;
        let now = OffsetDateTime::now_utc().unix_timestamp();
        let entry = Entry {
            token_sha256: token_sha256.clone(),
            filename: safe_filename(filename),
            size: bytes.len() as u64,
            sha256: digest(bytes),
            created_at: now,
            expires_at: now + LIFETIME_SECONDS,
            blob_id,
        };
        state.total_bytes += entry.size;
        state.entries.insert(token_sha256.clone(), entry.clone());
        if let Err(error) = self.persist_locked(&state) {
            state.entries.remove(&token_sha256);
            state.total_bytes -= entry.size;
            let _ = fs::remove_file(blob);
            return Err(error);
        }
        Ok(record(entry, token))
    }

    pub(crate) fn stat(&self, token: &str) -> Result<Record, GatewayError> {
        let mut state = self.state.lock().expect("artifact store poisoned");
        self.cleanup_locked(&mut state);
        let entry = state
            .entries
            .get(&digest(token.as_bytes()))
            .cloned()
            .ok_or_else(|| storage("artifact not found"))?;
        Ok(record(entry, token.to_owned()))
    }

    #[cfg(test)]
    pub(crate) fn read(&self, token: &str) -> Result<(Record, Vec<u8>), GatewayError> {
        let record = self.stat(token)?;
        let state = self.state.lock().expect("artifact store poisoned");
        let entry = state
            .entries
            .get(&digest(token.as_bytes()))
            .ok_or_else(|| storage("artifact not found"))?;
        let bytes = fs::read(self.blobs.join(&entry.blob_id))
            .map_err(|error| storage(format!("read artifact: {error}")))?;
        if bytes.len() as u64 != entry.size || digest(&bytes) != entry.sha256 {
            return Err(storage("artifact integrity check failed"));
        }
        Ok((record, bytes))
    }

    pub(crate) fn open_file(&self, token: &str) -> Result<(Record, fs::File), GatewayError> {
        let record = self.stat(token)?;
        let state = self.state.lock().expect("artifact store poisoned");
        let entry = state
            .entries
            .get(&digest(token.as_bytes()))
            .ok_or_else(|| storage("artifact not found"))?;
        let path = self.blobs.join(&entry.blob_id);
        let mut file =
            fs::File::open(&path).map_err(|error| storage(format!("open artifact: {error}")))?;
        let mut hasher = Sha256::new();
        let copied = std::io::copy(&mut file, &mut hasher)
            .map_err(|error| storage(format!("verify artifact: {error}")))?;
        if copied != entry.size || hex(&hasher.finalize()) != entry.sha256 {
            return Err(storage("artifact integrity check failed"));
        }
        let file =
            fs::File::open(path).map_err(|error| storage(format!("reopen artifact: {error}")))?;
        Ok((record, file))
    }

    fn delete(&self, token: &str) -> Result<(), GatewayError> {
        let mut state = self.state.lock().expect("artifact store poisoned");
        let key = digest(token.as_bytes());
        let entry = state
            .entries
            .remove(&key)
            .ok_or_else(|| storage("artifact not found"))?;
        state.total_bytes = state.total_bytes.saturating_sub(entry.size);
        if let Err(error) = self.persist_locked(&state) {
            state.total_bytes += entry.size;
            state.entries.insert(key, entry);
            return Err(error);
        }
        fs::remove_file(self.blobs.join(entry.blob_id))
            .map_err(|error| storage(format!("delete artifact: {error}")))
    }

    async fn put_response(
        &self,
        filename: &str,
        response: reqwest::Response,
    ) -> Result<Record, GatewayError> {
        if response
            .content_length()
            .is_some_and(|size| size == 0 || size > MAX_FETCH_BYTES)
        {
            return Err(storage("artifact output is empty or too large"));
        }
        {
            let mut state = self.state.lock().expect("artifact store poisoned");
            self.cleanup_locked(&mut state);
            if state.entries.len() + state.pending_entries >= MAX_ENTRIES
                || MAX_FETCH_BYTES
                    > MAX_BYTES.saturating_sub(state.total_bytes + state.pending_bytes)
            {
                return Err(storage("artifact store capacity reached"));
            }
            state.pending_entries += 1;
            state.pending_bytes += MAX_FETCH_BYTES;
        }
        let blob_id = random_hex(32);
        let temporary = self.blobs.join(format!(".artifact-stream-{blob_id}"));
        let standard = match create_blob(&temporary) {
            Ok(file) => file,
            Err(error) => {
                self.release_pending();
                return Err(error);
            }
        };
        let mut file = tokio::fs::File::from_std(standard);
        let mut stream = response.bytes_stream();
        let mut size = 0_u64;
        let mut hasher = Sha256::new();
        let streamed = async {
            while let Some(chunk) = stream.next().await {
                let chunk = chunk.map_err(|error| storage(format!("fetch artifact: {error}")))?;
                size = size.saturating_add(chunk.len() as u64);
                if size > MAX_FETCH_BYTES {
                    return Err(storage("artifact output is too large"));
                }
                hasher.update(&chunk);
                file.write_all(&chunk)
                    .await
                    .map_err(|error| storage(format!("write artifact: {error}")))?;
            }
            if size == 0 {
                return Err(storage("artifact output is empty"));
            }
            file.sync_all()
                .await
                .map_err(|error| storage(format!("sync artifact: {error}")))
        }
        .await;
        drop(file);
        self.release_pending();
        if let Err(error) = streamed {
            let _ = fs::remove_file(&temporary);
            return Err(error);
        }
        self.commit_staged(filename, temporary, blob_id, size, hex(&hasher.finalize()))
    }

    fn commit_staged(
        &self,
        filename: &str,
        temporary: PathBuf,
        blob_id: String,
        size: u64,
        sha256: String,
    ) -> Result<Record, GatewayError> {
        let mut state = self.state.lock().expect("artifact store poisoned");
        self.cleanup_locked(&mut state);
        if state.entries.len() >= MAX_ENTRIES || size > MAX_BYTES.saturating_sub(state.total_bytes)
        {
            let _ = fs::remove_file(temporary);
            return Err(storage("artifact store capacity reached"));
        }
        let token = random_id(32);
        let token_sha256 = digest(token.as_bytes());
        let blob = self.blobs.join(&blob_id);
        fs::rename(&temporary, &blob)
            .map_err(|error| storage(format!("store artifact bytes: {error}")))?;
        let now = OffsetDateTime::now_utc().unix_timestamp();
        let entry = Entry {
            token_sha256: token_sha256.clone(),
            filename: safe_filename(filename),
            size,
            sha256,
            created_at: now,
            expires_at: now + LIFETIME_SECONDS,
            blob_id,
        };
        state.total_bytes += size;
        state.entries.insert(token_sha256.clone(), entry.clone());
        if let Err(error) = self.persist_locked(&state) {
            state.entries.remove(&token_sha256);
            state.total_bytes -= size;
            let _ = fs::remove_file(blob);
            return Err(error);
        }
        Ok(record(entry, token))
    }

    fn release_pending(&self) {
        let mut state = self.state.lock().expect("artifact store poisoned");
        state.pending_entries = state.pending_entries.saturating_sub(1);
        state.pending_bytes = state.pending_bytes.saturating_sub(MAX_FETCH_BYTES);
    }

    fn cleanup_locked(&self, state: &mut State) {
        let now = OffsetDateTime::now_utc().unix_timestamp();
        let expired = state
            .entries
            .iter()
            .filter(|(_, entry)| entry.expires_at <= now)
            .map(|(key, entry)| (key.clone(), entry.clone()))
            .collect::<Vec<_>>();
        for (key, entry) in expired {
            state.entries.remove(&key);
            state.total_bytes = state.total_bytes.saturating_sub(entry.size);
            let _ = fs::remove_file(self.blobs.join(entry.blob_id));
        }
    }

    fn persist(&self) -> Result<(), GatewayError> {
        self.persist_locked(&self.state.lock().expect("artifact store poisoned"))
    }

    fn persist_locked(&self, state: &State) -> Result<(), GatewayError> {
        let mut entries = state.entries.values().cloned().collect::<Vec<_>>();
        entries.sort_by(|left, right| left.token_sha256.cmp(&right.token_sha256));
        private_file::write_json(
            &self.index,
            &Index {
                schema: INDEX_SCHEMA.to_owned(),
                entries,
            },
        )
    }
}

fn record(entry: Entry, token: String) -> Record {
    Record {
        token,
        filename: entry.filename,
        size: entry.size,
        sha256: entry.sha256,
    }
}

pub(crate) async fn materialize(
    gateway: &crate::web::Gateway,
    origin: &str,
    result: &mut crate::chathub::ChatResult,
) -> Result<String, GatewayError> {
    if result.artifacts.is_empty() {
        if crate::chathub::contains_protected_artifact_reference(&result.text) {
            return Err(storage("generated artifact could not be made downloadable"));
        }
        return Ok(String::new());
    }
    let origin = valid_public_origin(origin)?;
    let client = reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(std::time::Duration::from_secs(
            gateway.settings.current().chat_timeout_seconds,
        ))
        .build()
        .map_err(|error| storage(format!("create artifact client: {error}")))?;
    let mut created = Vec::new();
    let mut links = Vec::new();
    for artifact in &mut result.artifacts {
        let location = valid_upstream_location(&artifact.upstream_url)?;
        let mut response = None;
        for _ in 0..2 {
            let token = gateway
                .tokens
                .resource_access_token(ARTIFACT_SCOPE)
                .await
                .map_err(|_| storage("artifact authorization unavailable"))?;
            if !valid_bearer_token(&token) {
                return Err(storage("artifact authorization unavailable"));
            }
            let candidate = client
                .get(location.clone())
                .bearer_auth(token)
                .header("ms-ic3-product", "Copilot")
                .header(
                    "x-ms-client-version",
                    format!("M365-AI-Gateway/{}", env!("CARGO_PKG_VERSION")),
                )
                .header(header::ACCEPT_ENCODING, "identity")
                .send()
                .await
                .map_err(|_| storage("artifact upstream request failed"))?;
            if candidate.status() == StatusCode::UNAUTHORIZED {
                continue;
            }
            if !candidate.status().is_success() {
                return Err(storage("artifact upstream status rejected"));
            }
            response = Some(candidate);
            break;
        }
        let response = response.ok_or_else(|| storage("artifact authorization rejected"))?;
        let filename = if artifact.filename.trim().is_empty() {
            location
                .path_segments()
                .and_then(|mut parts| parts.next_back())
                .unwrap_or("artifact")
        } else {
            artifact.filename.as_str()
        };
        let record = match gateway.artifacts.put_response(filename, response).await {
            Ok(record) if !record.sha256.is_empty() => record,
            Ok(record) => {
                let _ = gateway.artifacts.delete(&record.token);
                rollback(&gateway.artifacts, &created);
                return Err(storage("generated artifact could not be made downloadable"));
            }
            Err(error) => {
                rollback(&gateway.artifacts, &created);
                return Err(error);
            }
        };
        let public_url = format!("{origin}/v1/artifacts/{}/content", record.token);
        result.text = result.text.replace(&artifact.upstream_url, &public_url);
        artifact.public_url = public_url.clone();
        artifact.filename = record.filename.clone();
        let label = markdown_label(&record.filename);
        let link = format!("[下載 {label}]({public_url})");
        if !result.text.contains(&link) {
            links.push(link);
        }
        created.push(record.token);
    }
    if crate::chathub::contains_protected_artifact_reference(&result.text) {
        rollback(&gateway.artifacts, &created);
        return Err(storage("generated artifact could not be made downloadable"));
    }
    if links.is_empty() {
        return Ok(String::new());
    }
    let appended = links.join("\n");
    if result.text.trim().is_empty() {
        result.text = appended.clone();
        return Ok(appended);
    }
    let appended = format!("\n\n{appended}");
    result.text = format!("{}{}", result.text.trim_end(), appended);
    Ok(appended)
}

fn rollback(store: &Store, tokens: &[String]) {
    for token in tokens {
        let _ = store.delete(token);
    }
}

fn markdown_label(filename: &str) -> String {
    filename
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .replace('\\', "\\\\")
        .replace('[', "\\[")
        .replace(']', "\\]")
}

fn valid_public_origin(raw: &str) -> Result<String, GatewayError> {
    let url = url::Url::parse(raw).map_err(|_| storage("artifact public origin is required"))?;
    let loopback = url
        .host_str()
        .and_then(|host| host.parse::<std::net::IpAddr>().ok())
        .is_some_and(|address| address.is_loopback())
        || url
            .host_str()
            .is_some_and(|host| host.eq_ignore_ascii_case("localhost"));
    if url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
        || url.path() != "/"
        || !(url.scheme() == "https" || (url.scheme() == "http" && loopback))
    {
        return Err(storage("artifact public origin is invalid"));
    }
    Ok(raw.trim_end_matches('/').to_owned())
}

fn valid_upstream_location(raw: &str) -> Result<url::Url, GatewayError> {
    if raw.trim() != raw {
        return Err(storage("artifact upstream location is invalid"));
    }
    let url = url::Url::parse(raw).map_err(|_| storage("artifact upstream location is invalid"))?;
    let hostname = url.host_str().unwrap_or_default().to_ascii_lowercase();
    let suffix = ".asyncgw.teams.microsoft.com";
    let prefix = hostname.strip_suffix(suffix).unwrap_or_default();
    let safe_prefix = !prefix.is_empty()
        && prefix.split('.').all(|label| {
            !label.is_empty()
                && !label.starts_with('-')
                && !label.ends_with('-')
                && label
                    .bytes()
                    .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-')
        });
    let parts = url
        .path_segments()
        .map(Iterator::collect::<Vec<_>>)
        .unwrap_or_default();
    if url.scheme() != "https"
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
        || url.port().is_some_and(|port| port != 443)
        || !safe_prefix
        || parts.len() < 6
        || parts[0] != "v1"
        || parts[1] != "objects"
        || parts[2].is_empty()
        || !matches!(parts[3], "content" | "views")
        || parts[4] != "original"
        || parts
            .iter()
            .any(|part| part.is_empty() || matches!(*part, "." | ".."))
        || url.path().to_ascii_lowercase().contains("%2f")
        || url.path().to_ascii_lowercase().contains("%5c")
        || decoded_path_has_control(url.path())
    {
        return Err(storage("artifact upstream location is invalid"));
    }
    Ok(url)
}

pub(crate) fn release_stream_safe_prefix(buffer: &mut String) -> String {
    let value = buffer.as_bytes();
    let mut hold_at = value.len();
    for marker in PROTECTED_STREAM_MARKERS {
        if let Some(index) = index_ascii_fold(value, marker.as_bytes()) {
            hold_at = hold_at.min(index);
        }
    }
    if hold_at == value.len() {
        let mut keep = 0;
        for marker in PROTECTED_STREAM_MARKERS {
            for length in 1..marker.len().min(value.len() + 1) {
                if equal_ascii_fold(&value[value.len() - length..], &marker.as_bytes()[..length]) {
                    keep = keep.max(length);
                }
            }
        }
        hold_at -= keep;
    }
    let held = buffer.split_off(hold_at);
    std::mem::replace(buffer, held)
}

fn index_ascii_fold(value: &[u8], marker: &[u8]) -> Option<usize> {
    value
        .windows(marker.len())
        .position(|window| equal_ascii_fold(window, marker))
}

fn equal_ascii_fold(value: &[u8], marker: &[u8]) -> bool {
    value.len() == marker.len()
        && value
            .iter()
            .zip(marker)
            .all(|(left, right)| left.to_ascii_lowercase() == *right)
}

fn valid_bearer_token(token: &str) -> bool {
    !token.is_empty()
        && token.trim() == token
        && !token
            .chars()
            .any(|character| character.is_whitespace() || character.is_control())
}

fn decoded_path_has_control(path: &str) -> bool {
    let source = path.as_bytes();
    let mut decoded = Vec::with_capacity(source.len());
    let mut index = 0;
    while index < source.len() {
        if source[index] == b'%' && index + 2 < source.len() {
            let pair = &path[index + 1..index + 3];
            let Ok(byte) = u8::from_str_radix(pair, 16) else {
                return true;
            };
            decoded.push(byte);
            index += 3;
        } else {
            decoded.push(source[index]);
            index += 1;
        }
    }
    String::from_utf8(decoded)
        .map(|value| value.chars().any(char::is_control))
        .unwrap_or(true)
}

pub(crate) async fn content(
    AxumState(gateway): AxumState<std::sync::Arc<crate::web::Gateway>>,
    AxumPath(capability): AxumPath<String>,
    request: Request,
) -> Response {
    if capability.len() != 43
        || !capability
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
    {
        return StatusCode::NOT_FOUND.into_response();
    }
    let method = request.method().clone();
    let (record, body) = if method == Method::HEAD {
        match gateway.artifacts.stat(&capability) {
            Ok(record) => (record, Body::empty()),
            Err(_) => return StatusCode::NOT_FOUND.into_response(),
        }
    } else {
        let (record, file) = match gateway.artifacts.open_file(&capability) {
            Ok(value) => value,
            Err(_) => return StatusCode::NOT_FOUND.into_response(),
        };
        let file = tokio::fs::File::from_std(file);
        let output = stream::unfold(file, |mut file| async move {
            let mut buffer = vec![0_u8; 64 * 1024];
            match file.read(&mut buffer).await {
                Ok(0) => None,
                Ok(count) => {
                    buffer.truncate(count);
                    Some((Ok::<_, std::io::Error>(Bytes::from(buffer)), file))
                }
                Err(error) => Some((Err(error), file)),
            }
        });
        (record, Body::from_stream(output))
    };
    let mut response = body.into_response();
    let headers = response.headers_mut();
    headers.insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/octet-stream"),
    );
    headers.insert(
        header::CONTENT_LENGTH,
        HeaderValue::from_str(&record.size.to_string()).expect("artifact size is a header value"),
    );
    headers.insert(
        header::CONTENT_DISPOSITION,
        HeaderValue::from_str(&content_disposition(&record.filename))
            .expect("sanitized artifact filename is a header value"),
    );
    headers.insert(
        header::CACHE_CONTROL,
        HeaderValue::from_static("private, no-store"),
    );
    headers.insert(
        "x-content-type-options",
        HeaderValue::from_static("nosniff"),
    );
    response
}

fn content_disposition(filename: &str) -> String {
    let encoded = filename
        .as_bytes()
        .iter()
        .map(|byte| {
            if byte.is_ascii_alphanumeric() || matches!(*byte, b'-' | b'.' | b'_') {
                (*byte as char).to_string()
            } else {
                format!("%{byte:02X}")
            }
        })
        .collect::<String>();
    format!("attachment; filename*=UTF-8''{encoded}")
}

fn safe_filename(raw: &str) -> String {
    let name = Path::new(raw)
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("artifact");
    let mut value = name
        .chars()
        .map(|character| {
            if character == '/' || character == '\\' || character == ':' || character.is_control() {
                '_'
            } else {
                character
            }
        })
        .collect::<String>();
    value = value.trim_matches([' ', '.']).to_owned();
    if value.is_empty() {
        value = "artifact".to_owned();
    }
    while value.len() > 240 {
        value.pop();
    }
    value
}

#[cfg(test)]
fn write_blob(path: &Path, bytes: &[u8]) -> Result<(), GatewayError> {
    use std::io::Write;

    let mut file = create_blob(path)?;
    file.write_all(bytes)
        .and_then(|_| file.sync_all())
        .map_err(|error| storage(format!("write artifact: {error}")))
}

fn create_blob(path: &Path) -> Result<fs::File, GatewayError> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options
        .open(path)
        .map_err(|error| storage(format!("create artifact: {error}")))
}

fn private_dir(path: &Path) -> Result<(), GatewayError> {
    fs::create_dir_all(path)
        .map_err(|error| storage(format!("create artifact directory: {error}")))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700))
            .map_err(|error| storage(format!("secure artifact directory: {error}")))?;
    }
    Ok(())
}

fn random_id(size: usize) -> String {
    let mut bytes = vec![0_u8; size];
    rand::rng().fill(bytes.as_mut_slice());
    URL_SAFE_NO_PAD.encode(bytes)
}

fn random_hex(size: usize) -> String {
    let mut bytes = vec![0_u8; size];
    rand::rng().fill(bytes.as_mut_slice());
    hex(&bytes)
}

fn digest(bytes: &[u8]) -> String {
    hex(&Sha256::digest(bytes))
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn storage(message: impl Into<String>) -> GatewayError {
    GatewayError::Storage(message.into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn private_artifact_store_round_trips_exact_bytes_across_restart() {
        let root = tempfile::tempdir().unwrap();
        let store = Store::open(root.path()).unwrap();
        let record = store.put("../報告.csv", b"abc").unwrap();
        assert_eq!(record.filename, "報告.csv");
        assert_eq!(record.size, 3);
        assert_eq!(
            record.sha256,
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        assert_eq!(record.token.len(), 43);
        drop(store);

        let reopened = Store::open(root.path()).unwrap();
        let (metadata, bytes) = reopened.read(&record.token).unwrap();
        assert_eq!(metadata.filename, "報告.csv");
        assert_eq!(bytes, b"abc");
    }

    #[test]
    fn artifact_network_boundaries_fail_closed() {
        assert!(
            valid_upstream_location(
                "https://us-prod.asyncgw.teams.microsoft.com/v1/objects/id/views/original/report.csv?sig=private"
            )
            .is_ok()
        );
        for location in [
            "not a URL",
            " https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
            "http://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
            "https://asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
            "https://asyncgw.teams.microsoft.com.evil.test/v1/objects/id/views/original/a.txt",
            "https://bad..asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
            "https://user@us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt",
            "https://us.asyncgw.teams.microsoft.com:444/v1/objects/id/views/original/a.txt",
            "https://us.asyncgw.teams.microsoft.com/v2/objects/id/views/original/a.txt",
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/raw/a.txt",
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/thumbnail/a.txt",
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a%00.txt",
            "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/a.txt#fragment",
        ] {
            assert!(valid_upstream_location(location).is_err(), "{location}");
        }

        assert!(valid_public_origin("https://sidecar.example.test/").is_ok());
        assert!(valid_public_origin("http://127.0.0.1:4141/").is_ok());
        for origin in [
            "http://sidecar.example.test/",
            "https://user@sidecar.example.test/",
            "https://sidecar.example.test/path",
            "https://sidecar.example.test/?query=1",
        ] {
            assert!(valid_public_origin(origin).is_err(), "{origin}");
        }
    }

    #[test]
    fn artifact_bearer_token_rejects_empty_whitespace_and_controls() {
        assert!(valid_bearer_token("opaque-token"));
        for token in ["", " token", "token\n", "token\0"] {
            assert!(!valid_bearer_token(token));
        }
    }

    #[test]
    fn artifact_stream_guard_holds_same_and_split_urls() {
        for prefix in ["ordinary ", "KX", "ȺX"] {
            let mut buffer =
                format!("{prefix}https://artifact.asyncgw.teams.microsoft.com/private");
            assert_eq!(release_stream_safe_prefix(&mut buffer), prefix);
            assert!(buffer.starts_with("https://"));
        }

        let mut split = "safe KXhtt".to_owned();
        assert_eq!(release_stream_safe_prefix(&mut split), "safe KX");
        assert_eq!(split, "htt");
        split.push_str("ps://artifact.asyncgw.teams.microsoft.com/private");
        assert_eq!(release_stream_safe_prefix(&mut split), "");
        assert!(split.starts_with("https://"));
    }
}
