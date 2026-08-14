package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

const (
	SchemaV1            = "m365-wp2-capability-evidence/v1"
	MaxEvidenceBytes    = 64 * 1024
	MaxJSONNestingDepth = 32
)

type Classification string

const (
	ClassificationVerified        Classification = "VERIFIED"
	ClassificationConfirmedDefect Classification = "CONFIRMED_DEFECT"
	ClassificationUnsupported     Classification = "UNSUPPORTED"
	ClassificationInconclusive    Classification = "INCONCLUSIVE"
)

type TestExecutionStatus string

const (
	TestExecutionPass    TestExecutionStatus = "PASS"
	TestExecutionFail    TestExecutionStatus = "FAIL"
	TestExecutionBlocked TestExecutionStatus = "BLOCKED"
	TestExecutionError   TestExecutionStatus = "ERROR"
)

type ManifestV1 struct {
	Schema                  string              `json:"schema"`
	NormativeADRSHA256      string              `json:"normative_adr_sha256"`
	SourceHead              string              `json:"source_head"`
	DirtyContentSHA256      *string             `json:"dirty_content_sha256,omitempty"`
	BinarySHA256            string              `json:"binary_sha256"`
	HarnessSHA256           string              `json:"harness_sha256"`
	ObservationSHA256       string              `json:"observation_sha256"`
	CanonicalRoute          string              `json:"canonical_route"`
	ResolvedTone            string              `json:"resolved_tone"`
	Protocol                string              `json:"protocol"`
	AccountProfileRef       string              `json:"account_profile_ref"`
	EffectiveSettingsSHA256 string              `json:"effective_settings_sha256"`
	MappingEvidence         string              `json:"mapping_evidence"`
	IdentityStatus          string              `json:"identity_status"`
	CapabilityID            string              `json:"capability_id"`
	Classification          Classification      `json:"classification"`
	TestExecutionStatus     TestExecutionStatus `json:"test_execution_status"`
}

type IdentitySet struct {
	NormativeADRSHA256      string
	SourceHead              string
	DirtyContentSHA256      string
	BinarySHA256            string
	HarnessSHA256           string
	ObservationSHA256       string
	CanonicalRoute          string
	ResolvedTone            string
	Protocol                string
	AccountProfileRef       string
	EffectiveSettingsSHA256 string
}

type ValidatedRecord struct {
	Manifest       ManifestV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type ValidationError struct {
	Code string `json:"code"`
	Rule string `json:"rule"`
	Path string `json:"path,omitempty"`
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return e.Code + ": " + e.Rule
	}
	return e.Code + ": " + e.Rule + " at " + e.Path
}

