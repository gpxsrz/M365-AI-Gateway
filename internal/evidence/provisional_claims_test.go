package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildProvisionalClaimInventoryClassifiesClosedClaims(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	first, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("provisional claim inventory is not deterministic")
	}
	if first.Inventory.EntryCount != 3 || first.Inventory.DispositionCounts.EvidenceBacked != 1 || first.Inventory.DispositionCounts.ImplementedUnaccepted != 1 || first.Inventory.DispositionCounts.Unsupported != 1 {
		t.Fatalf("unexpected counts: %#v", first.Inventory.DispositionCounts)
	}

	byID := make(map[string]ProvisionalClaimRecordV1, len(first.Inventory.Entries))
	for _, entry := range first.Inventory.Entries {
		byID[entry.ClaimID] = entry
	}
	routeIdentity := byID["route_identity"]
	if routeIdentity.Disposition != ProvisionalClaimEvidenceBacked || len(routeIdentity.AcceptedSupportSHA256) != 2 || len(routeIdentity.Fields) != 0 {
		t.Fatalf("route identity disposition=%#v", routeIdentity)
	}
	tools := byID["tool_calling"]
	if tools.Disposition != ProvisionalClaimImplementedUnaccepted || !tools.CompatibilityPreserved || len(tools.Fields) != 2 || len(tools.HistoricalReferences) != 1 {
		t.Fatalf("tool disposition=%#v", tools)
	}
	for _, field := range tools.Fields {
		if field.WP1ValueSHA256 == "" || field.WP1ValueSHA256 != field.CurrentValueSHA256 {
			t.Fatalf("field compatibility drifted: %#v", field)
		}
	}
	fileUpload := byID["file_upload_api"]
	if fileUpload.Disposition != ProvisionalClaimUnsupported || len(fileUpload.HistoricalReferences) != 1 || fileUpload.HistoricalReferences[0].Classification != ClassificationUnsupported {
		t.Fatalf("unsupported disposition=%#v", fileUpload)
	}
	if got := first.Inventory.EvidenceBackedCapabilityIDs; len(got) != 1 || got[0] != "route_identity" {
		t.Fatalf("evidence-backed capability IDs=%#v", got)
	}
}

func TestBuildProvisionalClaimInventoryRejectsCompatibilityDrift(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	input.CurrentSource = []byte(strings.Replace(string(input.CurrentSource), `"supports_tools": true`, `"supports_tools": false`, 1))
	input.CurrentSourceIdentity = provisionalSourceIdentity(input.CurrentSourceIdentity.Commit, input.CurrentSourceIdentity.Path, input.CurrentSource)
	if _, err := buildProvisionalClaimInventory(input, policies, expectedWP1); validationCode(err) != "compatibility_drift" {
		t.Fatalf("compatibility drift error=%v, want compatibility_drift", err)
	}
}

func TestBuildProvisionalClaimInventoryRejectsUnsupportedWithoutAcceptedNegativeEvidence(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	input.HistoricalInventory.Entries[0].Classification = ClassificationInconclusive
	input.HistoricalInventory.ClassificationCounts.Unsupported = 0
	input.HistoricalInventory.ClassificationCounts.Inconclusive = 2
	input.HistoricalInventorySHA256 = provisionalJSONSHA256(t, input.HistoricalInventory)
	if _, err := buildProvisionalClaimInventory(input, policies, expectedWP1); validationCode(err) != "unsupported_evidence_required" {
		t.Fatalf("unsupported evidence error=%v, want unsupported_evidence_required", err)
	}
}

func TestBuildProvisionalClaimInventoryRejectsAdvancedEvidencePromotion(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	policies[1].Disposition = ProvisionalClaimEvidenceBacked
	policies[1].AcceptedCapabilityID = "tools"
	if _, err := buildProvisionalClaimInventory(input, policies, expectedWP1); validationCode(err) != "verification_scope_forbidden" {
		t.Fatalf("advanced promotion error=%v, want verification_scope_forbidden", err)
	}
}

func TestBuildProvisionalClaimInventoryRejectsSourceIdentityDrift(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	input.WP1SourceIdentity.SHA256 = repeatHex("f", 64)
	if _, err := buildProvisionalClaimInventory(input, policies, expectedWP1); validationCode(err) != "identity_mismatch" {
		t.Fatalf("source drift error=%v, want identity_mismatch", err)
	}
}

