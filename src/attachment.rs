use std::{
    fs::{File, OpenOptions},
    io::{Read, Write},
    net::{IpAddr, Ipv4Addr, Ipv6Addr},
    path::{Path, PathBuf},
    time::{Duration, SystemTime},
};

use base64::{Engine, engine::general_purpose::STANDARD};
use futures_util::{StreamExt, stream};
use rand::Rng;
use reqwest::{Client, StatusCode, Url, header::HeaderValue, multipart};
use serde::Deserialize;
use serde_json::json;
use sha2::{Digest, Sha256};
use tokio::io::AsyncReadExt;

use crate::chathub::{Account, Attachment, AttachmentFailureKind, ChatError};

pub(crate) const MAX_ATTACHMENTS: usize = 3;
pub(crate) const MAX_BYTES: u64 = 512 << 20;
pub(crate) const MAX_PREPARED_DOC_ID_UTF16: usize = 2_048;
pub(crate) const MAX_PREPARED_NAME_UTF16: usize = 2_048;
pub(crate) const MAX_PREPARED_REFERENCE_URL_UTF16: usize = 4_096;
const MAX_REDIRECTS: usize = 5;
const DOCUMENT_CHUNK: usize = 983_040;
const GRAPH_API_BASE: &str = "https://graph.microsoft.com/v1.0";
const DOCUMENT_UPLOAD_MAX_ATTEMPTS: usize = 2;
const DOCUMENT_UPLOAD_BACKOFF: Duration = Duration::from_millis(100);
const DOCUMENT_UPLOAD_MAX_RETRY_DELAY: Duration = Duration::from_secs(2);

pub(crate) fn validate_attachment_slots(attachments: &[Attachment]) -> Result<(), &'static str> {
    if attachments.len() > MAX_ATTACHMENTS {
        return Err("active attachments exceed the shared limit of 3");
    }
    let generated_indexes: Vec<usize> = attachments
        .iter()
        .enumerate()
        .filter_map(|(index, attachment)| attachment.generated_oversize_text.then_some(index))
        .collect();
    if attachments.len() - generated_indexes.len() > MAX_ATTACHMENTS - 1 {
        return Err("ordinary attachments exceed the two-slot limit");
    }
    if generated_indexes.len() > 1 {
        return Err("generated spill attachments exceed the reserved slot");
    }
    if generated_indexes
        .first()
        .is_some_and(|index| *index != attachments.len() - 1)
    {
        return Err("generated spill must occupy the reserved third attachment slot");
    }
    Ok(())
}

pub async fn prepare(
    account: &Account,
    conversation_id: &str,
    session_id: &str,
    attachments: &mut [Attachment],
) -> Result<(), ChatError> {
    validate_attachment_slots(attachments).map_err(protocol)?;
    for (index, attachment) in attachments.iter_mut().enumerate() {
        let generated_oversize_text = attachment.generated_oversize_text;
        let result = match attachment.kind.as_str() {
            "image" => {
                if !attachment.doc_id.is_empty()
                    && attachment.uploaded_conversation_id == conversation_id
                    && attachment.uploaded_session_id == session_id
                {
                    continue;
                }
                attachment.doc_id.clear();
                attachment.file_type.clear();
                attachment.uploaded_conversation_id.clear();
                attachment.uploaded_session_id.clear();
                upload_image(account, conversation_id, session_id, index, attachment).await
            }
            "file" => {
                if !attachment.doc_id.is_empty()
                    && !attachment.reference_url.is_empty()
                    && attachment.uploaded_conversation_id == conversation_id
                    && attachment.uploaded_session_id == session_id
                {
                    continue;
                }
                attachment.doc_id.clear();
                attachment.reference_url.clear();
                attachment.transport_name.clear();
                attachment.uploaded_conversation_id.clear();
                attachment.uploaded_session_id.clear();
                upload_document(account, conversation_id, session_id, attachment).await
            }
            _ => return Err(protocol("unsupported attachment type")),
        };
        if let Err(error) = result {
            return Err(match error {
                ChatError::Attachment { .. } => error,
                error => ChatError::Attachment {
                    generated_oversize_text,
                    failure: AttachmentFailureKind::UnknownAttachmentTransport,
                    message: error.to_string(),
                },
            });
        }
    }
    Ok(())
}

async fn upload_document(
    account: &Account,
    conversation_id: &str,
    session_id: &str,
    attachment: &mut Attachment,
) -> Result<(), ChatError> {
    upload_document_at(
        account,
        conversation_id,
        session_id,
        attachment,
        GRAPH_API_BASE,
        validate_upload_url,
    )
    .await
}

#[cfg(test)]
pub(crate) async fn prepare_document_at_for_test(
    account: &Account,
    conversation_id: &str,
    session_id: &str,
    attachment: &mut Attachment,
    graph_api_base: &str,
) -> Result<(), ChatError> {
    upload_document_at(
        account,
        conversation_id,
        session_id,
        attachment,
        graph_api_base,
        validate_upload_url_for_test,
    )
    .await
}

#[cfg(test)]
fn validate_upload_url_for_test(raw: &str) -> Result<(), ChatError> {
    let url = Url::parse(raw).map_err(|_| protocol("test upload URL is invalid"))?;
    if url.scheme() == "http"
        && url.host_str() == Some("127.0.0.1")
        && url.username().is_empty()
        && url.password().is_none()
    {
        Ok(())
    } else {
        validate_upload_url(raw)
    }
}

