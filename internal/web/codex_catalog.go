// Codex model catalog compatibility lives here. It is intentionally kept in
// package web because route handlers share unexported request and settings types.
package web

import (
	"encoding/json"
	"fmt"
	"strings"
)

type modelLimits struct{ ContextWindow, MaxInputTokens, MaxOutputTokens int }
type reasoningConfig struct {
	Effort            string                     `json:"effort,omitempty"`
	Summary           string                     `json:"summary,omitempty"`
	IngressRaw        json.RawMessage            `json:"-"`
	IngressExtensions map[string]json.RawMessage `json:"-"`
}

type modelSpec struct {
	ID, Owner, DisplayName, DefaultReasoningLevel string
	CanonicalRoute, ResolvedTone                  string
	RouteKind                                     routeKind
	OperationalStatus                             operationalStatus
	MappingEvidence                               mappingEvidence
	IdentityStatus                                identityStatus
	CatalogVisibility                             catalogVisibility
	AliasUsed, CompatibilityRequired              bool
	ConfiguredMapping, Experimental, Deprecated   bool
}

type reasoningEffortPreset struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

var advertisedReasoningEfforts = []reasoningEffortPreset{
	{Effort: "none", Description: "Disable additional reasoning."},
	{Effort: "minimal", Description: "Fast responses with minimal reasoning."},
	{Effort: "low", Description: "Fast responses with lighter reasoning."},
	{Effort: "medium", Description: "Balances speed and reasoning depth for everyday tasks."},
	{Effort: "high", Description: "Greater reasoning depth for complex problems."},
	{Effort: "xhigh", Description: "Extra high reasoning depth for complex problems."},
}

// gatewayCodexBaseInstructions is returned only in the Codex model catalog.
// Codex uses it to build its own request instructions; it is not interpreted
// or forwarded directly by the gateway's ChatHub adapter.
const gatewayCodexBaseInstructions = `You are Codex, a coding agent collaborating with the user in their workspace. Follow the user's request, inspect the repository before making changes, preserve unrelated work, and verify changes proportionately. Keep responses clear, concise, and grounded in observed evidence.`

func codexModelMessages() map[string]any {
	return map[string]any{
		"instructions_template": gatewayCodexBaseInstructions,
		"instructions_variables": map[string]string{
			"personality_default":   "",
			"personality_friendly":  "",
			"personality_pragmatic": "",
		},
		"approvals":   nil,
		"auto_review": nil,
	}
}

func gatewayModelSpecs() []modelSpec {
	return modelSpecsFromRoutes(catalogRouteDefinitions(nil))
}

// gatewayModels remains as the package-level compatibility projection used by
// existing tests and callers. Runtime catalog generation still uses the route
// registry so settings mappings are evaluated per request.
var gatewayModels = gatewayModelSpecs()

func modelSpecsFromRoutes(routes []routeDefinition) []modelSpec {
	models := make([]modelSpec, 0, len(routes))
	for _, route := range routes {
		displayName := route.DisplayName
		if displayName == "" {
			displayName = route.ID
		}
		models = append(models, modelSpec{
			ID:                    route.ID,
			Owner:                 route.Owner,
			DisplayName:           displayName,
			DefaultReasoningLevel: route.DefaultReasoningLevel,
			CanonicalRoute:        route.CanonicalRoute,
			ResolvedTone:          route.Tone,
			RouteKind:             route.Kind,
			OperationalStatus:     route.OperationalStatus,
			MappingEvidence:       route.MappingEvidence,
			IdentityStatus:        route.IdentityStatus,
			CatalogVisibility:     route.CatalogVisibility,
			AliasUsed:             route.Kind == routeKindAlias || route.Kind == routeKindPreset,
			CompatibilityRequired: route.CompatibilityRequired,
			ConfiguredMapping:     route.ConfiguredMapping,
			Experimental:          route.Experimental,
			Deprecated:            route.Deprecated,
		})
	}
	return models
}

func validUpstreamTone(tone string) bool {
	for _, known := range knownUpstreamTones() {
		if tone == known {
			return true
		}
	}
	return false
}

func knownUpstreamTones() []string {
	return []string{"Gpt_5_2_Chat", "Gpt_5_2_Reasoning", "Gpt_5_3_Chat", "Gpt_5_4_Chat", "Gpt_5_4_Reasoning", "Gpt_5_5_Chat", "Gpt_5_5_Reasoning", "Gpt_5_6_Reasoning", "Gpt_Quick", "Gpt_Reasoning", "Claude_Sonnet", "Claude_Sonnet_Reasoning"}
}

func configuredModelSpecs(mappings []modelMapping) []modelSpec {
	return modelSpecsFromRoutes(catalogRouteDefinitions(mappings))
}

