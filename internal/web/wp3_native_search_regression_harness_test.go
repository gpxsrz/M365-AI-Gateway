package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"m365-native/internal/chathub"
	evidence "m365-native/internal/evidence/offline"
)

const wp3NativeSearchSettingsSchemaV1 = "m365-wp3-native-search-regression-settings/v1"

type WP3NativeSearchRegressionHarnessOptions struct {
	NormativeADRSHA256 string
	NormativeADRBytes  int64
	SourceHead         string
	SourceTree         string
	HarnessSHA256      string
	HarnessBytes       int64
}

type wp3NativeSearchAttempt struct {
	clientToolsCount              int
	legacyGlobalRestrictionSeen   bool
	scopedSearchAllowanceObserved bool
}

type wp3NativeSearchHarnessChat struct {
	result   chathub.Result
	attempts []wp3NativeSearchAttempt
}

func (chat *wp3NativeSearchHarnessChat) record(request chathub.Request) {
	chat.attempts = append(chat.attempts, wp3NativeSearchAttempt{
		clientToolsCount:              len(request.Tools),
		legacyGlobalRestrictionSeen:   strings.Contains(request.Text, "Never use, request, or mention Microsoft 365/Copilot native tools"),
		scopedSearchAllowanceObserved: strings.Contains(request.Text, "Microsoft 365 native Bing web search, citations, grounding, and read-only information retrieval remain allowed"),
	})
}

func (chat *wp3NativeSearchHarnessChat) Chat(_ context.Context, _ chathub.Account, request chathub.Request) (chathub.Result, error) {
	chat.record(request)
	return chat.result, nil
}

func (chat *wp3NativeSearchHarnessChat) ChatWithDelta(_ context.Context, _ chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	chat.record(request)
	if emit != nil && chat.result.Text != "" {
		if err := emit(chat.result.Text); err != nil {
			return chathub.Result{}, err
		}
	}
	return chat.result, nil
}

func (chat *wp3NativeSearchHarnessChat) ChatWithEvents(_ context.Context, _ chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	chat.record(request)
	if emit != nil && chat.result.Text != "" {
		if err := emit(chathub.StreamEvent{Kind: "text", Text: chat.result.Text}); err != nil {
			return chathub.Result{}, err
		}
	}
	return chat.result, nil
}

func BuildWP3NativeSearchRegressionPackage(options WP3NativeSearchRegressionHarnessOptions) (evidence.BuiltNativeSearchRegressionPackage, error) {
	if envTrue("M365_INCLUDE_UPSTREAM_EVENTS") {
		return evidence.BuiltNativeSearchRegressionPackage{}, errors.New("WP3 native search harness requires upstream event exposure to remain disabled")
	}
	settingsJSON, settingsSHA256, err := wp3NativeSearchEffectiveSettings()
	if err != nil {
		return evidence.BuiltNativeSearchRegressionPackage{}, fmt.Errorf("WP3 native search effective settings: %w", err)
	}
	if len(settingsJSON) == 0 {
		return evidence.BuiltNativeSearchRegressionPackage{}, errors.New("WP3 native search effective settings are empty")
	}
	observations := make([]evidence.NativeSearchRegressionObservationV1, 0, 10)
	for _, fixture := range wp3NativeSearchFixtureMatrix() {
		observation, err := runWP3NativeSearchFixture(fixture)
		if err != nil {
			return evidence.BuiltNativeSearchRegressionPackage{}, fmt.Errorf("%s %s: %w", fixture.caseID, fixture.protocol, err)
		}
		observations = append(observations, observation)
	}
	return evidence.BuildNativeSearchRegressionPackage(evidence.NativeSearchRegressionBuildInput{
		Identity: evidence.NativeSearchRegressionIdentityInput{
			NormativeADRSHA256:      options.NormativeADRSHA256,
			NormativeADRBytes:       options.NormativeADRBytes,
			SourceHead:              options.SourceHead,
			SourceTree:              options.SourceTree,
			HarnessSHA256:           options.HarnessSHA256,
			HarnessBytes:            options.HarnessBytes,
			EffectiveSettingsSHA256: settingsSHA256,
		},
		Observations: observations,
	})
}

