package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
)

const (
	NativeSearchRegressionPackageSchemaV1     = "m365-wp3-native-search-regression-package/v1"
	NativeSearchRegressionObservationSchemaV1 = "m365-wp3-native-search-regression-observation/v1"
	MaxNativeSearchRegressionPackageBytes     = 1024 * 1024

	NativeSearchScopeSyntheticFixture = "synthetic_fixture"
	NativeSearchLiveUnverified        = "unverified"
	NativeSearchLiveVerified          = "verified"
	NativeSearchOpenAINotPromoted     = "not_promoted"
	NativeSearchOpenAIVerified        = "verified"
	NativeSearchNormativeADRPath      = "docs/adr/0001-m365-model-route-identity-and-catalog-governance.normative.md"
)

type NativeSearchRegressionCase string

const (
	NativeSearchCaseZeroTools    NativeSearchRegressionCase = "zero_tools"
	NativeSearchCaseGeneralTools NativeSearchRegressionCase = "general_tools"
	NativeSearchCaseCustomExec   NativeSearchRegressionCase = "custom_exec"
)

type NativeSearchRegressionProtocol string

const (
	NativeSearchProtocolChatNonStream      NativeSearchRegressionProtocol = "openai_chat_completions_nonstream"
	NativeSearchProtocolChatStream         NativeSearchRegressionProtocol = "openai_chat_completions_stream"
	NativeSearchProtocolResponsesNonStream NativeSearchRegressionProtocol = "openai_responses_nonstream"
	NativeSearchProtocolResponsesStream    NativeSearchRegressionProtocol = "openai_responses_stream"
)

type NativeSearchRegressionTerminal string

const (
	NativeSearchTerminalJSON              NativeSearchRegressionTerminal = "json"
	NativeSearchTerminalChatDone          NativeSearchRegressionTerminal = "chat_done"
	NativeSearchTerminalResponseCompleted NativeSearchRegressionTerminal = "response_completed"
)

type NativeSearchToolPromptMode string

const (
	NativeSearchPromptNone                     NativeSearchToolPromptMode = "none"
	NativeSearchPromptClientPluginsNoExecution NativeSearchToolPromptMode = "client_plugins_tool_choice_none"
	NativeSearchPromptScopedCustomExec         NativeSearchToolPromptMode = "scoped_custom_exec"
)

type NativeSearchRegressionIdentityInput struct {
	NormativeADRSHA256      string
	NormativeADRBytes       int64
	SourceHead              string
	SourceTree              string
	HarnessSHA256           string
	HarnessBytes            int64
	EffectiveSettingsSHA256 string
}

type NativeSearchRegressionIdentityV1 struct {
	NormativeADRPath        string `json:"normative_adr_path"`
	NormativeADRSHA256      string `json:"normative_adr_sha256"`
	NormativeADRBytes       int64  `json:"normative_adr_bytes"`
	SourceHead              string `json:"source_head"`
	SourceTree              string `json:"source_tree"`
	HarnessSHA256           string `json:"harness_sha256"`
	HarnessBytes            int64  `json:"harness_bytes"`
	EffectiveSettingsSHA256 string `json:"effective_settings_sha256"`
	FixtureSetSHA256        string `json:"fixture_set_sha256"`
}

type NativeSearchRegressionEventCountsV1 struct {
	RawFrames                int `json:"raw_frames"`
	SearchProgress           int `json:"search_progress"`
	ToolProgress             int `json:"tool_progress"`
	CodeProgress             int `json:"code_progress"`
	Message                  int `json:"message"`
	Unknown                  int `json:"unknown"`
	SourceAttributionMarkers int `json:"source_attribution_markers"`
	SearchResultMarkers      int `json:"search_result_markers"`
}

