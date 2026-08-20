use std::{
    future::Future,
    net::{Ipv4Addr, SocketAddrV4, TcpStream},
    path::{Path, PathBuf},
    pin::Pin,
    process::Stdio,
    time::Duration,
};

use futures_util::{SinkExt, StreamExt};
use serde::Deserialize;
use serde_json::{Value, json};
use tokio::{
    process::{Child, Command},
    time::Instant,
};
use tokio_tungstenite::{connect_async, tungstenite::Message};
use url::Url;

#[derive(Clone)]
pub struct Capture {
    pub code: String,
    pub state: String,
    pub error: String,
    pub teams_code: String,
    pub teams_verifier: String,
}

pub type CaptureFuture<'a> = Pin<Box<dyn Future<Output = Result<Capture, String>> + Send + 'a>>;

pub trait Runner: Send + Sync {
    fn capture<'a>(
        &'a self,
        authorization_url: String,
        redirect_uri: String,
        state: String,
        profile_dir: PathBuf,
    ) -> CaptureFuture<'a>;
}

pub struct LiveRunner;

impl Runner for LiveRunner {
    fn capture<'a>(
        &'a self,
        authorization_url: String,
        redirect_uri: String,
        state: String,
        profile_dir: PathBuf,
    ) -> CaptureFuture<'a> {
        Box::pin(
            async move { capture(&authorization_url, &redirect_uri, &state, &profile_dir).await },
        )
    }
}

#[cfg(test)]
pub struct DisabledRunner;

#[cfg(test)]
impl Runner for DisabledRunner {
    fn capture<'a>(&'a self, _: String, _: String, _: String, _: PathBuf) -> CaptureFuture<'a> {
        Box::pin(async { Err("browser capture disabled for test".to_owned()) })
    }
}

