package web

import (
	"context"
	"encoding/json"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNonStreamBlocksUndeclaredNativeMutationBeforeToolEmission(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Events: []json.RawMessage{
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"tools":[`+routerFallbackTool+`],"tool_choice":"auto"`))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if code := wp1ErrorCode(t, rr); code != "native_mutation_blocked" {
		t.Fatalf("code=%q body=%s", code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"tool_calls"`) {
		t.Fatalf("blocked native mutation emitted a tool call: %s", rr.Body.String())
	}
}

func TestNonStreamBlocksArgumentlessNativeToolShape(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Events: []json.RawMessage{
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file"}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"tools":[`+routerFallbackTool+`],"tool_choice":"auto"`))
	if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "native_mutation_blocked" {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"tool_calls"`) {
		t.Fatalf("argumentless native mutation emitted a tool call: %s", rr.Body.String())
	}
}

func TestNonStreamBlocksNativeMutationBeforeArtifactFetch(t *testing.T) {
	const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/result.csv"
	result := artifactResultFixture(t, "result", []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "result.csv"}})
	result.Events = append(result.Events, json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`))
	chat := &wp1CandidateChat{result: result}
	server := newWP1CandidateServer(t, chat)
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.artifacts = store
	server.artifactOrigin = "https://sidecar.example.test"
	fetchCalls, tokenCalls := 0, 0
	server.artifactFetch = &artifactFetchClient{
		HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
			fetchCalls++
			return artifactResponse(http.StatusOK, "must-not-be-fetched"), nil
		})},
		Token: func(context.Context, string) (string, error) {
			tokenCalls++
			return "must-not-be-requested", nil
		},
	}

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("m365-auto", ""))
	if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "native_mutation_blocked" {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if fetchCalls != 0 || tokenCalls != 0 {
		t.Fatalf("blocked result reached artifact service: fetch=%d token=%d", fetchCalls, tokenCalls)
	}
}

func TestLegacyChatBlocksNativeMutationBeforeArtifactFetch(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		run  func(*Server, http.ResponseWriter, *http.Request)
	}{
		{name: "nonstream", path: "/api/chat", run: (*Server).chatOnce},
		{name: "buffered stream", path: "/api/chat/stream", run: (*Server).chatStream},
	} {
		t.Run(test.name, func(t *testing.T) {
			const upstream = "https://us.asyncgw.teams.microsoft.com/v1/objects/id/views/original/result.csv"
			result := artifactResultFixture(t, "result", []artifactFixture{{ReferenceID: "file", URL: upstream, Filename: "result.csv"}})
			result.Events = append(result.Events, json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`))
			server := newWP1CandidateServer(t, &wp1CandidateChat{result: result})
			store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			server.artifacts = store
			server.artifactOrigin = "https://sidecar.example.test"
			fetchCalls, tokenCalls := 0, 0
			server.artifactFetch = &artifactFetchClient{
				HTTPClient: &http.Client{Transport: artifactRoundTripFunc(func(*http.Request) (*http.Response, error) {
					fetchCalls++
					return artifactResponse(http.StatusOK, "must-not-be-fetched"), nil
				})},
				Token: func(context.Context, string) (string, error) {
					tokenCalls++
					return "must-not-be-requested", nil
				},
			}
			rr := httptest.NewRecorder()
			test.run(server, rr, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{"model":"m365-auto","message":"create file"}`)))
			if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "native_mutation_blocked" {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if fetchCalls != 0 || tokenCalls != 0 || strings.Contains(rr.Body.String(), "event:") {
				t.Fatalf("blocked legacy result crossed safety boundary: fetch=%d token=%d body=%s", fetchCalls, tokenCalls, rr.Body.String())
			}
		})
	}
}

