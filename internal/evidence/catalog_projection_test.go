package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestValidateCatalogProjectionManifestAcceptsExactEvidence(t *testing.T) {
	manifest := catalogProjectionTestManifest()
	raw := mustCatalogProjectionJSON(t, manifest)
	validated, err := ValidateCatalogProjectionManifest(raw, CatalogProjectionExpected{
		ManifestSHA256: catalogProjectionDigest(raw),
		Packages:       manifest.Packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if validated.ChecksumSHA256 != catalogProjectionDigest(raw) {
		t.Fatalf("checksum=%s", validated.ChecksumSHA256)
	}
	identity, ok := findCatalogProjectionIdentityForTest(validated, "m365-auto")
	if !ok || identity.CanonicalRoute != "m365-auto" || identity.PackageIssue != 4 {
		t.Fatalf("identity=%#v ok=%t", identity, ok)
	}
	claims := catalogProjectionTestClaims(validated, "m365-auto", "magic")
	if len(claims) != 1 || claims[0].Protocol != "openai_chat_completions_nonstream" || claims[0].RouteEligibility != ClassificationVerified || claims[0].AccountDependent {
		t.Fatalf("claims=%#v", claims)
	}
}

func findCatalogProjectionIdentityForTest(validated ValidatedCatalogProjection, requested string) (CatalogProjectionIdentityEvidenceV1, bool) {
	for _, identity := range validated.Manifest.Identities {
		if identity.RequestedIdentity == requested {
			return identity, true
		}
	}
	return CatalogProjectionIdentityEvidenceV1{}, false
}

func catalogProjectionTestClaims(validated ValidatedCatalogProjection, route, tone string) []AccountPoolGlobalClaimV1 {
	claims := make([]AccountPoolGlobalClaimV1, 0)
	for _, claim := range validated.Manifest.GlobalClaims {
		if claim.CanonicalRoute == route && claim.ResolvedTone == tone {
			claims = append(claims, claim)
		}
	}
	return claims
}

func TestValidateCatalogProjectionManifestRejectsStaleIdentity(t *testing.T) {
	original := catalogProjectionTestManifest()
	originalRaw := mustCatalogProjectionJSON(t, original)
	expected := CatalogProjectionExpected{
		ManifestSHA256: catalogProjectionDigest(originalRaw),
		Packages:       original.Packages,
	}

	mutations := map[string]func(*CatalogProjectionManifestV1){
		"source": func(m *CatalogProjectionManifestV1) {
			m.Packages[3].SourceHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"binary":      func(m *CatalogProjectionManifestV1) { m.Packages[3].BinarySHA256 = repeatHex("a", 64) },
		"harness":     func(m *CatalogProjectionManifestV1) { m.Packages[3].HarnessSHA256 = repeatHex("b", 64) },
		"settings":    func(m *CatalogProjectionManifestV1) { m.Packages[3].EffectiveSettingsSHA256 = repeatHex("c", 64) },
		"profile_set": func(m *CatalogProjectionManifestV1) { m.Packages[3].ProfileSetSHA256 = repeatHex("d", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := catalogProjectionTestManifest()
			mutate(&candidate)
			if _, err := ValidateCatalogProjectionManifest(mustCatalogProjectionJSON(t, candidate), expected); err == nil {
				t.Fatal("stale manifest accepted")
			}
		})
	}
}

func TestValidateCatalogProjectionManifestRejectsAdvancedVerification(t *testing.T) {
	manifest := catalogProjectionTestManifest()
	manifest.GlobalClaims[0].Capabilities = append(manifest.GlobalClaims[0].Capabilities, AccountPoolGlobalCapabilityClaimV1{
		CapabilityID:             "tools",
		Classification:           ClassificationVerified,
		SupportingEvidenceSHA256: []string{repeatHex("e", 64)},
	})
	raw := mustCatalogProjectionJSON(t, manifest)
	if _, err := ValidateCatalogProjectionManifest(raw, CatalogProjectionExpected{
		ManifestSHA256: catalogProjectionDigest(raw),
		Packages:       manifest.Packages,
	}); err == nil {
		t.Fatal("advanced capability verification accepted")
	}
}

func catalogProjectionTestManifest() CatalogProjectionManifestV1 {
	packages := []CatalogProjectionPackageV1{
		catalogProjectionTestPackage(4, "route_protocol", "4"),
		catalogProjectionTestPackage(5, "alias_projection", "5"),
		catalogProjectionTestPackage(6, "legacy_configured", "6"),
		catalogProjectionTestPackage(7, "account_pool", "7"),
	}
	packages[3].ProfileSetSHA256 = repeatHex("9", 64)
	return CatalogProjectionManifestV1{
		Schema:           CatalogProjectionManifestSchemaV1,
		AcceptanceStatus: CatalogProjectionAccepted,
		Packages:         packages,
		Identities: []CatalogProjectionIdentityEvidenceV1{
			catalogProjectionTestIdentity("m365-auto", "m365-auto", "magic", "web_mode", "public", false, "web_payload_verified", "dynamic_unidentified", 4, "1"),
			catalogProjectionTestIdentity("m365-copilot", "m365-auto", "magic", "alias", "compatibility", true, "web_payload_verified", "dynamic_unidentified", 5, "2"),
		},
		GlobalClaims: []AccountPoolGlobalClaimV1{
			{
				CanonicalRoute: "m365-auto", ResolvedTone: "magic", Protocol: "openai_chat_completions_nonstream",
				EligibleProfileCount: 2, UnavailableProfileCount: 1, RouteEligibility: ClassificationVerified,
				Capabilities: []AccountPoolGlobalCapabilityClaimV1{
					{CapabilityID: "route_identity", Classification: ClassificationVerified, SupportingEvidenceSHA256: []string{repeatHex("3", 64), repeatHex("4", 64)}},
					{CapabilityID: "route_mapping", Classification: ClassificationVerified, SupportingEvidenceSHA256: []string{repeatHex("5", 64), repeatHex("6", 64)}},
					{CapabilityID: "basic_text_delivery", Classification: ClassificationVerified, SupportingEvidenceSHA256: []string{repeatHex("7", 64), repeatHex("8", 64)}},
					{CapabilityID: "protocol_transport", Classification: ClassificationVerified, SupportingEvidenceSHA256: []string{repeatHex("a", 64), repeatHex("b", 64)}},
				},
			},
		},
	}
}

func catalogProjectionTestIdentity(requested, canonical, tone, kind, visibility string, compatibility bool, mapping, identity string, issue int, seed string) CatalogProjectionIdentityEvidenceV1 {
	supporting := []string{repeatHex(seed, 64)}
	setSHA, err := CatalogProjectionEvidenceSetSHA256(supporting)
	if err != nil {
		panic(err)
	}
	catalogObservation := ""
	if issue != 4 {
		catalogObservation = repeatHex(seed, 64)
	}
	return CatalogProjectionIdentityEvidenceV1{
		RequestedIdentity: requested, CanonicalRoute: canonical, ResolvedTone: tone,
		RouteKind: kind, CatalogVisibility: visibility, CompatibilityRequired: compatibility,
		MappingEvidence: mapping, IdentityStatus: identity, PackageIssue: issue,
		CatalogObservationSHA256: catalogObservation, SupportingEvidenceSHA256: supporting,
		CapabilityEvidenceSetSHA256: setSHA,
	}
}

func catalogProjectionTestPackage(issue int, kind, seed string) CatalogProjectionPackageV1 {
	return CatalogProjectionPackageV1{
		Issue: issue, Kind: kind,
		NormativeADRSHA256:      repeatHex(seed, 64),
		SourceHead:              repeatHex(seed, 40),
		BinarySHA256:            repeatHex(seed, 64),
		HarnessSHA256:           repeatHex(seed, 64),
		EffectiveSettingsSHA256: repeatHex(seed, 64),
		EvidenceIndexSHA256:     repeatHex(seed, 64),
		PayloadJSONSHA256:       repeatHex(seed, 64),
	}
}

func mustCatalogProjectionJSON(t *testing.T, value CatalogProjectionManifestV1) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func catalogProjectionDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func repeatHex(value string, count int) string {
	out := ""
	for len(out) < count {
		out += value
	}
	return out[:count]
}
