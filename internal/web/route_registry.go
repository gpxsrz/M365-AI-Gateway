package web

import (
	"fmt"
	"net/http"
	"strings"
)

type routeKind string
type operationalStatus string
type mappingEvidence string
type identityStatus string
type catalogVisibility string

const (
	routeKindWebMode      routeKind = "web_mode"
	routeKindWebModel     routeKind = "web_model_route"
	routeKindAlias        routeKind = "alias"
	routeKindPreset       routeKind = "preset"
	routeKindConfigured   routeKind = "configured_mapping"
	routeKindLegacyDirect routeKind = "legacy_direct"

	operationalEnabled  operationalStatus = "enabled"
	operationalDisabled operationalStatus = "disabled"

	mappingAPIToneAccepted mappingEvidence = "api_tone_accepted"
	mappingUnverified      mappingEvidence = "unverified"

	identityDynamicUnidentified identityStatus = "dynamic_unidentified"
	identityAcceptedUnverified  identityStatus = "accepted_unverified"

	catalogPublic        catalogVisibility = "public"
	catalogCompatibility catalogVisibility = "compatibility"
	catalogHidden        catalogVisibility = "hidden"
)

type routeDefinition struct {
	ID                    string
	CanonicalRoute        string
	Tone                  string
	WebLabel              string
	Kind                  routeKind
	OperationalStatus     operationalStatus
	MappingEvidence       mappingEvidence
	IdentityStatus        identityStatus
	CatalogVisibility     catalogVisibility
	CompatibilityRequired bool
	Experimental          bool
	ConfiguredMapping     bool
	Deprecated            bool
	Owner                 string
	DisplayName           string
	DefaultReasoningLevel string
}

type routeResolution struct {
	RequestedModel         string
	ResponseModel          string
	CanonicalRoute         string
	ResolvedTone           string
	WebLabel               string
	RouteKind              routeKind
	OperationalStatus      operationalStatus
	MappingEvidence        mappingEvidence
	IdentityStatus         identityStatus
	CatalogVisibility      catalogVisibility
	AliasUsed              bool
	CompatibilityRequired  bool
	Experimental           bool
	ReasoningEffortIgnored bool
	ConfiguredMapping      bool
}

func (r routeResolution) metadata() map[string]any {
	return map[string]any{
		"requested_model":          r.RequestedModel,
		"response_model":           r.ResponseModel,
		"canonical_route":          r.CanonicalRoute,
		"resolved_tone":            r.ResolvedTone,
		"web_label":                r.WebLabel,
		"route_kind":               r.RouteKind,
		"operational_status":       r.OperationalStatus,
		"mapping_evidence":         r.MappingEvidence,
		"identity_status":          r.IdentityStatus,
		"catalog_visibility":       r.CatalogVisibility,
		"alias_used":               r.AliasUsed,
		"compatibility_required":   r.CompatibilityRequired,
		"experimental":             r.Experimental,
		"fallback_used":            false,
		"reasoning_effort_ignored": r.ReasoningEffortIgnored,
		"configured_mapping":       r.ConfiguredMapping,
	}
}

type routeResolveError struct {
	Status  int
	Code    string
	Message string
}

func (e *routeResolveError) Error() string { return e.Message }

