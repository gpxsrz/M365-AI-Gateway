use std::{
    collections::{HashMap, VecDeque},
    sync::{Arc, Mutex},
};

use axum::{
    Json,
    body::{Body, to_bytes},
    extract::{Request, State},
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{
    artifact::{ResolvedStage, StageRecord, Store},
    attachment,
    chathub::{Attachment, StagedAttachmentSource},
    error::openai_error,
    web::Gateway,
};

pub(crate) const STAGE_PATH: &str = "/hermes/v1/attachments/stage";
pub(crate) const RELEASE_PATH: &str = "/hermes/v1/attachments/release";
pub(crate) const TURN_PATH: &str = "/hermes/v1/attachments/turn";
pub(crate) const CONTEXT_SCHEMA: &str = "m365-hermes-native-attachment-context/v1";
pub(crate) const STAGE_SCHEMA: &str = "m365-hermes-native-attachment-stage/v2";
const STAGE_AUTH_HEADER: &str = "x-m365-hermes-attachment-auth";
const SESSION_HEADER: &str = "x-m365-hermes-session-id";
const TURN_HEADER: &str = "x-m365-hermes-turn-id";
const BINDING_HEADER: &str = "x-m365-hermes-turn-binding";
const EXPECTED_SIZE_HEADER: &str = "x-m365-expected-size";
const STAGE_ID_HEADER: &str = "x-m365-hermes-stage-id";
const TURN_ACTION_HEADER: &str = "x-m365-hermes-turn-action";
const RELEASE_BODY_LIMIT: usize = 64 * 1024;
const MAX_FILENAME_CHARS: usize = 512;
const MAX_EXTENSION_CHARS: usize = 64;
const MAX_MIME_CHARS: usize = 128;
const MAX_PROVENANCE_CHARS: usize = 512;
const MAX_PREPARED_ENTRIES: usize = 256;
const ATTACHMENT_KEY_DOMAIN: &[u8] = b"m365-hermes-native-attachments/v1";
const STAGE_AUTH_DOMAIN: &[u8] = b"m365-hermes-native-attachments/stage/v1";
const RELEASE_AUTH_DOMAIN: &[u8] = b"m365-hermes-native-attachments/release/v1";
const CONTEXT_SIGNATURE_DOMAIN: &[u8] = b"m365-hermes-native-attachments/context/v1";
const BINDING_DOMAIN: &[u8] = b"m365-hermes-native-attachments/binding/v1";
const TURN_AUTH_DOMAIN: &[u8] = b"m365-hermes-native-attachments/turn/v1";

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct NativeAttachmentReference {
    #[serde(rename = "stage_ref")]
    pub(crate) stage_ref: String,
    pub(crate) size: u64,
    pub(crate) sha256: String,
    pub(crate) original_filename: String,
    #[serde(default)]
    pub(crate) extension: String,
    #[serde(default, rename = "mime_type")]
    pub(crate) mime_type: String,
    #[serde(default)]
    pub(crate) attachment_id: String,
    #[serde(default)]
    pub(crate) source_message_id: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct NativeAttachmentContext {
    pub(crate) schema: String,
    pub(crate) session_key: String,
    pub(crate) turn_id: String,
    #[serde(default)]
    pub(crate) attachments: Vec<NativeAttachmentReference>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(crate) error: Option<String>,
    pub(crate) signature: String,
}

#[derive(Clone, Debug, Serialize)]
pub(crate) struct NativeAttachmentMetadata {
    pub(crate) original_filename: String,
    pub(crate) extension: String,
    pub(crate) mime_type: String,
    pub(crate) sha256: String,
    pub(crate) attachment_id: String,
    pub(crate) source_message_id: String,
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, Eq, PartialEq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum FailureReason {
    InvalidAttachmentCount,
    AttachmentSlotUnavailable,
    PathDenied,
    FileMissing,
    NotRegularFile,
    EmptyFile,
    FileTooLarge,
    HashMismatch,
    LocalFileChanged,
    StageTransportFailed,
    NativeAttachmentStateLost,
    NativeAttachmentBindingInvalid,
    NativeAttachmentContextMalformed,
    NativeAttachmentCapabilityInvalidOrExpired,
    NativeAttachmentSlotConflict,
    NativeAttachmentIntegrityFailed,
    NativeAttachmentsNotAllowed,
}

impl FailureReason {
    pub(crate) const fn code(self) -> &'static str {
        match self {
            Self::InvalidAttachmentCount => "invalid_attachment_count",
            Self::AttachmentSlotUnavailable => "attachment_slot_unavailable",
            Self::PathDenied => "path_denied",
            Self::FileMissing => "file_missing",
            Self::NotRegularFile => "not_regular_file",
            Self::EmptyFile => "empty_file",
            Self::FileTooLarge => "file_too_large",
            Self::HashMismatch => "hash_mismatch",
            Self::LocalFileChanged => "local_file_changed",
            Self::StageTransportFailed => "stage_transport_failed",
            Self::NativeAttachmentStateLost => "native_attachment_state_lost",
            Self::NativeAttachmentBindingInvalid => "native_attachment_binding_invalid",
            Self::NativeAttachmentContextMalformed => "native_attachment_context_malformed",
            Self::NativeAttachmentCapabilityInvalidOrExpired => {
                "native_attachment_capability_invalid_or_expired"
            }
            Self::NativeAttachmentSlotConflict => "native_attachment_slot_conflict",
            Self::NativeAttachmentIntegrityFailed => "native_attachment_integrity_failed",
            Self::NativeAttachmentsNotAllowed => "native_attachments_not_allowed",
        }
    }

    pub(crate) const fn message(self) -> &'static str {
        match self {
            Self::InvalidAttachmentCount => {
                "m365_native_attach requires one or two original attachments"
            }
            Self::AttachmentSlotUnavailable => {
                "native attachment slots are unavailable for this request"
            }
            Self::PathDenied => "the local attachment path is outside the allowed roots",
            Self::FileMissing => "the local attachment file is missing",
            Self::NotRegularFile => "the local attachment is not a regular file",
            Self::EmptyFile => "the local attachment is empty",
            Self::FileTooLarge => "the local attachment exceeds the 512 MiB limit",
            Self::HashMismatch => "the local attachment hash does not match expected_sha256",
            Self::LocalFileChanged => "the local attachment changed while it was being staged",
            Self::StageTransportFailed => {
                "the native attachment could not be staged; request was not sent upstream"
            }
            Self::NativeAttachmentStateLost => {
                "native attachment state was lost; request was not sent upstream"
            }
            Self::NativeAttachmentBindingInvalid => {
                "native attachment binding is invalid; request was not sent upstream"
            }
            Self::NativeAttachmentContextMalformed => {
                "native attachment context is malformed; request was not sent upstream"
            }
            Self::NativeAttachmentCapabilityInvalidOrExpired => {
                "native attachment capability is invalid or expired; request was not sent upstream"
            }
            Self::NativeAttachmentSlotConflict => {
                "native attachment slots conflict with ordinary attachments"
            }
            Self::NativeAttachmentIntegrityFailed => {
                "native attachment integrity validation failed; request was not sent upstream"
            }
            Self::NativeAttachmentsNotAllowed => {
                "native attachments are only available on the Hermes chat route"
            }
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReleaseRequest {
    stage_refs: Vec<String>,
}

#[derive(Serialize)]
struct StageResponse<'a> {
    schema: &'a str,
    capability: &'a str,
    size: u64,
    sha256: &'a str,
}

#[derive(Clone, Debug)]
pub(crate) struct ResolvedNativeAttachments {
    pub(crate) attachments: Vec<Attachment>,
    pub(crate) metadata: Vec<NativeAttachmentMetadata>,
    pub(crate) stage_refs: Vec<String>,
}

#[derive(Clone, Debug)]
struct PreparedEntry {
    conversation_id: String,
    session_id: String,
    doc_id: String,
    transport_name: String,
    reference_url: String,
    file_type: String,
}

struct PreparedCache {
    values: HashMap<String, PreparedEntry>,
    order: VecDeque<String>,
}

const MAX_TURN_BINDINGS: usize = 1_024;

#[derive(Clone, Copy)]
enum TurnAction {
    Bind,
    End,
}

impl TurnAction {
    fn parse(value: &str) -> Option<Self> {
        match value {
            "bind" => Some(Self::Bind),
            "end" => Some(Self::End),
            _ => None,
        }
    }

    const fn as_str(self) -> &'static str {
        match self {
            Self::Bind => "bind",
            Self::End => "end",
        }
    }
}

struct TurnAuthority {
    values: HashMap<String, String>,
    order: VecDeque<String>,
}

pub(crate) struct NativeAttachmentManager {
    store: Store,
    attach_key: [u8; 32],
    enabled: bool,
    prepared: Mutex<PreparedCache>,
    turns: Mutex<TurnAuthority>,
}

impl std::fmt::Debug for NativeAttachmentManager {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("NativeAttachmentManager(..)")
    }
}

impl NativeAttachmentManager {
    pub(crate) fn open(
        data_dir: &std::path::Path,
        recall_secret: &str,
    ) -> Result<Self, crate::error::GatewayError> {
        Ok(Self {
            store: Store::open(data_dir.join("hermes-native-attachments"))?,
            attach_key: hmac_sha256(recall_secret.as_bytes(), ATTACHMENT_KEY_DOMAIN),
            enabled: !recall_secret.trim().is_empty(),
            prepared: Mutex::new(PreparedCache {
                values: HashMap::new(),
                order: VecDeque::new(),
            }),
            turns: Mutex::new(TurnAuthority {
                values: HashMap::new(),
                order: VecDeque::new(),
            }),
        })
    }

    #[cfg(test)]
    pub(crate) fn open_for_test(root: &std::path::Path, recall_secret: &str) -> Self {
        Self {
            store: Store::open(root.join("hermes-native-attachments")).unwrap(),
            attach_key: hmac_sha256(recall_secret.as_bytes(), ATTACHMENT_KEY_DOMAIN),
            enabled: !recall_secret.trim().is_empty(),
            prepared: Mutex::new(PreparedCache {
                values: HashMap::new(),
                order: VecDeque::new(),
            }),
            turns: Mutex::new(TurnAuthority {
                values: HashMap::new(),
                order: VecDeque::new(),
            }),
        }
    }

    pub(crate) fn auth_header_present(&self, headers: &HeaderMap) -> bool {
        headers
            .get(STAGE_AUTH_HEADER)
            .and_then(|value| value.to_str().ok())
            .is_some_and(|value| {
                value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
            })
    }

    pub(crate) fn turn_binding(&self, session_key: &str, turn_id: &str) -> String {
        hex(&hmac_sha256(
            &self.attach_key,
            &join_lines(&[BINDING_DOMAIN, session_key.as_bytes(), turn_id.as_bytes()]),
        ))
    }

    pub(crate) fn context_signature(&self, context: &NativeAttachmentContext) -> String {
        format!(
            "sha256={}",
            hex(&hmac_sha256(&self.attach_key, &canonical_context(context)))
        )
    }

    fn bind_current_turn(&self, session_key: &str, turn_id: &str) -> bool {
        let mut authority = self.turns.lock().expect("turn authority poisoned");
        if authority
            .values
            .get(session_key)
            .is_some_and(|value| value != turn_id)
        {
            return false;
        }
        authority
            .values
            .insert(session_key.to_owned(), turn_id.to_owned());
        authority.order.retain(|value| value != session_key);
        authority.order.push_back(session_key.to_owned());
        while authority.order.len() > MAX_TURN_BINDINGS {
            if let Some(old_session) = authority.order.pop_front() {
                authority.values.remove(&old_session);
            }
        }
        true
    }

    fn clear_current_turn(&self, session_key: &str, turn_id: &str) {
        let mut authority = self.turns.lock().expect("turn authority poisoned");
        if authority
            .values
            .get(session_key)
            .is_some_and(|value| value == turn_id)
        {
            authority.values.remove(session_key);
            authority.order.retain(|value| value != session_key);
        }
    }

    fn is_current_turn(&self, session_key: &str, turn_id: &str) -> bool {
        self.turns
            .lock()
            .expect("turn authority poisoned")
            .values
            .get(session_key)
            .is_some_and(|value| value == turn_id)
    }

    fn turn_authenticated(&self, headers: &HeaderMap) -> Option<TurnAction> {
        if !self.enabled {
            return None;
        }
        let session = header_text(headers, SESSION_HEADER)?;
        let turn = header_text(headers, TURN_HEADER)?;
        let binding = header_text(headers, BINDING_HEADER)?;
        let action = TurnAction::parse(header_text(headers, TURN_ACTION_HEADER)?)?;
        let provided = header_text(headers, STAGE_AUTH_HEADER)?;
        if !valid_identity(session, 512)
            || !valid_identity(turn, 512)
            || !valid_hex(binding, 64)
            || binding != self.turn_binding(session, turn)
        {
            return None;
        }
        let expected = hex(&hmac_sha256(
            &self.attach_key,
            &join_lines(&[
                TURN_AUTH_DOMAIN,
                session.as_bytes(),
                turn.as_bytes(),
                binding.as_bytes(),
                action.as_str().as_bytes(),
            ]),
        ));
        constant_time_equal(provided.as_bytes(), expected.as_bytes()).then_some(action)
    }

    pub(crate) fn stage_authenticated(&self, headers: &HeaderMap) -> bool {
        if !self.enabled {
            return false;
        }
        let Some(session) = header_text(headers, SESSION_HEADER) else {
            return false;
        };
        let Some(turn) = header_text(headers, TURN_HEADER) else {
            return false;
        };
        let Some(binding) = header_text(headers, BINDING_HEADER) else {
            return false;
        };
        let Some(size) = header_text(headers, EXPECTED_SIZE_HEADER) else {
            return false;
        };
        let Some(stage_id) = header_text(headers, STAGE_ID_HEADER) else {
            return false;
        };
        let Some(provided) = header_text(headers, STAGE_AUTH_HEADER) else {
            return false;
        };
        if !valid_identity(session, 512)
            || !valid_identity(turn, 512)
            || !valid_hex(binding, 64)
            || !valid_capability(stage_id)
            || size.parse::<u64>().is_err()
            || binding != self.turn_binding(session, turn)
        {
            return false;
        }
        let expected = hex(&hmac_sha256(
            &self.attach_key,
            &join_lines(&[
                STAGE_AUTH_DOMAIN,
                session.as_bytes(),
                turn.as_bytes(),
                binding.as_bytes(),
                size.as_bytes(),
                stage_id.as_bytes(),
            ]),
        ));
        constant_time_equal(provided.as_bytes(), expected.as_bytes())
    }

    #[cfg(test)]
    pub(crate) fn stage_headers_for_test(
        &self,
        session_key: &str,
        turn_id: &str,
        size: u64,
    ) -> HeaderMap {
        let binding = self.turn_binding(session_key, turn_id);
        let stage_id = "I".repeat(43);
        let auth = hex(&hmac_sha256(
            &self.attach_key,
            &join_lines(&[
                STAGE_AUTH_DOMAIN,
                session_key.as_bytes(),
                turn_id.as_bytes(),
                binding.as_bytes(),
                size.to_string().as_bytes(),
                stage_id.as_bytes(),
            ]),
        ));
        let mut headers = HeaderMap::new();
        headers.insert(SESSION_HEADER, session_key.parse().unwrap());
        headers.insert(TURN_HEADER, turn_id.parse().unwrap());
        headers.insert(BINDING_HEADER, binding.parse().unwrap());
        headers.insert(EXPECTED_SIZE_HEADER, size.to_string().parse().unwrap());
        headers.insert(STAGE_ID_HEADER, stage_id.parse().unwrap());
        headers.insert(STAGE_AUTH_HEADER, auth.parse().unwrap());
        headers
    }

    #[cfg(test)]
    pub(crate) fn turn_headers_for_test(
        &self,
        session_key: &str,
        turn_id: &str,
        action: &str,
    ) -> HeaderMap {
        let binding = self.turn_binding(session_key, turn_id);
        let auth = hex(&hmac_sha256(
            &self.attach_key,
            &join_lines(&[
                TURN_AUTH_DOMAIN,
                session_key.as_bytes(),
                turn_id.as_bytes(),
                binding.as_bytes(),
                action.as_bytes(),
            ]),
        ));
        let mut headers = HeaderMap::new();
        headers.insert(SESSION_HEADER, session_key.parse().unwrap());
        headers.insert(TURN_HEADER, turn_id.parse().unwrap());
        headers.insert(BINDING_HEADER, binding.parse().unwrap());
        headers.insert(TURN_ACTION_HEADER, action.parse().unwrap());
        headers.insert(STAGE_AUTH_HEADER, auth.parse().unwrap());
        headers
    }

    #[cfg(test)]
    pub(crate) fn release_headers_for_test(
        &self,
        session_key: &str,
        turn_id: &str,
        stage_refs: &[String],
    ) -> HeaderMap {
        let binding = self.turn_binding(session_key, turn_id);
        let mut lines = vec![
            RELEASE_AUTH_DOMAIN.to_vec(),
            session_key.as_bytes().to_vec(),
            turn_id.as_bytes().to_vec(),
            binding.as_bytes().to_vec(),
        ];
        lines.extend(stage_refs.iter().map(|value| value.as_bytes().to_vec()));
        let auth = hex(&hmac_sha256(&self.attach_key, &join_lines_vec(&lines)));
        let mut headers = HeaderMap::new();
        headers.insert(SESSION_HEADER, session_key.parse().unwrap());
        headers.insert(TURN_HEADER, turn_id.parse().unwrap());
        headers.insert(BINDING_HEADER, binding.parse().unwrap());
        headers.insert(STAGE_AUTH_HEADER, auth.parse().unwrap());
        headers
    }

    async fn stage_body(
        &self,
        binding: &str,
        stage_id: &str,
        body: Body,
    ) -> Result<StageRecord, crate::error::GatewayError> {
        self.store
            .stage_body_with_capability(binding, stage_id, body)
            .await
    }

    #[cfg(test)]
    pub(crate) async fn stage_for_test(
        &self,
        session_key: &str,
        turn_id: &str,
        bytes: &[u8],
    ) -> StageRecord {
        self.bind_current_turn(session_key, turn_id);
        let binding = self.turn_binding(session_key, turn_id);
        self.store
            .stage_body_for_test(&binding, Body::from(bytes.to_vec()))
            .await
            .unwrap()
    }

    #[cfg(test)]
    pub(crate) fn bind_turn_for_test(&self, session_key: &str, turn_id: &str) {
        assert!(self.bind_current_turn(session_key, turn_id));
    }

    fn release_authenticated(&self, headers: &HeaderMap, stage_refs: &[String]) -> bool {
        if !self.enabled {
            return false;
        }
        let Some(session) = header_text(headers, SESSION_HEADER) else {
            return false;
        };
        let Some(turn) = header_text(headers, TURN_HEADER) else {
            return false;
        };
        let Some(binding) = header_text(headers, BINDING_HEADER) else {
            return false;
        };
        let Some(provided) = header_text(headers, STAGE_AUTH_HEADER) else {
            return false;
        };
        if !valid_identity(session, 512)
            || !valid_identity(turn, 512)
            || !valid_hex(binding, 64)
            || stage_refs.is_empty()
            || stage_refs.len() > 2
            || stage_refs.iter().any(|value| !valid_capability(value))
            || binding != self.turn_binding(session, turn)
        {
            return false;
        }
        let mut lines = vec![
            RELEASE_AUTH_DOMAIN.to_vec(),
            session.as_bytes().to_vec(),
            turn.as_bytes().to_vec(),
            binding.as_bytes().to_vec(),
        ];
        lines.extend(stage_refs.iter().map(|value| value.as_bytes().to_vec()));
        let expected = hex(&hmac_sha256(&self.attach_key, &join_lines_vec(&lines)));
        constant_time_equal(provided.as_bytes(), expected.as_bytes())
    }

    pub(crate) fn resolve_context(
        &self,
        context: &NativeAttachmentContext,
        request_session_key: &str,
        existing_attachment_count: usize,
    ) -> Result<ResolvedNativeAttachments, FailureReason> {
        if !self.enabled {
            return Err(FailureReason::NativeAttachmentsNotAllowed);
        }
        if context.schema != CONTEXT_SCHEMA
            || !valid_identity(&context.session_key, 512)
            || !valid_identity(&context.turn_id, 512)
            || !valid_signature(&context.signature)
            || !constant_time_equal(
                self.context_signature(context).as_bytes(),
                context.signature.as_bytes(),
            )
        {
            return Err(FailureReason::NativeAttachmentContextMalformed);
        }
        if request_session_key != context.session_key {
            return Err(FailureReason::NativeAttachmentBindingInvalid);
        }
        if let Some(error) = context.error.as_deref() {
            if !context.attachments.is_empty() {
                return Err(FailureReason::NativeAttachmentContextMalformed);
            }
            return Err(
                failure_from_code(error).unwrap_or(FailureReason::NativeAttachmentContextMalformed)
            );
        }
        if context.attachments.is_empty() || context.attachments.len() > 2 {
            return Err(FailureReason::NativeAttachmentContextMalformed);
        }
        if !self.is_current_turn(&context.session_key, &context.turn_id) {
            return Err(FailureReason::NativeAttachmentBindingInvalid);
        }
        if existing_attachment_count + context.attachments.len() > 2 {
            return Err(FailureReason::NativeAttachmentSlotConflict);
        }
        let binding = self.turn_binding(&context.session_key, &context.turn_id);
        let mut attachments = Vec::with_capacity(context.attachments.len());
        let mut metadata = Vec::with_capacity(context.attachments.len());
        let mut stage_refs = Vec::with_capacity(context.attachments.len());
        for reference in &context.attachments {
            validate_reference(reference)?;
            if stage_refs.iter().any(|value| value == &reference.stage_ref) {
                return Err(FailureReason::NativeAttachmentSlotConflict);
            }
            let resolved = self
                .store
                .resolve_staged(
                    &reference.stage_ref,
                    &reference.sha256,
                    reference.size,
                    &binding,
                )
                .map_err(|error| match error {
                    crate::error::GatewayError::Storage(message)
                        if message == "staged attachment integrity check failed" =>
                    {
                        FailureReason::NativeAttachmentIntegrityFailed
                    }
                    _ => FailureReason::NativeAttachmentCapabilityInvalidOrExpired,
                })?;
            attachments.push(attachment_from_reference(reference, resolved));
            metadata.push(NativeAttachmentMetadata {
                original_filename: reference.original_filename.clone(),
                extension: reference.extension.clone(),
                mime_type: reference.mime_type.clone(),
                sha256: reference.sha256.clone(),
                attachment_id: reference.attachment_id.clone(),
                source_message_id: reference.source_message_id.clone(),
            });
            stage_refs.push(reference.stage_ref.clone());
        }
        Ok(ResolvedNativeAttachments {
            attachments,
            metadata,
            stage_refs,
        })
    }

    pub(crate) fn apply_prepared_cache(
        &self,
        attachments: &mut [Attachment],
        indexes: &[usize],
        stage_refs: &[String],
        conversation_id: &str,
        session_id: &str,
    ) {
        let mut cache = self
            .prepared
            .lock()
            .expect("prepared attachment cache poisoned");
        for (index, stage_ref) in indexes.iter().zip(stage_refs) {
            let Some(attachment) = attachments.get_mut(*index) else {
                continue;
            };
            let Some(entry) = cache.values.get(stage_ref).cloned() else {
                continue;
            };
            if entry.conversation_id != conversation_id || entry.session_id != session_id {
                continue;
            }
            attachment.doc_id = entry.doc_id;
            attachment.transport_name = entry.transport_name;
            attachment.reference_url = entry.reference_url;
            attachment.file_type = entry.file_type;
            attachment.uploaded_conversation_id = conversation_id.to_owned();
            attachment.uploaded_session_id = session_id.to_owned();
            touch_cache(&mut cache.order, stage_ref);
        }
    }

    pub(crate) fn record_prepared(
        &self,
        attachments: &[Attachment],
        indexes: &[usize],
        stage_refs: &[String],
        conversation_id: &str,
        session_id: &str,
    ) {
        let mut cache = self
            .prepared
            .lock()
            .expect("prepared attachment cache poisoned");
        for (index, stage_ref) in indexes.iter().zip(stage_refs) {
            let Some(attachment) = attachments.get(*index) else {
                continue;
            };
            if attachment.doc_id.is_empty()
                || attachment.uploaded_conversation_id != conversation_id
                || attachment.uploaded_session_id != session_id
                || (attachment.kind == "file"
                    && (attachment.transport_name.is_empty()
                        || attachment.reference_url.is_empty()))
            {
                continue;
            }
            cache.values.insert(
                stage_ref.clone(),
                PreparedEntry {
                    conversation_id: conversation_id.to_owned(),
                    session_id: session_id.to_owned(),
                    doc_id: attachment.doc_id.clone(),
                    transport_name: attachment.transport_name.clone(),
                    reference_url: attachment.reference_url.clone(),
                    file_type: attachment.file_type.clone(),
                },
            );
            touch_cache(&mut cache.order, stage_ref);
        }
        while cache.order.len() > MAX_PREPARED_ENTRIES {
            if let Some(old) = cache.order.pop_front() {
                cache.values.remove(&old);
            }
        }
    }

    pub(crate) fn release_refs(&self, session_key: &str, turn_id: &str, stage_refs: &[String]) {
        let binding = self.turn_binding(session_key, turn_id);
        for stage_ref in stage_refs {
            self.store.release_staged(stage_ref, &binding);
            self.prepared
                .lock()
                .expect("prepared attachment cache poisoned")
                .values
                .remove(stage_ref);
        }
        let mut cache = self
            .prepared
            .lock()
            .expect("prepared attachment cache poisoned");
        cache
            .order
            .retain(|value| !stage_refs.iter().any(|ref_value| ref_value == value));
    }
}

fn touch_cache(order: &mut VecDeque<String>, stage_ref: &str) {
    order.retain(|value| value != stage_ref);
    order.push_back(stage_ref.to_owned());
}

fn validate_reference(reference: &NativeAttachmentReference) -> Result<(), FailureReason> {
    if !valid_capability(&reference.stage_ref) {
        return Err(FailureReason::NativeAttachmentCapabilityInvalidOrExpired);
    }
    if !valid_sha256(&reference.sha256)
        || reference.size == 0
        || reference.size > attachment::MAX_BYTES
        || !bounded_text(&reference.original_filename, MAX_FILENAME_CHARS, false)
        || !bounded_text(&reference.extension, MAX_EXTENSION_CHARS, true)
        || !bounded_text(&reference.mime_type, MAX_MIME_CHARS, true)
        || !bounded_text(&reference.attachment_id, MAX_PROVENANCE_CHARS, true)
        || !bounded_text(&reference.source_message_id, MAX_PROVENANCE_CHARS, true)
    {
        return Err(FailureReason::NativeAttachmentIntegrityFailed);
    }
    Ok(())
}

fn attachment_from_reference(
    reference: &NativeAttachmentReference,
    resolved: ResolvedStage,
) -> Attachment {
    let kind = if reference
        .mime_type
        .split(';')
        .next()
        .is_some_and(|mime| mime.trim().to_ascii_lowercase().starts_with("image/"))
    {
        "image"
    } else {
        "file"
    };
    Attachment {
        kind: kind.to_owned(),
        name: reference.original_filename.clone(),
        mime_type: reference.mime_type.clone(),
        staged: Some(StagedAttachmentSource {
            path: resolved.path,
            size: resolved.size,
            sha256: resolved.sha256,
        }),
        ..Attachment::default()
    }
}

pub(crate) async fn stage(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    if !gateway
        .hermes_attachments
        .stage_authenticated(request.headers())
    {
        return openai_error(
            StatusCode::UNAUTHORIZED,
            "auth_error",
            "auth_error",
            "Hermes attachment authentication required",
        );
    }
    let session_key = header_text(request.headers(), SESSION_HEADER)
        .unwrap_or_default()
        .to_owned();
    let turn_id = header_text(request.headers(), TURN_HEADER)
        .unwrap_or_default()
        .to_owned();
    if !gateway
        .hermes_attachments
        .is_current_turn(&session_key, &turn_id)
    {
        return native_attachment_error(FailureReason::NativeAttachmentBindingInvalid);
    }
    let binding = header_text(request.headers(), BINDING_HEADER)
        .unwrap_or_default()
        .to_owned();
    let stage_id = header_text(request.headers(), STAGE_ID_HEADER)
        .unwrap_or_default()
        .to_owned();
    let expected_size = match header_text(request.headers(), EXPECTED_SIZE_HEADER)
        .and_then(|value| value.parse::<u64>().ok())
    {
        Some(value) if value > 0 && value <= attachment::MAX_BYTES => value,
        _ => return stage_metadata_error(),
    };
    if request
        .headers()
        .get(header::CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<u64>().ok())
        .is_some_and(|value| value != expected_size)
    {
        return stage_upload_error(StatusCode::CONFLICT);
    }
    let staged = match gateway
        .hermes_attachments
        .stage_body(&binding, &stage_id, request.into_body())
        .await
    {
        Ok(staged) => staged,
        Err(_) => return stage_upload_error(StatusCode::BAD_GATEWAY),
    };
    if staged.size != expected_size {
        gateway
            .hermes_attachments
            .store
            .release_staged(&staged.capability, &binding);
        return native_attachment_error(FailureReason::LocalFileChanged);
    }
    Json(StageResponse {
        schema: STAGE_SCHEMA,
        capability: &staged.capability,
        size: staged.size,
        sha256: &staged.sha256,
    })
    .into_response()
}

pub(crate) async fn turn(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let action = match gateway
        .hermes_attachments
        .turn_authenticated(request.headers())
    {
        Some(action) => action,
        None => {
            return openai_error(
                StatusCode::UNAUTHORIZED,
                "auth_error",
                "auth_error",
                "Hermes attachment authentication required",
            );
        }
    };
    let session_key = header_text(request.headers(), SESSION_HEADER)
        .unwrap_or_default()
        .to_owned();
    let turn_id = header_text(request.headers(), TURN_HEADER)
        .unwrap_or_default()
        .to_owned();
    match action {
        TurnAction::Bind => {
            if !gateway
                .hermes_attachments
                .bind_current_turn(&session_key, &turn_id)
            {
                return native_attachment_error(FailureReason::NativeAttachmentBindingInvalid);
            }
        }
        TurnAction::End => gateway
            .hermes_attachments
            .clear_current_turn(&session_key, &turn_id),
    }
    Json(serde_json::json!({"ok": true})).into_response()
}

pub(crate) async fn release(State(gateway): State<Arc<Gateway>>, request: Request) -> Response {
    let headers = request.headers().clone();
    let bytes = match to_bytes(request.into_body(), RELEASE_BODY_LIMIT).await {
        Ok(bytes) => bytes,
        Err(_) => return stage_metadata_error(),
    };
    let release = match serde_json::from_slice::<ReleaseRequest>(&bytes) {
        Ok(release)
            if !release.stage_refs.is_empty()
                && release.stage_refs.len() <= 2
                && release
                    .stage_refs
                    .iter()
                    .all(|value| valid_capability(value)) =>
        {
            release
        }
        _ => return stage_metadata_error(),
    };
    if !gateway
        .hermes_attachments
        .release_authenticated(&headers, &release.stage_refs)
    {
        return openai_error(
            StatusCode::UNAUTHORIZED,
            "auth_error",
            "auth_error",
            "Hermes attachment authentication required",
        );
    }
    let session_key = header_text(&headers, SESSION_HEADER).unwrap_or_default();
    let turn_id = header_text(&headers, TURN_HEADER).unwrap_or_default();
    gateway
        .hermes_attachments
        .release_refs(session_key, turn_id, &release.stage_refs);
    Json(serde_json::json!({"ok": true})).into_response()
}

pub(crate) fn stage_path(path: &str) -> bool {
    matches!(path, STAGE_PATH | RELEASE_PATH | TURN_PATH)
}

fn native_attachment_error(reason: FailureReason) -> Response {
    openai_error(
        StatusCode::CONFLICT,
        "invalid_state_error",
        reason.code(),
        reason.message(),
    )
}

fn stage_metadata_error() -> Response {
    openai_error(
        StatusCode::BAD_REQUEST,
        "invalid_request_error",
        "invalid_stage_metadata",
        "native attachment stage metadata is invalid",
    )
}

fn stage_upload_error(status: StatusCode) -> Response {
    openai_error(
        status,
        "upstream_error",
        FailureReason::StageTransportFailed.code(),
        FailureReason::StageTransportFailed.message(),
    )
}

fn failure_from_code(code: &str) -> Option<FailureReason> {
    [
        FailureReason::InvalidAttachmentCount,
        FailureReason::AttachmentSlotUnavailable,
        FailureReason::PathDenied,
        FailureReason::FileMissing,
        FailureReason::NotRegularFile,
        FailureReason::EmptyFile,
        FailureReason::FileTooLarge,
        FailureReason::HashMismatch,
        FailureReason::LocalFileChanged,
        FailureReason::StageTransportFailed,
        FailureReason::NativeAttachmentStateLost,
        FailureReason::NativeAttachmentBindingInvalid,
        FailureReason::NativeAttachmentContextMalformed,
        FailureReason::NativeAttachmentCapabilityInvalidOrExpired,
        FailureReason::NativeAttachmentSlotConflict,
        FailureReason::NativeAttachmentIntegrityFailed,
        FailureReason::NativeAttachmentsNotAllowed,
    ]
    .into_iter()
    .find(|reason| reason.code() == code)
}

fn canonical_context(context: &NativeAttachmentContext) -> Vec<u8> {
    let mut lines = vec![
        CONTEXT_SIGNATURE_DOMAIN.to_vec(),
        context.schema.as_bytes().to_vec(),
        context.session_key.as_bytes().to_vec(),
        context.turn_id.as_bytes().to_vec(),
        context
            .error
            .as_deref()
            .unwrap_or_default()
            .as_bytes()
            .to_vec(),
        context.attachments.len().to_string().into_bytes(),
    ];
    for reference in &context.attachments {
        lines.extend([
            reference.stage_ref.as_bytes().to_vec(),
            reference.size.to_string().into_bytes(),
            reference.sha256.as_bytes().to_vec(),
            reference.original_filename.as_bytes().to_vec(),
            reference.extension.as_bytes().to_vec(),
            reference.mime_type.as_bytes().to_vec(),
            reference.attachment_id.as_bytes().to_vec(),
            reference.source_message_id.as_bytes().to_vec(),
        ]);
    }
    join_lines_vec(&lines)
}

#[cfg(test)]
pub(crate) fn canonical_context_for_test(context: &NativeAttachmentContext) -> Vec<u8> {
    canonical_context(context)
}

fn join_lines(parts: &[&[u8]]) -> Vec<u8> {
    join_lines_vec(&parts.iter().map(|part| part.to_vec()).collect::<Vec<_>>())
}

fn join_lines_vec(parts: &[Vec<u8>]) -> Vec<u8> {
    let mut output = Vec::new();
    for (index, part) in parts.iter().enumerate() {
        if index > 0 {
            output.push(b'\n');
        }
        output.extend_from_slice(part);
    }
    output
}

fn hmac_sha256(key: &[u8], message: &[u8]) -> [u8; 32] {
    let mut block = [0_u8; 64];
    if key.len() > block.len() {
        block[..32].copy_from_slice(&Sha256::digest(key));
    } else {
        block[..key.len()].copy_from_slice(key);
    }
    let mut inner = [0_u8; 64];
    let mut outer = [0_u8; 64];
    for index in 0..64 {
        inner[index] = block[index] ^ 0x36;
        outer[index] = block[index] ^ 0x5c;
    }
    let mut inner_hasher = Sha256::new();
    inner_hasher.update(inner);
    inner_hasher.update(message);
    let inner_digest = inner_hasher.finalize();
    let mut outer_hasher = Sha256::new();
    outer_hasher.update(outer);
    outer_hasher.update(inner_digest);
    outer_hasher.finalize().into()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn constant_time_equal(left: &[u8], right: &[u8]) -> bool {
    if left.len() != right.len() {
        return false;
    }
    left.iter()
        .zip(right)
        .fold(0_u8, |difference, (left, right)| {
            difference | (left ^ right)
        })
        == 0
}

fn valid_signature(value: &str) -> bool {
    value.len() == 71 && value.starts_with("sha256=") && valid_hex(&value[7..], 64)
}

fn valid_capability(value: &str) -> bool {
    value.len() == 43
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_'))
}

fn valid_sha256(value: &str) -> bool {
    valid_hex(value, 64)
}

fn valid_hex(value: &str, length: usize) -> bool {
    value.len() == length && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn valid_identity(value: &str, limit: usize) -> bool {
    !value.trim().is_empty()
        && value.chars().count() <= limit
        && !value.chars().any(char::is_control)
}

fn bounded_text(value: &str, limit: usize, allow_empty: bool) -> bool {
    (allow_empty || !value.trim().is_empty())
        && value.chars().count() <= limit
        && !value.chars().any(char::is_control)
}

fn header_text<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers.get(name).and_then(|value| value.to_str().ok())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canonical_context_is_unicode_stable() {
        let context = NativeAttachmentContext {
            schema: CONTEXT_SCHEMA.to_owned(),
            session_key: "session-附件".to_owned(),
            turn_id: "turn-😀".to_owned(),
            attachments: vec![NativeAttachmentReference {
                stage_ref: "A".repeat(43),
                size: 7,
                sha256: "b".repeat(64),
                original_filename: "報告.xlsx".to_owned(),
                extension: "xlsx".to_owned(),
                mime_type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
                    .to_owned(),
                attachment_id: "附件-1".to_owned(),
                source_message_id: "郵件-1".to_owned(),
            }],
            error: None,
            signature: String::new(),
        };
        assert_eq!(
            String::from_utf8(canonical_context_for_test(&context)).unwrap(),
            format!(
                "{}\n{CONTEXT_SCHEMA}\nsession-附件\nturn-😀\n\n1\n{}\n7\n{}\n報告.xlsx\nxlsx\napplication/vnd.openxmlformats-officedocument.spreadsheetml.sheet\n附件-1\n郵件-1",
                String::from_utf8(CONTEXT_SIGNATURE_DOMAIN.to_vec()).unwrap(),
                "A".repeat(43),
                "b".repeat(64),
            )
        );
    }

    #[test]
    fn unicode_canonical_context_matches_the_python_fixture() {
        #[derive(Deserialize)]
        struct Fixture {
            context: NativeAttachmentContext,
            canonical: String,
        }

        let fixture: Fixture = serde_json::from_str(include_str!(
            "../integrations/hermes/m365_native_attachments/canonical_context_unicode_fixture.json"
        ))
        .unwrap();
        assert_eq!(
            String::from_utf8(canonical_context_for_test(&fixture.context)).unwrap(),
            fixture.canonical
        );
    }

    #[test]
    fn derived_key_does_not_equal_raw_secret() {
        let manager =
            NativeAttachmentManager::open_for_test(tempfile::tempdir().unwrap().path(), "secret");
        assert_ne!(manager.turn_binding("session", "turn"), "secret");
    }

    #[test]
    fn authenticated_turn_authority_rejects_a_replayed_previous_turn() {
        let manager =
            NativeAttachmentManager::open_for_test(tempfile::tempdir().unwrap().path(), "secret");
        let bind_a = manager.turn_headers_for_test("session", "turn-a", "bind");
        assert!(matches!(
            manager.turn_authenticated(&bind_a),
            Some(TurnAction::Bind)
        ));
        assert!(manager.bind_current_turn("session", "turn-a"));
        let bind_b = manager.turn_headers_for_test("session", "turn-b", "bind");
        assert!(matches!(
            manager.turn_authenticated(&bind_b),
            Some(TurnAction::Bind)
        ));
        assert!(!manager.bind_current_turn("session", "turn-b"));
        manager.clear_current_turn("session", "turn-a");
        assert!(manager.bind_current_turn("session", "turn-b"));
        assert!(!manager.is_current_turn("session", "turn-a"));
        assert!(manager.is_current_turn("session", "turn-b"));
    }

    #[tokio::test]
    async fn prepared_cache_reuses_only_the_same_m365_binding_and_cleans_up() {
        let root = tempfile::tempdir().unwrap();
        let manager = NativeAttachmentManager::open_for_test(root.path(), "secret");
        let staged = manager
            .stage_for_test("session", "turn", b"native sentinel")
            .await;
        let mut context = NativeAttachmentContext {
            schema: CONTEXT_SCHEMA.to_owned(),
            session_key: "session".to_owned(),
            turn_id: "turn".to_owned(),
            attachments: vec![NativeAttachmentReference {
                stage_ref: staged.capability.clone(),
                size: staged.size,
                sha256: staged.sha256.clone(),
                original_filename: "native.txt".to_owned(),
                extension: "txt".to_owned(),
                mime_type: "text/plain".to_owned(),
                attachment_id: "attachment-1".to_owned(),
                source_message_id: "message-1".to_owned(),
            }],
            error: None,
            signature: String::new(),
        };
        context.signature = manager.context_signature(&context);

        manager.clear_current_turn("session", "turn");
        assert!(manager.bind_current_turn("session", "other-turn"));
        assert!(matches!(
            manager.resolve_context(&context, "session", 0),
            Err(FailureReason::NativeAttachmentBindingInvalid)
        ));
        manager.clear_current_turn("session", "other-turn");
        assert!(manager.bind_current_turn("session", "turn"));

        let mut prepared = manager.resolve_context(&context, "session", 0).unwrap();
        prepared.attachments[0].doc_id = "prepared-doc".to_owned();
        prepared.attachments[0].transport_name = "native-random.txt".to_owned();
        prepared.attachments[0].reference_url = "https://tenant.example/native".to_owned();
        prepared.attachments[0].uploaded_conversation_id = "conversation".to_owned();
        prepared.attachments[0].uploaded_session_id = "session".to_owned();
        manager.record_prepared(
            &prepared.attachments,
            &[0],
            &prepared.stage_refs,
            "conversation",
            "session",
        );

        let mut reused = manager.resolve_context(&context, "session", 0).unwrap();
        manager.apply_prepared_cache(
            &mut reused.attachments,
            &[0],
            &reused.stage_refs,
            "conversation",
            "session",
        );
        assert_eq!(reused.attachments[0].doc_id, "prepared-doc");
        assert_eq!(
            reused.attachments[0].reference_url,
            "https://tenant.example/native"
        );

        let mut drifted = manager.resolve_context(&context, "session", 0).unwrap();
        manager.apply_prepared_cache(
            &mut drifted.attachments,
            &[0],
            &drifted.stage_refs,
            "other-conversation",
            "session",
        );
        assert!(drifted.attachments[0].doc_id.is_empty());

        manager.release_refs("session", "turn", &reused.stage_refs);
        assert!(manager.resolve_context(&context, "session", 0).is_err());
    }
}
