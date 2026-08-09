package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

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

var allowedLegacyConfiguredCaptureFields = map[string]struct{}{
	"schema": {}, "case_id": {}, "classification": {}, "requested_model": {},
	"metadata_requested_model": {}, "metadata_response_model": {}, "top_level_model": {},
	"canonical_route": {}, "resolved_tone": {}, "route_kind": {}, "owner": {},
	"operational_status": {}, "mapping_evidence": {}, "identity_status": {},
	"catalog_visibility": {}, "configured_mapping": {}, "experimental": {},
	"default_reasoning_level": {}, "listed_in_catalog": {}, "per_key_restricted": {},
	"protocol": {}, "endpoint_path": {}, "auth_mode": {}, "effort": {},
	"reasoning_effort_applied": {}, "reasoning_effort_ignored": {}, "http_status": {},
	"basic_text_delivered": {}, "upstream_attempts": {}, "route_switches": {},
	"cross_account_resends": {}, "request_id_observed": {}, "security_headers_observed": {},
	"failure_code": {},
}

var allowedLegacyConfiguredProtocols = map[string]struct{}{
	"openai_models_catalog": {}, "openai_chat_completions_nonstream": {},
	"openai_responses_nonstream": {}, "anthropic_messages_nonstream": {},
	"legacy_chat_nonstream": {}, "legacy_chat_stream": {},
}

var allowedLegacyConfiguredEfforts = map[string]struct{}{
	"not_applicable": {}, "omitted": {}, "none": {}, "minimal": {}, "low": {},
	"medium": {}, "high": {}, "xhigh": {},
}

