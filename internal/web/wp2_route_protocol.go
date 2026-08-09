package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

type WP2RouteProtocolHarnessOptions struct {
	Binding   evidence.CaptureBinding
	Routes    []string
	Protocols []string
	Runs      int
}

type wp2RouteProtocolAdapter struct {
	protocol               string
	path                   string
	authMode               string
	reasoningEffortApplied bool
	buildRequest           func(route string) *http.Request
	decodeText             func(map[string]any) string
}

type wp2RouteProtocolHarness struct {
	handler      http.Handler
	server       *Server
	apiKey       string
	adminSession string
}

func (h wp2RouteProtocolHarness) serve(adapter wp2RouteProtocolAdapter, writer http.ResponseWriter, request *http.Request) {
	h.serveWithAuth(adapter.authMode, writer, request)
}

func (h wp2RouteProtocolHarness) serveWithAuth(authMode string, writer http.ResponseWriter, request *http.Request) {
	switch authMode {
	case "api_key":
		request.Header.Set("Authorization", "Bearer "+h.apiKey)
	case "admin_session":
		request.AddCookie(&http.Cookie{Name: "m365_admin_session", Value: h.adminSession})
		// The legacy admin-session protocol is a synthetic local-console probe.
		request.Host = "127.0.0.1"
		request.RemoteAddr = "127.0.0.1:1"
		request.Header.Set("Origin", "http://127.0.0.1")
	}
	h.handler.ServeHTTP(writer, request)
}

type wp2HarnessAttempt struct {
	tone string
	oid  string
	tid  string
}

type wp2HarnessChat struct {
	result   chathub.Result
	err      error
	attempts []wp2HarnessAttempt
}

func (c *wp2HarnessChat) record(account chathub.Account, request chathub.Request) {
	c.attempts = append(c.attempts, wp2HarnessAttempt{tone: request.Tone, oid: account.OID, tid: account.TID})
}

func (c *wp2HarnessChat) boundResult() chathub.Result {
	result := c.result
	if result.ConversationID == "" {
		result.ConversationID = fmt.Sprintf("wp2-harness-conversation-%d", len(c.attempts))
	}
	if result.SessionID == "" {
		result.SessionID = fmt.Sprintf("wp2-harness-session-%d", len(c.attempts))
	}
	return result
}

func (c *wp2HarnessChat) Chat(_ context.Context, account chathub.Account, request chathub.Request) (chathub.Result, error) {
	c.record(account, request)
	return c.boundResult(), c.err
}

func (c *wp2HarnessChat) ChatWithDelta(_ context.Context, account chathub.Account, request chathub.Request, emit func(string) error) (chathub.Result, error) {
	c.record(account, request)
	if c.err == nil && emit != nil && c.result.Text != "" {
		if err := emit(c.result.Text); err != nil {
			return chathub.Result{}, err
		}
	}
	return c.boundResult(), c.err
}

func (c *wp2HarnessChat) ChatWithEvents(_ context.Context, account chathub.Account, request chathub.Request, emit chathub.StreamHandler) (chathub.Result, error) {
	c.record(account, request)
	if c.err == nil && emit != nil && c.result.Text != "" {
		if err := emit(chathub.StreamEvent{Kind: "text", Text: c.result.Text}); err != nil {
			return chathub.Result{}, err
		}
	}
	return c.boundResult(), c.err
}

var wp2PrimaryRouteOrder = []string{
	"m365-auto",
	"quick",
	"think-deeper",
	"m365-gpt-5.6-think-deeper",
	"m365-gpt-5.5-quick-response",
}

