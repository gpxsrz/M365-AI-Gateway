package web

import (
	"bytes"
	"encoding/json"
	"testing"

	"m365-native/internal/evidence"
)

func wp2AliasProjectionTestBinding() evidence.CaptureBinding {
	return evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "753d994b821ff450681141934cdabceb56c83b51",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
	}
}

type wp2AliasExpectation struct {
	canonical               string
	toneLow                 string
	toneHigh                string
	kind                    routeKind
	visibility              catalogVisibility
	defaultReasoning        string
	identity                identityStatus
	locked                  bool
	listed                  bool
	acceptedMappingEvidence string
}

var wp2AliasExpectations = map[string]wp2AliasExpectation{
	"m365-copilot": {
		canonical: "m365-auto", toneLow: "Magic", toneHigh: "Magic", kind: routeKindAlias,
		visibility: catalogCompatibility, defaultReasoning: "none", identity: identityDynamicUnidentified,
		locked: true, listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"gpt-5.6-reasoning": {
		canonical: "m365-gpt-5.6-think-deeper", toneLow: "Gpt_5_6_Reasoning", toneHigh: "Gpt_5_6_Reasoning", kind: routeKindAlias,
		visibility: catalogCompatibility, defaultReasoning: "medium", identity: identityAcceptedUnverified,
		locked: true, listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"gpt-5.5": {
		canonical: "m365-gpt-5.5-quick-response", toneLow: "Gpt_5_5_Chat", toneHigh: "Gpt_5_5_Reasoning", kind: routeKindAlias,
		visibility: catalogCompatibility, defaultReasoning: "low", identity: identityAcceptedUnverified,
		listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"gpt-5.6-sol": {
		canonical: "m365-gpt-5.6-think-deeper", toneLow: "Gpt_5_6_Reasoning", toneHigh: "Gpt_5_6_Reasoning", kind: routeKindPreset,
		visibility: catalogCompatibility, defaultReasoning: "low", identity: identityAcceptedUnverified,
		locked: true, listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"gpt-5.6-terra": {
		canonical: "m365-gpt-5.6-think-deeper", toneLow: "Gpt_5_6_Reasoning", toneHigh: "Gpt_5_6_Reasoning", kind: routeKindPreset,
		visibility: catalogCompatibility, defaultReasoning: "medium", identity: identityAcceptedUnverified,
		locked: true, listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"gpt-5.6-luna": {
		canonical: "m365-gpt-5.6-think-deeper", toneLow: "Gpt_5_6_Reasoning", toneHigh: "Gpt_5_6_Reasoning", kind: routeKindPreset,
		visibility: catalogCompatibility, defaultReasoning: "medium", identity: identityAcceptedUnverified,
		locked: true, listed: true, acceptedMappingEvidence: "web_payload_verified",
	},
	"claude": {
		canonical: "claude-sonnet", toneLow: "Claude_Sonnet", toneHigh: "Claude_Sonnet_Reasoning", kind: routeKindAlias,
		visibility: catalogHidden, identity: identityAcceptedUnverified,
		acceptedMappingEvidence: "api_tone_accepted",
	},
	"gpt-5.4-quick": {
		canonical: "gpt-5.4", toneLow: "Gpt_5_4_Chat", toneHigh: "Gpt_Reasoning", kind: routeKindAlias,
		visibility: catalogHidden, identity: identityAcceptedUnverified,
		acceptedMappingEvidence: "api_tone_accepted",
	},
	"gpt-5.3-think-deeper": {
		canonical: "gpt-5.3", toneLow: "Gpt_5_3_Chat", toneHigh: "Gpt_Reasoning", kind: routeKindAlias,
		visibility: catalogHidden, identity: identityAcceptedUnverified,
		acceptedMappingEvidence: "api_tone_accepted",
	},
}

func expectedWP2AliasTone(model, effort string) string {
	expectation := wp2AliasExpectations[model]
	switch effort {
	case "medium", "high", "xhigh":
		return expectation.toneHigh
	default:
		return expectation.toneLow
	}
}

func expectedWP2AliasMappingEvidence(model, effort string) string {
	if model == "gpt-5.5" && (effort == "medium" || effort == "high" || effort == "xhigh") {
		return "api_tone_accepted"
	}
	return wp2AliasExpectations[model].acceptedMappingEvidence
}

func TestWP2AliasProjectionContractMatchesRegistry(t *testing.T) {
	for model, expectation := range wp2AliasExpectations {
		route, ok := builtInRoute(model)
		if !ok {
			t.Fatalf("missing route %q", model)
		}
		if route.CanonicalRoute != expectation.canonical || route.Kind != expectation.kind || route.CatalogVisibility != expectation.visibility || route.DefaultReasoningLevel != expectation.defaultReasoning || route.IdentityStatus != expectation.identity || route.CompatibilityRequired != true || route.OperationalStatus != operationalEnabled || route.MappingEvidence != mappingAPIToneAccepted || route.Deprecated {
			t.Fatalf("route %s=%#v expectation=%#v", model, route, expectation)
		}
		for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh"} {
			resolution, err := resolveRoute(model, effort, nil)
			if err != nil {
				t.Fatalf("resolve %s/%s: %v", model, effort, err)
			}
			wantIgnored := expectation.locked && effort != ""
			if resolution.CanonicalRoute != expectation.canonical || resolution.ResolvedTone != expectedWP2AliasTone(model, effort) || resolution.RouteKind != expectation.kind || resolution.CatalogVisibility != expectation.visibility || !resolution.AliasUsed || !resolution.CompatibilityRequired || resolution.ReasoningEffortIgnored != wantIgnored {
				t.Fatalf("resolution %s/%s=%#v", model, effort, resolution)
			}
		}
	}
}

func TestWP2AliasProjectionGPT56ChatAndCatalog(t *testing.T) {
	got, err := BuildWP2AliasProjectionEvidenceSet(WP2AliasProjectionHarnessOptions{
		Binding:    wp2AliasProjectionTestBinding(),
		Identities: []string{"gpt-5.6-reasoning"},
		Protocols:  []string{"openai_chat_completions_nonstream"},
		Efforts:    []string{""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != evidence.AliasProjectionEvidenceSetSchemaV1 || len(got.Catalog) != 1 || len(got.Matrix) != 1 || len(got.Failures) != 1 || len(got.Records) != 3 {
		t.Fatalf("schema=%q catalog=%d matrix=%d failures=%d records=%d", got.Schema, len(got.Catalog), len(got.Matrix), len(got.Failures), len(got.Records))
	}
	catalog := got.Catalog[0]
	if catalog.RequestedIdentity != "gpt-5.6-reasoning" || catalog.CanonicalRoute != "m365-gpt-5.6-think-deeper" || catalog.RouteKind != "alias" || catalog.CatalogVisibility != "compatibility" || !catalog.CompatibilityRequired || catalog.DefaultReasoningLevel != "medium" || !catalog.Listed || catalog.ObservationSHA256 == "" {
		t.Fatalf("catalog=%#v", catalog)
	}
	entry := got.Matrix[0]
	if entry.RequestedIdentity != "gpt-5.6-reasoning" || entry.CanonicalRoute != "m365-gpt-5.6-think-deeper" || entry.RouteKind != "alias" || entry.Protocol != "openai_chat_completions_nonstream" || entry.EndpointPath != "/v1/chat/completions" || len(entry.EffortObservations) != 1 || entry.EffortObservations[0].Effort != "omitted" {
		t.Fatalf("entry=%#v", entry)
	}
	bySHA := map[string]evidence.AliasProjectionRecordV1{}
	for _, record := range got.Records {
		bySHA[record.ObservationSHA256] = record
	}
	success := bySHA[entry.EffortObservations[0].ObservationSHA256]
	observation := success.Observation
	if observation.CaseID != evidence.AliasProjectionCaseSuccess || observation.RequestedIdentity != "gpt-5.6-reasoning" || observation.TopLevelModel != "gpt-5.6-reasoning" || observation.MetadataRequestedModel != "gpt-5.6-reasoning" || observation.MetadataResponseModel != "gpt-5.6-reasoning" || !observation.RouteMetadataComplete || observation.FallbackUsed || observation.ConfiguredMapping || observation.CanonicalRoute != "m365-gpt-5.6-think-deeper" || observation.ResolvedTone != "Gpt_5_6_Reasoning" || observation.RouteKind != "alias" || observation.CatalogVisibility != "compatibility" || !observation.AliasUsed || !observation.CompatibilityRequired || observation.IdentityStatus != "accepted_unverified" || observation.Protocol != "openai_chat_completions_nonstream" || observation.EndpointPath != "/v1/chat/completions" || observation.AuthMode != "api_key" || observation.HTTPStatus != 200 || !observation.BasicTextDelivered || observation.UpstreamAttempts != 1 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || observation.PerKeyRestricted || len(success.Capabilities) != 4 {
		t.Fatalf("success=%#v capabilities=%#v", observation, success.Capabilities)
	}
	for _, capability := range success.Capabilities {
		manifest := capability.Evidence.Manifest
		if manifest.CanonicalRoute != "m365-gpt-5.6-think-deeper" || manifest.ResolvedTone != "Gpt_5_6_Reasoning" || manifest.Protocol != "openai_chat_completions_nonstream" || manifest.MappingEvidence != "web_payload_verified" || manifest.IdentityStatus != "accepted_unverified" || manifest.ObservationSHA256 != success.ObservationSHA256 || manifest.Classification != evidence.ClassificationVerified || manifest.TestExecutionStatus != evidence.TestExecutionPass {
			t.Fatalf("capability=%#v", capability)
		}
	}
	for _, failure := range got.Failures {
		record := bySHA[failure.ObservationSHA256]
		if record.Observation.HTTPStatus != 404 || record.Observation.UpstreamAttempts != 0 || record.Observation.FailureCode != failure.ExpectedFailureCode || len(record.Capabilities) != 0 {
			t.Fatalf("failure=%#v record=%#v", failure, record)
		}
	}
}

func TestWP2AliasProjectionFullMatrix(t *testing.T) {
	binding := wp2AliasProjectionTestBinding()
	got, err := BuildWP2AliasProjectionEvidenceSet(WP2AliasProjectionHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Catalog) != 9 || len(got.Matrix) != 45 || len(got.Failures) != 5 || len(got.Records) != 275 {
		t.Fatalf("catalog=%d matrix=%d failures=%d records=%d", len(got.Catalog), len(got.Matrix), len(got.Failures), len(got.Records))
	}
	bySHA := make(map[string]evidence.AliasProjectionRecordV1, len(got.Records))
	for _, record := range got.Records {
		if record.ObservationSHA256 == "" {
			t.Fatal("record missing observation checksum")
		}
		if _, duplicate := bySHA[record.ObservationSHA256]; duplicate {
			t.Fatalf("duplicate observation checksum %s", record.ObservationSHA256)
		}
		bySHA[record.ObservationSHA256] = record
	}
	for _, catalog := range got.Catalog {
		expectation, ok := wp2AliasExpectations[catalog.RequestedIdentity]
		if !ok {
			t.Fatalf("unexpected catalog identity %q", catalog.RequestedIdentity)
		}
		if catalog.CanonicalRoute != expectation.canonical || catalog.RouteKind != string(expectation.kind) || catalog.CatalogVisibility != string(expectation.visibility) || catalog.CompatibilityRequired != true || catalog.DefaultReasoningLevel != expectation.defaultReasoning || catalog.Listed != expectation.listed {
			t.Fatalf("catalog=%#v expectation=%#v", catalog, expectation)
		}
		record := bySHA[catalog.ObservationSHA256]
		observation := record.Observation
		if observation.CaseID != evidence.AliasProjectionCaseCatalog || observation.ListedInCatalog != expectation.listed || observation.Deprecated || !observation.RemovalDateAbsent || observation.PerKeyRestricted || observation.Protocol != "openai_models_catalog" || observation.EndpointPath != "/v1/models" || observation.AuthMode != "api_key" || observation.HTTPStatus != 200 || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || len(record.Capabilities) != 0 {
			t.Fatalf("catalog observation=%#v", observation)
		}
	}
	for _, entry := range got.Matrix {
		expectation, ok := wp2AliasExpectations[entry.RequestedIdentity]
		if !ok {
			t.Fatalf("unexpected matrix identity %q", entry.RequestedIdentity)
		}
		wantEfforts := 7
		if entry.Protocol == "anthropic_messages_nonstream" {
			wantEfforts = 1
		}
		if len(entry.EffortObservations) != wantEfforts || entry.CanonicalRoute != expectation.canonical || entry.RouteKind != string(expectation.kind) || entry.CatalogVisibility != string(expectation.visibility) {
			t.Fatalf("entry=%#v expectation=%#v", entry, expectation)
		}
		for _, ref := range entry.EffortObservations {
			record := bySHA[ref.ObservationSHA256]
			observation := record.Observation
			effort := ref.Effort
			if effort == "omitted" {
				effort = ""
			}
			wantIgnored := expectation.locked && effort != ""
			if observation.CaseID != evidence.AliasProjectionCaseSuccess || observation.RequestedIdentity != entry.RequestedIdentity || observation.TopLevelModel != entry.RequestedIdentity || observation.MetadataRequestedModel != entry.RequestedIdentity || observation.MetadataResponseModel != entry.RequestedIdentity || !observation.RouteMetadataComplete || observation.FallbackUsed || observation.ConfiguredMapping || observation.CanonicalRoute != expectation.canonical || observation.ResolvedTone != expectedWP2AliasTone(entry.RequestedIdentity, effort) || observation.RouteKind != string(expectation.kind) || observation.CatalogVisibility != string(expectation.visibility) || !observation.AliasUsed || !observation.CompatibilityRequired || observation.Effort != ref.Effort || observation.ReasoningEffortIgnored != wantIgnored || observation.HTTPStatus != 200 || !observation.BasicTextDelivered || observation.UpstreamAttempts != 1 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || observation.PerKeyRestricted || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || len(record.Capabilities) != 4 {
				t.Fatalf("observation=%#v entry=%#v", observation, entry)
			}
			capabilityIDs := map[string]bool{}
			for _, capability := range record.Capabilities {
				capabilityIDs[capability.CapabilityID] = true
				manifest := capability.Evidence.Manifest
				if manifest.CanonicalRoute != expectation.canonical || manifest.ResolvedTone != observation.ResolvedTone || manifest.Protocol != entry.Protocol || manifest.ObservationSHA256 != ref.ObservationSHA256 || manifest.MappingEvidence != expectedWP2AliasMappingEvidence(entry.RequestedIdentity, effort) || manifest.IdentityStatus != string(expectation.identity) || manifest.Classification != evidence.ClassificationVerified || manifest.TestExecutionStatus != evidence.TestExecutionPass {
					t.Fatalf("capability=%#v", capability)
				}
			}
			for _, required := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
				if !capabilityIDs[required] {
					t.Fatalf("missing capability %q in %#v", required, record.Capabilities)
				}
			}
		}
	}
	for _, failure := range got.Failures {
		record := bySHA[failure.ObservationSHA256]
		observation := record.Observation
		if observation.CaseID != failure.CaseID || observation.RequestedIdentity != failure.RequestedModel || observation.Protocol != failure.Protocol || observation.HTTPStatus != 404 || observation.FailureCode != failure.ExpectedFailureCode || observation.UpstreamAttempts != 0 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || observation.BasicTextDelivered || len(record.Capabilities) != 0 {
			t.Fatalf("failure=%#v observation=%#v", failure, observation)
		}
	}
	second, err := BuildWP2AliasProjectionEvidenceSet(WP2AliasProjectionHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(got)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("alias projection evidence set is not deterministic")
	}
}
