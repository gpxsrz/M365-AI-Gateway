package evidence

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func routeProtocolTestBinding() CaptureBinding {
	return CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "4120e4027cc9cb42c3fe51dd01de59af4d197984",
		BinarySHA256:            strings.Repeat("1", 64),
		HarnessSHA256:           strings.Repeat("2", 64),
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: strings.Repeat("3", 64),
	}
}

func routeProtocolTestDescriptor() RouteProtocolDescriptor {
	return RouteProtocolDescriptor{
		CanonicalRoute:  "m365-auto",
		ResolvedTone:    "magic",
		Protocol:        "openai_chat_completions_nonstream",
		EndpointPath:    "/v1/chat/completions",
		AuthMode:        "api_key",
		MappingEvidence: "web_payload_verified",
		IdentityStatus:  "dynamic_unidentified",
	}
}

func TestCaptureRouteProtocolCanonicalizesAndBindsCapabilities(t *testing.T) {
	capture := RouteProtocolCaptureV1{
		Schema:                  RouteProtocolCaptureSchemaV1,
		CaseID:                  RouteProtocolCaseSuccess,
		Run:                     1,
		RequestedModel:          "m365-auto",
		TopLevelModel:           "m365-auto",
		CanonicalRoute:          "m365-auto",
		ResolvedTone:            "magic",
		Protocol:                "openai_chat_completions_nonstream",
		EndpointPath:            "/v1/chat/completions",
		AuthMode:                "api_key",
		RequestIDObserved:       true,
		SecurityHeadersObserved: true,
		HTTPStatus:              200,
		BasicTextDelivered:      true,
		ReasoningEffortApplied:  true,
		ReasoningEffortIgnored:  true,
		UpstreamAttempts:        1,
		MeaningfulUpstreamEvent: "text",
	}
	raw, _ := json.Marshal(capture)
	got, err := CaptureRouteProtocol(raw, routeProtocolTestDescriptor(), routeProtocolTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation.Schema != RouteProtocolObservationSchemaV1 || got.ObservationSHA256 == "" || len(got.Capabilities) != 4 {
		t.Fatalf("got=%#v", got)
	}
	for _, capability := range got.Capabilities {
		if capability.Evidence.Manifest.ObservationSHA256 != got.ObservationSHA256 || capability.Evidence.Manifest.CanonicalRoute != "m365-auto" || capability.Evidence.Manifest.ResolvedTone != "magic" || capability.Evidence.Manifest.Protocol != "openai_chat_completions_nonstream" {
			t.Fatalf("capability=%#v", capability)
		}
	}

	var reordered map[string]any
	if err := json.Unmarshal(raw, &reordered); err != nil {
		t.Fatal(err)
	}
	reorderedRaw, _ := json.MarshalIndent(reordered, "", "  ")
	second, err := CaptureRouteProtocol(reorderedRaw, routeProtocolTestDescriptor(), routeProtocolTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservationSHA256 != second.ObservationSHA256 || !bytes.Equal(got.ObservationJSON, second.ObservationJSON) {
		t.Fatal("route/protocol observation is not deterministic")
	}
}

func TestCaptureRouteProtocolRejectsPrivacyFieldsAndInvalidAttemptSemantics(t *testing.T) {
	valid := `{"schema":"m365-wp2-route-protocol-capture/v1","case_id":"success","run":1,"requested_model":"m365-auto","top_level_model":"m365-auto","canonical_route":"m365-auto","resolved_tone":"magic","protocol":"openai_chat_completions_nonstream","endpoint_path":"/v1/chat/completions","auth_mode":"api_key","request_id_observed":true,"security_headers_observed":true,"http_status":200,"basic_text_delivered":true,"reasoning_effort_applied":true,"reasoning_effort_ignored":true,"upstream_attempts":1,"route_switches":0,"cross_account_resends":0,"meaningful_upstream_event":"text"}`
	for _, raw := range []string{
		strings.TrimSuffix(valid, "}") + `,"prompt":"secret"}`,
		strings.Replace(valid, `"upstream_attempts":1`, `"upstream_attempts":2`, 1),
		strings.Replace(valid, `"route_switches":0`, `"route_switches":1`, 1),
		strings.Replace(valid, `"cross_account_resends":0`, `"cross_account_resends":1`, 1),
		strings.Replace(valid, `"top_level_model":"m365-auto"`, `"top_level_model":"different"`, 1),
	} {
		if _, err := CaptureRouteProtocol([]byte(raw), routeProtocolTestDescriptor(), routeProtocolTestBinding()); err == nil {
			t.Fatalf("invalid capture accepted: %s", raw)
		}
	}
}

func TestCaptureRouteProtocolAcceptsStructuredFailClosedObservations(t *testing.T) {
	cases := []RouteProtocolCaptureV1{
		{
			Schema: RouteProtocolCaptureSchemaV1, CaseID: RouteProtocolCaseUpstreamEmpty, Run: 1,
			RequestedModel: "m365-auto", CanonicalRoute: "m365-auto", ResolvedTone: "magic", Protocol: "openai_chat_completions_nonstream",
			EndpointPath: "/v1/chat/completions", AuthMode: "api_key", RequestIDObserved: true, SecurityHeadersObserved: true,
			HTTPStatus: 502, UpstreamAttempts: 1, MeaningfulUpstreamEvent: "none", FailureCode: "upstream_empty_response",
		},
		{
			Schema: RouteProtocolCaptureSchemaV1, CaseID: RouteProtocolCaseUnknownRoute, Run: 1,
			RequestedModel: "wp2-unknown-route", Protocol: "openai_chat_completions_nonstream",
			EndpointPath: "/v1/chat/completions", AuthMode: "api_key", RequestIDObserved: true, SecurityHeadersObserved: true,
			HTTPStatus: 404, MeaningfulUpstreamEvent: "none", FailureCode: "model_not_found",
		},
		{
			Schema: RouteProtocolCaptureSchemaV1, CaseID: RouteProtocolCaseDisabledRoute, Run: 1,
			RequestedModel: "quick", Protocol: "openai_chat_completions_nonstream",
			EndpointPath: "/v1/chat/completions", AuthMode: "api_key", RequestIDObserved: true, SecurityHeadersObserved: true,
			HTTPStatus: 404, MeaningfulUpstreamEvent: "none", FailureCode: "model_unavailable",
		},
	}
	for _, capture := range cases {
		descriptor := routeProtocolTestDescriptor()
		if capture.CaseID == RouteProtocolCaseUnknownRoute || capture.CaseID == RouteProtocolCaseDisabledRoute {
			descriptor = RouteProtocolDescriptor{Protocol: capture.Protocol, EndpointPath: capture.EndpointPath, AuthMode: capture.AuthMode}
		}
		raw, _ := json.Marshal(capture)
		got, err := CaptureRouteProtocol(raw, descriptor, routeProtocolTestBinding())
		if err != nil {
			t.Fatalf("case=%s: %v", capture.CaseID, err)
		}
		if got.Observation.FailureCode != capture.FailureCode || got.Observation.UpstreamAttempts != capture.UpstreamAttempts || got.Observation.RouteSwitches != 0 || got.Observation.CrossAccountResends != 0 {
			t.Fatalf("observation=%#v", got.Observation)
		}
	}
}
