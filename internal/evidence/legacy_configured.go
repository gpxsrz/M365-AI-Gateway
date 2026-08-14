package evidence

import "encoding/json"

const (
	LegacyConfiguredCaptureSchemaV1     = "m365-wp2-legacy-configured-capture/v1"
	LegacyConfiguredObservationSchemaV1 = "m365-wp2-legacy-configured-observation/v1"
	LegacyConfiguredEvidenceSetSchemaV1 = "m365-wp2-legacy-configured-evidence-set/v1"
	MaxLegacyConfiguredCaptureBytes     = 12 * 1024
)

type LegacyConfiguredCase string

const (
	LegacyConfiguredCaseCatalog       LegacyConfiguredCase = "catalog_projection"
	LegacyConfiguredCaseSuccess       LegacyConfiguredCase = "success"
	LegacyConfiguredCaseUnknownRoute  LegacyConfiguredCase = "unknown_route"
	LegacyConfiguredCaseDisabledRoute LegacyConfiguredCase = "disabled_route"
)

type LegacyConfiguredDescriptor struct {
	RequestedModel          string
	CanonicalRoute          string
	ResolvedTone            string
	RouteKind               string
	Owner                   string
	OperationalStatus       string
	RuntimeMappingEvidence  string
	AcceptedMappingEvidence string
	IdentityStatus          string
	CatalogVisibility       string
	ConfiguredMapping       bool
	Experimental            bool
	DefaultReasoningLevel   string
	Protocol                string
	EndpointPath            string
	AuthMode                string
	Effort                  string
	ReasoningEffortApplied  bool
	ReasoningEffortIgnored  bool
	ListedInCatalog         bool
}

type LegacyConfiguredCaptureV1 struct {
	Schema                  string               `json:"schema"`
	CaseID                  LegacyConfiguredCase `json:"case_id"`
	Classification          Classification       `json:"classification"`
	RequestedModel          string               `json:"requested_model"`
	MetadataRequestedModel  string               `json:"metadata_requested_model,omitempty"`
	MetadataResponseModel   string               `json:"metadata_response_model,omitempty"`
	TopLevelModel           string               `json:"top_level_model,omitempty"`
	CanonicalRoute          string               `json:"canonical_route,omitempty"`
	ResolvedTone            string               `json:"resolved_tone,omitempty"`
	RouteKind               string               `json:"route_kind,omitempty"`
	Owner                   string               `json:"owner,omitempty"`
	OperationalStatus       string               `json:"operational_status,omitempty"`
	MappingEvidence         string               `json:"mapping_evidence,omitempty"`
	IdentityStatus          string               `json:"identity_status,omitempty"`
	CatalogVisibility       string               `json:"catalog_visibility,omitempty"`
	ConfiguredMapping       bool                 `json:"configured_mapping"`
	Experimental            bool                 `json:"experimental"`
	DefaultReasoningLevel   string               `json:"default_reasoning_level,omitempty"`
	ListedInCatalog         bool                 `json:"listed_in_catalog"`
	PerKeyRestricted        bool                 `json:"per_key_restricted"`
	Protocol                string               `json:"protocol"`
	EndpointPath            string               `json:"endpoint_path"`
	AuthMode                string               `json:"auth_mode"`
	Effort                  string               `json:"effort"`
	ReasoningEffortApplied  bool                 `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool                 `json:"reasoning_effort_ignored"`
	HTTPStatus              int                  `json:"http_status"`
	BasicTextDelivered      bool                 `json:"basic_text_delivered"`
	UpstreamAttempts        int                  `json:"upstream_attempts"`
	RouteSwitches           int                  `json:"route_switches"`
	CrossAccountResends     int                  `json:"cross_account_resends"`
	RequestIDObserved       bool                 `json:"request_id_observed"`
	SecurityHeadersObserved bool                 `json:"security_headers_observed"`
	FailureCode             string               `json:"failure_code,omitempty"`
}

type LegacyConfiguredObservationV1 struct {
	Schema                  string               `json:"schema"`
	ObservationID           string               `json:"observation_id"`
	CaseID                  LegacyConfiguredCase `json:"case_id"`
	Classification          Classification       `json:"classification"`
	RequestedModel          string               `json:"requested_model"`
	MetadataRequestedModel  string               `json:"metadata_requested_model,omitempty"`
	MetadataResponseModel   string               `json:"metadata_response_model,omitempty"`
	TopLevelModel           string               `json:"top_level_model,omitempty"`
	CanonicalRoute          string               `json:"canonical_route,omitempty"`
	ResolvedTone            string               `json:"resolved_tone,omitempty"`
	RouteKind               string               `json:"route_kind,omitempty"`
	Owner                   string               `json:"owner,omitempty"`
	OperationalStatus       string               `json:"operational_status,omitempty"`
	MappingEvidence         string               `json:"mapping_evidence,omitempty"`
	IdentityStatus          string               `json:"identity_status,omitempty"`
	CatalogVisibility       string               `json:"catalog_visibility,omitempty"`
	ConfiguredMapping       bool                 `json:"configured_mapping"`
	Experimental            bool                 `json:"experimental"`
	DefaultReasoningLevel   string               `json:"default_reasoning_level,omitempty"`
	ListedInCatalog         bool                 `json:"listed_in_catalog"`
	PerKeyRestricted        bool                 `json:"per_key_restricted"`
	Protocol                string               `json:"protocol"`
	EndpointPath            string               `json:"endpoint_path"`
	AuthMode                string               `json:"auth_mode"`
	Effort                  string               `json:"effort"`
	ReasoningEffortApplied  bool                 `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool                 `json:"reasoning_effort_ignored"`
	HTTPStatus              int                  `json:"http_status"`
	BasicTextDelivered      bool                 `json:"basic_text_delivered"`
	UpstreamAttempts        int                  `json:"upstream_attempts"`
	RouteSwitches           int                  `json:"route_switches"`
	CrossAccountResends     int                  `json:"cross_account_resends"`
	RequestIDObserved       bool                 `json:"request_id_observed"`
	SecurityHeadersObserved bool                 `json:"security_headers_observed"`
	FailureCode             string               `json:"failure_code,omitempty"`
}

