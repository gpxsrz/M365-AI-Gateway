package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

var historicalPassIDs = []string{
	"api_key_auth",
	"model_catalog",
	"nonstream_text",
	"json_response_format",
	"chat_streaming",
	"responses_nonstream",
	"responses_streaming",
	"anthropic_nonstream",
	"anthropic_streaming",
	"reasoning_levels",
	"invalid_reasoning_rejection",
	"function_calling",
	"tool_result_continuation",
	"responses_function_calling",
	"responses_tool_result_continuation",
	"responses_tool_streaming",
	"anthropic_tool_calling",
	"anthropic_tool_result_continuation",
	"anthropic_tool_streaming",
	"responses_custom_exec_tool",
	"multi_turn_conversation_ids",
	"session_key_persistence",
	"responses_previous_response_id",
	"bing_web_search",
	"vision_image_input",
	"context_boundary",
	"hermes_cli",
	"discord_gateway",
	"api_key_account_restart_persistence",
}

var historicalFailIDs = []string{
	"unknown_model_fail_closed",
	"empty_response_handling",
	"parallel_tool_calls",
	"max_tokens_enforcement",
	"file_attachment",
	"image_generation",
	"output_boundary",
	"sidecar_restart_persistence",
	"reasoning_summary",
	"verbosity_control",
	"openai_web_search_tool",
	"image_detail_original",
	"file_upload_api",
	"file_read_api",
	"custom_exec_native_search_preservation",
	"auto_route_reasoning_invariance",
	"context_overflow_rejection",
	"apply_patch_freeform_tool",
	"json_schema_strict",
}

func TestBuildHistoricalBaselineInventory(t *testing.T) {
	fixture := historicalFixtureFS(t)
	source, err := buildHistoricalFixtureSource(t, fixture, acceptedHistoricalBinaryFixture())
	if err != nil {
		t.Fatalf("BuildHistoricalBaselineSource: %v", err)
	}
	if got, want := len(source.Source.Entries), 48; got != want {
		t.Fatalf("source entries=%d, want %d", got, want)
	}
	if source.Source.PassCount != 29 || source.Source.FailCount != 19 {
		t.Fatalf("source counts=%d/%d, want 29/19", source.Source.PassCount, source.Source.FailCount)
	}

	catalog := historicalCatalogFixture(t)
	inventory, err := BuildHistoricalBaselineInventory(source, catalog)
	if err != nil {
		t.Fatalf("BuildHistoricalBaselineInventory: %v", err)
	}
	if got, want := len(inventory.Inventory.Entries), 48; got != want {
		t.Fatalf("inventory entries=%d, want %d", got, want)
	}

	counts := map[Classification]int{}
	byID := map[string]HistoricalBaselineDispositionV1{}
	for _, entry := range inventory.Inventory.Entries {
		counts[entry.Classification]++
		if _, exists := byID[entry.CapabilityID]; exists {
			t.Fatalf("duplicate capability %q", entry.CapabilityID)
		}
		byID[entry.CapabilityID] = entry
	}
	if counts[ClassificationVerified] != 2 || counts[ClassificationConfirmedDefect] != 8 || counts[ClassificationUnsupported] != 2 || counts[ClassificationInconclusive] != 36 {
		t.Fatalf("classification counts=%v, want VERIFIED=2 CONFIRMED_DEFECT=8 UNSUPPORTED=2 INCONCLUSIVE=36", counts)
	}

	for id, want := range map[string]Classification{
		"nonstream_text":                         ClassificationVerified,
		"anthropic_nonstream":                    ClassificationVerified,
		"responses_nonstream":                    ClassificationInconclusive,
		"unknown_model_fail_closed":              ClassificationConfirmedDefect,
		"empty_response_handling":                ClassificationConfirmedDefect,
		"max_tokens_enforcement":                 ClassificationConfirmedDefect,
		"custom_exec_native_search_preservation": ClassificationConfirmedDefect,
		"auto_route_reasoning_invariance":        ClassificationConfirmedDefect,
		"context_overflow_rejection":             ClassificationConfirmedDefect,
		"image_detail_original":                  ClassificationConfirmedDefect,
		"json_schema_strict":                     ClassificationConfirmedDefect,
		"file_upload_api":                        ClassificationUnsupported,
		"file_read_api":                          ClassificationUnsupported,
		"parallel_tool_calls":                    ClassificationInconclusive,
		"output_boundary":                        ClassificationInconclusive,
		"reasoning_levels":                       ClassificationInconclusive,
	} {
		if got := byID[id].Classification; got != want {
			t.Errorf("%s classification=%s, want %s", id, got, want)
		}
	}

	for _, id := range []string{"nonstream_text", "anthropic_nonstream"} {
		entry := byID[id]
		if len(entry.AcceptedSupport) != 6 {
			t.Fatalf("%s support=%d, want 6 route/capability records", id, len(entry.AcceptedSupport))
		}
		for _, support := range entry.AcceptedSupport {
			if support.Classification != ClassificationVerified || support.AccountDependent {
				t.Fatalf("%s has non-global support: %#v", id, support)
			}
			if support.CapabilityID != "basic_text_delivery" && support.CapabilityID != "protocol_transport" {
				t.Fatalf("%s promoted advanced support %q", id, support.CapabilityID)
			}
		}
	}
	if got := byID["responses_nonstream"]; got.Classification != ClassificationInconclusive || len(got.AcceptedSupport) != 6 || len(got.MissingEvidence) == 0 {
		t.Fatalf("responses_nonstream did not preserve partial accepted support: %#v", got)
	}
	for _, id := range append(append([]string{}, historicalPassIDs...), historicalFailIDs...) {
		entry, ok := byID[id]
		if !ok {
			t.Errorf("missing disposition for %q", id)
			continue
		}
		if entry.RationaleCode == "" {
			t.Errorf("%s has no rationale", id)
		}
	}

	second, err := BuildHistoricalBaselineInventory(source, catalog)
	if err != nil {
		t.Fatalf("second BuildHistoricalBaselineInventory: %v", err)
	}
	if string(inventory.CanonicalJSON) != string(second.CanonicalJSON) || inventory.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("inventory output is not deterministic")
	}
	validated, err := ValidateHistoricalBaselineInventory(inventory.CanonicalJSON, HistoricalBaselineInventoryExpected{
		HistoricalSource: source,
		AcceptedCatalog:  catalog,
	})
	if err != nil {
		t.Fatalf("ValidateHistoricalBaselineInventory: %v", err)
	}
	if validated.ChecksumSHA256 != inventory.ChecksumSHA256 {
		t.Fatalf("validated checksum=%s, want %s", validated.ChecksumSHA256, inventory.ChecksumSHA256)
	}
}

