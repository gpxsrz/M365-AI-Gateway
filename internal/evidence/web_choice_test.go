package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func webChoiceBinding() CaptureBinding {
	return CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "1a92a96ea940882b897a9dec17bc38c9f33e8586",
		DirtyContentSHA256:      strings.Repeat("1", 64),
		BinarySHA256:            strings.Repeat("2", 64),
		HarnessSHA256:           strings.Repeat("3", 64),
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: strings.Repeat("5", 64),
	}
}

func webChoiceRaw(tone string) []byte {
	return []byte(fmt.Sprintf(`{"schema":%q,"tone":%q}`, WebChoiceCaptureSchemaV1, tone))
}

func TestCaptureWebChoiceAcceptsFiveObservedMappings(t *testing.T) {
	tests := []struct {
		name         string
		route        WebChoiceRoute
		observedTone string
		behavior     MappingBehavior
	}{
		{
			name: "auto",
			route: WebChoiceRoute{
				WebChoiceID: "m365-auto", CanonicalRoute: "m365-auto", RegistryTone: "magic",
				RouteKind: "web_mode", IdentityStatus: "dynamic_unidentified",
			},
			observedTone: "Magic", behavior: MappingBehaviorCaseNormalized,
		},
		{
			name: "quick response",
			route: WebChoiceRoute{
				WebChoiceID: "quick", CanonicalRoute: "quick", RegistryTone: "Gpt_Quick",
				RouteKind: "web_mode", IdentityStatus: "accepted_unverified",
			},
			observedTone: "Chat", behavior: MappingBehaviorDifferent,
		},
		{
			name: "think deeper",
			route: WebChoiceRoute{
				WebChoiceID: "think-deeper", CanonicalRoute: "think-deeper", RegistryTone: "Gpt_Reasoning",
				RouteKind: "web_mode", IdentityStatus: "accepted_unverified",
			},
			observedTone: "Reasoning", behavior: MappingBehaviorDifferent,
		},
		{
			name: "gpt 5.6 think deeper",
			route: WebChoiceRoute{
				WebChoiceID: "m365-gpt-5.6-think-deeper", CanonicalRoute: "m365-gpt-5.6-think-deeper", RegistryTone: "Gpt_5_6_Reasoning",
				RouteKind: "web_model_route", IdentityStatus: "accepted_unverified",
			},
			observedTone: "Gpt_5_6_Reasoning", behavior: MappingBehaviorExact,
		},
		{
			name: "gpt 5.5 quick response",
			route: WebChoiceRoute{
				WebChoiceID: "m365-gpt-5.5-quick-response", CanonicalRoute: "m365-gpt-5.5-quick-response", RegistryTone: "Gpt_5_5_Chat",
				RouteKind: "web_model_route", IdentityStatus: "accepted_unverified",
			},
			observedTone: "Gpt_5_5_Chat", behavior: MappingBehaviorExact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CaptureWebChoice(webChoiceRaw(tt.observedTone), tt.route, webChoiceBinding())
			if err != nil {
				t.Fatalf("CaptureWebChoice() error = %v", err)
			}
			if got.Observation.Schema != WebChoiceObservationSchemaV1 ||
				got.Observation.WebChoiceID != tt.route.WebChoiceID ||
				got.Observation.CanonicalRoute != tt.route.CanonicalRoute ||
				got.Observation.RouteKind != tt.route.RouteKind ||
				got.Observation.RegistryTone != tt.route.RegistryTone ||
				got.Observation.ObservedWebTone != tt.observedTone ||
				got.Observation.MappingBehavior != tt.behavior {
				t.Fatalf("observation = %#v", got.Observation)
			}
			digest := sha256.Sum256(got.ObservationCanonicalJSON)
			if got.ObservationSHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("observation checksum = %q, want hash of canonical bytes", got.ObservationSHA256)
			}
			manifest := got.Evidence.Manifest
			if manifest.ObservationSHA256 != got.ObservationSHA256 ||
				manifest.CanonicalRoute != tt.route.CanonicalRoute ||
				manifest.ResolvedTone != tt.observedTone ||
				manifest.Protocol != "m365_web_outbound" ||
				manifest.MappingEvidence != "web_payload_verified" ||
				manifest.IdentityStatus != tt.route.IdentityStatus ||
				manifest.CapabilityID != "route_mapping" ||
				manifest.Classification != ClassificationVerified ||
				manifest.TestExecutionStatus != TestExecutionPass {
				t.Fatalf("evidence manifest = %#v", manifest)
			}
		})
	}
}