type UploadUrlValidator = fn(&str) -> Result<(), ChatError>;

async fn upload_document_at(
    account: &Account,
    conversation_id: &str,
    session_id: &str,
    attachment: &mut Attachment,
    graph_api_base: &str,
    upload_url_validator: UploadUrlValidator,
) -> Result<(), ChatError> {
    let generated_oversize_text = attachment.generated_oversize_text;
    if account.graph_access_token.trim().is_empty() {
        return Err(attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::GraphAuthorizationUnavailable,
        ));
    }
    let spool = spool_attachment(attachment).await.map_err(|_| {
        attachment_failure(generated_oversize_text, AttachmentFailureKind::LocalSpool)
    })?;
    let transport_name = document_name(&spool.name, attachment.generated_oversize_text);
    validate_prepared_metadata(
        &transport_name,
        MAX_PREPARED_NAME_UTF16,
        "document upload returned an oversized file name",
    )
    .map_err(|_| {
        attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::AttachmentMetadataInvalid,
        )
    })?;
    let create_url = format!(
        "{graph_api_base}/me/drive/special/copilotuploads:/{}:/createUploadSession",
        percent_encode_path(&transport_name)
    );
    let client = Client::builder()
        .timeout(Duration::from_secs(300))
        .build()
        .map_err(|_| {
            attachment_failure(
                generated_oversize_text,
                AttachmentFailureKind::UnknownAttachmentTransport,
            )
        })?;
    let session = create_upload_session_with_retries(
        &client,
        &create_url,
        &account.graph_access_token,
        &transport_name,
        generated_oversize_text,
    )
    .await
    .map_err(|failure| attachment_failure(generated_oversize_text, failure))?;
    upload_url_validator(&session.upload_url).map_err(|_| {
        attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::UntrustedUploadUrl,
        )
    })?;

    let mut file = tokio::fs::File::open(&spool.path).await.map_err(|_| {
        attachment_failure(generated_oversize_text, AttachmentFailureKind::LocalSpool)
    })?;
    let mut offset = 0_u64;
    let mut ready = None;
    while offset < spool.size {
        let length = ((spool.size - offset) as usize).min(DOCUMENT_CHUNK);
        let mut chunk = vec![0_u8; length];
        file.read_exact(&mut chunk).await.map_err(|_| {
            attachment_failure(generated_oversize_text, AttachmentFailureKind::LocalSpool)
        })?;
        let end = offset + length as u64 - 1;
        let content_range = format!("bytes {offset}-{end}/{}", spool.size);
        let response = upload_chunk_with_retries(
            &client,
            &session.upload_url,
            &content_range,
            &chunk,
            offset,
            end,
            generated_oversize_text,
        )
        .await
        .map_err(|failure| attachment_failure(generated_oversize_text, failure))?;
        if end + 1 == spool.size {
            ready = Some(response.json::<DriveItem>().await.map_err(|_| {
                attachment_failure(
                    generated_oversize_text,
                    AttachmentFailureKind::DriveItemInvalidJson,
                )
            })?);
        }
        offset = end + 1;
    }
    let ready = ready.ok_or_else(|| {
        attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::DriveItemIncomplete,
        )
    })?;
    if ready.id.trim().is_empty() {
        return Err(attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::DriveItemIncomplete,
        ));
    }
    let reference = Url::parse(&ready.web_url)
        .ok()
        .filter(|url| {
            url.scheme() == "https"
                && url.host_str().is_some()
                && url.username().is_empty()
                && url.password().is_none()
        })
        .ok_or_else(|| {
            attachment_failure(
                generated_oversize_text,
                AttachmentFailureKind::ReferenceValidationFailed,
            )
        })?;
    let doc_id = if ready.spo_id.trim().is_empty() {
        derive_local_file_id(&ready.id, &ready.parent_reference.drive_id).map_err(|_| {
            attachment_failure(
                generated_oversize_text,
                AttachmentFailureKind::DriveItemIncomplete,
            )
        })?
    } else {
        ready.spo_id
    };
    validate_prepared_metadata(
        &doc_id,
        MAX_PREPARED_DOC_ID_UTF16,
        "document upload returned an oversized document id",
    )
    .map_err(|_| {
        attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::DriveItemIncomplete,
        )
    })?;
    validate_prepared_metadata(
        reference.as_str(),
        MAX_PREPARED_REFERENCE_URL_UTF16,
        "document upload returned an oversized reference URL",
    )
    .map_err(|_| {
        attachment_failure(
            generated_oversize_text,
            AttachmentFailureKind::ReferenceValidationFailed,
        )
    })?;
    attachment.doc_id = doc_id;
    attachment.name = spool.name.clone();
    attachment.transport_name = transport_name;
    attachment.reference_url = reference.to_string();
    attachment.uploaded_conversation_id = conversation_id.to_owned();
    attachment.uploaded_session_id = session_id.to_owned();
    Ok(())
}

fn attachment_failure(generated_oversize_text: bool, failure: AttachmentFailureKind) -> ChatError {
    ChatError::Attachment {
        generated_oversize_text,
        failure,
        message: failure.message().to_owned(),
    }
}

