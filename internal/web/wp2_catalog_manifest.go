package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"m365-native/internal/evidence"
)

type wp2CommittedPackageSpec struct {
	Package       evidence.CatalogProjectionPackageV1
	IndexSchema   string
	PayloadSchema string
}

type wp2CommittedEvidenceIndex struct {
	Schema  string `json:"schema"`
	Payload struct {
		Path         string `json:"path"`
		Encoding     string `json:"encoding"`
		JSONSchema   string `json:"json_schema"`
		JSONSHA256   string `json:"json_sha256"`
		JSONBytes    int    `json:"json_bytes"`
		GzipSHA256   string `json:"gzip_sha256"`
		GzipBytes    int    `json:"gzip_bytes"`
		Base64SHA256 string `json:"base64_sha256"`
		Base64Bytes  int    `json:"base64_bytes"`
		JSON         struct {
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"json"`
		Gzip struct {
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"gzip"`
		Base64 struct {
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		} `json:"base64"`
	} `json:"payload"`
	Records                     int    `json:"records"`
	ProfileSetSHA256            string `json:"profile_set_sha256"`
	MatrixEntries               int    `json:"matrix_entries"`
	AcceptedCapabilityManifests int    `json:"accepted_capability_manifests"`
	GlobalClaims                int    `json:"global_claims"`
}

type wp2LoadedCommittedPackage struct {
	Spec       wp2CommittedPackageSpec
	Index      wp2CommittedEvidenceIndex
	IndexRaw   []byte
	PayloadRaw []byte
}

type wp2CommittedCapabilityRecord struct {
	CapabilityID   string
	Evidence       json.RawMessage
	EvidenceSHA256 string
}

type wp2CatalogIdentityAccumulator struct {
	Route                 routeDefinition
	PackageIssue          int
	CatalogObservationSHA string
	MappingEvidence       string
	IdentityStatus        string
	SupportingEvidenceSHA map[string]struct{}
}

func acceptedWP2CatalogPackageSpecs() []wp2CommittedPackageSpec {
	const normativeADR = "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a"
	return []wp2CommittedPackageSpec{
		{
			Package: evidence.CatalogProjectionPackageV1{
				Issue: 4, Kind: "route_protocol", NormativeADRSHA256: normativeADR,
				SourceHead:              "84a8ed3e3632a1027af5ea5d7b62a9a89e3f3b70",
				BinarySHA256:            "46b6f9513c427a03e2d04913cb740e072bbcfee671a0504f00d10f40d1e95be9",
				HarnessSHA256:           "8b25426ad1773b6c1a1c28bfdc4cebfa2d26ad8d2c2bbe679fe110b3ffe1d384",
				EffectiveSettingsSHA256: "18ba161dc4667fb3251c4a315884fcbe125f4d2dee128ca86f0348795690c478",
				EvidenceIndexSHA256:     "5bcc0d1274f45c06a5197b71dc580f37eb24bf248e9a899a6f78e0124c8511ad",
				PayloadJSONSHA256:       "f7f80bfbd54972afce4eec8ecb8ee9e598013e48ee6f24f50cc726d1c0718546",
			},
			IndexSchema:   "m365-wp2-route-protocol-evidence-index/v1",
			PayloadSchema: evidence.RouteProtocolEvidenceSetSchemaV1,
		},
		{
			Package: evidence.CatalogProjectionPackageV1{
				Issue: 5, Kind: "alias_projection", NormativeADRSHA256: normativeADR,
				SourceHead:              "db6b62a0e53eb83804c79a2571fc37b7bb60a53a",
				BinarySHA256:            "f0b4fa9655d51852169517cf9c11538bfd7b424937243e5fd21c89844b07f897",
				HarnessSHA256:           "72000e7b509895e779afb13ad5ea5f02e1751109ba12780fd467e3029697262d",
				EffectiveSettingsSHA256: "1a409bce9df6e746dd127620b0e4c3ac9000e08ef24f9d515ba91140d46374dd",
				EvidenceIndexSHA256:     "2ad92950de6778f7264766b33667ab7c391331c943620923c3b2cfb1cda10fc5",
				PayloadJSONSHA256:       "b08bf0d014ad116b4ad22e83aea120d7e098afc4ef81ff79e6e608df69f6892e",
			},
			IndexSchema:   "m365-wp2-alias-projection-evidence-index/v1",
			PayloadSchema: evidence.AliasProjectionEvidenceSetSchemaV1,
		},
		{
			Package: evidence.CatalogProjectionPackageV1{
				Issue: 6, Kind: "legacy_configured", NormativeADRSHA256: normativeADR,
				SourceHead:              "7649d077ede2a1baac450e2bbeb8dd223c6b81ac",
				BinarySHA256:            "1ddbdebf48d44ebe4798246be155fe067c5b80c3c9e3aef2d95b0c5a0a13ff28",
				HarnessSHA256:           "a542952afe0758874d36a7c3165afe762a819be58cf161221bca4d954c3cb30e",
				EffectiveSettingsSHA256: "7e34f393070b131e1287288fb319df411dfb457927825751dc3db5901f1bc672",
				EvidenceIndexSHA256:     "e7eec91af6752e8246bf33c06530829e0228fdf34ea1b5dade8a2eca5e6da80b",
				PayloadJSONSHA256:       "b9022595697b4db99c16573ded7a39e763e79ac5ed247ef37491da42152adf1b",
			},
			IndexSchema:   "m365-wp2-legacy-configured-evidence-index/v1",
			PayloadSchema: evidence.LegacyConfiguredEvidenceSetSchemaV1,
		},
		{
			Package: evidence.CatalogProjectionPackageV1{
				Issue: 7, Kind: "account_pool", NormativeADRSHA256: normativeADR,
				SourceHead:              "16f2fa8c1c2ede297882a707083b9f8d3ba7548f",
				BinarySHA256:            "5f79bb591276ae28dc24cd3f590b454ad10d7531e6666871998b82bbb8a1e9c9",
				HarnessSHA256:           "b199a02cf72f81a0ec6c92d3e4765cf666220521a5cdc0f1ed1406350558037b",
				EffectiveSettingsSHA256: "2e75210b9063ff39605456ce799ce6fb2b2c68ae63444865ecc2800dfebf0d52",
				EvidenceIndexSHA256:     "74d5839841b32955c941419a210e85f8ee46a341ddcad848150cab1bc3e43d95",
				PayloadJSONSHA256:       "505b3bfd790b91d89a550dd86cc9800f258b0b7e86b8a597ee6828ac3fcefba2",
				ProfileSetSHA256:        "91145a2da73857f1dfa6b716c0cd462b2039fde9fd9881b8faae513c17f10c2b",
			},
			IndexSchema:   "m365-wp2-account-pool-evidence-index/v1",
			PayloadSchema: evidence.AccountPoolEvidenceSetSchemaV1,
		},
	}
}

func BuildAcceptedWP2CatalogProjection(repoRoot string) ([]byte, evidence.CatalogProjectionExpected, error) {
	return BuildAcceptedWP2CatalogProjectionFromFS(os.DirFS(repoRoot), "docs/wp2/evidence")
}

func BuildAcceptedWP2CatalogProjectionFromFS(artifactFS fs.FS, basePath string) ([]byte, evidence.CatalogProjectionExpected, error) {
	specs := acceptedWP2CatalogPackageSpecs()
	loaded := make([]wp2LoadedCommittedPackage, 0, len(specs))
	for _, spec := range specs {
		pkg, err := loadWP2CommittedPackage(artifactFS, basePath, spec)
		if err != nil {
			return nil, evidence.CatalogProjectionExpected{}, err
		}
		loaded = append(loaded, pkg)
	}

	registry := make(map[string]routeDefinition, len(builtInRouteRegistry))
	for _, route := range wp2AcceptedRouteRegistry() {
		registry[strings.ToLower(route.ID)] = route
	}
	identities := make(map[string]*wp2CatalogIdentityAccumulator)
	if err := collectRouteProtocolCatalogEvidence(loaded[0], registry, identities); err != nil {
		return nil, evidence.CatalogProjectionExpected{}, err
	}
	if err := collectAliasCatalogEvidence(loaded[1], registry, identities); err != nil {
		return nil, evidence.CatalogProjectionExpected{}, err
	}
	if err := collectLegacyCatalogEvidence(loaded[2], registry, identities); err != nil {
		return nil, evidence.CatalogProjectionExpected{}, err
	}
	globalClaims, err := validateAccountPoolCatalogEvidence(loaded[3])
	if err != nil {
		return nil, evidence.CatalogProjectionExpected{}, err
	}

	manifestIdentities, err := finalizeWP2CatalogIdentities(identities)
	if err != nil {
		return nil, evidence.CatalogProjectionExpected{}, err
	}
	packages := make([]evidence.CatalogProjectionPackageV1, 0, len(specs))
	for _, spec := range specs {
		packages = append(packages, spec.Package)
	}
	manifest := evidence.CatalogProjectionManifestV1{
		Schema:           evidence.CatalogProjectionManifestSchemaV1,
		AcceptanceStatus: evidence.CatalogProjectionAccepted,
		Packages:         packages,
		Identities:       manifestIdentities,
		GlobalClaims:     globalClaims,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, evidence.CatalogProjectionExpected{}, fmt.Errorf("marshal WP2 catalog projection: %w", err)
	}
	expected := evidence.CatalogProjectionExpected{
		ManifestSHA256: wp2SHA256Hex(raw),
		Packages:       append([]evidence.CatalogProjectionPackageV1(nil), packages...),
	}
	if _, err := evidence.ValidateCatalogProjectionManifest(raw, expected); err != nil {
		return nil, evidence.CatalogProjectionExpected{}, fmt.Errorf("validate generated WP2 catalog projection: %w", err)
	}
	return raw, expected, nil
}

// wp2AcceptedRouteRegistry preserves the exact runtime identities that the
// immutable WP2 packages attested. WP6 intentionally changed three Web route
// mappings after newer live evidence; rebuilding historical manifests must not
// silently reinterpret those already-accepted bytes as current runtime state.
func wp2AcceptedRouteRegistry() []routeDefinition {
	routes := routeRegistry(nil)
	for i := range routes {
		switch routes[i].ID {
		case "m365-auto", "m365-copilot":
			routes[i].Tone = "magic"
		case "quick":
			routes[i].Tone = "Gpt_Quick"
			routes[i].OperationalStatus = operationalDisabled
			routes[i].MappingEvidence = mappingUnverified
		case "think-deeper":
			routes[i].Tone = "Gpt_Reasoning"
			routes[i].OperationalStatus = operationalDisabled
			routes[i].MappingEvidence = mappingUnverified
		}
	}
	return routes
}

func loadWP2CommittedPackage(artifactFS fs.FS, basePath string, spec wp2CommittedPackageSpec) (wp2LoadedCommittedPackage, error) {
	directory := path.Join(basePath, fmt.Sprintf("issue-%d", spec.Package.Issue))
	indexPath := path.Join(directory, "evidence-index.json")
	indexRaw, err := fs.ReadFile(artifactFS, indexPath)
	if err != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("read issue %d evidence index: %w", spec.Package.Issue, err)
	}
	if got := wp2SHA256Hex(indexRaw); got != spec.Package.EvidenceIndexSHA256 {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d evidence index SHA-256 mismatch: got %s", spec.Package.Issue, got)
	}
	var index wp2CommittedEvidenceIndex
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("decode issue %d evidence index: %w", spec.Package.Issue, err)
	}
	if index.Payload.JSONSHA256 == "" {
		index.Payload.JSONSHA256 = index.Payload.JSON.SHA256
		index.Payload.JSONBytes = index.Payload.JSON.Bytes
		index.Payload.GzipSHA256 = index.Payload.Gzip.SHA256
		index.Payload.GzipBytes = index.Payload.Gzip.Bytes
		index.Payload.Base64SHA256 = index.Payload.Base64.SHA256
		index.Payload.Base64Bytes = index.Payload.Base64.Bytes
	}
	if index.Schema != spec.IndexSchema || index.Payload.JSONSchema != spec.PayloadSchema {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d evidence schema mismatch", spec.Package.Issue)
	}
	if index.Payload.Path != "evidence-set.json.gz.b64" || !strings.HasPrefix(index.Payload.Encoding, "base64(gzip") {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d unsupported evidence payload encoding", spec.Package.Issue)
	}
	if index.Payload.JSONSHA256 != spec.Package.PayloadJSONSHA256 {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d evidence index does not bind the accepted raw payload", spec.Package.Issue)
	}

	base64Raw, err := fs.ReadFile(artifactFS, path.Join(directory, index.Payload.Path))
	if err != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("read issue %d evidence payload: %w", spec.Package.Issue, err)
	}
	if len(base64Raw) != index.Payload.Base64Bytes || wp2SHA256Hex(base64Raw) != index.Payload.Base64SHA256 {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d base64 payload identity mismatch", spec.Package.Issue)
	}
	encoded := bytes.Join(bytes.Fields(base64Raw), nil)
	gzipRaw, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("decode issue %d base64 payload: %w", spec.Package.Issue, err)
	}
	if len(gzipRaw) != index.Payload.GzipBytes || wp2SHA256Hex(gzipRaw) != index.Payload.GzipSHA256 {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d gzip payload identity mismatch", spec.Package.Issue)
	}
	reader, err := gzip.NewReader(bytes.NewReader(gzipRaw))
	if err != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("open issue %d gzip payload: %w", spec.Package.Issue, err)
	}
	payloadRaw, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("read issue %d gzip payload: %w", spec.Package.Issue, readErr)
	}
	if closeErr != nil {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("close issue %d gzip payload: %w", spec.Package.Issue, closeErr)
	}
	if len(payloadRaw) != index.Payload.JSONBytes || wp2SHA256Hex(payloadRaw) != index.Payload.JSONSHA256 {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d raw payload identity mismatch", spec.Package.Issue)
	}
	var schema struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(payloadRaw, &schema); err != nil || schema.Schema != spec.PayloadSchema {
		return wp2LoadedCommittedPackage{}, fmt.Errorf("issue %d raw payload schema mismatch", spec.Package.Issue)
	}
	return wp2LoadedCommittedPackage{Spec: spec, Index: index, IndexRaw: indexRaw, PayloadRaw: payloadRaw}, nil
}

