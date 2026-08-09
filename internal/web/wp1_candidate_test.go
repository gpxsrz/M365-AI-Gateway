package web

import (
	"context"
	"encoding/json"
	"fmt"
	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type wp1CandidateChat struct {
	requests []chathub.Request
	result   chathub.Result
	results  []chathub.Result
	err      error
	events   []chathub.StreamEvent
}

func (f *wp1CandidateChat) Chat(_ context.Context, _ chathub.Account, req chathub.Request) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	if len(f.results) >= len(f.requests) {
		return f.results[len(f.requests)-1], f.err
	}
	return f.result, f.err
}

func (f *wp1CandidateChat) ChatWithDelta(_ context.Context, _ chathub.Account, req chathub.Request, emit func(string) error) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	if f.err == nil && emit != nil && f.result.Text != "" {
		if err := emit(f.result.Text); err != nil {
			return chathub.Result{}, err
		}
	}
	return f.result, f.err
}

func (f *wp1CandidateChat) ChatWithEvents(_ context.Context, _ chathub.Account, req chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	f.requests = append(f.requests, req)
	if f.err == nil && emit != nil {
		for _, event := range f.events {
			if err := emit(event); err != nil {
				return chathub.Result{}, err
			}
		}
		if len(f.events) == 0 && f.result.Text != "" {
			if err := emit(chathub.StreamEvent{Kind: "text", Text: f.result.Text}); err != nil {
				return chathub.Result{}, err
			}
		}
	}
	return f.result, f.err
}

func newWP1CandidateServer(t *testing.T, chat *wp1CandidateChat) *Server {
	t.Helper()
	store, err := auth.OpenStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(auth.TokenSet{
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		Email:       "candidate@example.test",
		HomeOID:     "test-oid",
		TenantID:    "test-tid",
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		tokens:        store,
		chat:          chat,
		settings:      &settingsStore{v: defaultRuntimeSettings()},
		resourceToken: func(context.Context, string) (string, error) { return "test-resource-token", nil },
	}
}

func wp1ChatRequest(model, extra string) *http.Request {
	body := `{"model":` + mustJSON(model) + `,"messages":[{"role":"user","content":"WP1 candidate canary"}]` + extra + `}`
	return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
}

func wp1DecodeJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode JSON: %v; body=%s", err, body)
	}
	return out
}

func wp1ErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	out := wp1DecodeJSON(t, rr.Body.String())
	errBody, _ := out["error"].(map[string]any)
	code, _ := errBody["code"].(string)
	return code
}

func wp1AssertRouteMetadata(t *testing.T, response map[string]any, requested, canonical, tone string, alias bool) {
	t.Helper()
	if response["model"] != requested {
		t.Fatalf("top-level model=%v response=%#v", response["model"], response)
	}
	metadata, _ := response["m365"].(map[string]any)
	required := []string{
		"requested_model", "response_model", "canonical_route", "resolved_tone", "route_kind",
		"operational_status", "mapping_evidence", "identity_status", "catalog_visibility",
		"alias_used", "fallback_used",
	}
	for _, key := range required {
		if _, ok := metadata[key]; !ok {
			t.Fatalf("missing metadata %q: %#v", key, metadata)
		}
	}
	if metadata["requested_model"] != requested || metadata["response_model"] != requested || metadata["canonical_route"] != canonical || metadata["resolved_tone"] != tone {
		t.Fatalf("route metadata=%#v", metadata)
	}
	if metadata["operational_status"] != "enabled" || metadata["alias_used"] != alias || metadata["fallback_used"] != false {
		t.Fatalf("route state metadata=%#v", metadata)
	}
	for _, forbidden := range []string{"authorization_source", "allowlist_matched", "request_access_policy", "capability_claims", "account_dependent"} {
		if _, exists := metadata[forbidden]; exists {
			t.Fatalf("provisional metadata %q leaked: %#v", forbidden, metadata)
		}
	}
}

type wp1SSEEvent struct {
	name    string
	payload map[string]any
}

func wp1SSEEvents(t *testing.T, body string) []wp1SSEEvent {
	t.Helper()
	events := []wp1SSEEvent{}
	for _, block := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		name := ""
		data := ""
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if name == "" || data == "" || data == "[DONE]" {
			continue
		}
		events = append(events, wp1SSEEvent{name: name, payload: wp1DecodeJSON(t, data)})
	}
	return events
}

