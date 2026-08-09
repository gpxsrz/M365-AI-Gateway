package evidence

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func aliasProjectionTestBinding() CaptureBinding {
	return CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "753d994b821ff450681141934cdabceb56c83b51",
		BinarySHA256:            strings.Repeat("1", 64),
		HarnessSHA256:           strings.Repeat("2", 64),
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: strings.Repeat("3", 64),
	}
}

func aliasProjectionSuccessDescriptor() AliasProjectionDescriptor {
	return AliasProjectionDescriptor{
		RequestedIdentity:       "gpt-5.6-reasoning",
		CanonicalRoute:          "m365-gpt-5.6-think-deeper",
		ResolvedTone:            "Gpt_5_6_Reasoning",
		RouteKind:               "alias",
		OperationalStatus:       "enabled",
		RuntimeMappingEvidence:  "api_tone_accepted",
		AcceptedMappingEvidence: "web_payload_verified",
		IdentityStatus:          "accepted_unverified",
		CatalogVisibility:       "compatibility",
		CompatibilityRequired:   true,
		DefaultReasoningLevel:   "medium",
		Protocol:                "openai_chat_completions_nonstream",
		EndpointPath:            "/v1/chat/completions",
		AuthMode:                "api_key",
		Effort:                  "high",
		ReasoningEffortApplied:  true,
		ReasoningEffortIgnored:  true,
	}
}

func aliasProjectionSuccessCapture() AliasProjectionCaptureV1 {
	return AliasProjectionCaptureV1{
		Schema:                  AliasProjectionCaptureSchemaV1,
		CaseID:                  AliasProjectionCaseSuccess,
		RequestedIdentity:       "gpt-5.6-reasoning",
		TopLevelModel:           "gpt-5.6-reasoning",
		MetadataRequestedModel:  "gpt-5.6-reasoning",
		MetadataResponseModel:   "gpt-5.6-reasoning",
		RouteMetadataComplete:   true,
		CanonicalRoute:          "m365-gpt-5.6-think-deeper",
		ResolvedTone:            "Gpt_5_6_Reasoning",
		RouteKind:               "alias",
		OperationalStatus:       "enabled",
		MappingEvidence:         "api_tone_accepted",
		IdentityStatus:          "accepted_unverified",
		CatalogVisibility:       "compatibility",
		AliasUsed:               true,
		CompatibilityRequired:   true,
		DefaultReasoningLevel:   "medium",
		RemovalDateAbsent:       true,
		Protocol:                "openai_chat_completions_nonstream",
		EndpointPath:            "/v1/chat/completions",
		AuthMode:                "api_key",
		Effort:                  "high",
		ReasoningEffortApplied:  true,
		ReasoningEffortIgnored:  true,
		HTTPStatus:              200,
		BasicTextDelivered:      true,
		UpstreamAttempts:        1,
		RequestIDObserved:       true,
		SecurityHeadersObserved: true,
	}
}