func collectRouteProtocolCatalogEvidence(pkg wp2LoadedCommittedPackage, registry map[string]routeDefinition, identities map[string]*wp2CatalogIdentityAccumulator) error {
	var set evidence.RouteProtocolEvidenceSetV1
	if err := json.Unmarshal(pkg.PayloadRaw, &set); err != nil {
		return fmt.Errorf("decode issue 4 route-protocol payload: %w", err)
	}
	if set.Schema != pkg.Spec.PayloadSchema || len(set.Records) != pkg.Index.Records {
		return fmt.Errorf("issue 4 route-protocol record identity mismatch")
	}
	for _, record := range set.Records {
		var observation evidence.RouteProtocolObservationV1
		if err := json.Unmarshal(record.ObservationJSON, &observation); err != nil {
			return fmt.Errorf("decode issue 4 observation: %w", err)
		}
		manifests, err := validateWP2CommittedRecord(pkg.Spec.Package, record.ObservationJSON, record.ObservationSHA256, observation.CanonicalRoute, observation.ResolvedTone, observation.Protocol, routeProtocolCapabilityRecords(record.Capabilities))
		if err != nil {
			return err
		}
		if observation.CaseID != evidence.RouteProtocolCaseSuccess {
			continue
		}
		route, ok := registry[strings.ToLower(observation.RequestedModel)]
		if !ok || (route.Kind != routeKindWebMode && route.Kind != routeKindWebModel) || route.ID != observation.CanonicalRoute || route.Tone != observation.ResolvedTone {
			continue
		}
		manifest, checksum, ok := verifiedRouteIdentityManifest(manifests)
		if !ok {
			return fmt.Errorf("issue 4 route %q has no verified route-identity manifest", route.ID)
		}
		if err := addWP2CatalogIdentityEvidence(identities, route, 4, "", manifest, checksum); err != nil {
			return err
		}
	}
	return nil
}