var wp2RouteProtocolAdapters = []wp2RouteProtocolAdapter{
	{
		protocol:               "openai_chat_completions_nonstream",
		path:                   "/v1/chat/completions",
		authMode:               "api_key",
		reasoningEffortApplied: true,
		buildRequest: func(route string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"reasoning_effort":"high","messages":[{"role":"user","content":"WP2 route protocol canary"}]}`, route)
			return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		},
		decodeText: func(response map[string]any) string {
			choices, _ := response["choices"].([]any)
			if len(choices) == 0 {
				return ""
			}
			choice, _ := choices[0].(map[string]any)
			message, _ := choice["message"].(map[string]any)
			text, _ := message["content"].(string)
			return text
		},
	},
	{
		protocol:               "openai_responses_nonstream",
		path:                   "/v1/responses",
		authMode:               "api_key",
		reasoningEffortApplied: true,
		buildRequest: func(route string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"input":"WP2 route protocol canary","reasoning":{"effort":"high"}}`, route)
			return httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		},
		decodeText: func(response map[string]any) string {
			output, _ := response["output"].([]any)
			for _, rawItem := range output {
				item, _ := rawItem.(map[string]any)
				if item["type"] != "message" {
					continue
				}
				content, _ := item["content"].([]any)
				for _, rawBlock := range content {
					block, _ := rawBlock.(map[string]any)
					if block["type"] == "output_text" {
						text, _ := block["text"].(string)
						if strings.TrimSpace(text) != "" {
							return text
						}
					}
				}
			}
			return ""
		},
	},
	{
		protocol: "anthropic_messages_nonstream",
		path:     "/v1/messages",
		authMode: "api_key",
		buildRequest: func(route string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"WP2 route protocol canary"}]}`, route)
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		},
		decodeText: func(response map[string]any) string {
			content, _ := response["content"].([]any)
			for _, rawBlock := range content {
				block, _ := rawBlock.(map[string]any)
				if block["type"] == "text" {
					text, _ := block["text"].(string)
					if strings.TrimSpace(text) != "" {
						return text
					}
				}
			}
			return ""
		},
	},
	{
		protocol:               "legacy_chat_nonstream",
		path:                   "/api/chat",
		authMode:               "admin_session",
		reasoningEffortApplied: true,
		buildRequest: func(route string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"message":"WP2 route protocol canary","reasoning_effort":"high"}`, route)
			return httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		},
		decodeText: func(response map[string]any) string {
			text, _ := response["text"].(string)
			return text
		},
	},
}

