use std::{
    collections::HashMap,
    env, fs,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::{Path, PathBuf},
};

use axum::http::{HeaderMap, Method, Uri, header};
use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use rand::Rng;
use time::{Duration, OffsetDateTime};
use url::Url;

use crate::{error::GatewayError, private_file};

const SESSION_IDLE: Duration = Duration::minutes(30);
const SESSION_ABSOLUTE: Duration = Duration::hours(24);
const LOGIN_WINDOW: Duration = Duration::minutes(15);
const LOGIN_LOCK: Duration = Duration::minutes(15);
const MAX_LOGIN_ATTEMPTS: usize = 4_096;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum CredentialMode {
    Unavailable,
    Persisted,
    Bootstrap,
    BootstrapConsumed,
}

#[derive(Clone, Debug)]
struct Session {
    created_at: OffsetDateTime,
    last_seen_at: OffsetDateTime,
    expires_at: OffsetDateTime,
}

#[derive(Clone, Debug, Default)]
struct LoginAttempt {
    failures: u8,
    window_start: Option<OffsetDateTime>,
    locked_until: Option<OffsetDateTime>,
}

struct AdminInner {
    password: String,
    mode: CredentialMode,
    must_change_password: bool,
    sessions: HashMap<String, Session>,
    login_attempts: HashMap<String, LoginAttempt>,
}

pub struct AdminState {
    password_path: PathBuf,
    consumed_path: PathBuf,
    inner: std::sync::Mutex<AdminInner>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LoginSuccess {
    pub token: String,
    pub must_change_password: bool,
}

#[derive(Debug, thiserror::Error)]
pub enum AdminError {
    #[error("管理員憑證無法使用")]
    Unavailable,
    #[error("管理員密碼不正確")]
    InvalidCredential,
    #[error("登入失敗次數過多，請稍後再試")]
    RateLimited { retry_after_seconds: i64 },
    #[error("需要先以管理員身分登入")]
    Unauthenticated,
    #[error("目前密碼不正確")]
    InvalidCurrentPassword,
    #[error("新密碼不得與目前密碼相同")]
    SamePassword,
    #[error("{0}")]
    InvalidNewPassword(String),
    #[error("{0}")]
    Storage(#[from] GatewayError),
}

impl AdminState {
    pub fn from_env(data_dir: &Path) -> Result<Self, GatewayError> {
        let password_path = admin_password_path(data_dir);
        let bootstrap = match env::var("M365_ADMIN_PASSWORD_BOOTSTRAP_FILE") {
            Ok(path) if !path.trim().is_empty() => fs::read(path)
                .ok()
                .map(|bytes| String::from_utf8_lossy(&bytes).trim().to_owned())
                .filter(|value| !value.is_empty()),
            _ => env::var("M365_ADMIN_PASSWORD")
                .ok()
                .map(|value| value.trim().to_owned())
                .filter(|value| !value.is_empty()),
        };
        Self::open(password_path, bootstrap)
    }

    fn open(password_path: PathBuf, bootstrap: Option<String>) -> Result<Self, GatewayError> {
        let consumed_path =
            PathBuf::from(format!("{}.bootstrap-consumed", password_path.display()));
        let (password, mode) = match fs::read(&password_path) {
            Ok(bytes) => {
                let password = String::from_utf8_lossy(&bytes).trim().to_owned();
                if password.is_empty() {
                    (String::new(), CredentialMode::Unavailable)
                } else {
                    (password, CredentialMode::Persisted)
                }
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                if consumed_path.exists() {
                    (String::new(), CredentialMode::Unavailable)
                } else if let Some(password) = bootstrap {
                    (password, CredentialMode::Bootstrap)
                } else {
                    (String::new(), CredentialMode::Unavailable)
                }
            }
            Err(error) => {
                return Err(GatewayError::Storage(format!(
                    "{}: {error}",
                    password_path.display()
                )));
            }
        };
        Ok(Self {
            password_path,
            consumed_path,
            inner: std::sync::Mutex::new(AdminInner {
                password,
                mode,
                must_change_password: mode == CredentialMode::Bootstrap,
                sessions: HashMap::new(),
                login_attempts: HashMap::new(),
            }),
        })
    }

