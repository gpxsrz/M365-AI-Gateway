package evidence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const validEvidenceJSON = `{
  "test_execution_status":"PASS",
  "classification":"VERIFIED",
  "capability_id":"basic_text_delivery",
  "identity_status":"dynamic_unidentified",
  "mapping_evidence":"api_tone_accepted",
  "effective_settings_sha256":"5555555555555555555555555555555555555555555555555555555555555555",
  "account_profile_ref":"acct_0123456789abcdef0123456789abcdef",
  "protocol":"openai_chat_completions_nonstream",
  "resolved_tone":"magic",
  "canonical_route":"m365-auto",
  "observation_sha256":"4444444444444444444444444444444444444444444444444444444444444444",
  "harness_sha256":"3333333333333333333333333333333333333333333333333333333333333333",
  "binary_sha256":"2222222222222222222222222222222222222222222222222222222222222222",
  "dirty_content_sha256":"1111111111111111111111111111111111111111111111111111111111111111",
  "source_head":"79fd0265a027454e5fee4f0e813f1ec0a91ec855",
  "normative_adr_sha256":"4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
  "schema":"m365-wp2-capability-evidence/v1"
}`

const expectedCanonicalJSON = `{"schema":"m365-wp2-capability-evidence/v1","normative_adr_sha256":"4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a","source_head":"79fd0265a027454e5fee4f0e813f1ec0a91ec855","dirty_content_sha256":"1111111111111111111111111111111111111111111111111111111111111111","binary_sha256":"2222222222222222222222222222222222222222222222222222222222222222","harness_sha256":"3333333333333333333333333333333333333333333333333333333333333333","observation_sha256":"4444444444444444444444444444444444444444444444444444444444444444","canonical_route":"m365-auto","resolved_tone":"magic","protocol":"openai_chat_completions_nonstream","account_profile_ref":"acct_0123456789abcdef0123456789abcdef","effective_settings_sha256":"5555555555555555555555555555555555555555555555555555555555555555","mapping_evidence":"api_tone_accepted","identity_status":"dynamic_unidentified","capability_id":"basic_text_delivery","classification":"VERIFIED","test_execution_status":"PASS"}`
const expectedCanonicalSHA256 = "0567b63e4bfa5fef699402202c463d40fcb2118ff519c347f2d5568a0ca55158"

func expectedIdentity() IdentitySet {
	return IdentitySet{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "79fd0265a027454e5fee4f0e813f1ec0a91ec855",
		DirtyContentSHA256:      "1111111111111111111111111111111111111111111111111111111111111111",
		BinarySHA256:            "2222222222222222222222222222222222222222222222222222222222222222",
		HarnessSHA256:           "3333333333333333333333333333333333333333333333333333333333333333",
		ObservationSHA256:       "4444444444444444444444444444444444444444444444444444444444444444",
		CanonicalRoute:          "m365-auto",
		ResolvedTone:            "magic",
		Protocol:                "openai_chat_completions_nonstream",
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: "5555555555555555555555555555555555555555555555555555555555555555",
	}
}

func TestValidateCapabilityEvidenceCanonicalizesAndChecksums(t *testing.T) {
	got, err := ValidateCapabilityEvidence([]byte(validEvidenceJSON), expectedIdentity())
	if err != nil {
		t.Fatalf("ValidateCapabilityEvidence() error = %v", err)
	}
	if string(got.CanonicalJSON) != expectedCanonicalJSON {
		t.Fatalf("canonical JSON mismatch\nwant: %s\n got: %s", expectedCanonicalJSON, got.CanonicalJSON)
	}
	if got.ChecksumSHA256 != expectedCanonicalSHA256 {
		t.Fatalf("checksum = %q, want %q", got.ChecksumSHA256, expectedCanonicalSHA256)
	}
	if strings.Contains(string(got.CanonicalJSON), "checksum") {
		t.Fatal("canonical manifest must not contain a self-referential checksum")
	}
}

