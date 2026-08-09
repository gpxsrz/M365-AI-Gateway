package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

func TestCatalogWithoutAcceptedManifestEmitsNoVerifiedClaims(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	model := catalogModelByID(t, requestCatalog(t, harness), "m365-auto")
	if model["x_m365_standard_fields_source"] != "compatibility_default" {
		t.Fatalf("standard field provenance=%#v", model["x_m365_standard_fields_source"])
	}
	if model["x_m365_route_source"] != "registry_config" {
		t.Fatalf("route provenance=%#v", model["x_m365_route_source"])
	}
	if model["x_m365_mapping_source"] != "registry_config" {
		t.Fatalf("mapping provenance=%#v", model["x_m365_mapping_source"])
	}
	if model["x_m365_protocol_source"] != "none" || model["x_m365_evidence_source"] != "none" {
		t.Fatalf("unexpected evidence provenance: protocol=%#v evidence=%#v", model["x_m365_protocol_source"], model["x_m365_evidence_source"])
	}
	assertNoMultiAccountCatalogMetadata(t, model)

	// These legacy consumer fields intentionally remain present and true. The
	// new provenance must prevent clients from confusing compatibility defaults
	// with accepted WP2 verification.
	if model["supports_tools"] != true || model["supports_vision"] != true || model["supports_search_tool"] != true {
		t.Fatalf("legacy standard fields changed: %#v", model)
	}
}

func TestCatalogProjectsAcceptedIdentityEvidenceWithoutChangingStandardFields(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	baselineBody := requestCatalog(t, harness)
	raw, expected := catalogProjectionWebFixture(t)
	if err := configureCatalogEvidence(harness.server, raw, expected); err != nil {
		t.Fatal(err)
	}
	body := requestCatalog(t, harness)
	model := catalogModelByID(t, body, "m365-auto")

	if model["x_m365_mapping_source"] != "web_mapping" || model["x_m365_protocol_source"] != "none" || model["x_m365_evidence_source"] != "accepted_evidence" {
		t.Fatalf("accepted provenance not projected: %#v", model)
	}
	if model["x_m365_identity_evidence_sha256"] == "" || model["x_m365_accepted_mapping_evidence"] != "web_payload_verified" {
		t.Fatalf("accepted identity/mapping evidence not projected: %#v", model)
	}
	assertNoMultiAccountCatalogMetadata(t, model)

	if !reflect.DeepEqual(catalogStandardCatalog(baselineBody), catalogStandardCatalog(body)) {
		t.Fatalf("standard catalog fields changed\nbefore=%#v\nafter=%#v", catalogStandardCatalog(baselineBody), catalogStandardCatalog(body))
	}
	if !catalogContainsModel(body, "m365-copilot") {
		t.Fatal("accepted compatibility alias disappeared")
	}
	if catalogContainsModel(body, "claude") {
		t.Fatal("hidden request-only alias became catalog-visible")
	}
	alias := catalogModelByID(t, body, "m365-copilot")
	if alias["canonical_route"] != "m365-auto" || alias["x_m365_evidence_source"] != "accepted_evidence" || alias["x_m365_identity_evidence_sha256"] == "" {
		t.Fatalf("alias did not project canonical accepted evidence: %#v", alias)
	}
	assertNoMultiAccountCatalogMetadata(t, alias)
}

func TestCatalogDoesNotProjectHistoricalAccountIntersection(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	raw, expected := catalogProjectionWebFixture(t)
	if err := configureCatalogEvidence(harness.server, raw, expected); err != nil {
		t.Fatal(err)
	}
	model := catalogModelByID(t, requestCatalog(t, harness), "m365-gpt-5.6-think-deeper")
	if model["x_m365_evidence_source"] != "accepted_evidence" || model["x_m365_mapping_source"] != "web_mapping" || model["x_m365_protocol_source"] != "none" {
		t.Fatalf("accepted identity provenance not projected: %#v", model)
	}
	if model["x_m365_identity_evidence_sha256"] == "" || model["x_m365_accepted_mapping_evidence"] != "web_payload_verified" {
		t.Fatalf("accepted identity/mapping evidence not projected: %#v", model)
	}
	assertNoMultiAccountCatalogMetadata(t, model)
}

