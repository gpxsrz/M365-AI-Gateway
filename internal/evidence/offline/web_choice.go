package offline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	core "m365-native/internal/evidence"
)

const (
	WebChoiceCaptureSchemaV1     = core.WebChoiceCaptureSchemaV1
	WebChoiceObservationSchemaV1 = core.WebChoiceObservationSchemaV1
	MaxWebChoiceCaptureBytes     = core.MaxWebChoiceCaptureBytes
)

type MappingBehavior = core.MappingBehavior

const (
	MappingBehaviorExact          = core.MappingBehaviorExact
	MappingBehaviorCaseNormalized = core.MappingBehaviorCaseNormalized
	MappingBehaviorDifferent      = core.MappingBehaviorDifferent
)

type WebChoiceRoute = core.WebChoiceRoute
type WebChoiceObservationV1 = core.WebChoiceObservationV1
type CapturedWebChoice = core.CapturedWebChoice

type webChoiceCaptureV1 struct {
	Schema string `json:"schema"`
	Tone   string `json:"tone"`
}

var allowedWebChoiceCaptureFields = map[string]struct{}{
	"schema": {},
	"tone":   {},
}

func CaptureWebChoice(raw []byte, route WebChoiceRoute, binding CaptureBinding) (CapturedWebChoice, error) {
	if len(raw) == 0 {
		return CapturedWebChoice{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxWebChoiceCaptureBytes {
		return CapturedWebChoice{}, validationError("evidence_too_large", "web_choice_capture_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedWebChoiceCaptureFields); err != nil {
		return CapturedWebChoice{}, err
	}

	var capture webChoiceCaptureV1
	if err := json.Unmarshal(raw, &capture); err != nil {
		return CapturedWebChoice{}, validationError("invalid_json", "single_json_object", "/")
	}
	if capture.Schema == "" {
		return CapturedWebChoice{}, validationError("missing_field", "required_binding", "/schema")
	}
	if capture.Schema != WebChoiceCaptureSchemaV1 {
		return CapturedWebChoice{}, validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if capture.Tone == "" {
		return CapturedWebChoice{}, validationError("missing_field", "required_binding", "/tone")
	}
	if !tonePattern.MatchString(capture.Tone) {
		return CapturedWebChoice{}, validationError("invalid_tone", "observed_web_tone", "/tone")
	}
	if err := validateWebChoiceRoute(route); err != nil {
		return CapturedWebChoice{}, err
	}

	observation := WebChoiceObservationV1{
		Schema:          WebChoiceObservationSchemaV1,
		WebChoiceID:     route.WebChoiceID,
		CanonicalRoute:  route.CanonicalRoute,
		RouteKind:       route.RouteKind,
		RegistryTone:    route.RegistryTone,
		ObservedWebTone: capture.Tone,
		MappingBehavior: mappingBehavior(route.RegistryTone, capture.Tone),
	}
	canonicalObservation, err := json.Marshal(observation)
	if err != nil {
		return CapturedWebChoice{}, validationError("canonicalization_failed", "web_choice_observation", "/")
	}
	observationDigest := sha256.Sum256(canonicalObservation)
	observationSHA256 := hex.EncodeToString(observationDigest[:])

	var dirtyContentSHA256 *string
	if binding.DirtyContentSHA256 != "" {
		dirty := binding.DirtyContentSHA256
		dirtyContentSHA256 = &dirty
	}
	manifest := core.ManifestV1{
		Schema:                  core.SchemaV1,
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      dirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          route.CanonicalRoute,
		ResolvedTone:            capture.Tone,
		Protocol:                "m365_web_outbound",
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
		MappingEvidence:         "web_payload_verified",
		IdentityStatus:          route.IdentityStatus,
		CapabilityID:            "route_mapping",
		Classification:          core.ClassificationVerified,
		TestExecutionStatus:     core.TestExecutionPass,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return CapturedWebChoice{}, validationError("canonicalization_failed", "wp2_evidence_manifest", "/")
	}
	evidenceRecord, err := core.ValidateCapabilityEvidence(manifestJSON, core.IdentitySet{
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      binding.DirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          route.CanonicalRoute,
		ResolvedTone:            capture.Tone,
		Protocol:                "m365_web_outbound",
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
	})
	if err != nil {
		return CapturedWebChoice{}, err
	}

	return CapturedWebChoice{
		Observation:              observation,
		ObservationCanonicalJSON: append([]byte(nil), canonicalObservation...),
		ObservationSHA256:        observationSHA256,
		Evidence:                 evidenceRecord,
	}, nil
}

func validateWebChoiceRoute(route WebChoiceRoute) error {
	if !routePattern.MatchString(route.WebChoiceID) || !routePattern.MatchString(route.CanonicalRoute) {
		return validationError("invalid_mapping", "stable_web_choice_route", "/web_choice_id")
	}
	if route.WebChoiceID != route.CanonicalRoute {
		return validationError("invalid_mapping", "primary_web_choice", "/canonical_route")
	}
	if !tonePattern.MatchString(route.RegistryTone) {
		return validationError("invalid_mapping", "registry_tone", "/registry_tone")
	}
	if route.RouteKind != "web_mode" && route.RouteKind != "web_model_route" {
		return validationError("invalid_enum", "web_choice_route_kind", "/route_kind")
	}
	if _, ok := allowedIdentityStatus[route.IdentityStatus]; !ok {
		return validationError("invalid_enum", "identity_status", "/identity_status")
	}
	return nil
}

func mappingBehavior(registryTone, observedTone string) MappingBehavior {
	if registryTone == observedTone {
		return MappingBehaviorExact
	}
	if strings.EqualFold(registryTone, observedTone) {
		return MappingBehaviorCaseNormalized
	}
	return MappingBehaviorDifferent
}
