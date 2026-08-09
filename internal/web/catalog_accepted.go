package web

import (
	"sync"

	committedevidence "m365-native/docs/wp2/evidence"
	"m365-native/internal/evidence"
)

var acceptedWP2CatalogOnce struct {
	sync.Once
	validated evidence.ValidatedCatalogProjection
	err       error
}

func defaultAcceptedWP2CatalogProjection(cfg runtimeSettings) (*catalogEvidenceProjection, error) {
	acceptedWP2CatalogOnce.Do(func() {
		raw, expected, err := BuildAcceptedWP2CatalogProjectionFromFS(committedevidence.AcceptedArtifacts(), ".")
		if err != nil {
			acceptedWP2CatalogOnce.err = err
			return
		}
		acceptedWP2CatalogOnce.validated, acceptedWP2CatalogOnce.err = evidence.ValidateCatalogProjectionManifest(raw, expected)
	})
	if acceptedWP2CatalogOnce.err != nil {
		return nil, acceptedWP2CatalogOnce.err
	}
	return bindCatalogEvidence(cfg, acceptedWP2CatalogOnce.validated)
}