type NativeSearchRegressionObservationV1 struct {
	Schema                    string                              `json:"schema"`
	CaseID                    NativeSearchRegressionCase          `json:"case_id"`
	Protocol                  NativeSearchRegressionProtocol      `json:"protocol"`
	EndpointPath              string                              `json:"endpoint_path"`
	HTTPStatus                int                                 `json:"http_status"`
	Terminal                  NativeSearchRegressionTerminal      `json:"terminal"`
	ClientToolsCount          int                                 `json:"client_tools_count"`
	ToolPromptMode            NativeSearchToolPromptMode          `json:"tool_prompt_mode"`
	NativeSearchRequested     string                              `json:"native_search_requested"`
	NativeSearchEffective     string                              `json:"native_search_effective"`
	SourceAttributionObserved bool                                `json:"source_attribution_observed"`
	EventCounts               NativeSearchRegressionEventCountsV1 `json:"event_counts"`
	RawEventsRetained         bool                                `json:"raw_events_retained"`
	ContentRetained           bool                                `json:"content_retained"`
	ObservationScope          string                              `json:"observation_scope"`
	LiveMicrosoftStatus       string                              `json:"live_microsoft_status"`
	OpenAIWebSearchCapability string                              `json:"openai_web_search_capability"`
	UpstreamAttempts          int                                 `json:"upstream_attempts"`
}

type NativeSearchRegressionPackageV1 struct {
	Schema                    string                                `json:"schema"`
	Identity                  NativeSearchRegressionIdentityV1      `json:"identity"`
	Observations              []NativeSearchRegressionObservationV1 `json:"observations"`
	LiveMicrosoftStatus       string                                `json:"live_microsoft_status"`
	OpenAIWebSearchCapability string                                `json:"openai_web_search_capability"`
}

type NativeSearchRegressionBuildInput struct {
	Identity     NativeSearchRegressionIdentityInput
	Observations []NativeSearchRegressionObservationV1
}

