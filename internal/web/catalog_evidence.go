package web

import (
	"fmt"
	"strings"

	"m365-native/internal/evidence"
)

type catalogEvidenceProjection struct {
	validated  evidence.ValidatedCatalogProjection
	identities map[string]evidence.CatalogProjectionIdentityEvidenceV1
}

func bindCatalogEvidence(cfg runtimeSettings, validated evidence.ValidatedCatalogProjection) (*catalogEvidenceProjection, error) {
	routes := routeRegistry(cfg.ModelMappings)
	registry := make(map[string]routeDefinition, len(routes))
	for _, route := range routes {
		registry[strings.ToLower(route.ID)] = route
	}

	identities := make(map[string]evidence.CatalogProjectionIdentityEvidenceV1, len(validated.Manifest.Identities))
	for _, accepted := range validated.Manifest.Identities {
		key := strings.ToLower(accepted.RequestedIdentity)
		route, ok := registry[key]
		if !ok || matchCatalogEvidenceIdentity(route, accepted) != nil {
			continue
		}
		identities[key] = accepted
	}

	return &catalogEvidenceProjection{
		validated:  validated,
		identities: identities,
	}, nil
}

func (projection *catalogEvidenceProjection) forSettings(cfg runtimeSettings) *catalogEvidenceProjection {
	if projection == nil {
		return nil
	}
	rebound, err := bindCatalogEvidence(cfg, projection.validated)
	if err != nil {
		return nil
	}
	return rebound
}

func matchCatalogEvidenceIdentity(route routeDefinition, accepted evidence.CatalogProjectionIdentityEvidenceV1) error {
	if route.ID != accepted.RequestedIdentity ||
		route.CanonicalRoute != accepted.CanonicalRoute ||
		route.Tone != accepted.ResolvedTone ||
		string(route.Kind) != accepted.RouteKind ||
		string(route.CatalogVisibility) != accepted.CatalogVisibility ||
		route.CompatibilityRequired != accepted.CompatibilityRequired ||
		string(route.IdentityStatus) != accepted.IdentityStatus {
		return fmt.Errorf("catalog evidence identity %q does not match the canonical route registry", accepted.RequestedIdentity)
	}

	switch accepted.PackageIssue {
	case 4:
		if route.Kind != routeKindWebMode && route.Kind != routeKindWebModel {
			return fmt.Errorf("catalog evidence identity %q has an invalid route-protocol package scope", accepted.RequestedIdentity)
		}
	case 5:
		if route.Kind != routeKindAlias && route.Kind != routeKindPreset {
			return fmt.Errorf("catalog evidence identity %q has an invalid alias package scope", accepted.RequestedIdentity)
		}
	case 6:
		if route.Kind != routeKindLegacyDirect && route.Kind != routeKindConfigured {
			return fmt.Errorf("catalog evidence identity %q has an invalid legacy/configured package scope", accepted.RequestedIdentity)
		}
	default:
		return fmt.Errorf("catalog evidence identity %q has an unsupported package scope", accepted.RequestedIdentity)
	}

	switch accepted.MappingEvidence {
	case "web_payload_verified":
		if route.MappingEvidence != mappingAPIToneAccepted {
			return fmt.Errorf("catalog evidence identity %q cannot promote an unverified registry mapping to web mapping", accepted.RequestedIdentity)
		}
	default:
		if string(route.MappingEvidence) != accepted.MappingEvidence {
			return fmt.Errorf("catalog evidence identity %q mapping evidence does not match the registry", accepted.RequestedIdentity)
		}
	}
	return nil
}

func (projection *catalogEvidenceProjection) apply(model map[string]any, spec modelSpec) {
	if projection == nil {
		return
	}
	identity, ok := projection.identities[strings.ToLower(spec.ID)]
	if !ok {
		return
	}

	model["x_m365_evidence_source"] = "accepted_evidence"
	model["x_m365_identity_evidence_sha256"] = identity.CapabilityEvidenceSetSHA256
	if identity.CatalogObservationSHA256 != "" {
		model["x_m365_catalog_observation_sha256"] = identity.CatalogObservationSHA256
	}
	model["x_m365_accepted_mapping_evidence"] = identity.MappingEvidence
	if identity.MappingEvidence == "web_payload_verified" {
		model["x_m365_mapping_source"] = "web_mapping"
	}
}
