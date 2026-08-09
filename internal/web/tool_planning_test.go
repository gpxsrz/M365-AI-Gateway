package web

import "testing"

func TestToolPlanningModeDefaultsToRouter(t *testing.T) {
	for _, raw := range []string{"", "router", "ROUTER"} {
		if got := toolPlanningMode(raw); got != "router" {
			t.Fatalf("toolPlanningMode(%q)=%q, want router", raw, got)
		}
	}
}

func TestToolPlanningModePreservesUnknownForValidation(t *testing.T) {
	if got := toolPlanningMode(" unexpected "); got != "unexpected" {
		t.Fatalf("toolPlanningMode(unexpected)=%q, want unexpected", got)
	}
}

func TestToolPlanningModeAcceptsNative(t *testing.T) {
	if got := toolPlanningMode(" native "); got != "native" {
		t.Fatalf("toolPlanningMode(native)=%q, want native", got)
	}
}
