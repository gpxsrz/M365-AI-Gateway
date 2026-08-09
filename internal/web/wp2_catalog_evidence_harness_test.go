package web

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"m365-native/internal/evidence"
)

func TestBuildWP2CatalogEvidenceSetIsDeterministicAndPrivacySafe(t *testing.T) {
	options := WP2CatalogEvidenceHarnessOptions{Binding: evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "1111111111111111111111111111111111111111",
		BinarySHA256:            "2222222222222222222222222222222222222222222222222222222222222222",
		HarnessSHA256:           "3333333333333333333333333333333333333333333333333333333333333333",
		EffectiveSettingsSHA256: WP2CatalogHarnessEffectiveSettingsSHA256(),
	}}
	first, err := BuildWP2CatalogEvidenceSet(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWP2CatalogEvidenceSet(options)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstRaw) != string(secondRaw) {
		t.Fatal("catalog evidence output is not byte-deterministic")
	}
	if strings.Contains(string(firstRaw), "@") || strings.Contains(strings.ToLower(string(firstRaw)), "token") || strings.Contains(strings.ToLower(string(firstRaw)), "cookie") {
		t.Fatalf("privacy-sensitive value leaked: %s", firstRaw)
	}

	if first.Schema != evidence.CatalogEvidenceSetSchemaV1 {
		t.Fatalf("schema=%s", first.Schema)
	}
	if first.AcceptedManifestSHA256 != "3145af6cae994ddbd065237d1712e0065f3c1dd42ff3cf7122f4532af5371f0f" || first.AcceptedManifestBytes != 55732 {
		t.Fatalf("accepted manifest identity=%s/%d", first.AcceptedManifestSHA256, first.AcceptedManifestBytes)
	}
	if first.AcceptedIdentityCount != 20 || first.AcceptedIdentitySupportingEvidenceCount != 433 || first.GlobalClaimCount != 12 || first.AccountDependentGlobalClaimCount != 1 {
		t.Fatalf("scale=%#v", first)
	}
	if len(first.HTTPObservations) != 3 || len(first.StaleManifestRejections) != 5 {
		t.Fatalf("observation scale=http:%d stale:%d", len(first.HTTPObservations), len(first.StaleManifestRejections))
	}

	baseline := catalogHarnessObservationByCase(t, first, evidence.CatalogEvidenceCaseNoManifest)
	accepted := catalogHarnessObservationByCase(t, first, evidence.CatalogEvidenceCaseAccepted)
	drift := catalogHarnessObservationByCase(t, first, evidence.CatalogEvidenceCaseRuntimeMappingDrift)
	if baseline.AcceptedEvidenceModelCount != 0 || baseline.ProtocolClaimCount != 0 || baseline.AccountDependentModelCount != 0 {
		t.Fatalf("baseline=%#v", baseline)
	}
	if accepted.StandardCatalogSHA256 != baseline.StandardCatalogSHA256 {
		t.Fatalf("standard fields changed: baseline=%s accepted=%s", baseline.StandardCatalogSHA256, accepted.StandardCatalogSHA256)
	}
	if accepted.AcceptedEvidenceModelCount == 0 || accepted.ProtocolClaimCount != 0 || accepted.VerifiedProtocolClaimCount != 0 || accepted.AccountDependentProtocolCount != 0 || accepted.AccountDependentModelCount != 0 || len(accepted.ClaimCapabilityIDs) != 0 || len(accepted.AdvancedCapabilitiesPromoted) != 0 {
		t.Fatalf("claim boundary=%#v", accepted)
	}
	if !reflect.DeepEqual(accepted.VisiblePresets, []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}) || len(accepted.HiddenIdentityLeaks) != 0 {
		t.Fatalf("visibility=%#v", accepted)
	}
	thinkDeeper := catalogHarnessModelByID(t, accepted, "m365-gpt-5.6-think-deeper")
	if thinkDeeper.EvidenceSource != "accepted_evidence" || thinkDeeper.MappingSource != "web_mapping" || thinkDeeper.ProtocolSource != "none" || thinkDeeper.AccountDependent || len(thinkDeeper.Protocols) != 0 {
		t.Fatalf("accepted identity projection=%#v", thinkDeeper)
	}
	overridden := catalogHarnessModelByID(t, drift, "gpt-5.2")
	if overridden.RouteKind != "configured_mapping" || overridden.EvidenceSource != "none" || overridden.ProtocolSource != "none" || overridden.AccountDependent || len(overridden.Protocols) != 0 {
		t.Fatalf("runtime drift=%#v", overridden)
	}
	if catalogHarnessModelByID(t, drift, "m365-gpt-5.6-think-deeper").EvidenceSource != "accepted_evidence" {
		t.Fatalf("unrelated route lost evidence: %#v", drift)
	}
	for _, rejection := range first.StaleManifestRejections {
		if !rejection.Rejected || rejection.ErrorCode != "identity_mismatch" {
			t.Fatalf("stale rejection=%#v", rejection)
		}
	}
}

func catalogHarnessObservationByCase(t *testing.T, set evidence.CatalogEvidenceSetV1, caseID evidence.CatalogEvidenceCase) evidence.CatalogHTTPObservationV1 {
	t.Helper()
	for _, observation := range set.HTTPObservations {
		if observation.CaseID == caseID {
			return observation
		}
	}
	t.Fatalf("case %s not found", caseID)
	return evidence.CatalogHTTPObservationV1{}
}

func catalogHarnessModelByID(t *testing.T, observation evidence.CatalogHTTPObservationV1, id string) evidence.CatalogModelObservationV1 {
	t.Helper()
	for _, model := range observation.SelectedModels {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("model %s not found in case %s", id, observation.CaseID)
	return evidence.CatalogModelObservationV1{}
}
