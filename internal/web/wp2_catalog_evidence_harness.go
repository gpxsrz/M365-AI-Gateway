package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"

	committedevidence "m365-native/docs/wp2/evidence"
	"m365-native/internal/evidence"
)

type WP2CatalogEvidenceHarnessOptions struct {
	Binding evidence.CaptureBinding
}

type wp2CatalogEffectiveSettingsV1 struct {
	Schema          string         `json:"schema"`
	ContextWindow   int            `json:"context_window"`
	MaxOutputTokens int            `json:"max_output_tokens"`
	ModelMappings   []modelMapping `json:"model_mappings"`
}

var wp2CatalogSelectedModels = []string{
	"claude",
	"gpt-5.2",
	"gpt-5.6-sol",
	"m365-auto",
	"m365-copilot",
	"m365-gpt-5.6-think-deeper",
}

var wp2CatalogHiddenIdentities = []string{
	"claude",
	"gpt-5.3-think-deeper",
	"gpt-5.4-quick",
	"quick",
	"think-deeper",
}

var wp2CatalogAllowedClaimCapabilities = map[string]struct{}{
	"route_identity":      {},
	"route_mapping":       {},
	"basic_text_delivery": {},
	"protocol_transport":  {},
}

func WP2CatalogHarnessEffectiveSettingsSHA256() string {
	settings := wp2CatalogHarnessSettings()
	projection := wp2CatalogEffectiveSettingsV1{
		Schema:          "m365-wp2-catalog-effective-settings/v1",
		ContextWindow:   settings.ContextWindow,
		MaxOutputTokens: settings.MaxOutputTokens,
		ModelMappings:   append([]modelMapping(nil), settings.ModelMappings...),
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		panic(err)
	}
	return wp2CatalogSHA256(raw)
}

func BuildWP2CatalogEvidenceSet(options WP2CatalogEvidenceHarnessOptions) (evidence.CatalogEvidenceSetV1, error) {
	if err := evidence.ValidateCatalogEvidenceBinding(options.Binding); err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}
	if options.Binding.DirtyContentSHA256 != "" {
		return evidence.CatalogEvidenceSetV1{}, fmt.Errorf("catalog evidence requires a clean source commit")
	}
	settings := wp2CatalogHarnessSettings()
	settingsSHA := WP2CatalogHarnessEffectiveSettingsSHA256()
	if options.Binding.EffectiveSettingsSHA256 != settingsSHA {
		return evidence.CatalogEvidenceSetV1{}, fmt.Errorf("catalog evidence effective settings identity mismatch")
	}

	manifestRaw, expected, err := BuildAcceptedWP2CatalogProjectionFromFS(committedevidence.AcceptedArtifacts(), ".")
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, fmt.Errorf("build accepted catalog manifest: %w", err)
	}
	validated, err := evidence.ValidateCatalogProjectionManifest(manifestRaw, expected)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, fmt.Errorf("validate accepted catalog manifest: %w", err)
	}
	if options.Binding.NormativeADRSHA256 != validated.Manifest.Packages[0].NormativeADRSHA256 {
		return evidence.CatalogEvidenceSetV1{}, fmt.Errorf("catalog evidence normative ADR identity mismatch")
	}

	baseline, err := runWP2CatalogHTTPObservation(evidence.CatalogEvidenceCaseNoManifest, settings, nil)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}
	acceptedProjection, err := bindCatalogEvidence(settings, validated)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}
	accepted, err := runWP2CatalogHTTPObservation(evidence.CatalogEvidenceCaseAccepted, settings, acceptedProjection)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}
	driftSettings := settings
	driftSettings.ModelMappings = append(append([]modelMapping(nil), settings.ModelMappings...), modelMapping{
		PublicModel: "gpt-5.2", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Runtime override", DefaultReasoningLevel: "low",
	})
	drift, err := runWP2CatalogHTTPObservation(evidence.CatalogEvidenceCaseRuntimeMappingDrift, driftSettings, acceptedProjection)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}

	profileSet := ""
	for _, pkg := range validated.Manifest.Packages {
		if pkg.Issue == 7 {
			profileSet = pkg.ProfileSetSHA256
			break
		}
	}
	supportingEvidenceCount := 0
	for _, identity := range validated.Manifest.Identities {
		supportingEvidenceCount += len(identity.SupportingEvidenceSHA256)
	}
	accountDependentGlobalClaims := 0
	for _, claim := range validated.Manifest.GlobalClaims {
		if claim.AccountDependent {
			accountDependentGlobalClaims++
		}
	}
	rejections, err := catalogStaleManifestRejections(manifestRaw, expected)
	if err != nil {
		return evidence.CatalogEvidenceSetV1{}, err
	}

	return evidence.CatalogEvidenceSetV1{
		Schema:                                  evidence.CatalogEvidenceSetSchemaV1,
		NormativeADRSHA256:                      options.Binding.NormativeADRSHA256,
		SourceHead:                              options.Binding.SourceHead,
		DirtyContentSHA256:                      options.Binding.DirtyContentSHA256,
		BinarySHA256:                            options.Binding.BinarySHA256,
		HarnessSHA256:                           options.Binding.HarnessSHA256,
		EffectiveSettingsSHA256:                 settingsSHA,
		AcceptedManifestSHA256:                  validated.ChecksumSHA256,
		AcceptedManifestBytes:                   len(manifestRaw),
		AcceptedManifest:                        append(json.RawMessage(nil), manifestRaw...),
		AcceptedIdentityCount:                   len(validated.Manifest.Identities),
		AcceptedIdentitySupportingEvidenceCount: supportingEvidenceCount,
		GlobalClaimCount:                        len(validated.Manifest.GlobalClaims),
		AccountDependentGlobalClaimCount:        accountDependentGlobalClaims,
		ProfileSetSHA256:                        profileSet,
		HTTPObservations:                        []evidence.CatalogHTTPObservationV1{baseline, accepted, drift},
		StaleManifestRejections:                 rejections,
	}, nil
}

