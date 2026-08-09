package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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

var allowedAliasProjectionCaptureFields = map[string]struct{}{
	"schema":                    {},
	"case_id":                   {},
	"requested_identity":        {},
	"top_level_model":           {},
	"metadata_requested_model":  {},
	"metadata_response_model":   {},
	"route_metadata_complete":   {},
	"fallback_used":             {},
	"configured_mapping":        {},
	"canonical_route":           {},
	"resolved_tone":             {},
	"route_kind":                {},
	"operational_status":        {},
	"mapping_evidence":          {},
	"identity_status":           {},
	"catalog_visibility":        {},
	"alias_used":                {},
	"compatibility_required":    {},
	"default_reasoning_level":   {},
	"deprecated":                {},
	"removal_date_absent":       {},
	"listed_in_catalog":         {},
	"per_key_restricted":        {},
	"protocol":                  {},
	"endpoint_path":             {},
	"auth_mode":                 {},
	"effort":                    {},
	"reasoning_effort_applied":  {},
	"reasoning_effort_ignored":  {},
	"http_status":               {},
	"basic_text_delivered":      {},
	"upstream_attempts":         {},
	"route_switches":            {},
	"cross_account_resends":     {},
	"request_id_observed":       {},
	"security_headers_observed": {},
	"failure_code":              {},
}

var allowedAliasProjectionProtocols = map[string]struct{}{
	"openai_models_catalog":             {},
	"openai_chat_completions_nonstream": {},
	"openai_responses_nonstream":        {},
	"anthropic_messages_nonstream":      {},
	"legacy_chat_nonstream":             {},
	"legacy_chat_stream":                {},
}

var allowedAliasProjectionEfforts = map[string]struct{}{
	"not_applicable": {},
	"omitted":        {},
	"none":           {},
	"minimal":        {},
	"low":            {},
	"medium":         {},
	"high":           {},
	"xhigh":          {},
}

