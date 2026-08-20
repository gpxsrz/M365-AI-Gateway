use std::{
    fs::{File, OpenOptions},
    io::{Read, Write},
    net::{IpAddr, Ipv4Addr, Ipv6Addr},
    path::{Path, PathBuf},
    time::Duration,
};

use base64::{Engine, engine::general_purpose::STANDARD};
use futures_util::{StreamExt, stream};
use rand::Rng;
use reqwest::{Client, StatusCode, Url, multipart};
use serde::Deserialize;
use serde_json::json;
use tokio::io::AsyncReadExt;

use crate::chathub::{Account, Attachment, ChatError};

const MAX_ATTACHMENTS: usize = 3;
const MAX_BYTES: u64 = 512 << 20;
const MAX_REDIRECTS: usize = 5;
const DOCUMENT_CHUNK: usize = 983_040;

pub async fn prepare(
    account: &Account,
    conversation_id: &str,
    attachments: &mut [Attachment],
) -> Result<(), ChatError> {
    if attachments.len() > MAX_ATTACHMENTS {
        return Err(protocol("active attachments exceed the shared limit of 3"));
    }
    for (index, attachment) in attachments.iter_mut().enumerate() {
        match attachment.kind.as_str() {
            "image" => {
                if !attachment.doc_id.is_empty()
                    && attachment.uploaded_conversation_id == conversation_id
                {
                    continue;
                }
                attachment.doc_id.clear();
                attachment.file_type.clear();
                attachment.uploaded_conversation_id.clear();
                upload_image(account, conversation_id, index, attachment).await?;
            }
            "file" => {
                if !attachment.doc_id.is_empty()
                    && !attachment.reference_url.is_empty()
                    && attachment.uploaded_conversation_id == conversation_id
                {
                    continue;
                }
                attachment.doc_id.clear();
                attachment.reference_url.clear();
                attachment.transport_name.clear();
                attachment.uploaded_conversation_id.clear();
                upload_document(account, conversation_id, attachment).await?;
            }
            _ => return Err(protocol("unsupported attachment type")),
        }
    }
    Ok(())
}

async fn upload_document(
    account: &Account,
    conversation_id: &str,
    attachment: &mut Attachment,
) -> Result<(), ChatError> {
    if account.graph_access_token.trim().is_empty() {
        return Err(protocol(
            "document upload requires Microsoft Graph authorization",
        ));
    }
    let spool = spool(&attachment.url, &attachment.mime_type, &attachment.name).await?;
    let transport_name = document_name(&spool.name);
    let create_url = format!(
        "https://graph.microsoft.com/v1.0/me/drive/special/copilotuploads:/{}:/createUploadSession",
        percent_encode_path(&transport_name)
    );
    let client = Client::builder()
        .timeout(Duration::from_secs(300))
        .build()
        .map_err(|_| protocol("document upload client is unavailable"))?;
    let response = client
        .post(create_url)
        .bearer_auth(&account.graph_access_token)
        .json(&json!({"item":{
            "@microsoft.graph.conflictBehavior":"replace",
            "name":transport_name
        }}))
        .send()
        .await
        .map_err(|_| protocol("create document upload session failed"))?;
    if !response.status().is_success() {
        return Err(protocol(&format!(
            "create document upload session returned HTTP {}",
            response.status().as_u16()
        )));
    }
    let session: UploadSession = response
        .json()
        .await
        .map_err(|_| protocol("document upload session returned invalid JSON"))?;
    validate_upload_url(&session.upload_url)?;

    let mut file = tokio::fs::File::open(&spool.path)
        .await
        .map_err(|_| protocol("cannot read the private document spool"))?;
    let mut offset = 0_u64;
    let mut ready = None;
    while offset < spool.size {
        let length = ((spool.size - offset) as usize).min(DOCUMENT_CHUNK);
        let mut chunk = vec![0_u8; length];
        file.read_exact(&mut chunk)
            .await
            .map_err(|_| protocol("cannot read the private document spool"))?;
        let end = offset + length as u64 - 1;
        let response = client
            .put(&session.upload_url)
            .header(reqwest::header::CONTENT_TYPE, "application/octet-stream")
            .header(
                reqwest::header::CONTENT_RANGE,
                format!("bytes {offset}-{end}/{}", spool.size),
            )
            .body(chunk)
            .send()
            .await
            .map_err(|_| protocol("document upload chunk failed"))?;
        if !response.status().is_success() {
            return Err(protocol(&format!(
                "document upload chunk returned HTTP {}",
                response.status().as_u16()
            )));
        }
        if end + 1 == spool.size {
            ready = Some(
                response
                    .json::<DriveItem>()
                    .await
                    .map_err(|_| protocol("final document upload returned invalid JSON"))?,
            );
        }
        offset = end + 1;
    }
    let ready = ready.ok_or_else(|| protocol("document upload did not return a DriveItem"))?;
    if ready.id.trim().is_empty() {
        return Err(protocol("document upload returned an incomplete DriveItem"));
    }
    let reference = Url::parse(&ready.web_url)
        .ok()
        .filter(|url| {
            url.scheme() == "https"
                && url.host_str().is_some()
                && url.username().is_empty()
                && url.password().is_none()
        })
        .ok_or_else(|| protocol("document upload returned an invalid reference URL"))?;
    let doc_id = if ready.spo_id.trim().is_empty() {
        derive_local_file_id(&ready.id, &ready.parent_reference.drive_id)?
    } else {
        ready.spo_id
    };
    attachment.doc_id = doc_id;
    attachment.name = spool.name.clone();
    attachment.transport_name = transport_name;
    attachment.reference_url = reference.to_string();
    attachment.uploaded_conversation_id = conversation_id.to_owned();
    Ok(())
}

