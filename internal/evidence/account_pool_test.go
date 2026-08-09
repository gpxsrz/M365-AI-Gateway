package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

const (
	accountPoolTestRoute    = "m365-auto"
	accountPoolTestTone     = "magic"
	accountPoolTestProtocol = "openai_chat_completions_nonstream"
)

func accountPoolTestManifest(t *testing.T, profileRef, capabilityID string, classification Classification) AccountPoolCapabilityInputV1 {
	t.Helper()
	status := TestExecutionPass
	if classification == ClassificationInconclusive {
		status = TestExecutionBlocked
	}
	if classification == ClassificationConfirmedDefect {
		status = TestExecutionFail
	}
	observation := sha256.Sum256([]byte(profileRef + ":" + capabilityID + ":" + string(classification)))
	manifest := ManifestV1{
		Schema:                  SchemaV1,
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "49848dda87642f63b1beb15b2d87767e9edc4816",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		ObservationSHA256:       hex.EncodeToString(observation[:]),
		CanonicalRoute:          accountPoolTestRoute,
		ResolvedTone:            accountPoolTestTone,
		Protocol:                accountPoolTestProtocol,
		AccountProfileRef:       profileRef,
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
		MappingEvidence:         "web_payload_verified",
		IdentityStatus:          "dynamic_unidentified",
		CapabilityID:            capabilityID,
		Classification:          classification,
		TestExecutionStatus:     status,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateCapabilityEvidence(raw, IdentitySet{
		NormativeADRSHA256:      manifest.NormativeADRSHA256,
		SourceHead:              manifest.SourceHead,
		BinarySHA256:            manifest.BinarySHA256,
		HarnessSHA256:           manifest.HarnessSHA256,
		ObservationSHA256:       manifest.ObservationSHA256,
		CanonicalRoute:          manifest.CanonicalRoute,
		ResolvedTone:            manifest.ResolvedTone,
		Protocol:                manifest.Protocol,
		AccountProfileRef:       manifest.AccountProfileRef,
		EffectiveSettingsSHA256: manifest.EffectiveSettingsSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return AccountPoolCapabilityInputV1{CapabilityID: capabilityID, Evidence: validated.CanonicalJSON, EvidenceSHA256: validated.ChecksumSHA256}
}

func accountPoolTestEntry(t *testing.T, profileRef string, overrides map[string]Classification) AccountPoolRouteProtocolInputV1 {
	t.Helper()
	capabilities := make([]AccountPoolCapabilityInputV1, 0, 4)
	for _, capabilityID := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
		classification := ClassificationVerified
		if value, ok := overrides[capabilityID]; ok {
			classification = value
		}
		capabilities = append(capabilities, accountPoolTestManifest(t, profileRef, capabilityID, classification))
	}
	return AccountPoolRouteProtocolInputV1{
		CanonicalRoute:      accountPoolTestRoute,
		ResolvedTone:        accountPoolTestTone,
		Protocol:            accountPoolTestProtocol,
		UpstreamAttempts:    1,
		CrossAccountResends: 0,
		Capabilities:        capabilities,
	}
}

func accountPoolInputJSON(t *testing.T, profiles []AccountPoolProfileInputV1) []byte {
	t.Helper()
	raw, err := json.Marshal(AccountPoolInputV1{Schema: AccountPoolInputSchemaV1, Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBuildAccountPoolEvidenceUniformIntersection(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	input := accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, nil)}},
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
	})
	got, err := BuildAccountPoolEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != AccountPoolEvidenceSetSchemaV1 || got.ProfileSetSHA256 == "" || len(got.Profiles) != 2 || len(got.GlobalClaims) != 1 {
		t.Fatalf("set=%#v", got)
	}
	if got.Profiles[0].AccountProfileRef != profileA || got.Profiles[1].AccountProfileRef != profileB {
		t.Fatalf("profiles not sorted: %#v", got.Profiles)
	}
	claim := got.GlobalClaims[0]
	if claim.CanonicalRoute != accountPoolTestRoute || claim.Protocol != accountPoolTestProtocol || claim.EligibleProfileCount != 2 || claim.UnavailableProfileCount != 0 || claim.RouteEligibility != ClassificationVerified || claim.AccountDependent {
		t.Fatalf("claim=%#v", claim)
	}
	for _, capability := range claim.Capabilities {
		if capability.Classification != ClassificationVerified || len(capability.SupportingEvidenceSHA256) != 2 {
			t.Fatalf("capability=%#v", capability)
		}
	}

	reversed := accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, nil)}},
	})
	second, err := BuildAccountPoolEvidence(reversed)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(got)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("profile order changed deterministic output")
	}
}