func wp1ResponsesCompleted(t *testing.T, body string) map[string]any {
	t.Helper()
	for _, event := range wp1SSEEvents(t, body) {
		if event.name == "response.completed" {
			response, _ := event.payload["response"].(map[string]any)
			return response
		}
	}
	t.Fatalf("response.completed missing: %s", body)
	return nil
}

func TestWP1CandidateRouteRegistryHasNoProvisionalGovernance(t *testing.T) {
	for _, model := range []string{"claude", "gpt-5.4-quick", "gpt-5.3-think-deeper"} {
		resolution, err := resolveRoute(model, "", nil)
		if err != nil {
			t.Fatalf("resolve %s: %v", model, err)
		}
		if resolution.CatalogVisibility != catalogHidden || !resolution.AliasUsed {
			t.Fatalf("request-only alias %s resolution=%#v", model, resolution)
		}
	}
	listed := map[string]bool{}
	for _, route := range catalogRouteDefinitions(nil) {
		listed[route.ID] = true
	}
	for _, model := range []string{"claude", "gpt-5.4-quick", "gpt-5.3-think-deeper"} {
		if listed[model] {
			t.Fatalf("request-only alias leaked into catalog: %s", model)
		}
	}
	if _, err := resolveRoute("unknown-candidate-model", "", nil); err == nil {
		t.Fatal("unknown model unexpectedly resolved")
	} else if typed, ok := err.(*routeResolveError); !ok || typed.Status != http.StatusNotFound || typed.Code != "model_not_found" {
		t.Fatalf("unknown route error=%T %#v", err, err)
	}
	for _, tc := range []struct {
		model, tone string
	}{
		{model: "quick", tone: "Chat"},
		{model: "think-deeper", tone: "Reasoning"},
	} {
		resolution, err := resolveRoute(tc.model, "", nil)
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.model, err)
		}
		if resolution.CanonicalRoute != tc.model || resolution.ResolvedTone != tc.tone || resolution.RouteKind != routeKindWebMode || resolution.OperationalStatus != operationalEnabled || resolution.CatalogVisibility != catalogHidden || resolution.AliasUsed {
			t.Fatalf("superseded web route %s resolution=%#v", tc.model, resolution)
		}
	}
}

func TestWP1CandidateRequestOnlyAliasesAcrossFiveProtocols(t *testing.T) {
	cases := []struct {
		model, canonical, tone string
	}{
		{model: "claude", canonical: "claude-sonnet", tone: "Claude_Sonnet"},
		{model: "gpt-5.4-quick", canonical: "gpt-5.4", tone: "Gpt_5_4_Chat"},
		{model: "gpt-5.3-think-deeper", canonical: "gpt-5.3", tone: "Gpt_5_3_Chat"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			t.Run("chat-completions", func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "OK"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				s.openaiChat(rr, wp1ChatRequest(tc.model, ""))
				if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.tone {
					t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
				}
				wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), tc.model, tc.canonical, tc.tone, true)
			})

			t.Run("responses", func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "OK"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":%q,"input":"candidate canary"}`, tc.model)
				s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.tone {
					t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
				}
				wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), tc.model, tc.canonical, tc.tone, true)
			})

			t.Run("anthropic", func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "OK"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"candidate canary"}]}`, tc.model)
				s.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
				if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.tone {
					t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
				}
				wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), tc.model, tc.canonical, tc.tone, true)
			})

			t.Run("legacy-nonstream", func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "OK"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":%q,"message":"candidate canary"}`, tc.model)
				s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body)))
				if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.tone {
					t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
				}
				wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), tc.model, tc.canonical, tc.tone, true)
			})

			t.Run("legacy-stream", func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "OK"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":%q,"message":"candidate canary"}`, tc.model)
				s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body)))
				if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.tone {
					t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
				}
				events := wp1SSEEvents(t, rr.Body.String())
				if len(events) != 1 || events[0].name != "done" {
					t.Fatalf("events=%#v body=%s", events, rr.Body.String())
				}
				wp1AssertRouteMetadata(t, events[0].payload, tc.model, tc.canonical, tc.tone, true)
			})
		})
	}
}