async fn upload_image(
    account: &Account,
    conversation_id: &str,
    index: usize,
    attachment: &mut Attachment,
) -> Result<(), ChatError> {
    let spool = spool(&attachment.url, &attachment.mime_type, &attachment.name).await?;
    let detected = image_mime(&spool.path)?;
    if !compatible_mime(&spool.mime_type, detected) {
        return Err(protocol("image MIME type does not match its bytes"));
    }
    let (body, encoded_size) = image_form_body(&spool.path, spool.size, detected)?;
    let part = multipart::Part::stream_with_length(body, encoded_size)
        .mime_str(detected)
        .map_err(|_| protocol("invalid image MIME type"))?;
    let form = multipart::Form::new()
        .text("scenario", "UploadImage")
        .text("conversationId", conversation_id.to_owned())
        .part("FileBase64", part)
        .text("optionsSets", "cwcgptvsan")
        .text(
            "optionsSets",
            "flux_v3_gptv_enable_upload_multi_image_in_turn_wo_ch",
        )
        .text("optionsSets", "gptvnorm2048");
    let response = Client::builder()
        .timeout(Duration::from_secs(300))
        .build()
        .map_err(|_| protocol("image upload client is unavailable"))?
        .post("https://substrate.office.com/m365Copilot/UploadFile")
        .bearer_auth(&account.access_token)
        .header("Accept", "application/json")
        .header("Origin", "https://m365.cloud.microsoft")
        .header("X-Variants", "feature.EnableImageSupportInUploadFile")
        .header("X-Scenario", "OfficeWebIncludedCopilot")
        .header(
            "X-AnchorMailbox",
            format!("Oid:{}@{}", account.oid, account.tid),
        )
        .multipart(form)
        .send()
        .await
        .map_err(|_| protocol("image upload request failed"))?;
    if !response.status().is_success() {
        return Err(protocol(&format!(
            "image upload returned HTTP {}",
            response.status().as_u16()
        )));
    }
    let bytes = response
        .bytes()
        .await
        .map_err(|_| protocol("cannot read the image upload response"))?;
    if bytes.len() > 2 << 20 {
        return Err(protocol("image upload response is too large"));
    }
    let ready: UploadResponse = serde_json::from_slice(&bytes)
        .map_err(|_| protocol("image upload returned invalid JSON"))?;
    if ready.result.value != "Success" || ready.doc_id.trim().is_empty() {
        return Err(protocol("image upload did not return a ready image"));
    }
    attachment.doc_id = ready.doc_id;
    attachment.name = if ready.file_name.trim().is_empty() {
        if spool.name.trim().is_empty() {
            format!("image-{index}.{}", detected.trim_start_matches("image/"))
        } else {
            spool.name.clone()
        }
    } else {
        ready.file_name
    };
    attachment.file_type = normalize_image_extension(&ready.file_type, detected);
    attachment.mime_type = detected.to_owned();
    attachment.uploaded_conversation_id = conversation_id.to_owned();
    Ok(())
}