    #[cfg(test)]
    pub(crate) fn open_for_test(
        password_path: PathBuf,
        bootstrap: Option<String>,
    ) -> Result<Self, GatewayError> {
        Self::open(password_path, bootstrap)
    }

    pub fn credential_available(&self) -> bool {
        let inner = self.inner.lock().expect("administrator state poisoned");
        !inner.password.is_empty() && inner.mode != CredentialMode::Unavailable
    }

    pub fn must_change_password(&self) -> bool {
        self.inner
            .lock()
            .expect("administrator state poisoned")
            .must_change_password
    }

    pub fn login(
        &self,
        password: &str,
        ip: &str,
        now: OffsetDateTime,
    ) -> Result<LoginSuccess, AdminError> {
        let mut inner = self.inner.lock().expect("administrator state poisoned");
        if let Some(wait) = login_wait(inner.login_attempts.get(ip), now) {
            return Err(AdminError::RateLimited {
                retry_after_seconds: wait.whole_seconds().max(0) + 1,
            });
        }
        if inner.password.is_empty()
            || matches!(
                inner.mode,
                CredentialMode::Unavailable | CredentialMode::BootstrapConsumed
            )
        {
            return Err(AdminError::Unavailable);
        }
        if !constant_time_eq(password.as_bytes(), inner.password.as_bytes()) {
            record_login_failure(&mut inner.login_attempts, ip, now);
            return Err(AdminError::InvalidCredential);
        }

        let mut bytes = [0_u8; 32];
        rand::rng().fill(&mut bytes);
        let token = URL_SAFE_NO_PAD.encode(bytes);
        if inner.mode == CredentialMode::Bootstrap {
            if !private_file::create_marker(
                &self.consumed_path,
                "m365-admin-bootstrap-consumed-v1\n",
            )? {
                inner.password.clear();
                inner.mode = CredentialMode::Unavailable;
                inner.must_change_password = false;
                inner.sessions.clear();
                return Err(AdminError::Unavailable);
            }
            inner.mode = CredentialMode::BootstrapConsumed;
            inner.must_change_password = true;
            inner.sessions.clear();
        }
        let must_change_password = inner.must_change_password;
        inner.sessions.insert(
            token.clone(),
            Session {
                created_at: now,
                last_seen_at: now,
                expires_at: now + SESSION_ABSOLUTE,
            },
        );
        inner.login_attempts.remove(ip);
        Ok(LoginSuccess {
            token,
            must_change_password,
        })
    }

    pub fn valid_session(&self, token: &str, now: OffsetDateTime) -> bool {
        let mut inner = self.inner.lock().expect("administrator state poisoned");
        let valid = inner.sessions.get(token).is_some_and(|session| {
            session.created_at != OffsetDateTime::UNIX_EPOCH
                && session.last_seen_at != OffsetDateTime::UNIX_EPOCH
                && session.expires_at != OffsetDateTime::UNIX_EPOCH
                && now < session.expires_at
                && now - session.last_seen_at < SESSION_IDLE
        });
        if !valid {
            inner.sessions.remove(token);
            return false;
        }
        inner.sessions.get_mut(token).unwrap().last_seen_at = now;
        true
    }

    pub fn logout(&self, token: &str) {
        self.inner
            .lock()
            .expect("administrator state poisoned")
            .sessions
            .remove(token);
    }