var builtInRouteRegistry = []routeDefinition{
	{
		ID: "m365-auto", CanonicalRoute: "m365-auto", Tone: "Magic", WebLabel: "Auto",
		Kind: routeKindWebMode, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityDynamicUnidentified, CatalogVisibility: catalogPublic, Owner: "microsoft-365",
		DisplayName: "M365 Auto", DefaultReasoningLevel: "none",
	},
	{
		ID: "m365-gpt-5.6-think-deeper", CanonicalRoute: "m365-gpt-5.6-think-deeper", Tone: "Gpt_5_6_Reasoning", WebLabel: "GPT 5.6 — Think deeper",
		Kind: routeKindWebModel, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Owner: "microsoft-365",
		DisplayName: "M365 GPT 5.6 — Think deeper", DefaultReasoningLevel: "medium",
	},
	{
		ID: "m365-gpt-5.5-quick-response", CanonicalRoute: "m365-gpt-5.5-quick-response", Tone: "Gpt_5_5_Chat", WebLabel: "GPT 5.5 — Quick response",
		Kind: routeKindWebModel, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Owner: "microsoft-365",
		DisplayName: "M365 GPT 5.5 — Quick response", DefaultReasoningLevel: "low",
	},
	{
		ID: "m365-copilot", CanonicalRoute: "m365-auto", Tone: "Magic", WebLabel: "Auto",
		Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityDynamicUnidentified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true,
		Owner: "microsoft-365", DisplayName: "M365 Copilot (compatibility alias)", DefaultReasoningLevel: "none",
	},
	{
		ID: "gpt-5.6-reasoning", CanonicalRoute: "m365-gpt-5.6-think-deeper", Tone: "Gpt_5_6_Reasoning", WebLabel: "GPT 5.6 — Think deeper",
		Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true,
		Owner: "microsoft-365", DisplayName: "GPT 5.6 Reasoning (compatibility alias)", DefaultReasoningLevel: "medium",
	},
	{
		ID: "gpt-5.5", CanonicalRoute: "m365-gpt-5.5-quick-response", Tone: "Gpt_5_5_Chat", WebLabel: "GPT 5.5 — Quick response",
		Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted,
		IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true,
		Owner: "microsoft-365", DisplayName: "GPT 5.5 (compatibility alias)", DefaultReasoningLevel: "low",
	},

	// These direct legacy routes were publicly callable before WP1. WP1 keeps
	// that behavior unchanged; later per-key governance is deliberately absent.
	{ID: "gpt-5.2", CanonicalRoute: "gpt-5.2", Tone: "Gpt_5_2_Chat", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "gpt-5.2-reasoning", CanonicalRoute: "gpt-5.2-reasoning", Tone: "Gpt_5_2_Reasoning", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "gpt-5.3", CanonicalRoute: "gpt-5.3", Tone: "Gpt_5_3_Chat", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "gpt-5.4", CanonicalRoute: "gpt-5.4", Tone: "Gpt_5_4_Chat", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "gpt-5.4-reasoning", CanonicalRoute: "gpt-5.4-reasoning", Tone: "Gpt_5_4_Reasoning", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "gpt-5.5-reasoning", CanonicalRoute: "gpt-5.5-reasoning", Tone: "Gpt_5_5_Reasoning", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "microsoft-365"},
	{ID: "claude-sonnet", CanonicalRoute: "claude-sonnet", Tone: "Claude_Sonnet", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "anthropic-via-microsoft-365"},
	{ID: "claude-sonnet-reasoning", CanonicalRoute: "claude-sonnet-reasoning", Tone: "Claude_Sonnet_Reasoning", Kind: routeKindLegacyDirect, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true, Owner: "anthropic-via-microsoft-365"},

	{ID: "gpt-5.6-sol", CanonicalRoute: "m365-gpt-5.6-think-deeper", Tone: "Gpt_5_6_Reasoning", Kind: routeKindPreset, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true, Owner: "microsoft-365", DisplayName: "GPT-5.6-Sol (compatibility preset)", DefaultReasoningLevel: "low"},
	{ID: "gpt-5.6-terra", CanonicalRoute: "m365-gpt-5.6-think-deeper", Tone: "Gpt_5_6_Reasoning", Kind: routeKindPreset, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true, Owner: "microsoft-365", DisplayName: "GPT-5.6-Terra (compatibility preset)", DefaultReasoningLevel: "medium"},
	{ID: "gpt-5.6-luna", CanonicalRoute: "m365-gpt-5.6-think-deeper", Tone: "Gpt_5_6_Reasoning", Kind: routeKindPreset, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogCompatibility, CompatibilityRequired: true, Owner: "microsoft-365", DisplayName: "GPT-5.6-Luna (compatibility preset)", DefaultReasoningLevel: "medium"},

	// Request-only aliases remain callable but are not projected into catalogs.
	{ID: "claude", CanonicalRoute: "claude-sonnet", Tone: "Claude_Sonnet", Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogHidden, CompatibilityRequired: true, Owner: "anthropic-via-microsoft-365"},
	{ID: "gpt-5.4-quick", CanonicalRoute: "gpt-5.4", Tone: "Gpt_5_4_Chat", Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogHidden, CompatibilityRequired: true, Owner: "microsoft-365"},
	{ID: "gpt-5.3-think-deeper", CanonicalRoute: "gpt-5.3", Tone: "Gpt_5_3_Chat", Kind: routeKindAlias, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogHidden, CompatibilityRequired: true, Owner: "microsoft-365"},

	// WP6 live WebSocket evidence supersedes the old Gpt_Quick/Gpt_Reasoning
	// empty-result disposition. Keep these request-only until the Phase 8 live
	// gate proves the integrated catalog/consumer path.
	{ID: "quick", CanonicalRoute: "quick", Tone: "Chat", WebLabel: "Quick response", Kind: routeKindWebMode, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogHidden, Owner: "microsoft-365"},
	{ID: "think-deeper", CanonicalRoute: "think-deeper", Tone: "Reasoning", WebLabel: "Think deeper", Kind: routeKindWebMode, OperationalStatus: operationalEnabled, MappingEvidence: mappingAPIToneAccepted, IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogHidden, Owner: "microsoft-365"},
}