func wp2CatalogHarnessSettings() runtimeSettings {
	return runtimeSettings{
		MaxToolCallsPerTurn: 1,
		MaxToolRounds:       16,
		ContextWindow:       128000,
		MaxOutputTokens:     16384,
		ChatTimeoutSeconds:  120,
		ImageTimeoutSeconds: 150,
		LogLevel:            "info",
		ModelMappings:       append([]modelMapping(nil), defaultModelMappings...),
		ToolPlanningMode:    "code_mode_only",
	}
}

func runWP2CatalogHTTPObservation(caseID evidence.CatalogEvidenceCase, settings runtimeSettings, projection *catalogEvidenceProjection) (evidence.CatalogHTTPObservationV1, error) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServerWithSettings(&wp2HarnessChat{}, settings)
	if err != nil {
		return evidence.CatalogHTTPObservationV1{}, fmt.Errorf("create catalog HTTP harness: %w", err)
	}
	defer cleanup()
	harness.server.catalogEvidence = projection

	writer := httptest.NewRecorder()
	harness.serveWithAuth("api_key", writer, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if writer.Code != http.StatusOK {
		return evidence.CatalogHTTPObservationV1{}, fmt.Errorf("catalog HTTP status=%d", writer.Code)
	}
	var body struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
		return evidence.CatalogHTTPObservationV1{}, fmt.Errorf("decode catalog HTTP response: %w", err)
	}
	standardSHA, err := wp2CatalogNormalizedSHA256(body.Data, true)
	if err != nil {
		return evidence.CatalogHTTPObservationV1{}, err
	}
	normalizedSHA, err := wp2CatalogNormalizedSHA256(body.Data, false)
	if err != nil {
		return evidence.CatalogHTTPObservationV1{}, err
	}

	byID := make(map[string]map[string]any, len(body.Data))
	claimCapabilitySet := map[string]struct{}{}
	advancedCapabilitySet := map[string]struct{}{}
	visiblePresets := make([]string, 0)
	acceptedEvidenceModels := 0
	accountDependentModels := 0
	protocolClaims := 0
	verifiedProtocolClaims := 0
	accountDependentProtocols := 0
	for _, model := range body.Data {
		id, _ := model["id"].(string)
		byID[id] = model
		if model["x_m365_evidence_source"] == "accepted_evidence" {
			acceptedEvidenceModels++
		}
		if dependent, _ := model["account_dependent"].(bool); dependent {
			accountDependentModels++
		}
		if model["route_kind"] == "preset" {
			visiblePresets = append(visiblePresets, id)
		}
		claims, _ := model["x_m365_protocol_claims"].([]any)
		for _, value := range claims {
			claim, _ := value.(map[string]any)
			protocolClaims++
			classification, _ := claim["route_eligibility"].(string)
			dependent, _ := claim["account_dependent"].(bool)
			if classification == string(evidence.ClassificationVerified) && !dependent {
				verifiedProtocolClaims++
			}
			if dependent {
				accountDependentProtocols++
			}
			capabilities, _ := claim["capabilities"].([]any)
			for _, capabilityValue := range capabilities {
				capability, _ := capabilityValue.(map[string]any)
				capabilityID, _ := capability["capability_id"].(string)
				if capabilityID == "" {
					continue
				}
				claimCapabilitySet[capabilityID] = struct{}{}
				if _, allowed := wp2CatalogAllowedClaimCapabilities[capabilityID]; !allowed {
					advancedCapabilitySet[capabilityID] = struct{}{}
				}
			}
		}
	}
	sort.Strings(visiblePresets)
	hiddenLeaks := make([]string, 0)
	for _, id := range wp2CatalogHiddenIdentities {
		if _, exists := byID[id]; exists {
			hiddenLeaks = append(hiddenLeaks, id)
		}
	}
	selected := make([]evidence.CatalogModelObservationV1, 0, len(wp2CatalogSelectedModels))
	for _, id := range wp2CatalogSelectedModels {
		selected = append(selected, wp2CatalogModelObservation(id, byID[id]))
	}
	return evidence.CatalogHTTPObservationV1{
		CaseID:                        caseID,
		HTTPStatus:                    writer.Code,
		CatalogEntryCount:             len(body.Data),
		DataModelsAliasesEqual:        reflect.DeepEqual(body.Data, body.Models),
		StandardCatalogSHA256:         standardSHA,
		NormalizedCatalogSHA256:       normalizedSHA,
		AcceptedEvidenceModelCount:    acceptedEvidenceModels,
		AccountDependentModelCount:    accountDependentModels,
		ProtocolClaimCount:            protocolClaims,
		VerifiedProtocolClaimCount:    verifiedProtocolClaims,
		AccountDependentProtocolCount: accountDependentProtocols,
		ClaimCapabilityIDs:            wp2SortedSet(claimCapabilitySet),
		AdvancedCapabilitiesPromoted:  wp2SortedSet(advancedCapabilitySet),
		VisiblePresets:                visiblePresets,
		HiddenIdentityLeaks:           hiddenLeaks,
		SelectedModels:                selected,
	}, nil
}

