package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

const (
	AccountPoolInputSchemaV1       = "m365-wp2-account-pool-input/v1"
	AccountPoolEvidenceSetSchemaV1 = "m365-wp2-account-pool-evidence-set/v1"
	MaxAccountPoolInputBytes       = 8 * 1024 * 1024
)

type AccountPoolProfileStatus string

const (
	AccountPoolProfileEligible    AccountPoolProfileStatus = "eligible"
	AccountPoolProfileUnavailable AccountPoolProfileStatus = "unavailable"
)

type AccountPoolCapabilityInputV1 struct {
	CapabilityID   string          `json:"capability_id"`
	Evidence       json.RawMessage `json:"evidence"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
}

type AccountPoolRouteProtocolInputV1 struct {
	CanonicalRoute      string                         `json:"canonical_route"`
	ResolvedTone        string                         `json:"resolved_tone"`
	Protocol            string                         `json:"protocol"`
	UpstreamAttempts    int                            `json:"upstream_attempts"`
	CrossAccountResends int                            `json:"cross_account_resends"`
	Capabilities        []AccountPoolCapabilityInputV1 `json:"capabilities"`
}

type AccountPoolProfileInputV1 struct {
	AccountProfileRef string                            `json:"account_profile_ref"`
	Status            AccountPoolProfileStatus          `json:"status"`
	UnavailableReason string                            `json:"unavailable_reason,omitempty"`
	Matrix            []AccountPoolRouteProtocolInputV1 `json:"matrix,omitempty"`
}

type AccountPoolInputV1 struct {
	Schema   string                      `json:"schema"`
	Profiles []AccountPoolProfileInputV1 `json:"profiles"`
}

type AccountPoolCapabilityRecordV1 struct {
	CapabilityID   string          `json:"capability_id"`
	Classification Classification  `json:"classification"`
	Evidence       json.RawMessage `json:"evidence,omitempty"`
	EvidenceSHA256 string          `json:"evidence_sha256,omitempty"`
}

type AccountPoolProfileMatrixEntryV1 struct {
	CanonicalRoute      string                          `json:"canonical_route"`
	ResolvedTone        string                          `json:"resolved_tone"`
	Protocol            string                          `json:"protocol"`
	RouteEligibility    Classification                  `json:"route_eligibility"`
	UpstreamAttempts    int                             `json:"upstream_attempts"`
	CrossAccountResends int                             `json:"cross_account_resends"`
	Capabilities        []AccountPoolCapabilityRecordV1 `json:"capabilities"`
}

type AccountPoolProfileRecordV1 struct {
	AccountProfileRef string                            `json:"account_profile_ref"`
	Status            AccountPoolProfileStatus          `json:"status"`
	UnavailableReason string                            `json:"unavailable_reason,omitempty"`
	Matrix            []AccountPoolProfileMatrixEntryV1 `json:"matrix,omitempty"`
}

type AccountPoolGlobalCapabilityClaimV1 struct {
	CapabilityID             string         `json:"capability_id"`
	Classification           Classification `json:"classification"`
	SupportingEvidenceSHA256 []string       `json:"supporting_evidence_sha256"`
}

type AccountPoolGlobalClaimV1 struct {
	CanonicalRoute          string                               `json:"canonical_route"`
	ResolvedTone            string                               `json:"resolved_tone"`
	Protocol                string                               `json:"protocol"`
	EligibleProfileCount    int                                  `json:"eligible_profile_count"`
	UnavailableProfileCount int                                  `json:"unavailable_profile_count"`
	RouteEligibility        Classification                       `json:"route_eligibility"`
	AccountDependent        bool                                 `json:"account_dependent"`
	Capabilities            []AccountPoolGlobalCapabilityClaimV1 `json:"capabilities"`
}

type AccountPoolEvidenceSetV1 struct {
	Schema           string                       `json:"schema"`
	ProfileSetSHA256 string                       `json:"profile_set_sha256"`
	Profiles         []AccountPoolProfileRecordV1 `json:"profiles"`
	GlobalClaims     []AccountPoolGlobalClaimV1   `json:"global_claims"`
}

var allowedAccountPoolInputFields = map[string]struct{}{
	"schema":   {},
	"profiles": {},
}

var accountPoolCapabilityOrder = WP2VerifiableCapabilityIDs()

var allowedUnavailableReasons = map[string]struct{}{
	"profile_not_ready": {},
	"token_unavailable": {},
	"excluded_by_input": {},
}

func BuildAccountPoolEvidence(raw []byte) (AccountPoolEvidenceSetV1, error) {
	if len(raw) == 0 {
		return AccountPoolEvidenceSetV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxAccountPoolInputBytes {
		return AccountPoolEvidenceSetV1{}, validationError("evidence_too_large", "account_pool_input_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedAccountPoolInputFields); err != nil {
		return AccountPoolEvidenceSetV1{}, err
	}
	var input AccountPoolInputV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return AccountPoolEvidenceSetV1{}, validationError("unknown_field", "closed_schema", "/")
		}
		return AccountPoolEvidenceSetV1{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return AccountPoolEvidenceSetV1{}, err
	}
	if input.Schema != AccountPoolInputSchemaV1 {
		return AccountPoolEvidenceSetV1{}, validationError("invalid_schema", "versioned_schema", "/schema")
	}
	if len(input.Profiles) == 0 {
		return AccountPoolEvidenceSetV1{}, validationError("missing_field", "account_pool_profiles", "/profiles")
	}

	profiles := make([]AccountPoolProfileRecordV1, 0, len(input.Profiles))
	seenProfiles := make(map[string]struct{}, len(input.Profiles))
	for _, candidate := range input.Profiles {
		profile, err := validateAccountPoolProfile(candidate)
		if err != nil {
			return AccountPoolEvidenceSetV1{}, err
		}
		if _, exists := seenProfiles[profile.AccountProfileRef]; exists {
			return AccountPoolEvidenceSetV1{}, validationError("duplicate_identity", "account_profile_ref", "/profiles")
		}
		seenProfiles[profile.AccountProfileRef] = struct{}{}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].AccountProfileRef < profiles[j].AccountProfileRef })
	if err := validateAccountPoolAcceptedInputSet(profiles); err != nil {
		return AccountPoolEvidenceSetV1{}, err
	}

	profileIdentity, err := json.Marshal(profiles)
	if err != nil {
		return AccountPoolEvidenceSetV1{}, validationError("canonicalization_failed", "account_pool_profile_set", "/profiles")
	}
	profileDigest := sha256.Sum256(profileIdentity)

	return AccountPoolEvidenceSetV1{
		Schema:           AccountPoolEvidenceSetSchemaV1,
		ProfileSetSHA256: hex.EncodeToString(profileDigest[:]),
		Profiles:         profiles,
		GlobalClaims:     deriveAccountPoolGlobalClaims(profiles),
	}, nil
}

func validateAccountPoolProfile(candidate AccountPoolProfileInputV1) (AccountPoolProfileRecordV1, error) {
	if !opaqueRefPattern.MatchString(candidate.AccountProfileRef) {
		return AccountPoolProfileRecordV1{}, validationError("invalid_identity", "opaque_account_profile", "/profiles/account_profile_ref")
	}
	profile := AccountPoolProfileRecordV1{
		AccountProfileRef: candidate.AccountProfileRef,
		Status:            candidate.Status,
		UnavailableReason: candidate.UnavailableReason,
		Matrix:            []AccountPoolProfileMatrixEntryV1{},
	}
	switch candidate.Status {
	case AccountPoolProfileUnavailable:
		if _, ok := allowedUnavailableReasons[candidate.UnavailableReason]; !ok {
			return AccountPoolProfileRecordV1{}, validationError("invalid_enum", "unavailable_reason", "/profiles/unavailable_reason")
		}
		if len(candidate.Matrix) != 0 {
			return AccountPoolProfileRecordV1{}, validationError("invalid_observation", "unavailable_profile_has_no_matrix", "/profiles/matrix")
		}
		return profile, nil
	case AccountPoolProfileEligible:
		if candidate.UnavailableReason != "" {
			return AccountPoolProfileRecordV1{}, validationError("invalid_observation", "eligible_profile_has_no_unavailable_reason", "/profiles/unavailable_reason")
		}
		if len(candidate.Matrix) == 0 {
			return AccountPoolProfileRecordV1{}, validationError("missing_field", "eligible_profile_matrix", "/profiles/matrix")
		}
	default:
		return AccountPoolProfileRecordV1{}, validationError("invalid_enum", "account_profile_status", "/profiles/status")
	}

	seenEntries := map[string]struct{}{}
	for _, candidateEntry := range candidate.Matrix {
		entry, err := validateAccountPoolEntry(candidate.AccountProfileRef, candidateEntry)
		if err != nil {
			return AccountPoolProfileRecordV1{}, err
		}
		key := accountPoolEntryKey(entry.CanonicalRoute, entry.Protocol)
		if _, exists := seenEntries[key]; exists {
			return AccountPoolProfileRecordV1{}, validationError("duplicate_identity", "route_protocol_entry", "/profiles/matrix")
		}
		seenEntries[key] = struct{}{}
		profile.Matrix = append(profile.Matrix, entry)
	}
	sort.Slice(profile.Matrix, func(i, j int) bool {
		left, right := profile.Matrix[i], profile.Matrix[j]
		if left.CanonicalRoute != right.CanonicalRoute {
			return left.CanonicalRoute < right.CanonicalRoute
		}
		return left.Protocol < right.Protocol
	})
	return profile, nil
}

func validateAccountPoolEntry(profileRef string, candidate AccountPoolRouteProtocolInputV1) (AccountPoolProfileMatrixEntryV1, error) {
	if !routePattern.MatchString(candidate.CanonicalRoute) {
		return AccountPoolProfileMatrixEntryV1{}, validationError("invalid_route", "canonical_route", "/profiles/matrix/canonical_route")
	}
	if !tonePattern.MatchString(candidate.ResolvedTone) {
		return AccountPoolProfileMatrixEntryV1{}, validationError("invalid_tone", "resolved_tone", "/profiles/matrix/resolved_tone")
	}
	if _, ok := allowedProtocols[candidate.Protocol]; !ok {
		return AccountPoolProfileMatrixEntryV1{}, validationError("invalid_enum", "protocol", "/profiles/matrix/protocol")
	}
	if candidate.UpstreamAttempts < 0 || candidate.UpstreamAttempts > 1 {
		return AccountPoolProfileMatrixEntryV1{}, validationError("invalid_observation", "single_profile_upstream_attempt", "/profiles/matrix/upstream_attempts")
	}
	if candidate.CrossAccountResends != 0 {
		return AccountPoolProfileMatrixEntryV1{}, validationError("invalid_observation", "no_cross_account_resend", "/profiles/matrix/cross_account_resends")
	}

	byCapability := make(map[string]AccountPoolCapabilityRecordV1, len(candidate.Capabilities))
	for _, capability := range candidate.Capabilities {
		if _, allowed := wp2VerifiableCapabilities[capability.CapabilityID]; !allowed {
			return AccountPoolProfileMatrixEntryV1{}, validationError("verification_scope_forbidden", "wp2_verified_capability", "/profiles/matrix/capabilities/capability_id")
		}
		if _, exists := byCapability[capability.CapabilityID]; exists {
			return AccountPoolProfileMatrixEntryV1{}, validationError("duplicate_identity", "capability_id", "/profiles/matrix/capabilities")
		}
		record, err := validateAccountPoolCapability(profileRef, candidate, capability)
		if err != nil {
			return AccountPoolProfileMatrixEntryV1{}, err
		}
		byCapability[capability.CapabilityID] = record
	}

	capabilities := make([]AccountPoolCapabilityRecordV1, 0, len(accountPoolCapabilityOrder))
	for _, capabilityID := range accountPoolCapabilityOrder {
		record, ok := byCapability[capabilityID]
		if !ok {
			record = AccountPoolCapabilityRecordV1{CapabilityID: capabilityID, Classification: ClassificationInconclusive}
		}
		capabilities = append(capabilities, record)
	}
	return AccountPoolProfileMatrixEntryV1{
		CanonicalRoute:      candidate.CanonicalRoute,
		ResolvedTone:        candidate.ResolvedTone,
		Protocol:            candidate.Protocol,
		RouteEligibility:    deriveRouteEligibility(capabilities),
		UpstreamAttempts:    candidate.UpstreamAttempts,
		CrossAccountResends: candidate.CrossAccountResends,
		Capabilities:        capabilities,
	}, nil
}

func validateAccountPoolCapability(profileRef string, entry AccountPoolRouteProtocolInputV1, candidate AccountPoolCapabilityInputV1) (AccountPoolCapabilityRecordV1, error) {
	if len(candidate.Evidence) == 0 || candidate.EvidenceSHA256 == "" {
		return AccountPoolCapabilityRecordV1{}, validationError("missing_field", "accepted_capability_manifest", "/profiles/matrix/capabilities/evidence")
	}
	var manifest ManifestV1
	if err := json.Unmarshal(candidate.Evidence, &manifest); err != nil {
		return AccountPoolCapabilityRecordV1{}, validationError("invalid_json", "capability_manifest", "/profiles/matrix/capabilities/evidence")
	}
	if manifest.CapabilityID != candidate.CapabilityID || manifest.AccountProfileRef != profileRef || manifest.CanonicalRoute != entry.CanonicalRoute || manifest.ResolvedTone != entry.ResolvedTone || manifest.Protocol != entry.Protocol {
		return AccountPoolCapabilityRecordV1{}, validationError("identity_mismatch", "account_pool_capability_scope", "/profiles/matrix/capabilities/evidence")
	}
	validated, err := ValidateCapabilityEvidence(candidate.Evidence, IdentitySet{
		NormativeADRSHA256:      manifest.NormativeADRSHA256,
		SourceHead:              manifest.SourceHead,
		DirtyContentSHA256:      stringValue(manifest.DirtyContentSHA256),
		BinarySHA256:            manifest.BinarySHA256,
		HarnessSHA256:           manifest.HarnessSHA256,
		ObservationSHA256:       manifest.ObservationSHA256,
		CanonicalRoute:          manifest.CanonicalRoute,
		ResolvedTone:            manifest.ResolvedTone,
		Protocol:                manifest.Protocol,
		AccountProfileRef:       manifest.AccountProfileRef,
		EffectiveSettingsSHA256: manifest.EffectiveSettingsSHA256,
	})
	if err != nil {
		return AccountPoolCapabilityRecordV1{}, err
	}
	if validated.ChecksumSHA256 != candidate.EvidenceSHA256 {
		return AccountPoolCapabilityRecordV1{}, validationError("identity_mismatch", "capability_manifest_sha256", "/profiles/matrix/capabilities/evidence_sha256")
	}
	return AccountPoolCapabilityRecordV1{
		CapabilityID:   candidate.CapabilityID,
		Classification: manifest.Classification,
		Evidence:       append(json.RawMessage(nil), validated.CanonicalJSON...),
		EvidenceSHA256: validated.ChecksumSHA256,
	}, nil
}

type accountPoolAcceptedInputIdentity struct {
	NormativeADRSHA256      string
	SourceHead              string
	DirtyContentSHA256      string
	BinarySHA256            string
	HarnessSHA256           string
	EffectiveSettingsSHA256 string
}

func validateAccountPoolAcceptedInputSet(profiles []AccountPoolProfileRecordV1) error {
	var expected *accountPoolAcceptedInputIdentity
	for _, profile := range profiles {
		for _, entry := range profile.Matrix {
			for _, capability := range entry.Capabilities {
				if len(capability.Evidence) == 0 {
					continue
				}
				var manifest ManifestV1
				if err := json.Unmarshal(capability.Evidence, &manifest); err != nil {
					return validationError("invalid_json", "capability_manifest", "/profiles/matrix/capabilities/evidence")
				}
				identity := accountPoolAcceptedInputIdentity{
					NormativeADRSHA256:      manifest.NormativeADRSHA256,
					SourceHead:              manifest.SourceHead,
					DirtyContentSHA256:      stringValue(manifest.DirtyContentSHA256),
					BinarySHA256:            manifest.BinarySHA256,
					HarnessSHA256:           manifest.HarnessSHA256,
					EffectiveSettingsSHA256: manifest.EffectiveSettingsSHA256,
				}
				if expected == nil {
					expected = &identity
					continue
				}
				if *expected != identity {
					return validationError("identity_mismatch", "account_pool_accepted_input_set", "/profiles/matrix/capabilities/evidence")
				}
			}
		}
	}
	return nil
}

func deriveRouteEligibility(capabilities []AccountPoolCapabilityRecordV1) Classification {
	values := map[string]Classification{}
	for _, capability := range capabilities {
		values[capability.CapabilityID] = capability.Classification
	}
	identity, mapping := values["route_identity"], values["route_mapping"]
	if identity == ClassificationVerified && mapping == ClassificationVerified {
		return ClassificationVerified
	}
	if identity == ClassificationConfirmedDefect || mapping == ClassificationConfirmedDefect {
		return ClassificationConfirmedDefect
	}
	if identity == ClassificationUnsupported || mapping == ClassificationUnsupported {
		return ClassificationUnsupported
	}
	return ClassificationInconclusive
}

func deriveAccountPoolGlobalClaims(profiles []AccountPoolProfileRecordV1) []AccountPoolGlobalClaimV1 {
	eligibleProfiles := make([]AccountPoolProfileRecordV1, 0, len(profiles))
	unavailableCount := 0
	keys := map[string]struct{}{}
	for _, profile := range profiles {
		if profile.Status == AccountPoolProfileUnavailable {
			unavailableCount++
			continue
		}
		eligibleProfiles = append(eligibleProfiles, profile)
		for _, entry := range profile.Matrix {
			keys[accountPoolEntryKey(entry.CanonicalRoute, entry.Protocol)] = struct{}{}
		}
	}
	if len(eligibleProfiles) == 0 {
		return []AccountPoolGlobalClaimV1{}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	claims := make([]AccountPoolGlobalClaimV1, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		parts := strings.SplitN(key, "\x00", 2)
		route, protocol := parts[0], parts[1]
		tone := ""
		routeValues := make([]Classification, 0, len(eligibleProfiles))
		capabilityValues := make(map[string][]Classification, len(accountPoolCapabilityOrder))
		capabilityEvidence := make(map[string][]string, len(accountPoolCapabilityOrder))
		capabilityMissing := make(map[string]bool, len(accountPoolCapabilityOrder))
		accountDependent := false
		toneConflict := false
		for _, profile := range eligibleProfiles {
			entry, found := findAccountPoolEntry(profile.Matrix, route, protocol)
			if !found {
				routeValues = append(routeValues, ClassificationInconclusive)
				for _, capabilityID := range accountPoolCapabilityOrder {
					capabilityValues[capabilityID] = append(capabilityValues[capabilityID], ClassificationInconclusive)
					capabilityMissing[capabilityID] = true
				}
				continue
			}
			if tone == "" {
				tone = entry.ResolvedTone
			} else if tone != entry.ResolvedTone {
				accountDependent = true
				toneConflict = true
			}
			routeValues = append(routeValues, entry.RouteEligibility)
			for _, capability := range entry.Capabilities {
				capabilityValues[capability.CapabilityID] = append(capabilityValues[capability.CapabilityID], capability.Classification)
				if capability.EvidenceSHA256 != "" {
					capabilityEvidence[capability.CapabilityID] = append(capabilityEvidence[capability.CapabilityID], capability.EvidenceSHA256)
				} else {
					capabilityMissing[capability.CapabilityID] = true
				}
			}
		}
		routeClassification, routeVaries := intersectAccountPoolClassifications(routeValues)
		if toneConflict && routeClassification == ClassificationVerified {
			routeClassification = ClassificationInconclusive
			routeVaries = true
		}
		accountDependent = accountDependent || routeVaries
		capabilities := make([]AccountPoolGlobalCapabilityClaimV1, 0, len(accountPoolCapabilityOrder))
		for _, capabilityID := range accountPoolCapabilityOrder {
			classification, varies := intersectAccountPoolClassifications(capabilityValues[capabilityID])
			if capabilityMissing[capabilityID] {
				classification = ClassificationInconclusive
				varies = true
			}
			if classification == ClassificationVerified && (routeClassification != ClassificationVerified || toneConflict) {
				classification = ClassificationInconclusive
				varies = true
			}
			accountDependent = accountDependent || varies
			evidenceHashes := uniqueSortedStrings(capabilityEvidence[capabilityID])
			capabilities = append(capabilities, AccountPoolGlobalCapabilityClaimV1{
				CapabilityID:             capabilityID,
				Classification:           classification,
				SupportingEvidenceSHA256: evidenceHashes,
			})
		}
		claims = append(claims, AccountPoolGlobalClaimV1{
			CanonicalRoute:          route,
			ResolvedTone:            tone,
			Protocol:                protocol,
			EligibleProfileCount:    len(eligibleProfiles),
			UnavailableProfileCount: unavailableCount,
			RouteEligibility:        routeClassification,
			AccountDependent:        accountDependent,
			Capabilities:            capabilities,
		})
	}
	return claims
}

func intersectAccountPoolClassifications(values []Classification) (Classification, bool) {
	if len(values) == 0 {
		return ClassificationInconclusive, false
	}
	first := values[0]
	for _, value := range values[1:] {
		if value != first {
			return ClassificationInconclusive, true
		}
	}
	return first, false
}

func findAccountPoolEntry(entries []AccountPoolProfileMatrixEntryV1, route, protocol string) (AccountPoolProfileMatrixEntryV1, bool) {
	for _, entry := range entries {
		if entry.CanonicalRoute == route && entry.Protocol == protocol {
			return entry, true
		}
	}
	return AccountPoolProfileMatrixEntryV1{}, false
}

func accountPoolEntryKey(route, protocol string) string { return route + "\x00" + protocol }

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
