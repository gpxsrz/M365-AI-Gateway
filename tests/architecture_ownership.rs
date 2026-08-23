use std::{fs, path::Path};

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

    for path in ["src/lib.rs", "src/web.rs", "src/protocol.rs"] {
        let source = fs::read_to_string(root.join(path)).expect("read tracked Rust source");
        for forbidden in [
            "GovernanceStore",
            "agent-governance.json",
            "/api/admin/governance/runtime",
        ] {
            assert!(
                !source.contains(forbidden),
                "{path} still contains provider-owned ACP authority marker {forbidden:?}"
            );
        }
    }
}