async fn create_upload_session_with_retries(
    client: &Client,
    create_url: &str,
    graph_access_token: &str,
    transport_name: &str,
    generated_oversize_text: bool,
) -> Result<UploadSession, AttachmentFailureKind> {
    for attempt in 0..DOCUMENT_UPLOAD_MAX_ATTEMPTS {
        let response = client
            .post(create_url)
            .bearer_auth(graph_access_token)
            .json(&json!({"item":{
                "@microsoft.graph.conflictBehavior":"replace",
                "name":transport_name
            }}))
            .send()
            .await;
        let response = match response {
            Ok(response) => response,
            Err(_) if generated_oversize_text && attempt + 1 < DOCUMENT_UPLOAD_MAX_ATTEMPTS => {
                wait_before_document_retry(retry_delay(attempt, None, SystemTime::now())).await;
                continue;
            }
            Err(_) => return Err(AttachmentFailureKind::GraphUploadSessionTransport),
        };
        if response.status().is_success() {
            return response
                .json()
                .await
                .map_err(|_| AttachmentFailureKind::GraphUploadSessionInvalidJson);
        }
        let failure = graph_upload_http_failure(response.status());
        if generated_oversize_text
            && retryable_http_status(response.status())
            && attempt + 1 < DOCUMENT_UPLOAD_MAX_ATTEMPTS
        {
            wait_before_document_retry(retry_delay(
                attempt,
                response.headers().get(reqwest::header::RETRY_AFTER),
                SystemTime::now(),
            ))
            .await;
            continue;
        }
        return Err(failure);
    }
    unreachable!("document upload session retry loop always returns")
}

async fn upload_chunk_with_retries(
    client: &Client,
    upload_url: &str,
    content_range: &str,
    chunk: &[u8],
    offset: u64,
    end: u64,
    generated_oversize_text: bool,
) -> Result<reqwest::Response, AttachmentFailureKind> {
    // A transport error leaves the PUT outcome unknown. Reconcile the existing
    // upload session before retrying the exact range; never blindly replay a
    // possibly committed byte range.
    for attempt in 0..DOCUMENT_UPLOAD_MAX_ATTEMPTS {
        let response = client
            .put(upload_url)
            .header(reqwest::header::CONTENT_TYPE, "application/octet-stream")
            .header(reqwest::header::CONTENT_RANGE, content_range)
            .body(chunk.to_vec())
            .send()
            .await;
        let response = match response {
            Ok(response) => response,
            Err(_) if generated_oversize_text && attempt + 1 < DOCUMENT_UPLOAD_MAX_ATTEMPTS => {
                let status = upload_session_status(client, upload_url).await?;
                if !range_is_missing(&status.next_expected_ranges, offset, end) {
                    return Err(AttachmentFailureKind::SharePointUploadTransportUnknown);
                }
                wait_before_document_retry(retry_delay(attempt, None, SystemTime::now())).await;
                continue;
            }
            Err(_) => return Err(AttachmentFailureKind::SharePointUploadTransportUnknown),
        };
        if response.status().is_success() {
            return Ok(response);
        }
        let failure = sharepoint_upload_http_failure(response.status());
        if generated_oversize_text
            && retryable_http_status(response.status())
            && attempt + 1 < DOCUMENT_UPLOAD_MAX_ATTEMPTS
        {
            wait_before_document_retry(retry_delay(
                attempt,
                response.headers().get(reqwest::header::RETRY_AFTER),
                SystemTime::now(),
            ))
            .await;
            continue;
        }
        return Err(failure);
    }
    unreachable!("document upload chunk retry loop always returns")
}

async fn upload_session_status(
    client: &Client,
    upload_url: &str,
) -> Result<UploadStatus, AttachmentFailureKind> {
    let response = client
        .get(upload_url)
        .send()
        .await
        .map_err(|_| AttachmentFailureKind::SharePointUploadTransportUnknown)?;
    if !response.status().is_success() {
        return Err(AttachmentFailureKind::SharePointUploadTransportUnknown);
    }
    response
        .json()
        .await
        .map_err(|_| AttachmentFailureKind::SharePointUploadTransportUnknown)
}

fn range_is_missing(ranges: &[String], offset: u64, end: u64) -> bool {
    ranges.iter().any(|range| {
        let Some((range_start, range_end)) = parse_expected_range(range) else {
            return false;
        };
        range_start <= offset && range_end.is_none_or(|range_end| end <= range_end)
    })
}

fn parse_expected_range(value: &str) -> Option<(u64, Option<u64>)> {
    let (start, end) = value.trim().split_once('-')?;
    let start = start.parse().ok()?;
    let end = if end.is_empty() {
        None
    } else {
        Some(end.parse().ok()?)
    };
    Some((start, end))
}

fn retryable_http_status(status: StatusCode) -> bool {
    status == StatusCode::REQUEST_TIMEOUT
        || status == StatusCode::TOO_MANY_REQUESTS
        || status.is_server_error()
}

fn graph_upload_http_failure(status: StatusCode) -> AttachmentFailureKind {
    match status {
        StatusCode::REQUEST_TIMEOUT => AttachmentFailureKind::GraphUploadSessionHttp408,
        StatusCode::TOO_MANY_REQUESTS => AttachmentFailureKind::GraphUploadSessionHttp429,
        status if status.is_server_error() => AttachmentFailureKind::GraphUploadSessionHttp5xx,
        _ => AttachmentFailureKind::GraphUploadSessionHttp4xx,
    }
}