func configuredModelLimitsForSettings(cfg runtimeSettings) modelLimits {
	contextWindow := cfg.ContextWindow
	maxOutput := cfg.MaxOutputTokens
	if maxOutput >= contextWindow {
		maxOutput = contextWindow / 8
		if maxOutput < 1 {
			maxOutput = 1
		}
	}
	return modelLimits{ContextWindow: contextWindow, MaxInputTokens: contextWindow - maxOutput, MaxOutputTokens: maxOutput}
}
func normalizeReasoningEffort(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "", nil
	}
	switch e {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return e, nil
	case "max", "ultra":
		// Hermes v0.20.0 exposes these levels. ChatHub has no corresponding
		// request field, so keep the strongest supported Sidecar mapping without
		// forwarding the incompatible caller value upstream.
		return "xhigh", nil
	}
	return "", fmt.Errorf("unsupported reasoning effort %q; use none, minimal, low, medium, high, xhigh, max, or ultra", e)
}
func modelCatalogForSettingsAndEvidence(cfg runtimeSettings, projection *catalogEvidenceProjection) []map[string]any {
	l := configuredModelLimitsForSettings(cfg)
	models := configuredModelSpecs(cfg.ModelMappings)
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		// Keep capability fields both at the top level and under capabilities:
		// different OpenAI-compatible clients inspect different locations.
		features := []string{"tools", "function_calling", "streaming", "reasoning", "vision"}
		modalities := []string{"text", "image"}
		caps := map[string]any{
			"chat_completions": true, "responses": true, "streaming": true,
			"tools": true, "reasoning": true,
			"reasoning_efforts": advertisedReasoningEfforts, "supported_reasoning_levels": advertisedReasoningEfforts,
			"reasoning_mode": "gateway_tone_routing", "supports_tools": true, "tool_calls": true,
			"function_calling": true, "supports_function_calling": true, "supports_vision": true,
			"vision": true, "modalities": modalities, "input_modalities": modalities,
			"output_modalities": []string{"text"}, "supported_features": features,
		}
		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.ID
		}
		defaultReasoningLevel := m.DefaultReasoningLevel
		if defaultReasoningLevel == "" {
			defaultReasoningLevel = "medium"
		}
		entry := map[string]any{
			"id": m.ID, "slug": m.ID, "display_name": displayName, "description": "Microsoft 365 gateway model route.",
			"canonical_route": m.CanonicalRoute, "resolved_tone": m.ResolvedTone, "route_kind": m.RouteKind,
			"operational_status": m.OperationalStatus, "mapping_evidence": m.MappingEvidence, "identity_status": m.IdentityStatus,
			"catalog_visibility": m.CatalogVisibility, "alias_used": m.AliasUsed, "compatibility_required": m.CompatibilityRequired,
			"configured_mapping": m.ConfiguredMapping, "experimental": m.Experimental, "deprecated": m.Deprecated,
			"base_instructions": gatewayCodexBaseInstructions, "model_messages": codexModelMessages(),
			"default_reasoning_level": defaultReasoningLevel, "object": "model", "owned_by": m.Owner,
			"shell_type": "shell_command", "visibility": "list", "supported_in_api": true, "priority": 1,
			"additional_speed_tiers": []string{}, "service_tiers": []any{},
			"availability_nux": nil, "upgrade": nil, "include_skills_usage_instructions": false,
			"supports_reasoning_summaries": true, "default_reasoning_summary": "none",
			"support_verbosity": true, "default_verbosity": "low", "apply_patch_tool_type": "freeform",
			"web_search_tool_type": "text_and_image", "truncation_policy": map[string]any{"mode": "tokens", "limit": 10000},
			"supports_parallel_tool_calls": true, "supports_image_detail_original": true,
			"x_m365_accepted_downgrade_parameters": []string{"image_detail", "verbosity"},
			"x_m365_verbosity_semantics":           "accepted_and_downgraded",
			"x_m365_image_detail_semantics":        "accepted_and_downgraded",
			"max_context_window":                   l.ContextWindow, "effective_context_window_percent": 95,
			"experimental_supported_tools": []any{}, "supports_search_tool": true, "use_responses_lite": false,
			"tool_mode": "code_mode_only", "multi_agent_version": "v2",
			"context_window": l.ContextWindow, "max_input_tokens": l.MaxInputTokens, "max_output_tokens": l.MaxOutputTokens,
			"x_m365_standard_fields_source": "compatibility_default",
			"x_m365_route_source":           "registry_config",
			"x_m365_mapping_source":         "registry_config",
			"x_m365_protocol_source":        "none",
			"x_m365_evidence_source":        "none",
			"capabilities":                  caps, "supports_tools": true, "tool_calls": true,
			"supported_reasoning_levels": advertisedReasoningEfforts,
			"function_calling":           true, "supports_function_calling": true, "supports_vision": true,
			"vision": true, "modalities": modalities, "input_modalities": modalities,
			"output_modalities": []string{"text"}, "supported_features": features,
		}
		projection.apply(entry, m)
		out = append(out, entry)
	}
	return out
}