func collectAliasCatalogEvidence(pkg wp2LoadedCommittedPackage, registry map[string]routeDefinition, identities map[string]*wp2CatalogIdentityAccumulator) error {
	var set evidence.AliasProjectionEvidenceSetV1
	if err := json.Unmarshal(pkg.PayloadRaw, &set); err != nil {
		return fmt.Errorf("decode issue 5 alias payload: %w", err)
	}
	if set.Schema != pkg.Spec.PayloadSchema || len(set.Records) != pkg.Index.Records {
		return fmt.Errorf("issue 5 alias record identity mismatch")
	}
	catalog := make(map[string]string, len(set.Catalog))
	for _, entry := range set.Catalog {
		route, ok := registry[strings.ToLower(entry.RequestedIdentity)]
		if !ok || (route.Kind != routeKindAlias && route.Kind != routeKindPreset) ||
			route.CanonicalRoute != entry.CanonicalRoute || string(route.Kind) != entry.RouteKind ||
			string(route.CatalogVisibility) != entry.CatalogVisibility || route.CompatibilityRequired != entry.CompatibilityRequired ||
			(entry.Listed != (route.CatalogVisibility != catalogHidden)) {
			return fmt.Errorf("issue 5 catalog observation %q does not match the canonical registry", entry.RequestedIdentity)
		}
		catalog[strings.ToLower(entry.RequestedIdentity)] = entry.ObservationSHA256
	}
	catalogSeen := make(map[string]struct{}, len(catalog))
	for _, record := range set.Records {
		var observation evidence.AliasProjectionObservationV1
		if err := json.Unmarshal(record.ObservationJSON, &observation); err != nil {
			return fmt.Errorf("decode issue 5 observation: %w", err)
		}
		manifests, err := validateWP2CommittedRecord(pkg.Spec.Package, record.ObservationJSON, record.ObservationSHA256, observation.CanonicalRoute, observation.ResolvedTone, observation.Protocol, aliasCapabilityRecords(record.Capabilities))
		if err != nil {
			return err
		}
		key := strings.ToLower(observation.RequestedIdentity)
		if observation.CaseID == evidence.AliasProjectionCaseCatalog {
			if catalog[key] != record.ObservationSHA256 {
				return fmt.Errorf("issue 5 catalog observation hash mismatch for %q", observation.RequestedIdentity)
			}
			catalogSeen[key] = struct{}{}
			continue
		}
		if observation.CaseID != evidence.AliasProjectionCaseSuccess {
			continue
		}
		route, ok := registry[key]
		if !ok || (route.Kind != routeKindAlias && route.Kind != routeKindPreset) || route.CanonicalRoute != observation.CanonicalRoute || route.Tone != observation.ResolvedTone {
			continue
		}
		manifest, checksum, ok := verifiedRouteIdentityManifest(manifests)
		if !ok {
			return fmt.Errorf("issue 5 identity %q has no verified route-identity manifest", route.ID)
		}
		if err := addWP2CatalogIdentityEvidence(identities, route, 5, catalog[key], manifest, checksum); err != nil {
			return err
		}
	}
	if len(catalogSeen) != len(catalog) {
		return fmt.Errorf("issue 5 catalog observations are incomplete")
	}
	return nil
}