fn sharepoint_upload_http_failure(status: StatusCode) -> AttachmentFailureKind {
    match status {
        StatusCode::REQUEST_TIMEOUT => AttachmentFailureKind::SharePointUploadHttp408,
        StatusCode::TOO_MANY_REQUESTS => AttachmentFailureKind::SharePointUploadHttp429,
        status if status.is_server_error() => AttachmentFailureKind::SharePointUploadHttp5xx,
        _ => AttachmentFailureKind::SharePointUploadHttp4xx,
    }
}

fn retry_delay(attempt: usize, retry_after: Option<&HeaderValue>, now: SystemTime) -> Duration {
    if let Some(retry_after) = retry_after.and_then(|value| parse_retry_after(value, now)) {
        return retry_after.min(DOCUMENT_UPLOAD_MAX_RETRY_DELAY);
    }
    let multiplier = 1_u32.checked_shl(attempt.min(4) as u32).unwrap_or(u32::MAX);
    DOCUMENT_UPLOAD_BACKOFF
        .checked_mul(multiplier)
        .unwrap_or(DOCUMENT_UPLOAD_MAX_RETRY_DELAY)
        .min(DOCUMENT_UPLOAD_MAX_RETRY_DELAY)
}

fn parse_retry_after(value: &HeaderValue, now: SystemTime) -> Option<Duration> {
    let value = value.to_str().ok()?.trim();
    if let Ok(seconds) = value.parse::<u64>() {
        return Some(Duration::from_secs(seconds).min(DOCUMENT_UPLOAD_MAX_RETRY_DELAY));
    }
    let at = httpdate::parse_http_date(value).ok()?;
    Some(
        at.duration_since(now)
            .unwrap_or_default()
            .min(DOCUMENT_UPLOAD_MAX_RETRY_DELAY),
    )
}

async fn wait_before_document_retry(delay: Duration) {
    #[cfg(not(test))]
    tokio::time::sleep(delay).await;
    #[cfg(test)]
    let _ = delay;
}

async fn upload_image(
    account: &Account,
    conversation_id: &str,
    session_id: &str,
    index: usize,
    attachment: &mut Attachment,
) -> Result<(), ChatError> {
    let spool = spool_attachment(attachment).await?;
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
    let doc_id = ready.doc_id;
    let name = if ready.file_name.trim().is_empty() {
        if spool.name.trim().is_empty() {
            format!("image-{index}.{}", detected.trim_start_matches("image/"))
        } else {
            spool.name.clone()
        }
    } else {
        ready.file_name
    };
    let file_type = normalize_image_extension(&ready.file_type, detected);
    validate_prepared_metadata(
        &doc_id,
        MAX_PREPARED_DOC_ID_UTF16,
        "image upload returned an oversized document id",
    )?;
    validate_prepared_metadata(
        &name,
        MAX_PREPARED_NAME_UTF16,
        "image upload returned an oversized file name",
    )?;
    validate_prepared_metadata(
        &file_type,
        MAX_PREPARED_NAME_UTF16,
        "image upload returned an oversized file type",
    )?;
    attachment.doc_id = doc_id;
    attachment.name = name;
    attachment.file_type = file_type;
    attachment.mime_type = detected.to_owned();
    attachment.uploaded_conversation_id = conversation_id.to_owned();
    attachment.uploaded_session_id = session_id.to_owned();
    Ok(())
}

