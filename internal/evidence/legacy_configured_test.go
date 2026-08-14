package evidence_test

import (
	"encoding/json"
	"strings"
	"testing"

	. "m365-native/internal/evidence/offline"
)

func legacyConfiguredTestBinding() CaptureBinding {
	return CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "2a6057b2c14269b8ccf310a996678eb6766a276c",
		BinarySHA256:            strings.Repeat("1", 64),
		HarnessSHA256:           strings.Repeat("2", 64),
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: strings.Repeat("3", 64),
	}
}

func legacyConfiguredDescriptor(kind string, configured bool) LegacyConfiguredDescriptor {
	mapping := "api_tone_accepted"
	owner := "microsoft-365"
	model := "gpt-5.2"
	tone := "Gpt_5_2_Chat"
	if configured {
		mapping = "unverified"
		owner = "anthropic-via-microsoft-365"
		model = "existing-claude-route"
		tone = "Claude_Sonnet_Reasoning"
	}
	return LegacyConfiguredDescriptor{
		RequestedModel: model, CanonicalRoute: model, ResolvedTone: tone, RouteKind: kind, Owner: owner,
		OperationalStatus: "enabled", RuntimeMappingEvidence: mapping, AcceptedMappingEvidence: mapping,
		IdentityStatus: "accepted_unverified", CatalogVisibility: "public", ConfiguredMapping: configured,
		Protocol: "openai_chat_completions_nonstream", EndpointPath: "/v1/chat/completions", AuthMode: "api_key",
		Effort: "omitted",
	}
}

func validLegacyConfiguredSuccessCapture(descriptor LegacyConfiguredDescriptor) LegacyConfiguredCaptureV1 {
	return LegacyConfiguredCaptureV1{
		Schema: LegacyConfiguredCaptureSchemaV1, CaseID: LegacyConfiguredCaseSuccess,
		Classification: ClassificationVerified, RequestedModel: descriptor.RequestedModel,
		MetadataRequestedModel: descriptor.RequestedModel, MetadataResponseModel: descriptor.RequestedModel,
		TopLevelModel: descriptor.RequestedModel, CanonicalRoute: descriptor.CanonicalRoute, ResolvedTone: descriptor.ResolvedTone,
		RouteKind: descriptor.RouteKind, Owner: descriptor.Owner, OperationalStatus: descriptor.OperationalStatus,
		MappingEvidence: descriptor.RuntimeMappingEvidence, IdentityStatus: descriptor.IdentityStatus,
		CatalogVisibility: descriptor.CatalogVisibility, ConfiguredMapping: descriptor.ConfiguredMapping,
		Protocol: descriptor.Protocol, EndpointPath: descriptor.EndpointPath, AuthMode: descriptor.AuthMode,
		Effort: descriptor.Effort, HTTPStatus: 200, BasicTextDelivered: true, UpstreamAttempts: 1,
		RequestIDObserved: true, SecurityHeadersObserved: true,
	}
}

func TestCaptureLegacyConfiguredBindsOnlySuccessfulHTTPObservations(t *testing.T) {
	for _, descriptor := range []LegacyConfiguredDescriptor{
		legacyConfiguredDescriptor("legacy_direct", false),
		legacyConfiguredDescriptor("configured_mapping", true),
	} {
		capture := validLegacyConfiguredSuccessCapture(descriptor)
		raw, _ := json.Marshal(capture)
		record, err := CaptureLegacyConfigured(raw, descriptor, legacyConfiguredTestBinding())
		if err != nil {
			t.Fatal(err)
		}
		if record.Observation.Classification != ClassificationVerified || len(record.Capabilities) != 4 {
			t.Fatalf("record=%#v", record)
		}
		for _, capability := range record.Capabilities {
			manifest := capability.Evidence.Manifest
			if manifest.CanonicalRoute != descriptor.CanonicalRoute || manifest.ResolvedTone != descriptor.ResolvedTone || manifest.MappingEvidence != descriptor.AcceptedMappingEvidence || manifest.AccountProfileRef != legacyConfiguredTestBinding().AccountProfileRef || manifest.EffectiveSettingsSHA256 != legacyConfiguredTestBinding().EffectiveSettingsSHA256 {
				t.Fatalf("manifest=%#v", manifest)
			}
		}
	}
}