type BuiltNativeSearchRegressionPackage struct {
	Package        NativeSearchRegressionPackageV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type NativeSearchRegressionExpected struct {
	NormativeADRSHA256      string
	NormativeADRBytes       int64
	SourceHead              string
	SourceTree              string
	HarnessSHA256           string
	HarnessBytes            int64
	EffectiveSettingsSHA256 string
}

type ValidatedNativeSearchRegressionPackage struct {
	Package        NativeSearchRegressionPackageV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

var allowedNativeSearchRegressionPackageFields = map[string]struct{}{
	"schema":                       {},
	"identity":                     {},
	"observations":                 {},
	"live_microsoft_status":        {},
	"openai_web_search_capability": {},
}

var nativeSearchRegressionCaseRank = map[NativeSearchRegressionCase]int{
	NativeSearchCaseZeroTools:    0,
	NativeSearchCaseGeneralTools: 1,
	NativeSearchCaseCustomExec:   2,
}

var nativeSearchRegressionProtocolRank = map[NativeSearchRegressionProtocol]int{
	NativeSearchProtocolChatNonStream:      0,
	NativeSearchProtocolChatStream:         1,
	NativeSearchProtocolResponsesNonStream: 2,
	NativeSearchProtocolResponsesStream:    3,
}

func BuildNativeSearchRegressionPackage(input NativeSearchRegressionBuildInput) (BuiltNativeSearchRegressionPackage, error) {
	if err := validateNativeSearchRegressionIdentityInput(input.Identity); err != nil {
		return BuiltNativeSearchRegressionPackage{}, err
	}
	observations := append([]NativeSearchRegressionObservationV1(nil), input.Observations...)
	if err := validateNativeSearchRegressionObservations(observations); err != nil {
		return BuiltNativeSearchRegressionPackage{}, err
	}
	sort.Slice(observations, func(i, j int) bool {
		leftCase, rightCase := nativeSearchRegressionCaseRank[observations[i].CaseID], nativeSearchRegressionCaseRank[observations[j].CaseID]
		if leftCase != rightCase {
			return leftCase < rightCase
		}
		return nativeSearchRegressionProtocolRank[observations[i].Protocol] < nativeSearchRegressionProtocolRank[observations[j].Protocol]
	})
	fixtureJSON, err := json.Marshal(observations)
	if err != nil {
		return BuiltNativeSearchRegressionPackage{}, validationError("canonicalization_failed", "native_search_fixture_set", "/observations")
	}
	fixtureDigest := sha256.Sum256(fixtureJSON)
	pkg := NativeSearchRegressionPackageV1{
		Schema: NativeSearchRegressionPackageSchemaV1,
		Identity: NativeSearchRegressionIdentityV1{
			NormativeADRPath:        NativeSearchNormativeADRPath,
			NormativeADRSHA256:      input.Identity.NormativeADRSHA256,
			NormativeADRBytes:       input.Identity.NormativeADRBytes,
			SourceHead:              input.Identity.SourceHead,
			SourceTree:              input.Identity.SourceTree,
			HarnessSHA256:           input.Identity.HarnessSHA256,
			HarnessBytes:            input.Identity.HarnessBytes,
			EffectiveSettingsSHA256: input.Identity.EffectiveSettingsSHA256,
			FixtureSetSHA256:        hex.EncodeToString(fixtureDigest[:]),
		},
		Observations:              observations,
		LiveMicrosoftStatus:       NativeSearchLiveUnverified,
		OpenAIWebSearchCapability: NativeSearchOpenAINotPromoted,
	}
	canonical, err := json.Marshal(pkg)
	if err != nil {
		return BuiltNativeSearchRegressionPackage{}, validationError("canonicalization_failed", "native_search_regression_package", "/")
	}
	digest := sha256.Sum256(canonical)
	return BuiltNativeSearchRegressionPackage{Package: pkg, CanonicalJSON: append([]byte(nil), canonical...), ChecksumSHA256: hex.EncodeToString(digest[:])}, nil
}

func ValidateNativeSearchRegressionPackage(raw []byte, expected NativeSearchRegressionExpected) (ValidatedNativeSearchRegressionPackage, error) {
	if len(raw) == 0 {
		return ValidatedNativeSearchRegressionPackage{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxNativeSearchRegressionPackageBytes {
		return ValidatedNativeSearchRegressionPackage{}, validationError("evidence_too_large", "native_search_regression_package_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedNativeSearchRegressionPackageFields); err != nil {
		return ValidatedNativeSearchRegressionPackage{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var pkg NativeSearchRegressionPackageV1
	if err := decoder.Decode(&pkg); err != nil {
		return ValidatedNativeSearchRegressionPackage{}, validationError("invalid_json", "closed_schema", "/")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ValidatedNativeSearchRegressionPackage{}, err
	}
	if pkg.Schema != NativeSearchRegressionPackageSchemaV1 {
		return ValidatedNativeSearchRegressionPackage{}, validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if err := validateNativeSearchRegressionExpected(pkg.Identity, expected); err != nil {
		return ValidatedNativeSearchRegressionPackage{}, err
	}
	rebuilt, err := BuildNativeSearchRegressionPackage(NativeSearchRegressionBuildInput{
		Identity: NativeSearchRegressionIdentityInput{
			NormativeADRSHA256:      pkg.Identity.NormativeADRSHA256,
			NormativeADRBytes:       pkg.Identity.NormativeADRBytes,
			SourceHead:              pkg.Identity.SourceHead,
			SourceTree:              pkg.Identity.SourceTree,
			HarnessSHA256:           pkg.Identity.HarnessSHA256,
			HarnessBytes:            pkg.Identity.HarnessBytes,
			EffectiveSettingsSHA256: pkg.Identity.EffectiveSettingsSHA256,
		},
		Observations: pkg.Observations,
	})
	if err != nil {
		return ValidatedNativeSearchRegressionPackage{}, err
	}
	if rebuilt.Package.Identity.FixtureSetSHA256 != pkg.Identity.FixtureSetSHA256 || !bytes.Equal(rebuilt.CanonicalJSON, raw) {
		return ValidatedNativeSearchRegressionPackage{}, validationError("canonical_mismatch", "deterministic_rederivation", "/")
	}
	return ValidatedNativeSearchRegressionPackage{Package: rebuilt.Package, CanonicalJSON: append([]byte(nil), rebuilt.CanonicalJSON...), ChecksumSHA256: rebuilt.ChecksumSHA256}, nil
}

func validateNativeSearchRegressionIdentityInput(identity NativeSearchRegressionIdentityInput) error {
	if err := requireSHA256(identity.NormativeADRSHA256, "/identity/normative_adr_sha256"); err != nil {
		return err
	}
	if identity.NormativeADRBytes < 1 {
		return validationError("invalid_identity", "normative_adr_bytes", "/identity/normative_adr_bytes")
	}
	if !gitCommitPattern.MatchString(identity.SourceHead) {
		return validationError("invalid_identity", "source_head", "/identity/source_head")
	}
	if !gitCommitPattern.MatchString(identity.SourceTree) {
		return validationError("invalid_identity", "source_tree", "/identity/source_tree")
	}
	if err := requireSHA256(identity.HarnessSHA256, "/identity/harness_sha256"); err != nil {
		return err
	}
	if identity.HarnessBytes < 1 {
		return validationError("invalid_identity", "harness_bytes", "/identity/harness_bytes")
	}
	return requireSHA256(identity.EffectiveSettingsSHA256, "/identity/effective_settings_sha256")
}

func validateNativeSearchRegressionExpected(identity NativeSearchRegressionIdentityV1, expected NativeSearchRegressionExpected) error {
	if err := validateNativeSearchRegressionIdentityInput(NativeSearchRegressionIdentityInput{
		NormativeADRSHA256:      expected.NormativeADRSHA256,
		NormativeADRBytes:       expected.NormativeADRBytes,
		SourceHead:              expected.SourceHead,
		SourceTree:              expected.SourceTree,
		HarnessSHA256:           expected.HarnessSHA256,
		HarnessBytes:            expected.HarnessBytes,
		EffectiveSettingsSHA256: expected.EffectiveSettingsSHA256,
	}); err != nil {
		return err
	}
	if identity.NormativeADRPath != NativeSearchNormativeADRPath || identity.NormativeADRSHA256 != expected.NormativeADRSHA256 || identity.NormativeADRBytes != expected.NormativeADRBytes || identity.SourceHead != expected.SourceHead || identity.SourceTree != expected.SourceTree || identity.HarnessSHA256 != expected.HarnessSHA256 || identity.HarnessBytes != expected.HarnessBytes || identity.EffectiveSettingsSHA256 != expected.EffectiveSettingsSHA256 {
		return validationError("identity_mismatch", "accepted_native_search_regression_identity", "/identity")
	}
	return nil
}

func validateNativeSearchRegressionObservations(observations []NativeSearchRegressionObservationV1) error {
	expected := nativeSearchRegressionExpectedMatrix()
	if len(observations) != len(expected) {
		return validationError("matrix_mismatch", "exact_native_search_regression_matrix", "/observations")
	}
	seen := map[string]struct{}{}
	for i, observation := range observations {
		if err := validateNativeSearchRegressionObservation(observation); err != nil {
			return err
		}
		key := string(observation.CaseID) + "|" + string(observation.Protocol)
		if _, duplicate := seen[key]; duplicate {
			return validationError("duplicate_observation", "exact_native_search_regression_matrix", "/observations")
		}
		seen[key] = struct{}{}
		if _, ok := expected[key]; !ok {
			_ = i
			return validationError("matrix_mismatch", "exact_native_search_regression_matrix", "/observations")
		}
	}
	for key := range expected {
		if _, ok := seen[key]; !ok {
			return validationError("matrix_mismatch", "exact_native_search_regression_matrix", "/observations")
		}
	}
	return nil
}

func validateNativeSearchRegressionObservation(observation NativeSearchRegressionObservationV1) error {
	if observation.Schema != NativeSearchRegressionObservationSchemaV1 {
		return validationError("invalid_schema", "native_search_regression_observation", "/observations/schema")
	}
	if _, ok := nativeSearchRegressionCaseRank[observation.CaseID]; !ok {
		return validationError("invalid_enum", "native_search_case", "/observations/case_id")
	}
	if _, ok := nativeSearchRegressionProtocolRank[observation.Protocol]; !ok {
		return validationError("invalid_enum", "native_search_protocol", "/observations/protocol")
	}
	if observation.EndpointPath != nativeSearchRegressionEndpoint(observation.Protocol) {
		return validationError("invalid_binding", "protocol_endpoint", "/observations/endpoint_path")
	}
	if observation.HTTPStatus != 200 || observation.Terminal != nativeSearchRegressionTerminal(observation.Protocol) {
		return validationError("invalid_result", "successful_http_terminal", "/observations")
	}
	tools, promptMode := nativeSearchRegressionCaseContract(observation.CaseID)
	if observation.ClientToolsCount != tools || observation.ToolPromptMode != promptMode {
		return validationError("invalid_binding", "case_tool_contract", "/observations")
	}
	if observation.NativeSearchRequested != "inherit" || observation.NativeSearchEffective != "unknown" {
		return validationError("scope_overclaim", "native_search_requested_vs_effective", "/observations")
	}
	if !observation.SourceAttributionObserved || observation.EventCounts.SourceAttributionMarkers < 1 || observation.EventCounts.SearchResultMarkers < 1 || observation.EventCounts.RawFrames < 1 || observation.EventCounts.SearchProgress < 1 {
		return validationError("fixture_signal_missing", "synthetic_search_and_attribution", "/observations/event_counts")
	}
	if observation.EventCounts.ToolProgress < 0 || observation.EventCounts.CodeProgress < 0 || observation.EventCounts.Message < 0 || observation.EventCounts.Unknown < 0 {
		return validationError("invalid_count", "non_negative_event_counts", "/observations/event_counts")
	}
	if observation.RawEventsRetained || observation.ContentRetained {
		return validationError("privacy_forbidden", "redacted_observation_only", "/observations")
	}
	if observation.ObservationScope != NativeSearchScopeSyntheticFixture || observation.LiveMicrosoftStatus != NativeSearchLiveUnverified || observation.OpenAIWebSearchCapability != NativeSearchOpenAINotPromoted {
		return validationError("scope_overclaim", "offline_fixture_only", "/observations")
	}
	if observation.UpstreamAttempts != 1 {
		return validationError("invalid_result", "single_upstream_attempt", "/observations/upstream_attempts")
	}
	return nil
}

func nativeSearchRegressionExpectedMatrix() map[string]struct{} {
	matrix := map[string]struct{}{}
	for _, caseID := range []NativeSearchRegressionCase{NativeSearchCaseZeroTools, NativeSearchCaseGeneralTools} {
		for protocol := range nativeSearchRegressionProtocolRank {
			matrix[string(caseID)+"|"+string(protocol)] = struct{}{}
		}
	}
	for _, protocol := range []NativeSearchRegressionProtocol{NativeSearchProtocolResponsesNonStream, NativeSearchProtocolResponsesStream} {
		matrix[string(NativeSearchCaseCustomExec)+"|"+string(protocol)] = struct{}{}
	}
	return matrix
}

func nativeSearchRegressionCaseContract(caseID NativeSearchRegressionCase) (int, NativeSearchToolPromptMode) {
	switch caseID {
	case NativeSearchCaseZeroTools:
		return 0, NativeSearchPromptNone
	case NativeSearchCaseGeneralTools:
		return 1, NativeSearchPromptClientPluginsNoExecution
	case NativeSearchCaseCustomExec:
		return 1, NativeSearchPromptScopedCustomExec
	default:
		return -1, ""
	}
}

func nativeSearchRegressionEndpoint(protocol NativeSearchRegressionProtocol) string {
	switch protocol {
	case NativeSearchProtocolChatNonStream, NativeSearchProtocolChatStream:
		return "/v1/chat/completions"
	case NativeSearchProtocolResponsesNonStream, NativeSearchProtocolResponsesStream:
		return "/v1/responses"
	default:
		return ""
	}
}

func nativeSearchRegressionTerminal(protocol NativeSearchRegressionProtocol) NativeSearchRegressionTerminal {
	switch protocol {
	case NativeSearchProtocolChatNonStream, NativeSearchProtocolResponsesNonStream:
		return NativeSearchTerminalJSON
	case NativeSearchProtocolChatStream:
		return NativeSearchTerminalChatDone
	case NativeSearchProtocolResponsesStream:
		return NativeSearchTerminalResponseCompleted
	default:
		return ""
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return validationError("invalid_json", "single_json_object", "/")
	}
	return nil
}