func TestBuildAccountPoolEvidencePartialAndConflictingResults(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	partialRoute, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, map[string]Classification{"route_mapping": ClassificationUnsupported})}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if claim := partialRoute.GlobalClaims[0]; !claim.AccountDependent || claim.RouteEligibility != ClassificationInconclusive {
		t.Fatalf("partially eligible route claim=%#v", claim)
	}

	partial, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, map[string]Classification{"basic_text_delivery": ClassificationInconclusive})}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	claim := partial.GlobalClaims[0]
	if !claim.AccountDependent || claim.RouteEligibility != ClassificationVerified {
		t.Fatalf("partial claim=%#v", claim)
	}
	for _, capability := range claim.Capabilities {
		if capability.CapabilityID == "basic_text_delivery" && capability.Classification != ClassificationInconclusive {
			t.Fatalf("partial capability=%#v", capability)
		}
	}

	conflicting, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, map[string]Classification{"protocol_transport": ClassificationConfirmedDefect})}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	claim = conflicting.GlobalClaims[0]
	if !claim.AccountDependent {
		t.Fatalf("conflicting claim=%#v", claim)
	}
	for _, capability := range claim.Capabilities {
		if capability.CapabilityID == "protocol_transport" && capability.Classification != ClassificationInconclusive {
			t.Fatalf("conflicting capability=%#v", capability)
		}
	}
}

