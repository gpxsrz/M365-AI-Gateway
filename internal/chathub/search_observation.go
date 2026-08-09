package chathub

import (
	"encoding/json"
	"strings"
)

// SearchEventSummary is intentionally content-free. It records only event
// classes and marker counts that are safe to persist in regression evidence.
type SearchEventSummary struct {
	RawFrames                 int  `json:"raw_frames"`
	SearchProgress            int  `json:"search_progress"`
	ToolProgress              int  `json:"tool_progress"`
	CodeProgress              int  `json:"code_progress"`
	Message                   int  `json:"message"`
	Unknown                   int  `json:"unknown"`
	SourceAttributionMarkers  int  `json:"source_attribution_markers"`
	SearchResultMarkers       int  `json:"search_result_markers"`
	SourceAttributionObserved bool `json:"source_attribution_observed"`
}

func SummarizeSearchEvents(raw []json.RawMessage) SearchEventSummary {
	summary := SearchEventSummary{RawFrames: len(raw)}
	for _, event := range SemanticEvents(raw) {
		switch event.Kind {
		case "search.progress":
			summary.SearchProgress++
		case "tool.progress":
			summary.ToolProgress++
		case "code.progress":
			summary.CodeProgress++
		case "message":
			summary.Message++
		default:
			summary.Unknown++
		}
	}
	for _, frame := range raw {
		var value any
		if json.Unmarshal(frame, &value) != nil {
			summary.Unknown++
			continue
		}
		scanSearchMarkers(value, &summary)
	}
	summary.SourceAttributionObserved = summary.SourceAttributionMarkers > 0
	return summary
}

func scanSearchMarkers(value any, summary *SearchEventSummary) {
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			scanSearchMarkers(child, summary)
		}
	case map[string]any:
		for key, child := range node {
			normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(key))
			switch normalized {
			case "sourceattribution", "sourceattributions":
				if nonEmptySearchMarker(child) {
					summary.SourceAttributionMarkers++
				}
			case "contenttype":
				if contentType, ok := child.(string); ok && contentType == "SearchResults" {
					summary.SearchResultMarkers++
				}
			}
			scanSearchMarkers(child, summary)
		}
	}
}

func nonEmptySearchMarker(value any) bool {
	switch marker := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(marker) != ""
	case []any:
		return len(marker) > 0
	case map[string]any:
		return len(marker) > 0
	case bool:
		return marker
	default:
		return true
	}
}
