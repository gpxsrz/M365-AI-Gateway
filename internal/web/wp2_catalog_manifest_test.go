package web

import (
	"bytes"
	"path/filepath"
	"runtime"
	"testing"

	committedevidence "m365-native/docs/wp2/evidence"
)

func TestBuildAcceptedWP2CatalogProjectionFromCommittedArtifacts(t *testing.T) {
	root := wp2CatalogTestRepoRoot(t)
	first, expected, err := BuildAcceptedWP2CatalogProjection(root)
	if err != nil {
		t.Fatal(err)
	}
	second, secondExpected, err := BuildAcceptedWP2CatalogProjection(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || expected.ManifestSHA256 != secondExpected.ManifestSHA256 {
		t.Fatal("catalog projection generation is not deterministic")
	}

	validated, err := validateAndBindAcceptedWP2CatalogProjection(defaultRuntimeSettings(), first, expected)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.validated.Manifest.Identities) != 20 {
		t.Fatalf("identity count=%d", len(validated.validated.Manifest.Identities))
	}
	if len(validated.validated.Manifest.GlobalClaims) != 12 {
		t.Fatalf("global claim count=%d", len(validated.validated.Manifest.GlobalClaims))
	}
	if _, ok := validated.validated.IdentityEvidence("existing-microsoft-route"); ok {
		t.Fatal("synthetic configured mapping became runtime catalog evidence")
	}
	if _, ok := validated.validated.IdentityEvidence("existing-claude-route"); ok {
		t.Fatal("synthetic configured mapping became runtime catalog evidence")
	}

	dependent := 0
	for _, claim := range validated.validated.Manifest.GlobalClaims {
		if !claim.AccountDependent {
			continue
		}
		dependent++
		if claim.CanonicalRoute != "m365-gpt-5.6-think-deeper" || claim.Protocol != "openai_responses_nonstream" || claim.RouteEligibility != "INCONCLUSIVE" {
			t.Fatalf("unexpected account-dependent claim=%#v", claim)
		}
	}
	if dependent != 1 {
		t.Fatalf("account-dependent claims=%d", dependent)
	}
}

func TestEmbeddedAcceptedWP2CatalogArtifactsMatchRepositoryArtifacts(t *testing.T) {
	repositoryRaw, repositoryExpected, err := BuildAcceptedWP2CatalogProjection(wp2CatalogTestRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	embeddedRaw, embeddedExpected, err := BuildAcceptedWP2CatalogProjectionFromFS(committedevidence.AcceptedArtifacts(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repositoryRaw, embeddedRaw) || repositoryExpected.ManifestSHA256 != embeddedExpected.ManifestSHA256 {
		t.Fatal("embedded accepted evidence does not reproduce the repository catalog manifest")
	}
}

func TestAcceptedWP2CatalogProjectionSkipsRuntimeRegistryDriftPerRoute(t *testing.T) {
	raw, expected, err := BuildAcceptedWP2CatalogProjection(wp2CatalogTestRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultRuntimeSettings()
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel: "gpt-5.2", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Local override",
	})
	projection, err := validateAndBindAcceptedWP2CatalogProjection(cfg, raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	catalog := modelCatalogForSettingsAndEvidence(cfg, projection)
	overridden := catalogModelMapByID(t, catalog, "gpt-5.2")
	if overridden["route_kind"] != routeKindConfigured || overridden["x_m365_evidence_source"] != "none" {
		t.Fatalf("overridden route inherited stale evidence: %#v", overridden)
	}
	canonical := catalogModelMapByID(t, catalog, "m365-gpt-5.6-think-deeper")
	if canonical["x_m365_evidence_source"] != "accepted_evidence" {
		t.Fatalf("unrelated accepted evidence disappeared: %#v", canonical)
	}
}

func catalogModelMapByID(t *testing.T, catalog []map[string]any, id string) map[string]any {
	t.Helper()
	for _, model := range catalog {
		if model["id"] == id {
			return model
		}
	}
	t.Fatalf("model %q not found", id)
	return nil
}

func wp2CatalogTestRepoRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
