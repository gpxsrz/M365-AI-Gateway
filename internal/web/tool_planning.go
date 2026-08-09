package web

import "strings"

func toolPlanningMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		return "router"
	}
	return mode
}