func TestCaptureWebChoiceIsDeterministicAndDoesNotOverwrite(t *testing.T) {
	route := WebChoiceRoute{
		WebChoiceID: "quick", CanonicalRoute: "quick", RegistryTone: "Gpt_Quick",
		RouteKind: "web_mode", IdentityStatus: "accepted_unverified",
	}
	first, err := CaptureWebChoice(webChoiceRaw("Chat"), route, webChoiceBinding())
	if err != nil {
		t.Fatal(err)
	}
	second, err := CaptureWebChoice([]byte("{\n  \"tone\":\"Chat\",\n  \"schema\":\"m365-wp2-web-choice-capture/v1\"\n}"), route, webChoiceBinding())
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservationSHA256 != second.ObservationSHA256 ||
		first.Evidence.ChecksumSHA256 != second.Evidence.ChecksumSHA256 ||
		string(first.ObservationCanonicalJSON) != string(second.ObservationCanonicalJSON) ||
		string(first.Evidence.CanonicalJSON) != string(second.Evidence.CanonicalJSON) {
		t.Fatalf("equivalent captures differ\nfirst=%#v\nsecond=%#v", first, second)
	}

	first.ObservationCanonicalJSON[0] = 'X'
	first.Evidence.CanonicalJSON[0] = 'X'
	if second.ObservationCanonicalJSON[0] != '{' || second.Evidence.CanonicalJSON[0] != '{' {
		t.Fatal("capture results share mutable canonical byte storage")
	}
}

func TestCaptureWebChoiceChangedSettingsProduceNewEvidenceIdentity(t *testing.T) {
	route := WebChoiceRoute{
		WebChoiceID: "m365-gpt-5.5-quick-response", CanonicalRoute: "m365-gpt-5.5-quick-response", RegistryTone: "Gpt_5_5_Chat",
		RouteKind: "web_model_route", IdentityStatus: "accepted_unverified",
	}
	firstBinding := webChoiceBinding()
	first, err := CaptureWebChoice(webChoiceRaw("Gpt_5_5_Chat"), route, firstBinding)
	if err != nil {
		t.Fatal(err)
	}
	secondBinding := firstBinding
	secondBinding.EffectiveSettingsSHA256 = strings.Repeat("a", 64)
	second, err := CaptureWebChoice(webChoiceRaw("Gpt_5_5_Chat"), route, secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	if first.ObservationSHA256 != second.ObservationSHA256 {
		t.Fatal("effective settings changed the minimized observation identity")
	}
	if first.Evidence.ChecksumSHA256 == second.Evidence.ChecksumSHA256 {
		t.Fatal("changed effective settings did not change evidence identity")
	}
}

func TestCaptureWebChoiceRejectsMissingUnexpectedAndAmbiguousMetadata(t *testing.T) {
	route := WebChoiceRoute{
		WebChoiceID: "m365-auto", CanonicalRoute: "m365-auto", RegistryTone: "magic",
		RouteKind: "web_mode", IdentityStatus: "dynamic_unidentified",
	}
	tests := []struct {
		name string
		raw  string
		code string
		rule string
	}{
		{"missing tone", `{"schema":"m365-wp2-web-choice-capture/v1"}`, "missing_field", "required_binding"},
		{"unexpected field", `{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic","unexpected":true}`, "unknown_field", "closed_schema"},
		{"duplicate tone", `{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic","tone":"Chat"}`, "duplicate_key", "closed_json"},
		{"wrong schema", `{"schema":"m365-wp2-web-choice-capture/v2","tone":"Magic"}`, "invalid_schema", "versioned_schema"},
		{"malformed tone", `{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic/../../"}`, "invalid_tone", "observed_web_tone"},
		{"complete frame", `{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic","arguments":[{"tone":"Magic"}]}`, "privacy_forbidden", "complete_frame"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CaptureWebChoice([]byte(tt.raw), route, webChoiceBinding())
			assertWebChoiceValidationError(t, err, tt.code, tt.rule)
		})
	}
}