type wp3NativeSearchFixture struct {
	caseID     evidence.NativeSearchRegressionCase
	protocol   evidence.NativeSearchRegressionProtocol
	stream     bool
	endpoint   string
	toolsCount int
	promptMode evidence.NativeSearchToolPromptMode
}

func wp3NativeSearchFixtureMatrix() []wp3NativeSearchFixture {
	fixtures := make([]wp3NativeSearchFixture, 0, 10)
	appendCase := func(caseID evidence.NativeSearchRegressionCase, protocols []evidence.NativeSearchRegressionProtocol, tools int, promptMode evidence.NativeSearchToolPromptMode) {
		for _, protocol := range protocols {
			fixtures = append(fixtures, wp3NativeSearchFixture{
				caseID:     caseID,
				protocol:   protocol,
				stream:     protocol == evidence.NativeSearchProtocolChatStream || protocol == evidence.NativeSearchProtocolResponsesStream,
				endpoint:   wp3NativeSearchEndpoint(protocol),
				toolsCount: tools,
				promptMode: promptMode,
			})
		}
	}
	all := []evidence.NativeSearchRegressionProtocol{
		evidence.NativeSearchProtocolChatNonStream,
		evidence.NativeSearchProtocolChatStream,
		evidence.NativeSearchProtocolResponsesNonStream,
		evidence.NativeSearchProtocolResponsesStream,
	}
	appendCase(evidence.NativeSearchCaseZeroTools, all, 0, evidence.NativeSearchPromptNone)
	appendCase(evidence.NativeSearchCaseGeneralTools, all, 1, evidence.NativeSearchPromptClientPluginsNoExecution)
	appendCase(evidence.NativeSearchCaseCustomExec, []evidence.NativeSearchRegressionProtocol{
		evidence.NativeSearchProtocolResponsesNonStream,
		evidence.NativeSearchProtocolResponsesStream,
	}, 1, evidence.NativeSearchPromptScopedCustomExec)
	return fixtures
}