func TestWP1CandidateExistingCustomMappingAcrossFiveProtocols(t *testing.T) {
	const model = "existing-custom-route"
	const tone = "Claude_Sonnet_Reasoning"
	configure := func(s *Server) {
		cfg := s.settings.get()
		cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{PublicModel: model, UpstreamTone: tone, DisplayName: "Existing custom route", DefaultReasoningLevel: "low"})
		s.settings.v = cfg
	}
	assert := func(t *testing.T, response map[string]any) {
		t.Helper()
		wp1AssertRouteMetadata(t, response, model, model, tone, false)
		metadata := response["m365"].(map[string]any)
		if metadata["configured_mapping"] != true || metadata["route_kind"] != "configured_mapping" {
			t.Fatalf("custom mapping metadata=%#v", metadata)
		}
	}

	for _, protocol := range []string{"chat-completions", "responses", "anthropic", "legacy-nonstream", "legacy-stream"} {
		t.Run(protocol, func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "CUSTOM_OK"}}
			s := newWP1CandidateServer(t, chat)
			configure(s)
			rr := httptest.NewRecorder()
			switch protocol {
			case "chat-completions":
				s.openaiChat(rr, wp1ChatRequest(model, `,"reasoning_effort":"high"`))
			case "responses":
				s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"custom canary","reasoning":{"effort":"high"}}`)))
			case "anthropic":
				s.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+model+`","max_tokens":64,"messages":[{"role":"user","content":"custom canary"}]}`)))
			case "legacy-nonstream":
				s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"`+model+`","message":"custom canary","reasoning_effort":"high"}`)))
			case "legacy-stream":
				s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"`+model+`","message":"custom canary","reasoning_effort":"high"}`)))
			}
			if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tone {
				t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
			}
			if protocol == "legacy-stream" {
				events := wp1SSEEvents(t, rr.Body.String())
				if len(events) != 1 || events[0].name != "done" {
					t.Fatalf("events=%#v body=%s", events, rr.Body.String())
				}
				assert(t, events[0].payload)
				return
			}
			assert(t, wp1DecodeJSON(t, rr.Body.String()))
		})
	}
}

func TestWP1CandidateLegacyEffortMatrixAndFixedReasoningRoute(t *testing.T) {
	cases := []struct {
		effort, wantTone string
	}{
		{effort: "", wantTone: "Gpt_5_5_Chat"},
		{effort: "none", wantTone: "Gpt_5_5_Chat"},
		{effort: "minimal", wantTone: "Gpt_5_5_Chat"},
		{effort: "low", wantTone: "Gpt_5_5_Chat"},
		{effort: "medium", wantTone: "Gpt_5_5_Reasoning"},
		{effort: "high", wantTone: "Gpt_5_5_Reasoning"},
		{effort: "xhigh", wantTone: "Gpt_5_5_Reasoning"},
	}
	for _, tc := range cases {
		t.Run(firstNonEmpty(tc.effort, "omitted"), func(t *testing.T) {
			chat := &wp1CandidateChat{result: chathub.Result{Text: "EFFORT_OK"}}
			s := newWP1CandidateServer(t, chat)
			extra := ""
			if tc.effort != "" {
				extra = `,"reasoning_effort":` + mustJSON(tc.effort)
			}
			rr := httptest.NewRecorder()
			s.openaiChat(rr, wp1ChatRequest("gpt-5.5", extra))
			if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != tc.wantTone {
				t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
			}
			wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), "gpt-5.5", "m365-gpt-5.5-quick-response", tc.wantTone, true)
		})
	}

	chat := &wp1CandidateChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
	s := newWP1CandidateServer(t, chat)
	rr := httptest.NewRecorder()
	s.openaiChat(rr, wp1ChatRequest("gpt-5.5", `,"reasoning_effort":"extreme"`))
	if rr.Code != http.StatusBadRequest || wp1ErrorCode(t, rr) != "invalid_reasoning_effort" || len(chat.requests) != 0 {
		t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
	}

	resolution, err := resolveRoute("gpt-5.6-reasoning", "none", nil)
	if err != nil || resolution.ResolvedTone != "Gpt_5_6_Reasoning" || !resolution.ReasoningEffortIgnored {
		t.Fatalf("fixed reasoning resolution=%#v err=%v", resolution, err)
	}
}