func collectLegacyCatalogEvidence(pkg wp2LoadedCommittedPackage, registry map[string]routeDefinition, identities map[string]*wp2CatalogIdentityAccumulator) error {
	var set evidence.LegacyConfiguredEvidenceSetV1
	if err := json.Unmarshal(pkg.PayloadRaw, &set); err != nil {
		return fmt.Errorf("decode issue 6 legacy/configured payload: %w", err)
	}
	if set.Schema != pkg.Spec.PayloadSchema || len(set.Records) != pkg.Index.Records {
		return fmt.Errorf("issue 6 legacy/configured record identity mismatch")
	}
	catalog := make(map[string]string)
	for _, entry := range set.Catalog {
		route, ok := registry[strings.ToLower(entry.RequestedModel)]
		if entry.ConfiguredMapping {
			continue
		}
		if !ok || route.Kind != routeKindLegacyDirect || route.CanonicalRoute != entry.CanonicalRoute || route.Tone != entry.ResolvedTone ||
			string(route.Kind) != entry.RouteKind || string(route.CatalogVisibility) != entry.CatalogVisibility || !entry.Listed {
			return fmt.Errorf("issue 6 catalog observation %q does not match the canonical registry", entry.RequestedModel)
		}
		catalog[strings.ToLower(entry.RequestedModel)] = entry.ObservationSHA256
	}
	catalogSeen := make(map[string]struct{}, len(catalog))
	for _, record := range set.Records {
		var observation evidence.LegacyConfiguredObservationV1
		if err := json.Unmarshal(record.ObservationJSON, &observation); err != nil {
			return fmt.Errorf("decode issue 6 observation: %w", err)
		}
		manifests, err := validateWP2CommittedRecord(pkg.Spec.Package, record.ObservationJSON, record.ObservationSHA256, observation.CanonicalRoute, observation.ResolvedTone, observation.Protocol, legacyCapabilityRecords(record.Capabilities))
		if err != nil {
			return err
		}
		key := strings.ToLower(observation.RequestedModel)
		if observation.CaseID == evidence.LegacyConfiguredCaseCatalog {
			if expected, relevant := catalog[key]; relevant {
				if expected != record.ObservationSHA256 {
					return fmt.Errorf("issue 6 catalog observation hash mismatch for %q", observation.RequestedModel)
				}
				catalogSeen[key] = struct{}{}
			}
			continue
		}
		if observation.CaseID != evidence.LegacyConfiguredCaseSuccess {
			continue
		}
		route, ok := registry[key]
		if !ok || route.Kind != routeKindLegacyDirect || route.CanonicalRoute != observation.CanonicalRoute || route.Tone != observation.ResolvedTone {
			continue
		}
		manifest, checksum, ok := verifiedRouteIdentityManifest(manifests)
		if !ok {
			return fmt.Errorf("issue 6 identity %q has no verified route-identity manifest", route.ID)
		}
		if err := addWP2CatalogIdentityEvidence(identities, route, 6, catalog[key], manifest, checksum); err != nil {
			return err
		}
	}
	if len(catalogSeen) != len(catalog) {
		return fmt.Errorf("issue 6 legacy catalog observations are incomplete")
	}
	return nil
}

