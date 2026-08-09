package web

import (
	"testing"

	"m365-native/internal/evidence"
)

func wp2LegacyConfiguredTestBinding() evidence.CaptureBinding {
	return evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "2a6057b2c14269b8ccf310a996678eb6766a276c",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
	}
}

func TestWP2LegacyConfiguredFirstPublicHTTPSlice(t *testing.T) {
	binding := wp2LegacyConfiguredTestBinding()
	got, err := BuildWP2LegacyConfiguredEvidenceSet(WP2LegacyConfiguredHarnessOptions{
		Binding:      binding,
		LegacyRoutes: []string{"gpt-5.2"},
		ConfiguredMappings: []WP2ConfiguredMapping{{
			PublicModel:           "existing-claude-route",
			UpstreamTone:          "Claude_Sonnet_Reasoning",
			DisplayName:           "Existing Claude Route",
			DefaultReasoningLevel: "medium",
		}},
		Protocols: []string{"openai_chat_completions_nonstream"},
		Efforts:   []string{""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != evidence.LegacyConfiguredEvidenceSetSchemaV1 || len(got.Catalog) != 2 || len(got.Matrix) != 2 || len(got.Failures) != 1 || len(got.Records) != 5 {
		t.Fatalf("schema=%q catalog=%d matrix=%d failures=%d records=%d", got.Schema, len(got.Catalog), len(got.Matrix), len(got.Failures), len(got.Records))
	}
	byID := map[string]evidence.LegacyConfiguredCatalogEntryV1{}
	for _, entry := range got.Catalog {
		byID[entry.RequestedModel] = entry
	}
	legacy := byID["gpt-5.2"]
	if legacy.RouteKind != "legacy_direct" || legacy.Owner != "microsoft-365" || legacy.ConfiguredMapping || legacy.Classification != evidence.ClassificationInconclusive || !legacy.Listed {
		t.Fatalf("legacy catalog=%#v", legacy)
	}
	configured := byID["existing-claude-route"]
	if configured.RouteKind != "configured_mapping" || configured.Owner != "anthropic-via-microsoft-365" || !configured.ConfiguredMapping || configured.Classification != evidence.ClassificationInconclusive || !configured.Listed {
		t.Fatalf("configured catalog=%#v", configured)
	}

	bySHA := map[string]evidence.LegacyConfiguredRecordV1{}
	for _, record := range got.Records {
		bySHA[record.ObservationSHA256] = record
	}
	for _, catalog := range got.Catalog {
		record := bySHA[catalog.ObservationSHA256]
		if record.Observation.Classification != evidence.ClassificationInconclusive || len(record.Capabilities) != 0 {
			t.Fatalf("catalog record=%#v", record)
		}
	}
	for _, entry := range got.Matrix {
		if len(entry.EffortObservations) != 1 {
			t.Fatalf("matrix=%#v", entry)
		}
		record := bySHA[entry.EffortObservations[0].ObservationSHA256]
		observation := record.Observation
		if observation.Classification != evidence.ClassificationVerified || observation.RequestedModel != entry.RequestedModel || observation.TopLevelModel != entry.RequestedModel || observation.CanonicalRoute != entry.CanonicalRoute || observation.RouteKind != entry.RouteKind || observation.Owner != entry.Owner || observation.Protocol != "openai_chat_completions_nonstream" || observation.EndpointPath != "/v1/chat/completions" || observation.HTTPStatus != 200 || !observation.BasicTextDelivered || observation.UpstreamAttempts != 1 || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || len(record.Capabilities) != 4 {
			t.Fatalf("success observation=%#v capabilities=%d", observation, len(record.Capabilities))
		}
		for _, capability := range record.Capabilities {
			manifest := capability.Evidence.Manifest
			if manifest.CanonicalRoute != observation.CanonicalRoute || manifest.ResolvedTone != observation.ResolvedTone || manifest.Protocol != observation.Protocol || manifest.AccountProfileRef != binding.AccountProfileRef || manifest.EffectiveSettingsSHA256 != binding.EffectiveSettingsSHA256 || manifest.Classification != evidence.ClassificationVerified || manifest.TestExecutionStatus != evidence.TestExecutionPass {
				t.Fatalf("manifest=%#v", manifest)
			}
		}
	}
}
