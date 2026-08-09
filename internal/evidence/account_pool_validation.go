package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type AccountPoolEvidenceSetExpected struct {
	JSONSHA256              string
	NormativeADRSHA256      string
	SourceHead              string
	DirtyContentSHA256      string
	BinarySHA256            string
	HarnessSHA256           string
	EffectiveSettingsSHA256 string
	ProfileSetSHA256        string
}

type ValidatedAccountPoolEvidenceSet struct {
	Set            AccountPoolEvidenceSetV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

var allowedAccountPoolEvidenceSetFields = map[string]struct{}{
	"schema":             {},
	"profile_set_sha256": {},
	"profiles":           {},
	"global_claims":      {},
}

func ValidateAccountPoolEvidenceSet(raw []byte, expected AccountPoolEvidenceSetExpected) (ValidatedAccountPoolEvidenceSet, error) {
	if len(raw) == 0 {
		return ValidatedAccountPoolEvidenceSet{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxAccountPoolInputBytes {
		return ValidatedAccountPoolEvidenceSet{}, validationError("evidence_too_large", "account_pool_evidence_set_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedAccountPoolEvidenceSetFields); err != nil {
		return ValidatedAccountPoolEvidenceSet{}, err
	}
	if err := validateAccountPoolEvidenceSetExpected(expected); err != nil {
		return ValidatedAccountPoolEvidenceSet{}, err
	}
	rawDigest := sha256.Sum256(raw)
	checksum := hex.EncodeToString(rawDigest[:])
	if checksum != expected.JSONSHA256 {
		return ValidatedAccountPoolEvidenceSet{}, validationError("identity_mismatch", "account_pool_evidence_json_sha256", "/")
	}

	var set AccountPoolEvidenceSetV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedAccountPoolEvidenceSet{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedAccountPoolEvidenceSet{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedAccountPoolEvidenceSet{}, err
	}
	if set.Schema != AccountPoolEvidenceSetSchemaV1 {
		return ValidatedAccountPoolEvidenceSet{}, validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if set.ProfileSetSHA256 != expected.ProfileSetSHA256 {
		return ValidatedAccountPoolEvidenceSet{}, validationError("identity_mismatch", "profile_set_sha256", "/profile_set_sha256")
	}

	canonical, err := json.Marshal(set)
	if err != nil {
		return ValidatedAccountPoolEvidenceSet{}, validationError("canonicalization_failed", "account_pool_evidence_set", "/")
	}
	input, evidenceCount, err := accountPoolInputFromEvidenceSet(set, expected)
	if err != nil {
		return ValidatedAccountPoolEvidenceSet{}, err
	}
	if evidenceCount == 0 {
		return ValidatedAccountPoolEvidenceSet{}, validationError("missing_field", "accepted_capability_manifest", "/profiles")
	}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		return ValidatedAccountPoolEvidenceSet{}, validationError("canonicalization_failed", "account_pool_input", "/profiles")
	}
	rebuilt, err := BuildAccountPoolEvidence(inputRaw)
	if err != nil {
		return ValidatedAccountPoolEvidenceSet{}, err
	}
	rebuiltRaw, err := json.Marshal(rebuilt)
	if err != nil {
		return ValidatedAccountPoolEvidenceSet{}, validationError("canonicalization_failed", "account_pool_evidence_set", "/")
	}
	if !bytes.Equal(rebuiltRaw, canonical) {
		return ValidatedAccountPoolEvidenceSet{}, validationError("identity_mismatch", "account_pool_rederived_output", "/")
	}
	return ValidatedAccountPoolEvidenceSet{
		Set:            set,
		CanonicalJSON:  append([]byte(nil), canonical...),
		ChecksumSHA256: checksum,
	}, nil
}

func accountPoolInputFromEvidenceSet(set AccountPoolEvidenceSetV1, expected AccountPoolEvidenceSetExpected) (AccountPoolInputV1, int, error) {
	input := AccountPoolInputV1{Schema: AccountPoolInputSchemaV1, Profiles: make([]AccountPoolProfileInputV1, 0, len(set.Profiles))}
	evidenceCount := 0
	for _, profile := range set.Profiles {
		candidate := AccountPoolProfileInputV1{
			AccountProfileRef: profile.AccountProfileRef,
			Status:            profile.Status,
			UnavailableReason: profile.UnavailableReason,
		}
		if profile.Status == AccountPoolProfileEligible {
			candidate.Matrix = make([]AccountPoolRouteProtocolInputV1, 0, len(profile.Matrix))
		}
		for _, entry := range profile.Matrix {
			candidateEntry := AccountPoolRouteProtocolInputV1{
				CanonicalRoute:      entry.CanonicalRoute,
				ResolvedTone:        entry.ResolvedTone,
				Protocol:            entry.Protocol,
				UpstreamAttempts:    entry.UpstreamAttempts,
				CrossAccountResends: entry.CrossAccountResends,
			}
			for _, capability := range entry.Capabilities {
				if len(capability.Evidence) == 0 {
					if capability.Classification != ClassificationInconclusive || capability.EvidenceSHA256 != "" {
						return AccountPoolInputV1{}, 0, validationError("missing_field", "accepted_capability_manifest", "/profiles/matrix/capabilities/evidence")
					}
					continue
				}
				if err := matchAccountPoolManifestExpectedIdentity(capability.Evidence, expected); err != nil {
					return AccountPoolInputV1{}, 0, err
				}
				candidateEntry.Capabilities = append(candidateEntry.Capabilities, AccountPoolCapabilityInputV1{
					CapabilityID:   capability.CapabilityID,
					Evidence:       append(json.RawMessage(nil), capability.Evidence...),
					EvidenceSHA256: capability.EvidenceSHA256,
				})
				evidenceCount++
			}
			candidate.Matrix = append(candidate.Matrix, candidateEntry)
		}
		input.Profiles = append(input.Profiles, candidate)
	}
	return input, evidenceCount, nil
}

func matchAccountPoolManifestExpectedIdentity(raw json.RawMessage, expected AccountPoolEvidenceSetExpected) error {
	var manifest ManifestV1
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return validationError("invalid_json", "capability_manifest", "/profiles/matrix/capabilities/evidence")
	}
	if manifest.NormativeADRSHA256 != expected.NormativeADRSHA256 ||
		manifest.SourceHead != expected.SourceHead ||
		stringValue(manifest.DirtyContentSHA256) != expected.DirtyContentSHA256 ||
		manifest.BinarySHA256 != expected.BinarySHA256 ||
		manifest.HarnessSHA256 != expected.HarnessSHA256 ||
		manifest.EffectiveSettingsSHA256 != expected.EffectiveSettingsSHA256 {
		return validationError("identity_mismatch", "account_pool_accepted_manifest_identity", "/profiles/matrix/capabilities/evidence")
	}
	return nil
}

func validateAccountPoolEvidenceSetExpected(expected AccountPoolEvidenceSetExpected) error {
	for path, value := range map[string]string{
		"/json_sha256":               expected.JSONSHA256,
		"/normative_adr_sha256":      expected.NormativeADRSHA256,
		"/binary_sha256":             expected.BinarySHA256,
		"/harness_sha256":            expected.HarnessSHA256,
		"/effective_settings_sha256": expected.EffectiveSettingsSHA256,
		"/profile_set_sha256":        expected.ProfileSetSHA256,
	} {
		if !sha256Pattern.MatchString(value) {
			return validationError("invalid_expected_identity", "sha256", path)
		}
	}
	if !gitCommitPattern.MatchString(expected.SourceHead) {
		return validationError("invalid_expected_identity", "git_commit_sha", "/source_head")
	}
	if expected.DirtyContentSHA256 != "" && !sha256Pattern.MatchString(expected.DirtyContentSHA256) {
		return validationError("invalid_expected_identity", "dirty_content_sha256", "/dirty_content_sha256")
	}
	return nil
}