func cloneRouteDefinition(route routeDefinition) routeDefinition { return route }

func configuredRouteDefinition(mapping modelMapping) routeDefinition {
	id := strings.TrimSpace(mapping.PublicModel)
	owner := "microsoft-365"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mapping.UpstreamTone)), "claude_") {
		owner = "anthropic-via-microsoft-365"
	}
	return routeDefinition{
		ID: id, CanonicalRoute: id, Tone: strings.TrimSpace(mapping.UpstreamTone),
		Kind: routeKindConfigured, OperationalStatus: operationalEnabled,
		MappingEvidence: mappingUnverified, IdentityStatus: identityAcceptedUnverified,
		CatalogVisibility: catalogCompatibility, CompatibilityRequired: true,
		ConfiguredMapping: true, Owner: owner,
		DisplayName:           strings.TrimSpace(mapping.DisplayName),
		DefaultReasoningLevel: strings.TrimSpace(mapping.DefaultReasoningLevel),
	}
}

func routeRegistry(mappings []modelMapping) []routeDefinition {
	routes := make([]routeDefinition, len(builtInRouteRegistry))
	index := make(map[string]int, len(builtInRouteRegistry)+len(mappings))
	for i, route := range builtInRouteRegistry {
		routes[i] = cloneRouteDefinition(route)
		index[strings.ToLower(strings.TrimSpace(route.ID))] = i
	}
	for _, mapping := range mappings {
		id := strings.TrimSpace(mapping.PublicModel)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		configured := configuredRouteDefinition(mapping)
		if i, exists := index[key]; exists {
			// Canonical M365 identities, verified Web modes, and accepted compatibility
			// identities are route-locked. Legacy direct routes retain the old settings
			// override behavior so WP1 does not retroactively break existing mappings.
			if strings.HasPrefix(key, "m365-") || routes[i].Kind == routeKindWebMode || routes[i].Kind == routeKindAlias || routes[i].Kind == routeKindPreset {
				continue
			}
			routes[i] = configured
			continue
		}
		index[key] = len(routes)
		routes = append(routes, configured)
	}
	return routes
}

func registeredRoute(model string, mappings []modelMapping) (routeDefinition, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, route := range routeRegistry(mappings) {
		if strings.EqualFold(route.ID, model) {
			return cloneRouteDefinition(route), true
		}
	}
	return routeDefinition{}, false
}

func serverRuntimeSettings(s *Server) runtimeSettings {
	if s != nil && s.settings != nil {
		return s.settings.get()
	}
	return currentSettings()
}

