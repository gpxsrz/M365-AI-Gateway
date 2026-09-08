use std::{fs, path::Path};

use m365_ai_gateway::checkpoint::CheckpointError;
use m365_ai_gateway::{M365_AUTHORITY_SURFACE, M365AuthoritySurface};

fn checkpoint_error_surface(error: &CheckpointError) -> &'static str {
    match error {
        CheckpointError::Identity => "identity",
        CheckpointError::KeyRequired => "key_required",
        CheckpointError::UnknownCursor => "unknown_cursor",
        CheckpointError::Ambiguous => "ambiguous",
        CheckpointError::Busy => "busy",
        CheckpointError::Capacity => "capacity",
        CheckpointError::HistoryLimit => "history_limit",
        CheckpointError::Stale => "stale",
        CheckpointError::RecoveryRequired => "recovery_required",
        CheckpointError::ConversationDrift => "conversation_drift",
        CheckpointError::Persistence(_) => "persistence",
    }
}

#[test]
fn m365_gateway_does_not_own_a_second_acp_governance_authority() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR"));
    assert!(
        !root.join("src/governance.rs").exists(),
        "ACP core belongs to gpxsrz/Agent-Control-Plane; M365 must not carry a second GovernanceStore"
    );
    assert!(
        !root
            .join("tests/fixtures/governance-v1-structural-acceptance.json")
            .exists(),
        "the M365-local ACP structural harness is donor history, not current provider-owned authority"
    );

    // Ownership is established by the Rust module boundary: the ledger is a
    // private implementation detail and only transport-facing modules are
    // exported to callers. This checks the interface, not a fragile keyword
    // blacklist over implementation text.
    let lib = fs::read_to_string(root.join("src/lib.rs")).expect("read crate module declarations");
    assert!(lib.lines().any(|line| line.trim() == "mod agent_ledger;"));
    assert!(
        !lib.lines()
            .any(|line| line.trim() == "pub mod agent_ledger;")
    );
    assert!(lib.lines().any(|line| line.trim() == "pub mod checkpoint;"));
    assert!(lib.lines().any(|line| line.trim() == "pub mod protocol;"));

    // The legitimate adapter/provenance surfaces remain present while the
    // ACP canonical state machine stays outside this repository.
    assert!(root.join("src/checkpoint.rs").is_file());
    assert!(root.join("src/protocol.rs").is_file());
    assert!(
        root.join("integrations/hermes/m365_recall_provenance/__init__.py")
            .is_file()
    );
    assert!(root.join("docs/zh-TW/agent-governance.md").is_file());
}

#[test]
fn public_authority_surface_is_transport_only() {
    // This exhaustive match makes a future Task/Run/governance authority
    // variant a compile failure. The marker carries no durable state.
    assert_eq!(
        match M365_AUTHORITY_SURFACE {
            M365AuthoritySurface::ModelProviderTransport => "model_provider_transport",
        },
        "model_provider_transport"
    );

    // Exhaustive matching makes a future Task/Run/governance error variant a
    // compile failure instead of a silently expanded public authority surface.
    assert_eq!(
        checkpoint_error_surface(&CheckpointError::RecoveryRequired),
        "recovery_required"
    );

    // The public protocol surface exposes provider request handlers, while its
    // request/projection/ledger types remain crate-local. These function items
    // are compile-time interface checks, not implementation-text searches.
    let _models = m365_ai_gateway::protocol::models;
    let _chat_completions = m365_ai_gateway::protocol::chat_completions;
    let _gateway_router = m365_ai_gateway::Gateway::router;
}