func wp2CatalogModelObservation(id string, model map[string]any) evidence.CatalogModelObservationV1 {
	observation := evidence.CatalogModelObservationV1{ID: id, Present: model != nil, Protocols: []evidence.CatalogProtocolObservationV1{}}
	if model == nil {
		return observation
	}
	observation.CanonicalRoute, _ = model["canonical_route"].(string)
	observation.ResolvedTone, _ = model["resolved_tone"].(string)
	observation.RouteKind, _ = model["route_kind"].(string)
	observation.CatalogVisibility, _ = model["catalog_visibility"].(string)
	observation.EvidenceSource, _ = model["x_m365_evidence_source"].(string)
	observation.MappingSource, _ = model["x_m365_mapping_source"].(string)
	observation.ProtocolSource, _ = model["x_m365_protocol_source"].(string)
	observation.AccountDependent, _ = model["account_dependent"].(bool)
	claims, _ := model["x_m365_protocol_claims"].([]any)
	for _, value := range claims {
		claim, _ := value.(map[string]any)
		protocol := evidence.CatalogProtocolObservationV1{Capabilities: []evidence.CatalogCapabilityObservationV1{}}
		protocol.Protocol, _ = claim["protocol"].(string)
		classification, _ := claim["route_eligibility"].(string)
		protocol.RouteEligibility = evidence.Classification(classification)
		protocol.AccountDependent, _ = claim["account_dependent"].(bool)
		capabilities, _ := claim["capabilities"].([]any)
		for _, capabilityValue := range capabilities {
			capability, _ := capabilityValue.(map[string]any)
			capabilityID, _ := capability["capability_id"].(string)
			capabilityClassification, _ := capability["classification"].(string)
			protocol.Capabilities = append(protocol.Capabilities, evidence.CatalogCapabilityObservationV1{
				CapabilityID: capabilityID, Classification: evidence.Classification(capabilityClassification),
			})
		}
		observation.Protocols = append(observation.Protocols, protocol)
	}
	return observation
}

