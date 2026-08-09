package web

import (
	"encoding/json"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatCompletionReportsFailClosedNativePolicyState(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("m365-auto", ""))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	response := wp1DecodeJSON(t, rr.Body.String())
	metadata, _ := response["m365"].(map[string]any)
	policy, _ := metadata["native_policy"].(map[string]any)
	if policy["schema"] != "m365-native-policy/v1" ||
		policy["requested_tool_policy"] != "none" ||
		policy["effective_tool_policy"] != "router" ||
		policy["native_search_status"] != "unverified" ||
		policy["native_mutation_status"] != "not_requested" ||
		policy["mutation_enforcement"] != "execution_enforced" ||
		policy["mutation_enforcement_verified"] != true ||
		policy["mutation_enforcement_scope"] != "sidecar_tool_emission" ||
		policy["upstream_native_mutation_status"] != "unverified" {
		t.Fatalf("native policy=%#v metadata=%#v", policy, metadata)
	}
	requested, _ := policy["requested_capabilities"].(map[string]any)
	effective, _ := policy["effective_capabilities"].(map[string]any)
	if requested["client_execution_tools"] != "enabled" ||
		requested["native_search"] != "inherit" ||
		requested["native_grounding"] != "inherit" ||
		requested["native_read_tools"] != "inherit" ||
		requested["native_mutation_tools"] != "disabled" ||
		effective["client_execution_tools"] != "enabled" ||
		effective["native_search"] != "inherit" ||
		effective["native_grounding"] != "inherit" ||
		effective["native_read_tools"] != "inherit" ||
		effective["native_mutation_tools"] != "disabled" {
		t.Fatalf("typed native policy requested=%#v effective=%#v", requested, effective)
	}
	if _, ok := metadata["events"]; ok {
		t.Fatalf("raw upstream events leaked by default: %#v", metadata)
	}
}

func TestResponsesReportsSameNativePolicyState(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	rr := httptest.NewRecorder()
	body := mustJSON(map[string]any{"model": "m365-auto", "input": "hello"})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	server.responses(rr, request)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	response := wp1DecodeJSON(t, rr.Body.String())
	metadata, _ := response["m365"].(map[string]any)
	policy, _ := metadata["native_policy"].(map[string]any)
	if policy["schema"] != nativePolicySchemaV1 ||
		policy["requested_tool_policy"] != "none" ||
		policy["effective_tool_policy"] != "router" ||
		policy["mutation_enforcement"] != "execution_enforced" ||
		policy["mutation_enforcement_verified"] != true ||
		policy["mutation_enforcement_scope"] != "sidecar_tool_emission" ||
		policy["upstream_native_mutation_status"] != "unverified" {
		t.Fatalf("native policy=%#v response=%#v", policy, response)
	}
}

func TestSettingsRejectUnknownNativePlanningMode(t *testing.T) {
	settings := defaultRuntimeSettings()
	settings.ToolPlanningMode = "unknown-mode"
	if err := validateSettings(settings); err == nil {
		t.Fatal("unknown tool planning mode accepted")
	}
}

func TestUnknownNativePlanningModeFailsClosed(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	settings := server.settings.get()
	settings.ToolPlanningMode = "unknown-mode"
	server.settings.v = settings
	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("m365-auto", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code := wp1ErrorCode(t, rr); code != "invalid_native_policy" {
		t.Fatalf("code=%q body=%s", code, rr.Body.String())
	}
}

func TestInvalidNativePlanningModePreventsStartup(t *testing.T) {
	tests := []struct {
		name      string
		persisted bool
	}{
		{name: "environment"},
		{name: "persisted settings", persisted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := sharedSettings
			sharedSettings = nil
			t.Cleanup(func() { sharedSettings = previous })
			dir := t.TempDir()
			t.Setenv("M365_DATA_DIR", "")
			t.Setenv("M365_TOKEN_CACHE", filepath.Join(dir, "accounts.json"))
			t.Setenv("M365_SETTINGS_FILE", filepath.Join(dir, "settings.json"))
			t.Setenv("M365_TOOL_PLANNING_MODE", "invalid-mode")
			if test.persisted {
				settings := defaultRuntimeSettings()
				settings.ToolPlanningMode = "invalid-mode"
				raw, err := json.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyStartupSettingsEnv(); err == nil {
				t.Fatal("invalid native planning mode did not prevent startup environment application")
			}
			if _, err := New(); err == nil {
				t.Fatal("invalid native planning mode did not prevent server startup")
			}
		})
	}
}

func TestStreamingChatCompletionReportsNativePolicy(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("m365-auto", `,"stream":true`))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, chunk := range openAIStreamChunks(t, rr.Body.String()) {
		metadata, _ := chunk["m365"].(map[string]any)
		policy, _ := metadata["native_policy"].(map[string]any)
		if policy["schema"] != nativePolicySchemaV1 || policy["effective_tool_policy"] != "router" {
			t.Fatalf("native policy=%#v chunk=%#v", policy, chunk)
		}
	}
}