fn image_form_body(path: &Path, size: u64, mime: &str) -> Result<(reqwest::Body, u64), ChatError> {
    let prefix = format!("data:{mime};base64,").into_bytes();
    let encoded_size = prefix.len() as u64 + size.div_ceil(3) * 4;
    let path = path.to_path_buf();
    let (sender, receiver) = tokio::sync::mpsc::channel::<Result<Vec<u8>, std::io::Error>>(4);
    std::thread::Builder::new()
        .name("m365-image-base64".to_owned())
        .spawn(move || {
            if sender.blocking_send(Ok(prefix)).is_err() {
                return;
            }
            let mut file = match File::open(path) {
                Ok(file) => file,
                Err(error) => {
                    let _ = sender.blocking_send(Err(error));
                    return;
                }
            };
            const CHUNK: u64 = 96 * 1024;
            loop {
                let mut buffer = Vec::with_capacity(CHUNK as usize);
                match (&mut file).take(CHUNK).read_to_end(&mut buffer) {
                    Ok(0) => return,
                    Ok(_) => {
                        if sender
                            .blocking_send(Ok(STANDARD.encode(&buffer).into_bytes()))
                            .is_err()
                        {
                            return;
                        }
                    }
                    Err(error) => {
                        let _ = sender.blocking_send(Err(error));
                        return;
                    }
                }
            }
        })
        .map_err(|_| protocol("cannot start the image encoder"))?;
    let body_stream = stream::unfold(receiver, |mut receiver| async move {
        receiver.recv().await.map(|item| (item, receiver))
    });
    Ok((reqwest::Body::wrap_stream(body_stream), encoded_size))
}

struct Spool {
    path: PathBuf,
    size: u64,
    mime_type: String,
    name: String,
}

impl Drop for Spool {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.path);
    }
}

async fn spool(raw: &str, claimed_mime: &str, name: &str) -> Result<Spool, ChatError> {
    let path = private_temp_path()?;
    let mut cleanup = true;
    let result = if raw.to_ascii_lowercase().starts_with("data:") {
        spool_data(&path, raw, claimed_mime, name)
    } else {
        spool_remote(&path, raw, claimed_mime, name).await
    };
    if result.is_ok() {
        cleanup = false;
    }
    if cleanup {
        let _ = std::fs::remove_file(&path);
    }
    result
}

fn spool_data(
    path: &PathBuf,
    raw: &str,
    claimed_mime: &str,
    name: &str,
) -> Result<Spool, ChatError> {
    let (header, encoded) = raw
        .get(5..)
        .filter(|_| {
            raw.get(..5)
                .is_some_and(|prefix| prefix.eq_ignore_ascii_case("data:"))
        })
        .and_then(|value| value.split_once(','))
        .ok_or_else(|| protocol("invalid attachment data URL"))?;
    let mut parts = header.split(';');
    let mime = parts.next().unwrap_or("application/octet-stream").trim();
    if !parts.any(|part| part.eq_ignore_ascii_case("base64")) {
        return Err(protocol("attachment data URL must be base64"));
    }
    if (encoded.len() as u64).saturating_mul(3) / 4 > MAX_BYTES {
        return Err(protocol("attachment exceeds the 512 MiB limit"));
    }
    let mut file = secure_create(path)?;
    let mut decoder = base64::read::DecoderReader::new(encoded.as_bytes(), &STANDARD);
    let mut buffer = vec![0_u8; 128 * 1024];
    let mut size = 0_u64;
    loop {
        let count = decoder
            .read(&mut buffer)
            .map_err(|_| protocol("attachment base64 is invalid"))?;
        if count == 0 {
            break;
        }
        size = size.saturating_add(count as u64);
        if size > MAX_BYTES {
            return Err(protocol("attachment exceeds the 512 MiB limit"));
        }
        file.write_all(&buffer[..count])
            .map_err(|_| protocol("cannot write the private attachment spool"))?;
    }
    if size == 0 {
        return Err(protocol("attachment is empty"));
    }
    Ok(Spool {
        path: path.clone(),
        size,
        mime_type: if claimed_mime.trim().is_empty() {
            mime.to_owned()
        } else {
            claimed_mime.to_owned()
        },
        name: name.to_owned(),
    })
}

