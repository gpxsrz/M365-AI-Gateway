package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

const (
	CatalogProjectionManifestSchemaV1 = "m365-wp2-catalog-projection-manifest/v1"
	CatalogProjectionAccepted         = "accepted"
	MaxCatalogProjectionManifestBytes = 512 * 1024
)

type CatalogProjectionPackageV1 struct {
	Issue                   int    `json:"issue"`
	Kind                    string `json:"kind"`
	NormativeADRSHA256      string `json:"normative_adr_sha256"`
	SourceHead              string `json:"source_head"`
	BinarySHA256            string `json:"binary_sha256"`
	HarnessSHA256           string `json:"harness_sha256"`
	EffectiveSettingsSHA256 string `json:"effective_settings_sha256"`
	EvidenceIndexSHA256     string `json:"evidence_index_sha256"`
	PayloadJSONSHA256       string `json:"payload_json_sha256"`
	ProfileSetSHA256        string `json:"profile_set_sha256,omitempty"`
}

type CatalogProjectionIdentityEvidenceV1 struct {
	RequestedIdentity           string   `json:"requested_identity"`
	CanonicalRoute              string   `json:"canonical_route"`
	ResolvedTone                string   `json:"resolved_tone"`
	RouteKind                   string   `json:"route_kind"`
	CatalogVisibility           string   `json:"catalog_visibility"`
	CompatibilityRequired       bool     `json:"compatibility_required"`
	MappingEvidence             string   `json:"mapping_evidence"`
	IdentityStatus              string   `json:"identity_status"`
	PackageIssue                int      `json:"package_issue"`
	CatalogObservationSHA256    string   `json:"catalog_observation_sha256,omitempty"`
	SupportingEvidenceSHA256    []string `json:"supporting_evidence_sha256"`
	CapabilityEvidenceSetSHA256 string   `json:"capability_evidence_set_sha256"`
}

type CatalogProjectionManifestV1 struct {
	Schema           string                                `json:"schema"`
	AcceptanceStatus string                                `json:"acceptance_status"`
	Packages         []CatalogProjectionPackageV1          `json:"packages"`
	Identities       []CatalogProjectionIdentityEvidenceV1 `json:"identities"`
	GlobalClaims     []AccountPoolGlobalClaimV1            `json:"global_claims"`
}

type CatalogProjectionExpected struct {
	ManifestSHA256 string
	Packages       []CatalogProjectionPackageV1
}

