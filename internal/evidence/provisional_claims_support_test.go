package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

const (
	ProvisionalClaimInventorySchemaV1   = "m365-wp2-provisional-claim-inventory/v1"
	MaxProvisionalClaimInventoryBytes   = 512 * 1024
	acceptedWP1ProvisionalClaimCommitV1 = "3c4cc894ca50a6b8182ad6982ef158ae4435218e"
	acceptedWP1ProvisionalClaimPathV1   = "internal/web/codex_catalog.go"
	acceptedWP1ProvisionalClaimBytesV1  = int64(9518)
	acceptedWP1ProvisionalClaimSHA256V1 = "52b4db6822a90cb5d25130b30bc93ad76524371eaa012606fd31a6149b2bd9b2"
)

type ProvisionalClaimDisposition string

const (
	ProvisionalClaimEvidenceBacked        ProvisionalClaimDisposition = "evidence_backed"
	ProvisionalClaimImplementedUnaccepted ProvisionalClaimDisposition = "implemented_unaccepted"
	ProvisionalClaimUnverified            ProvisionalClaimDisposition = "unverified"
	ProvisionalClaimUnsupported           ProvisionalClaimDisposition = "unsupported"
)

type ProvisionalClaimSourceIdentityV1 struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type ProvisionalClaimFieldV1 struct {
	Path               string `json:"path"`
	WP1ValueSHA256     string `json:"wp1_value_sha256"`
	CurrentValueSHA256 string `json:"current_value_sha256"`
}

type ProvisionalClaimHistoricalReferenceV1 struct {
	CapabilityID     string              `json:"capability_id"`
	HistoricalStatus TestExecutionStatus `json:"historical_status"`
	Classification   Classification      `json:"classification"`
	RationaleCode    string              `json:"rationale_code"`
}

type ProvisionalClaimRecordV1 struct {
	ClaimID                string                                  `json:"claim_id"`
	PublicSurface          string                                  `json:"public_surface"`
	Fields                 []ProvisionalClaimFieldV1               `json:"fields"`
	Disposition            ProvisionalClaimDisposition             `json:"disposition"`
	RationaleCode          string                                  `json:"rationale_code"`
	AcceptedCapabilityID   string                                  `json:"accepted_capability_id,omitempty"`
	AcceptedSupportSHA256  []string                                `json:"accepted_support_sha256"`
	HistoricalReferences   []ProvisionalClaimHistoricalReferenceV1 `json:"historical_references"`
	DeferredOwner          string                                  `json:"deferred_owner,omitempty"`
	CompatibilityPreserved bool                                    `json:"compatibility_preserved"`
}

type ProvisionalClaimDispositionCountsV1 struct {
	EvidenceBacked        int `json:"evidence_backed"`
	ImplementedUnaccepted int `json:"implemented_unaccepted"`
	Unverified            int `json:"unverified"`
	Unsupported           int `json:"unsupported"`
}

type ProvisionalClaimInventoryV1 struct {
	Schema                      string                              `json:"schema"`
	NormativeADRSHA256          string                              `json:"normative_adr_sha256"`
	WP1Source                   ProvisionalClaimSourceIdentityV1    `json:"wp1_source"`
	CurrentSource               ProvisionalClaimSourceIdentityV1    `json:"current_source"`
	AcceptedCatalogSHA256       string                              `json:"accepted_catalog_sha256"`
	HistoricalInventorySHA256   string                              `json:"historical_inventory_sha256"`
	EvidenceBackedCapabilityIDs []string                            `json:"evidence_backed_capability_ids"`
	EntryCount                  int                                 `json:"entry_count"`
	DispositionCounts           ProvisionalClaimDispositionCountsV1 `json:"disposition_counts"`
	Entries                     []ProvisionalClaimRecordV1          `json:"entries"`
}