async fn spool_remote(
    path: &PathBuf,
    raw: &str,
    claimed_mime: &str,
    name: &str,
) -> Result<Spool, ChatError> {
    let mut current = Url::parse(raw).map_err(|_| protocol("invalid attachment URL"))?;
    let mut file = secure_create(path)?;
    for redirect in 0..=MAX_REDIRECTS {
        validate_remote(&current)?;
        let host = current
            .host_str()
            .ok_or_else(|| protocol("attachment host required"))?;
        let port = current.port_or_known_default().unwrap_or(443);
        let addresses = tokio::net::lookup_host((host, port))
            .await
            .map_err(|_| protocol("attachment host does not resolve"))?
            .collect::<Vec<_>>();
        if addresses.is_empty() || addresses.iter().any(|address| unsafe_ip(address.ip())) {
            return Err(protocol("attachment URL targets a non-public address"));
        }
        let client = Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .resolve(host, addresses[0])
            .timeout(Duration::from_secs(300))
            .build()
            .map_err(|_| protocol("secure attachment client is unavailable"))?;
        let response = client
            .get(current.clone())
            .send()
            .await
            .map_err(|_| protocol("attachment download failed"))?;
        if response.status().is_redirection() {
            if redirect == MAX_REDIRECTS {
                return Err(protocol("too many attachment redirects"));
            }
            let location = response
                .headers()
                .get(reqwest::header::LOCATION)
                .and_then(|value| value.to_str().ok())
                .ok_or_else(|| protocol("attachment redirect is invalid"))?;
            current = current
                .join(location)
                .map_err(|_| protocol("attachment redirect is invalid"))?;
            continue;
        }
        if response.status() != StatusCode::OK {
            return Err(protocol(&format!(
                "attachment download returned HTTP {}",
                response.status().as_u16()
            )));
        }
        if response
            .content_length()
            .is_some_and(|size| size > MAX_BYTES)
        {
            return Err(protocol("attachment exceeds the 512 MiB limit"));
        }
        let response_mime = response
            .headers()
            .get(reqwest::header::CONTENT_TYPE)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.split(';').next())
            .unwrap_or_default()
            .to_owned();
        let mut size = 0_u64;
        let mut stream = response.bytes_stream();
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|_| protocol("attachment download failed"))?;
            size = size.saturating_add(chunk.len() as u64);
            if size > MAX_BYTES {
                return Err(protocol("attachment exceeds the 512 MiB limit"));
            }
            file.write_all(&chunk)
                .map_err(|_| protocol("cannot write the private attachment spool"))?;
        }
        if size == 0 {
            return Err(protocol("attachment is empty"));
        }
        return Ok(Spool {
            path: path.clone(),
            size,
            mime_type: if claimed_mime.trim().is_empty() {
                response_mime
            } else {
                claimed_mime.to_owned()
            },
            name: if name.trim().is_empty() {
                current
                    .path_segments()
                    .and_then(Iterator::last)
                    .unwrap_or("image")
                    .to_owned()
            } else {
                name.to_owned()
            },
        });
    }
    Err(protocol("too many attachment redirects"))
}

fn private_temp_path() -> Result<PathBuf, ChatError> {
    for _ in 0..16 {
        let mut bytes = [0_u8; 16];
        rand::rng().fill(&mut bytes);
        let name = bytes
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>();
        let path = std::env::temp_dir().join(format!(".m365-attachment-{name}"));
        if !path.exists() {
            return Ok(path);
        }
    }
    Err(protocol("cannot allocate a private attachment spool"))
}

