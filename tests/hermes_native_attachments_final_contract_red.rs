#[test]
fn core_attachment_has_no_outlook_provenance_fields() {
    let source = include_str!("../src/chathub.rs");
    let attachment = source
        .split("pub struct Attachment")
        .nth(1)
        .and_then(|source| {
            source
                .split("pub(crate) struct StagedAttachmentSource")
                .next()
        })
        .expect("Attachment definition");
    for field in [
        "pub(crate) original_filename",
        "pub(crate) original_extension",
        "pub(crate) source_mime_type",
        "pub sha256",
        "pub attachment_id",
        "pub source_message_id",
    ] {
        assert!(
            !attachment.contains(field),
            "prototype field remains: {field}"
        );
    }
}

#[test]
fn custom_microsoft_native_provenance_annotation_is_deleted() {
    let source = include_str!("../src/chathub.rs");
    assert!(!source.contains("m365NativeAttachmentProvenance"));
}

#[test]
fn stage_identity_is_deleted_from_the_bridge() {
    for source in [
        include_str!("../src/artifact.rs"),
        include_str!("../src/chathub.rs"),
        include_str!("../src/hermes_attachments.rs"),
        include_str!("../integrations/hermes/m365_native_attachments/__init__.py"),
    ] {
        assert!(!source.contains("stage_identity"));
    }
}

#[test]
fn public_artifact_index_has_no_purpose_or_owner_schema() {
    let source = include_str!("../src/artifact.rs");
    assert!(!source.contains("purpose: String"));
    assert!(!source.contains("owner: String"));
}

#[test]
fn protocol_has_one_signed_native_context_field() {
    let source = include_str!("../src/protocol.rs");
    assert!(source.contains("m365_native_attachment_context"));
    assert!(!source.contains("m365_native_attachments"));
    assert!(!source.contains("m365_native_attachment_error"));
}

#[test]
fn gateway_contract_has_private_store_and_cross_request_cache() {
    let manager = include_str!("../src/hermes_attachments.rs");
    assert!(manager.contains("hermes-native-attachments"));
    assert!(manager.contains("PreparedCache"));
}

#[test]
fn plugin_manifest_declares_recall_dependency_and_no_primary_post_api_cleanup() {
    let manifest = include_str!("../integrations/hermes/m365_native_attachments/plugin.yaml");
    assert!(manifest.contains("requires_plugins:"));
    assert!(manifest.contains("- m365-recall-provenance"));
    assert!(!manifest.contains("post_api_request"));
}

#[test]
fn qualification_has_no_staged_only_keep_exception() {
    let source = include_str!("../src/protocol.rs");
    let production = source
        .split("#[cfg(test)]")
        .next()
        .expect("protocol production source");
    assert!(!production.contains("attachment.staged.is_some()"));
}
