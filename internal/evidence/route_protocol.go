package evidence

import "encoding/json"

const (
	RouteProtocolCaptureSchemaV1     = "m365-wp2-route-protocol-capture/v1"
	RouteProtocolObservationSchemaV1 = "m365-wp2-route-protocol-observation/v1"
	RouteProtocolEvidenceSetSchemaV1 = "m365-wp2-route-protocol-evidence-set/v1"
	MaxRouteProtocolCaptureBytes     = 8 * 1024
)

type ProtocolClassification string

const ProtocolExposedAndSupported ProtocolClassification = "EXPOSED_AND_SUPPORTED"

type RouteProtocolCase string

const (
	RouteProtocolCaseSuccess       RouteProtocolCase = "success"
	RouteProtocolCaseUnknownRoute  RouteProtocolCase = "unknown_route"
	RouteProtocolCaseDisabledRoute RouteProtocolCase = "disabled_route"
	RouteProtocolCaseUpstreamEmpty RouteProtocolCase = "upstream_empty_response"
)

type RouteProtocolDescriptor struct {
	CanonicalRoute  string
	ResolvedTone    string
	Protocol        string
	EndpointPath    string
	AuthMode        string
	MappingEvidence string
	IdentityStatus  string
}

type RouteProtocolCaptureV1 struct {
	Schema                  string            `json:"schema"`
	CaseID                  RouteProtocolCase `json:"case_id"`
	Run                     int               `json:"run"`
	RequestedModel          string            `json:"requested_model"`
	TopLevelModel           string            `json:"top_level_model,omitempty"`
	CanonicalRoute          string            `json:"canonical_route,omitempty"`
	ResolvedTone            string            `json:"resolved_tone,omitempty"`
	Protocol                string            `json:"protocol"`
	EndpointPath            string            `json:"endpoint_path"`
	AuthMode                string            `json:"auth_mode"`
	RequestIDObserved       bool              `json:"request_id_observed"`
	SecurityHeadersObserved bool              `json:"security_headers_observed"`
	HTTPStatus              int               `json:"http_status"`
	BasicTextDelivered      bool              `json:"basic_text_delivered"`
	ReasoningEffortApplied  bool              `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool              `json:"reasoning_effort_ignored"`
	UpstreamAttempts        int               `json:"upstream_attempts"`
	RouteSwitches           int               `json:"route_switches"`
	CrossAccountResends     int               `json:"cross_account_resends"`
	MeaningfulUpstreamEvent string            `json:"meaningful_upstream_event"`
	FailureCode             string            `json:"failure_code,omitempty"`
}

type RouteProtocolObservationV1 struct {
	Schema                  string                 `json:"schema"`
	ObservationID           string                 `json:"observation_id"`
	CaseID                  RouteProtocolCase      `json:"case_id"`
	Run                     int                    `json:"run"`
	RequestedModel          string                 `json:"requested_model"`
	TopLevelModel           string                 `json:"top_level_model,omitempty"`
	CanonicalRoute          string                 `json:"canonical_route,omitempty"`
	ResolvedTone            string                 `json:"resolved_tone,omitempty"`
	Protocol                string                 `json:"protocol"`
	EndpointPath            string                 `json:"endpoint_path"`
	AuthMode                string                 `json:"auth_mode"`
	RequestIDObserved       bool                   `json:"request_id_observed"`
	SecurityHeadersObserved bool                   `json:"security_headers_observed"`
	Classification          ProtocolClassification `json:"classification"`
	HTTPStatus              int                    `json:"http_status"`
	BasicTextDelivered      bool                   `json:"basic_text_delivered"`
	ReasoningEffortApplied  bool                   `json:"reasoning_effort_applied"`
	ReasoningEffortIgnored  bool                   `json:"reasoning_effort_ignored"`
	UpstreamAttempts        int                    `json:"upstream_attempts"`
	RouteSwitches           int                    `json:"route_switches"`
	CrossAccountResends     int                    `json:"cross_account_resends"`
	MeaningfulUpstreamEvent string                 `json:"meaningful_upstream_event"`
	FailureCode             string                 `json:"failure_code,omitempty"`
}

type RouteProtocolCapabilityRecordV1 struct {
	CapabilityID   string          `json:"capability_id"`
	CanonicalJSON  json.RawMessage `json:"evidence"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
	Evidence       ValidatedRecord `json:"-"`
}

type RouteProtocolRecordV1 struct {
	ObservationJSON   json.RawMessage                   `json:"observation"`
	ObservationSHA256 string                            `json:"observation_sha256"`
	Capabilities      []RouteProtocolCapabilityRecordV1 `json:"capabilities"`
	Observation       RouteProtocolObservationV1        `json:"-"`
}

type RouteProtocolMatrixEntryV1 struct {
	CanonicalRoute           string                 `json:"canonical_route"`
	ResolvedTone             string                 `json:"resolved_tone"`
	Protocol                 string                 `json:"protocol"`
	EndpointPath             string                 `json:"endpoint_path"`
	Classification           ProtocolClassification `json:"classification"`
	SuccessObservationSHA256 []string               `json:"success_observation_sha256"`
	EmptyObservationSHA256   string                 `json:"empty_observation_sha256"`
}

type RouteProtocolFailureEntryV1 struct {
	RequestedModel      string            `json:"requested_model"`
	CaseID              RouteProtocolCase `json:"case_id"`
	Protocol            string            `json:"protocol"`
	ObservationSHA256   string            `json:"observation_sha256"`
	ExpectedFailureCode string            `json:"expected_failure_code"`
}

type RouteProtocolEvidenceSetV1 struct {
	Schema        string                        `json:"schema"`
	Matrix        []RouteProtocolMatrixEntryV1  `json:"matrix"`
	RouteFailures []RouteProtocolFailureEntryV1 `json:"route_failures"`
	Records       []RouteProtocolRecordV1       `json:"records"`
}
