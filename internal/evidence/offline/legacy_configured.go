package offline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	core "m365-native/internal/evidence"
)

type LegacyConfiguredCase = core.LegacyConfiguredCase
type LegacyConfiguredDescriptor = core.LegacyConfiguredDescriptor
type LegacyConfiguredCaptureV1 = core.LegacyConfiguredCaptureV1
type LegacyConfiguredObservationV1 = core.LegacyConfiguredObservationV1
type LegacyConfiguredCapabilityRecordV1 = core.LegacyConfiguredCapabilityRecordV1
type LegacyConfiguredRecordV1 = core.LegacyConfiguredRecordV1
type LegacyConfiguredCatalogEntryV1 = core.LegacyConfiguredCatalogEntryV1
type LegacyConfiguredEffortObservationRefV1 = core.LegacyConfiguredEffortObservationRefV1
type LegacyConfiguredMatrixEntryV1 = core.LegacyConfiguredMatrixEntryV1
type LegacyConfiguredFailureEntryV1 = core.LegacyConfiguredFailureEntryV1
type LegacyConfiguredEvidenceSetV1 = core.LegacyConfiguredEvidenceSetV1

const (
	LegacyConfiguredCaptureSchemaV1     = core.LegacyConfiguredCaptureSchemaV1
	LegacyConfiguredObservationSchemaV1 = core.LegacyConfiguredObservationSchemaV1
	LegacyConfiguredEvidenceSetSchemaV1 = core.LegacyConfiguredEvidenceSetSchemaV1
	MaxLegacyConfiguredCaptureBytes     = core.MaxLegacyConfiguredCaptureBytes
	LegacyConfiguredCaseCatalog         = core.LegacyConfiguredCaseCatalog
	LegacyConfiguredCaseSuccess         = core.LegacyConfiguredCaseSuccess
	LegacyConfiguredCaseUnknownRoute    = core.LegacyConfiguredCaseUnknownRoute
	LegacyConfiguredCaseDisabledRoute   = core.LegacyConfiguredCaseDisabledRoute
)

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
		if c.Classification != core.ClassificationInconclusive || c.Protocol != "openai_models_catalog" || c.EndpointPath != "/v1/models" || c.Effort != "not_applicable" || c.HTTPStatus != 200 || c.TopLevelModel != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.FailureCode != "" || c.ListedInCatalog != d.ListedInCatalog {
			return validationError("invalid_observation", "catalog_inconclusive_only", "/")
		}
	case LegacyConfiguredCaseSuccess:
		if err := matchLegacyConfiguredIdentity(c, d); err != nil {
			return err
		}
		if c.Classification != core.ClassificationVerified || c.Protocol == "openai_models_catalog" || c.Effort == "not_applicable" || c.HTTPStatus != 200 || !c.BasicTextDelivered || c.MetadataRequestedModel != c.RequestedModel || c.MetadataResponseModel != c.RequestedModel || c.TopLevelModel != c.RequestedModel || c.UpstreamAttempts != 1 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.FailureCode != "" || c.ListedInCatalog || c.ReasoningEffortApplied != d.ReasoningEffortApplied || c.ReasoningEffortIgnored != d.ReasoningEffortIgnored {
			return validationError("invalid_observation", "successful_route_classification", "/")
		}
	case LegacyConfiguredCaseUnknownRoute:
		if c.Classification != core.ClassificationInconclusive || c.HTTPStatus != 404 || c.FailureCode != "model_not_found" || c.TopLevelModel != "" || c.CanonicalRoute != "" || c.ResolvedTone != "" || c.RouteKind != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.Effort != "omitted" {
			return validationError("invalid_observation", "unknown_route_fail_closed", "/")
		}
	case LegacyConfiguredCaseDisabledRoute:
		if c.Classification != core.ClassificationInconclusive || c.HTTPStatus != 404 || c.FailureCode != "model_unavailable" || c.TopLevelModel != "" || c.CanonicalRoute != "" || c.ResolvedTone != "" || c.RouteKind != "" || c.BasicTextDelivered || c.UpstreamAttempts != 0 || c.RouteSwitches != 0 || c.CrossAccountResends != 0 || c.Effort != "omitted" {
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

func legacyConfiguredCapabilityRecord(capabilityID, observationSHA256 string, descriptor LegacyConfiguredDescriptor, binding CaptureBinding) (LegacyConfiguredCapabilityRecordV1, error) {
	built, err := buildVerifiedCapabilityEvidence(capabilityID, observationSHA256, capabilityEvidenceDescriptor{
		CanonicalRoute: descriptor.CanonicalRoute, ResolvedTone: descriptor.ResolvedTone, Protocol: descriptor.Protocol,
		MappingEvidence: descriptor.AcceptedMappingEvidence, IdentityStatus: descriptor.IdentityStatus,
	}, binding)
	if err != nil {
		return LegacyConfiguredCapabilityRecordV1{}, err
	}
	return LegacyConfiguredCapabilityRecordV1{CapabilityID: capabilityID, CanonicalJSON: built.CanonicalJSON, EvidenceSHA256: built.EvidenceSHA256, Evidence: built.Evidence}, nil
}
