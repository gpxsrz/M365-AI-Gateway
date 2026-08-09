package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

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

var allowedRouteProtocolCaptureFields = map[string]struct{}{
	"schema":                    {},
	"case_id":                   {},
	"run":                       {},
	"requested_model":           {},
	"top_level_model":           {},
	"canonical_route":           {},
	"resolved_tone":             {},
	"protocol":                  {},
	"endpoint_path":             {},
	"auth_mode":                 {},
	"request_id_observed":       {},
	"security_headers_observed": {},
	"http_status":               {},
	"basic_text_delivered":      {},
	"reasoning_effort_applied":  {},
	"reasoning_effort_ignored":  {},
	"upstream_attempts":         {},
	"route_switches":            {},
	"cross_account_resends":     {},
	"meaningful_upstream_event": {},
	"failure_code":              {},
}

func CaptureRouteProtocol(raw []byte, descriptor RouteProtocolDescriptor, binding CaptureBinding) (RouteProtocolRecordV1, error) {
	if len(raw) == 0 {
		return RouteProtocolRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxRouteProtocolCaptureBytes {
		return RouteProtocolRecordV1{}, validationError("evidence_too_large", "route_protocol_capture_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedRouteProtocolCaptureFields); err != nil {
		return RouteProtocolRecordV1{}, err
	}
	var capture RouteProtocolCaptureV1
	if err := json.Unmarshal(raw, &capture); err != nil {
		return RouteProtocolRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := validateRouteProtocolCapture(capture, descriptor); err != nil {
		return RouteProtocolRecordV1{}, err
	}

	observation := RouteProtocolObservationV1{
		Schema:                  RouteProtocolObservationSchemaV1,
		ObservationID:           fmt.Sprintf("%s.%s.%s.%d", capture.RequestedModel, capture.Protocol, capture.CaseID, capture.Run),
		CaseID:                  capture.CaseID,
		Run:                     capture.Run,
		RequestedModel:          capture.RequestedModel,
		TopLevelModel:           capture.TopLevelModel,
		CanonicalRoute:          capture.CanonicalRoute,
		ResolvedTone:            capture.ResolvedTone,
		Protocol:                capture.Protocol,
		EndpointPath:            capture.EndpointPath,
		AuthMode:                capture.AuthMode,
		RequestIDObserved:       capture.RequestIDObserved,
		SecurityHeadersObserved: capture.SecurityHeadersObserved,
		Classification:          ProtocolExposedAndSupported,
		HTTPStatus:              capture.HTTPStatus,
		BasicTextDelivered:      capture.BasicTextDelivered,
		ReasoningEffortApplied:  capture.ReasoningEffortApplied,
		ReasoningEffortIgnored:  capture.ReasoningEffortIgnored,
		UpstreamAttempts:        capture.UpstreamAttempts,
		RouteSwitches:           capture.RouteSwitches,
		CrossAccountResends:     capture.CrossAccountResends,
		MeaningfulUpstreamEvent: capture.MeaningfulUpstreamEvent,
		FailureCode:             capture.FailureCode,
	}
	canonicalObservation, err := json.Marshal(observation)
	if err != nil {
		return RouteProtocolRecordV1{}, validationError("canonicalization_failed", "route_protocol_observation", "/")
	}
	digest := sha256.Sum256(canonicalObservation)
	observationSHA256 := hex.EncodeToString(digest[:])

	capabilityIDs := []string{}
	switch capture.CaseID {
	case RouteProtocolCaseSuccess:
		capabilityIDs = []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"}
	case RouteProtocolCaseUpstreamEmpty:
		capabilityIDs = []string{"protocol_transport"}
	}
	capabilities := make([]RouteProtocolCapabilityRecordV1, 0, len(capabilityIDs))
	for _, capabilityID := range capabilityIDs {
		record, err := routeProtocolCapabilityRecord(capabilityID, observationSHA256, capture, descriptor, binding)
		if err != nil {
			return RouteProtocolRecordV1{}, err
		}
		capabilities = append(capabilities, record)
	}
	return RouteProtocolRecordV1{
		ObservationJSON:   append(json.RawMessage(nil), canonicalObservation...),
		ObservationSHA256: observationSHA256,
		Capabilities:      capabilities,
		Observation:       observation,
	}, nil
}

func validateRouteProtocolCapture(capture RouteProtocolCaptureV1, descriptor RouteProtocolDescriptor) error {
	if capture.Schema != RouteProtocolCaptureSchemaV1 {
		return validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if capture.Run < 1 || capture.Run > 3 {
		return validationError("invalid_observation", "independent_run", "/run")
	}
	if capture.RequestedModel == "" || !routePattern.MatchString(capture.RequestedModel) {
		return validationError("invalid_route", "requested_model", "/requested_model")
	}
	if capture.Protocol != descriptor.Protocol {
		return validationError("identity_mismatch", "protocol_descriptor", "/protocol")
	}
	if capture.EndpointPath != descriptor.EndpointPath {
		return validationError("identity_mismatch", "endpoint_descriptor", "/endpoint_path")
	}
	if capture.AuthMode != descriptor.AuthMode {
		return validationError("identity_mismatch", "auth_mode_descriptor", "/auth_mode")
	}
	if _, ok := allowedProtocols[capture.Protocol]; !ok {
		return validationError("invalid_enum", "protocol", "/protocol")
	}
	if !capture.RequestIDObserved || !capture.SecurityHeadersObserved {
		return validationError("invalid_observation", "public_http_handler_middleware", "/")
	}
	switch capture.AuthMode {
	case "api_key":
		if !strings.HasPrefix(capture.EndpointPath, "/v1/") {
			return validationError("invalid_observation", "api_key_public_route", "/auth_mode")
		}
	case "admin_session":
		if capture.EndpointPath != "/api/chat" {
			return validationError("invalid_observation", "admin_session_public_route", "/auth_mode")
		}
	default:
		return validationError("invalid_enum", "auth_mode", "/auth_mode")
	}
	if capture.UpstreamAttempts < 0 || capture.RouteSwitches < 0 || capture.CrossAccountResends < 0 {
		return validationError("invalid_observation", "nonnegative_attempt_counts", "/upstream_attempts")
	}
	if capture.MeaningfulUpstreamEvent != "none" && capture.MeaningfulUpstreamEvent != "text" {
		return validationError("invalid_enum", "meaningful_upstream_event", "/meaningful_upstream_event")
	}
	switch capture.CaseID {
	case RouteProtocolCaseSuccess:
		if capture.RequestedModel != descriptor.CanonicalRoute || capture.CanonicalRoute != descriptor.CanonicalRoute || capture.ResolvedTone != descriptor.ResolvedTone {
			return validationError("identity_mismatch", "route_descriptor", "/canonical_route")
		}
		if capture.HTTPStatus != 200 || !capture.BasicTextDelivered || capture.TopLevelModel != capture.RequestedModel || capture.UpstreamAttempts != 1 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.MeaningfulUpstreamEvent != "text" || capture.FailureCode != "" {
			return validationError("invalid_observation", "successful_basic_text_delivery", "/")
		}
	case RouteProtocolCaseUpstreamEmpty:
		if capture.RequestedModel != descriptor.CanonicalRoute || capture.CanonicalRoute != descriptor.CanonicalRoute || capture.ResolvedTone != descriptor.ResolvedTone {
			return validationError("identity_mismatch", "route_descriptor", "/canonical_route")
		}
		if capture.HTTPStatus != 502 || capture.BasicTextDelivered || capture.TopLevelModel != "" || capture.UpstreamAttempts != 1 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.MeaningfulUpstreamEvent != "none" || capture.FailureCode != "upstream_empty_response" {
			return validationError("invalid_observation", "upstream_empty_fail_closed", "/")
		}
	case RouteProtocolCaseUnknownRoute:
		if capture.TopLevelModel != "" || capture.CanonicalRoute != "" || capture.ResolvedTone != "" || capture.HTTPStatus != 404 || capture.BasicTextDelivered || capture.UpstreamAttempts != 0 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.FailureCode != "model_not_found" || capture.MeaningfulUpstreamEvent != "none" {
			return validationError("invalid_observation", "unknown_route_fail_closed", "/")
		}
	case RouteProtocolCaseDisabledRoute:
		if capture.TopLevelModel != "" || capture.CanonicalRoute != "" || capture.ResolvedTone != "" || capture.HTTPStatus != 404 || capture.BasicTextDelivered || capture.UpstreamAttempts != 0 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.FailureCode != "model_unavailable" || capture.MeaningfulUpstreamEvent != "none" {
			return validationError("invalid_observation", "disabled_route_fail_closed", "/")
		}
	default:
		return validationError("invalid_enum", "route_protocol_case", "/case_id")
	}
	return nil
}

func routeProtocolCapabilityRecord(capabilityID, observationSHA256 string, capture RouteProtocolCaptureV1, descriptor RouteProtocolDescriptor, binding CaptureBinding) (RouteProtocolCapabilityRecordV1, error) {
	var dirtyContentSHA256 *string
	if binding.DirtyContentSHA256 != "" {
		dirty := binding.DirtyContentSHA256
		dirtyContentSHA256 = &dirty
	}
	manifest := ManifestV1{
		Schema:                  SchemaV1,
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      dirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          descriptor.CanonicalRoute,
		ResolvedTone:            descriptor.ResolvedTone,
		Protocol:                descriptor.Protocol,
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
		MappingEvidence:         descriptor.MappingEvidence,
		IdentityStatus:          descriptor.IdentityStatus,
		CapabilityID:            capabilityID,
		Classification:          ClassificationVerified,
		TestExecutionStatus:     TestExecutionPass,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return RouteProtocolCapabilityRecordV1{}, validationError("canonicalization_failed", "wp2_evidence_manifest", "/")
	}
	validated, err := ValidateCapabilityEvidence(raw, IdentitySet{
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      binding.DirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          descriptor.CanonicalRoute,
		ResolvedTone:            descriptor.ResolvedTone,
		Protocol:                descriptor.Protocol,
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
	})
	if err != nil {
		return RouteProtocolCapabilityRecordV1{}, err
	}
	return RouteProtocolCapabilityRecordV1{
		CapabilityID:   capabilityID,
		CanonicalJSON:  append(json.RawMessage(nil), validated.CanonicalJSON...),
		EvidenceSHA256: validated.ChecksumSHA256,
		Evidence:       validated,
	}, nil
}
