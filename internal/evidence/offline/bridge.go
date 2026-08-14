package offline

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	core "m365-native/internal/evidence"
)

type Classification = core.Classification
type TestExecutionStatus = core.TestExecutionStatus
type ManifestV1 = core.ManifestV1
type IdentitySet = core.IdentitySet
type ValidatedRecord = core.ValidatedRecord
type ValidationError = core.ValidationError
type CaptureBinding = core.CaptureBinding

const (
	SchemaV1                      = core.SchemaV1
	ClassificationVerified        = core.ClassificationVerified
	ClassificationConfirmedDefect = core.ClassificationConfirmedDefect
	ClassificationUnsupported     = core.ClassificationUnsupported
	ClassificationInconclusive    = core.ClassificationInconclusive
	TestExecutionPass             = core.TestExecutionPass
	TestExecutionFail             = core.TestExecutionFail
	TestExecutionBlocked          = core.TestExecutionBlocked
	TestExecutionError            = core.TestExecutionError
)

var (
	sha256Pattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	routePattern     = regexp.MustCompile(`^[a-z0-9._-]+$`)
	tonePattern      = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

var allowedIdentityStatus = map[string]struct{}{
	"dynamic_unidentified":       {},
	"accepted_unverified":        {},
	"upstream_identity_verified": {},
}

var allowedProtocols = map[string]struct{}{
	"m365_web_outbound": {}, "openai_chat_completions_nonstream": {},
	"openai_chat_completions_stream": {}, "openai_responses_nonstream": {},
	"openai_responses_stream": {}, "anthropic_messages_nonstream": {},
	"anthropic_messages_stream": {}, "legacy_chat_nonstream": {}, "legacy_chat_stream": {},
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

func validationError(code, rule, path string) *core.ValidationError {
	return &core.ValidationError{Code: code, Rule: rule, Path: path}
}

func requireSHA256(value, path string) error {
	if !sha256Pattern.MatchString(value) {
		return validationError("invalid_identity", "sha256", path)
	}
	return nil
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
	if depth > core.MaxJSONNestingDepth {
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
	if depth > core.MaxJSONNestingDepth {
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

func ensureJSONEOF(decoder *json.Decoder) error {
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