fn validate_prepared_metadata(
    value: &str,
    limit: usize,
    message: &'static str,
) -> Result<(), ChatError> {
    let serialized_units = serde_json::to_string(value)
        .expect("prepared attachment metadata is serializable")
        .encode_utf16()
        .count();
    if serialized_units > limit.saturating_add(2) {
        return Err(protocol(message));
    }
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

#[derive(Clone, Copy)]
enum SpoolOwnership {
    Borrowed,
    Temporary,
}

struct Spool {
    path: PathBuf,
    size: u64,
    mime_type: String,
    name: String,
    ownership: SpoolOwnership,
}

impl Drop for Spool {
    fn drop(&mut self) {
        if matches!(self.ownership, SpoolOwnership::Temporary) {
            let _ = std::fs::remove_file(&self.path);
        }
    }
}

async fn spool_attachment(attachment: &Attachment) -> Result<Spool, ChatError> {
    let Some(source) = attachment.staged.as_ref() else {
        return spool(&attachment.url, &attachment.mime_type, &attachment.name).await;
    };
    let mut file = tokio::fs::File::open(&source.path)
        .await
        .map_err(|_| protocol("staged attachment is unavailable"))?;
    let initial = file
        .metadata()
        .await
        .map_err(|_| protocol("staged attachment is unavailable"))?;
    if !initial.is_file()
        || initial.len() == 0
        || initial.len() > MAX_BYTES
        || initial.len() != source.size
    {
        return Err(protocol("staged attachment changed before preparation"));
    }
    let mut hasher = Sha256::new();
    let mut size = 0_u64;
    let mut buffer = vec![0_u8; 128 * 1024];
    loop {
        let count = file
            .read(&mut buffer)
            .await
            .map_err(|_| protocol("staged attachment could not be verified"))?;
        if count == 0 {
            break;
        }
        size = size.saturating_add(count as u64);
        hasher.update(&buffer[..count]);
    }
    let final_metadata = file
        .metadata()
        .await
        .map_err(|_| protocol("staged attachment could not be verified"))?;
    if !final_metadata.is_file()
        || final_metadata.len() != source.size
        || size != source.size
        || format!("{:x}", hasher.finalize()) != source.sha256
    {
        return Err(protocol("staged attachment integrity check failed"));
    }
    Ok(Spool {
        path: source.path.clone(),
        size: source.size,
        mime_type: attachment.mime_type.clone(),
        name: attachment.name.clone(),
        ownership: SpoolOwnership::Borrowed,
    })
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
        ownership: SpoolOwnership::Temporary,
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
            ownership: SpoolOwnership::Temporary,
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

fn document_name(original: &str, preserve_generated_name: bool) -> String {
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
    if preserve_generated_name && is_generated_oversize_name(safe) {
        return safe.to_owned();
    }
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

fn is_generated_oversize_name(value: &str) -> bool {
    let Some(digest) = value
        .strip_prefix("m365-oversize-")
        .and_then(|value| value.strip_suffix(".txt"))
    else {
        return false;
    };
    digest.len() == 64 && digest.bytes().all(|byte| byte.is_ascii_hexdigit())
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
struct UploadStatus {
    #[serde(default)]
    next_expected_ranges: Vec<String>,
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
    use crate::chathub::StagedAttachmentSource;
    use sha2::{Digest, Sha256};
    use std::sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    };
    use tokio::io::AsyncWriteExt;
    use tokio::net::{TcpListener, TcpStream};

    #[derive(Clone, Copy)]
    enum TestUploadFailure {
        Success,
        CreateTransportUnknown,
        Create429,
        Create503,
        PutTransportUnknown,
        PutTransportUnknownAlways,
        PutTransportCommitted,
        Put429,
        Put503,
        PutForbidden,
    }

    struct TestUploadState {
        failure: TestUploadFailure,
        create_calls: AtomicUsize,
        put_calls: AtomicUsize,
        status_calls: AtomicUsize,
        content_ranges: Mutex<Vec<String>>,
        uploaded_bytes: Mutex<Vec<u8>>,
        upload_url: String,
    }

    struct TestUploadServer {
        address: std::net::SocketAddr,
        state: Arc<TestUploadState>,
        task: tokio::task::JoinHandle<()>,
    }

    impl TestUploadServer {
        async fn start(failure: TestUploadFailure) -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
            let address = listener.local_addr().unwrap();
            let state = Arc::new(TestUploadState {
                failure,
                create_calls: AtomicUsize::new(0),
                put_calls: AtomicUsize::new(0),
                status_calls: AtomicUsize::new(0),
                content_ranges: Mutex::new(Vec::new()),
                uploaded_bytes: Mutex::new(Vec::new()),
                upload_url: format!("http://{address}/upload"),
            });
            let task_state = Arc::clone(&state);
            let task = tokio::spawn(async move {
                loop {
                    let Ok((stream, _)) = listener.accept().await else {
                        break;
                    };
                    tokio::spawn(handle_test_upload_connection(
                        stream,
                        Arc::clone(&task_state),
                    ));
                }
            });
            Self {
                address,
                state,
                task,
            }
        }
    }

    impl Drop for TestUploadServer {
        fn drop(&mut self) {
            self.task.abort();
        }
    }

    async fn handle_test_upload_connection(mut stream: TcpStream, state: Arc<TestUploadState>) {
        let mut request = Vec::new();
        let header_end = loop {
            let mut buffer = [0_u8; 4_096];
            let count = match tokio::io::AsyncReadExt::read(&mut stream, &mut buffer).await {
                Ok(0) | Err(_) => return,
                Ok(count) => count,
            };
            request.extend_from_slice(&buffer[..count]);
            if let Some(position) = request.windows(4).position(|bytes| bytes == b"\r\n\r\n") {
                break position + 4;
            }
            if request.len() > 64 * 1024 {
                return;
            }
        };
        let headers = String::from_utf8_lossy(&request[..header_end]);
        let mut lines = headers.split("\r\n");
        let mut request_line = lines.next().unwrap_or_default().split_whitespace();
        let method = request_line.next().unwrap_or_default();
        let mut content_length = 0_usize;
        let mut content_range = String::new();
        for line in lines {
            let Some((name, value)) = line.split_once(':') else {
                continue;
            };
            match name.to_ascii_lowercase().as_str() {
                "content-length" => content_length = value.trim().parse().unwrap_or(0),
                "content-range" => content_range = value.trim().to_owned(),
                _ => {}
            }
        }
        let mut body = request[header_end..].to_vec();
        let body_read = body.len();
        if body_read < content_length {
            let mut rest = vec![0_u8; content_length - body_read];
            if tokio::io::AsyncReadExt::read_exact(&mut stream, &mut rest)
                .await
                .is_err()
            {
                return;
            }
            body.extend_from_slice(&rest);
        }

        if method == "POST" {
            let call = state.create_calls.fetch_add(1, Ordering::SeqCst);
            if call == 0 && matches!(state.failure, TestUploadFailure::CreateTransportUnknown) {
                return;
            }
            if call == 0 && matches!(state.failure, TestUploadFailure::Create429) {
                send_test_upload_response(&mut stream, 429, true, "{}").await;
                return;
            }
            if call == 0 && matches!(state.failure, TestUploadFailure::Create503) {
                send_test_upload_response(&mut stream, 503, false, "{}").await;
                return;
            }
            send_test_upload_response(
                &mut stream,
                200,
                false,
                &format!(r#"{{"uploadUrl":"{}"}}"#, state.upload_url),
            )
            .await;
            return;
        }
        if method == "GET" {
            state.status_calls.fetch_add(1, Ordering::SeqCst);
            if matches!(
                state.failure,
                TestUploadFailure::PutTransportUnknown
                    | TestUploadFailure::PutTransportUnknownAlways
                    | TestUploadFailure::PutTransportCommitted
            ) {
                let body = if matches!(state.failure, TestUploadFailure::PutTransportCommitted) {
                    r#"{"nextExpectedRanges":[]}"#
                } else {
                    r#"{"nextExpectedRanges":["0-"]}"#
                };
                send_test_upload_response(&mut stream, 200, false, body).await;
            } else {
                send_test_upload_response(&mut stream, 405, false, "{}").await;
            }
            return;
        }
        if method != "PUT" {
            send_test_upload_response(&mut stream, 405, false, "{}").await;
            return;
        }

        state.content_ranges.lock().unwrap().push(content_range);
        state.uploaded_bytes.lock().unwrap().clone_from(&body);
        let call = state.put_calls.fetch_add(1, Ordering::SeqCst);
        if call == 0
            && matches!(
                state.failure,
                TestUploadFailure::PutTransportUnknown
                    | TestUploadFailure::PutTransportUnknownAlways
                    | TestUploadFailure::PutTransportCommitted
            )
        {
            return;
        }
        if matches!(state.failure, TestUploadFailure::PutTransportUnknownAlways) {
            return;
        }
        if call == 0 && matches!(state.failure, TestUploadFailure::Put429) {
            send_test_upload_response(&mut stream, 429, true, "{}").await;
            return;
        }
        if call == 0 && matches!(state.failure, TestUploadFailure::Put503) {
            send_test_upload_response(&mut stream, 503, false, "{}").await;
            return;
        }
        if call == 0 && matches!(state.failure, TestUploadFailure::PutForbidden) {
            send_test_upload_response(&mut stream, 403, false, "{}").await;
            return;
        }
        send_test_upload_response(
            &mut stream,
            201,
            false,
            r#"{"id":"item-id","webUrl":"https://tenant.sharepoint.com/sites/test/generated.txt","spoId":"spo-item-id"}"#,
        )
        .await;
    }

    async fn send_test_upload_response(
        stream: &mut TcpStream,
        status: u16,
        retry_after: bool,
        body: &str,
    ) {
        let reason = match status {
            200 => "OK",
            201 => "Created",
            403 => "Forbidden",
            405 => "Method Not Allowed",
            429 => "Too Many Requests",
            503 => "Service Unavailable",
            _ => "Error",
        };
        let retry_after = if retry_after {
            "Retry-After: 0\r\n"
        } else {
            ""
        };
        let response = format!(
            "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n{retry_after}\r\n{body}",
            body.len()
        );
        let _ = stream.write_all(response.as_bytes()).await;
    }

    fn test_account() -> Account {
        Account {
            access_token: "access".to_owned(),
            graph_access_token: "graph-access".to_owned(),
            oid: "oid".to_owned(),
            tid: "tid".to_owned(),
        }
    }

    fn test_generated_attachment() -> Attachment {
        Attachment {
            kind: "file".to_owned(),
            url: "data:text/plain;base64,SGVsbG8=".to_owned(),
            name:
                "m365-oversize-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.txt"
                    .to_owned(),
            mime_type: "text/plain".to_owned(),
            generated_oversize_text: true,
            ..Attachment::default()
        }
    }

    async fn upload_against_test_server(
        failure: TestUploadFailure,
    ) -> (Result<(), ChatError>, Attachment, Arc<TestUploadState>) {
        upload_against_test_server_with_generated(failure, true).await
    }

    async fn upload_against_test_server_with_generated(
        failure: TestUploadFailure,
        generated_oversize_text: bool,
    ) -> (Result<(), ChatError>, Attachment, Arc<TestUploadState>) {
        let server = TestUploadServer::start(failure).await;
        let mut attachment = test_generated_attachment();
        attachment.generated_oversize_text = generated_oversize_text;
        let endpoint = format!("http://{}/v1.0", server.address);
        let result = upload_document_at(
            &test_account(),
            "conversation",
            "session",
            &mut attachment,
            &endpoint,
            validate_upload_url_for_test,
        )
        .await;
        let state = Arc::clone(&server.state);
        (result, attachment, state)
    }

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
        let name = document_name("../Quarterly report.pdf", false);
        assert!(name.starts_with("Quarterly report-"));
        assert!(name.ends_with(".pdf"));
        assert!(!name.contains('/'));
    }

    #[test]
    fn document_name_preserves_the_current_known_extension_set() {
        for extension in ["xlsx", "pptx", "pdf"] {
            let name = document_name(&format!("sentinel.{extension}"), false);
            assert!(name.ends_with(&format!(".{extension}")), "{name}");
        }
    }

    #[tokio::test]
    async fn staged_unknown_attachment_uploads_exact_bytes_with_txt_transport_name() {
        let server = TestUploadServer::start(TestUploadFailure::Success).await;
        let root = tempfile::tempdir().unwrap();
        let source_path = root.path().join("outlook-export.weird");
        let bytes = b"unknown-extension sentinel bytes";
        std::fs::write(&source_path, bytes).unwrap();
        let mut attachment = Attachment {
            kind: "file".to_owned(),
            name: "outlook-export.weird".to_owned(),
            mime_type: "text/plain".to_owned(),
            staged: Some(StagedAttachmentSource {
                path: source_path,
                size: bytes.len() as u64,
                sha256: format!("{:x}", Sha256::digest(bytes)),
            }),
            ..Attachment::default()
        };
        let endpoint = format!("http://{}/v1.0", server.address);
        upload_document_at(
            &test_account(),
            "conversation",
            "session",
            &mut attachment,
            &endpoint,
            validate_upload_url_for_test,
        )
        .await
        .unwrap();
        assert_eq!(
            server.state.uploaded_bytes.lock().unwrap().as_slice(),
            bytes
        );
        assert!(attachment.transport_name.ends_with(".txt"));
        assert_eq!(attachment.name, "outlook-export.weird");
    }

    #[tokio::test]
    async fn staged_attachment_integrity_is_checked_before_graph_upload() {
        let server = TestUploadServer::start(TestUploadFailure::Success).await;
        let root = tempfile::tempdir().unwrap();
        let source_path = root.path().join("changed.txt");
        let original = b"original";
        std::fs::write(&source_path, b"tampered").unwrap();
        let mut attachment = Attachment {
            kind: "file".to_owned(),
            name: "changed.txt".to_owned(),
            mime_type: "text/plain".to_owned(),
            staged: Some(StagedAttachmentSource {
                path: source_path,
                size: original.len() as u64,
                sha256: format!("{:x}", Sha256::digest(original)),
            }),
            ..Attachment::default()
        };
        let endpoint = format!("http://{}/v1.0", server.address);
        let result = upload_document_at(
            &test_account(),
            "conversation",
            "session",
            &mut attachment,
            &endpoint,
            validate_upload_url_for_test,
        )
        .await;

        assert!(matches!(
            result,
            Err(ChatError::Attachment {
                failure: AttachmentFailureKind::LocalSpool,
                ..
            })
        ));
        assert_eq!(server.state.create_calls.load(Ordering::SeqCst), 0);
        assert!(server.state.uploaded_bytes.lock().unwrap().is_empty());
    }

    #[test]
    fn generated_oversize_document_name_is_stable_across_retries() {
        let original =
            "m365-oversize-012345abcdef012345abcdef012345abcdef012345abcdef012345abcdef0123.txt";
        assert_eq!(document_name(original, true), original);
        assert_eq!(document_name(original, true), original);
        assert_ne!(document_name(original, false), original);
    }

    #[test]
    fn attachment_retry_delay_honors_retry_after_without_exceeding_the_bound() {
        assert_eq!(
            retry_delay(
                0,
                Some(&HeaderValue::from_static("0")),
                SystemTime::UNIX_EPOCH
            ),
            Duration::ZERO
        );
        assert_eq!(
            retry_delay(
                0,
                Some(&HeaderValue::from_static("120")),
                SystemTime::UNIX_EPOCH,
            ),
            DOCUMENT_UPLOAD_MAX_RETRY_DELAY
        );
        assert_eq!(
            retry_delay(0, None, SystemTime::UNIX_EPOCH),
            DOCUMENT_UPLOAD_BACKOFF
        );
        let now = SystemTime::UNIX_EPOCH + Duration::from_secs(1_700_000_000);
        let retry_at = httpdate::fmt_http_date(now + Duration::from_secs(1));
        let retry_after = HeaderValue::from_str(&retry_at).unwrap();
        assert_eq!(
            retry_delay(0, Some(&retry_after), now),
            Duration::from_secs(1)
        );
    }

    #[tokio::test]
    async fn generated_document_retries_transient_create_session_transport() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::CreateTransportUnknown).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 2);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
        assert_eq!(attachment.doc_id, "spo-item-id");
        assert_eq!(attachment.transport_name, attachment.name);
    }

    #[tokio::test]
    async fn generated_document_retries_transient_put_on_the_same_session_and_range() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::Put503).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 2);
        let ranges = state.content_ranges.lock().unwrap().clone();
        assert_eq!(ranges.len(), 2);
        assert_eq!(ranges[0], ranges[1]);
        assert_eq!(attachment.doc_id, "spo-item-id");
    }

    #[tokio::test]
    async fn graph_429_is_bounded_and_retried_using_retry_after() {
        let (result, _, state) = upload_against_test_server(TestUploadFailure::Create429).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 2);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn graph_5xx_is_bounded_and_retried() {
        let (result, _, state) = upload_against_test_server(TestUploadFailure::Create503).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 2);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn sharepoint_429_is_bounded_and_retried_using_retry_after() {
        let (result, _, state) = upload_against_test_server(TestUploadFailure::Put429).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 2);
    }

    #[tokio::test]
    async fn permanent_sharepoint_4xx_is_not_retried_and_is_typed() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::PutForbidden).await;
        let error = result.unwrap_err();
        assert!(matches!(
            error,
            ChatError::Attachment {
                failure: AttachmentFailureKind::SharePointUploadHttp4xx,
                ..
            }
        ));
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
        assert!(attachment.doc_id.is_empty());
    }

    #[tokio::test]
    async fn ordinary_document_upload_does_not_inherit_generated_retry_policy() {
        let (result, _, state) =
            upload_against_test_server_with_generated(TestUploadFailure::Put503, false).await;
        assert!(matches!(
            result,
            Err(ChatError::Attachment {
                failure: AttachmentFailureKind::SharePointUploadHttp5xx,
                ..
            })
        ));
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn unknown_put_outcome_retries_the_same_range_without_new_artifact_session() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::PutTransportUnknown).await;
        result.unwrap();
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 2);
        assert_eq!(state.status_calls.load(Ordering::SeqCst), 1);
        let ranges = state.content_ranges.lock().unwrap().clone();
        assert_eq!(ranges.len(), 2);
        assert_eq!(ranges[0], ranges[1]);
        assert_eq!(attachment.doc_id, "spo-item-id");
    }

    #[tokio::test]
    async fn repeated_unknown_put_outcome_fails_closed_with_bounded_diagnostic() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::PutTransportUnknownAlways).await;
        let error = result.unwrap_err();
        assert!(matches!(
            error,
            ChatError::Attachment {
                failure: AttachmentFailureKind::SharePointUploadTransportUnknown,
                ..
            }
        ));
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 2);
        assert!(attachment.doc_id.is_empty());
        assert!(attachment.reference_url.is_empty());
    }

    #[tokio::test]
    async fn committed_unknown_put_outcome_fails_closed_without_duplicate_range_replay() {
        let (result, attachment, state) =
            upload_against_test_server(TestUploadFailure::PutTransportCommitted).await;
        let error = result.unwrap_err();
        assert!(matches!(
            error,
            ChatError::Attachment {
                failure: AttachmentFailureKind::SharePointUploadTransportUnknown,
                ..
            }
        ));
        assert_eq!(state.create_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.put_calls.load(Ordering::SeqCst), 1);
        assert_eq!(state.status_calls.load(Ordering::SeqCst), 1);
        assert!(attachment.doc_id.is_empty());
    }

    #[test]
    fn prepared_metadata_bounds_are_measured_in_utf16_units() {
        assert!(
            validate_prepared_metadata(
                &"😀".repeat(MAX_PREPARED_DOC_ID_UTF16 / 2),
                MAX_PREPARED_DOC_ID_UTF16,
                "too large"
            )
            .is_ok()
        );
        assert!(
            validate_prepared_metadata(
                &"😀".repeat(MAX_PREPARED_DOC_ID_UTF16 / 2 + 1),
                MAX_PREPARED_DOC_ID_UTF16,
                "too large"
            )
            .is_err()
        );
        assert!(
            validate_prepared_metadata(
                &"\"".repeat(MAX_PREPARED_DOC_ID_UTF16),
                MAX_PREPARED_DOC_ID_UTF16,
                "too large"
            )
            .is_err()
        );
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
                uploaded_session_id: "same-session".to_owned(),
                ..Attachment::default()
            },
            Attachment {
                kind: "image".to_owned(),
                doc_id: "IMG_ready".to_owned(),
                file_type: "png".to_owned(),
                uploaded_conversation_id: "same".to_owned(),
                uploaded_session_id: "same-session".to_owned(),
                ..Attachment::default()
            },
        ];
        prepare(&account, "same", "same-session", &mut ready)
            .await
            .unwrap();
        assert_eq!(ready[0].doc_id, "SPO_ready");
        assert_eq!(ready[1].doc_id, "IMG_ready");

        let error = prepare(&account, "new", "same-session", &mut ready)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("Graph authorization"));
        assert!(matches!(
            error,
            ChatError::Attachment {
                failure: AttachmentFailureKind::GraphAuthorizationUnavailable,
                ..
            }
        ));
        assert!(ready[0].doc_id.is_empty());
        assert!(ready[0].reference_url.is_empty());
        assert!(ready[0].uploaded_conversation_id.is_empty());
        assert!(ready[0].uploaded_session_id.is_empty());

        let mut same_conversation_other_session = vec![Attachment {
            kind: "file".to_owned(),
            doc_id: "SPO_ready".to_owned(),
            transport_name: "ready.txt".to_owned(),
            reference_url: "https://tenant.sharepoint.com/ready".to_owned(),
            uploaded_conversation_id: "same".to_owned(),
            uploaded_session_id: "same-session".to_owned(),
            ..Attachment::default()
        }];
        let error = prepare(
            &account,
            "same",
            "other-session",
            &mut same_conversation_other_session,
        )
        .await
        .unwrap_err();
        assert!(error.to_string().contains("Graph authorization"));
        assert!(same_conversation_other_session[0].doc_id.is_empty());
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
        let error = prepare(&account, "conversation", "session", &mut attachments)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("shared limit of 3"));
    }

    #[tokio::test]
    async fn ordinary_attachment_count_reserves_the_generated_spill_slot() {
        let mut attachments = vec![
            Attachment {
                kind: "file".to_owned(),
                ..Attachment::default()
            },
            Attachment {
                kind: "file".to_owned(),
                ..Attachment::default()
            },
            Attachment {
                kind: "file".to_owned(),
                ..Attachment::default()
            },
        ];
        let account = Account {
            access_token: String::new(),
            graph_access_token: String::new(),
            oid: String::new(),
            tid: String::new(),
        };
        let error = prepare(&account, "conversation", "session", &mut attachments)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("ordinary attachments"));
    }

    #[test]
    fn ipv4_mapped_ipv6_cannot_bypass_private_address_checks() {
        assert!(unsafe_ip("::ffff:127.0.0.1".parse().unwrap()));
        assert!(unsafe_ip("::ffff:192.0.2.1".parse().unwrap()));
        assert!(!unsafe_ip("::ffff:8.8.8.8".parse().unwrap()));
    }
}