fn secure_create(path: &PathBuf) -> Result<File, ChatError> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options
        .open(path)
        .map_err(|_| protocol("cannot create a private attachment spool"))
}

fn image_mime(path: &PathBuf) -> Result<&'static str, ChatError> {
    let mut file = File::open(path).map_err(|_| protocol("cannot inspect image bytes"))?;
    let mut header = [0_u8; 16];
    let count = file
        .read(&mut header)
        .map_err(|_| protocol("cannot inspect image bytes"))?;
    let header = &header[..count];
    match header {
        bytes if bytes.starts_with(b"\x89PNG\r\n\x1a\n") => Ok("image/png"),
        [0xff, 0xd8, 0xff, ..] => Ok("image/jpeg"),
        bytes if bytes.starts_with(b"GIF87a") || bytes.starts_with(b"GIF89a") => Ok("image/gif"),
        bytes if bytes.len() >= 12 && &bytes[..4] == b"RIFF" && &bytes[8..12] == b"WEBP" => {
            Ok("image/webp")
        }
        _ => Err(protocol("image must be PNG, JPEG, GIF, or WebP")),
    }
}

fn compatible_mime(claimed: &str, detected: &str) -> bool {
    let claimed = claimed
        .split(';')
        .next()
        .unwrap_or_default()
        .trim()
        .to_ascii_lowercase();
    claimed.is_empty()
        || matches!(claimed.as_str(), "image/*" | "application/octet-stream")
        || (claimed == "image/jpg" && detected == "image/jpeg")
        || claimed == detected
}

fn normalize_image_extension(raw: &str, mime: &str) -> String {
    let extension = raw.trim().trim_start_matches('.').to_ascii_lowercase();
    match extension.as_str() {
        "jpeg" => "jpg".to_owned(),
        "" => mime.trim_start_matches("image/").replace("jpeg", "jpg"),
        _ => extension,
    }
}

fn document_name(original: &str) -> String {
    let safe = original
        .replace('\\', "/")
        .rsplit('/')
        .next()
        .unwrap_or("attachment")
        .chars()
        .map(|character| {
            if character.is_control() || "\"*:<>?/\\|".contains(character) {
                '_'
            } else {
                character
            }
        })
        .collect::<String>();
    let safe = safe.trim_matches([' ', '.']);
    let safe = if safe.is_empty() { "attachment" } else { safe };
    let known = [
        "txt", "pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "csv", "json", "md", "html",
        "htm", "rtf", "xml", "yaml", "yml", "py", "js", "ts", "java", "c", "cc", "cpp", "cs", "go",
        "rs", "swift", "sql", "log",
    ];
    let mut bytes = [0_u8; 8];
    rand::rng().fill(&mut bytes);
    let suffix = bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect::<String>();
    if let Some((root, extension)) = safe.rsplit_once('.')
        && known.contains(&extension.to_ascii_lowercase().as_str())
    {
        format!("{}-{suffix}.{extension}", truncate_utf16(root, 260))
    } else {
        format!("{}-{suffix}.txt", truncate_utf16(safe, 260))
    }
}

fn truncate_utf16(value: &str, limit: usize) -> String {
    let mut units = 0;
    value
        .chars()
        .take_while(|character| {
            let width = character.len_utf16();
            if units + width > limit {
                return false;
            }
            units += width;
            true
        })
        .collect()
}

fn percent_encode_path(value: &str) -> String {
    let mut output = String::new();
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            output.push(byte as char);
        } else {
            output.push_str(&format!("%{byte:02X}"));
        }
    }
    output
}

fn validate_upload_url(raw: &str) -> Result<(), ChatError> {
    let url = Url::parse(raw).map_err(|_| protocol("document upload URL is invalid"))?;
    let host = url.host_str().unwrap_or_default().to_ascii_lowercase();
    if url.scheme() != "https"
        || !url.username().is_empty()
        || url.password().is_some()
        || !(host.ends_with(".sharepoint.com") || host.ends_with(".sharepoint-df.com"))
    {
        return Err(protocol(
            "document upload URL is not a trusted SharePoint endpoint",
        ));
    }
    Ok(())
}