func CaptureLegacyConfigured(raw []byte, descriptor LegacyConfiguredDescriptor, binding CaptureBinding) (LegacyConfiguredRecordV1, error) {
	if len(raw) == 0 {
		return LegacyConfiguredRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxLegacyConfiguredCaptureBytes {
		return LegacyConfiguredRecordV1{}, validationError("evidence_too_large", "legacy_configured_capture_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedLegacyConfiguredCaptureFields); err != nil {
		return LegacyConfiguredRecordV1{}, err
	}
	var capture LegacyConfiguredCaptureV1
	if err := json.Unmarshal(raw, &capture); err != nil {
		return LegacyConfiguredRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := validateLegacyConfiguredCapture(capture, descriptor); err != nil {
		return LegacyConfiguredRecordV1{}, err
	}
	observation := LegacyConfiguredObservationV1{
		Schema: LegacyConfiguredObservationSchemaV1, ObservationID: fmt.Sprintf("%s.%s.%s.%s", capture.RequestedModel, capture.Protocol, capture.CaseID, capture.Effort),
		CaseID: capture.CaseID, Classification: capture.Classification, RequestedModel: capture.RequestedModel,
		MetadataRequestedModel: capture.MetadataRequestedModel, MetadataResponseModel: capture.MetadataResponseModel,
		TopLevelModel: capture.TopLevelModel, CanonicalRoute: capture.CanonicalRoute, ResolvedTone: capture.ResolvedTone,
		RouteKind: capture.RouteKind, Owner: capture.Owner, OperationalStatus: capture.OperationalStatus,
		MappingEvidence: capture.MappingEvidence, IdentityStatus: capture.IdentityStatus,
		CatalogVisibility: capture.CatalogVisibility, ConfiguredMapping: capture.ConfiguredMapping,
		Experimental: capture.Experimental, DefaultReasoningLevel: capture.DefaultReasoningLevel,
		ListedInCatalog: capture.ListedInCatalog, PerKeyRestricted: capture.PerKeyRestricted,
		Protocol: capture.Protocol, EndpointPath: capture.EndpointPath, AuthMode: capture.AuthMode,
		Effort: capture.Effort, ReasoningEffortApplied: capture.ReasoningEffortApplied,
		ReasoningEffortIgnored: capture.ReasoningEffortIgnored, HTTPStatus: capture.HTTPStatus,
		BasicTextDelivered: capture.BasicTextDelivered, UpstreamAttempts: capture.UpstreamAttempts,
		RouteSwitches: capture.RouteSwitches, CrossAccountResends: capture.CrossAccountResends,
		RequestIDObserved: capture.RequestIDObserved, SecurityHeadersObserved: capture.SecurityHeadersObserved,
		FailureCode: capture.FailureCode,
	}
	canonicalObservation, err := json.Marshal(observation)
	if err != nil {
		return LegacyConfiguredRecordV1{}, validationError("canonicalization_failed", "legacy_configured_observation", "/")
	}
	digest := sha256.Sum256(canonicalObservation)
	observationSHA256 := hex.EncodeToString(digest[:])
	capabilities := []LegacyConfiguredCapabilityRecordV1{}
	if capture.CaseID == LegacyConfiguredCaseSuccess {
		for _, capabilityID := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
			record, err := legacyConfiguredCapabilityRecord(capabilityID, observationSHA256, descriptor, binding)
			if err != nil {
				return LegacyConfiguredRecordV1{}, err
			}
			capabilities = append(capabilities, record)
		}
	}
	return LegacyConfiguredRecordV1{ObservationJSON: append(json.RawMessage(nil), canonicalObservation...), ObservationSHA256: observationSHA256, Capabilities: capabilities, Observation: observation}, nil
}

func validateLegacyConfiguredCapture(c LegacyConfiguredCaptureV1, d LegacyConfiguredDescriptor) error {
	if c.Schema != LegacyConfiguredCaptureSchemaV1 {
		return validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if c.RequestedModel == "" || !routePattern.MatchString(c.RequestedModel) {
		return validationError("invalid_route", "requested_model", "/requested_model")
	}
	if _, ok := allowedLegacyConfiguredProtocols[c.Protocol]; !ok {
		return validationError("invalid_enum", "legacy_configured_protocol", "/protocol")
	}
	if _, ok := allowedLegacyConfiguredEfforts[c.Effort]; !ok {
		return validationError("invalid_enum", "legacy_configured_effort", "/effort")
	}
	if c.Protocol != d.Protocol || c.EndpointPath != d.EndpointPath || c.AuthMode != d.AuthMode || c.Effort != d.Effort {
		return validationError("identity_mismatch", "protocol_descriptor", "/protocol")
	}
	if !c.RequestIDObserved || !c.SecurityHeadersObserved {
		return validationError("invalid_observation", "public_http_handler_middleware", "/")
	}
	if c.PerKeyRestricted {
		return validationError("invalid_observation", "no_per_key_restriction", "/per_key_restricted")
	}
	if c.UpstreamAttempts < 0 || c.RouteSwitches < 0 || c.CrossAccountResends < 0 {
		return validationError("invalid_observation", "nonnegative_attempt_counts", "/upstream_attempts")
	}
	switch c.AuthMode {
	case "api_key":
		if !strings.HasPrefix(c.EndpointPath, "/v1/") {
			return validationError("invalid_observation", "api_key_public_route", "/auth_mode")
		}
	case "admin_session":
		if c.EndpointPath != "/api/chat" && c.EndpointPath != "/api/chat/stream" {
			return validationError("invalid_observation", "admin_session_public_route", "/auth_mode")
		}
	default:
		return validationError("invalid_enum", "auth_mode", "/auth_mode")
	}
	switch c.CaseID {
	case LegacyConfiguredCaseCatalog:
		if err := matchLegacyConfiguredIdentity(c, d); err != nil {
			return err
		}
		if c.Classification != ClassificationInconclusive || c.Protocol != "openai_models_catalog" || c.EndpointPath != "/v1/models" || c.Effort != "not_applicable" || c.HTTPStatus != 200 || c.TopLevelModel != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.FailureCode != "" || c.ListedInCatalog != d.ListedInCatalog {
			return validationError("invalid_observation", "catalog_inconclusive_only", "/")
		}
	case LegacyConfiguredCaseSuccess:
		if err := matchLegacyConfiguredIdentity(c, d); err != nil {
			return err
		}
		if c.Classification != ClassificationVerified || c.Protocol == "openai_models_catalog" || c.Effort == "not_applicable" || c.HTTPStatus != 200 || !c.BasicTextDelivered || c.MetadataRequestedModel != c.RequestedModel || c.MetadataResponseModel != c.RequestedModel || c.TopLevelModel != c.RequestedModel || c.UpstreamAttempts != 1 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.FailureCode != "" || c.ListedInCatalog || c.ReasoningEffortApplied != d.ReasoningEffortApplied || c.ReasoningEffortIgnored != d.ReasoningEffortIgnored {
			return validationError("invalid_observation", "successful_route_classification", "/")
		}
	case LegacyConfiguredCaseUnknownRoute:
		if c.Classification != ClassificationInconclusive || c.HTTPStatus != 404 || c.FailureCode != "model_not_found" || c.TopLevelModel != "" || c.CanonicalRoute != "" || c.ResolvedTone != "" || c.RouteKind != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.Effort != "omitted" {
			return validationError("invalid_observation", "unknown_route_fail_closed", "/")
		}
	case LegacyConfiguredCaseDisabledRoute:
		if c.Classification != ClassificationInconclusive || c.HTTPStatus != 404 || c.FailureCode != "model_unavailable" || c.TopLevelModel != "" || c.CanonicalRoute != "" || c.ResolvedTone != "" || c.RouteKind != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.Effort != "omitted" {
			return validationError("invalid_observation", "disabled_route_fail_closed", "/")
		}
	default:
		return validationError("invalid_enum", "legacy_configured_case", "/case_id")
	}
	return nil
}

func matchLegacyConfiguredIdentity(c LegacyConfiguredCaptureV1, d LegacyConfiguredDescriptor) error {
	if c.RequestedModel != d.RequestedModel || c.CanonicalRoute != d.CanonicalRoute || c.ResolvedTone != d.ResolvedTone || c.RouteKind != d.RouteKind || c.Owner != d.Owner || c.OperationalStatus != d.OperationalStatus || c.MappingEvidence != d.RuntimeMappingEvidence || c.IdentityStatus != d.IdentityStatus || c.CatalogVisibility != d.CatalogVisibility || c.ConfiguredMapping != d.ConfiguredMapping || c.Experimental != d.Experimental || c.DefaultReasoningLevel != d.DefaultReasoningLevel {
		return validationError("identity_mismatch", "legacy_configured_descriptor", "/canonical_route")
	}
	if c.OperationalStatus != "enabled" {
		return validationError("invalid_observation", "route_enabled", "/operational_status")
	}
	if c.RouteKind != "legacy_direct" && c.RouteKind != "configured_mapping" {
		return validationError("invalid_enum", "legacy_or_configured_route_kind", "/route_kind")
	}
	if c.Owner != "microsoft-365" && c.Owner != "anthropic-via-microsoft-365" {
		return validationError("invalid_enum", "route_owner", "/owner")
	}
	if c.IdentityStatus == "upstream_identity_verified" {
		return validationError("verification_scope_forbidden", "no_separate_upstream_identity", "/identity_status")
	}
	return nil
}

func legacyConfiguredCapabilityRecord(capabilityID, observationSHA256 string, d LegacyConfiguredDescriptor, binding CaptureBinding) (LegacyConfiguredCapabilityRecordV1, error) {
	var dirty *string
	if binding.DirtyContentSHA256 != "" {
		x := binding.DirtyContentSHA256
		dirty = &x
	}
	manifest := ManifestV1{Schema: SchemaV1, NormativeADRSHA256: binding.NormativeADRSHA256, SourceHead: binding.SourceHead, DirtyContentSHA256: dirty, BinarySHA256: binding.BinarySHA256, HarnessSHA256: binding.HarnessSHA256, ObservationSHA256: observationSHA256, CanonicalRoute: d.CanonicalRoute, ResolvedTone: d.ResolvedTone, Protocol: d.Protocol, AccountProfileRef: binding.AccountProfileRef, EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256, MappingEvidence: d.AcceptedMappingEvidence, IdentityStatus: d.IdentityStatus, CapabilityID: capabilityID, Classification: ClassificationVerified, TestExecutionStatus: TestExecutionPass}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return LegacyConfiguredCapabilityRecordV1{}, validationError("canonicalization_failed", "wp2_evidence_manifest", "/")
	}
	validated, err := ValidateCapabilityEvidence(raw, IdentitySet{NormativeADRSHA256: binding.NormativeADRSHA256, SourceHead: binding.SourceHead, DirtyContentSHA256: binding.DirtyContentSHA256, BinarySHA256: binding.BinarySHA256, HarnessSHA256: binding.HarnessSHA256, ObservationSHA256: observationSHA256, CanonicalRoute: d.CanonicalRoute, ResolvedTone: d.ResolvedTone, Protocol: d.Protocol, AccountProfileRef: binding.AccountProfileRef, EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256})
	if err != nil {
		return LegacyConfiguredCapabilityRecordV1{}, err
	}
	return LegacyConfiguredCapabilityRecordV1{CapabilityID: capabilityID, CanonicalJSON: append(json.RawMessage(nil), validated.CanonicalJSON...), EvidenceSHA256: validated.ChecksumSHA256, Evidence: validated}, nil
}
