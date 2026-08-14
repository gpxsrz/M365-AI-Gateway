package evidence

import (
	"encoding/json"
)

const (
	AliasProjectionCaptureSchemaV1     = "m365-wp2-alias-projection-capture/v1"
	AliasProjectionObservationSchemaV1 = "m365-wp2-alias-projection-observation/v1"
	AliasProjectionEvidenceSetSchemaV1 = "m365-wp2-alias-projection-evidence-set/v1"
	MaxAliasProjectionCaptureBytes     = 12 * 1024
)

type AliasProjectionCase string

const (
	AliasProjectionCaseCatalog       AliasProjectionCase = "catalog_projection"
	AliasProjectionCaseSuccess       AliasProjectionCase = "success"
	AliasProjectionCaseUnknownRoute  AliasProjectionCase = "unknown_route"
	AliasProjectionCaseDisabledRoute AliasProjectionCase = "disabled_route"
)

type AliasProjectionDescriptor struct {
	RequestedIdentity       string
	CanonicalRoute          string
	ResolvedTone            string
	RouteKind               string
	OperationalStatus       string
	RuntimeMappingEvidence  string
	AcceptedMappingEvidence string
	IdentityStatus          string
	CatalogVisibility       string
	CompatibilityRequired   bool
	DefaultReasoningLevel   string
	Protocol                string
	EndpointPath            string
	AuthMode                string
	Effort                  string
	ReasoningEffortApplied  bool
	ReasoningEffortIgnored  bool
	ListedInCatalog         bool
}

type AliasProjectionCaptureV1 struct {
	Schema                  string              `json:"schema"`
	CaseID                  AliasProjectionCase `json:"case_id"`
	RequestedIdentity       string              `json:"requested_identity"`
	TopLevelModel           string              `json:"top_level_model,omitempty"`
	MetadataRequestedModel  string              `json:"metadata_requested_model,omitempty"`
	MetadataResponseModel   string              `json:"metadata_response_model,omitempty"`
	RouteMetadataComplete   bool                `json:"route_metadata_complete"`
	FallbackUsed            bool                `json:"fallback_used"`
	ConfiguredMapping       bool                `json:"configured_mapping"`
	CanonicalRoute          string              `json:"canonical_route,omitempty"`
	ResolvedTone            string              `json:"resolved_tone,omitempty"`
	RouteKind               string              `json:"route_kind,omitempty"`
	OperationalStatus       string              `json:"operational_status,omitempty"`
	MappingEvidence         string              `json:"mapping_evidence,omitempty"`
	IdentityStatus          string              `json:"identity_status,omitempty"`
	CatalogVisibility       string              `json:"catalog_visibility,omitempty"`
	AliasUsed               bool                `json:"alias_used"`
	CompatibilityRequired   bool                `json:"compatibility_required"`
	DefaultReasoningLevel   string              `json:"default_reasoning_level,omitempty"`
	Deprecated              bool                `json:"deprecated"`
	RemovalDateAbsent       bool                `json:"removal_date_absent"`
	ListedInCatalog         bool                `json:"listed_in_catalog"`
	PerKeyRestricted        bool                `json:"per_key_restricted"`
	Protocol                string              `json:"protocol"`
	EndpointPath            string              `json:"endpoint_path"`
	AuthMode                string              `json:"auth_mode"`
	Effort                  string              `json:"effort"`
	ReasoningEffortApplied  bool                `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool                `json:"reasoning_effort_ignored"`
	HTTPStatus              int                 `json:"http_status"`
	BasicTextDelivered      bool                `json:"basic_text_delivered"`
	UpstreamAttempts        int                 `json:"upstream_attempts"`
	RouteSwitches           int                 `json:"route_switches"`
	CrossAccountResends     int                 `json:"cross_account_resends"`
	RequestIDObserved       bool                `json:"request_id_observed"`
	SecurityHeadersObserved bool                `json:"security_headers_observed"`
	FailureCode             string              `json:"failure_code,omitempty"`
}

type AliasProjectionObservationV1 struct {
	Schema                  string              `json:"schema"`
	ObservationID           string              `json:"observation_id"`
	CaseID                  AliasProjectionCase `json:"case_id"`
	RequestedIdentity       string              `json:"requested_identity"`
	TopLevelModel           string              `json:"top_level_model,omitempty"`
	MetadataRequestedModel  string              `json:"metadata_requested_model,omitempty"`
	MetadataResponseModel   string              `json:"metadata_response_model,omitempty"`
	RouteMetadataComplete   bool                `json:"route_metadata_complete"`
	FallbackUsed            bool                `json:"fallback_used"`
	ConfiguredMapping       bool                `json:"configured_mapping"`
	CanonicalRoute          string              `json:"canonical_route,omitempty"`
	ResolvedTone            string              `json:"resolved_tone,omitempty"`
	RouteKind               string              `json:"route_kind,omitempty"`
	OperationalStatus       string              `json:"operational_status,omitempty"`
	MappingEvidence         string              `json:"mapping_evidence,omitempty"`
	IdentityStatus          string              `json:"identity_status,omitempty"`
	CatalogVisibility       string              `json:"catalog_visibility,omitempty"`
	AliasUsed               bool                `json:"alias_used"`
	CompatibilityRequired   bool                `json:"compatibility_required"`
	DefaultReasoningLevel   string              `json:"default_reasoning_level,omitempty"`
	Deprecated              bool                `json:"deprecated"`
	RemovalDateAbsent       bool                `json:"removal_date_absent"`
	ListedInCatalog         bool                `json:"listed_in_catalog"`
	PerKeyRestricted        bool                `json:"per_key_restricted"`
	Protocol                string              `json:"protocol"`
	EndpointPath            string              `json:"endpoint_path"`
	AuthMode                string              `json:"auth_mode"`
	Effort                  string              `json:"effort"`
	ReasoningEffortApplied  bool                `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool                `json:"reasoning_effort_ignored"`
	HTTPStatus              int                 `json:"http_status"`
	BasicTextDelivered      bool                `json:"basic_text_delivered"`
	UpstreamAttempts        int                 `json:"upstream_attempts"`
	RouteSwitches           int                 `json:"route_switches"`
	CrossAccountResends     int                 `json:"cross_account_resends"`
	RequestIDObserved       bool                `json:"request_id_observed"`
	SecurityHeadersObserved bool                `json:"security_headers_observed"`
	FailureCode             string              `json:"failure_code,omitempty"`
}