func CaptureAliasProjection(raw []byte, descriptor AliasProjectionDescriptor, binding CaptureBinding) (AliasProjectionRecordV1, error) {
	if len(raw) == 0 {
		return AliasProjectionRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxAliasProjectionCaptureBytes {
		return AliasProjectionRecordV1{}, validationError("evidence_too_large", "alias_projection_capture_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedAliasProjectionCaptureFields); err != nil {
		return AliasProjectionRecordV1{}, err
	}
	var capture AliasProjectionCaptureV1
	if err := json.Unmarshal(raw, &capture); err != nil {
		return AliasProjectionRecordV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := validateAliasProjectionCapture(capture, descriptor); err != nil {
		return AliasProjectionRecordV1{}, err
	}
	observation := AliasProjectionObservationV1{
		Schema:                  AliasProjectionObservationSchemaV1,
		ObservationID:           fmt.Sprintf("%s.%s.%s.%s", capture.RequestedIdentity, capture.Protocol, capture.CaseID, capture.Effort),
		CaseID:                  capture.CaseID,
		RequestedIdentity:       capture.RequestedIdentity,
		TopLevelModel:           capture.TopLevelModel,
		MetadataRequestedModel:  capture.MetadataRequestedModel,
		MetadataResponseModel:   capture.MetadataResponseModel,
		RouteMetadataComplete:   capture.RouteMetadataComplete,
		FallbackUsed:            capture.FallbackUsed,
		ConfiguredMapping:       capture.ConfiguredMapping,
		CanonicalRoute:          capture.CanonicalRoute,
		ResolvedTone:            capture.ResolvedTone,
		RouteKind:               capture.RouteKind,
		OperationalStatus:       capture.OperationalStatus,
		MappingEvidence:         capture.MappingEvidence,
		IdentityStatus:          capture.IdentityStatus,
		CatalogVisibility:       capture.CatalogVisibility,
		AliasUsed:               capture.AliasUsed,
		CompatibilityRequired:   capture.CompatibilityRequired,
		DefaultReasoningLevel:   capture.DefaultReasoningLevel,
		Deprecated:              capture.Deprecated,
		RemovalDateAbsent:       capture.RemovalDateAbsent,
		ListedInCatalog:         capture.ListedInCatalog,
		PerKeyRestricted:        capture.PerKeyRestricted,
		Protocol:                capture.Protocol,
		EndpointPath:            capture.EndpointPath,
		AuthMode:                capture.AuthMode,
		Effort:                  capture.Effort,
		ReasoningEffortApplied:  capture.ReasoningEffortApplied,
		ReasoningEffortIgnored:  capture.ReasoningEffortIgnored,
		HTTPStatus:              capture.HTTPStatus,
		BasicTextDelivered:      capture.BasicTextDelivered,
		UpstreamAttempts:        capture.UpstreamAttempts,
		RouteSwitches:           capture.RouteSwitches,
		CrossAccountResends:     capture.CrossAccountResends,
		RequestIDObserved:       capture.RequestIDObserved,
		SecurityHeadersObserved: capture.SecurityHeadersObserved,
		FailureCode:             capture.FailureCode,
	}
	canonicalObservation, err := json.Marshal(observation)
	if err != nil {
		return AliasProjectionRecordV1{}, validationError("canonicalization_failed", "alias_projection_observation", "/")
	}
	digest := sha256.Sum256(canonicalObservation)
	observationSHA256 := hex.EncodeToString(digest[:])
	capabilities := []AliasProjectionCapabilityRecordV1{}
	if capture.CaseID == AliasProjectionCaseSuccess {
		for _, capabilityID := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
			record, err := aliasProjectionCapabilityRecord(capabilityID, observationSHA256, descriptor, binding)
			if err != nil {
				return AliasProjectionRecordV1{}, err
			}
			capabilities = append(capabilities, record)
		}
	}
	return AliasProjectionRecordV1{
		ObservationJSON:   append(json.RawMessage(nil), canonicalObservation...),
		ObservationSHA256: observationSHA256,
		Capabilities:      capabilities,
		Observation:       observation,
	}, nil
}

func validateAliasProjectionCapture(capture AliasProjectionCaptureV1, descriptor AliasProjectionDescriptor) error {
	if capture.Schema != AliasProjectionCaptureSchemaV1 {
		return validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if capture.RequestedIdentity == "" || !routePattern.MatchString(capture.RequestedIdentity) {
		return validationError("invalid_route", "requested_identity", "/requested_identity")
	}
	if _, ok := allowedAliasProjectionProtocols[capture.Protocol]; !ok {
		return validationError("invalid_enum", "alias_projection_protocol", "/protocol")
	}
	if _, ok := allowedAliasProjectionEfforts[capture.Effort]; !ok {
		return validationError("invalid_enum", "alias_projection_effort", "/effort")
	}
	if capture.Protocol != descriptor.Protocol || capture.EndpointPath != descriptor.EndpointPath || capture.AuthMode != descriptor.AuthMode || capture.Effort != descriptor.Effort {
		return validationError("identity_mismatch", "protocol_descriptor", "/protocol")
	}
	if !capture.RequestIDObserved || !capture.SecurityHeadersObserved {
		return validationError("invalid_observation", "public_http_handler_middleware", "/")
	}
	if capture.PerKeyRestricted {
		return validationError("invalid_observation", "no_per_key_restriction", "/per_key_restricted")
	}
	if capture.UpstreamAttempts < 0 || capture.RouteSwitches < 0 || capture.CrossAccountResends < 0 {
		return validationError("invalid_observation", "nonnegative_attempt_counts", "/upstream_attempts")
	}
	switch capture.AuthMode {
	case "api_key":
		if !strings.HasPrefix(capture.EndpointPath, "/v1/") {
			return validationError("invalid_observation", "api_key_public_route", "/auth_mode")
		}
	case "admin_session":
		if capture.EndpointPath != "/api/chat" && capture.EndpointPath != "/api/chat/stream" {
			return validationError("invalid_observation", "admin_session_public_route", "/auth_mode")
		}
	default:
		return validationError("invalid_enum", "auth_mode", "/auth_mode")
	}
	switch capture.CaseID {
	case AliasProjectionCaseCatalog:
		if err := matchAliasProjectionIdentity(capture, descriptor); err != nil {
			return err
		}
		if capture.Protocol != "openai_models_catalog" || capture.EndpointPath != "/v1/models" || capture.Effort != "not_applicable" || capture.HTTPStatus != 200 || capture.TopLevelModel != "" || capture.MetadataRequestedModel != "" || capture.MetadataResponseModel != "" || capture.RouteMetadataComplete || capture.FallbackUsed || capture.ConfiguredMapping || capture.BasicTextDelivered || capture.UpstreamAttempts != 0 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.FailureCode != "" || capture.ListedInCatalog != descriptor.ListedInCatalog || capture.Deprecated || !capture.RemovalDateAbsent {
			return validationError("invalid_observation", "catalog_projection_contract", "/")
		}
	case AliasProjectionCaseSuccess:
		if err := matchAliasProjectionIdentity(capture, descriptor); err != nil {
			return err
		}
		if capture.Protocol == "openai_models_catalog" || capture.Effort == "not_applicable" || capture.HTTPStatus != 200 || !capture.BasicTextDelivered || capture.TopLevelModel != capture.RequestedIdentity || capture.MetadataRequestedModel != capture.RequestedIdentity || capture.MetadataResponseModel != capture.RequestedIdentity || !capture.RouteMetadataComplete || capture.FallbackUsed || capture.ConfiguredMapping || capture.UpstreamAttempts != 1 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.FailureCode != "" || capture.ListedInCatalog || capture.Deprecated || !capture.RemovalDateAbsent || capture.ReasoningEffortApplied != descriptor.ReasoningEffortApplied || capture.ReasoningEffortIgnored != descriptor.ReasoningEffortIgnored {
			return validationError("invalid_observation", "successful_alias_projection", "/")
		}
	case AliasProjectionCaseUnknownRoute:
		if capture.HTTPStatus != 404 || capture.FailureCode != "model_not_found" || capture.TopLevelModel != "" || capture.MetadataRequestedModel != "" || capture.MetadataResponseModel != "" || capture.RouteMetadataComplete || capture.FallbackUsed || capture.ConfiguredMapping || capture.CanonicalRoute != "" || capture.ResolvedTone != "" || capture.RouteKind != "" || capture.AliasUsed || capture.CompatibilityRequired || capture.BasicTextDelivered || capture.UpstreamAttempts != 0 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.Effort != "omitted" {
			return validationError("invalid_observation", "unknown_route_fail_closed", "/")
		}
	case AliasProjectionCaseDisabledRoute:
		if capture.HTTPStatus != 404 || capture.FailureCode != "model_unavailable" || capture.TopLevelModel != "" || capture.MetadataRequestedModel != "" || capture.MetadataResponseModel != "" || capture.RouteMetadataComplete || capture.FallbackUsed || capture.ConfiguredMapping || capture.CanonicalRoute != "" || capture.ResolvedTone != "" || capture.RouteKind != "" || capture.AliasUsed || capture.CompatibilityRequired || capture.BasicTextDelivered || capture.UpstreamAttempts != 0 || capture.RouteSwitches != 0 || capture.CrossAccountResends != 0 || capture.Effort != "omitted" {
			return validationError("invalid_observation", "disabled_route_fail_closed", "/")
		}
	default:
		return validationError("invalid_enum", "alias_projection_case", "/case_id")
	}
	return nil
}

func matchAliasProjectionIdentity(capture AliasProjectionCaptureV1, descriptor AliasProjectionDescriptor) error {
	if capture.RequestedIdentity != descriptor.RequestedIdentity || capture.CanonicalRoute != descriptor.CanonicalRoute || capture.ResolvedTone != descriptor.ResolvedTone || capture.RouteKind != descriptor.RouteKind || capture.OperationalStatus != descriptor.OperationalStatus || capture.MappingEvidence != descriptor.RuntimeMappingEvidence || capture.IdentityStatus != descriptor.IdentityStatus || capture.CatalogVisibility != descriptor.CatalogVisibility || capture.CompatibilityRequired != descriptor.CompatibilityRequired || capture.DefaultReasoningLevel != descriptor.DefaultReasoningLevel {
		return validationError("identity_mismatch", "alias_projection_descriptor", "/canonical_route")
	}
	if !capture.AliasUsed || !capture.CompatibilityRequired || capture.OperationalStatus != "enabled" {
		return validationError("invalid_observation", "compatibility_identity_enabled", "/")
	}
	if capture.RouteKind != "alias" && capture.RouteKind != "preset" {
		return validationError("invalid_enum", "route_kind_alias_or_preset", "/route_kind")
	}
	if capture.CatalogVisibility != "compatibility" && capture.CatalogVisibility != "hidden" {
		return validationError("invalid_enum", "compatibility_catalog_visibility", "/catalog_visibility")
	}
	if capture.IdentityStatus == "upstream_identity_verified" {
		return validationError("verification_scope_forbidden", "alias_not_separate_upstream_identity", "/identity_status")
	}
	return nil
}

func aliasProjectionCapabilityRecord(capabilityID, observationSHA256 string, descriptor AliasProjectionDescriptor, binding CaptureBinding) (AliasProjectionCapabilityRecordV1, error) {
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
		MappingEvidence:         descriptor.AcceptedMappingEvidence,
		IdentityStatus:          descriptor.IdentityStatus,
		CapabilityID:            capabilityID,
		Classification:          ClassificationVerified,
		TestExecutionStatus:     TestExecutionPass,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return AliasProjectionCapabilityRecordV1{}, validationError("canonicalization_failed", "wp2_evidence_manifest", "/")
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
		return AliasProjectionCapabilityRecordV1{}, err
	}
	return AliasProjectionCapabilityRecordV1{
		CapabilityID:   capabilityID,
		CanonicalJSON:  append(json.RawMessage(nil), validated.CanonicalJSON...),
		EvidenceSHA256: validated.ChecksumSHA256,
		Evidence:       validated,
	}, nil
}
