package web

import (
	"reflect"
	"strings"
	"testing"

	"m365-native/internal/evidence"
)

func wp2CaptureBinding() evidence.CaptureBinding {
	return evidence.CaptureBinding{
		NormativeADRSHA256:      "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a",
		SourceHead:              "1a92a96ea940882b897a9dec17bc38c9f33e8586",
		DirtyContentSHA256:      strings.Repeat("1", 64),
		BinarySHA256:            strings.Repeat("2", 64),
		HarnessSHA256:           strings.Repeat("3", 64),
		AccountProfileRef:       "acct_0123456789abcdef0123456789abcdef",
		EffectiveSettingsSHA256: strings.Repeat("5", 64),
	}
}

func TestCaptureWP2WebChoiceUsesFivePrimaryRegistryRoutes(t *testing.T) {
	before := append([]routeDefinition(nil), builtInRouteRegistry...)
	tests := []struct {
		id           string
		observedTone string
		registryTone string
		routeKind    routeKind
	}{
		{"m365-auto", "Magic", "Magic", routeKindWebMode},
		{"quick", "Chat", "Chat", routeKindWebMode},
		{"think-deeper", "Reasoning", "Reasoning", routeKindWebMode},
		{"m365-gpt-5.6-think-deeper", "Gpt_5_6_Reasoning", "Gpt_5_6_Reasoning", routeKindWebModel},
		{"m365-gpt-5.5-quick-response", "Gpt_5_5_Chat", "Gpt_5_5_Chat", routeKindWebModel},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			raw := []byte(`{"schema":"m365-wp2-web-choice-capture/v1","tone":"` + tt.observedTone + `"}`)
			got, err := CaptureWP2WebChoice(tt.id, raw, wp2CaptureBinding())
			if err != nil {
				t.Fatalf("CaptureWP2WebChoice() error = %v", err)
			}
			route, ok := builtInRoute(tt.id)
			if !ok {
				t.Fatal("expected built-in route")
			}
			if route.ID != route.CanonicalRoute || route.WebLabel == "" || route.Kind != tt.routeKind || route.Tone != tt.registryTone {
				t.Fatalf("registry route = %#v", route)
			}
			if got.Observation.WebChoiceID != route.ID ||
				got.Observation.CanonicalRoute != route.CanonicalRoute ||
				got.Observation.RegistryTone != route.Tone ||
				got.Observation.RouteKind != string(route.Kind) ||
				got.Observation.ObservedWebTone != tt.observedTone ||
				got.Evidence.Manifest.IdentityStatus != string(route.IdentityStatus) {
				t.Fatalf("capture = %#v route = %#v", got, route)
			}
		})
	}

	if !reflect.DeepEqual(before, builtInRouteRegistry) {
		t.Fatal("web-choice capture mutated the route registry")
	}
}

func TestCaptureWP2WebChoiceRejectsAliasesAndNonWebRoutes(t *testing.T) {
	for _, id := range []string{"m365-copilot", "gpt-5.6-reasoning", "gpt-5.5", "gpt-5.4", "claude-sonnet", "missing"} {
		t.Run(id, func(t *testing.T) {
			_, err := CaptureWP2WebChoice(id, []byte(`{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic"}`), wp2CaptureBinding())
			if err == nil {
				t.Fatal("non-primary web choice accepted")
			}
		})
	}

	const rejectedID = "person@example.com"
	_, err := CaptureWP2WebChoice(rejectedID, []byte(`{"schema":"m365-wp2-web-choice-capture/v1","tone":"Magic"}`), wp2CaptureBinding())
	if err == nil {
		t.Fatal("identity-bearing route id accepted")
	}
	if strings.Contains(err.Error(), rejectedID) {
		t.Fatalf("adapter error exposed rejected route id: %v", err)
	}
}