func TestBuildAccountPoolEvidenceMissingCapabilityIsAccountDependent(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	withoutBasicText := func(profile string) AccountPoolRouteProtocolInputV1 {
		entry := accountPoolTestEntry(t, profile, nil)
		filtered := make([]AccountPoolCapabilityInputV1, 0, len(entry.Capabilities)-1)
		for _, capability := range entry.Capabilities {
			if capability.CapabilityID != "basic_text_delivery" {
				filtered = append(filtered, capability)
			}
		}
		entry.Capabilities = filtered
		return entry
	}
	got, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{withoutBasicText(profileA)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{withoutBasicText(profileB)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	claim := got.GlobalClaims[0]
	if !claim.AccountDependent {
		t.Fatalf("missing capability was not account-dependent: %#v", claim)
	}
	for _, capability := range claim.Capabilities {
		if capability.CapabilityID == "basic_text_delivery" && capability.Classification != ClassificationInconclusive {
			t.Fatalf("missing capability classification=%#v", capability)
		}
	}
}

func TestBuildAccountPoolEvidenceUnavailableProfileAndProfileSetChange(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	one, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileUnavailable, UnavailableReason: "profile_not_ready"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if one.ProfileSetSHA256 == two.ProfileSetSHA256 {
		t.Fatal("profile-set change did not change identity")
	}
	claim := two.GlobalClaims[0]
	if claim.EligibleProfileCount != 1 || claim.UnavailableProfileCount != 1 || claim.AccountDependent || claim.RouteEligibility != ClassificationVerified {
		t.Fatalf("unavailable profile claim=%#v", claim)
	}
}

func TestBuildAccountPoolEvidenceRejectsMixedAcceptedInputIdentity(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	entryB := accountPoolTestEntry(t, profileB, nil)
	var manifest ManifestV1
	if err := json.Unmarshal(entryB.Capabilities[0].Evidence, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.EffectiveSettingsSHA256 = "4444444444444444444444444444444444444444444444444444444444444444"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateCapabilityEvidence(raw, IdentitySet{
		NormativeADRSHA256:      manifest.NormativeADRSHA256,
		SourceHead:              manifest.SourceHead,
		BinarySHA256:            manifest.BinarySHA256,
		HarnessSHA256:           manifest.HarnessSHA256,
		ObservationSHA256:       manifest.ObservationSHA256,
		CanonicalRoute:          manifest.CanonicalRoute,
		ResolvedTone:            manifest.ResolvedTone,
		Protocol:                manifest.Protocol,
		AccountProfileRef:       manifest.AccountProfileRef,
		EffectiveSettingsSHA256: manifest.EffectiveSettingsSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	entryB.Capabilities[0].Evidence = validated.CanonicalJSON
	entryB.Capabilities[0].EvidenceSHA256 = validated.ChecksumSHA256
	if _, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{entryB}},
	})); err == nil {
		t.Fatal("mixed effective-settings identity accepted")
	}
}

func TestValidateAccountPoolEvidenceSetExactAndStaleIdentity(t *testing.T) {
	profileA := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	profileB := "acct_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	set, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{
		{AccountProfileRef: profileA, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileA, nil)}},
		{AccountProfileRef: profileB, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profileB, nil)}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	expected := AccountPoolEvidenceSetExpected{
		JSONSHA256:              hex.EncodeToString(digest[:]),
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "49848dda87642f63b1beb15b2d87767e9edc4816",
		BinarySHA256:            "1111111111111111111111111111111111111111111111111111111111111111",
		HarnessSHA256:           "2222222222222222222222222222222222222222222222222222222222222222",
		EffectiveSettingsSHA256: "3333333333333333333333333333333333333333333333333333333333333333",
		ProfileSetSHA256:        set.ProfileSetSHA256,
	}
	validated, err := ValidateAccountPoolEvidenceSet(raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Set.ProfileSetSHA256 != set.ProfileSetSHA256 || validated.ChecksumSHA256 != expected.JSONSHA256 {
		t.Fatalf("validated=%#v", validated)
	}

	staleSource := expected
	staleSource.SourceHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ValidateAccountPoolEvidenceSet(raw, staleSource); err == nil {
		t.Fatal("stale source accepted")
	}
	staleProfileSet := expected
	staleProfileSet.ProfileSetSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ValidateAccountPoolEvidenceSet(raw, staleProfileSet); err == nil {
		t.Fatal("stale profile set accepted")
	}
}

func TestBuildAccountPoolEvidenceRejectsPrivacyAndCrossAccountResend(t *testing.T) {
	profile := "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	entry := accountPoolTestEntry(t, profile, nil)
	entry.CrossAccountResends = 1
	if _, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{{AccountProfileRef: profile, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{entry}}})); err == nil {
		t.Fatal("cross-account resend accepted")
	}
	if _, err := BuildAccountPoolEvidence(accountPoolInputJSON(t, []AccountPoolProfileInputV1{{AccountProfileRef: "person@example.com", Status: AccountPoolProfileUnavailable, UnavailableReason: "profile_not_ready"}})); err == nil {
		t.Fatal("identity-bearing profile ref accepted")
	}

	valid := accountPoolInputJSON(t, []AccountPoolProfileInputV1{{AccountProfileRef: profile, Status: AccountPoolProfileEligible, Matrix: []AccountPoolRouteProtocolInputV1{accountPoolTestEntry(t, profile, nil)}}})
	var object map[string]any
	if err := json.Unmarshal(valid, &object); err != nil {
		t.Fatal(err)
	}
	object["email"] = "forbidden@example.test"
	unknown, _ := json.Marshal(object)
	if _, err := BuildAccountPoolEvidence(unknown); err == nil {
		t.Fatal("unknown privacy field accepted")
	}
}