type ValidatedProvisionalClaimInventory struct {
	Inventory      ProvisionalClaimInventoryV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type ProvisionalClaimBuildInput struct {
	NormativeADRSHA256        string
	WP1SourceIdentity         ProvisionalClaimSourceIdentityV1
	WP1Source                 []byte
	CurrentSourceIdentity     ProvisionalClaimSourceIdentityV1
	CurrentSource             []byte
	AcceptedCatalog           ValidatedCatalogProjection
	HistoricalInventory       HistoricalBaselineInventoryV1
	HistoricalInventorySHA256 string
}

type provisionalClaimInventoryExpected struct {
	Input       ProvisionalClaimBuildInput
	Policies    []provisionalClaimPolicyV1
	ExpectedWP1 ProvisionalClaimSourceIdentityV1
}

type provisionalClaimPolicyV1 struct {
	ClaimID                 string
	PublicSurface           string
	FieldPaths              []string
	Disposition             ProvisionalClaimDisposition
	RationaleCode           string
	AcceptedCapabilityID    string
	HistoricalCapabilityIDs []string
	DeferredOwner           string
}

var allowedProvisionalClaimInventoryFields = map[string]struct{}{
	"schema":                         {},
	"normative_adr_sha256":           {},
	"wp1_source":                     {},
	"current_source":                 {},
	"accepted_catalog_sha256":        {},
	"historical_inventory_sha256":    {},
	"evidence_backed_capability_ids": {},
	"entry_count":                    {},
	"disposition_counts":             {},
	"entries":                        {},
}

var provisionalClaimNonCapabilityFieldsV1 = map[string]struct{}{
	"id":                     {},
	"slug":                   {},
	"display_name":           {},
	"description":            {},
	"canonical_route":        {},
	"resolved_tone":          {},
	"route_kind":             {},
	"operational_status":     {},
	"mapping_evidence":       {},
	"identity_status":        {},
	"catalog_visibility":     {},
	"alias_used":             {},
	"compatibility_required": {},
	"configured_mapping":     {},
	"experimental":           {},
	"deprecated":             {},
	"base_instructions":      {},
	"model_messages":         {},
	"object":                 {},
	"owned_by":               {},
	"visibility":             {},
	"priority":               {},
	"capabilities":           {},
	"account_dependent":      {},
}

func buildProvisionalClaimInventory(input ProvisionalClaimBuildInput, policies []provisionalClaimPolicyV1, expectedWP1 ProvisionalClaimSourceIdentityV1) (ValidatedProvisionalClaimInventory, error) {
	if err := requireSHA256(input.NormativeADRSHA256, "/normative_adr_sha256"); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	if input.WP1SourceIdentity != expectedWP1 {
		return ValidatedProvisionalClaimInventory{}, validationError("identity_mismatch", "accepted_wp1_source_identity", "/wp1_source")
	}
	if err := validateProvisionalSource(input.WP1SourceIdentity, input.WP1Source, "/wp1_source"); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	if err := validateProvisionalSource(input.CurrentSourceIdentity, input.CurrentSource, "/current_source"); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	if input.CurrentSourceIdentity.Path != expectedWP1.Path {
		return ValidatedProvisionalClaimInventory{}, validationError("identity_mismatch", "current_catalog_source_path", "/current_source/path")
	}
	if err := validateAcceptedCatalogIdentity(input.AcceptedCatalog, input.NormativeADRSHA256); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	historical, err := validateAcceptedHistoricalInventory(input)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}

	wp1Fields, err := extractProvisionalCatalogFields(input.WP1Source)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	currentFields, err := extractProvisionalCatalogFields(input.CurrentSource)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}

	normalizedPolicies, policyFields, err := normalizeProvisionalClaimPolicies(policies)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	if err := matchProvisionalFieldMembership(wp1Fields, policyFields, "/wp1_source"); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	if err := matchProvisionalFieldMembership(currentFields, policyFields, "/current_source"); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}

	historicalByID := make(map[string]HistoricalBaselineDispositionV1, len(historical.Entries))
	for _, entry := range historical.Entries {
		historicalByID[entry.CapabilityID] = entry
	}
	acceptedSupport := acceptedCatalogSupportByCapability(input.AcceptedCatalog)

	entries := make([]ProvisionalClaimRecordV1, 0, len(normalizedPolicies))
	counts := ProvisionalClaimDispositionCountsV1{}
	evidenceBackedIDs := make([]string, 0, len(wp2VerifiableCapabilities))
	for _, policy := range normalizedPolicies {
		record := ProvisionalClaimRecordV1{
			ClaimID:                policy.ClaimID,
			PublicSurface:          policy.PublicSurface,
			Fields:                 make([]ProvisionalClaimFieldV1, 0, len(policy.FieldPaths)),
			Disposition:            policy.Disposition,
			RationaleCode:          policy.RationaleCode,
			AcceptedCapabilityID:   policy.AcceptedCapabilityID,
			AcceptedSupportSHA256:  []string{},
			HistoricalReferences:   []ProvisionalClaimHistoricalReferenceV1{},
			DeferredOwner:          policy.DeferredOwner,
			CompatibilityPreserved: len(policy.FieldPaths) > 0,
		}
		for _, path := range policy.FieldPaths {
			wp1Value := wp1Fields[path]
			currentValue := currentFields[path]
			if wp1Value != currentValue {
				return ValidatedProvisionalClaimInventory{}, validationError("compatibility_drift", "wp1_catalog_field_value", "/entries/"+policy.ClaimID+"/fields/"+path)
			}
			record.Fields = append(record.Fields, ProvisionalClaimFieldV1{
				Path:               path,
				WP1ValueSHA256:     wp1Value,
				CurrentValueSHA256: currentValue,
			})
		}
		for _, capabilityID := range policy.HistoricalCapabilityIDs {
			historicalEntry, ok := historicalByID[capabilityID]
			if !ok {
				return ValidatedProvisionalClaimInventory{}, validationError("missing_field", "historical_capability_reference", "/entries/"+policy.ClaimID+"/historical_references")
			}
			record.HistoricalReferences = append(record.HistoricalReferences, ProvisionalClaimHistoricalReferenceV1{
				CapabilityID:     historicalEntry.CapabilityID,
				HistoricalStatus: historicalEntry.HistoricalStatus,
				Classification:   historicalEntry.Classification,
				RationaleCode:    historicalEntry.RationaleCode,
			})
		}
		if policy.Disposition == ProvisionalClaimEvidenceBacked {
			if _, ok := wp2VerifiableCapabilities[policy.AcceptedCapabilityID]; !ok || policy.AcceptedCapabilityID != policy.ClaimID {
				return ValidatedProvisionalClaimInventory{}, validationError("verification_scope_forbidden", "wp2_verified_capability", "/entries/"+policy.ClaimID+"/accepted_capability_id")
			}
			record.AcceptedSupportSHA256 = append(record.AcceptedSupportSHA256, acceptedSupport[policy.AcceptedCapabilityID]...)
			if len(record.AcceptedSupportSHA256) == 0 {
				return ValidatedProvisionalClaimInventory{}, validationError("missing_field", "accepted_wp2_support", "/entries/"+policy.ClaimID+"/accepted_support_sha256")
			}
			evidenceBackedIDs = append(evidenceBackedIDs, policy.ClaimID)
			counts.EvidenceBacked++
		} else {
			if policy.AcceptedCapabilityID != "" {
				return ValidatedProvisionalClaimInventory{}, validationError("verification_scope_forbidden", "non_evidence_backed_capability", "/entries/"+policy.ClaimID+"/accepted_capability_id")
			}
			switch policy.Disposition {
			case ProvisionalClaimImplementedUnaccepted:
				counts.ImplementedUnaccepted++
			case ProvisionalClaimUnverified:
				counts.Unverified++
			case ProvisionalClaimUnsupported:
				if len(record.HistoricalReferences) == 0 {
					return ValidatedProvisionalClaimInventory{}, validationError("unsupported_evidence_required", "accepted_negative_evidence", "/entries/"+policy.ClaimID+"/historical_references")
				}
				for _, reference := range record.HistoricalReferences {
					if reference.Classification != ClassificationUnsupported {
						return ValidatedProvisionalClaimInventory{}, validationError("unsupported_evidence_required", "accepted_negative_evidence", "/entries/"+policy.ClaimID+"/historical_references")
					}
				}
				counts.Unsupported++
			default:
				return ValidatedProvisionalClaimInventory{}, validationError("invalid_enum", "provisional_claim_disposition", "/entries/"+policy.ClaimID+"/disposition")
			}
		}
		entries = append(entries, record)
	}
	sort.Strings(evidenceBackedIDs)

	inventory := ProvisionalClaimInventoryV1{
		Schema:                      ProvisionalClaimInventorySchemaV1,
		NormativeADRSHA256:          input.NormativeADRSHA256,
		WP1Source:                   input.WP1SourceIdentity,
		CurrentSource:               input.CurrentSourceIdentity,
		AcceptedCatalogSHA256:       input.AcceptedCatalog.ChecksumSHA256,
		HistoricalInventorySHA256:   input.HistoricalInventorySHA256,
		EvidenceBackedCapabilityIDs: evidenceBackedIDs,
		EntryCount:                  len(entries),
		DispositionCounts:           counts,
		Entries:                     entries,
	}
	canonical, err := json.Marshal(inventory)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	digest := sha256.Sum256(canonical)
	return ValidatedProvisionalClaimInventory{
		Inventory:      inventory,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func validateProvisionalClaimInventory(raw []byte, expected provisionalClaimInventoryExpected) (ValidatedProvisionalClaimInventory, error) {
	if len(raw) == 0 {
		return ValidatedProvisionalClaimInventory{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxProvisionalClaimInventoryBytes {
		return ValidatedProvisionalClaimInventory{}, validationError("evidence_too_large", "provisional_claim_inventory_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedProvisionalClaimInventoryFields); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	var inventory ProvisionalClaimInventoryV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedProvisionalClaimInventory{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedProvisionalClaimInventory{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	derived, err := buildProvisionalClaimInventory(expected.Input, expected.Policies, expected.ExpectedWP1)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, err
	}
	canonical, err := json.Marshal(inventory)
	if err != nil {
		return ValidatedProvisionalClaimInventory{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	if !bytes.Equal(canonical, derived.CanonicalJSON) {
		return ValidatedProvisionalClaimInventory{}, validationError("identity_mismatch", "derived_provisional_claim_inventory", "/")
	}
	return derived, nil
}

func validateProvisionalSource(identity ProvisionalClaimSourceIdentityV1, raw []byte, path string) error {
	if !gitCommitPattern.MatchString(identity.Commit) {
		return validationError("invalid_identity", "git_commit_sha", path+"/commit")
	}
	if identity.Path == "" || strings.HasPrefix(identity.Path, "/") || strings.Contains(identity.Path, "..") {
		return validationError("invalid_identity", "repository_relative_path", path+"/path")
	}
	if identity.Bytes != int64(len(raw)) {
		return validationError("identity_mismatch", "source_byte_count", path+"/bytes")
	}
	if err := requireSHA256(identity.SHA256, path+"/sha256"); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if identity.SHA256 != hex.EncodeToString(digest[:]) {
		return validationError("identity_mismatch", "source_sha256", path+"/sha256")
	}
	return nil
}

func validateAcceptedCatalogIdentity(catalog ValidatedCatalogProjection, normativeADRSHA256 string) error {
	if catalog.Manifest.Schema != CatalogProjectionManifestSchemaV1 || catalog.Manifest.AcceptanceStatus != CatalogProjectionAccepted {
		return validationError("identity_mismatch", "accepted_catalog_manifest", "/accepted_catalog_sha256")
	}
	if err := requireSHA256(catalog.ChecksumSHA256, "/accepted_catalog_sha256"); err != nil {
		return err
	}
	digest := sha256.Sum256(catalog.CanonicalJSON)
	if catalog.ChecksumSHA256 != hex.EncodeToString(digest[:]) {
		return validationError("identity_mismatch", "accepted_catalog_manifest", "/accepted_catalog_sha256")
	}
	for _, pkg := range catalog.Manifest.Packages {
		if pkg.NormativeADRSHA256 != normativeADRSHA256 {
			return validationError("identity_mismatch", "normative_adr_sha256", "/accepted_catalog_sha256")
		}
	}
	return nil
}

func validateAcceptedHistoricalInventory(input ProvisionalClaimBuildInput) (HistoricalBaselineInventoryV1, error) {
	if err := requireSHA256(input.HistoricalInventorySHA256, "/historical_inventory_sha256"); err != nil {
		return HistoricalBaselineInventoryV1{}, err
	}
	if input.HistoricalInventory.Schema != HistoricalBaselineInventorySchemaV1 || input.HistoricalInventory.NormativeADRSHA256 != input.NormativeADRSHA256 || input.HistoricalInventory.AcceptedCatalogSHA256 != input.AcceptedCatalog.ChecksumSHA256 {
		return HistoricalBaselineInventoryV1{}, validationError("identity_mismatch", "accepted_historical_inventory", "/historical_inventory_sha256")
	}
	canonical, err := json.Marshal(input.HistoricalInventory)
	if err != nil {
		return HistoricalBaselineInventoryV1{}, validationError("canonicalization_failed", "deterministic_encoding", "/historical_inventory_sha256")
	}
	digest := sha256.Sum256(canonical)
	if input.HistoricalInventorySHA256 != hex.EncodeToString(digest[:]) {
		return HistoricalBaselineInventoryV1{}, validationError("identity_mismatch", "accepted_historical_inventory", "/historical_inventory_sha256")
	}
	if input.HistoricalInventory.EntryCount != len(input.HistoricalInventory.Entries) {
		return HistoricalBaselineInventoryV1{}, validationError("identity_mismatch", "historical_entry_count", "/historical_inventory_sha256")
	}
	seen := make(map[string]struct{}, len(input.HistoricalInventory.Entries))
	last := ""
	counts := HistoricalClassificationCountsV1{}
	passCount := 0
	failCount := 0
	for _, entry := range input.HistoricalInventory.Entries {
		if entry.CapabilityID == "" || entry.CapabilityID <= last {
			return HistoricalBaselineInventoryV1{}, validationError("determinism_violation", "sorted_historical_capability_ids", "/historical_inventory_sha256")
		}
		last = entry.CapabilityID
		if _, exists := seen[entry.CapabilityID]; exists {
			return HistoricalBaselineInventoryV1{}, validationError("duplicate_identity", "historical_capability_id", "/historical_inventory_sha256")
		}
		seen[entry.CapabilityID] = struct{}{}
		if entry.HistoricalStatus == TestExecutionPass {
			passCount++
		} else if entry.HistoricalStatus == TestExecutionFail {
			failCount++
		}
		switch entry.Classification {
		case ClassificationVerified:
			counts.Verified++
		case ClassificationConfirmedDefect:
			counts.ConfirmedDefect++
		case ClassificationUnsupported:
			counts.Unsupported++
		case ClassificationInconclusive:
			counts.Inconclusive++
		default:
			return HistoricalBaselineInventoryV1{}, validationError("invalid_enum", "historical_classification", "/historical_inventory_sha256")
		}
	}
	if input.HistoricalInventory.HistoricalPassCount != passCount || input.HistoricalInventory.HistoricalFailCount != failCount || input.HistoricalInventory.ClassificationCounts != counts {
		return HistoricalBaselineInventoryV1{}, validationError("identity_mismatch", "historical_inventory_counts", "/historical_inventory_sha256")
	}
	return input.HistoricalInventory, nil
}

func acceptedCatalogSupportByCapability(catalog ValidatedCatalogProjection) map[string][]string {
	sets := make(map[string]map[string]struct{}, len(wp2VerifiableCapabilities))
	for capabilityID := range wp2VerifiableCapabilities {
		sets[capabilityID] = make(map[string]struct{})
	}
	for _, claim := range catalog.Manifest.GlobalClaims {
		for _, capability := range claim.Capabilities {
			if capability.Classification != ClassificationVerified {
				continue
			}
			set, ok := sets[capability.CapabilityID]
			if !ok {
				continue
			}
			for _, checksum := range capability.SupportingEvidenceSHA256 {
				set[checksum] = struct{}{}
			}
		}
	}
	result := make(map[string][]string, len(sets))
	for capabilityID, set := range sets {
		values := make([]string, 0, len(set))
		for checksum := range set {
			values = append(values, checksum)
		}
		sort.Strings(values)
		result[capabilityID] = values
	}
	return result
}

func normalizeProvisionalClaimPolicies(policies []provisionalClaimPolicyV1) ([]provisionalClaimPolicyV1, map[string]struct{}, error) {
	normalized := append([]provisionalClaimPolicyV1(nil), policies...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ClaimID < normalized[j].ClaimID })
	seenClaims := make(map[string]struct{}, len(normalized))
	fieldSet := make(map[string]struct{})
	last := ""
	for i := range normalized {
		policy := &normalized[i]
		if policy.ClaimID == "" || !capabilityPattern.MatchString(policy.ClaimID) || policy.ClaimID == last {
			return nil, nil, validationError("duplicate_identity", "provisional_claim_id", "/entries")
		}
		last = policy.ClaimID
		if _, exists := seenClaims[policy.ClaimID]; exists {
			return nil, nil, validationError("duplicate_identity", "provisional_claim_id", "/entries")
		}
		seenClaims[policy.ClaimID] = struct{}{}
		if policy.PublicSurface == "" || policy.RationaleCode == "" {
			return nil, nil, validationError("missing_field", "provisional_claim_policy", "/entries/"+policy.ClaimID)
		}
		policy.FieldPaths = append([]string(nil), policy.FieldPaths...)
		sort.Strings(policy.FieldPaths)
		for j, path := range policy.FieldPaths {
			if path == "" || (j > 0 && path == policy.FieldPaths[j-1]) {
				return nil, nil, validationError("duplicate_identity", "provisional_field_path", "/entries/"+policy.ClaimID+"/fields")
			}
			if _, exists := fieldSet[path]; exists {
				return nil, nil, validationError("duplicate_identity", "provisional_field_path", "/entries/"+policy.ClaimID+"/fields")
			}
			fieldSet[path] = struct{}{}
		}
		policy.HistoricalCapabilityIDs = append([]string(nil), policy.HistoricalCapabilityIDs...)
		sort.Strings(policy.HistoricalCapabilityIDs)
		for j := 1; j < len(policy.HistoricalCapabilityIDs); j++ {
			if policy.HistoricalCapabilityIDs[j] == policy.HistoricalCapabilityIDs[j-1] {
				return nil, nil, validationError("duplicate_identity", "historical_capability_reference", "/entries/"+policy.ClaimID+"/historical_references")
			}
		}
	}
	return normalized, fieldSet, nil
}

func matchProvisionalFieldMembership(fields map[string]string, expected map[string]struct{}, path string) error {
	observed := make(map[string]struct{})
	for fieldPath := range fields {
		if strings.HasPrefix(fieldPath, "capabilities.") {
			observed[fieldPath] = struct{}{}
			continue
		}
		if strings.HasPrefix(fieldPath, "x_m365_") || fieldPath == "account_dependent" {
			continue
		}
		if _, excluded := provisionalClaimNonCapabilityFieldsV1[fieldPath]; !excluded {
			observed[fieldPath] = struct{}{}
		}
	}
	if len(observed) != len(expected) {
		return validationError("identity_mismatch", "provisional_claim_field_membership", path)
	}
	for fieldPath := range expected {
		if _, ok := observed[fieldPath]; !ok {
			return validationError("identity_mismatch", "provisional_claim_field_membership", path+"/"+fieldPath)
		}
	}
	return nil
}

func extractProvisionalCatalogFields(raw []byte) (map[string]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, acceptedWP1ProvisionalClaimPathV1, raw, parser.SkipObjectResolution)
	if err != nil {
		return nil, validationError("invalid_source", "parse_catalog_source", "/")
	}
	var fallback *ast.FuncDecl
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		switch fn.Name.Name {
		case "modelCatalogForSettingsAndEvidence":
			target = fn
		case "modelCatalog":
			fallback = fn
		}
	}
	if target == nil {
		target = fallback
	}
	if target == nil {
		return nil, validationError("missing_field", "catalog_builder_function", "/")
	}

	var caps *ast.CompositeLit
	var entry *ast.CompositeLit
	ast.Inspect(target.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range typed.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(typed.Rhs) {
					continue
				}
				literal, ok := typed.Rhs[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				switch ident.Name {
				case "caps":
					caps = literal
				case "entry":
					entry = literal
				}
			}
		case *ast.CallExpr:
			ident, ok := typed.Fun.(*ast.Ident)
			if !ok || ident.Name != "append" || len(typed.Args) < 2 {
				break
			}
			literal, ok := typed.Args[1].(*ast.CompositeLit)
			if ok && compositeLiteralHasStringKey(literal, "capabilities") && compositeLiteralHasStringKey(literal, "id") {
				entry = literal
			}
		}
		return true
	})
	if caps == nil || entry == nil {
		return nil, validationError("missing_field", "catalog_compatibility_maps", "/")
	}
	fields := make(map[string]string)
	if err := extractMapExpressions(fset, caps, "capabilities.", fields); err != nil {
		return nil, err
	}
	if err := extractMapExpressions(fset, entry, "", fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func extractMapExpressions(fset *token.FileSet, literal *ast.CompositeLit, prefix string, fields map[string]string) error {
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyLiteral, ok := pair.Key.(*ast.BasicLit)
		if !ok || keyLiteral.Kind != token.STRING {
			continue
		}
		key, err := strconv.Unquote(keyLiteral.Value)
		if err != nil {
			return validationError("invalid_source", "catalog_field_name", "/")
		}
		if prefix == "" && key == "truncation_policy" {
			nested, ok := pair.Value.(*ast.CompositeLit)
			if !ok {
				return validationError("invalid_source", "truncation_policy_literal", "/truncation_policy")
			}
			if err := extractMapExpressions(fset, nested, "truncation_policy.", fields); err != nil {
				return err
			}
			continue
		}
		path := prefix + key
		if _, exists := fields[path]; exists {
			return validationError("duplicate_identity", "catalog_field_path", "/"+path)
		}
		var formatted bytes.Buffer
		if err := format.Node(&formatted, fset, pair.Value); err != nil {
			return validationError("invalid_source", "catalog_field_expression", "/"+path)
		}
		digest := sha256.Sum256(formatted.Bytes())
		fields[path] = hex.EncodeToString(digest[:])
	}
	return nil
}

func compositeLiteralHasStringKey(literal *ast.CompositeLit, key string) bool {
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyLiteral, ok := pair.Key.(*ast.BasicLit)
		if !ok || keyLiteral.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(keyLiteral.Value)
		if err == nil && value == key {
			return true
		}
	}
	return false
}