func TestCaptureWebChoiceRejectsSensitiveMetadataWithoutEcho(t *testing.T) {
	route := WebChoiceRoute{
		WebChoiceID: "m365-auto", CanonicalRoute: "m365-auto", RegistryTone: "magic",
		RouteKind: "web_mode", IdentityStatus: "dynamic_unidentified",
	}
	tests := []struct {
		field string
		rule  string
	}{
		{`"token":"DO_NOT_ECHO_3d91",`, "token"},
		{`"authorization":"DO_NOT_ECHO_3d91",`, "token"},
		{`"cookie":"DO_NOT_ECHO_3d91",`, "cookie"},
		{`"api_key":"DO_NOT_ECHO_3d91",`, "api_key"},
		{`"oauth_secret":"DO_NOT_ECHO_3d91",`, "oauth_secret"},
		{`"prompt":"DO_NOT_ECHO_3d91",`, "prompt"},
		{`"request_body":"DO_NOT_ECHO_3d91",`, "request_body"},
		{`"response_body":"DO_NOT_ECHO_3d91",`, "response_body"},
		{`"complete_frame":{"private":"DO_NOT_ECHO_3d91"},`, "complete_frame"},
		{`"messages":[{"content":"DO_NOT_ECHO_3d91"}],`, "conversation_content"},
		{`"email":"person@example.com",`, "email"},
		{`"oid":"00000000-0000-0000-0000-000000000000",`, "object_id"},
		{`"tid":"00000000-0000-0000-0000-000000000000",`, "tenant_id"},
	}
	for _, tt := range tests {
		raw := "{" + tt.field + `"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic"}`
		_, err := CaptureWebChoice([]byte(raw), route, webChoiceBinding())
		assertWebChoiceValidationError(t, err, "privacy_forbidden", tt.rule)
		if strings.Contains(err.Error(), "DO_NOT_ECHO_3d91") || strings.Contains(err.Error(), "person@example.com") {
			t.Fatalf("validation error exposed rejected input: %v", err)
		}
	}
}

func TestCaptureWebChoiceRejectsInvalidRouteAndAccountBinding(t *testing.T) {
	validRoute := WebChoiceRoute{
		WebChoiceID: "m365-auto", CanonicalRoute: "m365-auto", RegistryTone: "magic",
		RouteKind: "web_mode", IdentityStatus: "dynamic_unidentified",
	}

	invalidRoute := validRoute
	invalidRoute.WebChoiceID = "gpt-5.5"
	invalidRoute.CanonicalRoute = "m365-gpt-5.5-quick-response"
	_, err := CaptureWebChoice(webChoiceRaw("Gpt_5_5_Chat"), invalidRoute, webChoiceBinding())
	assertWebChoiceValidationError(t, err, "invalid_mapping", "primary_web_choice")

	invalidKind := validRoute
	invalidKind.RouteKind = "alias"
	_, err = CaptureWebChoice(webChoiceRaw("Magic"), invalidKind, webChoiceBinding())
	assertWebChoiceValidationError(t, err, "invalid_enum", "web_choice_route_kind")

	invalidIdentityStatus := validRoute
	invalidIdentityStatus.IdentityStatus = "unknown"
	_, err = CaptureWebChoice(webChoiceRaw("Magic"), invalidIdentityStatus, webChoiceBinding())
	assertWebChoiceValidationError(t, err, "invalid_enum", "identity_status")

	invalidRegistryTone := validRoute
	invalidRegistryTone.RegistryTone = "Magic/../../"
	_, err = CaptureWebChoice(webChoiceRaw("Magic"), invalidRegistryTone, webChoiceBinding())
	assertWebChoiceValidationError(t, err, "invalid_mapping", "registry_tone")

	invalidBinding := webChoiceBinding()
	invalidBinding.AccountProfileRef = "person@example.com"
	_, err = CaptureWebChoice(webChoiceRaw("Magic"), validRoute, invalidBinding)
	assertWebChoiceValidationError(t, err, "invalid_identity", "opaque_account_profile")
}

func assertWebChoiceValidationError(t *testing.T, err error, code, rule string) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Code != code || validationErr.Rule != rule {
		t.Fatalf("error = %#v, want code=%q rule=%q", validationErr, code, rule)
	}
}