func BuildWP2RouteProtocolEvidenceSet(options WP2RouteProtocolHarnessOptions) (evidence.RouteProtocolEvidenceSetV1, error) {
	routes, err := selectedWP2Routes(options.Routes)
	if err != nil {
		return evidence.RouteProtocolEvidenceSetV1{}, err
	}
	adapters, err := selectedWP2RouteProtocolAdapters(options.Protocols)
	if err != nil {
		return evidence.RouteProtocolEvidenceSetV1{}, err
	}
	runs := options.Runs
	if runs == 0 {
		runs = 3
	}
	if runs < 1 || runs > 3 {
		return evidence.RouteProtocolEvidenceSetV1{}, errors.New("WP2 route protocol runs must be between 1 and 3")
	}

	set := evidence.RouteProtocolEvidenceSetV1{
		Schema:        evidence.RouteProtocolEvidenceSetSchemaV1,
		Matrix:        make([]evidence.RouteProtocolMatrixEntryV1, 0, len(routes)*len(adapters)),
		RouteFailures: make([]evidence.RouteProtocolFailureEntryV1, 0, len(adapters)),
		Records:       make([]evidence.RouteProtocolRecordV1, 0, len(routes)*len(adapters)*(runs+1)+len(adapters)),
	}
	for _, routeID := range routes {
		route, _ := builtInRoute(routeID)
		descriptorBase := evidence.RouteProtocolDescriptor{
			CanonicalRoute:  route.CanonicalRoute,
			ResolvedTone:    route.Tone,
			MappingEvidence: "web_payload_verified",
			IdentityStatus:  string(route.IdentityStatus),
		}
		for _, adapter := range adapters {
			descriptor := descriptorBase
			descriptor.Protocol = adapter.protocol
			descriptor.EndpointPath = adapter.path
			descriptor.AuthMode = adapter.authMode
			entry := evidence.RouteProtocolMatrixEntryV1{
				CanonicalRoute: route.CanonicalRoute,
				ResolvedTone:   route.Tone,
				Protocol:       adapter.protocol,
				EndpointPath:   adapter.path,
				Classification: evidence.ProtocolExposedAndSupported,
			}
			for run := 1; run <= runs; run++ {
				record, err := runWP2RouteProtocolObservation(route, adapter, run, false, descriptor, options.Binding)
				if err != nil {
					return evidence.RouteProtocolEvidenceSetV1{}, fmt.Errorf("%s %s success run %d: %w", route.ID, adapter.protocol, run, err)
				}
				entry.SuccessObservationSHA256 = append(entry.SuccessObservationSHA256, record.ObservationSHA256)
				set.Records = append(set.Records, record)
			}
			empty, err := runWP2RouteProtocolObservation(route, adapter, 1, true, descriptor, options.Binding)
			if err != nil {
				return evidence.RouteProtocolEvidenceSetV1{}, fmt.Errorf("%s %s upstream empty: %w", route.ID, adapter.protocol, err)
			}
			entry.EmptyObservationSHA256 = empty.ObservationSHA256
			set.Records = append(set.Records, empty)
			set.Matrix = append(set.Matrix, entry)
		}
	}
	for _, adapter := range adapters {
		for _, failure := range []struct {
			model  string
			caseID evidence.RouteProtocolCase
			code   string
		}{
			{model: "wp2-unknown-route", caseID: evidence.RouteProtocolCaseUnknownRoute, code: "model_not_found"},
		} {
			record, err := runWP2RouteProtocolRouteFailure(failure.model, failure.caseID, adapter, evidence.RouteProtocolDescriptor{
				Protocol:     adapter.protocol,
				EndpointPath: adapter.path,
				AuthMode:     adapter.authMode,
			}, options.Binding)
			if err != nil {
				return evidence.RouteProtocolEvidenceSetV1{}, fmt.Errorf("%s %s: %w", failure.caseID, adapter.protocol, err)
			}
			set.RouteFailures = append(set.RouteFailures, evidence.RouteProtocolFailureEntryV1{
				RequestedModel:      failure.model,
				CaseID:              failure.caseID,
				Protocol:            adapter.protocol,
				ObservationSHA256:   record.ObservationSHA256,
				ExpectedFailureCode: failure.code,
			})
			set.Records = append(set.Records, record)
		}
	}
	return set, nil
}

func selectedWP2Routes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), wp2PrimaryRouteOrder...), nil
	}
	allowed := make(map[string]bool, len(wp2PrimaryRouteOrder))
	for _, route := range wp2PrimaryRouteOrder {
		allowed[route] = true
	}
	selected := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, route := range requested {
		route = strings.TrimSpace(route)
		if !allowed[route] {
			return nil, fmt.Errorf("route %q is not a primary WP2 canonical route", route)
		}
		if !seen[route] {
			selected = append(selected, route)
			seen[route] = true
		}
	}
	return selected, nil
}

func selectedWP2RouteProtocolAdapters(requested []string) ([]wp2RouteProtocolAdapter, error) {
	if len(requested) == 0 {
		return append([]wp2RouteProtocolAdapter(nil), wp2RouteProtocolAdapters...), nil
	}
	byID := make(map[string]wp2RouteProtocolAdapter, len(wp2RouteProtocolAdapters))
	for _, adapter := range wp2RouteProtocolAdapters {
		byID[adapter.protocol] = adapter
	}
	selected := make([]wp2RouteProtocolAdapter, 0, len(requested))
	seen := map[string]bool{}
	for _, protocol := range requested {
		protocol = strings.TrimSpace(protocol)
		adapter, ok := byID[protocol]
		if !ok {
			return nil, fmt.Errorf("protocol %q is not an exposed WP2 non-stream protocol", protocol)
		}
		if !seen[protocol] {
			selected = append(selected, adapter)
			seen[protocol] = true
		}
	}
	return selected, nil
}