func TestValidateCapabilityEvidenceIsDeterministicAcrossInputOrderAndWhitespace(t *testing.T) {
	ordered := expectedCanonicalJSON
	first, err := ValidateCapabilityEvidence([]byte(validEvidenceJSON), expectedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ValidateCapabilityEvidence([]byte(ordered), expectedIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CanonicalJSON) != string(second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatalf("equivalent inputs produced different records: %#v != %#v", first, second)
	}
}

func TestCapabilityClassificationIsIndependentFromExecutionStatus(t *testing.T) {
	accepted := []struct {
		classification string
		status         string
	}{
		{"VERIFIED", "PASS"},
		{"CONFIRMED_DEFECT", "FAIL"},
		{"UNSUPPORTED", "PASS"},
		{"UNSUPPORTED", "FAIL"},
		{"INCONCLUSIVE", "PASS"},
		{"INCONCLUSIVE", "FAIL"},
		{"INCONCLUSIVE", "BLOCKED"},
		{"INCONCLUSIVE", "ERROR"},
	}
	for _, tt := range accepted {
		t.Run("accept_"+tt.classification+"_"+tt.status, func(t *testing.T) {
			raw := strings.Replace(validEvidenceJSON, `"classification":"VERIFIED"`, `"classification":"`+tt.classification+`"`, 1)
			raw = strings.Replace(raw, `"test_execution_status":"PASS"`, `"test_execution_status":"`+tt.status+`"`, 1)
			if _, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity()); err != nil {
				t.Fatalf("explicit classification/status pair rejected: %v", err)
			}
		})
	}

	rejected := []struct {
		classification string
		status         string
		rule           string
	}{
		{"VERIFIED", "FAIL", "verified_requires_pass"},
		{"VERIFIED", "BLOCKED", "verified_requires_pass"},
		{"VERIFIED", "ERROR", "verified_requires_pass"},
		{"CONFIRMED_DEFECT", "PASS", "confirmed_defect_requires_fail"},
		{"CONFIRMED_DEFECT", "BLOCKED", "confirmed_defect_requires_fail"},
		{"CONFIRMED_DEFECT", "ERROR", "confirmed_defect_requires_fail"},
	}
	for _, tt := range rejected {
		t.Run("reject_"+tt.classification+"_"+tt.status, func(t *testing.T) {
			raw := strings.Replace(validEvidenceJSON, `"classification":"VERIFIED"`, `"classification":"`+tt.classification+`"`, 1)
			raw = strings.Replace(raw, `"test_execution_status":"PASS"`, `"test_execution_status":"`+tt.status+`"`, 1)
			_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
			assertValidationError(t, err, "classification_status_conflict", tt.rule)
		})
	}
}

func TestValidateCapabilityEvidenceAcceptsDocumentedRouteAndToneCharacters(t *testing.T) {
	raw := strings.Replace(validEvidenceJSON, `"canonical_route":"m365-auto"`, `"canonical_route":"2.route"`, 1)
	raw = strings.Replace(raw, `"resolved_tone":"magic"`, `"resolved_tone":"_tone2"`, 1)
	expected := expectedIdentity()
	expected.CanonicalRoute = "2.route"
	expected.ResolvedTone = "_tone2"
	if _, err := ValidateCapabilityEvidence([]byte(raw), expected); err != nil {
		t.Fatalf("documented route/tone characters rejected: %v", err)
	}
}

func TestValidateCapabilityEvidenceRejectsForbiddenPrivacyFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
		rule  string
	}{
		{"token", `"access_token":"DO_NOT_ECHO_7f51",`, "token"},
		{"authorization", `"authorization":"DO_NOT_ECHO_7f51",`, "token"},
		{"cookie", `"cookie":"DO_NOT_ECHO_7f51",`, "cookie"},
		{"api_key", `"api_key":"DO_NOT_ECHO_7f51",`, "api_key"},
		{"oauth_secret", `"oauth_secret":"DO_NOT_ECHO_7f51",`, "oauth_secret"},
		{"prompt", `"prompt":"DO_NOT_ECHO_7f51",`, "prompt"},
		{"request_body", `"request_body":"DO_NOT_ECHO_7f51",`, "request_body"},
		{"full_request", `"full_request":"DO_NOT_ECHO_7f51",`, "request_body"},
		{"response_body", `"response_body":"DO_NOT_ECHO_7f51",`, "response_body"},
		{"full_response", `"full_response":"DO_NOT_ECHO_7f51",`, "response_body"},
		{"complete_frame", `"complete_frame":{"private":true},`, "complete_frame"},
		{"full_upstream_event", `"full_upstream_event":{"private":true},`, "complete_frame"},
		{"messages", `"messages":[{"content":"DO_NOT_ECHO_7f51"}],`, "conversation_content"},
		{"tool_arguments", `"tool_arguments":"DO_NOT_ECHO_7f51",`, "conversation_content"},
		{"attachment_content", `"attachment_content":"DO_NOT_ECHO_7f51",`, "conversation_content"},
		{"email", `"email":"person@example.com",`, "email"},
		{"object_id", `"object_id":"00000000-0000-0000-0000-000000000000",`, "object_id"},
		{"tenant_id", `"tenant_id":"00000000-0000-0000-0000-000000000000",`, "tenant_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := strings.Replace(validEvidenceJSON, "{", "{"+tt.field, 1)
			_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
			assertValidationError(t, err, "privacy_forbidden", tt.rule)
			if strings.Contains(err.Error(), "DO_NOT_ECHO_7f51") || strings.Contains(err.Error(), "person@example.com") {
				t.Fatalf("error exposed sensitive input: %v", err)
			}
			encoded, marshalErr := json.Marshal(err)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), "DO_NOT_ECHO_7f51") || strings.Contains(string(encoded), "person@example.com") {
				t.Fatalf("structured error exposed sensitive input: %s", encoded)
			}
		})
	}
}