    pub fn change_password(
        &self,
        token: &str,
        current: &str,
        new_password: &str,
        now: OffsetDateTime,
    ) -> Result<(), AdminError> {
        if !self.valid_session(token, now) {
            return Err(AdminError::Unauthenticated);
        }
        let mut inner = self.inner.lock().expect("administrator state poisoned");
        if inner.password.is_empty() || inner.mode == CredentialMode::Unavailable {
            return Err(AdminError::Unavailable);
        }
        if !constant_time_eq(current.as_bytes(), inner.password.as_bytes()) {
            return Err(AdminError::InvalidCurrentPassword);
        }
        if constant_time_eq(new_password.as_bytes(), inner.password.as_bytes()) {
            return Err(AdminError::SamePassword);
        }
        if new_password.chars().count() < 6 {
            return Err(AdminError::InvalidNewPassword(
                "新密碼至少需要 6 個字元".to_owned(),
            ));
        }
        if new_password.chars().count() > 256 {
            return Err(AdminError::InvalidNewPassword("新密碼過長".to_owned()));
        }
        private_file::write_text(&self.password_path, &format!("{new_password}\n"))?;
        inner.password = new_password.to_owned();
        inner.mode = CredentialMode::Persisted;
        inner.must_change_password = false;
        inner.sessions.clear();
        Ok(())
    }
}

fn admin_password_path(data_dir: &Path) -> PathBuf {
    if let Some(path) = env::var_os("M365_ADMIN_PASSWORD_FILE").filter(|value| !value.is_empty()) {
        return path.into();
    }
    if env::var_os("M365_DATA_DIR").is_some() {
        return data_dir.join("admin-password");
    }
    if let Some(path) = env::var_os("M365_CONFIG").filter(|value| !value.is_empty()) {
        let path = PathBuf::from(path);
        return path
            .parent()
            .unwrap_or_else(|| Path::new("."))
            .join("admin-password");
    }
    data_dir.join("admin-password")
}

fn login_wait(attempt: Option<&LoginAttempt>, now: OffsetDateTime) -> Option<Duration> {
    attempt?
        .locked_until
        .filter(|locked_until| now < *locked_until)
        .map(|locked_until| locked_until - now)
}

fn record_login_failure(
    attempts: &mut HashMap<String, LoginAttempt>,
    ip: &str,
    now: OffsetDateTime,
) {
    if !attempts.contains_key(ip) && attempts.len() >= MAX_LOGIN_ATTEMPTS {
        attempts.retain(|_, attempt| {
            attempt
                .window_start
                .is_some_and(|start| now - start <= LOGIN_WINDOW)
                || attempt
                    .locked_until
                    .is_some_and(|locked_until| now <= locked_until)
        });
        if attempts.len() >= MAX_LOGIN_ATTEMPTS {
            return;
        }
    }
    let attempt = attempts.entry(ip.to_owned()).or_default();
    if attempt
        .window_start
        .is_none_or(|start| now - start > LOGIN_WINDOW)
    {
        *attempt = LoginAttempt {
            window_start: Some(now),
            ..LoginAttempt::default()
        };
    }
    attempt.failures = attempt.failures.saturating_add(1);
    if attempt.failures >= 5 {
        attempt.locked_until = Some(now + LOGIN_LOCK);
    }
}

fn constant_time_eq(left: &[u8], right: &[u8]) -> bool {
    let size = left.len().max(right.len());
    let mut difference = left.len() ^ right.len();
    for index in 0..size {
        difference |= usize::from(left.get(index).copied().unwrap_or_default())
            ^ usize::from(right.get(index).copied().unwrap_or_default());
    }
    difference == 0
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct AllowedHost {
    host: String,
    port: Option<u16>,
}

impl AllowedHost {
    fn loopback(&self) -> bool {
        self.host == "localhost"
            || self
                .host
                .parse::<IpAddr>()
                .is_ok_and(|address| address.is_loopback())
    }
}

#[derive(Clone, Copy, Debug)]
struct TrustedNetwork {
    network: u128,
    prefix: u8,
    bits: u8,
}

impl TrustedNetwork {
    fn contains(self, address: IpAddr) -> bool {
        let (value, bits) = address_value(address);
        bits == self.bits && masked(value, self.prefix, bits) == self.network
    }
}

#[derive(Clone, Debug, Default)]
pub struct AdminSecurityPolicy {
    allowed_hosts: Vec<AllowedHost>,
    trusted_proxies: Vec<TrustedNetwork>,
}

#[derive(Clone, Debug)]
pub struct AdminRequestInfo {
    pub scheme: String,
    pub client_ip: IpAddr,
    pub secure: bool,
    pub local_console: bool,
    authority: AllowedHost,
}

impl AdminSecurityPolicy {
    pub fn from_env() -> Result<Self, GatewayError> {
        let allowed_hosts = split_env("M365_ADMIN_ALLOWED_HOSTS")
            .into_iter()
            .map(|raw| parse_authority(&raw))
            .collect::<Result<Vec<_>, _>>()?;
        let trusted_proxies = split_env("M365_ADMIN_TRUSTED_PROXIES")
            .into_iter()
            .map(|raw| parse_trusted_network(&raw))
            .collect::<Result<Vec<_>, _>>()?;
        Ok(Self {
            allowed_hosts,
            trusted_proxies,
        })
    }

    pub fn inspect(
        &self,
        headers: &HeaderMap,
        uri: &Uri,
        direct_peer: SocketAddr,
    ) -> Result<AdminRequestInfo, GatewayError> {
        let peer = direct_peer.ip();
        let trusted = self
            .trusted_proxies
            .iter()
            .any(|network| network.contains(peer));
        let (scheme, authority, client_ip, secure) = if trusted {
            let proto = one_header(headers, "x-forwarded-proto")?;
            if !proto.eq_ignore_ascii_case("https") {
                return Err(security(
                    "受信任 Proxy 必須提供單一 HTTPS forwarded protocol",
                ));
            }
            let host = one_header(headers, "x-forwarded-host")?;
            let authority = parse_authority(host)?;
            let forwarded = headers
                .get_all("x-forwarded-for")
                .iter()
                .filter_map(|value| value.to_str().ok())
                .flat_map(|value| value.split(','))
                .next_back()
                .and_then(|value| value.trim().parse::<IpAddr>().ok())
                .ok_or_else(|| security("受信任 Proxy 必須提供有效的 forwarded client 位址"))?;
            ("https".to_owned(), authority, forwarded, true)
        } else {
            let host = headers
                .get(header::HOST)
                .and_then(|value| value.to_str().ok())
                .or_else(|| uri.authority().map(|authority| authority.as_str()))
                .ok_or_else(|| security("管理 Host 無效"))?;
            let authority = parse_authority(host)?;
            let scheme = uri.scheme_str().unwrap_or("http").to_owned();
            let secure = scheme == "https";
            (scheme, authority, peer, secure)
        };
        let local_console = peer.is_loopback() && client_ip.is_loopback() && authority.loopback();
        if !local_console && !secure {
            return Err(security("非 loopback 管理介面必須使用 HTTPS"));
        }
        if !local_console && !self.host_allowed(&authority) {
            return Err(security("管理 Host 不在允許清單中"));
        }
        Ok(AdminRequestInfo {
            scheme,
            client_ip,
            secure,
            local_console,
            authority,
        })
    }

    fn host_allowed(&self, candidate: &AllowedHost) -> bool {
        self.allowed_hosts.iter().any(|allowed| {
            allowed.host == candidate.host
                && (allowed.port.is_none()
                    || allowed.port == candidate.port
                    || (allowed.port == Some(443) && candidate.port.is_none()))
        })
    }
}

pub fn management_security_bypass(path: &str) -> bool {
    let path = clean_path(path);
    path == "/internal/hindsight/webhook"
        || path == "/v1"
        || path.starts_with("/v1/")
        || path == "/hermes/v1"
        || path.starts_with("/hermes/v1/")
        || path == "/memory/v1"
        || path.starts_with("/memory/v1/")
}

pub fn validate_origin(
    method: &Method,
    headers: &HeaderMap,
    info: &AdminRequestInfo,
) -> Result<(), GatewayError> {
    if matches!(*method, Method::GET | Method::HEAD | Method::OPTIONS) {
        return Ok(());
    }
    let raw = one_header(headers, "origin")?;
    if raw == "null" || raw.chars().any(|ch| ch.is_whitespace() || ch == ',') {
        return Err(security("Origin 無效"));
    }
    let parsed = Url::parse(raw).map_err(|_| security("Origin 無效"))?;
    if !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
        || !matches!(parsed.scheme(), "http" | "https")
        || !raw.split_once("://").is_some_and(|(_, authority)| {
            !authority.is_empty() && !authority.chars().any(|ch| "/?#".contains(ch))
        })
    {
        return Err(security("Origin 無效"));
    }
    let authority = parse_authority(
        parsed
            .host_str()
            .map(|host| match parsed.port() {
                Some(port) if host.contains(':') => format!("[{host}]:{port}"),
                Some(port) => format!("{host}:{port}"),
                None => host.to_owned(),
            })
            .as_deref()
            .ok_or_else(|| security("Origin Host 無效"))?,
    )?;
    if parsed.scheme() != info.scheme
        || origin_key(&authority, parsed.scheme()) != origin_key(&info.authority, &info.scheme)
    {
        return Err(security("Origin 與管理 Host 不相符"));
    }
    Ok(())
}

fn split_env(name: &str) -> Vec<String> {
    env::var(name)
        .unwrap_or_default()
        .split(|ch: char| ch == ',' || ch == ';' || ch.is_whitespace())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_owned)
        .collect()
}

fn parse_authority(raw: &str) -> Result<AllowedHost, GatewayError> {
    let raw = raw.trim();
    if raw.is_empty()
        || raw.contains("://")
        || raw
            .chars()
            .any(|ch| ch.is_whitespace() || "/\\?#@*%".contains(ch))
    {
        return Err(security(format!("管理 Host {raw:?} 無效")));
    }
    let (host, port) = if let Ok(address) = raw.trim_matches(['[', ']']).parse::<IpAddr>() {
        (address.to_string(), None)
    } else if let Some(rest) = raw.strip_prefix('[') {
        let (host, suffix) = rest
            .split_once(']')
            .ok_or_else(|| security(format!("管理 Host {raw:?} 無效")))?;
        let port = suffix
            .strip_prefix(':')
            .filter(|value| !value.is_empty())
            .map(parse_port)
            .transpose()?;
        if !suffix.is_empty() && port.is_none() {
            return Err(security(format!("管理 Host {raw:?} 無效")));
        }
        let address = host
            .parse::<IpAddr>()
            .map_err(|_| security(format!("管理 Host {raw:?} 無效")))?;
        (address.to_string(), port)
    } else if raw.matches(':').count() == 1 {
        let (host, port) = raw.rsplit_once(':').unwrap();
        (host.to_owned(), Some(parse_port(port)?))
    } else if raw.contains(':') {
        return Err(security(format!("管理 Host {raw:?} 無效")));
    } else {
        (raw.to_owned(), None)
    };
    let host = host.trim().trim_end_matches('.').to_ascii_lowercase();
    if host.is_empty() {
        return Err(security(format!("管理 Host {raw:?} 無效")));
    }
    Ok(AllowedHost { host, port })
}

fn parse_port(raw: &str) -> Result<u16, GatewayError> {
    raw.parse::<u16>()
        .ok()
        .filter(|port| *port > 0)
        .ok_or_else(|| security("管理 Host 的連接埠無效"))
}

fn parse_trusted_network(raw: &str) -> Result<TrustedNetwork, GatewayError> {
    let (address, prefix) = if let Some((address, prefix)) = raw.split_once('/') {
        let address = address
            .parse::<IpAddr>()
            .map_err(|_| security(format!("受信任 Proxy {raw:?} 無效")))?;
        let prefix = prefix
            .parse::<u8>()
            .map_err(|_| security(format!("受信任 Proxy {raw:?} 無效")))?;
        (address, prefix)
    } else {
        let address = raw
            .parse::<IpAddr>()
            .map_err(|_| security(format!("受信任 Proxy {raw:?} 無效")))?;
        let prefix = if address.is_ipv4() { 32 } else { 128 };
        (address, prefix)
    };
    let (value, bits) = address_value(address);
    if prefix > bits {
        return Err(security(format!("受信任 Proxy {raw:?} 無效")));
    }
    let network = masked(value, prefix, bits);
    let start = value_address(network, bits);
    let end = value_address(network | !mask(prefix, bits), bits);
    if !start.is_loopback() || !end.is_loopback() || (bits == 128 && prefix != 128) {
        return Err(security(format!(
            "受信任 Proxy 範圍 {raw:?} 未完全位於 loopback"
        )));
    }
    Ok(TrustedNetwork {
        network,
        prefix,
        bits,
    })
}

fn address_value(address: IpAddr) -> (u128, u8) {
    match address {
        IpAddr::V4(address) => (u32::from(address) as u128, 32),
        IpAddr::V6(address) => (u128::from(address), 128),
    }
}

fn value_address(value: u128, bits: u8) -> IpAddr {
    if bits == 32 {
        IpAddr::V4(Ipv4Addr::from(value as u32))
    } else {
        IpAddr::V6(value.into())
    }
}

fn mask(prefix: u8, bits: u8) -> u128 {
    if prefix == 0 {
        0
    } else if bits == 32 {
        u128::from(u32::MAX << (32 - prefix))
    } else {
        u128::MAX << (128 - prefix)
    }
}

fn masked(value: u128, prefix: u8, bits: u8) -> u128 {
    value & mask(prefix, bits)
}

fn one_header<'a>(headers: &'a HeaderMap, name: &str) -> Result<&'a str, GatewayError> {
    let mut values = headers.get_all(name).iter();
    let value = values
        .next()
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty() && !value.contains(','))
        .ok_or_else(|| security(format!("{name} 必須恰好出現一次")))?;
    if values.next().is_some() {
        return Err(security(format!("{name} 必須恰好出現一次")));
    }
    Ok(value)
}