func TestCatalogProjectsCommittedAcceptedEvidenceThroughPublicHTTP(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	projection, err := defaultAcceptedWP2CatalogProjection(serverRuntimeSettings(harness.server))
	if err != nil {
		t.Fatal(err)
	}
	harness.server.catalogEvidence = projection
	body := requestCatalog(t, harness)

	// The committed WP2 package attested lowercase "magic". WP6 changed the
	// runtime route to the live-verified "Magic", so stale evidence must not be
	// rebound to that changed identity.
	auto := catalogModelByID(t, body, "m365-auto")
	if auto["x_m365_evidence_source"] != "none" || auto["x_m365_mapping_source"] != "registry_config" || auto["x_m365_protocol_source"] != "none" {
		t.Fatalf("m365-auto inherited stale WP2 provenance=%#v", auto)
	}
	assertNoMultiAccountCatalogMetadata(t, auto)

	thinkDeeper := catalogModelByID(t, body, "m365-gpt-5.6-think-deeper")
	if thinkDeeper["x_m365_evidence_source"] != "accepted_evidence" || thinkDeeper["x_m365_identity_evidence_sha256"] == "" || thinkDeeper["x_m365_protocol_source"] != "none" {
		t.Fatalf("committed identity evidence=%#v", thinkDeeper)
	}
	assertNoMultiAccountCatalogMetadata(t, thinkDeeper)
	for _, preset := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		model := catalogModelByID(t, body, preset)
		if model["route_kind"] != "preset" || model["x_m365_evidence_source"] != "accepted_evidence" || model["x_m365_identity_evidence_sha256"] == "" {
			t.Fatalf("committed preset visibility/provenance changed for %s: %#v", preset, model)
		}
		assertNoMultiAccountCatalogMetadata(t, model)
	}
	for _, hidden := range []string{"claude", "gpt-5.4-quick", "gpt-5.3-think-deeper", "quick", "think-deeper"} {
		if catalogContainsModel(body, hidden) {
			t.Fatalf("committed hidden alias became catalog-visible: %s", hidden)
		}
	}
}

func TestCatalogRebindsAcceptedEvidenceAfterRuntimeMappingChange(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	projection, err := defaultAcceptedWP2CatalogProjection(serverRuntimeSettings(harness.server))
	if err != nil {
		t.Fatal(err)
	}
	harness.server.catalogEvidence = projection
	before := catalogModelByID(t, requestCatalog(t, harness), "gpt-5.2")
	if before["x_m365_evidence_source"] != "accepted_evidence" {
		t.Fatalf("accepted legacy route missing initial evidence: %#v", before)
	}

	cfg := harness.server.settings.get()
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel: "gpt-5.2", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Runtime override", DefaultReasoningLevel: "low",
	})
	harness.server.settings.mu.Lock()
	harness.server.settings.v = cfg
	harness.server.settings.mu.Unlock()

	body := requestCatalog(t, harness)
	overridden := catalogModelByID(t, body, "gpt-5.2")
	if overridden["route_kind"] != "configured_mapping" || overridden["x_m365_evidence_source"] != "none" || overridden["x_m365_protocol_source"] != "none" {
		t.Fatalf("runtime mapping inherited stale accepted evidence: %#v", overridden)
	}
	assertNoMultiAccountCatalogMetadata(t, overridden)
	unchanged := catalogModelByID(t, body, "m365-gpt-5.6-think-deeper")
	if unchanged["x_m365_evidence_source"] != "accepted_evidence" || unchanged["x_m365_identity_evidence_sha256"] == "" {
		t.Fatalf("unrelated accepted route lost evidence: %#v", unchanged)
	}
	assertNoMultiAccountCatalogMetadata(t, unchanged)
}

func TestCatalogRejectsStaleManifestBeforePublicProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*evidence.CatalogProjectionManifestV1)
	}{
		{name: "source", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].SourceHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "binary", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].BinarySHA256 = webRepeatHex("a", 64)
		}},
		{name: "harness", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].HarnessSHA256 = webRepeatHex("b", 64)
		}},
		{name: "settings", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].EffectiveSettingsSHA256 = webRepeatHex("c", 64)
		}},
		{name: "profile_set", mutate: func(manifest *evidence.CatalogProjectionManifestV1) {
			manifest.Packages[3].ProfileSetSHA256 = webRepeatHex("d", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()

			raw, expected := catalogProjectionWebFixture(t)
			var stale evidence.CatalogProjectionManifestV1
			if err := json.Unmarshal(raw, &stale); err != nil {
				t.Fatal(err)
			}
			test.mutate(&stale)
			staleRaw, err := json.Marshal(stale)
			if err != nil {
				t.Fatal(err)
			}
			if err := configureCatalogEvidence(harness.server, staleRaw, expected); err == nil {
				t.Fatal("stale manifest accepted")
			}

			model := catalogModelByID(t, requestCatalog(t, harness), "m365-auto")
			if model["x_m365_evidence_source"] != "none" || model["x_m365_protocol_source"] != "none" {
				t.Fatalf("rejected evidence leaked into public catalog: %#v", model)
			}
			assertNoMultiAccountCatalogMetadata(t, model)
		})
	}
}