fn derive_local_file_id(item_id: &str, drive_id: &str) -> Result<String, ChatError> {
    let encoded = drive_id
        .trim()
        .get(2..)
        .ok_or_else(|| protocol("DriveItem identity is incomplete"))?;
    let raw = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(encoded)
        .map_err(|_| protocol("DriveItem drive identity is invalid"))?;
    if item_id.trim().is_empty() || raw.len() < 48 {
        return Err(protocol("DriveItem identity is incomplete"));
    }
    let guids = raw[..48]
        .chunks_exact(16)
        .map(microsoft_guid)
        .collect::<Vec<_>>()
        .join(",");
    Ok(format!(
        "SPO_{}_{}",
        base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(guids),
        item_id.trim()
    ))
}

fn microsoft_guid(raw: &[u8]) -> String {
    let order = [3, 2, 1, 0, 5, 4, 7, 6, 8, 9, 10, 11, 12, 13, 14, 15];
    let value = order
        .iter()
        .map(|index| format!("{:02x}", raw[*index]))
        .collect::<String>();
    format!(
        "{}-{}-{}-{}-{}",
        &value[..8],
        &value[8..12],
        &value[12..16],
        &value[16..20],
        &value[20..]
    )
}

fn validate_remote(url: &Url) -> Result<(), ChatError> {
    if url.scheme() != "https"
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
    {
        return Err(protocol("attachment source must be a public HTTPS URL"));
    }
    if url
        .host_str()
        .and_then(|host| host.parse::<IpAddr>().ok())
        .is_some_and(unsafe_ip)
    {
        return Err(protocol("attachment URL targets a non-public address"));
    }
    Ok(())
}

fn unsafe_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(ip) => unsafe_v4(ip),
        IpAddr::V6(ip) => ip
            .to_ipv4_mapped()
            .map(unsafe_v4)
            .unwrap_or_else(|| unsafe_v6(ip)),
    }
}

fn unsafe_v4(ip: Ipv4Addr) -> bool {
    let value = u32::from(ip);
    let in_prefix = |network: Ipv4Addr, bits: u32| {
        let mask = if bits == 0 {
            0
        } else {
            u32::MAX << (32 - bits)
        };
        value & mask == u32::from(network) & mask
    };
    ip.is_private()
        || ip.is_loopback()
        || ip.is_link_local()
        || ip.is_multicast()
        || ip.is_unspecified()
        || ip.is_broadcast()
        || in_prefix(Ipv4Addr::new(0, 0, 0, 0), 8)
        || in_prefix(Ipv4Addr::new(100, 64, 0, 0), 10)
        || in_prefix(Ipv4Addr::new(192, 0, 0, 0), 24)
        || in_prefix(Ipv4Addr::new(192, 0, 2, 0), 24)
        || in_prefix(Ipv4Addr::new(192, 88, 99, 0), 24)
        || in_prefix(Ipv4Addr::new(198, 18, 0, 0), 15)
        || in_prefix(Ipv4Addr::new(198, 51, 100, 0), 24)
        || in_prefix(Ipv4Addr::new(203, 0, 113, 0), 24)
        || in_prefix(Ipv4Addr::new(240, 0, 0, 0), 4)
}

fn unsafe_v6(ip: Ipv6Addr) -> bool {
    let value = u128::from(ip);
    let in_prefix = |network: Ipv6Addr, bits: u32| {
        let mask = if bits == 0 {
            0
        } else {
            u128::MAX << (128 - bits)
        };
        value & mask == u128::from(network) & mask
    };
    ip.is_loopback()
        || ip.is_unspecified()
        || ip.is_multicast()
        || in_prefix("fc00::".parse().unwrap(), 7)
        || in_prefix("fe80::".parse().unwrap(), 10)
        || in_prefix("64:ff9b::".parse().unwrap(), 96)
        || in_prefix("64:ff9b:1::".parse().unwrap(), 48)
        || in_prefix("100::".parse().unwrap(), 64)
        || in_prefix("2001::".parse().unwrap(), 23)
        || in_prefix("2001:db8::".parse().unwrap(), 32)
        || in_prefix("2002::".parse().unwrap(), 16)
}

