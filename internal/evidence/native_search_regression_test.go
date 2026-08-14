package evidence_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	. "m365-native/internal/evidence/offline"
)

func TestBuildNativeSearchRegressionPackageIsDeterministicClosedAndScoped(t *testing.T) {
	input := nativeSearchRegressionTestInput()
	first, err := BuildNativeSearchRegressionPackage(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildNativeSearchRegressionPackage(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("native search regression package is not deterministic")
	}
	if first.Package.Schema != NativeSearchRegressionPackageSchemaV1 || len(first.Package.Observations) != 10 {
		t.Fatalf("package=%#v", first.Package)
	}
	if first.Package.LiveMicrosoftStatus != NativeSearchLiveUnverified || first.Package.OpenAIWebSearchCapability != NativeSearchOpenAINotPromoted {
		t.Fatalf("scope overclaim: %#v", first.Package)
	}
	for _, field := range []string{"legacy_global_restriction_seen", "scoped_search_allowance_observed"} {
		if bytes.Contains(first.CanonicalJSON, []byte(`"`+field+`"`)) {
			t.Fatalf("unrequested prompt diagnostic persisted in evidence: %s", field)
		}
	}
	if first.Package.Identity.FixtureSetSHA256 == "" ||
		first.Package.Identity.NormativeADRPath != NativeSearchNormativeADRPath ||
		first.Package.Identity.NormativeADRBytes != input.Identity.NormativeADRBytes ||
		first.Package.Identity.SourceHead != input.Identity.SourceHead ||
		first.Package.Identity.SourceTree != input.Identity.SourceTree {
		t.Fatalf("identity=%#v", first.Package.Identity)
	}
	for _, observation := range first.Package.Observations {
		if !observation.SourceAttributionObserved || observation.EventCounts.SourceAttributionMarkers < 1 || observation.EventCounts.SearchResultMarkers < 1 {
			t.Fatalf("missing synthetic search signal: %#v", observation)
		}
		if observation.RawEventsRetained || observation.ContentRetained || observation.ObservationScope != NativeSearchScopeSyntheticFixture || observation.LiveMicrosoftStatus != NativeSearchLiveUnverified || observation.OpenAIWebSearchCapability != NativeSearchOpenAINotPromoted {
			t.Fatalf("privacy/scope drift: %#v", observation)
		}
	}

	validated, err := ValidateNativeSearchRegressionPackage(first.CanonicalJSON, NativeSearchRegressionExpected{
		NormativeADRSHA256:      input.Identity.NormativeADRSHA256,
		NormativeADRBytes:       input.Identity.NormativeADRBytes,
		SourceHead:              input.Identity.SourceHead,
		SourceTree:              input.Identity.SourceTree,
		HarnessSHA256:           input.Identity.HarnessSHA256,
		HarnessBytes:            input.Identity.HarnessBytes,
		EffectiveSettingsSHA256: input.Identity.EffectiveSettingsSHA256,
	})
	if err != nil || validated.ChecksumSHA256 != first.ChecksumSHA256 {
		t.Fatalf("validate: %#v err=%v", validated, err)
	}
}

func TestNativeSearchRegressionPackageRejectsMatrixAndScopeDrift(t *testing.T) {
	input := nativeSearchRegressionTestInput()
	input.Observations = input.Observations[:9]
	if _, err := BuildNativeSearchRegressionPackage(input); err == nil {
		t.Fatal("incomplete matrix accepted")
	}

	input = nativeSearchRegressionTestInput()
	input.Observations[0].LiveMicrosoftStatus = NativeSearchLiveVerified
	if _, err := BuildNativeSearchRegressionPackage(input); err == nil {
		t.Fatal("live Microsoft status overclaim accepted")
	}

	input = nativeSearchRegressionTestInput()
	input.Observations[0].OpenAIWebSearchCapability = NativeSearchOpenAIVerified
	if _, err := BuildNativeSearchRegressionPackage(input); err == nil {
		t.Fatal("OpenAI web_search promotion accepted")
	}

	input = nativeSearchRegressionTestInput()
	input.Observations[0].RawEventsRetained = true
	if _, err := BuildNativeSearchRegressionPackage(input); err == nil {
		t.Fatal("raw event retention accepted")
	}
}

func TestValidateNativeSearchRegressionPackageRejectsUnknownDuplicateAndPrivacyFields(t *testing.T) {
	built, err := BuildNativeSearchRegressionPackage(nativeSearchRegressionTestInput())
	if err != nil {
		t.Fatal(err)
	}

	var generic map[string]any
	if err := json.Unmarshal(built.CanonicalJSON, &generic); err != nil {
		t.Fatal(err)
	}
	generic["unexpected"] = true
	unknown, _ := json.Marshal(generic)
	if _, err := ValidateNativeSearchRegressionPackage(unknown, nativeSearchRegressionExpected()); err == nil {
		t.Fatal("unknown field accepted")
	}

	duplicate := []byte(`{"schema":"m365-wp3-native-search-regression-package/v1","schema":"duplicate"}`)
	if _, err := ValidateNativeSearchRegressionPackage(duplicate, nativeSearchRegressionExpected()); err == nil {
		t.Fatal("duplicate key accepted")
	}

	privacy := bytes.Replace(built.CanonicalJSON, []byte(`"observations":[`), []byte(`"observations":[{"prompt":"sensitive"},`), 1)
	if _, err := ValidateNativeSearchRegressionPackage(privacy, nativeSearchRegressionExpected()); err == nil {
		t.Fatal("privacy field accepted")
	}
}

func nativeSearchRegressionTestInput() NativeSearchRegressionBuildInput {
	return NativeSearchRegressionBuildInput{
		Identity: NativeSearchRegressionIdentityInput{
			NormativeADRSHA256:      strings.Repeat("1", 64),
			NormativeADRBytes:       135432,
			SourceHead:              strings.Repeat("2", 40),
			SourceTree:              strings.Repeat("5", 40),
			HarnessSHA256:           strings.Repeat("3", 64),
			HarnessBytes:            123456,
			EffectiveSettingsSHA256: strings.Repeat("4", 64),
		},
		Observations: nativeSearchRegressionTestObservations(),
	}
}

func nativeSearchRegressionExpected() NativeSearchRegressionExpected {
	input := nativeSearchRegressionTestInput()
	return NativeSearchRegressionExpected{
		NormativeADRSHA256:      input.Identity.NormativeADRSHA256,
		NormativeADRBytes:       input.Identity.NormativeADRBytes,
		SourceHead:              input.Identity.SourceHead,
		SourceTree:              input.Identity.SourceTree,
		HarnessSHA256:           input.Identity.HarnessSHA256,
		HarnessBytes:            input.Identity.HarnessBytes,
		EffectiveSettingsSHA256: input.Identity.EffectiveSettingsSHA256,
	}
}

func nativeSearchRegressionTestObservations() []NativeSearchRegressionObservationV1 {
	observations := []NativeSearchRegressionObservationV1{}
	appendCase := func(caseID NativeSearchRegressionCase, protocols []NativeSearchRegressionProtocol, tools int, promptMode NativeSearchToolPromptMode) {
		for _, protocol := range protocols {
			terminal := NativeSearchTerminalJSON
			if protocol == NativeSearchProtocolChatStream {
				terminal = NativeSearchTerminalChatDone
			}
			if protocol == NativeSearchProtocolResponsesStream {
				terminal = NativeSearchTerminalResponseCompleted
			}
			observation := NativeSearchRegressionObservationV1{
				Schema:                    NativeSearchRegressionObservationSchemaV1,
				CaseID:                    caseID,
				Protocol:                  protocol,
				EndpointPath:              NativeSearchRegressionEndpoint(protocol),
				HTTPStatus:                200,
				Terminal:                  terminal,
				ClientToolsCount:          tools,
				ToolPromptMode:            promptMode,
				NativeSearchRequested:     "inherit",
				NativeSearchEffective:     "unknown",
				SourceAttributionObserved: true,
				EventCounts:               NativeSearchRegressionEventCountsV1{RawFrames: 2, SearchProgress: 1, Message: 1, SourceAttributionMarkers: 1, SearchResultMarkers: 1},
				RawEventsRetained:         false,
				ContentRetained:           false,
				ObservationScope:          NativeSearchScopeSyntheticFixture,
				LiveMicrosoftStatus:       NativeSearchLiveUnverified,
				OpenAIWebSearchCapability: NativeSearchOpenAINotPromoted,
				UpstreamAttempts:          1,
			}
			observations = append(observations, observation)
		}
	}
	all := []NativeSearchRegressionProtocol{NativeSearchProtocolChatNonStream, NativeSearchProtocolChatStream, NativeSearchProtocolResponsesNonStream, NativeSearchProtocolResponsesStream}
	appendCase(NativeSearchCaseZeroTools, all, 0, NativeSearchPromptNone)
	appendCase(NativeSearchCaseGeneralTools, all, 1, NativeSearchPromptClientPluginsNoExecution)
	appendCase(NativeSearchCaseCustomExec, []NativeSearchRegressionProtocol{NativeSearchProtocolResponsesNonStream, NativeSearchProtocolResponsesStream}, 1, NativeSearchPromptScopedCustomExec)
	return observations
}