func runWP2RouteProtocolObservation(route routeDefinition, adapter wp2RouteProtocolAdapter, run int, upstreamEmpty bool, descriptor evidence.RouteProtocolDescriptor, binding evidence.CaptureBinding) (evidence.RouteProtocolRecordV1, error) {
	text := fmt.Sprintf("WP2_BASIC_TEXT_DELIVERY_%d", run)
	result := chathub.Result{Text: text}
	caseID := evidence.RouteProtocolCaseSuccess
	if upstreamEmpty {
		result = chathub.Result{}
		caseID = evidence.RouteProtocolCaseUpstreamEmpty
	}
	chat := &wp2HarnessChat{result: result}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	defer cleanup()

	writer := httptest.NewRecorder()
	harness.serve(adapter, writer, adapter.buildRequest(route.ID))
	response := map[string]any{}
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	metadata, _ := response["m365"].(map[string]any)
	failureCode := routeProtocolFailureCode(response)
	topLevelModel, _ := response["model"].(string)
	basicText := strings.TrimSpace(adapter.decodeText(response)) != ""
	routeSwitches, crossAccountResends := wp2AttemptDivergence(chat.attempts, route.Tone)

	canonicalRoute := route.CanonicalRoute
	resolvedTone := route.Tone
	reasoningIgnored := false
	if !upstreamEmpty {
		if value, ok := metadata["canonical_route"].(string); ok {
			canonicalRoute = value
		}
		if value, ok := metadata["resolved_tone"].(string); ok {
			resolvedTone = value
		}
		reasoningIgnored, _ = metadata["reasoning_effort_ignored"].(bool)
	}
	capture := evidence.RouteProtocolCaptureV1{
		Schema:                  evidence.RouteProtocolCaptureSchemaV1,
		CaseID:                  caseID,
		Run:                     run,
		RequestedModel:          route.ID,
		TopLevelModel:           topLevelModel,
		CanonicalRoute:          canonicalRoute,
		ResolvedTone:            resolvedTone,
		Protocol:                adapter.protocol,
		EndpointPath:            adapter.path,
		AuthMode:                adapter.authMode,
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: writer.Header().Get("X-Content-Type-Options") == "nosniff" && writer.Header().Get("X-Frame-Options") == "DENY",
		HTTPStatus:              writer.Code,
		BasicTextDelivered:      basicText,
		ReasoningEffortApplied:  adapter.reasoningEffortApplied,
		ReasoningEffortIgnored:  reasoningIgnored,
		UpstreamAttempts:        len(chat.attempts),
		RouteSwitches:           routeSwitches,
		CrossAccountResends:     crossAccountResends,
		MeaningfulUpstreamEvent: "text",
		FailureCode:             failureCode,
	}
	if upstreamEmpty {
		capture.TopLevelModel = ""
		capture.BasicTextDelivered = false
		capture.ReasoningEffortIgnored = false
		capture.MeaningfulUpstreamEvent = "none"
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	return evidence.CaptureRouteProtocol(raw, descriptor, binding)
}

func runWP2RouteProtocolRouteFailure(model string, caseID evidence.RouteProtocolCase, adapter wp2RouteProtocolAdapter, descriptor evidence.RouteProtocolDescriptor, binding evidence.CaptureBinding) (evidence.RouteProtocolRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	defer cleanup()

	writer := httptest.NewRecorder()
	harness.serve(adapter, writer, adapter.buildRequest(model))
	response := map[string]any{}
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	capture := evidence.RouteProtocolCaptureV1{
		Schema:                  evidence.RouteProtocolCaptureSchemaV1,
		CaseID:                  caseID,
		Run:                     1,
		RequestedModel:          model,
		Protocol:                adapter.protocol,
		EndpointPath:            adapter.path,
		AuthMode:                adapter.authMode,
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: writer.Header().Get("X-Content-Type-Options") == "nosniff" && writer.Header().Get("X-Frame-Options") == "DENY",
		HTTPStatus:              writer.Code,
		ReasoningEffortApplied:  adapter.reasoningEffortApplied,
		UpstreamAttempts:        len(chat.attempts),
		MeaningfulUpstreamEvent: "none",
		FailureCode:             routeProtocolFailureCode(response),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.RouteProtocolRecordV1{}, err
	}
	return evidence.CaptureRouteProtocol(raw, descriptor, binding)
}

func newWP2RouteProtocolHarnessServer(chat chatService) (wp2RouteProtocolHarness, func(), error) {
	return newWP2RouteProtocolHarnessServerWithSettings(chat, defaultRuntimeSettings())
}

func newWP2RouteProtocolHarnessServerWithSettings(chat chatService, settings runtimeSettings) (wp2RouteProtocolHarness, func(), error) {
	dir, err := os.MkdirTemp("", "m365-wp2-route-protocol-")
	if err != nil {
		return wp2RouteProtocolHarness{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	store, err := auth.OpenStore(filepath.Join(dir, "accounts.json"))
	if err != nil {
		cleanup()
		return wp2RouteProtocolHarness{}, nil, err
	}
	if _, err := store.Upsert(auth.TokenSet{
		AccessToken: "wp2-synthetic-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		Email:       "wp2-matrix@example.test",
		HomeOID:     "wp2-synthetic-oid",
		TenantID:    "wp2-synthetic-tid",
	}); err != nil {
		cleanup()
		return wp2RouteProtocolHarness{}, nil, err
	}
	const apiKey = "m365_wp2_synthetic_api_key"
	const adminSessionToken = "wp2-synthetic-admin-session"
	adminSessionNow := time.Now()
	checkpoints, err := openTransportCheckpointStore(filepath.Join(dir, "checkpoints", "transport.json"))
	if err != nil {
		cleanup()
		return wp2RouteProtocolHarness{}, nil, err
	}
	server := &Server{
		tokens:              store,
		pkce:                map[string]pendingPKCE{},
		chat:                chat,
		checkpoints:         checkpoints,
		adminPassword:       "wp2-synthetic-admin-password",
		adminCredentialMode: adminCredentialPersisted,
		adminSessions:       map[string]adminSession{adminSessionToken: {CreatedAt: adminSessionNow, LastSeenAt: adminSessionNow, ExpiresAt: adminSessionNow.Add(time.Hour)}},
		loginAttempts:       map[string]loginAttempt{},
		apiKeys: &apiKeyStore{
			Path: filepath.Join(dir, "api-keys.json"),
			Keys: []apiKeyRecord{{
				ID:        "wp2",
				Name:      "WP2 synthetic",
				Prefix:    "m365_wp2",
				Hash:      keyHash(apiKey),
				CreatedAt: time.Now(),
			}},
		},
		debug:    &debugStore{path: filepath.Join(dir, "debug.jsonl")},
		settings: &settingsStore{v: settings},
	}
	return wp2RouteProtocolHarness{
		handler:      server.Routes(),
		server:       server,
		apiKey:       apiKey,
		adminSession: adminSessionToken,
	}, cleanup, nil
}

func routeProtocolFailureCode(response map[string]any) string {
	errorBody, _ := response["error"].(map[string]any)
	code, _ := errorBody["code"].(string)
	return code
}

func wp2AttemptDivergence(attempts []wp2HarnessAttempt, expectedTone string) (routeSwitches, crossAccountResends int) {
	if len(attempts) == 0 {
		return 0, 0
	}
	firstOID, firstTID := attempts[0].oid, attempts[0].tid
	for index, attempt := range attempts {
		if attempt.tone != expectedTone {
			routeSwitches++
		}
		if index > 0 && (attempt.oid != firstOID || attempt.tid != firstTID) {
			crossAccountResends++
		}
	}
	return routeSwitches, crossAccountResends
}