func TestWP1CandidateSupportedEffortMatrix(t *testing.T) {
	efforts := []string{"", "none", "minimal", "low", "medium", "high", "xhigh"}
	customMappings := []modelMapping{{PublicModel: "existing-custom-route", UpstreamTone: "Claude_Sonnet_Reasoning"}}
	cases := []struct {
		model, lowTone, highTone string
		locked                   bool
		mappings                 []modelMapping
	}{
		{model: "m365-auto", lowTone: "Magic", highTone: "Magic", locked: true},
		{model: "quick", lowTone: "Chat", highTone: "Chat", locked: true},
		{model: "think-deeper", lowTone: "Reasoning", highTone: "Reasoning", locked: true},
		{model: "m365-gpt-5.5-quick-response", lowTone: "Gpt_5_5_Chat", highTone: "Gpt_5_5_Chat", locked: true},
		{model: "gpt-5.6-reasoning", lowTone: "Gpt_5_6_Reasoning", highTone: "Gpt_5_6_Reasoning", locked: true},
		{model: "gpt-5.5", lowTone: "Gpt_5_5_Chat", highTone: "Gpt_5_5_Reasoning"},
		{model: "claude", lowTone: "Claude_Sonnet", highTone: "Claude_Sonnet_Reasoning"},
		{model: "gpt-5.4-quick", lowTone: "Gpt_5_4_Chat", highTone: "Gpt_Reasoning"},
		{model: "gpt-5.3-think-deeper", lowTone: "Gpt_5_3_Chat", highTone: "Gpt_Reasoning"},
		{model: "gpt-5.6-sol", lowTone: "Gpt_5_6_Reasoning", highTone: "Gpt_5_6_Reasoning", locked: true},
		{model: "existing-custom-route", lowTone: "Claude_Sonnet_Reasoning", highTone: "Claude_Sonnet_Reasoning", locked: true, mappings: customMappings},
	}
	for _, tc := range cases {
		for _, effort := range efforts {
			t.Run(tc.model+"/"+firstNonEmpty(effort, "omitted"), func(t *testing.T) {
				resolution, err := resolveRoute(tc.model, effort, tc.mappings)
				if err != nil {
					t.Fatal(err)
				}
				wantTone := tc.lowTone
				if effort == "medium" || effort == "high" || effort == "xhigh" {
					wantTone = tc.highTone
				}
				wantIgnored := tc.locked && effort != ""
				if resolution.ResolvedTone != wantTone || resolution.ReasoningEffortIgnored != wantIgnored {
					t.Fatalf("resolution=%#v want_tone=%q want_ignored=%t", resolution, wantTone, wantIgnored)
				}
			})
		}
	}
	whitespace, err := resolveRoute("m365-auto", "   ", nil)
	if err != nil || whitespace.ReasoningEffortIgnored {
		t.Fatalf("whitespace-only effort resolution=%#v err=%v", whitespace, err)
	}
}

