package evidence

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBuildProvisionalClaimEvidenceSetIsDeterministic(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	inventory, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	revision := ProvisionalClaimRevisionIdentityV1{
		Head:     input.CurrentSourceIdentity.Commit,
		Tree:     repeatHex("a", 40),
		Modified: false,
	}
	harness := ProvisionalClaimHarnessIdentityV1{
		SHA256: repeatHex("b", 64),
		Bytes:  12345,
	}
	first, err := BuildProvisionalClaimEvidenceSet(revision, harness, inventory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildProvisionalClaimEvidenceSet(revision, harness, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) || first.ChecksumSHA256 != second.ChecksumSHA256 {
		t.Fatal("provisional claim evidence set is not deterministic")
	}
	if first.Set.InventorySHA256 != inventory.ChecksumSHA256 || first.Set.AcceptedCatalogSHA256 != inventory.Inventory.AcceptedCatalogSHA256 || first.Set.HistoricalInventorySHA256 != inventory.Inventory.HistoricalInventorySHA256 {
		t.Fatalf("evidence set bindings=%#v", first.Set)
	}
	validated, err := ValidateProvisionalClaimEvidenceSet(first.CanonicalJSON, ProvisionalClaimEvidenceSetExpected{
		Revision:  revision,
		Harness:   harness,
		Inventory: inventory,
	})
	if err != nil || validated.ChecksumSHA256 != first.ChecksumSHA256 {
		t.Fatalf("evidence set validation=%#v err=%v", validated, err)
	}
}

func TestBuildProvisionalClaimEvidenceSetRejectsDirtyOrMismatchedIdentity(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	inventory, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	harness := ProvisionalClaimHarnessIdentityV1{SHA256: repeatHex("b", 64), Bytes: 12345}
	for name, revision := range map[string]ProvisionalClaimRevisionIdentityV1{
		"dirty": {Head: input.CurrentSourceIdentity.Commit, Tree: repeatHex("a", 40), Modified: true},
		"head":  {Head: repeatHex("c", 40), Tree: repeatHex("a", 40), Modified: false},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildProvisionalClaimEvidenceSet(revision, harness, inventory); validationCode(err) != "identity_mismatch" {
				t.Fatalf("identity error=%v, want identity_mismatch", err)
			}
		})
	}
}

func TestValidateProvisionalClaimEvidenceSetRejectsUnknownAndTamperedContent(t *testing.T) {
	input, policies, expectedWP1 := provisionalClaimFixture(t)
	inventory, err := buildProvisionalClaimInventory(input, policies, expectedWP1)
	if err != nil {
		t.Fatal(err)
	}
	revision := ProvisionalClaimRevisionIdentityV1{Head: input.CurrentSourceIdentity.Commit, Tree: repeatHex("a", 40), Modified: false}
	harness := ProvisionalClaimHarnessIdentityV1{SHA256: repeatHex("b", 64), Bytes: 12345}
	valid, err := BuildProvisionalClaimEvidenceSet(revision, harness, inventory)
	if err != nil {
		t.Fatal(err)
	}
	expected := ProvisionalClaimEvidenceSetExpected{Revision: revision, Harness: harness, Inventory: inventory}

	var decoded map[string]any
	if err := json.Unmarshal(valid.CanonicalJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["extra"] = true
	unknown, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProvisionalClaimEvidenceSet(unknown, expected); validationCode(err) != "unknown_field" {
		t.Fatalf("unknown field error=%v, want unknown_field", err)
	}

	tampered := bytes.Replace(valid.CanonicalJSON, []byte(`"harness":{"sha256":"`+repeatHex("b", 64)), []byte(`"harness":{"sha256":"`+repeatHex("c", 64)), 1)
	if _, err := ValidateProvisionalClaimEvidenceSet(tampered, expected); validationCode(err) != "identity_mismatch" {
		t.Fatalf("tampered set error=%v, want identity_mismatch", err)
	}

	var treeDrift ProvisionalClaimEvidenceSetV1
	if err := json.Unmarshal(valid.CanonicalJSON, &treeDrift); err != nil {
		t.Fatal(err)
	}
	treeDrift.Revision.Tree = repeatHex("d", 40)
	treeDriftRaw, err := json.Marshal(treeDrift)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProvisionalClaimEvidenceSet(treeDriftRaw, expected); validationCode(err) != "identity_mismatch" {
		t.Fatalf("tree drift error=%v, want identity_mismatch", err)
	}
}