func validateAccountPoolCatalogEvidence(pkg wp2LoadedCommittedPackage) ([]evidence.AccountPoolGlobalClaimV1, error) {
	expected := evidence.AccountPoolEvidenceSetExpected{
		JSONSHA256:              pkg.Spec.Package.PayloadJSONSHA256,
		NormativeADRSHA256:      pkg.Spec.Package.NormativeADRSHA256,
		SourceHead:              pkg.Spec.Package.SourceHead,
		BinarySHA256:            pkg.Spec.Package.BinarySHA256,
		HarnessSHA256:           pkg.Spec.Package.HarnessSHA256,
		EffectiveSettingsSHA256: pkg.Spec.Package.EffectiveSettingsSHA256,
		ProfileSetSHA256:        pkg.Spec.Package.ProfileSetSHA256,
	}
	validated, err := evidence.ValidateAccountPoolEvidenceSet(pkg.PayloadRaw, expected)
	if err != nil {
		return nil, fmt.Errorf("validate issue 7 account-pool payload: %w", err)
	}
	if pkg.Index.ProfileSetSHA256 != validated.Set.ProfileSetSHA256 ||
		pkg.Index.MatrixEntries != accountPoolMatrixEntryCount(validated.Set) ||
		pkg.Index.AcceptedCapabilityManifests != accountPoolCapabilityManifestCount(validated.Set) ||
		pkg.Index.GlobalClaims != len(validated.Set.GlobalClaims) {
		return nil, fmt.Errorf("issue 7 evidence index summary does not match the validated payload")
	}
	return append([]evidence.AccountPoolGlobalClaimV1(nil), validated.Set.GlobalClaims...), nil
}