func wp2CatalogNormalizedSHA256(models []map[string]any, standardOnly bool) (string, error) {
	normalized := make([]map[string]any, 0, len(models))
	for _, model := range models {
		raw, err := json.Marshal(model)
		if err != nil {
			return "", fmt.Errorf("marshal catalog model: %w", err)
		}
		clone := map[string]any{}
		if err := json.Unmarshal(raw, &clone); err != nil {
			return "", fmt.Errorf("clone catalog model: %w", err)
		}
		delete(clone, "created")
		if standardOnly {
			delete(clone, "account_dependent")
			for key := range clone {
				if strings.HasPrefix(key, "x_m365_") {
					delete(clone, key)
				}
			}
		}
		normalized = append(normalized, clone)
	}
	sort.Slice(normalized, func(i, j int) bool {
		left, _ := normalized[i]["id"].(string)
		right, _ := normalized[j]["id"].(string)
		return left < right
	})
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal normalized catalog: %w", err)
	}
	return wp2CatalogSHA256(raw), nil
}

func catalogStaleManifestRejections(raw []byte, expected evidence.CatalogProjectionExpected) ([]evidence.CatalogStaleManifestRejection, error) {
	mutations := []struct {
		identity string
		mutate   func(*evidence.CatalogProjectionManifestV1)
	}{
		{identity: "source", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].SourceHead = strings.Repeat("a", 40)
		}},
		{identity: "binary", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].BinarySHA256 = strings.Repeat("a", 64)
		}},
		{identity: "harness", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].HarnessSHA256 = strings.Repeat("b", 64)
		}},
		{identity: "settings", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].EffectiveSettingsSHA256 = strings.Repeat("c", 64)
		}},
		{identity: "profile_set", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].ProfileSetSHA256 = strings.Repeat("d", 64)
		}},
	}
	rejections := make([]evidence.CatalogStaleManifestRejection, 0, len(mutations))
	for _, mutation := range mutations {
		var manifest evidence.CatalogProjectionManifestV1
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("decode accepted manifest for stale mutation: %w", err)
		}
		mutation.mutate(&manifest)
		candidate, err := json.Marshal(manifest)
		if err != nil {
			return nil, fmt.Errorf("marshal stale manifest mutation: %w", err)
		}
		_, validationErr := evidence.ValidateCatalogProjectionManifest(candidate, expected)
		rejection := evidence.CatalogStaleManifestRejection{Identity: mutation.identity, Rejected: validationErr != nil}
		var typed *evidence.ValidationError
		if errors.As(validationErr, &typed) {
			rejection.ErrorCode = typed.Code
			rejection.ErrorRule = typed.Rule
			rejection.ErrorPath = typed.Path
		}
		rejections = append(rejections, rejection)
	}
	return rejections, nil
}

func wp2SortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func wp2CatalogSHA256(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