func TestResponsesPreservesNativeMutationBlockedCode(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Events: []json.RawMessage{
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := mustJSON(map[string]any{
		"model": "gpt-5.6-reasoning",
		"input": "delete notes",
		"tools": []any{map[string]any{"type": "function", "name": "terminal", "parameters": map[string]any{"type": "object"}}},
	})

	rr := httptest.NewRecorder()
	server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "native_mutation_blocked" {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestStreamingResponsesPreservesNativeMutationBlockedCode(t *testing.T) {
	chat := &wp1CandidateChat{events: []chathub.StreamEvent{{
		Kind: "tool", ToolName: "delete_file", ContentType: "ToolCall", Arguments: json.RawMessage(`{"path":"notes.txt"}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := mustJSON(map[string]any{
		"model":  "gpt-5.6-reasoning",
		"input":  "delete notes",
		"stream": true,
		"tools":  []any{map[string]any{"type": "function", "name": "terminal", "parameters": map[string]any{"type": "object"}}},
	})

	rr := httptest.NewRecorder()
	server.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"code":"native_mutation_blocked"`) || !strings.Contains(rr.Body.String(), "event: response.failed") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAnthropicPreservesNativeMutationBlockedCode(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Events: []json.RawMessage{
		json.RawMessage(`{"messageType":"Progress","contentType":"ToolCall","pluginName":"delete_file","arguments":{"path":"notes.txt"}}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings
	body := mustJSON(map[string]any{
		"model":      "gpt-5.6-reasoning",
		"max_tokens": 128,
		"messages":   []any{map[string]any{"role": "user", "content": "delete notes"}},
		"tools":      []any{map[string]any{"name": "terminal", "input_schema": map[string]any{"type": "object"}}},
	})

	rr := httptest.NewRecorder()
	server.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	errorBody, _ := response["error"].(map[string]any)
	if rr.Code != http.StatusBadGateway || errorBody["code"] != "native_mutation_blocked" {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestStreamAllowsStructuredNativeSearchObservation(t *testing.T) {
	chat := &wp1CandidateChat{
		events: []chathub.StreamEvent{
			{Kind: "tool", ToolName: "web_search", ContentType: "SearchResults", MessageType: "Progress", Arguments: json.RawMessage(`{"query":"golang"}`)},
			{Kind: "text", Text: "search answer"},
		},
		result: chathub.Result{Text: "search answer"},
	}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"stream":true`))
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "native_mutation_blocked") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if text := chatStreamText(t, rr.Body.String()); text != "search answer" {
		t.Fatalf("stream text=%q body=%s", text, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"tool_calls"`) {
		t.Fatalf("native search observation became caller tool call: %s", rr.Body.String())
	}
}

func chatStreamText(t *testing.T, body string) string {
	t.Helper()
	var text strings.Builder
	for _, chunk := range openAIStreamChunks(t, body) {
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if content, _ := delta["content"].(string); content != "" {
			text.WriteString(content)
		}
	}
	return text.String()
}

func TestStreamBlocksArgumentlessNativeToolShape(t *testing.T) {
	chat := &wp1CandidateChat{events: []chathub.StreamEvent{{
		Kind: "tool", ToolName: "delete_file", ContentType: "ToolCall",
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"stream":true,"tools":[`+routerFallbackTool+`],"tool_choice":"auto"`))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"code":"native_mutation_blocked"`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"tool_calls"`) {
		t.Fatalf("argumentless native mutation emitted a tool call: %s", rr.Body.String())
	}
}

func TestStreamBlocksUndeclaredNativeMutationBeforeToolEmission(t *testing.T) {
	chat := &wp1CandidateChat{events: []chathub.StreamEvent{{
		Kind: "tool", ToolName: "delete_file", ContentType: "ToolCall", Arguments: json.RawMessage(`{"path":"notes.txt"}`),
	}}}
	server := newWP1CandidateServer(t, chat)
	settings := server.settings.get()
	settings.ToolPlanningMode = "native"
	server.settings.v = settings

	rr := httptest.NewRecorder()
	server.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"stream":true,"tools":[`+routerFallbackTool+`],"tool_choice":"auto"`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":"native_mutation_blocked"`) {
		t.Fatalf("missing blocked terminal: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"tool_calls"`) {
		t.Fatalf("blocked native mutation emitted a tool call: %s", rr.Body.String())
	}
}