func runWP3NativeSearchFixture(fixture wp3NativeSearchFixture) (evidence.NativeSearchRegressionObservationV1, error) {
	result := chathub.Result{
		Text:           "WP3_SYNTHETIC_SEARCH_ANSWER",
		ConversationID: "wp3-harness-conversation",
		SessionID:      "wp3-harness-session",
		Events:         wp3NativeSearchSyntheticEvents(),
	}
	chat := &wp3NativeSearchHarnessChat{result: result}
	settings := defaultRuntimeSettings()
	settings.ToolPlanningMode = "native"
	harness, cleanup, err := newWP2RouteProtocolHarnessServerWithSettings(chat, settings)
	if err != nil {
		return evidence.NativeSearchRegressionObservationV1{}, err
	}
	defer cleanup()

	request, err := wp3NativeSearchRequest(fixture)
	if err != nil {
		return evidence.NativeSearchRegressionObservationV1{}, err
	}
	writer := httptest.NewRecorder()
	harness.serveWithAuth("api_key", writer, request)
	terminal, err := wp3NativeSearchTerminal(fixture.protocol, writer.Body.String())
	if err != nil {
		return evidence.NativeSearchRegressionObservationV1{}, err
	}
	if writer.Code != http.StatusOK {
		return evidence.NativeSearchRegressionObservationV1{}, fmt.Errorf("HTTP status %d", writer.Code)
	}
	if wp3ResponseContainsRawEvents(writer.Body.String(), fixture.stream) {
		return evidence.NativeSearchRegressionObservationV1{}, errors.New("HTTP response retained raw upstream events")
	}
	if len(chat.attempts) != 1 {
		return evidence.NativeSearchRegressionObservationV1{}, fmt.Errorf("upstream attempts %d", len(chat.attempts))
	}
	attempt := chat.attempts[0]
	if attempt.clientToolsCount != fixture.toolsCount {
		return evidence.NativeSearchRegressionObservationV1{}, fmt.Errorf("client tools %d, want %d", attempt.clientToolsCount, fixture.toolsCount)
	}
	if attempt.legacyGlobalRestrictionSeen {
		return evidence.NativeSearchRegressionObservationV1{}, errors.New("legacy global native-tool restriction reached the upstream request")
	}
	if attempt.scopedSearchAllowanceObserved != (fixture.caseID == evidence.NativeSearchCaseCustomExec) {
		return evidence.NativeSearchRegressionObservationV1{}, errors.New("custom exec search allowance prompt contract mismatch")
	}
	summary := chathub.SummarizeSearchEvents(result.Events)
	return evidence.NativeSearchRegressionObservationV1{
		Schema:                    evidence.NativeSearchRegressionObservationSchemaV1,
		CaseID:                    fixture.caseID,
		Protocol:                  fixture.protocol,
		EndpointPath:              fixture.endpoint,
		HTTPStatus:                writer.Code,
		Terminal:                  terminal,
		ClientToolsCount:          attempt.clientToolsCount,
		ToolPromptMode:            fixture.promptMode,
		NativeSearchRequested:     "inherit",
		NativeSearchEffective:     "unknown",
		SourceAttributionObserved: summary.SourceAttributionObserved,
		EventCounts: evidence.NativeSearchRegressionEventCountsV1{
			RawFrames:                summary.RawFrames,
			SearchProgress:           summary.SearchProgress,
			ToolProgress:             summary.ToolProgress,
			CodeProgress:             summary.CodeProgress,
			Message:                  summary.Message,
			Unknown:                  summary.Unknown,
			SourceAttributionMarkers: summary.SourceAttributionMarkers,
			SearchResultMarkers:      summary.SearchResultMarkers,
		},
		RawEventsRetained:         false,
		ContentRetained:           false,
		ObservationScope:          evidence.NativeSearchScopeSyntheticFixture,
		LiveMicrosoftStatus:       evidence.NativeSearchLiveUnverified,
		OpenAIWebSearchCapability: evidence.NativeSearchOpenAINotPromoted,
		UpstreamAttempts:          len(chat.attempts),
	}, nil
}

func wp3NativeSearchRequest(fixture wp3NativeSearchFixture) (*http.Request, error) {
	var body map[string]any
	switch fixture.protocol {
	case evidence.NativeSearchProtocolChatNonStream, evidence.NativeSearchProtocolChatStream:
		body = map[string]any{
			"model":    "gpt-5.6-reasoning",
			"messages": []any{map[string]any{"role": "user", "content": "WP3 synthetic search canary"}},
			"stream":   fixture.stream,
		}
		if fixture.caseID == evidence.NativeSearchCaseGeneralTools {
			body["tools"] = []any{map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_current_time",
					"description": "Return a synthetic time value.",
					"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
				},
			}}
			body["tool_choice"] = "none"
		}
	case evidence.NativeSearchProtocolResponsesNonStream, evidence.NativeSearchProtocolResponsesStream:
		body = map[string]any{
			"model":  "gpt-5.6-reasoning",
			"input":  "WP3 synthetic search canary",
			"stream": fixture.stream,
		}
		switch fixture.caseID {
		case evidence.NativeSearchCaseGeneralTools:
			body["tools"] = []any{map[string]any{
				"type":        "function",
				"name":        "get_current_time",
				"description": "Return a synthetic time value.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			}}
			body["tool_choice"] = "none"
		case evidence.NativeSearchCaseCustomExec:
			body["tools"] = []any{map[string]any{
				"type":        "custom",
				"name":        "exec",
				"description": "Synthetic local execution bridge.",
			}}
			body["tool_choice"] = "none"
		}
	default:
		return nil, fmt.Errorf("unsupported protocol %q", fixture.protocol)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return httptest.NewRequest(http.MethodPost, fixture.endpoint, strings.NewReader(string(raw))), nil
}

func wp3NativeSearchTerminal(protocol evidence.NativeSearchRegressionProtocol, body string) (evidence.NativeSearchRegressionTerminal, error) {
	switch protocol {
	case evidence.NativeSearchProtocolChatNonStream, evidence.NativeSearchProtocolResponsesNonStream:
		var response map[string]any
		if json.Unmarshal([]byte(body), &response) != nil {
			return "", errors.New("invalid JSON response")
		}
		if _, exists := response["error"]; exists {
			return "", errors.New("error response")
		}
		return evidence.NativeSearchTerminalJSON, nil
	case evidence.NativeSearchProtocolChatStream:
		if strings.Count(body, "data: [DONE]") != 1 || strings.Contains(body, `"error"`) {
			return "", errors.New("invalid Chat Completions terminal")
		}
		return evidence.NativeSearchTerminalChatDone, nil
	case evidence.NativeSearchProtocolResponsesStream:
		if strings.Count(body, "event: response.completed") != 1 || strings.Contains(body, "event: response.failed") {
			return "", errors.New("invalid Responses terminal")
		}
		return evidence.NativeSearchTerminalResponseCompleted, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func wp3ResponseContainsRawEvents(body string, stream bool) bool {
	if !stream {
		var value any
		if json.Unmarshal([]byte(body), &value) != nil {
			return true
		}
		return wp3JSONContainsEventsField(value)
	}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &value) != nil {
			continue
		}
		if wp3JSONContainsEventsField(value) {
			return true
		}
	}
	return false
}

func wp3JSONContainsEventsField(value any) bool {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if wp3JSONContainsEventsField(child) {
				return true
			}
		}
	case map[string]any:
		for key, child := range node {
			if key == "events" || wp3JSONContainsEventsField(child) {
				return true
			}
		}
	}
	return false
}