pub async fn capture(
    authorization_url: &str,
    redirect_uri: &str,
    state: &str,
    profile_dir: &Path,
) -> Result<Capture, String> {
    validate_authorization_url(authorization_url)?;
    validate_redirect_uri(redirect_uri)?;
    prepare_profile(profile_dir)?;
    let executable = browser_executable()?;
    let mut child = Command::new(executable)
        .arg("--remote-debugging-port=0")
        .arg(format!("--user-data-dir={}", profile_dir.display()))
        .args([
            "--no-first-run",
            "--no-default-browser-check",
            "about:blank",
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| format!("start browser: {error}"))?;
    let result = tokio::time::timeout(
        Duration::from_secs(600),
        capture_from_browser(
            &mut child,
            profile_dir,
            authorization_url,
            redirect_uri,
            state,
        ),
    )
    .await;
    stop_browser(&mut child).await;
    result.map_err(|_| "browser authorization timed out".to_owned())?
}

async fn capture_from_browser(
    child: &mut Child,
    profile_dir: &Path,
    authorization_url: &str,
    redirect_uri: &str,
    state: &str,
) -> Result<Capture, String> {
    let (port, _) = wait_for_devtools(child, &profile_dir.join("DevToolsActivePort")).await?;
    let page_url = wait_for_page(child, port).await?;
    let (mut socket, _) = connect_async(&page_url)
        .await
        .map_err(|error| format!("connect browser devtools: {error}"))?;
    for (id, method) in [(1, "Network.enable"), (2, "Page.enable")] {
        socket
            .send(Message::Text(
                json!({"id":id,"method":method,"params":{}})
                    .to_string()
                    .into(),
            ))
            .await
            .map_err(|error| format!("send browser devtools command: {error}"))?;
        wait_for_ack(&mut socket, id).await?;
    }
    socket
        .send(Message::Text(
            json!({"id":3,"method":"Page.navigate","params":{"url":authorization_url}})
                .to_string()
                .into(),
        ))
        .await
        .map_err(|error| format!("navigate browser: {error}"))?;
    let mut capture = loop {
        let Some(message) = socket.next().await else {
            return Err("browser closed before OAuth callback".to_owned());
        };
        let message = message.map_err(|error| format!("read browser devtools event: {error}"))?;
        let Message::Text(text) = message else {
            continue;
        };
        let Ok(value) = serde_json::from_str::<Value>(&text) else {
            continue;
        };
        let candidate = event_url(&value);
        if let Some(capture) = callback_capture(candidate, redirect_uri, state) {
            break capture;
        }
    };
    if !capture.error.is_empty() {
        return Ok(capture);
    }

    let teams_state = crate::auth::verifier();
    let teams_verifier = crate::auth::verifier();
    let teams_url = crate::auth::teams_authorization_url(&teams_state, &teams_verifier)
        .map_err(|_| "build Teams authorization URL".to_owned())?;
    socket
        .send(Message::Text(
            json!({"id":4,"method":"Page.navigate","params":{"url":teams_url.as_str()}})
                .to_string()
                .into(),
        ))
        .await
        .map_err(|error| format!("navigate browser for Teams authorization: {error}"))?;
    while let Some(message) = socket.next().await {
        let message = message.map_err(|error| format!("read browser devtools event: {error}"))?;
        let Message::Text(text) = message else {
            continue;
        };
        let Ok(value) = serde_json::from_str::<Value>(&text) else {
            continue;
        };
        let Some(teams) = callback_capture(
            event_url(&value),
            crate::auth::TEAMS_REDIRECT_URI,
            &teams_state,
        ) else {
            continue;
        };
        if !teams.error.is_empty() {
            return Err("browser Teams authorization failed".to_owned());
        }
        capture.teams_code = teams.code;
        capture.teams_verifier = teams_verifier;
        return Ok(capture);
    }
    Err("browser closed before Teams authorization callback".to_owned())
}

fn event_url(value: &Value) -> &str {
    match value.get("method").and_then(Value::as_str) {
        Some("Network.requestWillBeSent") => value.pointer("/params/request/url"),
        Some("Page.frameRequestedNavigation") => value.pointer("/params/url"),
        _ => None,
    }
    .and_then(Value::as_str)
    .unwrap_or_default()
}

async fn wait_for_ack<S>(socket: &mut S, expected: i64) -> Result<(), String>
where
    S: StreamExt<Item = Result<Message, tokio_tungstenite::tungstenite::Error>> + Unpin,
{
    while let Some(message) = socket.next().await {
        let Message::Text(text) = message.map_err(|error| error.to_string())? else {
            continue;
        };
        let Ok(value) = serde_json::from_str::<Value>(&text) else {
            continue;
        };
        if value.get("id").and_then(Value::as_i64) != Some(expected) {
            continue;
        }
        if value.get("error").is_some() {
            return Err("browser devtools command failed".to_owned());
        }
        return Ok(());
    }
    Err("browser closed before devtools acknowledgement".to_owned())
}

async fn wait_for_devtools(child: &mut Child, path: &Path) -> Result<(u16, String), String> {
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if let Ok(raw) = tokio::fs::read_to_string(path).await {
            let mut lines = raw.lines();
            if let (Some(port), Some(socket)) = (lines.next(), lines.next())
                && let Ok(port) = port.trim().parse::<u16>()
                && socket.trim().starts_with('/')
            {
                return Ok((port, format!("ws://127.0.0.1:{port}{}", socket.trim())));
            }
        }
        if child
            .try_wait()
            .map_err(|error| error.to_string())?
            .is_some()
        {
            return Err("browser exited before devtools became ready".to_owned());
        }
        if Instant::now() >= deadline {
            return Err("browser devtools startup timed out".to_owned());
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Target {
    #[serde(rename = "type")]
    kind: String,
    url: String,
    web_socket_debugger_url: String,
}

async fn wait_for_page(child: &mut Child, port: u16) -> Result<String, String> {
    let deadline = Instant::now() + Duration::from_secs(15);
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(1))
        .build()
        .map_err(|error| error.to_string())?;
    loop {
        if let Ok(response) = client
            .get(format!("http://127.0.0.1:{port}/json/list"))
            .send()
            .await
            && let Ok(targets) = response.json::<Vec<Target>>().await
            && let Some(target) = targets
                .iter()
                .filter(|target| {
                    target.kind == "page" && !target.web_socket_debugger_url.is_empty()
                })
                .min_by_key(|target| {
                    !matches!(target.url.as_str(), "about:blank" | "chrome://newtab/")
                })
        {
            return Ok(target.web_socket_debugger_url.clone());
        }
        if child
            .try_wait()
            .map_err(|error| error.to_string())?
            .is_some()
        {
            return Err("browser exited before page target became ready".to_owned());
        }
        if Instant::now() >= deadline {
            return Err("browser page target startup timed out".to_owned());
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

fn callback_capture(candidate: &str, redirect_uri: &str, expected_state: &str) -> Option<Capture> {
    let candidate = Url::parse(candidate).ok()?;
    let redirect = Url::parse(redirect_uri).ok()?;
    if candidate.scheme() != redirect.scheme()
        || candidate.host_str() != redirect.host_str()
        || candidate.port_or_known_default() != redirect.port_or_known_default()
        || !candidate.username().is_empty()
        || candidate.password().is_some()
        || !callback_path_matches(candidate.path(), &redirect)
    {
        return None;
    }
    let values = candidate
        .query_pairs()
        .collect::<std::collections::HashMap<_, _>>();
    let state = values.get("state")?.as_ref();
    if state != expected_state {
        return None;
    }
    let code = values
        .get("code")
        .map(|value| value.to_string())
        .unwrap_or_default();
    let error = values
        .get("error")
        .map(|value| value.to_string())
        .unwrap_or_default();
    if code.is_empty() == error.is_empty() {
        return None;
    }
    Some(Capture {
        code,
        state: state.to_owned(),
        error,
        teams_code: String::new(),
        teams_verifier: String::new(),
    })
}

fn callback_path_matches(candidate: &str, redirect: &Url) -> bool {
    candidate == redirect.path()
        || (redirect.as_str() == crate::auth::TEAMS_REDIRECT_URI && candidate == "/v2/")
}

fn validate_authorization_url(raw: &str) -> Result<(), String> {
    let url = Url::parse(raw).map_err(|_| "invalid browser authorization URL".to_owned())?;
    if url.scheme() != "https"
        || url.host_str() != Some("login.microsoftonline.com")
        || !url.path().ends_with("/oauth2/v2.0/authorize")
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
    {
        return Err(
            "browser authorization URL is not the supported Microsoft authorize endpoint"
                .to_owned(),
        );
    }
    Ok(())
}

fn validate_redirect_uri(raw: &str) -> Result<(), String> {
    let url = Url::parse(raw).map_err(|_| "invalid browser redirect URI".to_owned())?;
    if url.as_str() != crate::auth::DEFAULT_REDIRECT_URI {
        return Err(
            "browser redirect URI is not the supported Microsoft nativeclient redirect".to_owned(),
        );
    }
    Ok(())
}

fn prepare_profile(path: &Path) -> Result<(), String> {
    std::fs::create_dir_all(path).map_err(|error| error.to_string())?;
    secure_directory(path)?;
    let marker = path.join("DevToolsActivePort");
    if let Ok(raw) = std::fs::read_to_string(&marker)
        && let Some(port) = raw
            .lines()
            .next()
            .and_then(|value| value.parse::<u16>().ok())
        && TcpStream::connect_timeout(
            &SocketAddrV4::new(Ipv4Addr::LOCALHOST, port).into(),
            Duration::from_millis(250),
        )
        .is_ok()
    {
        return Err("browser profile is already active".to_owned());
    }
    match std::fs::remove_file(marker) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(_) => Err("remove stale browser devtools marker".to_owned()),
    }
}

fn browser_executable() -> Result<PathBuf, String> {
    if let Some(path) = std::env::var_os("M365_BROWSER_PATH").map(PathBuf::from) {
        return path
            .is_file()
            .then_some(path)
            .ok_or_else(|| "configured browser executable is unavailable".to_owned());
    }
    let candidates = [
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
        "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
        "/Applications/Chromium.app/Contents/MacOS/Chromium",
        "/usr/bin/google-chrome",
        "/usr/bin/chromium",
        "/usr/bin/chromium-browser",
    ];
    candidates
        .into_iter()
        .map(PathBuf::from)
        .find(|path| path.is_file())
        .ok_or_else(|| "no CDP-capable browser was found".to_owned())
}

async fn stop_browser(child: &mut Child) {
    if child.try_wait().ok().flatten().is_none() {
        let _ = child.kill().await;
    }
}

#[cfg(unix)]
fn secure_directory(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))
        .map_err(|error| error.to_string())
}

#[cfg(not(unix))]
fn secure_directory(_: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn callback_capture_requires_exact_redirect_and_state() {
        let redirect = crate::auth::DEFAULT_REDIRECT_URI;
        let captured = callback_capture(
            &format!("{redirect}?code=secret&state=expected"),
            redirect,
            "expected",
        )
        .unwrap();
        assert_eq!(captured.code, "secret");
        assert!(
            callback_capture(
                &format!("{redirect}?code=secret&state=spoof"),
                redirect,
                "expected"
            )
            .is_none()
        );
        assert!(
            callback_capture(
                "https://example.invalid/?code=secret&state=expected",
                redirect,
                "expected"
            )
            .is_none()
        );
    }

    #[test]
    fn teams_callback_accepts_the_canonical_trailing_slash_and_exact_state() {
        let captured = callback_capture(
            "https://teams.microsoft.com/v2/?code=secret&state=expected",
            crate::auth::TEAMS_REDIRECT_URI,
            "expected",
        )
        .unwrap();
        assert_eq!(captured.code, "secret");
        assert!(
            callback_capture(
                "https://teams.microsoft.com/v2/?code=secret&state=spoof",
                crate::auth::TEAMS_REDIRECT_URI,
                "expected",
            )
            .is_none()
        );
        assert!(
            callback_capture(
                "https://teams.microsoft.com/v2/extra?code=secret&state=expected",
                crate::auth::TEAMS_REDIRECT_URI,
                "expected",
            )
            .is_none()
        );
    }

    #[test]
    fn browser_profile_setup_removes_a_stale_devtools_marker() {
        let root = tempfile::tempdir().unwrap();
        let marker = root.path().join("DevToolsActivePort");
        std::fs::write(&marker, "stale-port\nstale-socket\n").unwrap();

        prepare_profile(root.path()).unwrap();

        assert!(!marker.exists());
    }

    #[test]
    fn browser_profile_setup_preserves_a_live_devtools_marker() {
        let root = tempfile::tempdir().unwrap();
        let listener = std::net::TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).unwrap();
        let marker = root.path().join("DevToolsActivePort");
        std::fs::write(
            &marker,
            format!(
                "{}\n/devtools/browser/live\n",
                listener.local_addr().unwrap().port()
            ),
        )
        .unwrap();

        assert_eq!(
            prepare_profile(root.path()).unwrap_err(),
            "browser profile is already active"
        );
        assert!(marker.exists());
    }
}
