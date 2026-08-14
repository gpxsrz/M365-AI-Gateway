package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const (
	WP2VerificationPackageSchemaV1         = "m365-wp2-exact-verification-package/v1"
	WP2VerificationInventorySchemaV1       = "m365-wp2-verification-inventory/v1"
	WP2ExactVerificationSettingsSchemaV1   = "m365-wp2-exact-verification-settings/v1"
	WP2ExactVerificationSettingsStableName = "effective-settings.json"
	MaxWP2VerificationPackageBytes         = 4 * 1024 * 1024
)

type WP2VerificationRevisionIdentityV1 struct {
	Head     string `json:"head"`
	Tree     string `json:"tree"`
	Modified bool   `json:"modified"`
}

type WP2VerificationBinaryIdentityV1 struct {
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	VCSRevision string `json:"vcs_revision"`
	VCSModified bool   `json:"vcs_modified"`
}

type WP2VerificationFileIdentityV1 struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Schema string `json:"schema"`
}

type WP2AcceptedPackageIdentityV1 struct {
	Issue                  int    `json:"issue"`
	Kind                   string `json:"kind"`
	RepositoryPath         string `json:"repository_path"`
	RepositorySHA256       string `json:"repository_sha256"`
	RepositoryBytes        int64  `json:"repository_bytes"`
	PayloadSchema          string `json:"payload_schema"`
	PayloadSHA256          string `json:"payload_sha256"`
	PayloadBytes           int64  `json:"payload_bytes"`
	SourceHead             string `json:"source_head,omitempty"`
	BinarySHA256           string `json:"binary_sha256,omitempty"`
	HarnessSHA256          string `json:"harness_sha256,omitempty"`
	SettingsSHA256         string `json:"effective_settings_sha256,omitempty"`
	ProfileSetSHA256       string `json:"profile_set_sha256,omitempty"`
	AcceptedManifestSHA256 string `json:"accepted_manifest_sha256,omitempty"`
	InventorySHA256        string `json:"inventory_sha256,omitempty"`
}

type WP2VerifiedWebChoiceV1 struct {
	WebChoiceID     string `json:"web_choice_id"`
	ObservedTone    string `json:"observed_tone"`
	MappingBehavior string `json:"mapping_behavior"`
	EvidenceSHA256  string `json:"evidence_sha256"`
}

type WP2VerificationTestV1 struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	AcceptanceCounted bool   `json:"acceptance_counted"`
}

type WP2VerificationInventoryV1 struct {
	Schema   string                  `json:"schema"`
	InScope  []WP2VerificationTestV1 `json:"in_scope"`
	Deferred []WP2VerificationTestV1 `json:"deferred"`
}

type WP2VerificationStatesV1 struct {
	Implementation string `json:"implementation"`
	Verification   string `json:"verification"`
	Review         string `json:"review"`
	Deployment     string `json:"deployment"`
}

type WP2VerificationTraceabilityV1 struct {
	AcceptedPackageCount             int                                 `json:"accepted_package_count"`
	WebChoiceMappingCount            int                                 `json:"web_choice_mapping_count"`
	CatalogManifestSHA256            string                              `json:"catalog_manifest_sha256"`
	CatalogIdentityCount             int                                 `json:"catalog_identity_count"`
	GlobalClaimCount                 int                                 `json:"global_claim_count"`
	AccountDependentGlobalClaimCount int                                 `json:"account_dependent_global_claim_count"`
	VerifiedCapabilityIDs            []string                            `json:"verified_capability_ids"`
	HistoricalInventorySHA256        string                              `json:"historical_inventory_sha256"`
	HistoricalEntryCount             int                                 `json:"historical_entry_count"`
	HistoricalClassificationCounts   HistoricalClassificationCountsV1    `json:"historical_classification_counts"`
	ProvisionalInventorySHA256       string                              `json:"provisional_inventory_sha256"`
	ProvisionalEntryCount            int                                 `json:"provisional_entry_count"`
	ProvisionalDispositionCounts     ProvisionalClaimDispositionCountsV1 `json:"provisional_disposition_counts"`
}