func wp3NativeSearchSyntheticEvents() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`{"type":1,"target":"update","arguments":[{"messages":[{"messageType":"Progress","contentType":"SearchResults","text":"WP3_SYNTHETIC_SEARCH_QUERY","searchQueries":["WP3_SYNTHETIC_SEARCH_QUERY"],"sourceAttributions":[{"provider":"bing","url":"https://synthetic.example/source"}]},{"messageType":"","contentType":"","text":"WP3_SYNTHETIC_SEARCH_ANSWER"}]}]}`),
		json.RawMessage(`{"type":3}`),
	}
}

func wp3NativeSearchEffectiveSettings() ([]byte, string, error) {
	settings := struct {
		Schema                string `json:"schema"`
		CanonicalRoute        string `json:"canonical_route"`
		ToolPlanningMode      string `json:"tool_planning_mode"`
		GeneralToolChoice     string `json:"general_tool_choice"`
		CustomExecToolChoice  string `json:"custom_exec_tool_choice"`
		NativeSearchRequested string `json:"native_search_requested"`
		IncludeUpstreamEvents bool   `json:"include_upstream_events"`
		FixtureMode           string `json:"fixture_mode"`
		LiveMicrosoftAccess   bool   `json:"live_microsoft_access"`
		ProductionAccess      bool   `json:"production_access"`
		HermesAccess          bool   `json:"hermes_access"`
	}{
		Schema:                wp3NativeSearchSettingsSchemaV1,
		CanonicalRoute:        "gpt-5.6-reasoning",
		ToolPlanningMode:      "native",
		GeneralToolChoice:     "none",
		CustomExecToolChoice:  "none",
		NativeSearchRequested: "inherit",
		FixtureMode:           "offline_synthetic",
	}
	canonical, err := json.Marshal(settings)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func wp3NativeSearchEndpoint(protocol evidence.NativeSearchRegressionProtocol) string {
	switch protocol {
	case evidence.NativeSearchProtocolChatNonStream, evidence.NativeSearchProtocolChatStream:
		return "/v1/chat/completions"
	case evidence.NativeSearchProtocolResponsesNonStream, evidence.NativeSearchProtocolResponsesStream:
		return "/v1/responses"
	default:
		return ""
	}
}