func TestHistoricalBaselineFailClosed(t *testing.T) {
	fixture := historicalFixtureFS(t)
	binary := acceptedHistoricalBinaryFixture()

	source, err := buildHistoricalFixtureSource(t, fixture, binary)
	if err != nil {
		t.Fatalf("BuildHistoricalBaselineSource: %v", err)
	}
	wrongBinary := binary
	wrongBinary.SHA256 = strings.Repeat("0", 64)
	if _, err := buildHistoricalFixtureSource(t, historicalFixtureFS(t), wrongBinary); validationCode(err) != "identity_mismatch" {
		t.Fatalf("wrong binary error=%v, want identity_mismatch", err)
	}
	assertHistoricalPrivacySafe(t, source.CanonicalJSON)

	artifactTamper := historicalFixtureFS(t)
	expectedArtifacts := historicalFixtureArtifactIdentities(t, artifactTamper)
	artifactTamper["final-matrix.md"].Data = append(append([]byte(nil), artifactTamper["final-matrix.md"].Data...), []byte("tampered")...)
	if _, err := buildHistoricalBaselineSource(artifactTamper, ".", binary, expectedArtifacts); validationCode(err) != "identity_mismatch" {
		t.Fatalf("artifact tamper error=%v, want identity_mismatch", err)
	}

	drifted := historicalFixtureFS(t)
	var progress map[string]any
	if err := json.Unmarshal(drifted["progress.json"].Data, &progress); err != nil {
		t.Fatal(err)
	}
	progress["capabilities"].(map[string]any)["nonstream_text"].(map[string]any)["status"] = "FAIL"
	drifted["progress.json"].Data = mustJSON(t, progress)
	if _, err := buildHistoricalFixtureSource(t, drifted, binary); validationCode(err) != "identity_mismatch" {
		t.Fatalf("progress drift error=%v, want identity_mismatch", err)
	}

	missingReplay := historicalFixtureFS(t)
	missingReplay["phase1-details.json"].Data = mustJSON(t, map[string]any{"phase": 1, "results": map[string]any{}})
	missingSource, err := buildHistoricalFixtureSource(t, missingReplay, binary)
	if err != nil {
		t.Fatalf("missing replay source: %v", err)
	}
	inventory, err := BuildHistoricalBaselineInventory(missingSource, historicalCatalogFixture(t))
	if err != nil {
		t.Fatalf("missing replay inventory: %v", err)
	}
	for _, entry := range inventory.Inventory.Entries {
		if entry.CapabilityID == "unknown_model_fail_closed" && (entry.Classification != ClassificationInconclusive || entry.RationaleCode != "defect_replay_missing") {
			t.Fatalf("missing replay did not fail closed: %#v", entry)
		}
	}

	catalog := historicalCatalogFixture(t)
	valid, err := BuildHistoricalBaselineInventory(source, catalog)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoricalPrivacySafe(t, valid.CanonicalJSON)

	wrongScope := source
	wrongScope.Source.Entries = append([]HistoricalBaselineSourceEntryV1(nil), source.Source.Entries...)
	for index := range wrongScope.Source.Entries {
		if wrongScope.Source.Entries[index].CapabilityID != "unknown_model_fail_closed" {
			continue
		}
		replay := *wrongScope.Source.Entries[index].ReplayEvidence
		replay.Scope = "account_or_upstream_condition"
		wrongScope.Source.Entries[index].ReplayEvidence = &replay
		break
	}
	wrongScope.CanonicalJSON, wrongScope.ChecksumSHA256, err = marshalHistoricalValue(wrongScope.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildHistoricalBaselineInventory(wrongScope, catalog); validationCode(err) != "identity_mismatch" {
		t.Fatalf("wrong replay scope error=%v, want identity_mismatch", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(valid.CanonicalJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["extra"] = true
	if _, err := ValidateHistoricalBaselineInventory(mustJSON(t, decoded), HistoricalBaselineInventoryExpected{HistoricalSource: source, AcceptedCatalog: catalog}); validationCode(err) != "unknown_field" {
		t.Fatalf("unknown field error=%v, want unknown_field", err)
	}

	tampered := bytes.Replace(valid.CanonicalJSON, []byte("accepted_global_basic_transport"), []byte("historical_pass_not_wp2_verified"), 1)
	if _, err := ValidateHistoricalBaselineInventory(tampered, HistoricalBaselineInventoryExpected{HistoricalSource: source, AcceptedCatalog: catalog}); validationCode(err) != "identity_mismatch" {
		t.Fatalf("tampered inventory error=%v, want identity_mismatch", err)
	}
}

func validationCode(err error) string {
	if typed, ok := err.(*ValidationError); ok {
		return typed.Code
	}
	return ""
}

func acceptedHistoricalBinaryFixture() HistoricalBinaryIdentityV1 {
	return HistoricalBinaryIdentityV1{
		SHA256:         historicalBinarySHA256V1,
		Bytes:          historicalBinaryBytesV1,
		SourceHead:     historicalBinarySourceHeadV1,
		SourceModified: true,
	}
}

func buildHistoricalFixtureSource(t *testing.T, fixture fstest.MapFS, binary HistoricalBinaryIdentityV1) (ValidatedHistoricalBaselineSource, error) {
	t.Helper()
	return buildHistoricalBaselineSource(fixture, ".", binary, historicalFixtureArtifactIdentities(t, fixture))
}

func historicalFixtureArtifactIdentities(t *testing.T, fixture fstest.MapFS) []HistoricalArtifactIdentityV1 {
	t.Helper()
	result := make([]HistoricalArtifactIdentityV1, 0, len(historicalArtifactIdentitiesV1))
	for _, accepted := range historicalArtifactIdentitiesV1 {
		file, ok := fixture[accepted.Path]
		if !ok {
			t.Fatalf("fixture missing %s", accepted.Path)
		}
		digest := sha256.Sum256(file.Data)
		result = append(result, HistoricalArtifactIdentityV1{
			Path:   accepted.Path,
			Bytes:  int64(len(file.Data)),
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	return result
}

func assertHistoricalPrivacySafe(t *testing.T, raw []byte) {
	t.Helper()
	for _, forbidden := range []string{
		"historical issue",
		"fixture-prompt-secret",
		"fixture-response-secret",
		"fixture-cookie-secret",
		"fixture-token-secret",
		"fixture-account-secret",
		"fixture@example.invalid",
		"127.0.0.1",
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("privacy-safe output retained %q", forbidden)
		}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	forbiddenKeys := map[string]struct{}{
		"prompt": {}, "prompts": {}, "request_body": {}, "response_body": {}, "messages": {},
		"cookie": {}, "token": {}, "api_key": {}, "email": {}, "tenant_id": {}, "object_id": {},
		"account_id": {}, "account_mapping": {}, "attachment_content": {}, "details": {}, "summary": {}, "target": {},
	}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if _, forbidden := forbiddenKeys[key]; forbidden {
					t.Fatalf("privacy-safe output retained forbidden key %q", key)
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
}

func historicalFixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()
	capabilities := map[string]any{}
	final := make([]any, 0, 48)
	for _, id := range historicalPassIDs {
		capabilities[id] = map[string]any{"status": "PASS", "attempts": 1, "summary": id, "evidence": []any{}}
		final = append(final, map[string]any{"capability": id, "status": "PASS", "classification": "verified_working", "attempts": 1, "summary": id, "evidence": []any{}})
	}
	for _, id := range historicalFailIDs {
		capabilities[id] = map[string]any{"status": "FAIL", "attempts": 1, "summary": id, "evidence": []any{}}
		final = append(final, map[string]any{"capability": id, "status": "FAIL", "classification": "historical_failure", "attempts": 1, "summary": id, "evidence": []any{}})
	}
	capabilities["api_key_auth"].(map[string]any)["prompt"] = "fixture-prompt-secret"
	capabilities["api_key_auth"].(map[string]any)["response_body"] = "fixture-response-secret"
	capabilities["api_key_auth"].(map[string]any)["cookie"] = "fixture-cookie-secret"
	capabilities["api_key_auth"].(map[string]any)["token"] = "fixture-token-secret"
	capabilities["api_key_auth"].(map[string]any)["account_id"] = "fixture-account-secret"
	capabilities["api_key_auth"].(map[string]any)["email"] = "fixture@example.invalid"

	issues := make([]byte, 0)
	for index := 0; index < 27; index++ {
		row := map[string]any{
			"capability":    historicalFailIDs[index%len(historicalFailIDs)],
			"details":       map[string]any{"attempt": index + 1, "account_mapping": "fixture-account-secret"},
			"summary":       "historical issue",
			"prompt":        "fixture-prompt-secret",
			"response_body": "fixture-response-secret",
			"time":          "2026-08-02T00:00:00Z",
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, encoded...)
		issues = append(issues, '\n')
	}

	return fstest.MapFS{
		"progress.json": {Data: mustJSON(t, map[string]any{
			"version":      1,
			"started_at":   "2026-08-02T00:25:15Z",
			"target":       "http://127.0.0.1:4141",
			"capabilities": capabilities,
			"updated_at":   "2026-08-02T01:00:51Z",
		})},
		"final-matrix.json": {Data: mustJSON(t, map[string]any{
			"version":               1,
			"total":                 48,
			"status_counts":         map[string]any{"PASS": 29, "FAIL": 19},
			"classification_counts": map[string]any{"historical": 48},
			"completed_at":          "2026-08-02T01:02:45Z",
			"capabilities":          final,
		})},
		"final-matrix.md": {Data: []byte("# Historical matrix\n")},
		"issues.jsonl":    {Data: issues},
		"phase1-details.json": {Data: mustJSON(t, map[string]any{"phase": 1, "results": map[string]any{
			"unknown_model_fail_closed": map[string]any{"http_status": 200, "content_length": 113},
			"empty_response_handling":   map[string]any{"http_status": 200, "content_length": 0},
		}})},
		"phase2-details.json": {Data: mustJSON(t, map[string]any{"phase": 2, "results": map[string]any{}})},
		"phase3-details.json": {Data: mustJSON(t, map[string]any{"phase": 3, "results": map[string]any{
			"max_tokens_enforcement": map[string]any{"cases": []any{
				map[string]any{"max_tokens": 1, "http_status": 200, "content_length": 979},
				map[string]any{"max_tokens": 8, "http_status": 200, "content_length": 1136},
			}},
			"custom_exec_native_search_preservation": map[string]any{"rows": []any{
				map[string]any{"search_signal_count": 1, "url_count": 0, "keyword_hits": map[string]any{"sourceattribution": 0}},
				map[string]any{"search_signal_count": 1, "url_count": 0, "keyword_hits": map[string]any{"sourceattribution": 0}},
			}},
			"auto_route_reasoning_invariance": map[string]any{"code_has_auto_invariance_guard": false, "none_marker": true, "medium_marker": false},
			"context_overflow_rejection":      map[string]any{"approx_words": 140000, "prompt_chars": 840038, "http_status": 200, "end_marker_seen": true},
			"image_detail_original":           map[string]any{"detail_field_used_in_payload_builder": false, "original_http": 200, "invalid_http": 200, "original_bytes": 436, "invalid_bytes": 436},
			"file_upload_api":                 map[string]any{"http_status": 404, "response_bytes": 19},
			"file_read_api":                   map[string]any{"http_status": 404, "response_bytes": 19},
		}})},
		"phase4-details.json": {Data: mustJSON(t, map[string]any{"phase": 4, "results": map[string]any{
			"json_schema_strict": map[string]any{"valid_http": 200, "valid_schema_match": false, "invalid_http": 200},
		}})},
		"phase5-context-details.json": {Data: mustJSON(t, map[string]any{"status": "FAIL", "details": map[string]any{}})},
	}
}

func historicalCatalogFixture(t *testing.T) ValidatedCatalogProjection {
	t.Helper()
	packages := make([]CatalogProjectionPackageV1, 0, 4)
	for issue := 4; issue <= 7; issue++ {
		pkg := CatalogProjectionPackageV1{
			Issue:                   issue,
			Kind:                    catalogProjectionPackageKinds[issue],
			NormativeADRSHA256:      strings.Repeat("1", 64),
			SourceHead:              strings.Repeat(string(rune('0'+issue)), 40),
			BinarySHA256:            strings.Repeat("2", 64),
			HarnessSHA256:           strings.Repeat("3", 64),
			EffectiveSettingsSHA256: strings.Repeat("4", 64),
			EvidenceIndexSHA256:     strings.Repeat("5", 64),
			PayloadJSONSHA256:       strings.Repeat("6", 64),
		}
		if issue == 7 {
			pkg.ProfileSetSHA256 = strings.Repeat("7", 64)
		}
		packages = append(packages, pkg)
	}

	routes := []struct {
		id   string
		tone string
	}{
		{"m365-auto", "magic"},
		{"m365-gpt-5.5-quick-response", "Gpt_5_5_Chat"},
		{"m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning"},
	}
	identities := make([]CatalogProjectionIdentityEvidenceV1, 0, len(routes))
	for index, route := range routes {
		hashes := []string{strings.Repeat(string(rune('a'+index)), 64)}
		setSHA, err := CatalogProjectionEvidenceSetSHA256(hashes)
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, CatalogProjectionIdentityEvidenceV1{
			RequestedIdentity:           route.id,
			CanonicalRoute:              route.id,
			ResolvedTone:                route.tone,
			RouteKind:                   "web_model_route",
			CatalogVisibility:           "public",
			MappingEvidence:             "web_payload_verified",
			IdentityStatus:              "upstream_identity_verified",
			PackageIssue:                4,
			SupportingEvidenceSHA256:    hashes,
			CapabilityEvidenceSetSHA256: setSHA,
		})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].RequestedIdentity < identities[j].RequestedIdentity })

	protocols := []string{
		"anthropic_messages_nonstream",
		"openai_chat_completions_nonstream",
		"openai_responses_nonstream",
	}
	claims := make([]AccountPoolGlobalClaimV1, 0, len(routes)*len(protocols))
	for _, route := range routes {
		for _, protocol := range protocols {
			classification := ClassificationVerified
			accountDependent := false
			if route.id == "m365-gpt-5.6-think-deeper" && protocol == "openai_responses_nonstream" {
				classification = ClassificationInconclusive
				accountDependent = true
			}
			capabilities := make([]AccountPoolGlobalCapabilityClaimV1, 0, len(accountPoolCapabilityOrder))
			for _, capabilityID := range accountPoolCapabilityOrder {
				capabilities = append(capabilities, AccountPoolGlobalCapabilityClaimV1{
					CapabilityID:             capabilityID,
					Classification:           classification,
					SupportingEvidenceSHA256: []string{strings.Repeat("d", 64)},
				})
			}
			claims = append(claims, AccountPoolGlobalClaimV1{
				CanonicalRoute:          route.id,
				ResolvedTone:            route.tone,
				Protocol:                protocol,
				EligibleProfileCount:    2,
				UnavailableProfileCount: 1,
				RouteEligibility:        classification,
				AccountDependent:        accountDependent,
				Capabilities:            capabilities,
			})
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		return accountPoolEntryKey(claims[i].CanonicalRoute, claims[i].Protocol) < accountPoolEntryKey(claims[j].CanonicalRoute, claims[j].Protocol)
	})

	manifest := CatalogProjectionManifestV1{
		Schema:           CatalogProjectionManifestSchemaV1,
		AcceptanceStatus: CatalogProjectionAccepted,
		Packages:         packages,
		Identities:       identities,
		GlobalClaims:     claims,
	}
	if err := validateCatalogProjectionManifest(manifest); err != nil {
		t.Fatalf("catalog fixture invalid: %v", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return ValidatedCatalogProjection{
		Manifest:       manifest,
		CanonicalJSON:  canonical,
		ChecksumSHA256: hex.EncodeToString(digest[:]),
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