func TestValidateCapabilityEvidenceRejectsAmbiguousJSON(t *testing.T) {
	t.Run("duplicate key", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, `"schema":"m365-wp2-capability-evidence/v1"`, `"schema":"m365-wp2-capability-evidence/v1","schema":"m365-wp2-capability-evidence/v1"`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "duplicate_key", "closed_json")
	})

	t.Run("wrong schema version", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, `"schema":"m365-wp2-capability-evidence/v1"`, `"schema":"m365-wp2-capability-evidence/v2"`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "invalid_schema", "versioned_schema")
	})

	t.Run("unknown field", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, "{", `{"unexpected":true,`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "unknown_field", "closed_schema")
	})

	t.Run("case variant field", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, `"schema"`, `"Schema"`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "unknown_field", "closed_schema")
	})

	t.Run("escaped duplicate key", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, `"schema":"m365-wp2-capability-evidence/v1"`, `"schema":"m365-wp2-capability-evidence/v1","\u0073chema":"m365-wp2-capability-evidence/v1"`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "duplicate_key", "closed_json")
	})

	t.Run("self hash", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, "{", `{"manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "self_hash_forbidden", "external_checksum")
	})

	t.Run("excessive nesting", func(t *testing.T) {
		nested := strings.Repeat("[", MaxJSONNestingDepth) + "0" + strings.Repeat("]", MaxJSONNestingDepth)
		raw := strings.Replace(validEvidenceJSON, `"schema":"m365-wp2-capability-evidence/v1"`, `"schema":`+nested, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "json_depth_exceeded", "nesting_limit")
	})

	t.Run("dirty content null", func(t *testing.T) {
		raw := strings.Replace(validEvidenceJSON, `"dirty_content_sha256":"1111111111111111111111111111111111111111111111111111111111111111"`, `"dirty_content_sha256":null`, 1)
		_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
		assertValidationError(t, err, "invalid_identity", "sha256")
	})

	t.Run("trailing value", func(t *testing.T) {
		_, err := ValidateCapabilityEvidence([]byte(validEvidenceJSON+` {}`), expectedIdentity())
		assertValidationError(t, err, "invalid_json", "single_json_object")
	})
}

func TestValidateCapabilityEvidenceRejectsMalformedIdentities(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		path string
	}{
		{"normative ADR", expectedIdentity().NormativeADRSHA256, "not-a-digest", "/normative_adr_sha256"},
		{"source", expectedIdentity().SourceHead, "ABC", "/source_head"},
		{"dirty content", expectedIdentity().DirtyContentSHA256, "dirty", "/dirty_content_sha256"},
		{"binary", expectedIdentity().BinarySHA256, "binary", "/binary_sha256"},
		{"harness", expectedIdentity().HarnessSHA256, "harness", "/harness_sha256"},
		{"observation", strings.Repeat("4", 64), "observation", "/observation_sha256"},
		{"account", "acct_0123456789abcdef0123456789abcdef", "person@example.com", "/account_profile_ref"},
		{"raw account identifier", "acct_0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", "/account_profile_ref"},
		{"settings", strings.Repeat("5", 64), "settings", "/effective_settings_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := strings.Replace(validEvidenceJSON, tt.old, tt.new, 1)
			_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if validationErr.Code != "invalid_identity" || validationErr.Path != tt.path {
				t.Fatalf("error = %#v, want invalid_identity at %s", validationErr, tt.path)
			}
		})
	}
}

func TestValidateCapabilityEvidenceAcceptsCleanSourceBinding(t *testing.T) {
	raw := strings.Replace(validEvidenceJSON, `"dirty_content_sha256":"1111111111111111111111111111111111111111111111111111111111111111",`, "", 1)
	expected := expectedIdentity()
	expected.DirtyContentSHA256 = ""
	got, err := ValidateCapabilityEvidence([]byte(raw), expected)
	if err != nil {
		t.Fatalf("clean source rejected: %v", err)
	}
	if strings.Contains(string(got.CanonicalJSON), "dirty_content_sha256") {
		t.Fatalf("clean canonical record retained dirty-content identity: %s", got.CanonicalJSON)
	}
}

func TestValidateCapabilityEvidenceRejectsChangedExpectedIdentity(t *testing.T) {
	tests := []struct {
		name string
		edit func(*IdentitySet)
		path string
	}{
		{"normative ADR", func(v *IdentitySet) { v.NormativeADRSHA256 = strings.Repeat("a", 64) }, "/normative_adr_sha256"},
		{"source", func(v *IdentitySet) { v.SourceHead = strings.Repeat("a", 40) }, "/source_head"},
		{"dirty content", func(v *IdentitySet) { v.DirtyContentSHA256 = strings.Repeat("a", 64) }, "/dirty_content_sha256"},
		{"binary", func(v *IdentitySet) { v.BinarySHA256 = strings.Repeat("a", 64) }, "/binary_sha256"},
		{"harness", func(v *IdentitySet) { v.HarnessSHA256 = strings.Repeat("a", 64) }, "/harness_sha256"},
		{"observation", func(v *IdentitySet) { v.ObservationSHA256 = strings.Repeat("a", 64) }, "/observation_sha256"},
		{"route", func(v *IdentitySet) { v.CanonicalRoute = "m365-other" }, "/canonical_route"},
		{"tone", func(v *IdentitySet) { v.ResolvedTone = "Gpt_5_6_Reasoning" }, "/resolved_tone"},
		{"protocol", func(v *IdentitySet) { v.Protocol = "openai_responses_nonstream" }, "/protocol"},
		{"account", func(v *IdentitySet) { v.AccountProfileRef = "acct_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }, "/account_profile_ref"},
		{"settings", func(v *IdentitySet) { v.EffectiveSettingsSHA256 = strings.Repeat("a", 64) }, "/effective_settings_sha256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := expectedIdentity()
			tt.edit(&expected)
			_, err := ValidateCapabilityEvidence([]byte(validEvidenceJSON), expected)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want *ValidationError", err)
			}
			if validationErr.Code != "identity_mismatch" || validationErr.Path != tt.path {
				t.Fatalf("error = %#v, want identity_mismatch at %s", validationErr, tt.path)
			}
		})
	}
}

func TestValidateCapabilityEvidenceEnforcesWP2VerificationBoundary(t *testing.T) {
	raw := strings.Replace(validEvidenceJSON, `"capability_id":"basic_text_delivery"`, `"capability_id":"function_calling"`, 1)
	_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
	assertValidationError(t, err, "verification_scope_forbidden", "wp2_verified_capability")

	raw = strings.Replace(raw, `"classification":"VERIFIED"`, `"classification":"INCONCLUSIVE"`, 1)
	if _, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity()); err != nil {
		t.Fatalf("deferred capability with non-VERIFIED classification rejected: %v", err)
	}
}

func TestValidateCapabilityEvidenceRequiresCompleteBinding(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"route", `"canonical_route":"m365-auto",`},
		{"tone", `"resolved_tone":"magic",`},
		{"protocol", `"protocol":"openai_chat_completions_nonstream",`},
		{"account", `"account_profile_ref":"acct_0123456789abcdef0123456789abcdef",`},
		{"settings", `"effective_settings_sha256":"5555555555555555555555555555555555555555555555555555555555555555",`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := strings.Replace(validEvidenceJSON, tt.field, "", 1)
			_, err := ValidateCapabilityEvidence([]byte(raw), expectedIdentity())
			assertValidationError(t, err, "missing_field", "required_binding")
		})
	}
}

func assertValidationError(t *testing.T, err error, code, rule string) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Code != code || validationErr.Rule != rule {
		t.Fatalf("error = %#v, want code=%q rule=%q", validationErr, code, rule)
	}
}
