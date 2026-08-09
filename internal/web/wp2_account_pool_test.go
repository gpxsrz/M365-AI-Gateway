package web

import (
	"encoding/json"
	"strings"
	"testing"

	"m365-native/internal/evidence"
)

func wp2AccountPoolTestBinding() evidence.CaptureBinding {
	return evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "49848dda87642f63b1beb15b2d87767e9edc4816",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
	}
}

// This is a historical WP2 evidence-builder regression. It runs each profile
// in an isolated single-account server; it is not a runtime account pool.
func TestHistoricalWP2AccountPoolEvidenceMatrix(t *testing.T) {
	got, err := BuildWP2AccountPoolEvidenceSet(WP2AccountPoolHarnessOptions{Binding: wp2AccountPoolTestBinding()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != evidence.AccountPoolEvidenceSetSchemaV1 || got.ProfileSetSHA256 == "" || len(got.Profiles) != 3 || len(got.GlobalClaims) != 20 {
		t.Fatalf("schema=%q profiles=%d claims=%d set=%#v", got.Schema, len(got.Profiles), len(got.GlobalClaims), got)
	}
	eligibleProfiles := 0
	unavailableProfiles := 0
	matrixEntries := 0
	evidenceRecords := 0
	for _, profile := range got.Profiles {
		switch profile.Status {
		case evidence.AccountPoolProfileEligible:
			eligibleProfiles++
			if len(profile.Matrix) != 20 {
				t.Fatalf("profile %s matrix=%d", profile.AccountProfileRef, len(profile.Matrix))
			}
			matrixEntries += len(profile.Matrix)
			for _, entry := range profile.Matrix {
				if entry.UpstreamAttempts != 1 || entry.CrossAccountResends != 0 {
					t.Fatalf("entry=%#v", entry)
				}
				for _, capability := range entry.Capabilities {
					if capability.EvidenceSHA256 != "" {
						evidenceRecords++
					}
				}
			}
		case evidence.AccountPoolProfileUnavailable:
			unavailableProfiles++
			if len(profile.Matrix) != 0 || profile.UnavailableReason != "profile_not_ready" {
				t.Fatalf("unavailable profile=%#v", profile)
			}
		}
	}
	if eligibleProfiles != 2 || unavailableProfiles != 1 || matrixEntries != 40 || evidenceRecords != 157 {
		t.Fatalf("eligible=%d unavailable=%d matrix=%d evidence=%d", eligibleProfiles, unavailableProfiles, matrixEntries, evidenceRecords)
	}

	dependent := 0
	for _, claim := range got.GlobalClaims {
		if claim.EligibleProfileCount != 2 || claim.UnavailableProfileCount != 1 {
			t.Fatalf("claim counts=%#v", claim)
		}
		if claim.AccountDependent {
			dependent++
			if claim.CanonicalRoute != "m365-gpt-5.6-think-deeper" || claim.Protocol != "openai_responses_nonstream" || claim.RouteEligibility != evidence.ClassificationInconclusive {
				t.Fatalf("unexpected dependent claim=%#v", claim)
			}
		}
	}
	if dependent != 1 {
		t.Fatalf("account-dependent claims=%d", dependent)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw)
	for _, forbidden := range []string{"@example.test", "wp2-pool-internal", "wp2-pool-token", "wp2-pool-tenant", "access_token", "refresh_token", "tenant_id", "email"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("identity-bearing or secret value leaked: %q", forbidden)
		}
	}
}

func TestWP2AccountPoolEvidenceIsDeterministic(t *testing.T) {
	first, err := BuildWP2AccountPoolEvidenceSet(WP2AccountPoolHarnessOptions{Binding: wp2AccountPoolTestBinding()})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWP2AccountPoolEvidenceSet(WP2AccountPoolHarnessOptions{Binding: wp2AccountPoolTestBinding()})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("account-pool evidence is not deterministic")
	}
}