func TestWP1CandidateMissingBlankAndUnknownModels(t *testing.T) {
	for _, modelField := range []string{"", `"model":"   ",`} {
		chat := &wp1CandidateChat{result: chathub.Result{Text: "DEFAULT_OK"}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		body := `{` + modelField + `"messages":[{"role":"user","content":"default canary"}]}`
		s.openaiChat(rr, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 || chat.requests[0].Tone != "Magic" {
			t.Fatalf("status=%d requests=%#v body=%s", rr.Code, chat.requests, rr.Body.String())
		}
		wp1AssertRouteMetadata(t, wp1DecodeJSON(t, rr.Body.String()), "m365-copilot", "m365-auto", "Magic", true)
	}
	for _, tc := range []struct{ model, code string }{{"unknown-candidate-model", "model_not_found"}} {
		for _, protocol := range []string{"chat-completions", "responses", "anthropic", "legacy-nonstream", "legacy-stream"} {
			t.Run(tc.model+"/"+protocol, func(t *testing.T) {
				chat := &wp1CandidateChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				switch protocol {
				case "chat-completions":
					s.openaiChat(rr, wp1ChatRequest(tc.model, ""))
				case "responses":
					body := fmt.Sprintf(`{"model":%q,"input":"negative canary"}`, tc.model)
					s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				case "anthropic":
					body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"negative canary"}]}`, tc.model)
					s.anthropicMessages(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)))
				case "legacy-nonstream":
					body := fmt.Sprintf(`{"model":%q,"message":"negative canary"}`, tc.model)
					s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body)))
				case "legacy-stream":
					body := fmt.Sprintf(`{"model":%q,"message":"negative canary"}`, tc.model)
					s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body)))
				}
				if rr.Code != http.StatusNotFound || wp1ErrorCode(t, rr) != tc.code || len(chat.requests) != 0 {
					t.Fatalf("model=%s protocol=%s status=%d upstream=%d body=%s", tc.model, protocol, rr.Code, len(chat.requests), rr.Body.String())
				}
			})
		}
	}
}

func TestWP1CandidateLegacyAdaptersFailBeforeCommit(t *testing.T) {
	toolEvent := json.RawMessage(`{"name":"lookup","arguments":{"query":"canary"}}`)
	toolDefinition := `{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}`
	cases := []struct {
		name      string
		result    chathub.Result
		extraBody string
		code      string
	}{
		{name: "empty", result: chathub.Result{}, code: "upstream_empty_response"},
		{name: "image-only", result: chathub.Result{Images: []string{"data:image/png;base64,V1Ax"}}, code: "upstream_empty_response"},
		{name: "tool-only", result: chathub.Result{Events: []json.RawMessage{toolEvent}}, extraBody: `,"tools":[` + toolDefinition + `]`, code: "native_mutation_blocked"},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				chat := &wp1CandidateChat{result: tc.result}
				s := newWP1CandidateServer(t, chat)
				rr := httptest.NewRecorder()
				body := `{"model":"gpt-5.6-reasoning","message":"legacy canary"` + tc.extraBody + `}`
				if stream {
					s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body)))
				} else {
					s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body)))
				}
				if tc.name == "image-only" && !stream {
					response := wp1DecodeJSON(t, rr.Body.String())
					images, _ := response["images"].([]any)
					if rr.Code != http.StatusOK || len(chat.requests) != 1 || len(images) != 1 || images[0] != "data:image/png;base64,V1Ax" {
						t.Fatalf("status=%d upstream=%d images=%#v body=%s", rr.Code, len(chat.requests), images, rr.Body.String())
					}
					wp1AssertRouteMetadata(t, response, "gpt-5.6-reasoning", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", true)
					return
				}
				if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != tc.code || len(chat.requests) != 1 {
					t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
				}
				if stream && (strings.Contains(rr.Body.String(), "event:") || strings.Contains(rr.Body.String(), "data:")) {
					t.Fatalf("legacy stream committed before validation: %s", rr.Body.String())
				}
			})
		}
	}
}

func TestWP1CandidateResponsesMixedTextToolAndImageLifecycle(t *testing.T) {
	t.Run("mixed-text-and-tool", func(t *testing.T) {
		chat := &wp1CandidateChat{
			result: chathub.Result{Text: "answer"},
			events: []chathub.StreamEvent{
				{Kind: "text", Text: "answer"},
				{Kind: "tool", ToolName: "lookup", Arguments: json.RawMessage(`{"query":"candidate"}`)},
			},
		}
		s := newWP1CandidateServer(t, chat)
		cfg := s.settings.get()
		cfg.ToolPlanningMode = "native"
		s.settings.v = cfg
		rr := httptest.NewRecorder()
		body := `{"model":"gpt-5.6-reasoning","input":"mixed canary","stream":true,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
		events := wp1SSEEvents(t, rr.Body.String())
		addedAt := map[float64]int{}
		seenText := false
		seenTool := false
		completed := 0
		for i, event := range events {
			if event.name == "response.output_item.added" {
				if index, ok := event.payload["output_index"].(float64); ok {
					addedAt[index] = i
				}
			}
			if index, ok := event.payload["output_index"].(float64); ok {
				if added, exists := addedAt[index]; !exists || added > i {
					t.Fatalf("event before output_item.added: index=%v event=%s events=%#v", index, event.name, events)
				}
			}
			switch event.name {
			case "response.output_text.delta":
				seenText = seenText || event.payload["delta"] == "answer"
			case "response.function_call_arguments.done":
				seenTool = true
			case "response.completed":
				completed++
			}
		}
		if !seenText || !seenTool || completed != 1 {
			t.Fatalf("seen_text=%t seen_tool=%t completed=%d body=%s", seenText, seenTool, completed, rr.Body.String())
		}
	})

	t.Run("mixed-text-and-tool-nonstream", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{
			Text:   "answer",
			Events: []json.RawMessage{json.RawMessage(`{"name":"lookup","arguments":{"query":"candidate"}}`)},
		}}
		s := newWP1CandidateServer(t, chat)
		cfg := s.settings.get()
		cfg.ToolPlanningMode = "native"
		s.settings.v = cfg
		rr := httptest.NewRecorder()
		body := `{"model":"gpt-5.6-reasoning","input":"mixed canary","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
		response := wp1DecodeJSON(t, rr.Body.String())
		wp1AssertRouteMetadata(t, response, "gpt-5.6-reasoning", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", true)
		output, _ := response["output"].([]any)
		seenText := false
		seenTool := false
		for _, raw := range output {
			item, _ := raw.(map[string]any)
			switch item["type"] {
			case "message":
				content, _ := item["content"].([]any)
				for _, rawBlock := range content {
					block, _ := rawBlock.(map[string]any)
					seenText = seenText || block["type"] == "output_text" && block["text"] == "answer"
				}
			case "function_call":
				seenTool = item["name"] == "lookup"
			}
		}
		if !seenText || !seenTool {
			t.Fatalf("seen_text=%t seen_tool=%t response=%#v", seenText, seenTool, response)
		}
	})

	t.Run("image-only", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{"data:image/png;base64,V1Ax"}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-reasoning","input":"image canary","stream":true}`)))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
		events := wp1SSEEvents(t, rr.Body.String())
		added := 0
		done := 0
		completed := 0
		for _, event := range events {
			switch event.name {
			case "response.output_item.added":
				added++
			case "response.output_item.done":
				done++
			case "response.completed":
				completed++
			case "response.failed":
				t.Fatalf("image-only response failed: %s", rr.Body.String())
			}
		}
		if added != 1 || done != 1 || completed != 1 || !strings.Contains(rr.Body.String(), `"type":"output_image"`) {
			t.Fatalf("added=%d done=%d completed=%d body=%s", added, done, completed, rr.Body.String())
		}
	})
}