type LegacyConfiguredCapabilityRecordV1 struct {
	CapabilityID   string          `json:"capability_id"`
	CanonicalJSON  json.RawMessage `json:"evidence"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
	Evidence       ValidatedRecord `json:"-"`
}

type LegacyConfiguredRecordV1 struct {
	ObservationJSON   json.RawMessage                      `json:"observation"`
	ObservationSHA256 string                               `json:"observation_sha256"`
	Capabilities      []LegacyConfiguredCapabilityRecordV1 `json:"capabilities"`
	Observation       LegacyConfiguredObservationV1        `json:"-"`
}

type LegacyConfiguredCatalogEntryV1 struct {
	RequestedModel        string         `json:"requested_model"`
	CanonicalRoute        string         `json:"canonical_route"`
	ResolvedTone          string         `json:"resolved_tone"`
	RouteKind             string         `json:"route_kind"`
	Owner                 string         `json:"owner"`
	CatalogVisibility     string         `json:"catalog_visibility"`
	ConfiguredMapping     bool           `json:"configured_mapping"`
	Experimental          bool           `json:"experimental"`
	DefaultReasoningLevel string         `json:"default_reasoning_level,omitempty"`
	Classification        Classification `json:"classification"`
	Listed                bool           `json:"listed"`
	ObservationSHA256     string         `json:"observation_sha256"`
}

type LegacyConfiguredEffortObservationRefV1 struct {
	Effort            string `json:"effort"`
	ResolvedTone      string `json:"resolved_tone"`
	ObservationSHA256 string `json:"observation_sha256"`
}

type LegacyConfiguredMatrixEntryV1 struct {
	RequestedModel     string                                   `json:"requested_model"`
	CanonicalRoute     string                                   `json:"canonical_route"`
	RouteKind          string                                   `json:"route_kind"`
	Owner              string                                   `json:"owner"`
	ConfiguredMapping  bool                                     `json:"configured_mapping"`
	Protocol           string                                   `json:"protocol"`
	EndpointPath       string                                   `json:"endpoint_path"`
	Classification     Classification                           `json:"classification"`
	EffortObservations []LegacyConfiguredEffortObservationRefV1 `json:"effort_observations"`
}

type LegacyConfiguredFailureEntryV1 struct {
	RequestedModel      string               `json:"requested_model"`
	CaseID              LegacyConfiguredCase `json:"case_id"`
	Protocol            string               `json:"protocol"`
	ObservationSHA256   string               `json:"observation_sha256"`
	ExpectedFailureCode string               `json:"expected_failure_code"`
}

type LegacyConfiguredEvidenceSetV1 struct {
	Schema   string                           `json:"schema"`
	Catalog  []LegacyConfiguredCatalogEntryV1 `json:"catalog"`
	Matrix   []LegacyConfiguredMatrixEntryV1  `json:"matrix"`
	Failures []LegacyConfiguredFailureEntryV1 `json:"failures"`
	Records  []LegacyConfiguredRecordV1       `json:"records"`
}
