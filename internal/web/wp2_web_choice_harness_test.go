package web

import (
	"errors"

	"m365-native/internal/evidence"
	offlineevidence "m365-native/internal/evidence/offline"
)

func CaptureWP2WebChoice(id string, raw []byte, binding evidence.CaptureBinding) (evidence.CapturedWebChoice, error) {
	route, ok := builtInRoute(id)
	if !ok || route.ID != route.CanonicalRoute || route.WebLabel == "" ||
		(route.Kind != routeKindWebMode && route.Kind != routeKindWebModel) {
		return evidence.CapturedWebChoice{}, errors.New("route is not a primary M365 web choice")
	}
	return offlineevidence.CaptureWebChoice(raw, evidence.WebChoiceRoute{
		WebChoiceID:    route.ID,
		CanonicalRoute: route.CanonicalRoute,
		RegistryTone:   route.Tone,
		RouteKind:      string(route.Kind),
		IdentityStatus: string(route.IdentityStatus),
	}, binding)
}