func validateWP2CommittedRecord(pkg evidence.CatalogProjectionPackageV1, observationJSON json.RawMessage, observationSHA, canonicalRoute, resolvedTone, protocol string, capabilities []wp2CommittedCapabilityRecord) (map[string]struct {
	Manifest evidence.ManifestV1
	Checksum string
}, error) {
	if wp2SHA256Hex(observationJSON) != observationSHA {
		return nil, fmt.Errorf("issue %d observation SHA-256 mismatch", pkg.Issue)
	}
	validatedCapabilities := make(map[string]struct {
		Manifest evidence.ManifestV1
		Checksum string
	}, len(capabilities))
	for _, capability := range capabilities {
		var manifest evidence.ManifestV1
		if err := json.Unmarshal(capability.Evidence, &manifest); err != nil {
			return nil, fmt.Errorf("decode issue %d capability manifest: %w", pkg.Issue, err)
		}
		if manifest.NormativeADRSHA256 != pkg.NormativeADRSHA256 || manifest.SourceHead != pkg.SourceHead || manifest.DirtyContentSHA256 != nil ||
			manifest.BinarySHA256 != pkg.BinarySHA256 || manifest.HarnessSHA256 != pkg.HarnessSHA256 ||
			manifest.EffectiveSettingsSHA256 != pkg.EffectiveSettingsSHA256 || manifest.ObservationSHA256 != observationSHA ||
			manifest.CanonicalRoute != canonicalRoute || manifest.ResolvedTone != resolvedTone || manifest.Protocol != protocol ||
			manifest.CapabilityID != capability.CapabilityID {
			return nil, fmt.Errorf("issue %d capability manifest identity mismatch", pkg.Issue)
		}
		validated, err := evidence.ValidateCapabilityEvidence(capability.Evidence, evidence.IdentitySet{
			NormativeADRSHA256:      pkg.NormativeADRSHA256,
			SourceHead:              pkg.SourceHead,
			BinarySHA256:            pkg.BinarySHA256,
			HarnessSHA256:           pkg.HarnessSHA256,
			ObservationSHA256:       observationSHA,
			CanonicalRoute:          canonicalRoute,
			ResolvedTone:            resolvedTone,
			Protocol:                protocol,
			AccountProfileRef:       manifest.AccountProfileRef,
			EffectiveSettingsSHA256: pkg.EffectiveSettingsSHA256,
		})
		if err != nil {
			return nil, fmt.Errorf("validate issue %d capability manifest: %w", pkg.Issue, err)
		}
		if validated.ChecksumSHA256 != capability.EvidenceSHA256 {
			return nil, fmt.Errorf("issue %d capability manifest checksum mismatch", pkg.Issue)
		}
		if _, duplicate := validatedCapabilities[capability.CapabilityID]; duplicate {
			return nil, fmt.Errorf("issue %d duplicate capability %q", pkg.Issue, capability.CapabilityID)
		}
		validatedCapabilities[capability.CapabilityID] = struct {
			Manifest evidence.ManifestV1
			Checksum string
		}{Manifest: manifest, Checksum: validated.ChecksumSHA256}
	}
	return validatedCapabilities, nil
}