func TestWP1CandidateImageReferenceValidation(t *testing.T) {
	const validImage = "data:image/png;base64,V1Ax"
	const invalidImage = "data:image/png;base64,%%%"

	t.Run("legacy-nonstream-invalid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{invalidImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.chatOnce(rr, httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"gpt-5.6-reasoning","message":"invalid image"}`)))
		if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "upstream_empty_response" || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
	})

	t.Run("chat-nonstream-valid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{validImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", ""))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 || !strings.Contains(rr.Body.String(), validImage) {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
	})

	t.Run("chat-nonstream-invalid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{invalidImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", ""))
		if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "upstream_empty_response" || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
	})

	t.Run("chat-stream-invalid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{invalidImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.openaiChat(rr, wp1ChatRequest("gpt-5.6-reasoning", `,"stream":true`))
		body := rr.Body.String()
		if len(chat.requests) != 1 || strings.Count(body, "data: [DONE]") != 1 || !strings.Contains(body, "upstream_empty_response") || strings.Contains(body, invalidImage) {
			t.Fatalf("upstream=%d body=%s", len(chat.requests), body)
		}
	})

	t.Run("responses-nonstream-valid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{validImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-reasoning","input":"valid image"}`)))
		if rr.Code != http.StatusOK || len(chat.requests) != 1 || !strings.Contains(rr.Body.String(), validImage) || !strings.Contains(rr.Body.String(), `"type":"output_image"`) {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
	})

	t.Run("responses-nonstream-invalid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{invalidImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-reasoning","input":"invalid image"}`)))
		if rr.Code != http.StatusBadGateway || wp1ErrorCode(t, rr) != "upstream_empty_response" || len(chat.requests) != 1 {
			t.Fatalf("status=%d upstream=%d body=%s", rr.Code, len(chat.requests), rr.Body.String())
		}
	})

	t.Run("responses-stream-invalid", func(t *testing.T) {
		chat := &wp1CandidateChat{result: chathub.Result{Images: []string{invalidImage}}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-reasoning","input":"invalid image","stream":true}`)))
		body := rr.Body.String()
		if len(chat.requests) != 1 || strings.Count(body, "event: response.failed") != 1 || strings.Contains(body, "event: response.completed") || !strings.Contains(body, "upstream_empty_response") || strings.Contains(body, invalidImage) {
			t.Fatalf("upstream=%d body=%s", len(chat.requests), body)
		}
	})
}

