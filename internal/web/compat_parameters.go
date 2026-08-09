package web

import (
	"net/http"
	"sort"
	"strings"
)

func ignoredOpenAICompatibilityParameters(body oaiReq) []string {
	present := map[string]bool{
		"temperature":           len(body.Temperature) > 0,
		"top_p":                 len(body.TopP) > 0,
		"max_tokens":            len(body.MaxTokens) > 0,
		"max_completion_tokens": len(body.MaxCompletionTokens) > 0,
		"stop":                  len(body.Stop) > 0,
		"seed":                  len(body.Seed) > 0,
		"frequency_penalty":     len(body.FrequencyPenalty) > 0,
		"presence_penalty":      len(body.PresencePenalty) > 0,
	}
	names := make([]string, 0, len(present))
	for name, yes := range present {
		if yes {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func setIgnoredParameters(w http.ResponseWriter, names []string) {
	if len(names) == 0 {
		return
	}
	const header = "X-M365-Ignored-Parameters"
	w.Header().Set(header, strings.Join(names, ","))
	exposeCompatibilityHeader(w, header)
}

func setStreamingSemantics(w http.ResponseWriter, value string) {
	const header = "X-M365-Streaming-Semantics"
	w.Header().Set(header, value)
	exposeCompatibilityHeader(w, header)
}

func exposeCompatibilityHeader(w http.ResponseWriter, header string) {
	exposed := map[string]struct{}{header: {}}
	for _, value := range strings.Split(w.Header().Get("Access-Control-Expose-Headers"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			exposed[value] = struct{}{}
		}
	}
	values := make([]string, 0, len(exposed))
	for value := range exposed {
		values = append(values, value)
	}
	sort.Strings(values)
	w.Header().Set("Access-Control-Expose-Headers", strings.Join(values, ", "))
}