type WP2VerificationPackageV1 struct {
	Schema                string                            `json:"schema"`
	NormativeADRSHA256    string                            `json:"normative_adr_sha256"`
	Revision              WP2VerificationRevisionIdentityV1 `json:"revision"`
	Sidecar               WP2VerificationBinaryIdentityV1   `json:"sidecar"`
	Harness               WP2VerificationBinaryIdentityV1   `json:"harness"`
	EffectiveSettings     WP2VerificationFileIdentityV1     `json:"effective_settings"`
	CapabilityContract    WP2VerificationFileIdentityV1     `json:"capability_contract"`
	AcceptedPackages      []WP2AcceptedPackageIdentityV1    `json:"accepted_packages"`
	WebChoiceMappings     []WP2VerifiedWebChoiceV1          `json:"web_choice_mappings"`
	CatalogManifest       CatalogProjectionManifestV1       `json:"catalog_manifest"`
	HistoricalInventory   HistoricalBaselineInventoryV1     `json:"historical_inventory"`
	ProvisionalInventory  ProvisionalClaimInventoryV1       `json:"provisional_inventory"`
	VerificationInventory WP2VerificationInventoryV1        `json:"verification_inventory"`
	Traceability          WP2VerificationTraceabilityV1     `json:"traceability"`
	States                WP2VerificationStatesV1           `json:"states"`
}

