package evidence

import "encoding/json"

const CatalogEvidenceSetSchemaV1 = "m365-wp2-catalog-evidence-set/v1"

type CatalogEvidenceCase string

const (
	CatalogEvidenceCaseNoManifest          CatalogEvidenceCase = "no_accepted_manifest"
	CatalogEvidenceCaseAccepted            CatalogEvidenceCase = "accepted_manifest"
	CatalogEvidenceCaseRuntimeMappingDrift CatalogEvidenceCase = "runtime_mapping_drift"
)

type CatalogEvidenceSetV1 struct {
	Schema                                  string                          `json:"schema"`
	NormativeADRSHA256                      string                          `json:"normative_adr_sha256"`
	SourceHead                              string                          `json:"source_head"`
	DirtyContentSHA256                      string                          `json:"dirty_content_sha256,omitempty"`
	BinarySHA256                            string                          `json:"binary_sha256"`
	HarnessSHA256                           string                          `json:"harness_sha256"`
	EffectiveSettingsSHA256                 string                          `json:"effective_settings_sha256"`
	AcceptedManifestSHA256                  string                          `json:"accepted_manifest_sha256"`
	AcceptedManifestBytes                   int                             `json:"accepted_manifest_bytes"`
	AcceptedManifest                        json.RawMessage                 `json:"accepted_manifest"`
	AcceptedIdentityCount                   int                             `json:"accepted_identity_count"`
	AcceptedIdentitySupportingEvidenceCount int                             `json:"accepted_identity_supporting_evidence_count"`
	GlobalClaimCount                        int                             `json:"global_claim_count"`
	AccountDependentGlobalClaimCount        int                             `json:"account_dependent_global_claim_count"`
	ProfileSetSHA256                        string                          `json:"profile_set_sha256"`
	HTTPObservations                        []CatalogHTTPObservationV1      `json:"http_observations"`
	StaleManifestRejections                 []CatalogStaleManifestRejection `json:"stale_manifest_rejections"`
}

type CatalogHTTPObservationV1 struct {
	CaseID                        CatalogEvidenceCase         `json:"case_id"`
	HTTPStatus                    int                         `json:"http_status"`
	CatalogEntryCount             int                         `json:"catalog_entry_count"`
	DataModelsAliasesEqual        bool                        `json:"data_models_aliases_equal"`
	StandardCatalogSHA256         string                      `json:"standard_catalog_sha256"`
	NormalizedCatalogSHA256       string                      `json:"normalized_catalog_sha256"`
	AcceptedEvidenceModelCount    int                         `json:"accepted_evidence_model_count"`
	AccountDependentModelCount    int                         `json:"account_dependent_model_count"`
	ProtocolClaimCount            int                         `json:"protocol_claim_count"`
	VerifiedProtocolClaimCount    int                         `json:"verified_protocol_claim_count"`
	AccountDependentProtocolCount int                         `json:"account_dependent_protocol_count"`
	ClaimCapabilityIDs            []string                    `json:"claim_capability_ids"`
	AdvancedCapabilitiesPromoted  []string                    `json:"advanced_capabilities_promoted"`
	VisiblePresets                []string                    `json:"visible_presets"`
	HiddenIdentityLeaks           []string                    `json:"hidden_identity_leaks"`
	SelectedModels                []CatalogModelObservationV1 `json:"selected_models"`
}

type CatalogModelObservationV1 struct {
	ID                string                         `json:"id"`
	Present           bool                           `json:"present"`
	CanonicalRoute    string                         `json:"canonical_route,omitempty"`
	ResolvedTone      string                         `json:"resolved_tone,omitempty"`
	RouteKind         string                         `json:"route_kind,omitempty"`
	CatalogVisibility string                         `json:"catalog_visibility,omitempty"`
	EvidenceSource    string                         `json:"evidence_source,omitempty"`
	MappingSource     string                         `json:"mapping_source,omitempty"`
	ProtocolSource    string                         `json:"protocol_source,omitempty"`
	AccountDependent  bool                           `json:"account_dependent"`
	Protocols         []CatalogProtocolObservationV1 `json:"protocols"`
}

type CatalogProtocolObservationV1 struct {
	Protocol         string                           `json:"protocol"`
	RouteEligibility Classification                   `json:"route_eligibility"`
	AccountDependent bool                             `json:"account_dependent"`
	Capabilities     []CatalogCapabilityObservationV1 `json:"capabilities"`
}

type CatalogCapabilityObservationV1 struct {
	CapabilityID   string         `json:"capability_id"`
	Classification Classification `json:"classification"`
}

type CatalogStaleManifestRejection struct {
	Identity  string `json:"identity"`
	Rejected  bool   `json:"rejected"`
	ErrorCode string `json:"error_code"`
	ErrorRule string `json:"error_rule"`
	ErrorPath string `json:"error_path,omitempty"`
}