var (
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitCommitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	opaqueRefPattern  = regexp.MustCompile(`^acct_[0-9a-f]{32}$`)
	routePattern      = regexp.MustCompile(`^[a-z0-9._-]+$`)
	capabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	tonePattern       = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

var allowedManifestFields = map[string]struct{}{
	"schema":                    {},
	"normative_adr_sha256":      {},
	"source_head":               {},
	"dirty_content_sha256":      {},
	"binary_sha256":             {},
	"harness_sha256":            {},
	"observation_sha256":        {},
	"canonical_route":           {},
	"resolved_tone":             {},
	"protocol":                  {},
	"account_profile_ref":       {},
	"effective_settings_sha256": {},
	"mapping_evidence":          {},
	"identity_status":           {},
	"capability_id":             {},
	"classification":            {},
	"test_execution_status":     {},
}

var allowedProtocols = map[string]struct{}{
	"m365_web_outbound":                 {},
	"openai_chat_completions_nonstream": {},
	"openai_chat_completions_stream":    {},
	"openai_responses_nonstream":        {},
	"openai_responses_stream":           {},
	"anthropic_messages_nonstream":      {},
	"anthropic_messages_stream":         {},
	"legacy_chat_nonstream":             {},
	"legacy_chat_stream":                {},
}

var allowedMappingEvidence = map[string]struct{}{
	"unverified":           {},
	"api_tone_accepted":    {},
	"web_payload_verified": {},
}

var allowedIdentityStatus = map[string]struct{}{
	"dynamic_unidentified":       {},
	"accepted_unverified":        {},
	"upstream_identity_verified": {},
}

var allowedClassifications = map[Classification]struct{}{
	ClassificationVerified:        {},
	ClassificationConfirmedDefect: {},
	ClassificationUnsupported:     {},
	ClassificationInconclusive:    {},
}

var allowedTestExecutionStatuses = map[TestExecutionStatus]struct{}{
	TestExecutionPass:    {},
	TestExecutionFail:    {},
	TestExecutionBlocked: {},
	TestExecutionError:   {},
}

var wp2VerifiableCapabilityOrder = []string{
	"route_identity",
	"route_mapping",
	"basic_text_delivery",
	"protocol_transport",
}

var wp2VerifiableCapabilities = func() map[string]struct{} {
	result := make(map[string]struct{}, len(wp2VerifiableCapabilityOrder))
	for _, capabilityID := range wp2VerifiableCapabilityOrder {
		result[capabilityID] = struct{}{}
	}
	return result
}()

func WP2VerifiableCapabilityIDs() []string {
	return append([]string(nil), wp2VerifiableCapabilityOrder...)
}

var forbiddenPrivacyFields = map[string]string{
	"token":               "token",
	"accesstoken":         "token",
	"refreshtoken":        "token",
	"authorization":       "token",
	"authorizationheader": "token",
	"cookie":              "cookie",
	"setcookie":           "cookie",
	"apikey":              "api_key",
	"oauthsecret":         "oauth_secret",
	"clientsecret":        "oauth_secret",
	"prompt":              "prompt",
	"systemprompt":        "prompt",
	"userprompt":          "prompt",
	"requestbody":         "request_body",
	"fullrequest":         "request_body",
	"completerequest":     "request_body",
	"responsebody":        "response_body",
	"fullresponse":        "response_body",
	"completeresponse":    "response_body",
	"upstreamevent":       "complete_frame",
	"fullupstreamevent":   "complete_frame",
	"frame":               "complete_frame",
	"fullframe":           "complete_frame",
	"completeframe":       "complete_frame",
	"arguments":           "complete_frame",
	"email":               "email",
	"oid":                 "object_id",
	"objectid":            "object_id",
	"tid":                 "tenant_id",
	"tenantid":            "tenant_id",
	"accountid":           "account_mapping",
	"accountmapping":      "account_mapping",
	"conversationcontent": "conversation_content",
	"messages":            "conversation_content",
	"toolarguments":       "conversation_content",
	"attachmentcontent":   "conversation_content",
}

var forbiddenSelfHashFields = map[string]struct{}{
	"checksum":       {},
	"checksumsha256": {},
	"manifestsha256": {},
	"recordsha256":   {},
	"selfsha256":     {},
}

func ValidateCapabilityEvidence(raw []byte, expected IdentitySet) (ValidatedRecord, error) {
	if len(raw) == 0 {
		return ValidatedRecord{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxEvidenceBytes {
		return ValidatedRecord{}, validationError("evidence_too_large", "size_limit", "/")
	}
	if err := scanJSONContract(raw); err != nil {
		return ValidatedRecord{}, err
	}

	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil {
		return ValidatedRecord{}, validationError("invalid_json", "single_json_object", "/")
	}
	if dirty, present := rawFields["dirty_content_sha256"]; present && bytes.Equal(bytes.TrimSpace(dirty), []byte("null")) {
		return ValidatedRecord{}, validationError("invalid_identity", "sha256", "/dirty_content_sha256")
	}

	var manifest ManifestV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedRecord{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedRecord{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedRecord{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := validateManifest(manifest); err != nil {
		return ValidatedRecord{}, err
	}
	if err := validateExpectedIdentity(expected); err != nil {
		return ValidatedRecord{}, err
	}
	if err := matchExpectedIdentity(manifest, expected); err != nil {
		return ValidatedRecord{}, err
	}

	canonical, err := json.Marshal(manifest)
	if err != nil {
		return ValidatedRecord{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	digest := sha256.Sum256(canonical)
	return ValidatedRecord{
		Manifest:       manifest,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func validateManifest(manifest ManifestV1) error {
	if manifest.Schema == "" {
		return validationError("missing_field", "required_binding", "/schema")
	}
	if manifest.Schema != SchemaV1 {
		return validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if err := requireSHA256(manifest.NormativeADRSHA256, "/normative_adr_sha256"); err != nil {
		return err
	}
	if !gitCommitPattern.MatchString(manifest.SourceHead) {
		return validationError("invalid_identity", "git_commit_sha", "/source_head")
	}
	if manifest.DirtyContentSHA256 != nil {
		if err := requireSHA256(*manifest.DirtyContentSHA256, "/dirty_content_sha256"); err != nil {
			return err
		}
	}
	if err := requireSHA256(manifest.BinarySHA256, "/binary_sha256"); err != nil {
		return err
	}
	if err := requireSHA256(manifest.HarnessSHA256, "/harness_sha256"); err != nil {
		return err
	}
	if err := requireSHA256(manifest.ObservationSHA256, "/observation_sha256"); err != nil {
		return err
	}
	if manifest.CanonicalRoute == "" {
		return validationError("missing_field", "required_binding", "/canonical_route")
	}
	if !routePattern.MatchString(manifest.CanonicalRoute) {
		return validationError("invalid_route", "stable_route_id", "/canonical_route")
	}
	if manifest.ResolvedTone == "" {
		return validationError("missing_field", "required_binding", "/resolved_tone")
	}
	if !tonePattern.MatchString(manifest.ResolvedTone) {
		return validationError("invalid_tone", "resolved_tone", "/resolved_tone")
	}
	if manifest.Protocol == "" {
		return validationError("missing_field", "required_binding", "/protocol")
	}
	if _, ok := allowedProtocols[manifest.Protocol]; !ok {
		return validationError("invalid_enum", "protocol", "/protocol")
	}
	if manifest.AccountProfileRef == "" {
		return validationError("missing_field", "required_binding", "/account_profile_ref")
	}
	if !opaqueRefPattern.MatchString(manifest.AccountProfileRef) {
		return validationError("invalid_identity", "opaque_account_profile", "/account_profile_ref")
	}
	if manifest.EffectiveSettingsSHA256 == "" {
		return validationError("missing_field", "required_binding", "/effective_settings_sha256")
	}
	if err := requireSHA256(manifest.EffectiveSettingsSHA256, "/effective_settings_sha256"); err != nil {
		return err
	}
	if _, ok := allowedMappingEvidence[manifest.MappingEvidence]; !ok {
		if manifest.MappingEvidence == "" {
			return validationError("missing_field", "required_binding", "/mapping_evidence")
		}
		return validationError("invalid_enum", "mapping_evidence", "/mapping_evidence")
	}
	if _, ok := allowedIdentityStatus[manifest.IdentityStatus]; !ok {
		if manifest.IdentityStatus == "" {
			return validationError("missing_field", "required_binding", "/identity_status")
		}
		return validationError("invalid_enum", "identity_status", "/identity_status")
	}
	if manifest.CapabilityID == "" {
		return validationError("missing_field", "required_binding", "/capability_id")
	}
	if !capabilityPattern.MatchString(manifest.CapabilityID) {
		return validationError("invalid_capability", "stable_capability_id", "/capability_id")
	}
	if _, ok := allowedClassifications[manifest.Classification]; !ok {
		if manifest.Classification == "" {
			return validationError("missing_field", "required_binding", "/classification")
		}
		return validationError("invalid_enum", "classification", "/classification")
	}
	if _, ok := allowedTestExecutionStatuses[manifest.TestExecutionStatus]; !ok {
		if manifest.TestExecutionStatus == "" {
			return validationError("missing_field", "required_binding", "/test_execution_status")
		}
		return validationError("invalid_enum", "test_execution_status", "/test_execution_status")
	}
	if manifest.Classification == ClassificationVerified {
		if manifest.TestExecutionStatus != TestExecutionPass {
			return validationError("classification_status_conflict", "verified_requires_pass", "/test_execution_status")
		}
		if _, ok := wp2VerifiableCapabilities[manifest.CapabilityID]; !ok {
			return validationError("verification_scope_forbidden", "wp2_verified_capability", "/capability_id")
		}
	}
	if manifest.Classification == ClassificationConfirmedDefect && manifest.TestExecutionStatus != TestExecutionFail {
		return validationError("classification_status_conflict", "confirmed_defect_requires_fail", "/test_execution_status")
	}
	return nil
}

func validateExpectedIdentity(expected IdentitySet) error {
	if !sha256Pattern.MatchString(expected.NormativeADRSHA256) {
		return validationError("invalid_expected_identity", "normative_adr_sha256", "/normative_adr_sha256")
	}
	if !gitCommitPattern.MatchString(expected.SourceHead) {
		return validationError("invalid_expected_identity", "source_head", "/source_head")
	}
	if expected.DirtyContentSHA256 != "" && !sha256Pattern.MatchString(expected.DirtyContentSHA256) {
		return validationError("invalid_expected_identity", "dirty_content_sha256", "/dirty_content_sha256")
	}
	if !sha256Pattern.MatchString(expected.BinarySHA256) {
		return validationError("invalid_expected_identity", "binary_sha256", "/binary_sha256")
	}
	if !sha256Pattern.MatchString(expected.HarnessSHA256) {
		return validationError("invalid_expected_identity", "harness_sha256", "/harness_sha256")
	}
	if !sha256Pattern.MatchString(expected.ObservationSHA256) {
		return validationError("invalid_expected_identity", "observation_sha256", "/observation_sha256")
	}
	if !routePattern.MatchString(expected.CanonicalRoute) {
		return validationError("invalid_expected_identity", "canonical_route", "/canonical_route")
	}
	if !tonePattern.MatchString(expected.ResolvedTone) {
		return validationError("invalid_expected_identity", "resolved_tone", "/resolved_tone")
	}
	if _, ok := allowedProtocols[expected.Protocol]; !ok {
		return validationError("invalid_expected_identity", "protocol", "/protocol")
	}
	if !opaqueRefPattern.MatchString(expected.AccountProfileRef) {
		return validationError("invalid_expected_identity", "account_profile_ref", "/account_profile_ref")
	}
	if !sha256Pattern.MatchString(expected.EffectiveSettingsSHA256) {
		return validationError("invalid_expected_identity", "effective_settings_sha256", "/effective_settings_sha256")
	}
	return nil
}

func matchExpectedIdentity(manifest ManifestV1, expected IdentitySet) error {
	if manifest.NormativeADRSHA256 != expected.NormativeADRSHA256 {
		return validationError("identity_mismatch", "exact_identity", "/normative_adr_sha256")
	}
	if manifest.SourceHead != expected.SourceHead {
		return validationError("identity_mismatch", "exact_identity", "/source_head")
	}
	if expected.DirtyContentSHA256 == "" {
		if manifest.DirtyContentSHA256 != nil {
			return validationError("identity_mismatch", "exact_identity", "/dirty_content_sha256")
		}
	} else if manifest.DirtyContentSHA256 == nil || *manifest.DirtyContentSHA256 != expected.DirtyContentSHA256 {
		return validationError("identity_mismatch", "exact_identity", "/dirty_content_sha256")
	}
	if manifest.BinarySHA256 != expected.BinarySHA256 {
		return validationError("identity_mismatch", "exact_identity", "/binary_sha256")
	}
	if manifest.HarnessSHA256 != expected.HarnessSHA256 {
		return validationError("identity_mismatch", "exact_identity", "/harness_sha256")
	}
	if manifest.ObservationSHA256 != expected.ObservationSHA256 {
		return validationError("identity_mismatch", "exact_identity", "/observation_sha256")
	}
	if manifest.CanonicalRoute != expected.CanonicalRoute {
		return validationError("identity_mismatch", "exact_identity", "/canonical_route")
	}
	if manifest.ResolvedTone != expected.ResolvedTone {
		return validationError("identity_mismatch", "exact_identity", "/resolved_tone")
	}
	if manifest.Protocol != expected.Protocol {
		return validationError("identity_mismatch", "exact_identity", "/protocol")
	}
	if manifest.AccountProfileRef != expected.AccountProfileRef {
		return validationError("identity_mismatch", "exact_identity", "/account_profile_ref")
	}
	if manifest.EffectiveSettingsSHA256 != expected.EffectiveSettingsSHA256 {
		return validationError("identity_mismatch", "exact_identity", "/effective_settings_sha256")
	}
	return nil
}

func requireSHA256(value, path string) error {
	if !sha256Pattern.MatchString(value) {
		return validationError("invalid_identity", "sha256", path)
	}
	return nil
}

func scanJSONContract(raw []byte) error {
	return scanClosedJSONContract(raw, allowedManifestFields)
}

func scanClosedJSONContract(raw []byte, allowedTopLevelFields map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return validationError("invalid_json", "single_json_object", "/")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return validationError("invalid_json", "single_json_object", "/")
	}
	if err := scanJSONObject(decoder, allowedTopLevelFields, 1); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return validationError("invalid_json", "single_json_object", "/")
	}
	return nil
}

func scanJSONObject(decoder *json.Decoder, allowedFields map[string]struct{}, depth int) error {
	if depth > MaxJSONNestingDepth {
		return validationError("json_depth_exceeded", "nesting_limit", "/")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return validationError("invalid_json", "single_json_object", "/")
		}
		key, ok := token.(string)
		if !ok {
			return validationError("invalid_json", "single_json_object", "/")
		}
		if _, exists := seen[key]; exists {
			return validationError("duplicate_key", "closed_json", "/")
		}
		seen[key] = struct{}{}
		normalized := normalizeFieldName(key)
		if _, forbidden := forbiddenSelfHashFields[normalized]; forbidden {
			return validationError("self_hash_forbidden", "external_checksum", "/")
		}
		if rule, forbidden := forbiddenPrivacyFields[normalized]; forbidden {
			return validationError("privacy_forbidden", rule, "/")
		}
		if allowedFields != nil {
			if _, allowed := allowedFields[key]; !allowed {
				return validationError("unknown_field", "closed_schema", "/")
			}
		}
		if err := scanJSONValue(decoder, depth); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return validationError("invalid_json", "single_json_object", "/")
	}
	return nil
}

func scanJSONArray(decoder *json.Decoder, depth int) error {
	if depth > MaxJSONNestingDepth {
		return validationError("json_depth_exceeded", "nesting_limit", "/")
	}
	for decoder.More() {
		if err := scanJSONValue(decoder, depth); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return validationError("invalid_json", "single_json_object", "/")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return validationError("invalid_json", "single_json_object", "/")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return scanJSONObject(decoder, nil, depth+1)
	case '[':
		return scanJSONArray(decoder, depth+1)
	default:
		return validationError("invalid_json", "single_json_object", "/")
	}
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return validationError("invalid_json", "single_json_object", "/")
	}
	return nil
}

func normalizeFieldName(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range strings.ToLower(value) {
		switch r {
		case '_', '-', ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validationError(code, rule, path string) *ValidationError {
	return &ValidationError{Code: code, Rule: rule, Path: path}
}
