package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

type WP2ConfiguredMapping struct {
	PublicModel           string `json:"public_model"`
	UpstreamTone          string `json:"upstream_tone"`
	DisplayName           string `json:"display_name"`
	DefaultReasoningLevel string `json:"default_reasoning_level"`
}

type WP2LegacyConfiguredHarnessOptions struct {
	Binding            evidence.CaptureBinding
	LegacyRoutes       []string
	ConfiguredMappings []WP2ConfiguredMapping
	Protocols          []string
	Efforts            []string
}

var wp2LegacyDirectOrder = []string{
	"gpt-5.2",
	"gpt-5.2-reasoning",
	"gpt-5.3",
	"gpt-5.4",
	"gpt-5.4-reasoning",
	"gpt-5.5-reasoning",
	"claude-sonnet",
	"claude-sonnet-reasoning",
}

var wp2ConfiguredMappingFixtures = []WP2ConfiguredMapping{
	{PublicModel: "existing-microsoft-route", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Existing Microsoft Route", DefaultReasoningLevel: "medium"},
	{PublicModel: "existing-claude-route", UpstreamTone: "Claude_Sonnet_Reasoning", DisplayName: "Existing Claude Route", DefaultReasoningLevel: "medium"},
}

func BuildWP2LegacyConfiguredEvidenceSet(options WP2LegacyConfiguredHarnessOptions) (evidence.LegacyConfiguredEvidenceSetV1, error) {
	legacyIDs, err := selectedWP2LegacyRoutes(options.LegacyRoutes)
	if err != nil {
		return evidence.LegacyConfiguredEvidenceSetV1{}, err
	}
	configured := options.ConfiguredMappings
	if configured == nil {
		configured = append([]WP2ConfiguredMapping(nil), wp2ConfiguredMappingFixtures...)
	}
	if err := validateWP2ConfiguredMappings(configured); err != nil {
		return evidence.LegacyConfiguredEvidenceSetV1{}, err
	}
	adapters, err := selectedWP2AliasProjectionAdapters(options.Protocols)
	if err != nil {
		return evidence.LegacyConfiguredEvidenceSetV1{}, err
	}
	efforts, err := selectedWP2AliasProjectionEfforts(options.Efforts)
	if err != nil {
		return evidence.LegacyConfiguredEvidenceSetV1{}, err
	}
	settings := defaultRuntimeSettings()
	settings.ModelMappings = append(settings.ModelMappings, wp2ModelMappings(configured)...)
	routes := make([]routeDefinition, 0, len(legacyIDs)+len(configured))
	seenRoutes := make(map[string]bool, len(legacyIDs)+len(configured))
	for _, id := range legacyIDs {
		route, ok := registeredRoute(id, settings.ModelMappings)
		if !ok || (route.Kind != routeKindLegacyDirect && route.Kind != routeKindConfigured) || route.Kind == routeKindConfigured && !route.ConfiguredMapping {
			return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("WP2 legacy route %q did not resolve as legacy_direct or an existing configured override", id)
		}
		routes = append(routes, route)
		seenRoutes[strings.ToLower(route.ID)] = true
	}
	for _, mapping := range configured {
		route, ok := registeredRoute(mapping.PublicModel, settings.ModelMappings)
		if !ok || route.Kind != routeKindConfigured || !route.ConfiguredMapping {
			return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("WP2 configured mapping %q did not normalize as configured_mapping", mapping.PublicModel)
		}
		if seenRoutes[strings.ToLower(route.ID)] {
			continue
		}
		routes = append(routes, route)
		seenRoutes[strings.ToLower(route.ID)] = true
	}
	set := evidence.LegacyConfiguredEvidenceSetV1{
		Schema:   evidence.LegacyConfiguredEvidenceSetSchemaV1,
		Catalog:  make([]evidence.LegacyConfiguredCatalogEntryV1, 0, len(routes)),
		Matrix:   make([]evidence.LegacyConfiguredMatrixEntryV1, 0, len(routes)*len(adapters)),
		Failures: make([]evidence.LegacyConfiguredFailureEntryV1, 0, len(adapters)),
		Records:  []evidence.LegacyConfiguredRecordV1{},
	}
	for _, route := range routes {
		catalogRecord, err := runWP2LegacyConfiguredCatalogObservation(route, settings, options.Binding)
		if err != nil {
			return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("catalog %s: %w", route.ID, err)
		}
		set.Catalog = append(set.Catalog, evidence.LegacyConfiguredCatalogEntryV1{
			RequestedModel: route.ID, CanonicalRoute: route.CanonicalRoute, ResolvedTone: route.Tone,
			RouteKind: string(route.Kind), Owner: route.Owner, CatalogVisibility: string(route.CatalogVisibility),
			ConfiguredMapping: route.ConfiguredMapping, Experimental: route.Experimental,
			DefaultReasoningLevel: catalogRecord.Observation.DefaultReasoningLevel, Classification: evidence.ClassificationInconclusive,
			Listed: route.CatalogVisibility != catalogHidden, ObservationSHA256: catalogRecord.ObservationSHA256,
		})
		set.Records = append(set.Records, catalogRecord)
		for _, adapter := range adapters {
			entry := evidence.LegacyConfiguredMatrixEntryV1{
				RequestedModel: route.ID, CanonicalRoute: route.CanonicalRoute, RouteKind: string(route.Kind),
				Owner: route.Owner, ConfiguredMapping: route.ConfiguredMapping, Protocol: adapter.protocol,
				EndpointPath: adapter.path, Classification: evidence.ClassificationVerified,
				EffortObservations: []evidence.LegacyConfiguredEffortObservationRefV1{},
			}
			for _, effort := range effortsForWP2AliasAdapter(adapter, efforts) {
				resolution, err := resolveRoute(route.ID, effort, settings.ModelMappings)
				if err != nil {
					return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("resolve %s/%s: %w", route.ID, wp2AliasEffortLabel(effort), err)
				}
				descriptor := wp2LegacyConfiguredDescriptor(route, resolution, adapter, wp2AliasEffortLabel(effort), adapter.supportsEffort && effort != "")
				record, err := runWP2LegacyConfiguredSuccessObservation(route, resolution, settings, adapter, effort, descriptor, options.Binding)
				if err != nil {
					return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("%s %s %s: %w", route.ID, adapter.protocol, wp2AliasEffortLabel(effort), err)
				}
				entry.EffortObservations = append(entry.EffortObservations, evidence.LegacyConfiguredEffortObservationRefV1{Effort: wp2AliasEffortLabel(effort), ResolvedTone: resolution.ResolvedTone, ObservationSHA256: record.ObservationSHA256})
				set.Records = append(set.Records, record)
			}
			set.Matrix = append(set.Matrix, entry)
		}
	}
	for _, adapter := range adapters {
		for _, failure := range []struct {
			model  string
			caseID evidence.LegacyConfiguredCase
			code   string
		}{
			{model: "wp2-unknown-legacy-route", caseID: evidence.LegacyConfiguredCaseUnknownRoute, code: "model_not_found"},
		} {
			descriptor := evidence.LegacyConfiguredDescriptor{Protocol: adapter.protocol, EndpointPath: adapter.path, AuthMode: adapter.authMode, Effort: "omitted"}
			record, err := runWP2LegacyConfiguredFailureObservation(failure.model, failure.caseID, settings, adapter, descriptor, options.Binding)
			if err != nil {
				return evidence.LegacyConfiguredEvidenceSetV1{}, fmt.Errorf("%s %s: %w", failure.model, adapter.protocol, err)
			}
			set.Failures = append(set.Failures, evidence.LegacyConfiguredFailureEntryV1{RequestedModel: failure.model, CaseID: failure.caseID, Protocol: adapter.protocol, ObservationSHA256: record.ObservationSHA256, ExpectedFailureCode: failure.code})
			set.Records = append(set.Records, record)
		}
	}
	return set, nil
}

func selectedWP2LegacyRoutes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), wp2LegacyDirectOrder...), nil
	}
	allowed := map[string]bool{}
	for _, id := range wp2LegacyDirectOrder {
		allowed[id] = true
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, id := range requested {
		id = strings.ToLower(strings.TrimSpace(id))
		if !allowed[id] {
			return nil, fmt.Errorf("route %q is not a WP2 legacy direct route", id)
		}
		if !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	return selected, nil
}

func validateWP2ConfiguredMappings(mappings []WP2ConfiguredMapping) error {
	settings := defaultRuntimeSettings()
	settings.ModelMappings = wp2ModelMappings(mappings)
	if err := validateSettings(settings); err != nil {
		return err
	}
	protected := map[string]bool{}
	for _, route := range builtInRouteRegistry {
		if strings.HasPrefix(strings.ToLower(route.ID), "m365-") || route.Kind == routeKindAlias || route.Kind == routeKindPreset {
			protected[strings.ToLower(route.ID)] = true
		}
	}
	for _, mapping := range mappings {
		if protected[strings.ToLower(strings.TrimSpace(mapping.PublicModel))] {
			return fmt.Errorf("configured mapping %q targets a protected canonical or compatibility identity", mapping.PublicModel)
		}
	}
	return nil
}

func wp2ModelMappings(mappings []WP2ConfiguredMapping) []modelMapping {
	out := make([]modelMapping, 0, len(mappings))
	for _, mapping := range mappings {
		out = append(out, modelMapping{PublicModel: mapping.PublicModel, UpstreamTone: mapping.UpstreamTone, DisplayName: mapping.DisplayName, DefaultReasoningLevel: mapping.DefaultReasoningLevel})
	}
	return out
}

func wp2LegacyConfiguredDescriptor(route routeDefinition, resolution routeResolution, adapter wp2AliasProjectionProtocolAdapter, effort string, effortApplied bool) evidence.LegacyConfiguredDescriptor {
	return evidence.LegacyConfiguredDescriptor{
		RequestedModel: route.ID, CanonicalRoute: resolution.CanonicalRoute, ResolvedTone: resolution.ResolvedTone,
		RouteKind: string(route.Kind), Owner: route.Owner, OperationalStatus: string(route.OperationalStatus),
		RuntimeMappingEvidence: string(route.MappingEvidence), AcceptedMappingEvidence: string(route.MappingEvidence),
		IdentityStatus: string(route.IdentityStatus), CatalogVisibility: string(route.CatalogVisibility),
		ConfiguredMapping: route.ConfiguredMapping, Experimental: route.Experimental,
		DefaultReasoningLevel: route.DefaultReasoningLevel, Protocol: adapter.protocol, EndpointPath: adapter.path,
		AuthMode: adapter.authMode, Effort: effort, ReasoningEffortApplied: effortApplied,
		ReasoningEffortIgnored: resolution.ReasoningEffortIgnored, ListedInCatalog: route.CatalogVisibility != catalogHidden,
	}
}

func runWP2LegacyConfiguredCatalogObservation(route routeDefinition, settings runtimeSettings, binding evidence.CaptureBinding) (evidence.LegacyConfiguredRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServerWithSettings(chat, settings)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth("api_key", writer, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	var model map[string]any
	for _, item := range body.Data {
		if wp2AliasString(item["id"]) == route.ID {
			model = item
			break
		}
	}
	listed := model != nil
	if !listed {
		model = map[string]any{}
	}
	descriptor := evidence.LegacyConfiguredDescriptor{
		RequestedModel: route.ID, CanonicalRoute: route.CanonicalRoute, ResolvedTone: route.Tone,
		RouteKind: string(route.Kind), Owner: route.Owner, OperationalStatus: string(route.OperationalStatus),
		RuntimeMappingEvidence: string(route.MappingEvidence), AcceptedMappingEvidence: string(route.MappingEvidence),
		IdentityStatus: string(route.IdentityStatus), CatalogVisibility: string(route.CatalogVisibility),
		ConfiguredMapping: route.ConfiguredMapping, Experimental: route.Experimental,
		DefaultReasoningLevel: wp2AliasString(model["default_reasoning_level"]), Protocol: "openai_models_catalog", EndpointPath: "/v1/models",
		AuthMode: "api_key", Effort: "not_applicable", ListedInCatalog: listed,
	}
	capture := evidence.LegacyConfiguredCaptureV1{
		Schema: evidence.LegacyConfiguredCaptureSchemaV1, CaseID: evidence.LegacyConfiguredCaseCatalog,
		Classification: evidence.ClassificationInconclusive, RequestedModel: route.ID,
		CanonicalRoute: wp2AliasString(model["canonical_route"]), ResolvedTone: wp2AliasString(model["resolved_tone"]),
		RouteKind: wp2AliasString(model["route_kind"]), Owner: wp2AliasString(model["owned_by"]),
		OperationalStatus: wp2AliasString(model["operational_status"]), MappingEvidence: wp2AliasString(model["mapping_evidence"]),
		IdentityStatus: wp2AliasString(model["identity_status"]), CatalogVisibility: wp2AliasString(model["catalog_visibility"]),
		ConfiguredMapping: wp2AliasBool(model["configured_mapping"]), Experimental: wp2AliasBool(model["experimental"]),
		DefaultReasoningLevel: wp2AliasString(model["default_reasoning_level"]), ListedInCatalog: listed,
		PerKeyRestricted: wp2PerKeyRestricted(model), Protocol: "openai_models_catalog", EndpointPath: "/v1/models",
		AuthMode: "api_key", Effort: "not_applicable", HTTPStatus: writer.Code,
		RequestIDObserved: writer.Header().Get(requestIDHeader) != "", SecurityHeadersObserved: wp2SecurityHeadersObserved(writer),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	return evidence.CaptureLegacyConfigured(raw, descriptor, binding)
}

func runWP2LegacyConfiguredSuccessObservation(route routeDefinition, resolution routeResolution, settings runtimeSettings, adapter wp2AliasProjectionProtocolAdapter, effort string, descriptor evidence.LegacyConfiguredDescriptor, binding evidence.CaptureBinding) (evidence.LegacyConfiguredRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "WP2_LEGACY_CONFIGURED_OK"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServerWithSettings(chat, settings)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth(adapter.authMode, writer, adapter.buildRequest(route.ID, effort))
	response, text, err := adapter.decodeResponse(writer.Body.Bytes())
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	metadata, _ := response["m365"].(map[string]any)
	routeSwitches, crossAccountResends := wp2AttemptDivergence(chat.attempts, resolution.ResolvedTone)
	capture := evidence.LegacyConfiguredCaptureV1{
		Schema: evidence.LegacyConfiguredCaptureSchemaV1, CaseID: evidence.LegacyConfiguredCaseSuccess,
		Classification: evidence.ClassificationVerified, RequestedModel: route.ID,
		MetadataRequestedModel: wp2AliasString(metadata["requested_model"]), MetadataResponseModel: wp2AliasString(metadata["response_model"]),
		TopLevelModel: wp2AliasString(response["model"]), CanonicalRoute: wp2AliasString(metadata["canonical_route"]),
		ResolvedTone: wp2AliasString(metadata["resolved_tone"]), RouteKind: wp2AliasString(metadata["route_kind"]),
		Owner: route.Owner, OperationalStatus: wp2AliasString(metadata["operational_status"]),
		MappingEvidence: wp2AliasString(metadata["mapping_evidence"]), IdentityStatus: wp2AliasString(metadata["identity_status"]),
		CatalogVisibility: wp2AliasString(metadata["catalog_visibility"]), ConfiguredMapping: wp2AliasBool(metadata["configured_mapping"]),
		Experimental: wp2AliasBool(metadata["experimental"]), DefaultReasoningLevel: route.DefaultReasoningLevel,
		PerKeyRestricted: wp2PerKeyRestricted(metadata), Protocol: adapter.protocol, EndpointPath: adapter.path,
		AuthMode: adapter.authMode, Effort: wp2AliasEffortLabel(effort), ReasoningEffortApplied: adapter.supportsEffort && effort != "",
		ReasoningEffortIgnored: wp2AliasBool(metadata["reasoning_effort_ignored"]), HTTPStatus: writer.Code,
		BasicTextDelivered: strings.TrimSpace(text) != "", UpstreamAttempts: len(chat.attempts), RouteSwitches: routeSwitches,
		CrossAccountResends: crossAccountResends, RequestIDObserved: writer.Header().Get(requestIDHeader) != "",
		SecurityHeadersObserved: wp2SecurityHeadersObserved(writer), FailureCode: routeProtocolFailureCode(response),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	return evidence.CaptureLegacyConfigured(raw, descriptor, binding)
}

func runWP2LegacyConfiguredFailureObservation(model string, caseID evidence.LegacyConfiguredCase, settings runtimeSettings, adapter wp2AliasProjectionProtocolAdapter, descriptor evidence.LegacyConfiguredDescriptor, binding evidence.CaptureBinding) (evidence.LegacyConfiguredRecordV1, error) {
	chat := &wp2HarnessChat{result: chathub.Result{Text: "SHOULD_NOT_RUN"}}
	harness, cleanup, err := newWP2RouteProtocolHarnessServerWithSettings(chat, settings)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	defer cleanup()
	writer := httptest.NewRecorder()
	harness.serveWithAuth(adapter.authMode, writer, adapter.buildRequest(model, ""))
	response := map[string]any{}
	_ = json.Unmarshal(writer.Body.Bytes(), &response)
	capture := evidence.LegacyConfiguredCaptureV1{
		Schema: evidence.LegacyConfiguredCaptureSchemaV1, CaseID: caseID, Classification: evidence.ClassificationInconclusive,
		RequestedModel: model, Protocol: adapter.protocol, EndpointPath: adapter.path, AuthMode: adapter.authMode,
		Effort: "omitted", HTTPStatus: writer.Code, UpstreamAttempts: len(chat.attempts),
		RequestIDObserved: writer.Header().Get(requestIDHeader) != "", SecurityHeadersObserved: wp2SecurityHeadersObserved(writer),
		FailureCode: routeProtocolFailureCode(response),
	}
	raw, err := json.Marshal(capture)
	if err != nil {
		return evidence.LegacyConfiguredRecordV1{}, err
	}
	return evidence.CaptureLegacyConfigured(raw, descriptor, binding)
}

func WP2LegacyConfiguredEffectiveSettings(mappings []WP2ConfiguredMapping) map[string]any {
	ordered := append([]WP2ConfiguredMapping(nil), mappings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PublicModel < ordered[j].PublicModel })
	return map[string]any{
		"schema":              "m365-wp2-legacy-configured-effective-settings/v1",
		"legacy_routes":       append([]string(nil), wp2LegacyDirectOrder...),
		"configured_mappings": ordered,
		"protocols":           []string{"openai_chat_completions_nonstream", "openai_responses_nonstream", "anthropic_messages_nonstream", "legacy_chat_nonstream", "legacy_chat_stream"},
		"efforts":             []string{"omitted", "none", "minimal", "low", "medium", "high", "xhigh"},
		"production_access":   false, "hermes_access": false, "per_key_governance": false, "visibility_reduction": false,
	}
}