func TestCaptureLegacyConfiguredCatalogCannotPromoteStaticConfiguration(t *testing.T) {
	descriptor := legacyConfiguredDescriptor("legacy_direct", false)
	descriptor.Protocol = "openai_models_catalog"
	descriptor.EndpointPath = "/v1/models"
	descriptor.Effort = "not_applicable"
	descriptor.ListedInCatalog = true
	capture := LegacyConfiguredCaptureV1{
		Schema: LegacyConfiguredCaptureSchemaV1, CaseID: LegacyConfiguredCaseCatalog,
		Classification: ClassificationInconclusive, RequestedModel: descriptor.RequestedModel,
		CanonicalRoute: descriptor.CanonicalRoute, ResolvedTone: descriptor.ResolvedTone, RouteKind: descriptor.RouteKind,
		Owner: descriptor.Owner, OperationalStatus: descriptor.OperationalStatus, MappingEvidence: descriptor.RuntimeMappingEvidence,
		IdentityStatus: descriptor.IdentityStatus, CatalogVisibility: descriptor.CatalogVisibility, ListedInCatalog: true,
		Protocol: descriptor.Protocol, EndpointPath: descriptor.EndpointPath, AuthMode: "api_key", Effort: descriptor.Effort,
		HTTPStatus: 200, RequestIDObserved: true, SecurityHeadersObserved: true,
	}
	raw, _ := json.Marshal(capture)
	record, err := CaptureLegacyConfigured(raw, descriptor, legacyConfiguredTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	if record.Observation.Classification != ClassificationInconclusive || len(record.Capabilities) != 0 {
		t.Fatalf("catalog record=%#v", record)
	}
	capture.Classification = ClassificationVerified
	raw, _ = json.Marshal(capture)
	if _, err := CaptureLegacyConfigured(raw, descriptor, legacyConfiguredTestBinding()); err == nil {
		t.Fatal("catalog projection incorrectly promoted to VERIFIED")
	}
}

func TestCaptureLegacyConfiguredRejectsPrivacyIdentityAndPolicyDrift(t *testing.T) {
	descriptor := legacyConfiguredDescriptor("configured_mapping", true)
	valid := validLegacyConfiguredSuccessCapture(descriptor)
	raw, _ := json.Marshal(valid)
	cases := [][]byte{
		append(raw[:len(raw)-1], []byte(`,"prompt":"secret"}`)...),
	}
	mutations := []func(*LegacyConfiguredCaptureV1){
		func(c *LegacyConfiguredCaptureV1) { c.Owner = "microsoft-365" },
		func(c *LegacyConfiguredCaptureV1) { c.ConfiguredMapping = false },
		func(c *LegacyConfiguredCaptureV1) { c.MetadataRequestedModel = "different" },
		func(c *LegacyConfiguredCaptureV1) { c.PerKeyRestricted = true },
		func(c *LegacyConfiguredCaptureV1) { c.RequestIDObserved = false },
		func(c *LegacyConfiguredCaptureV1) { c.IdentityStatus = "upstream_identity_verified" },
	}
	for _, mutate := range mutations {
		copy := valid
		mutate(&copy)
		encoded, _ := json.Marshal(copy)
		cases = append(cases, encoded)
	}
	for _, candidate := range cases {
		if _, err := CaptureLegacyConfigured(candidate, descriptor, legacyConfiguredTestBinding()); err == nil {
			t.Fatalf("invalid capture accepted: %s", candidate)
		}
	}
}
