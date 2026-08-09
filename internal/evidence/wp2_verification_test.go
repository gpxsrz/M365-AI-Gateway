package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func TestBuildWP2VerificationPackageIsDeterministicAndClosed(t *testing.T) {
	input := wp2VerificationFixture(t)
	first, err := BuildWP2VerificationPackage(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWP2VerificationPackage(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("WP2 verification package is not deterministic")
	}
	if first.Package.Schema != WP2VerificationPackageSchemaV1 || first.Package.Revision.Head != input.Revision.Head || first.Package.Revision.Tree != input.Revision.Tree {
		t.Fatalf("package identity=%#v", first.Package)
	}
	if got := first.Package.Traceability.VerifiedCapabilityIDs; !reflect.DeepEqual(got, []string{"basic_text_delivery", "protocol_transport", "route_identity", "route_mapping"}) {
		t.Fatalf("verified capabilities=%#v", got)
	}
	if first.Package.Traceability.CatalogIdentityCount != 20 || first.Package.Traceability.GlobalClaimCount != 12 || first.Package.Traceability.AccountDependentGlobalClaimCount != 1 {
		t.Fatalf("catalog traceability=%#v", first.Package.Traceability)
	}
	if first.Package.HistoricalInventory.EntryCount != 48 || first.Package.ProvisionalInventory.EntryCount != 36 {
		t.Fatalf("inventory bindings: historical=%d provisional=%d", first.Package.HistoricalInventory.EntryCount, first.Package.ProvisionalInventory.EntryCount)
	}
	if first.Package.States != (WP2VerificationStatesV1{Implementation: "complete", Verification: "pass", Review: "pending_independent", Deployment: "not_authorized"}) {
		t.Fatalf("states=%#v", first.Package.States)
	}
	validated, err := ValidateWP2VerificationPackage(first.CanonicalJSON, WP2VerificationPackageExpected{Input: input})
	if err != nil || validated.ChecksumSHA256 != first.ChecksumSHA256 {
		t.Fatalf("validation=%#v err=%v", validated, err)
	}
}

func TestBuildWP2VerificationPackageRejectsIdentityScopeAndDeferredDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*WP2VerificationBuildInput)
		code   string
	}{
		{name: "dirty source", mutate: func(input *WP2VerificationBuildInput) { input.Revision.Modified = true }, code: "identity_mismatch"},
		{name: "sidecar revision", mutate: func(input *WP2VerificationBuildInput) { input.Sidecar.VCSRevision = repeatHex("f", 40) }, code: "identity_mismatch"},
		{name: "missing package", mutate: func(input *WP2VerificationBuildInput) { input.Packages = input.Packages[:7] }, code: "identity_mismatch"},
		{name: "settings stable name", mutate: func(input *WP2VerificationBuildInput) {
			input.EffectiveSettings.Path = "alternate/effective-settings.json"
		}, code: "identity_mismatch"},
		{name: "advanced catalog claim", mutate: func(input *WP2VerificationBuildInput) {
			input.Catalog.Manifest.GlobalClaims[0].Capabilities = append(input.Catalog.Manifest.GlobalClaims[0].Capabilities, AccountPoolGlobalCapabilityClaimV1{
				CapabilityID: "tools", Classification: ClassificationVerified, SupportingEvidenceSHA256: []string{repeatHex("e", 64)},
			})
		}, code: "verification_scope_forbidden"},
		{name: "deferred counted", mutate: func(input *WP2VerificationBuildInput) {
			input.VerificationInventory.Deferred[0].AcceptanceCounted = true
		}, code: "verification_scope_forbidden"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := wp2VerificationFixture(t)
			test.mutate(&input)
			if _, err := BuildWP2VerificationPackage(input); validationCode(err) != test.code {
				t.Fatalf("error=%v, want %s", err, test.code)
			}
		})
	}
}