type ValidatedWP2VerificationPackage struct {
	Package        WP2VerificationPackageV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type WP2VerificationBuildInput struct {
	NormativeADRSHA256    string
	Revision              WP2VerificationRevisionIdentityV1
	Sidecar               WP2VerificationBinaryIdentityV1
	Harness               WP2VerificationBinaryIdentityV1
	EffectiveSettings     WP2VerificationFileIdentityV1
	CapabilityContract    WP2VerificationFileIdentityV1
	Packages              []WP2AcceptedPackageIdentityV1
	WebChoiceMappings     []WP2VerifiedWebChoiceV1
	Catalog               ValidatedCatalogProjection
	Historical            ValidatedHistoricalBaselineInventory
	Provisional           ValidatedProvisionalClaimInventory
	VerificationInventory WP2VerificationInventoryV1
	States                WP2VerificationStatesV1
}

type WP2VerificationPackageExpected struct {
	Input WP2VerificationBuildInput
}

var allowedWP2VerificationPackageFields = map[string]struct{}{
	"schema":                 {},
	"normative_adr_sha256":   {},
	"revision":               {},
	"sidecar":                {},
	"harness":                {},
	"effective_settings":     {},
	"capability_contract":    {},
	"accepted_packages":      {},
	"web_choice_mappings":    {},
	"catalog_manifest":       {},
	"historical_inventory":   {},
	"provisional_inventory":  {},
	"verification_inventory": {},
	"traceability":           {},
	"states":                 {},
}

var wp2VerificationInScopeTestIDs = []string{
	"accepted_package_layers",
	"account_pool_global_intersection",
	"catalog_claim_traceability",
	"catalog_standard_field_compatibility",
	"focused_wp2_tests",
	"full_go_suite",
	"go_build",
	"go_vet",
	"historical_48_entry_inventory",
	"package_checksum_verification",
	"privacy_structural_scan",
	"provisional_36_claim_inventory",
	"race_suite",
	"source_binary_harness_identity",
	"wp1_fail_closed_regressions",
}

var wp2VerificationDeferredTestIDs = []string{
	"apply_patch",
	"files",
	"function_calling",
	"image_detail",
	"image_generation",
	"native_grounding",
	"native_search",
	"parallel_tools",
	"reasoning_enhancements",
	"tool_result_continuation",
	"tools",
	"verbosity",
	"vision",
}

func DefaultWP2VerificationInventory() WP2VerificationInventoryV1 {
	inventory := WP2VerificationInventoryV1{Schema: WP2VerificationInventorySchemaV1}
	for _, id := range wp2VerificationInScopeTestIDs {
		inventory.InScope = append(inventory.InScope, WP2VerificationTestV1{ID: id, Status: "PASS", AcceptanceCounted: true})
	}
	for _, id := range wp2VerificationDeferredTestIDs {
		inventory.Deferred = append(inventory.Deferred, WP2VerificationTestV1{ID: id, Status: "DEFERRED", AcceptanceCounted: false})
	}
	return inventory
}

func BuildWP2VerificationPackage(input WP2VerificationBuildInput) (ValidatedWP2VerificationPackage, error) {
	traceability, err := validateWP2VerificationInput(input)
	if err != nil {
		return ValidatedWP2VerificationPackage{}, err
	}
	pkg := WP2VerificationPackageV1{
		Schema:                WP2VerificationPackageSchemaV1,
		NormativeADRSHA256:    input.NormativeADRSHA256,
		Revision:              input.Revision,
		Sidecar:               input.Sidecar,
		Harness:               input.Harness,
		EffectiveSettings:     input.EffectiveSettings,
		CapabilityContract:    input.CapabilityContract,
		AcceptedPackages:      append([]WP2AcceptedPackageIdentityV1(nil), input.Packages...),
		WebChoiceMappings:     append([]WP2VerifiedWebChoiceV1(nil), input.WebChoiceMappings...),
		CatalogManifest:       input.Catalog.Manifest,
		HistoricalInventory:   input.Historical.Inventory,
		ProvisionalInventory:  input.Provisional.Inventory,
		VerificationInventory: cloneWP2VerificationInventory(input.VerificationInventory),
		Traceability:          traceability,
		States:                input.States,
	}
	canonical, err := json.Marshal(pkg)
	if err != nil {
		return ValidatedWP2VerificationPackage{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	digest := sha256.Sum256(canonical)
	return ValidatedWP2VerificationPackage{
		Package:        pkg,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func ValidateWP2VerificationPackage(raw []byte, expected WP2VerificationPackageExpected) (ValidatedWP2VerificationPackage, error) {
	if len(raw) == 0 {
		return ValidatedWP2VerificationPackage{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxWP2VerificationPackageBytes {
		return ValidatedWP2VerificationPackage{}, validationError("evidence_too_large", "wp2_verification_package_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedWP2VerificationPackageFields); err != nil {
		return ValidatedWP2VerificationPackage{}, err
	}
	var submitted WP2VerificationPackageV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submitted); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedWP2VerificationPackage{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedWP2VerificationPackage{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedWP2VerificationPackage{}, err
	}
	derived, err := BuildWP2VerificationPackage(expected.Input)
	if err != nil {
		return ValidatedWP2VerificationPackage{}, err
	}
	canonical, err := json.Marshal(submitted)
	if err != nil {
		return ValidatedWP2VerificationPackage{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	if !bytes.Equal(canonical, derived.CanonicalJSON) {
		return ValidatedWP2VerificationPackage{}, validationError("identity_mismatch", "derived_wp2_verification_package", "/")
	}
	return derived, nil
}

func validateWP2VerificationInput(input WP2VerificationBuildInput) (WP2VerificationTraceabilityV1, error) {
	if err := requireSHA256(input.NormativeADRSHA256, "/normative_adr_sha256"); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if !gitCommitPattern.MatchString(input.Revision.Head) || !gitCommitPattern.MatchString(input.Revision.Tree) {
		return WP2VerificationTraceabilityV1{}, validationError("invalid_identity", "git_revision", "/revision")
	}
	if input.Revision.Modified {
		return WP2VerificationTraceabilityV1{}, validationError("identity_mismatch", "clean_source_required", "/revision/modified")
	}
	for path, binary := range map[string]WP2VerificationBinaryIdentityV1{"/sidecar": input.Sidecar, "/harness": input.Harness} {
		if err := validateWP2VerificationBinary(path, binary, input.Revision.Head); err != nil {
			return WP2VerificationTraceabilityV1{}, err
		}
	}
	if err := validateWP2VerificationFile("/effective_settings", input.EffectiveSettings); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if input.EffectiveSettings.Path != WP2ExactVerificationSettingsStableName || input.EffectiveSettings.Schema != WP2ExactVerificationSettingsSchemaV1 {
		return WP2VerificationTraceabilityV1{}, validationError("identity_mismatch", "exact_verification_settings_identity", "/effective_settings")
	}
	if err := validateWP2VerificationFile("/capability_contract", input.CapabilityContract); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if input.CapabilityContract.Schema != SchemaV1 {
		return WP2VerificationTraceabilityV1{}, validationError("invalid_schema", "capability_evidence_contract", "/capability_contract/schema")
	}
	if err := validateWP2AcceptedPackages(input.Packages); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if err := validateWP2NormativeIdentity(input); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if err := validateWP2WebChoices(input.WebChoiceMappings); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	verifiedIDs, accountDependent, err := validateWP2VerificationCatalog(input.Catalog)
	if err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if err := validateWP2HistoricalInventory(input.Historical); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if err := validateWP2ProvisionalInventory(input.Provisional); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if err := validateWP2TestInventory(input.VerificationInventory); err != nil {
		return WP2VerificationTraceabilityV1{}, err
	}
	if input.States != (WP2VerificationStatesV1{Implementation: "complete", Verification: "pass", Review: "pending_independent", Deployment: "not_authorized"}) {
		return WP2VerificationTraceabilityV1{}, validationError("invalid_state", "separate_wp2_states", "/states")
	}
	return WP2VerificationTraceabilityV1{
		AcceptedPackageCount:             len(input.Packages),
		WebChoiceMappingCount:            len(input.WebChoiceMappings),
		CatalogManifestSHA256:            input.Catalog.ChecksumSHA256,
		CatalogIdentityCount:             len(input.Catalog.Manifest.Identities),
		GlobalClaimCount:                 len(input.Catalog.Manifest.GlobalClaims),
		AccountDependentGlobalClaimCount: accountDependent,
		VerifiedCapabilityIDs:            verifiedIDs,
		HistoricalInventorySHA256:        input.Historical.ChecksumSHA256,
		HistoricalEntryCount:             input.Historical.Inventory.EntryCount,
		HistoricalClassificationCounts:   input.Historical.Inventory.ClassificationCounts,
		ProvisionalInventorySHA256:       input.Provisional.ChecksumSHA256,
		ProvisionalEntryCount:            input.Provisional.Inventory.EntryCount,
		ProvisionalDispositionCounts:     input.Provisional.Inventory.DispositionCounts,
	}, nil
}

func validateWP2VerificationBinary(path string, binary WP2VerificationBinaryIdentityV1, head string) error {
	if err := requireSHA256(binary.SHA256, path+"/sha256"); err != nil {
		return err
	}
	if binary.Bytes <= 0 || binary.VCSRevision != head || binary.VCSModified {
		return validationError("identity_mismatch", "clean_binary_revision", path)
	}
	return nil
}

func validateWP2VerificationFile(path string, file WP2VerificationFileIdentityV1) error {
	if file.Path == "" || file.Schema == "" || file.Bytes <= 0 {
		return validationError("missing_field", "file_identity", path)
	}
	return requireSHA256(file.SHA256, path+"/sha256")
}

func validateWP2AcceptedPackages(packages []WP2AcceptedPackageIdentityV1) error {
	if len(packages) != 8 {
		return validationError("identity_mismatch", "accepted_wp2_package_set", "/accepted_packages")
	}
	expectedKinds := []string{"web_choice_mapping", "route_protocol", "alias_projection", "legacy_configured", "account_pool", "catalog_projection", "historical_baseline", "provisional_claims"}
	expectedSchemas := []string{
		"m365-wp2-web-choice-evidence-set/v1",
		RouteProtocolEvidenceSetSchemaV1,
		AliasProjectionEvidenceSetSchemaV1,
		LegacyConfiguredEvidenceSetSchemaV1,
		AccountPoolEvidenceSetSchemaV1,
		CatalogEvidenceSetSchemaV1,
		"m365-wp2-historical-baseline-evidence-set/v1",
		ProvisionalClaimEvidenceSetSchemaV1,
	}
	for index, pkg := range packages {
		expectedIssue := index + 3
		expectedPath := "docs/wp2/evidence/issue-" + strconv.Itoa(expectedIssue) + "/evidence-index.json"
		if expectedIssue == 3 {
			expectedPath = "docs/wp2/evidence/issue-3/SHA256SUMS"
		}
		if pkg.Issue != expectedIssue || pkg.Kind != expectedKinds[index] || pkg.RepositoryPath != expectedPath || pkg.PayloadSchema != expectedSchemas[index] || pkg.RepositoryBytes <= 0 || pkg.PayloadBytes <= 0 {
			return validationError("identity_mismatch", "accepted_wp2_package_order", "/accepted_packages")
		}
		if err := requireSHA256(pkg.RepositorySHA256, "/accepted_packages/repository_sha256"); err != nil {
			return err
		}
		if err := requireSHA256(pkg.PayloadSHA256, "/accepted_packages/payload_sha256"); err != nil {
			return err
		}
		for path, value := range map[string]string{
			"source_head": pkg.SourceHead, "binary_sha256": pkg.BinarySHA256, "harness_sha256": pkg.HarnessSHA256,
			"effective_settings_sha256": pkg.SettingsSHA256, "profile_set_sha256": pkg.ProfileSetSHA256,
			"accepted_manifest_sha256": pkg.AcceptedManifestSHA256, "inventory_sha256": pkg.InventorySHA256,
		} {
			if value == "" {
				continue
			}
			if path == "source_head" {
				if !gitCommitPattern.MatchString(value) {
					return validationError("invalid_identity", "git_commit_sha", "/accepted_packages/"+path)
				}
				continue
			}
			if err := requireSHA256(value, "/accepted_packages/"+path); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWP2NormativeIdentity(input WP2VerificationBuildInput) error {
	for _, pkg := range input.Catalog.Manifest.Packages {
		if pkg.NormativeADRSHA256 != input.NormativeADRSHA256 {
			return validationError("identity_mismatch", "normative_adr_identity", "/catalog_manifest/packages")
		}
	}
	if input.Historical.Inventory.NormativeADRSHA256 != input.NormativeADRSHA256 || input.Provisional.Inventory.NormativeADRSHA256 != input.NormativeADRSHA256 {
		return validationError("identity_mismatch", "normative_adr_identity", "/")
	}
	return nil
}

func validateWP2WebChoices(mappings []WP2VerifiedWebChoiceV1) error {
	expected := []string{"m365-auto", "m365-gpt-5.5-quick-response", "m365-gpt-5.6-think-deeper", "quick", "think-deeper"}
	if len(mappings) != len(expected) {
		return validationError("identity_mismatch", "five_web_choices", "/web_choice_mappings")
	}
	for index, mapping := range mappings {
		if mapping.WebChoiceID != expected[index] || mapping.ObservedTone == "" {
			return validationError("invalid_order", "web_choice_mapping_order", "/web_choice_mappings")
		}
		switch mapping.MappingBehavior {
		case string(MappingBehaviorExact), string(MappingBehaviorCaseNormalized), string(MappingBehaviorDifferent):
		default:
			return validationError("invalid_enum", "mapping_behavior", "/web_choice_mappings/mapping_behavior")
		}
		if err := requireSHA256(mapping.EvidenceSHA256, "/web_choice_mappings/evidence_sha256"); err != nil {
			return err
		}
	}
	return nil
}

func validateWP2VerificationCatalog(catalog ValidatedCatalogProjection) ([]string, int, error) {
	for _, claim := range catalog.Manifest.GlobalClaims {
		for _, capability := range claim.Capabilities {
			if !isWP2VerifiableCapabilityForTest(capability.CapabilityID) {
				return nil, 0, validationError("verification_scope_forbidden", "wp2_verified_capability", "/catalog_manifest/global_claims/capabilities")
			}
		}
	}
	if err := validateCatalogProjectionManifest(catalog.Manifest); err != nil {
		return nil, 0, err
	}
	verified := map[string]struct{}{}
	accountDependent := 0
	for _, claim := range catalog.Manifest.GlobalClaims {
		if claim.AccountDependent {
			accountDependent++
		}
		for _, capability := range claim.Capabilities {
			if capability.Classification == ClassificationVerified {
				verified[capability.CapabilityID] = struct{}{}
			}
		}
	}
	canonical, err := json.Marshal(catalog.Manifest)
	if err != nil {
		return nil, 0, validationError("canonicalization_failed", "catalog_manifest", "/catalog_manifest")
	}
	digest := sha256.Sum256(canonical)
	if catalog.ChecksumSHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(canonical, catalog.CanonicalJSON) {
		return nil, 0, validationError("identity_mismatch", "catalog_manifest", "/catalog_manifest")
	}
	ids := make([]string, 0, len(verified))
	for id := range verified {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !equalStringSlices(ids, sortedWP2VerifiableCapabilityIDs()) {
		return nil, 0, validationError("identity_mismatch", "complete_wp2_verified_capability_set", "/catalog_manifest/global_claims")
	}
	if len(catalog.Manifest.Identities) != 20 || len(catalog.Manifest.GlobalClaims) != 12 || accountDependent != 1 {
		return nil, 0, validationError("identity_mismatch", "accepted_wp2_catalog_shape", "/catalog_manifest")
	}
	return ids, accountDependent, nil
}

func validateWP2HistoricalInventory(inventory ValidatedHistoricalBaselineInventory) error {
	canonical, err := json.Marshal(inventory.Inventory)
	if err != nil {
		return validationError("canonicalization_failed", "historical_inventory", "/historical_inventory")
	}
	digest := sha256.Sum256(canonical)
	expectedCounts := HistoricalClassificationCountsV1{Verified: 2, ConfirmedDefect: 8, Unsupported: 2, Inconclusive: 36}
	if inventory.ChecksumSHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(canonical, inventory.CanonicalJSON) || inventory.Inventory.EntryCount != 48 || len(inventory.Inventory.Entries) != 48 || inventory.Inventory.ClassificationCounts != expectedCounts {
		return validationError("identity_mismatch", "historical_48_entry_inventory", "/historical_inventory")
	}
	return nil
}

func validateWP2ProvisionalInventory(inventory ValidatedProvisionalClaimInventory) error {
	canonical, err := json.Marshal(inventory.Inventory)
	if err != nil {
		return validationError("canonicalization_failed", "provisional_inventory", "/provisional_inventory")
	}
	digest := sha256.Sum256(canonical)
	if inventory.ChecksumSHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(canonical, inventory.CanonicalJSON) || inventory.Inventory.EntryCount != len(inventory.Inventory.Entries) {
		return validationError("identity_mismatch", "provisional_claim_inventory", "/provisional_inventory")
	}
	expectedIDs := sortedWP2VerifiableCapabilityIDs()
	if !equalStringSlices(inventory.Inventory.EvidenceBackedCapabilityIDs, expectedIDs) {
		return validationError("verification_scope_forbidden", "provisional_evidence_backed_capability_set", "/provisional_inventory/evidence_backed_capability_ids")
	}
	counts := ProvisionalClaimDispositionCountsV1{}
	verified := make([]string, 0, len(expectedIDs))
	previous := ""
	for _, entry := range inventory.Inventory.Entries {
		if entry.ClaimID == "" || (previous != "" && previous >= entry.ClaimID) {
			return validationError("invalid_order", "provisional_claim_entries", "/provisional_inventory/entries")
		}
		previous = entry.ClaimID
		switch entry.Disposition {
		case ProvisionalClaimEvidenceBacked:
			counts.EvidenceBacked++
			if entry.AcceptedCapabilityID != entry.ClaimID || len(entry.AcceptedSupportSHA256) == 0 {
				return validationError("identity_mismatch", "evidence_backed_claim_support", "/provisional_inventory/entries")
			}
			if !isWP2VerifiableCapabilityForTest(entry.AcceptedCapabilityID) {
				return validationError("verification_scope_forbidden", "wp2_verified_capability", "/provisional_inventory/entries")
			}
			verified = append(verified, entry.AcceptedCapabilityID)
		case ProvisionalClaimImplementedUnaccepted:
			counts.ImplementedUnaccepted++
		case ProvisionalClaimUnverified:
			counts.Unverified++
		case ProvisionalClaimUnsupported:
			counts.Unsupported++
		default:
			return validationError("invalid_enum", "provisional_claim_disposition", "/provisional_inventory/entries")
		}
		if entry.Disposition != ProvisionalClaimEvidenceBacked && (entry.AcceptedCapabilityID != "" || len(entry.AcceptedSupportSHA256) != 0) {
			return validationError("verification_scope_forbidden", "advanced_claim_support", "/provisional_inventory/entries")
		}
	}
	expectedCounts := ProvisionalClaimDispositionCountsV1{EvidenceBacked: 4, ImplementedUnaccepted: 11, Unverified: 19, Unsupported: 2}
	if counts != inventory.Inventory.DispositionCounts || counts != expectedCounts || inventory.Inventory.EntryCount != 36 || !equalStringSlices(verified, expectedIDs) {
		return validationError("identity_mismatch", "provisional_claim_disposition_counts", "/provisional_inventory")
	}
	return nil
}

func isWP2VerifiableCapabilityForTest(capabilityID string) bool {
	for _, allowed := range WP2VerifiableCapabilityIDs() {
		if capabilityID == allowed {
			return true
		}
	}
	return false
}

func validateWP2TestInventory(inventory WP2VerificationInventoryV1) error {
	if inventory.Schema != WP2VerificationInventorySchemaV1 || len(inventory.InScope) != len(wp2VerificationInScopeTestIDs) || len(inventory.Deferred) != len(wp2VerificationDeferredTestIDs) {
		return validationError("invalid_schema", "wp2_verification_inventory", "/verification_inventory")
	}
	for index, test := range inventory.InScope {
		if test.ID != wp2VerificationInScopeTestIDs[index] || test.Status != "PASS" || !test.AcceptanceCounted {
			return validationError("identity_mismatch", "closed_in_scope_test_inventory", "/verification_inventory/in_scope")
		}
	}
	for index, test := range inventory.Deferred {
		if test.AcceptanceCounted {
			return validationError("verification_scope_forbidden", "deferred_test_not_counted", "/verification_inventory/deferred")
		}
		if test.ID != wp2VerificationDeferredTestIDs[index] || test.Status != "DEFERRED" {
			return validationError("identity_mismatch", "closed_deferred_test_inventory", "/verification_inventory/deferred")
		}
	}
	return nil
}

func cloneWP2VerificationInventory(inventory WP2VerificationInventoryV1) WP2VerificationInventoryV1 {
	return WP2VerificationInventoryV1{
		Schema:   inventory.Schema,
		InScope:  append([]WP2VerificationTestV1(nil), inventory.InScope...),
		Deferred: append([]WP2VerificationTestV1(nil), inventory.Deferred...),
	}
}

func sortedWP2VerifiableCapabilityIDs() []string {
	ids := WP2VerifiableCapabilityIDs()
	sort.Strings(ids)
	return ids
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