func TestCatalogOmitsMultiAccountMetadataFromDataAndModels(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	raw, expected := catalogProjectionWebFixture(t)
	if err := configureCatalogEvidence(harness.server, raw, expected); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data   []map[string]any `json:"data"`
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(requestCatalog(t, harness), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) == 0 || !reflect.DeepEqual(body.Data, body.Models) {
		t.Fatalf("catalog aliases changed: data=%d models=%d", len(body.Data), len(body.Models))
	}
	for _, model := range append(body.Data, body.Models...) {
		assertNoMultiAccountCatalogMetadata(t, model)
	}
}

func requestCatalog(t *testing.T, harness wp2RouteProtocolHarness) []byte {
	t.Helper()
	writer := httptest.NewRecorder()
	harness.serveWithAuth("api_key", writer, httptest.NewRequest("GET", "/v1/models", nil))
	if writer.Code != 200 {
		t.Fatalf("status=%d body=%s", writer.Code, writer.Body.String())
	}
	return writer.Body.Bytes()
}

func catalogModelByID(t *testing.T, raw []byte, id string) map[string]any {
	t.Helper()
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, model := range body.Data {
		if model["id"] == id {
			return model
		}
	}
	t.Fatalf("model %q not found", id)
	return nil
}

func catalogContainsModel(raw []byte, id string) bool {
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	for _, model := range body.Data {
		if model["id"] == id {
			return true
		}
	}
	return false
}

func assertNoMultiAccountCatalogMetadata(t *testing.T, model map[string]any) {
	t.Helper()
	for _, field := range []string{
		"account_dependent",
		"x_m365_profile_set_sha256",
		"eligible_profile_count",
		"unavailable_profile_count",
		"x_m365_protocol_claims",
		"x_m365_acceptance_manifest_sha256",
	} {
		if _, exists := model[field]; exists {
			t.Fatalf("model=%#v exposes removed multi-account field %q", model["id"], field)
		}
	}
}

func catalogStandardCatalog(raw []byte) map[string]map[string]any {
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	catalog := make(map[string]map[string]any, len(body.Data))
	for _, model := range body.Data {
		id, _ := model["id"].(string)
		catalog[id] = catalogStandardFields(model)
	}
	return catalog
}

func catalogStandardFields(model map[string]any) map[string]any {
	standard := make(map[string]any, len(model))
	for key, value := range model {
		if key == "created" || key == "account_dependent" || strings.HasPrefix(key, "x_m365_") {
			continue
		}
		standard[key] = value
	}
	return standard
}

