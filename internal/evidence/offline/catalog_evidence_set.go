package offline

import core "m365-native/internal/evidence"

type CatalogEvidenceCase = core.CatalogEvidenceCase
type CatalogEvidenceSetV1 = core.CatalogEvidenceSetV1
type CatalogHTTPObservationV1 = core.CatalogHTTPObservationV1
type CatalogModelObservationV1 = core.CatalogModelObservationV1
type CatalogProtocolObservationV1 = core.CatalogProtocolObservationV1
type CatalogCapabilityObservationV1 = core.CatalogCapabilityObservationV1
type CatalogStaleManifestRejection = core.CatalogStaleManifestRejection

const (
	CatalogEvidenceSetSchemaV1             = core.CatalogEvidenceSetSchemaV1
	CatalogEvidenceCaseNoManifest          = core.CatalogEvidenceCaseNoManifest
	CatalogEvidenceCaseAccepted            = core.CatalogEvidenceCaseAccepted
	CatalogEvidenceCaseRuntimeMappingDrift = core.CatalogEvidenceCaseRuntimeMappingDrift
)

func ValidateCatalogEvidenceBinding(binding CaptureBinding) error {
	for path, value := range map[string]string{
		"/normative_adr_sha256":      binding.NormativeADRSHA256,
		"/binary_sha256":             binding.BinarySHA256,
		"/harness_sha256":            binding.HarnessSHA256,
		"/effective_settings_sha256": binding.EffectiveSettingsSHA256,
	} {
		if !sha256Pattern.MatchString(value) {
			return validationError("invalid_identity", "sha256", path)
		}
	}
	if !gitCommitPattern.MatchString(binding.SourceHead) {
		return validationError("invalid_identity", "git_commit_sha", "/source_head")
	}
	if binding.DirtyContentSHA256 != "" && !sha256Pattern.MatchString(binding.DirtyContentSHA256) {
		return validationError("invalid_identity", "dirty_content_sha256", "/dirty_content_sha256")
	}
	if binding.AccountProfileRef != "" {
		return validationError("privacy_forbidden", "catalog_evidence_has_no_account_profile", "/account_profile_ref")
	}
	return nil
}