func TestStreamingResponsesReportsNativePolicy(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	rr := httptest.NewRecorder()
	body := mustJSON(map[string]any{"model": "m365-auto", "input": "hello", "stream": true})
	server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, event := range wp1SSEEvents(t, rr.Body.String()) {
		metadata, _ := event.payload["m365"].(map[string]any)
		policy, _ := metadata["native_policy"].(map[string]any)
		if policy["schema"] != nativePolicySchemaV1 || policy["effective_tool_policy"] != "router" {
			t.Fatalf("event=%q native policy=%#v payload=%#v", event.name, policy, event.payload)
		}
	}
	response := wp1ResponsesCompleted(t, rr.Body.String())
	metadata, _ := response["m365"].(map[string]any)
	policy, _ := metadata["native_policy"].(map[string]any)
	if policy["schema"] != nativePolicySchemaV1 || policy["effective_tool_policy"] != "router" {
		t.Fatalf("native policy=%#v response=%#v", policy, response)
	}
}

func firstOpenAIStreamChunk(t *testing.T, body string) map[string]any {
	t.Helper()
	return openAIStreamChunks(t, body)[0]
}

func openAIStreamChunks(t *testing.T, body string) []map[string]any {
	t.Helper()
	chunks := []map[string]any{}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		chunks = append(chunks, wp1DecodeJSON(t, strings.TrimSpace(strings.TrimPrefix(line, "data: "))))
	}
	if len(chunks) == 0 {
		t.Fatalf("chat completion chunk missing: %s", body)
	}
	return chunks
}

func TestResponsesCustomExecReportsSidecarExecutionEnforcement(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "ok"}})
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := mustJSON(map[string]any{
		"model": "m365-auto",
		"input": "inspect",
		"tools": []any{map[string]any{"type": "custom", "name": "exec", "description": "local execution"}},
	})
	rr := httptest.NewRecorder()
	server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := wp1DecodeJSON(t, rr.Body.String())
	metadata, _ := response["m365"].(map[string]any)
	policy, _ := metadata["native_policy"].(map[string]any)
	if policy["requested_tool_policy"] != "custom_exec_only" ||
		policy["effective_tool_policy"] != "custom_exec_only" ||
		policy["native_mutation_status"] != "not_requested" ||
		policy["mutation_enforcement"] != "execution_enforced" ||
		policy["mutation_enforcement_verified"] != true ||
		policy["mutation_enforcement_scope"] != "sidecar_tool_emission" ||
		policy["upstream_native_mutation_status"] != "unverified" {
		t.Fatalf("native policy=%#v response=%#v", policy, response)
	}
}

func TestBufferedToolStreamReportsNativePolicy(t *testing.T) {
	policy, err := resolveNativePolicy("native", terminalCatalog())
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	response := bufferedToolResponse("terminal", `{"command":"status"}`)
	if err := writeBufferedChatCompletionStream(rr, response, routeResolution{}, terminalCatalog(), "auto", policy); err != nil {
		t.Fatal(err)
	}
	chunk := firstOpenAIStreamChunk(t, rr.Body.String())
	metadata, _ := chunk["m365"].(map[string]any)
	got, _ := metadata["native_policy"].(map[string]any)
	if got["schema"] != nativePolicySchemaV1 || got["native_mutation_status"] != "unverified" {
		t.Fatalf("native policy=%#v chunk=%#v", got, chunk)
	}
}

func TestNativeToolResponseReportsSidecarExecutionEnforcement(t *testing.T) {
	server := newWP1CandidateServer(t, &wp1CandidateChat{result: chathub.Result{Text: "```terminal\n{\"command\":\"status\"}\n```"}})
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	request := wp1ChatRequest("gpt-5.6-reasoning", `,"tools":[`+routerFallbackTool+`],"tool_choice":"auto"`)
	rr := httptest.NewRecorder()
	server.openaiChat(rr, request)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	response := wp1DecodeJSON(t, rr.Body.String())
	metadata, _ := response["m365"].(map[string]any)
	policy, _ := metadata["native_policy"].(map[string]any)
	if policy["requested_tool_policy"] != "declared_tools" ||
		policy["effective_tool_policy"] != "native" ||
		policy["native_mutation_status"] != "unverified" ||
		policy["mutation_enforcement"] != "execution_enforced" ||
		policy["mutation_enforcement_verified"] != true ||
		policy["mutation_enforcement_scope"] != "sidecar_tool_emission" ||
		policy["upstream_native_mutation_status"] != "unverified" {
		t.Fatalf("native policy=%#v response=%#v", policy, response)
	}
}
