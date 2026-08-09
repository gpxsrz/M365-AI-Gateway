package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

type WP2AliasProjectionHarnessOptions struct {
	Binding    evidence.CaptureBinding
	Identities []string
	Protocols  []string
	Efforts    []string
}

type wp2AliasProjectionProtocolAdapter struct {
	protocol       string
	path           string
	authMode       string
	supportsEffort bool
	buildRequest   func(identity, effort string) *http.Request
	decodeResponse func([]byte) (map[string]any, string, error)
}

var wp2AliasProjectionOrder = []string{
	"m365-copilot",
	"gpt-5.6-reasoning",
	"gpt-5.5",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"claude",
	"gpt-5.4-quick",
	"gpt-5.3-think-deeper",
}

var wp2AliasEffortOrder = []string{"", "none", "minimal", "low", "medium", "high", "xhigh"}

var wp2AliasProjectionAdapters = []wp2AliasProjectionProtocolAdapter{
	{
		protocol:       "openai_chat_completions_nonstream",
		path:           "/v1/chat/completions",
		authMode:       "api_key",
		supportsEffort: true,
		buildRequest: func(identity, effort string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"WP2 alias projection canary"}]`, identity)
			if effort != "" {
				body += fmt.Sprintf(`,"reasoning_effort":%q`, effort)
			}
			body += `}`
			return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		},
		decodeResponse: decodeWP2AliasChatCompletion,
	},
	{
		protocol:       "openai_responses_nonstream",
		path:           "/v1/responses",
		authMode:       "api_key",
		supportsEffort: true,
		buildRequest: func(identity, effort string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"input":"WP2 alias projection canary"`, identity)
			if effort != "" {
				body += fmt.Sprintf(`,"reasoning":{"effort":%q}`, effort)
			}
			body += `}`
			return httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		},
		decodeResponse: decodeWP2AliasResponses,
	},
	{
		protocol: "anthropic_messages_nonstream",
		path:     "/v1/messages",
		authMode: "api_key",
		buildRequest: func(identity, _ string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":"WP2 alias projection canary"}]}`, identity)
			return httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		},
		decodeResponse: decodeWP2AliasAnthropic,
	},
	{
		protocol:       "legacy_chat_nonstream",
		path:           "/api/chat",
		authMode:       "admin_session",
		supportsEffort: true,
		buildRequest: func(identity, effort string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"message":"WP2 alias projection canary"`, identity)
			if effort != "" {
				body += fmt.Sprintf(`,"reasoning_effort":%q`, effort)
			}
			body += `}`
			return httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
		},
		decodeResponse: decodeWP2AliasLegacyNonStream,
	},
	{
		protocol:       "legacy_chat_stream",
		path:           "/api/chat/stream",
		authMode:       "admin_session",
		supportsEffort: true,
		buildRequest: func(identity, effort string) *http.Request {
			body := fmt.Sprintf(`{"model":%q,"message":"WP2 alias projection canary"`, identity)
			if effort != "" {
				body += fmt.Sprintf(`,"reasoning_effort":%q`, effort)
			}
			body += `}`
			return httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(body))
		},
		decodeResponse: decodeWP2AliasLegacyStream,
	},
}

func BuildWP2AliasProjectionEvidenceSet(options WP2AliasProjectionHarnessOptions) (evidence.AliasProjectionEvidenceSetV1, error) {
	identities, err := selectedWP2AliasProjectionIdentities(options.Identities)
	if err != nil {
		return evidence.AliasProjectionEvidenceSetV1{}, err
	}
	adapters, err := selectedWP2AliasProjectionAdapters(options.Protocols)
	if err != nil {
		return evidence.AliasProjectionEvidenceSetV1{}, err
	}
	efforts, err := selectedWP2AliasProjectionEfforts(options.Efforts)
	if err != nil {
		return evidence.AliasProjectionEvidenceSetV1{}, err
	}
	set := evidence.AliasProjectionEvidenceSetV1{
		Schema:   evidence.AliasProjectionEvidenceSetSchemaV1,
		Catalog:  make([]evidence.AliasProjectionCatalogEntryV1, 0, len(identities)),
		Matrix:   make([]evidence.AliasProjectionMatrixEntryV1, 0, len(identities)*len(adapters)),
		Failures: make([]evidence.AliasProjectionFailureEntryV1, 0, len(adapters)),
		Records:  []evidence.AliasProjectionRecordV1{},
	}
	for _, identity := range identities {
		route, ok := builtInRoute(identity)
		if !ok {
			return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("WP2 alias projection identity %q is not registered", identity)
		}
		if route.Kind != routeKindAlias && route.Kind != routeKindPreset {
			return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("WP2 identity %q is not an alias or preset", identity)
		}
		if route.OperationalStatus != operationalEnabled || !route.CompatibilityRequired {
			return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("WP2 compatibility identity %q is not enabled and compatibility-required", identity)
		}
		catalogRecord, err := runWP2AliasCatalogObservation(route, options.Binding)
		if err != nil {
			return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("catalog %s: %w", identity, err)
		}
		set.Catalog = append(set.Catalog, evidence.AliasProjectionCatalogEntryV1{
			RequestedIdentity:     identity,
			CanonicalRoute:        route.CanonicalRoute,
			RouteKind:             string(route.Kind),
			CatalogVisibility:     string(route.CatalogVisibility),
			CompatibilityRequired: route.CompatibilityRequired,
			DefaultReasoningLevel: route.DefaultReasoningLevel,
			Listed:                route.CatalogVisibility != catalogHidden,
			ObservationSHA256:     catalogRecord.ObservationSHA256,
		})
		set.Records = append(set.Records, catalogRecord)
		for _, adapter := range adapters {
			entry := evidence.AliasProjectionMatrixEntryV1{
				RequestedIdentity:     identity,
				CanonicalRoute:        route.CanonicalRoute,
				RouteKind:             string(route.Kind),
				CatalogVisibility:     string(route.CatalogVisibility),
				CompatibilityRequired: route.CompatibilityRequired,
				Protocol:              adapter.protocol,
				EndpointPath:          adapter.path,
				EffortObservations:    []evidence.AliasProjectionEffortObservationRefV1{},
			}
			for _, effort := range effortsForWP2AliasAdapter(adapter, efforts) {
				resolution, err := resolveRoute(identity, effort, nil)
				if err != nil {
					return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("resolve %s/%s: %w", identity, wp2AliasEffortLabel(effort), err)
				}
				descriptor := wp2AliasProjectionDescriptor(route, resolution, adapter.protocol, adapter.path, adapter.authMode, wp2AliasEffortLabel(effort), adapter.supportsEffort && effort != "")
				record, err := runWP2AliasSuccessObservation(route, resolution, adapter, effort, descriptor, options.Binding)
				if err != nil {
					return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("%s %s %s: %w", identity, adapter.protocol, wp2AliasEffortLabel(effort), err)
				}
				entry.EffortObservations = append(entry.EffortObservations, evidence.AliasProjectionEffortObservationRefV1{
					Effort:            wp2AliasEffortLabel(effort),
					ResolvedTone:      resolution.ResolvedTone,
					ObservationSHA256: record.ObservationSHA256,
				})
				set.Records = append(set.Records, record)
			}
			set.Matrix = append(set.Matrix, entry)
		}
	}
	for _, adapter := range adapters {
		for _, failure := range []struct {
			model  string
			caseID evidence.AliasProjectionCase
			code   string
		}{
			{model: "wp2-unknown-alias", caseID: evidence.AliasProjectionCaseUnknownRoute, code: "model_not_found"},
		} {
			descriptor := evidence.AliasProjectionDescriptor{
				Protocol:     adapter.protocol,
				EndpointPath: adapter.path,
				AuthMode:     adapter.authMode,
				Effort:       "omitted",
			}
			record, err := runWP2AliasFailureObservation(failure.model, failure.caseID, adapter, descriptor, options.Binding)
			if err != nil {
				return evidence.AliasProjectionEvidenceSetV1{}, fmt.Errorf("%s %s: %w", failure.model, adapter.protocol, err)
			}
			set.Failures = append(set.Failures, evidence.AliasProjectionFailureEntryV1{
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

func selectedWP2AliasProjectionIdentities(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), wp2AliasProjectionOrder...), nil
	}
	allowed := make(map[string]bool, len(wp2AliasProjectionOrder))
	for _, identity := range wp2AliasProjectionOrder {
		allowed[identity] = true
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, identity := range requested {
		identity = strings.ToLower(strings.TrimSpace(identity))
		if !allowed[identity] {
			return nil, fmt.Errorf("identity %q is not in the WP2 alias projection scope", identity)
		}
		if !seen[identity] {
			selected = append(selected, identity)
			seen[identity] = true
		}
	}
	return selected, nil
}

func selectedWP2AliasProjectionAdapters(requested []string) ([]wp2AliasProjectionProtocolAdapter, error) {
	if len(requested) == 0 {
		return append([]wp2AliasProjectionProtocolAdapter(nil), wp2AliasProjectionAdapters...), nil
	}
	byID := make(map[string]wp2AliasProjectionProtocolAdapter, len(wp2AliasProjectionAdapters))
	for _, adapter := range wp2AliasProjectionAdapters {
		byID[adapter.protocol] = adapter
	}
	selected := []wp2AliasProjectionProtocolAdapter{}
	seen := map[string]bool{}
	for _, protocol := range requested {
		protocol = strings.TrimSpace(protocol)
		adapter, ok := byID[protocol]
		if !ok {
			return nil, fmt.Errorf("protocol %q is not in the WP2 alias projection scope", protocol)
		}
		if !seen[protocol] {
			selected = append(selected, adapter)
			seen[protocol] = true
		}
	}
	return selected, nil
}

func selectedWP2AliasProjectionEfforts(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), wp2AliasEffortOrder...), nil
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, effort := range requested {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort != "" {
			if _, err := normalizeReasoningEffort(effort); err != nil {
				return nil, err
			}
		}
		if !seen[effort] {
			selected = append(selected, effort)
			seen[effort] = true
		}
	}
	return selected, nil
}

func effortsForWP2AliasAdapter(adapter wp2AliasProjectionProtocolAdapter, selected []string) []string {
	if !adapter.supportsEffort {
		return []string{""}
	}
	return selected
}

func wp2AliasEffortLabel(effort string) string {
	if effort == "" {
		return "omitted"
	}
	return effort
}

func wp2AliasProjectionDescriptor(route routeDefinition, resolution routeResolution, protocol, path, authMode, effort string, effortApplied bool) evidence.AliasProjectionDescriptor {
	return evidence.AliasProjectionDescriptor{
		RequestedIdentity:       route.ID,
		CanonicalRoute:          resolution.CanonicalRoute,
		ResolvedTone:            resolution.ResolvedTone,
		RouteKind:               string(resolution.RouteKind),
		OperationalStatus:       string(resolution.OperationalStatus),
		RuntimeMappingEvidence:  string(resolution.MappingEvidence),
		AcceptedMappingEvidence: acceptedAliasProjectionMappingEvidence(route, resolution),
		IdentityStatus:          string(resolution.IdentityStatus),
		CatalogVisibility:       string(resolution.CatalogVisibility),
		CompatibilityRequired:   resolution.CompatibilityRequired,
		DefaultReasoningLevel:   route.DefaultReasoningLevel,
		Protocol:                protocol,
		EndpointPath:            path,
		AuthMode:                authMode,
		Effort:                  effort,
		ReasoningEffortApplied:  effortApplied,
		ReasoningEffortIgnored:  resolution.ReasoningEffortIgnored,
		ListedInCatalog:         route.CatalogVisibility != catalogHidden,
	}
}

func acceptedAliasProjectionMappingEvidence(route routeDefinition, resolution routeResolution) string {
	canonical, ok := builtInRoute(route.CanonicalRoute)
	if ok && (canonical.ID == "m365-auto" || canonical.ID == "m365-gpt-5.6-think-deeper" || canonical.ID == "m365-gpt-5.5-quick-response") && canonical.Tone == resolution.ResolvedTone {
		return "web_payload_verified"
	}
	return string(route.MappingEvidence)
}

func runWP2AliasCatalogObservation(route routeDefinition, binding evidence.CaptureBinding) (evidence.AliasProjectionRecordV1, error) {
	resolution, err := resolveRoute(route.ID, "", nil)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	descriptor := wp2AliasProjectionDescriptor(route, resolution, "openai_models_catalog", "/v1/models", "api_key", "not_applicable", false)
	chat := &wp2HarnessChat{}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth("api_key", writer, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var response map[string]any
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	models, _ := response["data"].([]any)
	var listedModel map[string]any
	for _, raw := range models {
		model, _ := raw.(map[string]any)
		if model["id"] == route.ID {
			listedModel = model
			break
		}
	}
	listed := listedModel != nil
	capture := evidence.AliasProjectionCaptureV1{
		Schema:                  evidence.AliasProjectionCaptureSchemaV1,
		CaseID:                  evidence.AliasProjectionCaseCatalog,
		RequestedIdentity:       route.ID,
		CanonicalRoute:          route.CanonicalRoute,
		ResolvedTone:            route.Tone,
		RouteKind:               string(route.Kind),
		OperationalStatus:       string(route.OperationalStatus),
		MappingEvidence:         string(route.MappingEvidence),
		IdentityStatus:          string(route.IdentityStatus),
		CatalogVisibility:       string(route.CatalogVisibility),
		AliasUsed:               true,
		CompatibilityRequired:   route.CompatibilityRequired,
		DefaultReasoningLevel:   route.DefaultReasoningLevel,
		Deprecated:              route.Deprecated,
		RemovalDateAbsent:       true,
		ListedInCatalog:         listed,
		Protocol:                "openai_models_catalog",
		EndpointPath:            "/v1/models",
		AuthMode:                "api_key",
		Effort:                  "not_applicable",
		HTTPStatus:              writer.Code,
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: wp2SecurityHeadersObserved(writer),
	}
	if listed {
		capture.CanonicalRoute, _ = listedModel["canonical_route"].(string)
		capture.ResolvedTone, _ = listedModel["resolved_tone"].(string)
		capture.RouteKind, _ = listedModel["route_kind"].(string)
		capture.OperationalStatus, _ = listedModel["operational_status"].(string)
		capture.MappingEvidence, _ = listedModel["mapping_evidence"].(string)
		capture.IdentityStatus, _ = listedModel["identity_status"].(string)
		capture.CatalogVisibility, _ = listedModel["catalog_visibility"].(string)
		capture.AliasUsed, _ = listedModel["alias_used"].(bool)
		capture.CompatibilityRequired, _ = listedModel["compatibility_required"].(bool)
		capture.DefaultReasoningLevel, _ = listedModel["default_reasoning_level"].(string)
		capture.Deprecated, _ = listedModel["deprecated"].(bool)
		capture.RemovalDateAbsent = wp2RemovalDateAbsent(listedModel)
		capture.PerKeyRestricted = wp2PerKeyRestricted(listedModel)
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	return evidence.CaptureAliasProjection(raw, descriptor, binding)
}

func runWP2AliasSuccessObservation(route routeDefinition, resolution routeResolution, adapter wp2AliasProjectionProtocolAdapter, effort string, descriptor evidence.AliasProjectionDescriptor, binding evidence.CaptureBinding) (evidence.AliasProjectionRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "WP2_ALIAS_BASIC_TEXT"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth(adapter.authMode, writer, adapter.buildRequest(route.ID, effort))
	response, text, decodeErr := adapter.decodeResponse(writer.Body.Bytes())
	if decodeErr != nil {
		return evidence.AliasProjectionRecordV1{}, decodeErr
	}
	metadata, _ := response["m365"].(map[string]any)
	routeSwitches, crossAccountResends := wp2AttemptDivergence(chat.attempts, resolution.ResolvedTone)
	capture := evidence.AliasProjectionCaptureV1{
		Schema:                  evidence.AliasProjectionCaptureSchemaV1,
		CaseID:                  evidence.AliasProjectionCaseSuccess,
		RequestedIdentity:       route.ID,
		TopLevelModel:           wp2AliasString(response["model"]),
		MetadataRequestedModel:  wp2AliasString(metadata["requested_model"]),
		MetadataResponseModel:   wp2AliasString(metadata["response_model"]),
		RouteMetadataComplete:   wp2AliasRouteMetadataComplete(metadata),
		FallbackUsed:            wp2AliasBool(metadata["fallback_used"]),
		ConfiguredMapping:       wp2AliasBool(metadata["configured_mapping"]),
		CanonicalRoute:          wp2AliasString(metadata["canonical_route"]),
		ResolvedTone:            wp2AliasString(metadata["resolved_tone"]),
		RouteKind:               wp2AliasString(metadata["route_kind"]),
		OperationalStatus:       wp2AliasString(metadata["operational_status"]),
		MappingEvidence:         wp2AliasString(metadata["mapping_evidence"]),
		IdentityStatus:          wp2AliasString(metadata["identity_status"]),
		CatalogVisibility:       wp2AliasString(metadata["catalog_visibility"]),
		AliasUsed:               wp2AliasBool(metadata["alias_used"]),
		CompatibilityRequired:   wp2AliasBool(metadata["compatibility_required"]),
		DefaultReasoningLevel:   route.DefaultReasoningLevel,
		Deprecated:              route.Deprecated,
		RemovalDateAbsent:       true,
		PerKeyRestricted:        wp2PerKeyRestricted(metadata),
		Protocol:                adapter.protocol,
		EndpointPath:            adapter.path,
		AuthMode:                adapter.authMode,
		Effort:                  wp2AliasEffortLabel(effort),
		ReasoningEffortApplied:  adapter.supportsEffort && effort != "",
		ReasoningEffortIgnored:  wp2AliasBool(metadata["reasoning_effort_ignored"]),
		HTTPStatus:              writer.Code,
		BasicTextDelivered:      strings.TrimSpace(text) != "",
		UpstreamAttempts:        len(chat.attempts),
		RouteSwitches:           routeSwitches,
		CrossAccountResends:     crossAccountResends,
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: wp2SecurityHeadersObserved(writer),
		FailureCode:             routeProtocolFailureCode(response),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	return evidence.CaptureAliasProjection(raw, descriptor, binding)
}

func runWP2AliasFailureObservation(model string, caseID evidence.AliasProjectionCase, adapter wp2AliasProjectionProtocolAdapter, descriptor evidence.AliasProjectionDescriptor, binding evidence.CaptureBinding) (evidence.AliasProjectionRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(chat)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth(adapter.authMode, writer, adapter.buildRequest(model, ""))
	response := map[string]any{}
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	capture := evidence.AliasProjectionCaptureV1{
		Schema:                  evidence.AliasProjectionCaptureSchemaV1,
		CaseID:                  caseID,
		RequestedIdentity:       model,
		Protocol:                adapter.protocol,
		EndpointPath:            adapter.path,
		AuthMode:                adapter.authMode,
		Effort:                  "omitted",
		HTTPStatus:              writer.Code,
		UpstreamAttempts:        len(chat.attempts),
		RequestIDObserved:       writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: wp2SecurityHeadersObserved(writer),
		FailureCode:             routeProtocolFailureCode(response),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.AliasProjectionRecordV1{}, err
	}
	return evidence.CaptureAliasProjection(raw, descriptor, binding)
}

func decodeWP2AliasChatCompletion(body []byte) (map[string]any, string, error) {
	response, err := decodeWP2AliasJSON(body)
	if err != nil {
		return nil, "", err
	}
	choices, _ := response["choices"].([]any)
	if len(choices) == 0 {
		return response, "", nil
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	return response, wp2AliasString(message["content"]), nil
}

func decodeWP2AliasResponses(body []byte) (map[string]any, string, error) {
	response, err := decodeWP2AliasJSON(body)
	if err != nil {
		return nil, "", err
	}
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
				if text := wp2AliasString(block["text"]); strings.TrimSpace(text) != "" {
					return response, text, nil
				}
			}
		}
	}
	return response, "", nil
}

func decodeWP2AliasAnthropic(body []byte) (map[string]any, string, error) {
	response, err := decodeWP2AliasJSON(body)
	if err != nil {
		return nil, "", err
	}
	content, _ := response["content"].([]any)
	for _, rawBlock := range content {
		block, _ := rawBlock.(map[string]any)
		if block["type"] == "text" {
			if text := wp2AliasString(block["text"]); strings.TrimSpace(text) != "" {
				return response, text, nil
			}
		}
	}
	return response, "", nil
}

func decodeWP2AliasLegacyNonStream(body []byte) (map[string]any, string, error) {
	response, err := decodeWP2AliasJSON(body)
	if err != nil {
		return nil, "", err
	}
	return response, wp2AliasString(response["text"]), nil
}

func decodeWP2AliasLegacyStream(body []byte) (map[string]any, string, error) {
	for _, block := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n") {
		eventName := ""
		data := ""
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
		}
		if eventName != "done" || data == "" {
			continue
		}
		response := map[string]any{}
		if err := json.Unmarshal([]byte(data), &response); err != nil {
			return nil, "", err
		}
		return response, wp2AliasString(response["text"]), nil
	}
	return nil, "", errors.New("legacy stream done event missing")
}

func decodeWP2AliasJSON(body []byte) (map[string]any, error) {
	response := map[string]any{}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func wp2SecurityHeadersObserved(writer *httptest.ResponseRecorder) bool {
	return writer.Header().Get("X-Content-Type-Options") == "nosniff" && writer.Header().Get("X-Frame-Options") == "DENY"
}

func wp2AliasRouteMetadataComplete(metadata map[string]any) bool {
	for _, key := range []string{
		"requested_model", "response_model", "canonical_route", "resolved_tone", "route_kind",
		"operational_status", "mapping_evidence", "identity_status", "catalog_visibility",
		"alias_used", "compatibility_required", "fallback_used", "reasoning_effort_ignored", "configured_mapping",
	} {
		if _, ok := metadata[key]; !ok {
			return false
		}
	}
	return true
}

func wp2RemovalDateAbsent(values map[string]any) bool {
	for _, key := range []string{"removal_date", "removalDate", "sunset_at", "sunsetAt", "deprecation_date", "deprecationDate"} {
		if _, exists := values[key]; exists {
			return false
		}
	}
	return true
}

func wp2PerKeyRestricted(values map[string]any) bool {
	if wp2AliasString(values["catalog_visibility"]) == "per_key" {
		return true
	}
	for _, key := range []string{"authorization_source", "allowlist_matched", "request_access_policy", "per_key_restricted"} {
		if _, exists := values[key]; exists {
			return true
		}
	}
	return false
}

func wp2AliasString(value any) string {
	text, _ := value.(string)
	return text
}

func wp2AliasBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}