func verifiedRouteIdentityManifest(manifests map[string]struct {
	Manifest evidence.ManifestV1
	Checksum string
}) (evidence.ManifestV1, string, bool) {
	record, ok := manifests["route_identity"]
	if !ok || record.Manifest.Classification != evidence.ClassificationVerified || record.Manifest.TestExecutionStatus != evidence.TestExecutionPass {
		return evidence.ManifestV1{}, "", false
	}
	return record.Manifest, record.Checksum, true
}

func addWP2CatalogIdentityEvidence(identities map[string]*wp2CatalogIdentityAccumulator, route routeDefinition, issue int, catalogObservation string, manifest evidence.ManifestV1, checksum string) error {
	key := strings.ToLower(route.ID)
	accumulator, exists := identities[key]
	if !exists {
		accumulator = &wp2CatalogIdentityAccumulator{
			Route: route, PackageIssue: issue, CatalogObservationSHA: catalogObservation,
			MappingEvidence: manifest.MappingEvidence, IdentityStatus: manifest.IdentityStatus,
			SupportingEvidenceSHA: make(map[string]struct{}),
		}
		identities[key] = accumulator
	}
	if accumulator.PackageIssue != issue || accumulator.Route.ID != route.ID || accumulator.CatalogObservationSHA != catalogObservation ||
		accumulator.MappingEvidence != manifest.MappingEvidence || accumulator.IdentityStatus != manifest.IdentityStatus {
		return fmt.Errorf("accepted identity evidence for %q is internally inconsistent", route.ID)
	}
	accumulator.SupportingEvidenceSHA[checksum] = struct{}{}
	return nil
}