fn protocol(message: &str) -> ChatError {
    ChatError::Protocol(message.to_owned())
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct UploadResponse {
    doc_id: String,
    #[serde(default)]
    file_name: String,
    #[serde(default)]
    file_type: String,
    result: UploadResult,
}

#[derive(Deserialize)]
struct UploadResult {
    value: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct UploadSession {
    upload_url: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct DriveItem {
    id: String,
    #[serde(default)]
    web_url: String,
    #[serde(default)]
    spo_id: String,
    #[serde(default)]
    parent_reference: ParentReference,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct ParentReference {
    #[serde(default)]
    drive_id: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn private_and_documentation_addresses_are_blocked() {
        for value in ["127.0.0.1", "10.0.0.1", "192.0.2.1", "::1", "2001:db8::1"] {
            assert!(unsafe_ip(value.parse().unwrap()), "{value}");
        }
        assert!(!unsafe_ip("1.1.1.1".parse().unwrap()));
    }

    #[test]
    fn image_magic_is_authoritative() {
        let path = private_temp_path().unwrap();
        let mut file = secure_create(&path).unwrap();
        file.write_all(b"\x89PNG\r\n\x1a\nrest").unwrap();
        drop(file);
        assert_eq!(image_mime(&path).unwrap(), "image/png");
        std::fs::remove_file(path).unwrap();
    }

    #[test]
    fn document_name_keeps_a_known_extension_at_the_end() {
        let name = document_name("../Quarterly report.pdf");
        assert!(name.starts_with("Quarterly report-"));
        assert!(name.ends_with(".pdf"));
        assert!(!name.contains('/'));
    }

    #[tokio::test]
    async fn ready_attachments_are_reused_only_for_the_same_conversation() {
        let account = Account {
            access_token: String::new(),
            graph_access_token: String::new(),
            oid: String::new(),
            tid: String::new(),
        };
        let mut ready = vec![
            Attachment {
                kind: "file".to_owned(),
                doc_id: "SPO_ready".to_owned(),
                transport_name: "ready.txt".to_owned(),
                reference_url: "https://tenant.sharepoint.com/ready".to_owned(),
                uploaded_conversation_id: "same".to_owned(),
                ..Attachment::default()
            },
            Attachment {
                kind: "image".to_owned(),
                doc_id: "IMG_ready".to_owned(),
                file_type: "png".to_owned(),
                uploaded_conversation_id: "same".to_owned(),
                ..Attachment::default()
            },
        ];
        prepare(&account, "same", &mut ready).await.unwrap();
        assert_eq!(ready[0].doc_id, "SPO_ready");
        assert_eq!(ready[1].doc_id, "IMG_ready");

        let error = prepare(&account, "new", &mut ready).await.unwrap_err();
        assert!(error.to_string().contains("Graph authorization"));
        assert!(ready[0].doc_id.is_empty());
        assert!(ready[0].reference_url.is_empty());
        assert!(ready[0].uploaded_conversation_id.is_empty());
    }

    #[tokio::test]
    async fn attachment_quota_is_shared_across_files_and_images() {
        let mut attachments = vec![
            Attachment::default(),
            Attachment::default(),
            Attachment::default(),
            Attachment::default(),
        ];
        let account = Account {
            access_token: String::new(),
            graph_access_token: String::new(),
            oid: String::new(),
            tid: String::new(),
        };
        let error = prepare(&account, "conversation", &mut attachments)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("shared limit of 3"));
    }

    #[test]
    fn ipv4_mapped_ipv6_cannot_bypass_private_address_checks() {
        assert!(unsafe_ip("::ffff:127.0.0.1".parse().unwrap()));
        assert!(unsafe_ip("::ffff:192.0.2.1".parse().unwrap()));
        assert!(!unsafe_ip("::ffff:8.8.8.8".parse().unwrap()));
    }
}