type AliasProjectionCapabilityRecordV1 struct {
	CapabilityID   string          `json:"capability_id"`
	CanonicalJSON  json.RawMessage `json:"evidence"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
	Evidence       ValidatedRecord `json:"-"`
}

type AliasProjectionRecordV1 struct {
	ObservationJSON   json.RawMessage                     `json:"observation"`
	ObservationSHA256 string                              `json:"observation_sha256"`
	Capabilities      []AliasProjectionCapabilityRecordV1 `json:"capabilities"`
	Observation       AliasProjectionObservationV1        `json:"-"`
}

type AliasProjectionCatalogEntryV1 struct {
	RequestedIdentity     string `json:"requested_identity"`
	CanonicalRoute        string `json:"canonical_route"`
	RouteKind             string `json:"route_kind"`
	CatalogVisibility     string `json:"catalog_visibility"`
	CompatibilityRequired bool   `json:"compatibility_required"`
	DefaultReasoningLevel string `json:"default_reasoning_level,omitempty"`
	Listed                bool   `json:"listed"`
	ObservationSHA256     string `json:"observation_sha256"`
}

type AliasProjectionEffortObservationRefV1 struct {
	Effort            string `json:"effort"`
	ResolvedTone      string `json:"resolved_tone"`
	ObservationSHA256 string `json:"observation_sha256"`
}

type AliasProjectionMatrixEntryV1 struct {
	RequestedIdentity     string                                  `json:"requested_identity"`
	CanonicalRoute        string                                  `json:"canonical_route"`
	RouteKind             string                                  `json:"route_kind"`
	CatalogVisibility     string                                  `json:"catalog_visibility"`
	CompatibilityRequired bool                                    `json:"compatibility_required"`
	Protocol              string                                  `json:"protocol"`
	EndpointPath          string                                  `json:"endpoint_path"`
	EffortObservations    []AliasProjectionEffortObservationRefV1 `json:"effort_observations"`
}

type AliasProjectionFailureEntryV1 struct {
	RequestedModel      string              `json:"requested_model"`
	CaseID              AliasProjectionCase `json:"case_id"`
	Protocol            string              `json:"protocol"`
	ObservationSHA256   string              `json:"observation_sha256"`
	ExpectedFailureCode string              `json:"expected_failure_code"`
}

type AliasProjectionEvidenceSetV1 struct {
	Schema   string                          `json:"schema"`
	Catalog  []AliasProjectionCatalogEntryV1 `json:"catalog"`
	Matrix   []AliasProjectionMatrixEntryV1  `json:"matrix"`
	Failures []AliasProjectionFailureEntryV1 `json:"failures"`
	Records  []AliasProjectionRecordV1       `json:"records"`
}