type ValidatedCatalogProjection struct {
	Manifest       CatalogProjectionManifestV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

var allowedCatalogProjectionFields = map[string]struct{}{
	"schema":            {},
	"acceptance_status": {},
	"packages":          {},
	"identities":        {},
	"global_claims":     {},
}

var catalogProjectionPackageKinds = map[int]string{
	4: "route_protocol",
	5: "alias_projection",
	6: "legacy_configured",
	7: "account_pool",
}

var catalogProjectionRouteKinds = map[string]struct{}{
	"web_mode":           {},
	"web_model_route":    {},
	"alias":              {},
	"preset":             {},
	"configured_mapping": {},
	"legacy_direct":      {},
}

var catalogProjectionVisibilities = map[string]struct{}{
	"public":        {},
	"compatibility": {},
	"hidden":        {},
}

func CatalogProjectionEvidenceSetSHA256(hashes []string) (string, error) {
	if len(hashes) == 0 {
		return "", validationError("missing_field", "supporting_evidence_sha256", "/identities/supporting_evidence_sha256")
	}
	if !sort.StringsAreSorted(hashes) {
		return "", validationError("invalid_order", "supporting_evidence_sha256", "/identities/supporting_evidence_sha256")
	}
	previous := ""
	for _, checksum := range hashes {
		if err := requireSHA256(checksum, "/identities/supporting_evidence_sha256"); err != nil {
			return "", err
		}
		if checksum == previous {
			return "", validationError("duplicate_identity", "supporting_evidence_sha256", "/identities/supporting_evidence_sha256")
		}
		previous = checksum
	}
	canonical, err := json.Marshal(hashes)
	if err != nil {
		return "", validationError("canonicalization_failed", "supporting_evidence_sha256", "/identities/supporting_evidence_sha256")
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateCatalogProjectionManifest(raw []byte, expected CatalogProjectionExpected) (ValidatedCatalogProjection, error) {
	if len(raw) == 0 {
		return ValidatedCatalogProjection{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxCatalogProjectionManifestBytes {
		return ValidatedCatalogProjection{}, validationError("evidence_too_large", "catalog_projection_manifest_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedCatalogProjectionFields); err != nil {
		return ValidatedCatalogProjection{}, err
	}

	var manifest CatalogProjectionManifestV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedCatalogProjection{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedCatalogProjection{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedCatalogProjection{}, err
	}
	if err := validateCatalogProjectionManifest(manifest); err != nil {
		return ValidatedCatalogProjection{}, err
	}
	if err := validateCatalogProjectionExpected(expected); err != nil {
		return ValidatedCatalogProjection{}, err
	}
	if len(manifest.Packages) != len(expected.Packages) {
		return ValidatedCatalogProjection{}, validationError("identity_mismatch", "accepted_package_set", "/packages")
	}
	for index := range manifest.Packages {
		if manifest.Packages[index] != expected.Packages[index] {
			return ValidatedCatalogProjection{}, validationError("identity_mismatch", "accepted_package_identity", "/packages")
		}
	}

	canonical, err := json.Marshal(manifest)
	if err != nil {
		return ValidatedCatalogProjection{}, validationError("canonicalization_failed", "catalog_projection_manifest", "/")
	}
	digest := sha256.Sum256(canonical)
	checksum := hex.EncodeToString(digest[:])
	if checksum != expected.ManifestSHA256 {
		return ValidatedCatalogProjection{}, validationError("identity_mismatch", "accepted_catalog_manifest_sha256", "/")
	}
	return ValidatedCatalogProjection{
		Manifest:       manifest,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: checksum,
	}, nil
}

func validateCatalogProjectionManifest(manifest CatalogProjectionManifestV1) error {
	if manifest.Schema != CatalogProjectionManifestSchemaV1 {
		return validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if manifest.AcceptanceStatus != CatalogProjectionAccepted {
		return validationError("invalid_enum", "accepted_catalog_manifest", "/acceptance_status")
	}
	if len(manifest.Packages) != len(catalogProjectionPackageKinds) {
		return validationError("missing_field", "accepted_package_set", "/packages")
	}
	for index, pkg := range manifest.Packages {
		expectedIssue := index + 4
		if pkg.Issue != expectedIssue || pkg.Kind != catalogProjectionPackageKinds[expectedIssue] {
			return validationError("identity_mismatch", "accepted_package_order", "/packages")
		}
		if err := validateCatalogProjectionPackage(pkg); err != nil {
			return err
		}
	}
	if len(manifest.Identities) == 0 {
		return validationError("missing_field", "catalog_identity_evidence", "/identities")
	}
	seenIdentities := make(map[string]struct{}, len(manifest.Identities))
	previousIdentity := ""
	for _, identity := range manifest.Identities {
		if err := validateCatalogProjectionIdentity(identity); err != nil {
			return err
		}
		key := strings.ToLower(identity.RequestedIdentity)
		if _, exists := seenIdentities[key]; exists {
			return validationError("duplicate_identity", "requested_identity", "/identities")
		}
		if previousIdentity != "" && previousIdentity >= key {
			return validationError("invalid_order", "requested_identity", "/identities")
		}
		seenIdentities[key] = struct{}{}
		previousIdentity = key
	}
	if len(manifest.GlobalClaims) == 0 {
		return validationError("missing_field", "account_pool_global_claims", "/global_claims")
	}
	seenClaims := make(map[string]struct{}, len(manifest.GlobalClaims))
	previousClaim := ""
	for _, claim := range manifest.GlobalClaims {
		if err := validateCatalogProjectionGlobalClaim(claim); err != nil {
			return err
		}
		key := accountPoolEntryKey(claim.CanonicalRoute, claim.Protocol)
		if _, exists := seenClaims[key]; exists {
			return validationError("duplicate_identity", "route_protocol_global_claim", "/global_claims")
		}
		if previousClaim != "" && previousClaim >= key {
			return validationError("invalid_order", "route_protocol_global_claim", "/global_claims")
		}
		seenClaims[key] = struct{}{}
		previousClaim = key
	}
	return nil
}

func validateCatalogProjectionPackage(pkg CatalogProjectionPackageV1) error {
	if expectedKind, ok := catalogProjectionPackageKinds[pkg.Issue]; !ok || pkg.Kind != expectedKind {
		return validationError("invalid_enum", "accepted_package_kind", "/packages/kind")
	}
	if err := requireSHA256(pkg.NormativeADRSHA256, "/packages/normative_adr_sha256"); err != nil {
		return err
	}
	if !gitCommitPattern.MatchString(pkg.SourceHead) {
		return validationError("invalid_identity", "git_commit_sha", "/packages/source_head")
	}
	for path, value := range map[string]string{
		"/packages/binary_sha256":             pkg.BinarySHA256,
		"/packages/harness_sha256":            pkg.HarnessSHA256,
		"/packages/effective_settings_sha256": pkg.EffectiveSettingsSHA256,
		"/packages/evidence_index_sha256":     pkg.EvidenceIndexSHA256,
		"/packages/payload_json_sha256":       pkg.PayloadJSONSHA256,
	} {
		if err := requireSHA256(value, path); err != nil {
			return err
		}
	}
	if pkg.Issue == 7 {
		if err := requireSHA256(pkg.ProfileSetSHA256, "/packages/profile_set_sha256"); err != nil {
			return err
		}
	} else if pkg.ProfileSetSHA256 != "" {
		return validationError("invalid_identity", "profile_set_only_account_pool", "/packages/profile_set_sha256")
	}
	return nil
}

func validateCatalogProjectionIdentity(identity CatalogProjectionIdentityEvidenceV1) error {
	if !routePattern.MatchString(identity.RequestedIdentity) {
		return validationError("invalid_route", "requested_identity", "/identities/requested_identity")
	}
	if !routePattern.MatchString(identity.CanonicalRoute) {
		return validationError("invalid_route", "canonical_route", "/identities/canonical_route")
	}
	if !tonePattern.MatchString(identity.ResolvedTone) {
		return validationError("invalid_tone", "resolved_tone", "/identities/resolved_tone")
	}
	if _, ok := catalogProjectionRouteKinds[identity.RouteKind]; !ok {
		return validationError("invalid_enum", "route_kind", "/identities/route_kind")
	}
	if _, ok := catalogProjectionVisibilities[identity.CatalogVisibility]; !ok {
		return validationError("invalid_enum", "catalog_visibility", "/identities/catalog_visibility")
	}
	if _, ok := allowedMappingEvidence[identity.MappingEvidence]; !ok {
		return validationError("invalid_enum", "mapping_evidence", "/identities/mapping_evidence")
	}
	if _, ok := allowedIdentityStatus[identity.IdentityStatus]; !ok {
		return validationError("invalid_enum", "identity_status", "/identities/identity_status")
	}
	if identity.PackageIssue < 4 || identity.PackageIssue > 6 {
		return validationError("invalid_enum", "identity_evidence_package", "/identities/package_issue")
	}
	if identity.PackageIssue == 4 {
		if identity.CatalogObservationSHA256 != "" {
			return validationError("invalid_identity", "route_protocol_has_no_catalog_observation", "/identities/catalog_observation_sha256")
		}
	} else if err := requireSHA256(identity.CatalogObservationSHA256, "/identities/catalog_observation_sha256"); err != nil {
		return err
	}
	calculated, err := CatalogProjectionEvidenceSetSHA256(identity.SupportingEvidenceSHA256)
	if err != nil {
		return err
	}
	if identity.CapabilityEvidenceSetSHA256 != calculated {
		return validationError("identity_mismatch", "capability_evidence_set_sha256", "/identities/capability_evidence_set_sha256")
	}
	return nil
}

func validateCatalogProjectionGlobalClaim(claim AccountPoolGlobalClaimV1) error {
	if !routePattern.MatchString(claim.CanonicalRoute) {
		return validationError("invalid_route", "canonical_route", "/global_claims/canonical_route")
	}
	if !tonePattern.MatchString(claim.ResolvedTone) {
		return validationError("invalid_tone", "resolved_tone", "/global_claims/resolved_tone")
	}
	if _, ok := allowedProtocols[claim.Protocol]; !ok {
		return validationError("invalid_enum", "protocol", "/global_claims/protocol")
	}
	if claim.EligibleProfileCount < 1 || claim.UnavailableProfileCount < 0 {
		return validationError("invalid_observation", "profile_counts", "/global_claims/eligible_profile_count")
	}
	if _, ok := allowedClassifications[claim.RouteEligibility]; !ok {
		return validationError("invalid_enum", "route_eligibility", "/global_claims/route_eligibility")
	}
	if len(claim.Capabilities) != len(accountPoolCapabilityOrder) {
		return validationError("missing_field", "wp2_capability_set", "/global_claims/capabilities")
	}
	for index, capability := range claim.Capabilities {
		if capability.CapabilityID != accountPoolCapabilityOrder[index] {
			return validationError("invalid_order", "wp2_capability_set", "/global_claims/capabilities")
		}
		if _, allowed := wp2VerifiableCapabilities[capability.CapabilityID]; !allowed {
			return validationError("verification_scope_forbidden", "wp2_verified_capability", "/global_claims/capabilities/capability_id")
		}
		if _, ok := allowedClassifications[capability.Classification]; !ok {
			return validationError("invalid_enum", "classification", "/global_claims/capabilities/classification")
		}
		if capability.Classification == ClassificationVerified && len(capability.SupportingEvidenceSHA256) == 0 {
			return validationError("missing_field", "verified_supporting_evidence", "/global_claims/capabilities/supporting_evidence_sha256")
		}
		if !sort.StringsAreSorted(capability.SupportingEvidenceSHA256) {
			return validationError("invalid_order", "supporting_evidence_sha256", "/global_claims/capabilities/supporting_evidence_sha256")
		}
		previous := ""
		for _, checksum := range capability.SupportingEvidenceSHA256 {
			if err := requireSHA256(checksum, "/global_claims/capabilities/supporting_evidence_sha256"); err != nil {
				return err
			}
			if checksum == previous {
				return validationError("duplicate_identity", "supporting_evidence_sha256", "/global_claims/capabilities/supporting_evidence_sha256")
			}
			previous = checksum
		}
		if claim.AccountDependent && capability.Classification == ClassificationVerified {
			return validationError("classification_status_conflict", "account_dependent_not_global_verified", "/global_claims/capabilities/classification")
		}
	}
	if claim.AccountDependent && claim.RouteEligibility == ClassificationVerified {
		return validationError("classification_status_conflict", "account_dependent_route_not_verified", "/global_claims/route_eligibility")
	}
	return nil
}

func validateCatalogProjectionExpected(expected CatalogProjectionExpected) error {
	if !sha256Pattern.MatchString(expected.ManifestSHA256) {
		return validationError("invalid_expected_identity", "catalog_manifest_sha256", "/")
	}
	if len(expected.Packages) != len(catalogProjectionPackageKinds) {
		return validationError("invalid_expected_identity", "accepted_package_set", "/packages")
	}
	for index, pkg := range expected.Packages {
		if pkg.Issue != index+4 {
			return validationError("invalid_expected_identity", "accepted_package_order", "/packages")
		}
		if err := validateCatalogProjectionPackage(pkg); err != nil {
			return validationError("invalid_expected_identity", "accepted_package_identity", "/packages")
		}
	}
	return nil
}

func (validated ValidatedCatalogProjection) IdentityEvidence(requestedIdentity string) (CatalogProjectionIdentityEvidenceV1, bool) {
	for _, identity := range validated.Manifest.Identities {
		if strings.EqualFold(identity.RequestedIdentity, requestedIdentity) {
			return identity, true
		}
	}
	return CatalogProjectionIdentityEvidenceV1{}, false
}

func (validated ValidatedCatalogProjection) GlobalClaims(canonicalRoute, resolvedTone string) []AccountPoolGlobalClaimV1 {
	claims := make([]AccountPoolGlobalClaimV1, 0)
	for _, claim := range validated.Manifest.GlobalClaims {
		if claim.CanonicalRoute == canonicalRoute && claim.ResolvedTone == resolvedTone {
			claims = append(claims, claim)
		}
	}
	return claims
}