func TestCaptureAliasProjectionBindsCanonicalCapabilities(t *testing.T) {
	capture := aliasProjectionSuccessCapture()
	raw, _ := json.Marshal(capture)
	got, err := CaptureAliasProjection(raw, aliasProjectionSuccessDescriptor(), aliasProjectionTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.Schema != AliasProjectionObservationSchemaV1 || got.ObservationSHA256 == "" || len(got.Capabilities) != 4 {
		t.Fatalf("record=%#v", got)
	}
	if got.Observation.RequestedIdentity != "gpt-5.6-reasoning" || got.Observation.TopLevelModel != "gpt-5.6-reasoning" || got.Observation.MetadataRequestedModel != "gpt-5.6-reasoning" || got.Observation.MetadataResponseModel != "gpt-5.6-reasoning" || !got.Observation.RouteMetadataComplete || got.Observation.FallbackUsed || got.Observation.ConfiguredMapping || got.Observation.CanonicalRoute != "m365-gpt-5.6-think-deeper" || got.Observation.IdentityStatus != "accepted_unverified" {
		t.Fatalf("observation=%#v", got.Observation)
	}
	for _, capability := range got.Capabilities {
		manifest := capability.Evidence.Manifest
		if manifest.CanonicalRoute != "m365-gpt-5.6-think-deeper" || manifest.ResolvedTone != "Gpt_5_6_Reasoning" || manifest.MappingEvidence != "web_payload_verified" || manifest.IdentityStatus != "accepted_unverified" || manifest.ObservationSHA256 != got.ObservationSHA256 || manifest.Classification != ClassificationVerified || manifest.TestExecutionStatus != TestExecutionPass {
			t.Fatalf("capability=%#v", capability)
		}
	}

	var reordered map[string]any
	if err := json.Unmarshal(raw, &reordered); err != nil {
		t.Fatal(err)
	}
	reorderedRaw, _ := json.MarshalIndent(reordered, "", "  ")
	second, err := CaptureAliasProjection(reorderedRaw, aliasProjectionSuccessDescriptor(), aliasProjectionTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservationSHA256 != second.ObservationSHA256 || !bytes.Equal(got.ObservationJSON, second.ObservationJSON) {
		t.Fatal("alias projection observation is not deterministic")
	}
}

func TestCaptureAliasProjectionAcceptsCatalogAndFailClosedCases(t *testing.T) {
	catalogDescriptor := aliasProjectionSuccessDescriptor()
	catalogDescriptor.Protocol = "openai_models_catalog"
	catalogDescriptor.EndpointPath = "/v1/models"
	catalogDescriptor.Effort = "not_applicable"
	catalogDescriptor.ReasoningEffortApplied = false
	catalogDescriptor.ReasoningEffortIgnored = false
	catalogDescriptor.ListedInCatalog = true
	catalog := aliasProjectionSuccessCapture()
	catalog.CaseID = AliasProjectionCaseCatalog
	catalog.TopLevelModel = ""
	catalog.MetadataRequestedModel = ""
	catalog.MetadataResponseModel = ""
	catalog.RouteMetadataComplete = false
	catalog.Protocol = "openai_models_catalog"
	catalog.EndpointPath = "/v1/models"
	catalog.Effort = "not_applicable"
	catalog.ReasoningEffortApplied = false
	catalog.ReasoningEffortIgnored = false
	catalog.BasicTextDelivered = false
	catalog.UpstreamAttempts = 0
	catalog.ListedInCatalog = true
	raw, _ := json.Marshal(catalog)
	got, err := CaptureAliasProjection(raw, catalogDescriptor, aliasProjectionTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.CaseID != AliasProjectionCaseCatalog || !got.Observation.ListedInCatalog || len(got.Capabilities) != 0 {
		t.Fatalf("catalog=%#v", got)
	}

	for _, tc := range []struct {
		model, code string
		caseID      AliasProjectionCase
	}{
		{model: "wp2-unknown-alias", code: "model_not_found", caseID: AliasProjectionCaseUnknownRoute},
		{model: "quick", code: "model_unavailable", caseID: AliasProjectionCaseDisabledRoute},
	} {
		capture := AliasProjectionCaptureV1{
			Schema: AliasProjectionCaptureSchemaV1, CaseID: tc.caseID, RequestedIdentity: tc.model,
			Protocol: "openai_chat_completions_nonstream", EndpointPath: "/v1/chat/completions", AuthMode: "api_key", Effort: "omitted",
			HTTPStatus: 404, RequestIDObserved: true, SecurityHeadersObserved: true, FailureCode: tc.code,
		}
		descriptor := AliasProjectionDescriptor{Protocol: capture.Protocol, EndpointPath: capture.EndpointPath, AuthMode: capture.AuthMode, Effort: capture.Effort}
		raw, _ := json.Marshal(capture)
		got, err := CaptureAliasProjection(raw, descriptor, aliasProjectionTestBinding())
		if err != nil {
			t.Fatalf("case=%s: %v", tc.caseID, err)
		}
		if got.Observation.FailureCode != tc.code || got.Observation.UpstreamAttempts != 0 || len(got.Capabilities) != 0 {
			t.Fatalf("negative=%#v", got)
		}
	}
}

func TestCaptureAliasProjectionRejectsPrivacyAndCompatibilityDrift(t *testing.T) {
	validCapture := aliasProjectionSuccessCapture()
	validRaw, _ := json.Marshal(validCapture)
	invalidRaw := []string{
		strings.TrimSuffix(string(validRaw), "}") + `,"prompt":"secret"}`,
		strings.Replace(string(validRaw), `"identity_status":"accepted_unverified"`, `"identity_status":"upstream_identity_verified"`, 1),
		strings.Replace(string(validRaw), `"per_key_restricted":false`, `"per_key_restricted":true`, 1),
		strings.Replace(string(validRaw), `"request_id_observed":true`, `"request_id_observed":false`, 1),
		strings.Replace(string(validRaw), `"security_headers_observed":true`, `"security_headers_observed":false`, 1),
		strings.Replace(string(validRaw), `"top_level_model":"gpt-5.6-reasoning"`, `"top_level_model":"m365-gpt-5.6-think-deeper"`, 1),
		strings.Replace(string(validRaw), `"metadata_requested_model":"gpt-5.6-reasoning"`, `"metadata_requested_model":"m365-gpt-5.6-think-deeper"`, 1),
		strings.Replace(string(validRaw), `"metadata_response_model":"gpt-5.6-reasoning"`, `"metadata_response_model":"m365-gpt-5.6-think-deeper"`, 1),
		strings.Replace(string(validRaw), `"route_metadata_complete":true`, `"route_metadata_complete":false`, 1),
		strings.Replace(string(validRaw), `"fallback_used":false`, `"fallback_used":true`, 1),
		strings.Replace(string(validRaw), `"configured_mapping":false`, `"configured_mapping":true`, 1),
		strings.Replace(string(validRaw), `"canonical_route":"m365-gpt-5.6-think-deeper"`, `"canonical_route":"gpt-5.6-reasoning"`, 1),
		strings.Replace(string(validRaw), `"route_kind":"alias"`, `"route_kind":"web_model_route"`, 1),
		strings.Replace(string(validRaw), `"compatibility_required":true`, `"compatibility_required":false`, 1),
		strings.Replace(string(validRaw), `"reasoning_effort_ignored":true`, `"reasoning_effort_ignored":false`, 1),
	}
	for _, raw := range invalidRaw {
		if _, err := CaptureAliasProjection([]byte(raw), aliasProjectionSuccessDescriptor(), aliasProjectionTestBinding()); err == nil {
			t.Fatalf("invalid capture accepted: %s", raw)
		}
	}
}