func resolutionFromRoute(requested string, route routeDefinition) routeResolution {
	return routeResolution{
		RequestedModel:        requested,
		ResponseModel:         requested,
		CanonicalRoute:        route.CanonicalRoute,
		ResolvedTone:          route.Tone,
		WebLabel:              route.WebLabel,
		RouteKind:             route.Kind,
		OperationalStatus:     route.OperationalStatus,
		MappingEvidence:       route.MappingEvidence,
		IdentityStatus:        route.IdentityStatus,
		CatalogVisibility:     route.CatalogVisibility,
		AliasUsed:             route.Kind == routeKindAlias || route.Kind == routeKindPreset,
		CompatibilityRequired: route.CompatibilityRequired,
		Experimental:          route.Experimental,
		ConfiguredMapping:     route.ConfiguredMapping,
	}
}

func routeLocksReasoningEffort(route routeDefinition) bool {
	id := strings.ToLower(strings.TrimSpace(route.ID))
	if strings.HasPrefix(id, "m365-") || route.ConfiguredMapping || route.Kind == routeKindPreset || route.Kind == routeKindWebMode {
		return true
	}
	return id == "gpt-5.6-reasoning"
}

func resolveRoute(model, effort string, mappings []modelMapping) (routeResolution, error) {
	requested := strings.TrimSpace(model)
	if requested == "" {
		requested = "m365-copilot"
	}
	normalizedEffort, err := normalizeReasoningEffort(effort)
	if err != nil {
		return routeResolution{}, &routeResolveError{Status: http.StatusBadRequest, Code: "invalid_reasoning_effort", Message: err.Error()}
	}
	route, ok := registeredRoute(requested, mappings)
	if !ok {
		return routeResolution{}, &routeResolveError{Status: http.StatusNotFound, Code: "model_not_found", Message: fmt.Sprintf("Unknown M365 route: %s", requested)}
	}
	if route.OperationalStatus != operationalEnabled {
		return routeResolution{}, &routeResolveError{Status: http.StatusNotFound, Code: "model_unavailable", Message: fmt.Sprintf("M365 route %q is unavailable because its sidecar mapping is not verified", requested)}
	}
	resolution := resolutionFromRoute(requested, route)
	if routeLocksReasoningEffort(route) {
		resolution.ReasoningEffortIgnored = normalizedEffort != ""
		return resolution, nil
	}
	resolution.ResolvedTone = applyReasoningEffort(route.ID, route.Tone, normalizedEffort)
	return resolution, nil
}

func resolveChatRoute(model, tone, effort string, mappings []modelMapping) (routeResolution, error) {
	if strings.TrimSpace(model) != "" || strings.TrimSpace(tone) == "" {
		return resolveRoute(model, effort, mappings)
	}
	tone = strings.TrimSpace(tone)
	for _, visibility := range []catalogVisibility{catalogPublic, catalogCompatibility, catalogHidden} {
		for _, route := range routeRegistry(mappings) {
			if route.OperationalStatus == operationalEnabled && route.CatalogVisibility == visibility && route.Tone == tone {
				return resolutionFromRoute(route.ID, route), nil
			}
		}
	}
	return routeResolution{}, &routeResolveError{Status: http.StatusNotFound, Code: "model_not_found", Message: fmt.Sprintf("Unknown or unavailable M365 tone: %s", tone)}
}

func applyReasoningEffort(model, base, effort string) string {
	if effort == "" || effort == "none" || effort == "minimal" || effort == "low" {
		return base
	}
	if strings.Contains(strings.ToLower(model), "reasoning") {
		return base
	}
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude", "claude-sonnet":
		return "Claude_Sonnet_Reasoning"
	case "gpt-5.2":
		return "Gpt_5_2_Reasoning"
	case "gpt-5.3":
		return "Gpt_5_3_Reasoning"
	case "gpt-5.4":
		return "Gpt_5_4_Reasoning"
	case "gpt-5.5":
		return "Gpt_5_5_Reasoning"
	case "gpt-5.6":
		return "Gpt_5_5_Reasoning"
	default:
		return "Gpt_Reasoning"
	}
}

func catalogRouteDefinitions(mappings []modelMapping) []routeDefinition {
	routes := routeRegistry(mappings)
	out := make([]routeDefinition, 0, len(routes))
	for _, route := range routes {
		if route.OperationalStatus != operationalEnabled || route.CatalogVisibility == catalogHidden {
			continue
		}
		out = append(out, cloneRouteDefinition(route))
	}
	return out
}