fn origin_key(authority: &AllowedHost, scheme: &str) -> String {
    let port = authority
        .port
        .unwrap_or(if scheme == "https" { 443 } else { 80 });
    format!("{}:{port}", authority.host)
}

fn clean_path(path: &str) -> String {
    let mut parts = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                parts.pop();
            }
            value => parts.push(value),
        }
    }
    format!("/{}", parts.join("/"))
}

fn security(message: impl Into<String>) -> GatewayError {
    GatewayError::InvalidRequest(message.into())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn now() -> OffsetDateTime {
        OffsetDateTime::from_unix_timestamp(1_800_000_000).unwrap()
    }

    #[test]
    fn bootstrap_is_consumed_once_and_requires_rotation() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("admin-password");
        let state = AdminState::open(path.clone(), Some("one-time-secret".to_owned())).unwrap();
        let login = state.login("one-time-secret", "127.0.0.1", now()).unwrap();
        assert!(login.must_change_password);
        assert!(path.with_extension("bootstrap-consumed").exists() || state.consumed_path.exists());
        assert!(matches!(
            state.login("one-time-secret", "127.0.0.2", now()),
            Err(AdminError::Unavailable)
        ));
        state
            .change_password(&login.token, "one-time-secret", "durable-password", now())
            .unwrap();
        assert!(!state.valid_session(&login.token, now()));
        let reopened = AdminState::open(path, Some("one-time-secret".to_owned())).unwrap();
        assert!(
            reopened
                .login("durable-password", "127.0.0.1", now())
                .is_ok()
        );
    }

    #[test]
    fn persisted_file_is_authoritative_even_when_empty() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("admin-password");
        fs::write(&path, "\n").unwrap();
        let state = AdminState::open(path, Some("bootstrap".to_owned())).unwrap();
        assert!(!state.credential_available());
    }

    #[test]
    fn fifth_failure_locks_login_for_fifteen_minutes() {
        let root = tempfile::tempdir().unwrap();
        let state = AdminState::open(
            root.path().join("admin-password"),
            Some("correct-password".to_owned()),
        )
        .unwrap();
        for _ in 0..5 {
            assert!(matches!(
                state.login("wrong", "192.0.2.1", now()),
                Err(AdminError::InvalidCredential)
            ));
        }
        assert!(matches!(
            state.login("correct-password", "192.0.2.1", now()),
            Err(AdminError::RateLimited { .. })
        ));
        assert!(
            state
                .login(
                    "correct-password",
                    "192.0.2.1",
                    now() + Duration::minutes(16)
                )
                .is_ok()
        );
    }

    #[test]
    fn management_security_requires_https_host_and_matching_origin() {
        let policy = AdminSecurityPolicy {
            allowed_hosts: vec![parse_authority("admin.example.test").unwrap()],
            trusted_proxies: Vec::new(),
        };
        let mut headers = HeaderMap::new();
        headers.insert(header::HOST, "admin.example.test".parse().unwrap());
        let peer = "198.51.100.20:5000".parse().unwrap();
        assert!(
            policy
                .inspect(
                    &headers,
                    &"http://admin.example.test/".parse().unwrap(),
                    peer
                )
                .is_err()
        );
        let info = policy
            .inspect(
                &headers,
                &"https://admin.example.test/".parse().unwrap(),
                peer,
            )
            .unwrap();
        headers.insert(header::ORIGIN, "https://evil.example.test".parse().unwrap());
        assert!(validate_origin(&Method::POST, &headers, &info).is_err());
        headers.insert(
            header::ORIGIN,
            "https://admin.example.test".parse().unwrap(),
        );
        assert!(validate_origin(&Method::POST, &headers, &info).is_ok());
    }

    #[test]
    fn trusted_proxy_ranges_must_be_entirely_loopback() {
        assert!(parse_trusted_network("127.0.0.0/8").is_ok());
        assert!(parse_trusted_network("127.0.0.0/7").is_err());
        assert!(parse_trusted_network("::1/128").is_ok());
        assert!(parse_trusted_network("::1/127").is_err());
    }

    #[test]
    fn trusted_proxy_requires_exact_forwarded_authority_and_origin() {
        let policy = AdminSecurityPolicy {
            allowed_hosts: vec![parse_authority("admin.example.test").unwrap()],
            trusted_proxies: vec![parse_trusted_network("127.0.0.1/32").unwrap()],
        };
        let peer = "127.0.0.1:5000".parse().unwrap();
        let uri = "http://127.0.0.1/admin".parse().unwrap();
        let mut headers = HeaderMap::new();
        headers.insert("x-forwarded-proto", "https".parse().unwrap());
        headers.insert("x-forwarded-host", "admin.example.test".parse().unwrap());
        headers.insert("x-forwarded-for", "198.51.100.20".parse().unwrap());
        headers.insert(
            header::ORIGIN,
            "https://admin.example.test".parse().unwrap(),
        );

        let info = policy.inspect(&headers, &uri, peer).unwrap();
        assert!(validate_origin(&Method::POST, &headers, &info).is_ok());

        let mut duplicate_host = headers.clone();
        duplicate_host.append("x-forwarded-host", "admin.example.test".parse().unwrap());
        assert!(policy.inspect(&duplicate_host, &uri, peer).is_err());

        let mut insecure = headers.clone();
        insecure.insert("x-forwarded-proto", "http".parse().unwrap());
        assert!(policy.inspect(&insecure, &uri, peer).is_err());

        let mut wrong_host = headers.clone();
        wrong_host.insert("x-forwarded-host", "evil.example.test".parse().unwrap());
        assert!(policy.inspect(&wrong_host, &uri, peer).is_err());

        let mut duplicate_origin = headers.clone();
        duplicate_origin.append(
            header::ORIGIN,
            "https://admin.example.test".parse().unwrap(),
        );
        assert!(validate_origin(&Method::POST, &duplicate_origin, &info).is_err());
    }
}