func TestWP1CandidateTerminalFinalityAndMetadataParity(t *testing.T) {
	chat := &wp1CandidateChat{result: chathub.Result{Text: "TERMINAL_OK"}}
	s := newWP1CandidateServer(t, chat)
	rr := httptest.NewRecorder()
	s.chatStream(rr, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"gpt-5.6-reasoning","message":"terminal canary"}`)))
	events := wp1SSEEvents(t, rr.Body.String())
	if len(events) != 1 || events[0].name != "done" || strings.Count(rr.Body.String(), "event: done") != 1 {
		t.Fatalf("legacy terminal events=%#v body=%s", events, rr.Body.String())
	}
	if events[0].payload["text"] != "TERMINAL_OK" {
		t.Fatalf("legacy terminal=%#v", events[0].payload)
	}
	wp1AssertRouteMetadata(t, events[0].payload, "gpt-5.6-reasoning", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", true)

	for _, stream := range []bool{false, true} {
		chat := &wp1CandidateChat{result: chathub.Result{Text: "RESPONSES_OK"}}
		s := newWP1CandidateServer(t, chat)
		rr := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":"gpt-5.6-reasoning","input":"metadata canary","stream":%t}`, stream)
		s.responses(rr, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		var response map[string]any
		if stream {
			if strings.Count(rr.Body.String(), "event: response.completed") != 1 || strings.Contains(rr.Body.String(), "event: response.failed") {
				t.Fatalf("responses stream terminal body=%s", rr.Body.String())
			}
			response = wp1ResponsesCompleted(t, rr.Body.String())
		} else {
			response = wp1DecodeJSON(t, rr.Body.String())
		}
		wp1AssertRouteMetadata(t, response, "gpt-5.6-reasoning", "m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", true)
	}
}

func TestWP1CandidateCatalogPreservesLegacyShapeWithoutWP1ProvisionalClaims(t *testing.T) {
	models := modelCatalog()
	byID := map[string]map[string]any{}
	for _, model := range models {
		id, _ := model["id"].(string)
		byID[id] = model
		for _, forbidden := range []string{"capability_claims", "x_m365_chat_completions_source", "authorization_source", "allowlist_matched"} {
			if _, exists := model[forbidden]; exists {
				t.Fatalf("model %s exposes provisional field %s", id, forbidden)
			}
		}
	}
	for _, required := range []string{"m365-auto", "m365-gpt-5.6-think-deeper", "m365-gpt-5.5-quick-response", "m365-copilot", "gpt-5.6-reasoning", "gpt-5.5"} {
		if byID[required] == nil {
			t.Fatalf("required catalog model missing: %s", required)
		}
	}
	for _, hidden := range []string{"claude", "gpt-5.4-quick", "gpt-5.3-think-deeper", "quick", "think-deeper"} {
		if byID[hidden] != nil {
			t.Fatalf("hidden route leaked into catalog: %s", hidden)
		}
	}
	// Existing consumer capability shape is preserved. WP1 still forbids its
	// provisional claim fields, while the accepted WP2 catalog contract may add
	// evidence-scoped provenance and account-dependence metadata.
	if caps, ok := byID["gpt-5.6-reasoning"]["capabilities"].(map[string]any); !ok || caps["chat_completions"] != true || caps["tools"] != true {
		t.Fatalf("legacy catalog capability shape changed: %#v", byID["gpt-5.6-reasoning"])
	}
}