func TestValidateWP2VerificationPackageRejectsUnknownAndTamperedContent(t *testing.T) {
	input := wp2VerificationFixture(t)
	valid, err := BuildWP2VerificationPackage(input)
	if err != nil {
		t.Fatal(err)
	}
	expected := WP2VerificationPackageExpected{Input: input}
	var decoded map[string]any
	if err := json.Unmarshal(valid.CanonicalJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["extra"] = true
	unknown, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWP2VerificationPackage(unknown, expected); validationCode(err) != "unknown_field" {
		t.Fatalf("unknown field error=%v", err)
	}
	for _, field := range []string{"prompt", "token", "cookie", "request_body", "response_body", "email", "tenant_id", "object_id", "account_mapping", "tool_arguments", "attachment_content"} {
		var privacy map[string]any
		if err := json.Unmarshal(valid.CanonicalJSON, &privacy); err != nil {
			t.Fatal(err)
		}
		privacy[field] = "sensitive-sentinel"
		privacyRaw, err := json.Marshal(privacy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateWP2VerificationPackage(privacyRaw, expected); validationCode(err) != "privacy_forbidden" {
			t.Fatalf("privacy field %q error=%v", field, err)
		}
	}
	tampered := bytes.Replace(valid.CanonicalJSON, []byte(`"verification":"pass"`), []byte(`"verification":"fail"`), 1)
	if _, err := ValidateWP2VerificationPackage(tampered, expected); validationCode(err) != "identity_mismatch" {
		t.Fatalf("tamper error=%v", err)
	}
}

func wp2VerificationFixture(t *testing.T) WP2VerificationBuildInput {
	t.Helper()
	catalog := historicalCatalogFixture(t)
	for index := range catalog.Manifest.Packages {
		catalog.Manifest.Packages[index].NormativeADRSHA256 = historicalNormativeADRSHA256
	}
	catalogRaw, err := json.Marshal(catalog.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	catalogDigest := sha256.Sum256(catalogRaw)
	catalog.CanonicalJSON = catalogRaw
	catalog.ChecksumSHA256 = hex.EncodeToString(catalogDigest[:])
	catalog = expandWP2VerificationCatalog(t, catalog)

	historicalSource, err := buildHistoricalFixtureSource(t, historicalFixtureFS(t), acceptedHistoricalBinaryFixture())
	if err != nil {
		t.Fatal(err)
	}
	historical, err := BuildHistoricalBaselineInventory(historicalSource, catalog)
	if err != nil {
		t.Fatal(err)
	}

	provisionalInput, _, _ := provisionalClaimFixture(t)
	provisionalInput.NormativeADRSHA256 = historicalNormativeADRSHA256
	provisionalInput.AcceptedCatalog = catalog
	provisionalInput.HistoricalInventory = historical.Inventory
	provisionalInput.HistoricalInventorySHA256 = historical.ChecksumSHA256
	verifiedIDs := []string{"basic_text_delivery", "protocol_transport", "route_identity", "route_mapping"}
	provisionalEntries := make([]ProvisionalClaimRecordV1, 0, 36)
	for _, capabilityID := range verifiedIDs {
		provisionalEntries = append(provisionalEntries, ProvisionalClaimRecordV1{
			ClaimID: capabilityID, PublicSurface: "accepted_wp2_evidence", Disposition: ProvisionalClaimEvidenceBacked,
			RationaleCode: "accepted_wp2_evidence", AcceptedCapabilityID: capabilityID,
			AcceptedSupportSHA256: []string{repeatHex("d", 64)}, HistoricalReferences: []ProvisionalClaimHistoricalReferenceV1{},
			Fields: []ProvisionalClaimFieldV1{}, CompatibilityPreserved: true,
		})
	}
	for index := 1; index <= 11; index++ {
		provisionalEntries = append(provisionalEntries, ProvisionalClaimRecordV1{
			ClaimID: fmt.Sprintf("implemented_%02d", index), PublicSurface: "catalog_compatibility",
			Disposition: ProvisionalClaimImplementedUnaccepted, RationaleCode: "implemented_outside_wp2_acceptance",
			AcceptedSupportSHA256: []string{}, HistoricalReferences: []ProvisionalClaimHistoricalReferenceV1{},
			Fields: []ProvisionalClaimFieldV1{}, CompatibilityPreserved: true,
		})
	}
	for index := 1; index <= 19; index++ {
		provisionalEntries = append(provisionalEntries, ProvisionalClaimRecordV1{
			ClaimID: fmt.Sprintf("unverified_%02d", index), PublicSurface: "catalog_compatibility",
			Disposition: ProvisionalClaimUnverified, RationaleCode: "deferred_without_complete_evidence",
			AcceptedSupportSHA256: []string{}, HistoricalReferences: []ProvisionalClaimHistoricalReferenceV1{},
			Fields: []ProvisionalClaimFieldV1{}, CompatibilityPreserved: true,
		})
	}
	for index := 1; index <= 2; index++ {
		provisionalEntries = append(provisionalEntries, ProvisionalClaimRecordV1{
			ClaimID: fmt.Sprintf("unsupported_%02d", index), PublicSurface: "http_endpoint",
			Disposition: ProvisionalClaimUnsupported, RationaleCode: "accepted_sidecar_endpoint_absent",
			AcceptedSupportSHA256: []string{}, HistoricalReferences: []ProvisionalClaimHistoricalReferenceV1{},
			Fields: []ProvisionalClaimFieldV1{}, CompatibilityPreserved: true,
		})
	}
	sort.Slice(provisionalEntries, func(i, j int) bool { return provisionalEntries[i].ClaimID < provisionalEntries[j].ClaimID })
	provisionalInventory := ProvisionalClaimInventoryV1{
		Schema: ProvisionalClaimInventorySchemaV1, NormativeADRSHA256: historicalNormativeADRSHA256,
		WP1Source: provisionalInput.WP1SourceIdentity, CurrentSource: provisionalInput.CurrentSourceIdentity,
		AcceptedCatalogSHA256: catalog.ChecksumSHA256, HistoricalInventorySHA256: historical.ChecksumSHA256,
		EvidenceBackedCapabilityIDs: verifiedIDs, EntryCount: len(provisionalEntries),
		DispositionCounts: ProvisionalClaimDispositionCountsV1{EvidenceBacked: 4, ImplementedUnaccepted: 11, Unverified: 19, Unsupported: 2}, Entries: provisionalEntries,
	}
	provisionalRaw, err := json.Marshal(provisionalInventory)
	if err != nil {
		t.Fatal(err)
	}
	provisionalDigest := sha256.Sum256(provisionalRaw)
	provisional := ValidatedProvisionalClaimInventory{
		Inventory: provisionalInventory, CanonicalJSON: provisionalRaw, ChecksumSHA256: hex.EncodeToString(provisionalDigest[:]),
	}
	packages := make([]WP2AcceptedPackageIdentityV1, 0, 8)
	payloadSeeds := []string{"a", "b", "c", "d", "e", "f", "1", "2"}
	packageKinds := []string{"web_choice_mapping", "route_protocol", "alias_projection", "legacy_configured", "account_pool", "catalog_projection", "historical_baseline", "provisional_claims"}
	packageSchemas := []string{
		"m365-wp2-web-choice-evidence-set/v1", RouteProtocolEvidenceSetSchemaV1, AliasProjectionEvidenceSetSchemaV1,
		LegacyConfiguredEvidenceSetSchemaV1, AccountPoolEvidenceSetSchemaV1, CatalogEvidenceSetSchemaV1,
		"m365-wp2-historical-baseline-evidence-set/v1", ProvisionalClaimEvidenceSetSchemaV1,
	}
	for issue := 3; issue <= 10; issue++ {
		repositoryPath := fmt.Sprintf("docs/wp2/evidence/issue-%d/evidence-index.json", issue)
		if issue == 3 {
			repositoryPath = "docs/wp2/evidence/issue-3/SHA256SUMS"
		}
		packages = append(packages, WP2AcceptedPackageIdentityV1{
			Issue: issue, Kind: packageKinds[issue-3], RepositoryPath: repositoryPath,
			RepositorySHA256: repeatHex(string(rune('0'+issue%10)), 64), RepositoryBytes: int64(1000 + issue),
			PayloadSchema: packageSchemas[issue-3], PayloadSHA256: repeatHex(payloadSeeds[issue-3], 64), PayloadBytes: int64(2000 + issue),
		})
	}
	return WP2VerificationBuildInput{
		NormativeADRSHA256: historicalNormativeADRSHA256,
		Revision:           WP2VerificationRevisionIdentityV1{Head: provisionalInput.CurrentSourceIdentity.Commit, Tree: repeatHex("a", 40), Modified: false},
		Sidecar:            WP2VerificationBinaryIdentityV1{SHA256: repeatHex("b", 64), Bytes: 111, VCSRevision: provisionalInput.CurrentSourceIdentity.Commit, VCSModified: false},
		Harness:            WP2VerificationBinaryIdentityV1{SHA256: repeatHex("c", 64), Bytes: 222, VCSRevision: provisionalInput.CurrentSourceIdentity.Commit, VCSModified: false},
		EffectiveSettings:  WP2VerificationFileIdentityV1{Path: "effective-settings.json", SHA256: repeatHex("e", 64), Bytes: 333, Schema: "m365-wp2-exact-verification-settings/v1"},
		CapabilityContract: WP2VerificationFileIdentityV1{Path: "docs/specs/wp2-capability-evidence-v1.md", SHA256: repeatHex("f", 64), Bytes: 444, Schema: SchemaV1},
		Packages:           packages,
		WebChoiceMappings: []WP2VerifiedWebChoiceV1{
			{WebChoiceID: "m365-auto", ObservedTone: "Magic", MappingBehavior: "case_normalized", EvidenceSHA256: repeatHex("1", 64)},
			{WebChoiceID: "m365-gpt-5.5-quick-response", ObservedTone: "Gpt_5_5_Chat", MappingBehavior: "exact", EvidenceSHA256: repeatHex("2", 64)},
			{WebChoiceID: "m365-gpt-5.6-think-deeper", ObservedTone: "Gpt_5_6_Reasoning", MappingBehavior: "exact", EvidenceSHA256: repeatHex("3", 64)},
			{WebChoiceID: "quick", ObservedTone: "Chat", MappingBehavior: "different", EvidenceSHA256: repeatHex("4", 64)},
			{WebChoiceID: "think-deeper", ObservedTone: "Reasoning", MappingBehavior: "different", EvidenceSHA256: repeatHex("5", 64)},
		},
		Catalog:               catalog,
		Historical:            historical,
		Provisional:           provisional,
		VerificationInventory: DefaultWP2VerificationInventory(),
		States:                WP2VerificationStatesV1{Implementation: "complete", Verification: "pass", Review: "pending_independent", Deployment: "not_authorized"},
	}
}

func expandWP2VerificationCatalog(t *testing.T, catalog ValidatedCatalogProjection) ValidatedCatalogProjection {
	t.Helper()
	for index := 1; index <= 17; index++ {
		requested := fmt.Sprintf("fixture-alias-%02d", index)
		digest := sha256.Sum256([]byte(requested))
		evidenceHash := hex.EncodeToString(digest[:])
		evidenceSetSHA, err := CatalogProjectionEvidenceSetSHA256([]string{evidenceHash})
		if err != nil {
			t.Fatal(err)
		}
		catalog.Manifest.Identities = append(catalog.Manifest.Identities, CatalogProjectionIdentityEvidenceV1{
			RequestedIdentity: requested, CanonicalRoute: "m365-auto", ResolvedTone: "magic",
			RouteKind: "alias", CatalogVisibility: "hidden", MappingEvidence: "web_payload_verified",
			IdentityStatus: "dynamic_unidentified", PackageIssue: 5, CatalogObservationSHA256: evidenceHash,
			SupportingEvidenceSHA256: []string{evidenceHash}, CapabilityEvidenceSetSHA256: evidenceSetSHA,
		})
	}
	routes := []struct {
		id   string
		tone string
	}{
		{"m365-auto", "magic"},
		{"m365-gpt-5.5-quick-response", "Gpt_5_5_Chat"},
		{"m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning"},
	}
	for _, route := range routes {
		capabilities := make([]AccountPoolGlobalCapabilityClaimV1, 0, len(accountPoolCapabilityOrder))
		for _, capabilityID := range accountPoolCapabilityOrder {
			capabilities = append(capabilities, AccountPoolGlobalCapabilityClaimV1{
				CapabilityID: capabilityID, Classification: ClassificationVerified,
				SupportingEvidenceSHA256: []string{repeatHex("e", 64)},
			})
		}
		catalog.Manifest.GlobalClaims = append(catalog.Manifest.GlobalClaims, AccountPoolGlobalClaimV1{
			CanonicalRoute: route.id, ResolvedTone: route.tone, Protocol: "legacy_chat_nonstream",
			EligibleProfileCount: 2, UnavailableProfileCount: 1, RouteEligibility: ClassificationVerified,
			Capabilities: capabilities,
		})
	}
	sort.Slice(catalog.Manifest.Identities, func(i, j int) bool {
		return catalog.Manifest.Identities[i].RequestedIdentity < catalog.Manifest.Identities[j].RequestedIdentity
	})
	sort.Slice(catalog.Manifest.GlobalClaims, func(i, j int) bool {
		return accountPoolEntryKey(catalog.Manifest.GlobalClaims[i].CanonicalRoute, catalog.Manifest.GlobalClaims[i].Protocol) < accountPoolEntryKey(catalog.Manifest.GlobalClaims[j].CanonicalRoute, catalog.Manifest.GlobalClaims[j].Protocol)
	})
	if err := validateCatalogProjectionManifest(catalog.Manifest); err != nil {
		t.Fatalf("expanded catalog invalid: %v", err)
	}
	canonical, err := json.Marshal(catalog.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	catalog.CanonicalJSON = canonical
	catalog.ChecksumSHA256 = hex.EncodeToString(digest[:])
	return catalog
}