func finalizeWP2CatalogIdentities(accumulators map[string]*wp2CatalogIdentityAccumulator) ([]evidence.CatalogProjectionIdentityEvidenceV1, error) {
	keys := make([]string, 0, len(accumulators))
	for key := range accumulators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	identities := make([]evidence.CatalogProjectionIdentityEvidenceV1, 0, len(keys))
	for _, key := range keys {
		accumulator := accumulators[key]
		hashes := make([]string, 0, len(accumulator.SupportingEvidenceSHA))
		for checksum := range accumulator.SupportingEvidenceSHA {
			hashes = append(hashes, checksum)
		}
		sort.Strings(hashes)
		setSHA, err := evidence.CatalogProjectionEvidenceSetSHA256(hashes)
		if err != nil {
			return nil, fmt.Errorf("identity %q evidence set: %w", accumulator.Route.ID, err)
		}
		identities = append(identities, evidence.CatalogProjectionIdentityEvidenceV1{
			RequestedIdentity: accumulator.Route.ID, CanonicalRoute: accumulator.Route.CanonicalRoute,
			ResolvedTone: accumulator.Route.Tone, RouteKind: string(accumulator.Route.Kind),
			CatalogVisibility: string(accumulator.Route.CatalogVisibility), CompatibilityRequired: accumulator.Route.CompatibilityRequired,
			MappingEvidence: accumulator.MappingEvidence, IdentityStatus: accumulator.IdentityStatus, PackageIssue: accumulator.PackageIssue,
			CatalogObservationSHA256: accumulator.CatalogObservationSHA, SupportingEvidenceSHA256: hashes,
			CapabilityEvidenceSetSHA256: setSHA,
		})
	}
	return identities, nil
}

func routeProtocolCapabilityRecords(records []evidence.RouteProtocolCapabilityRecordV1) []wp2CommittedCapabilityRecord {
	out := make([]wp2CommittedCapabilityRecord, 0, len(records))
	for _, record := range records {
		out = append(out, wp2CommittedCapabilityRecord{CapabilityID: record.CapabilityID, Evidence: record.CanonicalJSON, EvidenceSHA256: record.EvidenceSHA256})
	}
	return out
}

func aliasCapabilityRecords(records []evidence.AliasProjectionCapabilityRecordV1) []wp2CommittedCapabilityRecord {
	out := make([]wp2CommittedCapabilityRecord, 0, len(records))
	for _, record := range records {
		out = append(out, wp2CommittedCapabilityRecord{CapabilityID: record.CapabilityID, Evidence: record.CanonicalJSON, EvidenceSHA256: record.EvidenceSHA256})
	}
	return out
}

func legacyCapabilityRecords(records []evidence.LegacyConfiguredCapabilityRecordV1) []wp2CommittedCapabilityRecord {
	out := make([]wp2CommittedCapabilityRecord, 0, len(records))
	for _, record := range records {
		out = append(out, wp2CommittedCapabilityRecord{CapabilityID: record.CapabilityID, Evidence: record.CanonicalJSON, EvidenceSHA256: record.EvidenceSHA256})
	}
	return out
}

func accountPoolMatrixEntryCount(set evidence.AccountPoolEvidenceSetV1) int {
	count := 0
	for _, profile := range set.Profiles {
		count += len(profile.Matrix)
	}
	return count
}

func accountPoolCapabilityManifestCount(set evidence.AccountPoolEvidenceSetV1) int {
	count := 0
	for _, profile := range set.Profiles {
		for _, entry := range profile.Matrix {
			for _, capability := range entry.Capabilities {
				if len(capability.Evidence) != 0 {
					count++
				}
			}
		}
	}
	return count
}

func wp2SHA256Hex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
