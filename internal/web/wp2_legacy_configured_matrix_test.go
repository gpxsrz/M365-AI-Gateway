package web

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"m365-native/internal/evidence"
)

type wp2LegacyConfiguredExpectation struct {
	lowTone         string
	highTone        string
	owner           string
	kind            string
	configured      bool
	experimental    bool
	mappingEvidence string
	reasoningLocked bool
}

var wp2LegacyConfiguredExpectations = map[string]wp2LegacyConfiguredExpectation{
	"gpt-5.2":                  {lowTone: "Gpt_5_2_Chat", highTone: "Gpt_5_2_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"gpt-5.2-reasoning":        {lowTone: "Gpt_5_2_Reasoning", highTone: "Gpt_5_2_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"gpt-5.3":                  {lowTone: "Gpt_5_3_Chat", highTone: "Gpt_5_3_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"gpt-5.4":                  {lowTone: "Gpt_5_4_Chat", highTone: "Gpt_5_4_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"gpt-5.4-reasoning":        {lowTone: "Gpt_5_4_Reasoning", highTone: "Gpt_5_4_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"gpt-5.5-reasoning":        {lowTone: "Gpt_5_5_Reasoning", highTone: "Gpt_5_5_Reasoning", owner: "microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"claude-sonnet":            {lowTone: "Claude_Sonnet", highTone: "Claude_Sonnet_Reasoning", owner: "anthropic-via-microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"claude-sonnet-reasoning":  {lowTone: "Claude_Sonnet_Reasoning", highTone: "Claude_Sonnet_Reasoning", owner: "anthropic-via-microsoft-365", kind: "legacy_direct", experimental: true, mappingEvidence: "api_tone_accepted"},
	"existing-microsoft-route": {lowTone: "Gpt_5_6_Reasoning", highTone: "Gpt_5_6_Reasoning", owner: "microsoft-365", kind: "configured_mapping", configured: true, mappingEvidence: "unverified", reasoningLocked: true},
	"existing-claude-route":    {lowTone: "Claude_Sonnet_Reasoning", highTone: "Claude_Sonnet_Reasoning", owner: "anthropic-via-microsoft-365", kind: "configured_mapping", configured: true, mappingEvidence: "unverified", reasoningLocked: true},
}

func expectedWP2LegacyConfiguredTone(model, effort string) string {
	expectation := wp2LegacyConfiguredExpectations[model]
	switch effort {
	case "medium", "high", "xhigh":
		return expectation.highTone
	default:
		return expectation.lowTone
	}
}

func TestWP2LegacyConfiguredCompleteMatrix(t *testing.T) {
	binding := wp2LegacyConfiguredTestBinding()
	got, err := BuildWP2LegacyConfiguredEvidenceSet(WP2LegacyConfiguredHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != evidence.LegacyConfiguredEvidenceSetSchemaV1 || len(got.Catalog) != 10 || len(got.Matrix) != 50 || len(got.Failures) != 5 || len(got.Records) != 305 {
		t.Fatalf("schema=%q catalog=%d matrix=%d failures=%d records=%d", got.Schema, len(got.Catalog), len(got.Matrix), len(got.Failures), len(got.Records))
	}
	bySHA := make(map[string]evidence.LegacyConfiguredRecordV1, len(got.Records))
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
		expectation := wp2LegacyConfiguredExpectations[catalog.RequestedModel]
		if catalog.Classification != evidence.ClassificationInconclusive || catalog.CanonicalRoute != catalog.RequestedModel || catalog.RouteKind != expectation.kind || catalog.Owner != expectation.owner || catalog.ConfiguredMapping != expectation.configured || catalog.Experimental != expectation.experimental || !catalog.Listed {
			t.Fatalf("catalog=%#v expectation=%#v", catalog, expectation)
		}
		record := bySHA[catalog.ObservationSHA256]
		if record.Observation.CaseID != evidence.LegacyConfiguredCaseCatalog || record.Observation.Classification != evidence.ClassificationInconclusive || len(record.Capabilities) != 0 {
			t.Fatalf("catalog record=%#v", record)
		}
	}
	for _, entry := range got.Matrix {
		expectation := wp2LegacyConfiguredExpectations[entry.RequestedModel]
		wantEfforts := 7
		if entry.Protocol == "anthropic_messages_nonstream" {
			wantEfforts = 1
		}
		if entry.Classification != evidence.ClassificationVerified || entry.CanonicalRoute != entry.RequestedModel || entry.RouteKind != expectation.kind || entry.Owner != expectation.owner || entry.ConfiguredMapping != expectation.configured || len(entry.EffortObservations) != wantEfforts {
			t.Fatalf("entry=%#v expectation=%#v", entry, expectation)
		}
		for _, ref := range entry.EffortObservations {
			record := bySHA[ref.ObservationSHA256]
			observation := record.Observation
			effort := ref.Effort
			if effort == "omitted" {
				effort = ""
			}
			wantIgnored := expectation.reasoningLocked && effort != ""
			if observation.CaseID != evidence.LegacyConfiguredCaseSuccess || observation.Classification != evidence.ClassificationVerified || observation.RequestedModel != entry.RequestedModel || observation.MetadataRequestedModel != entry.RequestedModel || observation.MetadataResponseModel != entry.RequestedModel || observation.TopLevelModel != entry.RequestedModel || observation.CanonicalRoute != entry.CanonicalRoute || observation.ResolvedTone != expectedWP2LegacyConfiguredTone(entry.RequestedModel, effort) || observation.RouteKind != expectation.kind || observation.Owner != expectation.owner || observation.ConfiguredMapping != expectation.configured || observation.Experimental != expectation.experimental || observation.ReasoningEffortIgnored != wantIgnored || observation.HTTPStatus != 200 || !observation.BasicTextDelivered || observation.UpstreamAttempts != 1 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || observation.PerKeyRestricted || !observation.RequestIDObserved || !observation.SecurityHeadersObserved || len(record.Capabilities) != 4 {
				t.Fatalf("observation=%#v capabilities=%d", observation, len(record.Capabilities))
			}
			for _, capability := range record.Capabilities {
				manifest := capability.Evidence.Manifest
				if manifest.CanonicalRoute != entry.CanonicalRoute || manifest.ResolvedTone != observation.ResolvedTone || manifest.Protocol != entry.Protocol || manifest.ObservationSHA256 != ref.ObservationSHA256 || manifest.AccountProfileRef != binding.AccountProfileRef || manifest.EffectiveSettingsSHA256 != binding.EffectiveSettingsSHA256 || manifest.MappingEvidence != expectation.mappingEvidence || manifest.IdentityStatus != "accepted_unverified" || manifest.Classification != evidence.ClassificationVerified || manifest.TestExecutionStatus != evidence.TestExecutionPass {
					t.Fatalf("manifest=%#v", manifest)
				}
			}
		}
	}
	for _, failure := range got.Failures {
		record := bySHA[failure.ObservationSHA256]
		observation := record.Observation
		if observation.Classification != evidence.ClassificationInconclusive || observation.HTTPStatus != 404 || observation.FailureCode != failure.ExpectedFailureCode || observation.UpstreamAttempts != 0 || observation.RouteSwitches != 0 || observation.CrossAccountResends != 0 || len(record.Capabilities) != 0 {
			t.Fatalf("failure=%#v record=%#v", failure, record)
		}
	}
	second, err := BuildWP2LegacyConfiguredEvidenceSet(WP2LegacyConfiguredHarnessOptions{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(got)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("legacy/configured evidence set is not deterministic")
	}
}

func TestWP2ConfiguredMappingsCannotOverrideProtectedIdentities(t *testing.T) {
	protected := []string{}
	for _, route := range builtInRouteRegistry {
		if strings.HasPrefix(route.ID, "m365-") || route.Kind == routeKindWebMode || route.Kind == routeKindAlias || route.Kind == routeKindPreset {
			protected = append(protected, route.ID)
		}
	}
	mappings := make([]modelMapping, 0, len(protected))
	for _, id := range protected {
		mappings = append(mappings, modelMapping{PublicModel: id, UpstreamTone: "Claude_Sonnet_Reasoning", DisplayName: "Must Not Override", DefaultReasoningLevel: "high"})
	}
	for _, id := range protected {
		before, _ := builtInRoute(id)
		after, ok := registeredRoute(id, mappings)
		if !ok || after.Tone != before.Tone || after.Kind != before.Kind || after.CanonicalRoute != before.CanonicalRoute || after.ConfiguredMapping {
			t.Fatalf("protected identity %s overridden: before=%#v after=%#v", id, before, after)
		}
	}
}

func TestWP2ExistingLegacyDirectOverrideRemainsCallableAsConfiguredMapping(t *testing.T) {
	got, err := BuildWP2LegacyConfiguredEvidenceSet(WP2LegacyConfiguredHarnessOptions{
		Binding:      wp2LegacyConfiguredTestBinding(),
		LegacyRoutes: []string{"gpt-5.2", "gpt-5.4"},
		ConfiguredMappings: []WP2ConfiguredMapping{{
			PublicModel:           "gpt-5.4",
			UpstreamTone:          "Gpt_5_4_Reasoning",
			DisplayName:           "Existing GPT-5.4 Override",
			DefaultReasoningLevel: "high",
		}},
		Protocols: []string{"openai_chat_completions_nonstream"},
		Efforts:   []string{"high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Matrix) != 2 {
		t.Fatalf("overridden route was duplicated: %#v", got.Matrix)
	}
	var found bool
	for _, entry := range got.Matrix {
		if entry.RequestedModel == "gpt-5.4" {
			found = entry.RouteKind == "configured_mapping" && entry.ConfiguredMapping && entry.EffortObservations[0].ResolvedTone == "Gpt_5_4_Reasoning"
		}
	}
	if !found {
		t.Fatalf("legacy override was not preserved: %#v", got.Matrix)
	}
}
