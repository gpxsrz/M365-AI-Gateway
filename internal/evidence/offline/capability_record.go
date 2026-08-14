package offline

import (
	"encoding/json"

	core "m365-native/internal/evidence"
)

type capabilityEvidenceDescriptor struct {
	CanonicalRoute  string
	ResolvedTone    string
	Protocol        string
	MappingEvidence string
	IdentityStatus  string
}

type builtCapabilityEvidence struct {
	CanonicalJSON  json.RawMessage
	EvidenceSHA256 string
	Evidence       core.ValidatedRecord
}

func buildVerifiedCapabilityEvidence(capabilityID, observationSHA256 string, descriptor capabilityEvidenceDescriptor, binding CaptureBinding) (builtCapabilityEvidence, error) {
	var dirtyContentSHA256 *string
	if binding.DirtyContentSHA256 != "" {
		dirty := binding.DirtyContentSHA256
		dirtyContentSHA256 = &dirty
	}
	manifest := core.ManifestV1{
		Schema:                  core.SchemaV1,
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      dirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          descriptor.CanonicalRoute,
		ResolvedTone:            descriptor.ResolvedTone,
		Protocol:                descriptor.Protocol,
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
		MappingEvidence:         descriptor.MappingEvidence,
		IdentityStatus:          descriptor.IdentityStatus,
		CapabilityID:            capabilityID,
		Classification:          core.ClassificationVerified,
		TestExecutionStatus:     core.TestExecutionPass,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return builtCapabilityEvidence{}, validationError("canonicalization_failed", "wp2_evidence_manifest", "/")
	}
	validated, err := core.ValidateCapabilityEvidence(raw, core.IdentitySet{
		NormativeADRSHA256:      binding.NormativeADRSHA256,
		SourceHead:              binding.SourceHead,
		DirtyContentSHA256:      binding.DirtyContentSHA256,
		BinarySHA256:            binding.BinarySHA256,
		HarnessSHA256:           binding.HarnessSHA256,
		ObservationSHA256:       observationSHA256,
		CanonicalRoute:          descriptor.CanonicalRoute,
		ResolvedTone:            descriptor.ResolvedTone,
		Protocol:                descriptor.Protocol,
		AccountProfileRef:       binding.AccountProfileRef,
		EffectiveSettingsSHA256: binding.EffectiveSettingsSHA256,
	})
	if err != nil {
		return builtCapabilityEvidence{}, err
	}
	return builtCapabilityEvidence{
		CanonicalJSON:  append(json.RawMessage(nil), validated.CanonicalJSON...),
		EvidenceSHA256: validated.ChecksumSHA256,
		Evidence:       validated,
	}, nil
}
