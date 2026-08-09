package web

import (
	"bytes"
	"encoding/json"
	"testing"

	"m365-native/internal/evidence"
)

func wp2RouteProtocolTestBinding() evidence.CaptureBinding {
	return evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "4120e4027cc9cb42c3fe51dd01de59af4d197984",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
	}
}

func expectedWP2AuthMode(protocol string) string {
	if protocol == "legacy_chat_nonstream" {
		return "admin_session"
	}
	return "api_key"
}

func TestWP2RouteProtocolMatrixAutoChatOneDelivery(t *testing.T) {
	got, err := BuildWP2RouteProtocolEvidenceSet(WP2RouteProtocolHarnessOptions{
		Binding:   wp2RouteProtocolTestBinding(),
		Routes:    []string{"m365-auto"},
		Protocols: []string{"openai_chat_completions_nonstream"},
		Runs:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matrix) != 1 || len(got.RouteFailures) != 1 || len(got.Records) != 3 {
		t.Fatalf("matrix=%d route_failures=%d records=%d", len(got.Matrix), len(got.RouteFailures), len(got.Records))
	}
	entry := got.Matrix[0]
	if entry.CanonicalRoute != "m365-auto" || entry.ResolvedTone != "Magic" || entry.Protocol != "openai_chat_completions_nonstream" || entry.Classification != evidence.ProtocolExposedAndSupported {
		t.Fatalf("entry=%#v", entry)
	}
	if len(entry.SuccessObservationSHA256) != 1 || entry.EmptyObservationSHA256 == "" {
		t.Fatalf("entry observations=%#v", entry)
	}
	success := got.Records[0]
	if success.Observation.CaseID != evidence.RouteProtocolCaseSuccess || success.Observation.HTTPStatus != 200 || !success.Observation.BasicTextDelivered || success.Observation.TopLevelModel != "m365-auto" || success.Observation.CanonicalRoute != "m365-auto" || success.Observation.ResolvedTone != "Magic" || success.Observation.EndpointPath != "/v1/chat/completions" || success.Observation.AuthMode != "api_key" || !success.Observation.RequestIDObserved || !success.Observation.SecurityHeadersObserved || !success.Observation.ReasoningEffortApplied || !success.Observation.ReasoningEffortIgnored || success.Observation.UpstreamAttempts != 1 || success.Observation.RouteSwitches != 0 || success.Observation.CrossAccountResends != 0 {
		t.Fatalf("success=%#v", success.Observation)
	}
	if len(success.Capabilities) != 4 {
		t.Fatalf("capabilities=%d", len(success.Capabilities))
	}
	for _, capability := range success.Capabilities {
		if capability.Evidence.Manifest.Classification != evidence.ClassificationVerified || capability.Evidence.Manifest.TestExecutionStatus != evidence.TestExecutionPass {
			t.Fatalf("capability=%#v", capability)
		}
	}
	empty := got.Records[1].Observation
	if empty.CaseID != evidence.RouteProtocolCaseUpstreamEmpty || empty.HTTPStatus != 502 || empty.FailureCode != "upstream_empty_response" || empty.UpstreamAttempts != 1 || empty.RouteSwitches != 0 || empty.CrossAccountResends != 0 {
		t.Fatalf("empty=%#v", empty)
	}
	for _, record := range got.Records[2:] {
		if record.Observation.HTTPStatus != 404 || record.Observation.UpstreamAttempts != 0 || record.Observation.RouteSwitches != 0 || record.Observation.CrossAccountResends != 0 {
			t.Fatalf("route failure=%#v", record.Observation)
		}
	}
}

func TestWP2RouteProtocolMatrixAllPrimaryRoutesAndNonStreamProtocols(t *testing.T) {
	binding := wp2RouteProtocolTestBinding()
	got, err := BuildWP2RouteProtocolEvidenceSet(WP2RouteProtocolHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != evidence.RouteProtocolEvidenceSetSchemaV1 || len(got.Matrix) != 20 || len(got.RouteFailures) != 4 || len(got.Records) != 84 {
		t.Fatalf("schema=%q matrix=%d route_failures=%d records=%d", got.Schema, len(got.Matrix), len(got.RouteFailures), len(got.Records))
	}

	bySHA := make(map[string]evidence.RouteProtocolRecordV1, len(got.Records))
	for _, record := range got.Records {
		if record.ObservationSHA256 == "" {
			t.Fatal("record missing observation checksum")
		}
		if _, duplicate := bySHA[record.ObservationSHA256]; duplicate {
			t.Fatalf("duplicate observation checksum %s", record.ObservationSHA256)
		}
		bySHA[record.ObservationSHA256] = record
	}
	for _, entry := range got.Matrix {
		if entry.Classification != evidence.ProtocolExposedAndSupported || len(entry.SuccessObservationSHA256) != 3 || entry.EmptyObservationSHA256 == "" {
			t.Fatalf("entry=%#v", entry)
		}
		seenRuns := map[int]bool{}
		for _, checksum := range entry.SuccessObservationSHA256 {
			record, ok := bySHA[checksum]
			if !ok {
				t.Fatalf("success observation %s missing", checksum)
			}
			observation := record.Observation
			if observation.CaseID != evidence.RouteProtocolCaseSuccess || observation.CanonicalRoute != entry.CanonicalRoute || observation.ResolvedTone != entry.ResolvedTone || observation.Protocol != entry.Protocol || observation.EndpointPath != entry.EndpointPath || observation.AuthMode != expectedWP2AuthMode(entry.Protocol) || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || observation.TopLevelModel != entry.CanonicalRoute || observation.HTTPStatus != 200 || !observation.BasicTextDelivered || observation.UpstreamAttempts != 1 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || len(record.Capabilities) != 4 {
				t.Fatalf("success=%#v capabilities=%d", observation, len(record.Capabilities))
			}
			if seenRuns[observation.Run] {
				t.Fatalf("duplicate run %d for %#v", observation.Run, entry)
			}
			seenRuns[observation.Run] = true
			if observation.ReasoningEffortApplied != (entry.Protocol != "anthropic_messages_nonstream") {
				t.Fatalf("reasoning effort seam=%#v", observation)
			}
			if observation.ReasoningEffortApplied && !observation.ReasoningEffortIgnored {
				t.Fatalf("canonical route drifted under reasoning effort: %#v", observation)
			}
			capabilities := map[string]bool{}
			for _, capability := range record.Capabilities {
				capabilities[capability.CapabilityID] = true
				if capability.Evidence.Manifest.CanonicalRoute != entry.CanonicalRoute || capability.Evidence.Manifest.ResolvedTone != entry.ResolvedTone || capability.Evidence.Manifest.Protocol != entry.Protocol || capability.Evidence.Manifest.ObservationSHA256 != checksum || capability.Evidence.Manifest.Classification != evidence.ClassificationVerified || capability.Evidence.Manifest.TestExecutionStatus != evidence.TestExecutionPass {
					t.Fatalf("capability=%#v", capability)
				}
			}
			for _, required := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
				if !capabilities[required] {
					t.Fatalf("missing capability %q in %#v", required, record.Capabilities)
				}
			}
		}
		for run := 1; run <= 3; run++ {
			if !seenRuns[run] {
				t.Fatalf("missing independent run %d for %#v", run, entry)
			}
		}
		empty, ok := bySHA[entry.EmptyObservationSHA256]
		if !ok || empty.Observation.CaseID != evidence.RouteProtocolCaseUpstreamEmpty || empty.Observation.EndpointPath != entry.EndpointPath || empty.Observation.AuthMode != expectedWP2AuthMode(entry.Protocol) || !empty.Observation.RequestIDObserved || !empty.Observation.SecurityHeadersObserved || empty.Observation.HTTPStatus != 502 || empty.Observation.FailureCode != "upstream_empty_response" || empty.Observation.UpstreamAttempts != 1 || empty.Observation.RouteSwitches != 0 || empty.Observation.CrossAccountResends != 0 || len(empty.Capabilities) != 1 || empty.Capabilities[0].CapabilityID != "protocol_transport" {
			t.Fatalf("empty record=%#v", empty)
		}
	}
	for _, failure := range got.RouteFailures {
		record, ok := bySHA[failure.ObservationSHA256]
		if !ok {
			t.Fatalf("route failure observation %s missing", failure.ObservationSHA256)
		}
		observation := record.Observation
		if observation.RequestedModel != failure.RequestedModel || observation.CaseID != failure.CaseID || observation.Protocol != failure.Protocol || observation.AuthMode != expectedWP2AuthMode(failure.Protocol) || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || observation.HTTPStatus != 404 || observation.FailureCode != failure.ExpectedFailureCode || observation.UpstreamAttempts != 0 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || len(record.Capabilities) != 0 {
			t.Fatalf("route failure=%#v record=%#v", failure, record)
		}
	}

	second, err := BuildWP2RouteProtocolEvidenceSet(WP2RouteProtocolHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(got)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("route/protocol evidence set is not deterministic")
	}
}