func TestValidateProvisionalClaimInventoryRejectsUnknownAndTamperedContent(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	valid, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	expected := provisionalClaimInventoryExpected{
		Input:       input,
		Policies:    policies,
		ExpectedWP1: expectedWP1,
	}
	accepted, err := validateProvisionalClaimInventory(valid.CanonicalJSON, expected)
	if err != nil || accepted.ChecksumSHA256 != valid.ChecksumSHA256 {
		t.Fatalf("valid inventory rejected: accepted=%#v err=%v", accepted, err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(valid.CanonicalJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["extra"] = true
	unknown, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateProvisionalClaimInventory(unknown, expected); validationCode(err) != "unknown_field" {
		t.Fatalf("unknown field error=%v, want unknown_field", err)
	}

	for _, field := range []string{"prompt", "token", "email", "tool_arguments", "attachment_content"} {
		var privacy map[string]any
		if err := json.Unmarshal(valid.CanonicalJSON, &privacy); err != nil {
			t.Fatal(err)
		}
		privacy[field] = "sensitive-sentinel"
		privacyRaw, err := json.Marshal(privacy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := validateProvisionalClaimInventory(privacyRaw, expected); validationCode(err) != "privacy_forbidden" {
			t.Fatalf("privacy field %q error=%v, want privacy_forbidden", field, err)
		}
	}

	tampered := bytes.Replace(valid.CanonicalJSON, []byte(`"disposition":"implemented_unaccepted"`), []byte(`"disposition":"unverified"`), 1)
	if _, err := validateProvisionalClaimInventory(tampered, expected); validationCode(err) != "identity_mismatch" {
		t.Fatalf("tampered inventory error=%v, want identity_mismatch", err)
	}
}

func provisionalClaimFixture(t *testing.T) (ProvisionalClaimBuildInput, []provisionalClaimPolicyV1, ProvisionalClaimSourceIdentityV1) {
	t.Helper()
	wp1Source := []byte(`package web
func modelCatalog() []map[string]any {
	caps := map[string]any{"tools": true}
	out := []map[string]any{}
	out = append(out, map[string]any{"id": "fixture", "supports_tools": true, "capabilities": caps})
	return out
}
`)
	currentSource := []byte(`package web
func modelCatalogForSettingsAndEvidence() []map[string]any {
	caps := map[string]any{"tools": true}
	entry := map[string]any{"supports_tools": true, "capabilities": caps}
	return []map[string]any{entry}
}
`)
	catalog := provisionalCatalogFixture(t)
	historical := HistoricalBaselineInventoryV1{
		Schema:                 HistoricalBaselineInventorySchemaV1,
		NormativeADRSHA256:     repeatHex("d", 64),
		HistoricalSourceSHA256: repeatHex("e", 64),
		AcceptedCatalogSHA256:  catalog.ChecksumSHA256,
		EntryCount:             2,
		HistoricalPassCount:    1,
		HistoricalFailCount:    1,
		ClassificationCounts: HistoricalClassificationCountsV1{
			Unsupported:  1,
			Inconclusive: 1,
		},
		Entries: []HistoricalBaselineDispositionV1{
			{
				CapabilityID:     "file_upload_api",
				HistoricalStatus: TestExecutionFail,
				Classification:   ClassificationUnsupported,
				RationaleCode:    "sidecar_endpoint_absent",
			},
			{
				CapabilityID:     "function_calling",
				HistoricalStatus: TestExecutionPass,
				Classification:   ClassificationInconclusive,
				RationaleCode:    "historical_pass_not_wp2_verified",
			},
		},
	}
	policies := []provisionalClaimPolicyV1{
		{
			ClaimID:                 "file_upload_api",
			PublicSurface:           "http_endpoint",
			Disposition:             ProvisionalClaimUnsupported,
			RationaleCode:           "accepted_sidecar_endpoint_absent",
			HistoricalCapabilityIDs: []string{"file_upload_api"},
			DeferredOwner:           "files_capability",
		},
		{
			ClaimID:              "route_identity",
			PublicSurface:        "accepted_catalog_projection",
			Disposition:          ProvisionalClaimEvidenceBacked,
			RationaleCode:        "accepted_wp2_scoped_evidence",
			AcceptedCapabilityID: "route_identity",
		},
		{
			ClaimID:                 "tool_calling",
			PublicSurface:           "catalog_compatibility",
			FieldPaths:              []string{"capabilities.tools", "supports_tools"},
			Disposition:             ProvisionalClaimImplementedUnaccepted,
			RationaleCode:           "implemented_outside_wp2_acceptance",
			HistoricalCapabilityIDs: []string{"function_calling"},
			DeferredOwner:           "advanced_capability_matrix",
		},
	}
	expectedWP1 := provisionalSourceIdentity("1111111111111111111111111111111111111111", "internal/web/codex_catalog.go", wp1Source)
	input := ProvisionalClaimBuildInput{
		NormativeADRSHA256:        repeatHex("d", 64),
		WP1SourceIdentity:         expectedWP1,
		WP1Source:                 wp1Source,
		CurrentSourceIdentity:     provisionalSourceIdentity("2222222222222222222222222222222222222222", "internal/web/codex_catalog.go", currentSource),
		CurrentSource:             currentSource,
		AcceptedCatalog:           catalog,
		HistoricalInventory:       historical,
		HistoricalInventorySHA256: provisionalJSONSHA256(t, historical),
	}
	return input, policies, expectedWP1
}

func provisionalCatalogFixture(t *testing.T) ValidatedCatalogProjection {
	t.Helper()
	manifest := catalogProjectionTestManifest()
	for i := range manifest.Packages {
		manifest.Packages[i].NormativeADRSHA256 = repeatHex("d", 64)
	}
	raw := mustCatalogProjectionJSON(t, manifest)
	validated, err := ValidateCatalogProjectionManifest(raw, CatalogProjectionExpected{
		ManifestSHA256: catalogProjectionDigest(raw),
		Packages:       manifest.Packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

func provisionalSourceIdentity(commit, path string, raw []byte) ProvisionalClaimSourceIdentityV1 {
	digest := sha256.Sum256(raw)
	return ProvisionalClaimSourceIdentityV1{
		Commit: commit,
		Path:   path,
		Bytes:  int64(len(raw)),
		SHA256: hex.EncodeToString(digest[:]),
	}
}

func provisionalJSONSHA256(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