func catalogProjectionWebFixture(t *testing.T) ([]byte, evidence.CatalogProjectionExpected) {
	t.Helper()
	packages := []evidence.CatalogProjectionPackageV1{
		webCatalogPackage(4, "route_protocol", "4"),
		webCatalogPackage(5, "alias_projection", "5"),
		webCatalogPackage(6, "legacy_configured", "6"),
		webCatalogPackage(7, "account_pool", "7"),
	}
	packages[3].ProfileSetSHA256 = webRepeatHex("9", 64)
	manifest := evidence.CatalogProjectionManifestV1{
		Schema:           evidence.CatalogProjectionManifestSchemaV1,
		AcceptanceStatus: evidence.CatalogProjectionAccepted,
		Packages:         packages,
		Identities: []evidence.CatalogProjectionIdentityEvidenceV1{
			webCatalogIdentity("claude", "claude-sonnet", "Claude_Sonnet", "alias", "hidden", true, "api_tone_accepted", "accepted_unverified", 5, "1"),
			webCatalogIdentity("gpt-5.6-reasoning", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", "alias", "compatibility", true, "web_payload_verified", "accepted_unverified", 5, "2"),
			webCatalogIdentity("m365-auto", "m365-auto", "Magic", "web_mode", "public", false, "web_payload_verified", "dynamic_unidentified", 4, "3"),
			webCatalogIdentity("m365-copilot", "m365-auto", "Magic", "alias", "compatibility", true, "web_payload_verified", "dynamic_unidentified", 5, "4"),
			webCatalogIdentity("m365-gpt-5.6-think-deeper", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", "web_model_route", "public", false, "web_payload_verified", "accepted_unverified", 4, "5"),
		},
		GlobalClaims: []evidence.AccountPoolGlobalClaimV1{
			webCatalogClaim("m365-auto", "Magic", "openai_chat_completions_nonstream", evidence.ClassificationVerified, false, "a"),
			webCatalogClaim("m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", "openai_chat_completions_nonstream", evidence.ClassificationVerified, false, "b"),
			webCatalogClaim("m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", "openai_responses_nonstream", evidence.ClassificationInconclusive, true, "c"),
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return raw, evidence.CatalogProjectionExpected{
		ManifestSHA256: hex.EncodeToString(digest[:]),
		Packages:       packages,
	}
}

func webCatalogIdentity(requested, canonical, tone, kind, visibility string, compatibility bool, mapping, identity string, issue int, seed string) evidence.CatalogProjectionIdentityEvidenceV1 {
	supporting := []string{webRepeatHex(seed, 64)}
	setSHA, err := evidence.CatalogProjectionEvidenceSetSHA256(supporting)
	if err != nil {
		panic(err)
	}
	catalogObservation := ""
	if issue != 4 {
		catalogObservation = webRepeatHex(seed, 64)
	}
	return evidence.CatalogProjectionIdentityEvidenceV1{
		RequestedIdentity: requested, CanonicalRoute: canonical, ResolvedTone: tone,
		RouteKind: kind, CatalogVisibility: visibility, CompatibilityRequired: compatibility,
		MappingEvidence: mapping, IdentityStatus: identity, PackageIssue: issue,
		CatalogObservationSHA256: catalogObservation, SupportingEvidenceSHA256: supporting,
		CapabilityEvidenceSetSHA256: setSHA,
	}
}

func webCatalogPackage(issue int, kind, seed string) evidence.CatalogProjectionPackageV1 {
	return evidence.CatalogProjectionPackageV1{
		Issue: issue, Kind: kind,
		NormativeADRSHA256:      webRepeatHex(seed, 64),
		SourceHead:              webRepeatHex(seed, 40),
		BinarySHA256:            webRepeatHex(seed, 64),
		HarnessSHA256:           webRepeatHex(seed, 64),
		EffectiveSettingsSHA256: webRepeatHex(seed, 64),
		EvidenceIndexSHA256:     webRepeatHex(seed, 64),
		PayloadJSONSHA256:       webRepeatHex(seed, 64),
	}
}

func webCatalogClaim(route, tone, protocol string, classification evidence.Classification, dependent bool, seed string) evidence.AccountPoolGlobalClaimV1 {
	capabilities := make([]evidence.AccountPoolGlobalCapabilityClaimV1, 0, 4)
	for _, capabilityID := range []string{"route_identity", "route_mapping", "basic_text_delivery", "protocol_transport"} {
		hashes := []string{webEvidenceHash(seed, capabilityID, "0")}
		if classification == evidence.ClassificationVerified {
			hashes = append(hashes, webEvidenceHash(seed, capabilityID, "1"))
		}
		if hashes[0] > hashes[len(hashes)-1] {
			hashes[0], hashes[len(hashes)-1] = hashes[len(hashes)-1], hashes[0]
		}
		capabilities = append(capabilities, evidence.AccountPoolGlobalCapabilityClaimV1{
			CapabilityID: capabilityID, Classification: classification, SupportingEvidenceSHA256: hashes,
		})
	}
	return evidence.AccountPoolGlobalClaimV1{
		CanonicalRoute: route, ResolvedTone: tone, Protocol: protocol,
		EligibleProfileCount: 2, UnavailableProfileCount: 1, RouteEligibility: classification,
		AccountDependent: dependent, Capabilities: capabilities,
	}
}

func webEvidenceHash(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func webRepeatHex(value string, count int) string {
	out := ""
	for len(out) < count {
		out += value
	}
	return out[:count]
}
