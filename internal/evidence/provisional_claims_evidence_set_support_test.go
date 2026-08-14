package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const (
	ProvisionalClaimEvidenceSetSchemaV1 = "m365-wp2-provisional-claim-evidence-set/v1"
	MaxProvisionalClaimEvidenceSetBytes = 1024 * 1024
)

type ProvisionalClaimRevisionIdentityV1 struct {
	Head     string `json:"head"`
	Tree     string `json:"tree"`
	Modified bool   `json:"modified"`
}

type ProvisionalClaimHarnessIdentityV1 struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type ProvisionalClaimEvidenceSetV1 struct {
	Schema                      string                              `json:"schema"`
	Revision                    ProvisionalClaimRevisionIdentityV1  `json:"revision"`
	Harness                     ProvisionalClaimHarnessIdentityV1   `json:"harness"`
	InventorySHA256             string                              `json:"inventory_sha256"`
	AcceptedCatalogSHA256       string                              `json:"accepted_catalog_sha256"`
	HistoricalInventorySHA256   string                              `json:"historical_inventory_sha256"`
	EntryCount                  int                                 `json:"entry_count"`
	DispositionCounts           ProvisionalClaimDispositionCountsV1 `json:"disposition_counts"`
	EvidenceBackedCapabilityIDs []string                            `json:"evidence_backed_capability_ids"`
	Inventory                   ProvisionalClaimInventoryV1         `json:"inventory"`
}

type ValidatedProvisionalClaimEvidenceSet struct {
	Set            ProvisionalClaimEvidenceSetV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type ProvisionalClaimEvidenceSetExpected struct {
	Revision  ProvisionalClaimRevisionIdentityV1
	Harness   ProvisionalClaimHarnessIdentityV1
	Inventory ValidatedProvisionalClaimInventory
}

var allowedProvisionalClaimEvidenceSetFields = map[string]struct{}{
	"schema":                         {},
	"revision":                       {},
	"harness":                        {},
	"inventory_sha256":               {},
	"accepted_catalog_sha256":        {},
	"historical_inventory_sha256":    {},
	"entry_count":                    {},
	"disposition_counts":             {},
	"evidence_backed_capability_ids": {},
	"inventory":                      {},
}

func BuildProvisionalClaimEvidenceSet(revision ProvisionalClaimRevisionIdentityV1, harness ProvisionalClaimHarnessIdentityV1, inventory ValidatedProvisionalClaimInventory) (ValidatedProvisionalClaimEvidenceSet, error) {
	if err := validateProvisionalClaimEvidenceSetInputs(revision, harness, inventory); err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, err
	}
	set := ProvisionalClaimEvidenceSetV1{
		Schema:                      ProvisionalClaimEvidenceSetSchemaV1,
		Revision:                    revision,
		Harness:                     harness,
		InventorySHA256:             inventory.ChecksumSHA256,
		AcceptedCatalogSHA256:       inventory.Inventory.AcceptedCatalogSHA256,
		HistoricalInventorySHA256:   inventory.Inventory.HistoricalInventorySHA256,
		EntryCount:                  inventory.Inventory.EntryCount,
		DispositionCounts:           inventory.Inventory.DispositionCounts,
		EvidenceBackedCapabilityIDs: append([]string(nil), inventory.Inventory.EvidenceBackedCapabilityIDs...),
		Inventory:                   inventory.Inventory,
	}
	canonical, err := json.Marshal(set)
	if err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	digest := sha256.Sum256(canonical)
	return ValidatedProvisionalClaimEvidenceSet{
		Set:            set,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func ValidateProvisionalClaimEvidenceSet(raw []byte, expected ProvisionalClaimEvidenceSetExpected) (ValidatedProvisionalClaimEvidenceSet, error) {
	if len(raw) == 0 {
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxProvisionalClaimEvidenceSetBytes {
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("evidence_too_large", "provisional_claim_evidence_set_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedProvisionalClaimEvidenceSetFields); err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, err
	}
	var set ProvisionalClaimEvidenceSetV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedProvisionalClaimEvidenceSet{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, err
	}
	derived, err := BuildProvisionalClaimEvidenceSet(expected.Revision, expected.Harness, expected.Inventory)
	if err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, err
	}
	canonical, err := json.Marshal(set)
	if err != nil {
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("canonicalization_failed", "deterministic_encoding", "/")
	}
	if !bytes.Equal(canonical, derived.CanonicalJSON) {
		return ValidatedProvisionalClaimEvidenceSet{}, validationError("identity_mismatch", "derived_provisional_claim_evidence_set", "/")
	}
	return derived, nil
}

func validateProvisionalClaimEvidenceSetInputs(revision ProvisionalClaimRevisionIdentityV1, harness ProvisionalClaimHarnessIdentityV1, inventory ValidatedProvisionalClaimInventory) error {
	if !gitCommitPattern.MatchString(revision.Head) {
		return validationError("invalid_identity", "git_commit_sha", "/revision/head")
	}
	if !gitCommitPattern.MatchString(revision.Tree) {
		return validationError("invalid_identity", "git_tree_sha", "/revision/tree")
	}
	if revision.Modified {
		return validationError("identity_mismatch", "clean_source_required", "/revision/modified")
	}
	if err := requireSHA256(harness.SHA256, "/harness/sha256"); err != nil {
		return err
	}
	if harness.Bytes <= 0 {
		return validationError("invalid_identity", "positive_byte_count", "/harness/bytes")
	}
	if inventory.Inventory.Schema != ProvisionalClaimInventorySchemaV1 || inventory.Inventory.CurrentSource.Commit != revision.Head {
		return validationError("identity_mismatch", "source_revision", "/revision/head")
	}
	if err := requireSHA256(inventory.ChecksumSHA256, "/inventory_sha256"); err != nil {
		return err
	}
	canonical, err := json.Marshal(inventory.Inventory)
	if err != nil {
		return validationError("canonicalization_failed", "deterministic_encoding", "/inventory")
	}
	digest := sha256.Sum256(canonical)
	if inventory.ChecksumSHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(canonical, inventory.CanonicalJSON) {
		return validationError("identity_mismatch", "provisional_claim_inventory", "/inventory")
	}
	return nil
}
