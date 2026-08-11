package web

import (
	"encoding/json"
	"fmt"
	"m365-native/internal/chathub"
	"os"
	"strings"
)

func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func compatM365Metadata(res chathub.Result, routes ...routeResolution) map[string]any {
	m := map[string]any{
		"conversationId": res.ConversationID,
		"sessionId":      res.SessionID,
		"requestId":      res.RequestID,
		"usage_source":   "unavailable_from_chathub",
	}
	if len(routes) > 0 {
		for key, value := range routes[0].metadata() {
			m[key] = value
		}
	}
	search := chathub.SummarizeSearchEvents(res.Events)
	if search.SearchResultMarkers > 0 {
		m["search"] = search
	}
	if references := chathub.SearchReferences(res.Events, res.RawResult); len(references) > 0 {
		m["references"] = references
	}
	if len(res.Attributions) > 0 {
		attributions := make([]map[string]any, 0, len(res.Attributions))
		for _, attribution := range res.Attributions {
			attributions = append(attributions, chathub.CanonicalAttributionSummary(attribution))
		}
		m["attributions"] = attributions
	}
	if len(res.Artifacts) > 0 {
		artifacts := make([]map[string]any, 0, len(res.Artifacts))
		for _, artifact := range res.Artifacts {
			artifacts = append(artifacts, chathub.CanonicalArtifactSummary(artifact))
		}
		m["artifacts"] = artifacts
	}
	if res.Terminal.Kind != "" {
		m["terminal"] = res.Terminal
	}
	if len(res.UnknownEvents) > 0 {
		m["unknown_provider_event_count"] = len(res.UnknownEvents)
	}
	if envTrue("M365_INCLUDE_UPSTREAM_EVENTS") {
		// The opt-in surface is still limited to the semantic projection. Raw
		// upstream frames can contain hidden reasoning and authentication-bearing
		// URLs and must never cross a caller-facing compatibility boundary.
		m["events"] = chathub.SemanticEvents(res.Events)
	}
	return m
}

// mergeSearchEvidence carries only the caller-safe Bing proof from an earlier
// ChatHub hop. Router calls can legitimately perform native search before the
// final answer call; raw frames and their unrelated reasoning must not cross
// that internal hop.
func mergeSearchEvidence(dst *chathub.Result, src chathub.Result) {
	if dst == nil {
		return
	}
	summary := chathub.SummarizeSearchEvents(src.Events)
	references := chathub.SearchReferences(src.Events, src.RawResult)
	if summary.SearchResultMarkers == 0 && !summary.SourceAttributionObserved && len(references) == 0 {
		return
	}
	frame := map[string]any{"type": 1, "target": "update"}
	if summary.SearchResultMarkers > 0 {
		frame["arguments"] = []any{map[string]any{"messages": []any{map[string]any{"messageType": "Progress", "contentType": "SearchResults"}}}}
	}
	if summary.SourceAttributionObserved {
		frame["sourceAttribution"] = true
	}
	if len(references) > 0 {
		byID := make(map[string]any, len(references))
		for _, reference := range references {
			byID[reference.ID] = reference
		}
		frame["references"] = byID
	}
	raw, err := json.Marshal(frame)
	if err == nil {
		dst.Events = append(dst.Events, raw)
	}
}

func normalizedToolChoiceMode(choice any) string {
	mode, err := strictToolChoiceMode(choice)
	if err != nil {
		return "invalid"
	}
	return mode
}

func strictToolChoiceMode(choice any) (string, error) {
	if choice == nil {
		return "auto", nil
	}
	if s, ok := choice.(string); ok {
		s = strings.TrimSpace(s)
		switch s {
		case "auto", "none", "required":
			return s, nil
		default:
			return "", fmt.Errorf("unsupported tool_choice %q", s)
		}
	}
	if m, ok := choice.(map[string]any); ok {
		if f, ok := m["function"].(map[string]any); ok {
			n, _ := f["name"].(string)
			n = strings.TrimSpace(n)
			if n == "" {
				return "", fmt.Errorf("tool_choice.function.name is required")
			}
			if typ, present := m["type"]; present && typ != "function" {
				return "", fmt.Errorf("tool_choice.type must be function")
			}
			return "named:" + n, nil
		}
		if n, ok := m["name"].(string); ok {
			n = strings.TrimSpace(n)
			if n == "" {
				return "", fmt.Errorf("tool_choice.name is required")
			}
			return "named:" + n, nil
		}
		return "", fmt.Errorf("invalid tool_choice object")
	}
	return "", fmt.Errorf("invalid tool_choice type")
}
