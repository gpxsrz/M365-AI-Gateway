package evidence

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
)

const (
	HistoricalBaselineSourceSchemaV1          = "m365-wp2-historical-baseline-source/v1"
	HistoricalBaselineInventorySchemaV1       = "m365-wp2-historical-baseline-inventory/v1"
	MaxHistoricalBaselineInventoryBytes       = 1024 * 1024
	historicalNormativeADRSHA256              = "4d510571b3b59762e5b56a4cda55d330729c28c7cc25a2bf3c9397f0a249918a"
	historicalBinarySHA256V1                  = "54b917ad67abbcc8072919ba260e02bf8194741a9f00a055a12f5c53739b2b95"
	historicalBinaryBytesV1             int64 = 18530564
	historicalBinarySourceHeadV1              = "5c8d948da509bba4a33e39c2f6eec30e2163c4fd"
)

var historicalArtifactIdentitiesV1 = []HistoricalArtifactIdentityV1{
	{Path: "final-matrix.json", Bytes: 17640, SHA256: "050336325f13ad7e2cbcff0d58f382cc9478e429344cf884da9a48fd52413b64"},
	{Path: "final-matrix.md", Bytes: 4515, SHA256: "b0173d33d7c43e9097c1412c8e9f97cdb9b282c161454fb02a6e86c6420fd96f"},
	{Path: "issues.jsonl", Bytes: 13090, SHA256: "e401529d7a6b47b213778141d364bba83594ac413b4cc8041269b504d191589d"},
	{Path: "phase1-details.json", Bytes: 9060, SHA256: "f2b844a8c7aaaea36ae0b4fe28f9d60eda2d7792b72915960bff669a02884166"},
	{Path: "phase2-details.json", Bytes: 5435, SHA256: "1a38eb73000233bfd3e3aa0bf953eede3460ff06f56fbf1e8abb23b81dddc60d"},
	{Path: "phase3-details.json", Bytes: 7877, SHA256: "03347a0c233e6cd5a6dec942b1bb0a029dadfad801a66d08e8fb7b50added5a9"},
	{Path: "phase4-details.json", Bytes: 3922, SHA256: "f2d201b44f1f40119576f5675f424f963595f588cc1a9e018ae5bb5a5c77ea21"},
	{Path: "phase5-context-details.json", Bytes: 837, SHA256: "ab84677cf3e65fe21f0692b8e40c6e4dcd186a0ce9904c50774d946279630302"},
	{Path: "progress.json", Bytes: 16540, SHA256: "c8dae47d9e935aa03a1e2f8ff0c634f4141462a5565c5a0bc5d8bc2259f63119"},
}

var historicalCapabilityStatuses = map[string]TestExecutionStatus{
	"anthropic_nonstream":                    TestExecutionPass,
	"anthropic_streaming":                    TestExecutionPass,
	"anthropic_tool_calling":                 TestExecutionPass,
	"anthropic_tool_result_continuation":     TestExecutionPass,
	"anthropic_tool_streaming":               TestExecutionPass,
	"api_key_account_restart_persistence":    TestExecutionPass,
	"api_key_auth":                           TestExecutionPass,
	"apply_patch_freeform_tool":              TestExecutionFail,
	"auto_route_reasoning_invariance":        TestExecutionFail,
	"bing_web_search":                        TestExecutionPass,
	"chat_streaming":                         TestExecutionPass,
	"context_boundary":                       TestExecutionPass,
	"context_overflow_rejection":             TestExecutionFail,
	"custom_exec_native_search_preservation": TestExecutionFail,
	"discord_gateway":                        TestExecutionPass,
	"empty_response_handling":                TestExecutionFail,
	"file_attachment":                        TestExecutionFail,
	"file_read_api":                          TestExecutionFail,
	"file_upload_api":                        TestExecutionFail,
	"function_calling":                       TestExecutionPass,
	"hermes_cli":                             TestExecutionPass,
	"image_detail_original":                  TestExecutionFail,
	"image_generation":                       TestExecutionFail,
	"invalid_reasoning_rejection":            TestExecutionPass,
	"json_response_format":                   TestExecutionPass,
	"json_schema_strict":                     TestExecutionFail,
	"max_tokens_enforcement":                 TestExecutionFail,
	"model_catalog":                          TestExecutionPass,
	"multi_turn_conversation_ids":            TestExecutionPass,
	"nonstream_text":                         TestExecutionPass,
	"openai_web_search_tool":                 TestExecutionFail,
	"output_boundary":                        TestExecutionFail,
	"parallel_tool_calls":                    TestExecutionFail,
	"reasoning_levels":                       TestExecutionPass,
	"reasoning_summary":                      TestExecutionFail,
	"responses_custom_exec_tool":             TestExecutionPass,
	"responses_function_calling":             TestExecutionPass,
	"responses_nonstream":                    TestExecutionPass,
	"responses_previous_response_id":         TestExecutionPass,
	"responses_streaming":                    TestExecutionPass,
	"responses_tool_result_continuation":     TestExecutionPass,
	"responses_tool_streaming":               TestExecutionPass,
	"session_key_persistence":                TestExecutionPass,
	"sidecar_restart_persistence":            TestExecutionFail,
	"tool_result_continuation":               TestExecutionPass,
	"unknown_model_fail_closed":              TestExecutionFail,
	"verbosity_control":                      TestExecutionFail,
	"vision_image_input":                     TestExecutionPass,
}

type historicalCapabilityPolicy struct {
	AcceptedProtocol     string
	ReplayKind           string
	ReplayScope          string
	ReplayArtifactPath   string
	ReplayClassification Classification
	RationaleCode        string
	Rationale            string
	DeferredClaims       []string
}

var historicalCapabilityPolicies = map[string]historicalCapabilityPolicy{
	"nonstream_text":      {AcceptedProtocol: "openai_chat_completions_nonstream"},
	"responses_nonstream": {AcceptedProtocol: "openai_responses_nonstream"},
	"anthropic_nonstream": {AcceptedProtocol: "anthropic_messages_nonstream"},
	"unknown_model_fail_closed": {
		ReplayKind: "unknown_model_returned_success", ReplayScope: "sidecar_route_validation", ReplayArtifactPath: "phase1-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records HTTP 200 with non-empty content for an unknown model, a sidecar fail-closed routing violation.",
	},
	"empty_response_handling": {
		ReplayKind: "empty_response_returned_success", ReplayScope: "sidecar_usable_result_validation", ReplayArtifactPath: "phase1-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records HTTP 200 with an empty deliverable, a sidecar usable-result validation violation.",
	},
	"max_tokens_enforcement": {
		ReplayKind: "max_tokens_not_enforced", ReplayScope: "sidecar_configured_output_limit", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records output far beyond max_tokens=1, proving the sidecar did not enforce its configured output contract.",
	},
	"custom_exec_native_search_preservation": {
		ReplayKind: "custom_exec_native_search_suppressed", ReplayScope: "sidecar_request_policy", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived A/B replay records search signals without URLs or source attribution under the sidecar custom-exec request policy.",
	},
	"auto_route_reasoning_invariance": {
		ReplayKind: "auto_route_reasoning_drift", ReplayScope: "sidecar_route_resolution", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records different route markers for m365-auto reasoning settings while the sidecar lacked an invariance guard.",
	},
	"context_overflow_rejection": {
		ReplayKind: "configured_context_limit_not_enforced", ReplayScope: "sidecar_configured_context_limit", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records acceptance beyond the sidecar configured context limit; this does not claim a Microsoft upstream limit.", DeferredClaims: []string{"upstream_context_limit"},
	},
	"image_detail_original": {
		ReplayKind: "image_detail_not_forwarded_or_validated", ReplayScope: "sidecar_image_detail_validation", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records that detail was not forwarded and an invalid detail value was accepted; image-processing semantics remain deferred.", DeferredClaims: []string{"image_detail_original_semantic_effect"},
	},
	"json_schema_strict": {
		ReplayKind: "strict_json_schema_not_enforced", ReplayScope: "sidecar_json_schema_contract", ReplayArtifactPath: "phase4-details.json", ReplayClassification: ClassificationConfirmedDefect,
		RationaleCode: "archived_sidecar_contract_replay", Rationale: "Archived replay records schema mismatch and acceptance of an invalid schema, proving sidecar strict-schema enforcement failed.",
	},
	"file_upload_api": {
		ReplayKind: "file_upload_endpoint_absent", ReplayScope: "sidecar_http_endpoint", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationUnsupported,
		RationaleCode: "sidecar_endpoint_absent", Rationale: "Archived replay records HTTP 404 for the sidecar endpoint; this account-independent endpoint was not implemented in the tested sidecar.", DeferredClaims: []string{"file_content_capability_acceptance"},
	},
	"file_read_api": {
		ReplayKind: "file_read_endpoint_absent", ReplayScope: "sidecar_http_endpoint", ReplayArtifactPath: "phase3-details.json", ReplayClassification: ClassificationUnsupported,
		RationaleCode: "sidecar_endpoint_absent", Rationale: "Archived replay records HTTP 404 for the sidecar endpoint; this account-independent endpoint was not implemented in the tested sidecar.", DeferredClaims: []string{"file_content_capability_acceptance"},
	},
	"parallel_tool_calls":         {RationaleCode: "single_call_does_not_prove_parallel_support", Rationale: "The best historical sample produced one tool call; this proves neither support nor unsupported status for parallel calls."},
	"output_boundary":             {RationaleCode: "short_output_does_not_prove_limit", Rationale: "A short observed output cannot prove or disprove the advertised output boundary."},
	"reasoning_levels":            {RationaleCode: "accepted_values_do_not_prove_semantics", Rationale: "The historical PASS proves accepted values completed requests, not that reasoning-depth semantics differed as requested."},
	"sidecar_restart_persistence": {RationaleCode: "restart_recall_contract_not_bound", Rationale: "The replay shows lost session recall after restart, but no accepted WP2 contract binds cross-restart conversation persistence."},
	"file_attachment":             {RationaleCode: "attachment_failure_not_account_isolated", Rationale: "The historical attachment failure is not bound to sufficient route, protocol, account, and upstream evidence for defect or unsupported status.", DeferredClaims: []string{"file_content_capability_acceptance"}},
	"image_generation":            {RationaleCode: "image_generation_failure_not_isolated", Rationale: "The historical failure does not distinguish sidecar behavior from account or upstream image-generation conditions."},
	"openai_web_search_tool":      {RationaleCode: "m365_search_is_not_openai_tool_semantics", Rationale: "Observed M365 search or citations do not establish OpenAI web_search tool-call compatibility or unsupported status."},
	"reasoning_summary":           {RationaleCode: "advanced_parameter_or_tool_deferred", Rationale: "The historical result concerns an advanced parameter or tool outside the WP2 VERIFIED boundary and lacks accepted defect or unsupported evidence."},
	"verbosity_control":           {RationaleCode: "advanced_parameter_or_tool_deferred", Rationale: "The historical result concerns an advanced parameter or tool outside the WP2 VERIFIED boundary and lacks accepted defect or unsupported evidence."},
	"apply_patch_freeform_tool":   {RationaleCode: "advanced_parameter_or_tool_deferred", Rationale: "The historical result concerns an advanced parameter or tool outside the WP2 VERIFIED boundary and lacks accepted defect or unsupported evidence."},
	"model_catalog":               {RationaleCode: "catalog_endpoint_is_not_claim_correctness", Rationale: "The endpoint observation does not verify the correctness of every catalog identity or capability claim."},
}

type HistoricalBinaryIdentityV1 struct {
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	SourceHead     string `json:"source_head"`
	SourceModified bool   `json:"source_modified"`
}

type HistoricalArtifactIdentityV1 struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type HistoricalSourceReferenceV1 struct {
	ArtifactPath string `json:"artifact_path"`
	RecordSHA256 string `json:"evidence_sha256"`
}

type HistoricalReplayEvidenceV1 struct {
	Kind         string `json:"kind"`
	Scope        string `json:"scope"`
	ArtifactPath string `json:"artifact_path"`
	RecordSHA256 string `json:"evidence_sha256"`
}

type HistoricalBaselineSourceEntryV1 struct {
	CapabilityID     string                        `json:"capability_id"`
	HistoricalStatus TestExecutionStatus           `json:"historical_status"`
	SourceReferences []HistoricalSourceReferenceV1 `json:"source_references"`
	ReplayEvidence   *HistoricalReplayEvidenceV1   `json:"replay_evidence,omitempty"`
}

type HistoricalBaselineSourceV1 struct {
	Schema             string                            `json:"schema"`
	NormativeADRSHA256 string                            `json:"normative_adr_sha256"`
	HistoricalBinary   HistoricalBinaryIdentityV1        `json:"historical_binary"`
	Artifacts          []HistoricalArtifactIdentityV1    `json:"artifacts"`
	EntryCount         int                               `json:"entry_count"`
	PassCount          int                               `json:"pass_count"`
	FailCount          int                               `json:"fail_count"`
	IssueRecordCount   int                               `json:"issue_record_count"`
	Entries            []HistoricalBaselineSourceEntryV1 `json:"entries"`
}

type ValidatedHistoricalBaselineSource struct {
	Source            HistoricalBaselineSourceV1
	CanonicalJSON     []byte
	ChecksumSHA256    string
	expectedArtifacts []HistoricalArtifactIdentityV1
}

type HistoricalAcceptedSupportV1 struct {
	CanonicalRoute           string         `json:"canonical_route"`
	ResolvedTone             string         `json:"resolved_tone"`
	Protocol                 string         `json:"protocol"`
	CapabilityID             string         `json:"capability_id"`
	Classification           Classification `json:"classification"`
	AccountDependent         bool           `json:"account_dependent"`
	SupportingEvidenceSHA256 []string       `json:"supporting_evidence_sha256"`
}

type HistoricalBaselineDispositionV1 struct {
	CapabilityID     string                        `json:"capability_id"`
	HistoricalStatus TestExecutionStatus           `json:"historical_status"`
	Classification   Classification                `json:"classification"`
	RationaleCode    string                        `json:"rationale_code"`
	Rationale        string                        `json:"rationale"`
	SourceReferences []HistoricalSourceReferenceV1 `json:"source_references"`
	ReplayEvidence   *HistoricalReplayEvidenceV1   `json:"replay_evidence,omitempty"`
	AcceptedSupport  []HistoricalAcceptedSupportV1 `json:"accepted_support"`
	MissingEvidence  []string                      `json:"missing_evidence"`
	DeferredClaims   []string                      `json:"deferred_claims"`
}

type HistoricalClassificationCountsV1 struct {
	Verified        int `json:"verified"`
	ConfirmedDefect int `json:"confirmed_defect"`
	Unsupported     int `json:"unsupported"`
	Inconclusive    int `json:"inconclusive"`
}

type HistoricalBaselineInventoryV1 struct {
	Schema                 string                            `json:"schema"`
	NormativeADRSHA256     string                            `json:"normative_adr_sha256"`
	HistoricalSourceSHA256 string                            `json:"historical_source_sha256"`
	AcceptedCatalogSHA256  string                            `json:"accepted_catalog_sha256"`
	EntryCount             int                               `json:"entry_count"`
	HistoricalPassCount    int                               `json:"historical_pass_count"`
	HistoricalFailCount    int                               `json:"historical_fail_count"`
	ClassificationCounts   HistoricalClassificationCountsV1  `json:"classification_counts"`
	Entries                []HistoricalBaselineDispositionV1 `json:"entries"`
}

type ValidatedHistoricalBaselineInventory struct {
	Inventory      HistoricalBaselineInventoryV1
	CanonicalJSON  []byte
	ChecksumSHA256 string
}

type HistoricalBaselineInventoryExpected struct {
	HistoricalSource ValidatedHistoricalBaselineSource
	AcceptedCatalog  ValidatedCatalogProjection
}

var allowedHistoricalInventoryFields = map[string]struct{}{
	"schema":                   {},
	"normative_adr_sha256":     {},
	"historical_source_sha256": {},
	"accepted_catalog_sha256":  {},
	"entry_count":              {},
	"historical_pass_count":    {},
	"historical_fail_count":    {},
	"classification_counts":    {},
	"entries":                  {},
}

func buildHistoricalBaselineSource(sourceFS fs.FS, basePath string, binary HistoricalBinaryIdentityV1, expectedArtifacts []HistoricalArtifactIdentityV1) (ValidatedHistoricalBaselineSource, error) {
	if err := validateHistoricalBinaryIdentity(binary); err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	if len(expectedArtifacts) != len(historicalArtifactIdentitiesV1) {
		return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "accepted_historical_artifact_set", "/artifacts")
	}

	artifactBytes := make(map[string][]byte, len(expectedArtifacts))
	artifacts := make([]HistoricalArtifactIdentityV1, 0, len(expectedArtifacts))
	for _, expected := range expectedArtifacts {
		raw, err := fs.ReadFile(sourceFS, path.Join(basePath, expected.Path))
		if err != nil {
			return ValidatedHistoricalBaselineSource{}, validationError("missing_field", "historical_artifact", "/artifacts")
		}
		digest := sha256.Sum256(raw)
		actual := HistoricalArtifactIdentityV1{Path: expected.Path, Bytes: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}
		if actual != expected {
			return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "accepted_historical_artifact", "/artifacts/"+expected.Path)
		}
		artifactBytes[expected.Path] = raw
		artifacts = append(artifacts, actual)
	}

	progressRecords, err := parseHistoricalProgress(artifactBytes["progress.json"])
	if err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	finalRecords, err := parseHistoricalFinalMatrix(artifactBytes["final-matrix.json"])
	if err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	issueRecords, issueCount, err := parseHistoricalIssues(artifactBytes["issues.jsonl"])
	if err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	if issueCount != 27 {
		return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "historical_issue_record_count", "/issue_record_count")
	}

	phaseRecords := make(map[string][]historicalPhaseRecord)
	for _, artifactPath := range []string{
		"phase1-details.json",
		"phase2-details.json",
		"phase3-details.json",
		"phase4-details.json",
		"phase5-context-details.json",
	} {
		records, err := parseHistoricalPhase(artifactPath, artifactBytes[artifactPath])
		if err != nil {
			return ValidatedHistoricalBaselineSource{}, err
		}
		for capabilityID, record := range records {
			if _, known := historicalCapabilityStatuses[capabilityID]; !known {
				return ValidatedHistoricalBaselineSource{}, validationError("invalid_capability", "historical_capability_id", "/entries/capability_id")
			}
			phaseRecords[capabilityID] = append(phaseRecords[capabilityID], record)
		}
	}

	ids := historicalCapabilityIDs()
	entries := make([]HistoricalBaselineSourceEntryV1, 0, len(ids))
	passCount := 0
	failCount := 0
	for _, capabilityID := range ids {
		expectedStatus := historicalCapabilityStatuses[capabilityID]
		progress, ok := progressRecords[capabilityID]
		if !ok || progress.Status != expectedStatus {
			return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "historical_progress_status", "/entries/historical_status")
		}
		final, ok := finalRecords[capabilityID]
		if !ok || final.Status != expectedStatus {
			return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "historical_final_matrix_status", "/entries/historical_status")
		}

		references := []HistoricalSourceReferenceV1{
			{ArtifactPath: "final-matrix.json", RecordSHA256: final.ChecksumSHA256},
			{ArtifactPath: "progress.json", RecordSHA256: progress.ChecksumSHA256},
		}
		references = append(references, issueRecords[capabilityID]...)
		for _, phase := range phaseRecords[capabilityID] {
			references = append(references, HistoricalSourceReferenceV1{
				ArtifactPath: phase.ArtifactPath,
				RecordSHA256: phase.ChecksumSHA256,
			})
		}
		sortHistoricalReferences(references)

		if expectedStatus == TestExecutionPass {
			passCount++
		} else {
			failCount++
		}
		entries = append(entries, HistoricalBaselineSourceEntryV1{
			CapabilityID:     capabilityID,
			HistoricalStatus: expectedStatus,
			SourceReferences: references,
			ReplayEvidence:   historicalReplayEvidence(capabilityID, phaseRecords[capabilityID]),
		})
	}
	if passCount != 29 || failCount != 19 {
		return ValidatedHistoricalBaselineSource{}, validationError("identity_mismatch", "historical_status_counts", "/")
	}

	source := HistoricalBaselineSourceV1{
		Schema:             HistoricalBaselineSourceSchemaV1,
		NormativeADRSHA256: historicalNormativeADRSHA256,
		HistoricalBinary:   binary,
		Artifacts:          artifacts,
		EntryCount:         len(entries),
		PassCount:          passCount,
		FailCount:          failCount,
		IssueRecordCount:   issueCount,
		Entries:            entries,
	}
	canonical, checksum, err := marshalHistoricalValue(source)
	if err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	validated := ValidatedHistoricalBaselineSource{
		Source:            source,
		CanonicalJSON:     canonical,
		ChecksumSHA256:    checksum,
		expectedArtifacts: append([]HistoricalArtifactIdentityV1(nil), expectedArtifacts...),
	}
	if err := validateHistoricalSource(validated); err != nil {
		return ValidatedHistoricalBaselineSource{}, err
	}
	return validated, nil
}

func BuildHistoricalBaselineInventory(source ValidatedHistoricalBaselineSource, catalog ValidatedCatalogProjection) (ValidatedHistoricalBaselineInventory, error) {
	if err := validateHistoricalSource(source); err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}
	if err := validateHistoricalCatalog(catalog); err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}

	entries := make([]HistoricalBaselineDispositionV1, 0, len(source.Source.Entries))
	counts := HistoricalClassificationCountsV1{}
	for _, sourceEntry := range source.Source.Entries {
		entry := classifyHistoricalEntry(sourceEntry, catalog)
		switch entry.Classification {
		case ClassificationVerified:
			counts.Verified++
		case ClassificationConfirmedDefect:
			counts.ConfirmedDefect++
		case ClassificationUnsupported:
			counts.Unsupported++
		case ClassificationInconclusive:
			counts.Inconclusive++
		}
		entries = append(entries, entry)
	}

	inventory := HistoricalBaselineInventoryV1{
		Schema:                 HistoricalBaselineInventorySchemaV1,
		NormativeADRSHA256:     historicalNormativeADRSHA256,
		HistoricalSourceSHA256: source.ChecksumSHA256,
		AcceptedCatalogSHA256:  catalog.ChecksumSHA256,
		EntryCount:             len(entries),
		HistoricalPassCount:    source.Source.PassCount,
		HistoricalFailCount:    source.Source.FailCount,
		ClassificationCounts:   counts,
		Entries:                entries,
	}
	canonical, checksum, err := marshalHistoricalValue(inventory)
	if err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}
	return ValidatedHistoricalBaselineInventory{Inventory: inventory, CanonicalJSON: canonical, ChecksumSHA256: checksum}, nil
}

func ValidateHistoricalBaselineInventory(raw []byte, expected HistoricalBaselineInventoryExpected) (ValidatedHistoricalBaselineInventory, error) {
	if len(raw) == 0 {
		return ValidatedHistoricalBaselineInventory{}, validationError("invalid_json", "single_json_object", "/")
	}
	if len(raw) > MaxHistoricalBaselineInventoryBytes {
		return ValidatedHistoricalBaselineInventory{}, validationError("evidence_too_large", "historical_inventory_size_limit", "/")
	}
	if err := scanClosedJSONContract(raw, allowedHistoricalInventoryFields); err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}

	var inventory HistoricalBaselineInventoryV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ValidatedHistoricalBaselineInventory{}, validationError("unknown_field", "closed_schema", "/")
		}
		return ValidatedHistoricalBaselineInventory{}, validationError("invalid_json", "single_json_object", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}

	derived, err := BuildHistoricalBaselineInventory(expected.HistoricalSource, expected.AcceptedCatalog)
	if err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}
	canonical, checksum, err := marshalHistoricalValue(inventory)
	if err != nil {
		return ValidatedHistoricalBaselineInventory{}, err
	}
	if !bytes.Equal(canonical, derived.CanonicalJSON) {
		return ValidatedHistoricalBaselineInventory{}, validationError("identity_mismatch", "derived_historical_inventory", "/")
	}
	return ValidatedHistoricalBaselineInventory{Inventory: inventory, CanonicalJSON: canonical, ChecksumSHA256: checksum}, nil
}

type historicalRawRecord struct {
	Status         TestExecutionStatus
	ChecksumSHA256 string
}

type historicalPhaseRecord struct {
	ArtifactPath   string
	Raw            json.RawMessage
	ChecksumSHA256 string
}

func parseHistoricalProgress(raw []byte) (map[string]historicalRawRecord, error) {
	var progress struct {
		Version      int                        `json:"version"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &progress); err != nil || progress.Version != 1 {
		return nil, validationError("invalid_json", "historical_progress", "/artifacts/progress.json")
	}
	if len(progress.Capabilities) != len(historicalCapabilityStatuses) {
		return nil, validationError("identity_mismatch", "historical_capability_count", "/entries")
	}
	return parseHistoricalStatusRecords(progress.Capabilities, "historical_progress")
}

func parseHistoricalFinalMatrix(raw []byte) (map[string]historicalRawRecord, error) {
	var matrix struct {
		Version      int               `json:"version"`
		Total        int               `json:"total"`
		StatusCounts map[string]int    `json:"status_counts"`
		Capabilities []json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &matrix); err != nil || matrix.Version != 1 {
		return nil, validationError("invalid_json", "historical_final_matrix", "/artifacts/final-matrix.json")
	}
	if matrix.Total != 48 || len(matrix.Capabilities) != 48 || matrix.StatusCounts["PASS"] != 29 || matrix.StatusCounts["FAIL"] != 19 {
		return nil, validationError("identity_mismatch", "historical_final_matrix_counts", "/artifacts/final-matrix.json")
	}
	records := make(map[string]json.RawMessage, len(matrix.Capabilities))
	for _, rawRecord := range matrix.Capabilities {
		var identity struct {
			Capability string `json:"capability"`
		}
		if err := json.Unmarshal(rawRecord, &identity); err != nil || identity.Capability == "" {
			return nil, validationError("invalid_json", "historical_final_matrix_entry", "/entries")
		}
		if _, exists := records[identity.Capability]; exists {
			return nil, validationError("duplicate_identity", "historical_capability_id", "/entries")
		}
		records[identity.Capability] = rawRecord
	}
	return parseHistoricalStatusRecords(records, "historical_final_matrix")
}

func parseHistoricalStatusRecords(rawRecords map[string]json.RawMessage, rule string) (map[string]historicalRawRecord, error) {
	result := make(map[string]historicalRawRecord, len(rawRecords))
	for capabilityID, rawRecord := range rawRecords {
		expected, known := historicalCapabilityStatuses[capabilityID]
		if !known {
			return nil, validationError("invalid_capability", "historical_capability_id", "/entries/capability_id")
		}
		var record struct {
			Status TestExecutionStatus `json:"status"`
		}
		if err := json.Unmarshal(rawRecord, &record); err != nil || record.Status != expected {
			return nil, validationError("identity_mismatch", rule+"_status", "/entries/historical_status")
		}
		checksum, err := canonicalJSONSHA256(rawRecord)
		if err != nil {
			return nil, err
		}
		result[capabilityID] = historicalRawRecord{Status: record.Status, ChecksumSHA256: checksum}
	}
	return result, nil
}

func parseHistoricalIssues(raw []byte) (map[string][]HistoricalSourceReferenceV1, int, error) {
	result := make(map[string][]HistoricalSourceReferenceV1)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), MaxHistoricalBaselineInventoryBytes)
	count := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var issue struct {
			Capability string `json:"capability"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			return nil, 0, validationError("invalid_json", "historical_issue_record", "/artifacts/issues.jsonl")
		}
		if _, known := historicalCapabilityStatuses[issue.Capability]; !known {
			return nil, 0, validationError("invalid_capability", "historical_issue_capability", "/artifacts/issues.jsonl")
		}
		checksum, err := canonicalJSONSHA256(line)
		if err != nil {
			return nil, 0, err
		}
		result[issue.Capability] = append(result[issue.Capability], HistoricalSourceReferenceV1{
			ArtifactPath: "issues.jsonl",
			RecordSHA256: checksum,
		})
		count++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, validationError("invalid_json", "historical_issue_record", "/artifacts/issues.jsonl")
	}
	for capabilityID := range result {
		sortHistoricalReferences(result[capabilityID])
	}
	return result, count, nil
}

func parseHistoricalPhase(artifactPath string, raw []byte) (map[string]historicalPhaseRecord, error) {
	var phase struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(raw, &phase); err != nil {
		return nil, validationError("invalid_json", "historical_phase_record", "/artifacts/"+artifactPath)
	}
	result := make(map[string]historicalPhaseRecord, len(phase.Results))
	for capabilityID, rawRecord := range phase.Results {
		checksum, err := canonicalJSONSHA256(rawRecord)
		if err != nil {
			return nil, err
		}
		result[capabilityID] = historicalPhaseRecord{ArtifactPath: artifactPath, Raw: rawRecord, ChecksumSHA256: checksum}
	}
	return result, nil
}

func historicalReplayEvidence(capabilityID string, records []historicalPhaseRecord) *HistoricalReplayEvidenceV1 {
	policy, known := historicalCapabilityPolicies[capabilityID]
	if !known || policy.ReplayKind == "" {
		return nil
	}
	byPath := make(map[string]historicalPhaseRecord, len(records))
	for _, record := range records {
		byPath[record.ArtifactPath] = record
	}
	makeReplay := func(condition bool) *HistoricalReplayEvidenceV1 {
		record, ok := byPath[policy.ReplayArtifactPath]
		if !condition || !ok {
			return nil
		}
		return &HistoricalReplayEvidenceV1{
			Kind:         policy.ReplayKind,
			Scope:        policy.ReplayScope,
			ArtifactPath: policy.ReplayArtifactPath,
			RecordSHA256: record.ChecksumSHA256,
		}
	}

	switch capabilityID {
	case "unknown_model_fail_closed":
		record, ok := byPath["phase1-details.json"]
		var value struct {
			HTTPStatus    int `json:"http_status"`
			ContentLength int `json:"content_length"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && value.HTTPStatus == 200 && value.ContentLength > 0)
	case "empty_response_handling":
		record, ok := byPath["phase1-details.json"]
		var value struct {
			HTTPStatus    int `json:"http_status"`
			ContentLength int `json:"content_length"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && value.HTTPStatus == 200 && value.ContentLength == 0)
	case "max_tokens_enforcement":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			Cases []struct {
				MaxTokens     int `json:"max_tokens"`
				HTTPStatus    int `json:"http_status"`
				ContentLength int `json:"content_length"`
			} `json:"cases"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		violated := false
		for _, candidate := range value.Cases {
			if candidate.MaxTokens == 1 && candidate.HTTPStatus == 200 && candidate.ContentLength > candidate.MaxTokens {
				violated = true
			}
		}
		return makeReplay(ok && violated)
	case "custom_exec_native_search_preservation":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			Rows []struct {
				SearchSignalCount int            `json:"search_signal_count"`
				URLCount          int            `json:"url_count"`
				KeywordHits       map[string]int `json:"keyword_hits"`
			} `json:"rows"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		suppressed := len(value.Rows) >= 2
		for _, row := range value.Rows {
			suppressed = suppressed && row.SearchSignalCount > 0 && row.URLCount == 0 && row.KeywordHits["sourceattribution"] == 0
		}
		return makeReplay(ok && suppressed)
	case "auto_route_reasoning_invariance":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			Guard        bool `json:"code_has_auto_invariance_guard"`
			NoneMarker   bool `json:"none_marker"`
			MediumMarker bool `json:"medium_marker"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && !value.Guard && value.NoneMarker && !value.MediumMarker)
	case "context_overflow_rejection":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			ApproxWords   int  `json:"approx_words"`
			PromptChars   int  `json:"prompt_chars"`
			HTTPStatus    int  `json:"http_status"`
			EndMarkerSeen bool `json:"end_marker_seen"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && value.ApproxWords >= 140000 && value.PromptChars > 0 && value.HTTPStatus == 200 && value.EndMarkerSeen)
	case "image_detail_original":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			UsedInBuilder bool `json:"detail_field_used_in_payload_builder"`
			OriginalHTTP  int  `json:"original_http"`
			InvalidHTTP   int  `json:"invalid_http"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && !value.UsedInBuilder && value.OriginalHTTP == 200 && value.InvalidHTTP == 200)
	case "json_schema_strict":
		record, ok := byPath["phase4-details.json"]
		var value struct {
			ValidHTTP        int  `json:"valid_http"`
			ValidSchemaMatch bool `json:"valid_schema_match"`
			InvalidHTTP      int  `json:"invalid_http"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && value.ValidHTTP == 200 && !value.ValidSchemaMatch && value.InvalidHTTP == 200)
	case "file_upload_api", "file_read_api":
		record, ok := byPath["phase3-details.json"]
		var value struct {
			HTTPStatus int `json:"http_status"`
		}
		_ = json.Unmarshal(record.Raw, &value)
		return makeReplay(ok && value.HTTPStatus == 404)
	default:
		return nil
	}
}

func classifyHistoricalEntry(source HistoricalBaselineSourceEntryV1, catalog ValidatedCatalogProjection) HistoricalBaselineDispositionV1 {
	policy := historicalCapabilityPolicies[source.CapabilityID]
	entry := HistoricalBaselineDispositionV1{
		CapabilityID:     source.CapabilityID,
		HistoricalStatus: source.HistoricalStatus,
		Classification:   ClassificationInconclusive,
		RationaleCode:    "historical_evidence_incomplete",
		Rationale:        "The historical verdict lacks complete accepted WP2 evidence for a final capability disposition.",
		SourceReferences: copyHistoricalReferences(source.SourceReferences),
		AcceptedSupport:  []HistoricalAcceptedSupportV1{},
		MissingEvidence:  []string{},
		DeferredClaims:   []string{"advanced_capability_acceptance"},
	}
	if policy.DeferredClaims != nil {
		entry.DeferredClaims = append([]string(nil), policy.DeferredClaims...)
	}

	if policy.AcceptedProtocol != "" {
		entry.DeferredClaims = []string{}
		entry.AcceptedSupport = historicalAcceptedSupport(catalog, policy.AcceptedProtocol)
		if historicalSupportGloballyVerified(entry.AcceptedSupport) {
			entry.Classification = ClassificationVerified
			entry.RationaleCode = "accepted_global_basic_transport"
			entry.Rationale = "Accepted account-pool evidence verifies basic text delivery and protocol transport for every canonical route without account-dependent claims."
			return entry
		}
		entry.RationaleCode = "accepted_support_incomplete"
		entry.Rationale = "Accepted route/protocol evidence exists, but at least one canonical route is missing or account-dependent, so the historical PASS is not globally VERIFIED."
		entry.MissingEvidence = []string{"complete_global_route_protocol_account_evidence"}
		return entry
	}

	if policy.ReplayKind != "" {
		if historicalReplayMatchesPolicy(source.ReplayEvidence, policy) {
			entry.Classification = policy.ReplayClassification
			entry.RationaleCode = policy.RationaleCode
			entry.Rationale = policy.Rationale
			entry.ReplayEvidence = copyHistoricalReplay(source.ReplayEvidence)
			return entry
		}
		if policy.ReplayClassification == ClassificationConfirmedDefect {
			entry.RationaleCode = "defect_replay_missing"
			entry.Rationale = "The ADR identifies a defect candidate, but the archived source does not satisfy the closed sidecar-controlled replay predicate required for CONFIRMED_DEFECT."
			entry.MissingEvidence = []string{"reproducible_sidecar_failure"}
		} else {
			entry.RationaleCode = "unsupported_replay_missing"
			entry.Rationale = "The historical FAIL does not include the closed account-independent endpoint evidence required for UNSUPPORTED."
			entry.MissingEvidence = []string{"route_account_protocol_unsupported_evidence"}
		}
		return entry
	}

	if policy.RationaleCode != "" {
		entry.RationaleCode = policy.RationaleCode
		entry.Rationale = policy.Rationale
	} else if source.HistoricalStatus == TestExecutionPass {
		entry.RationaleCode = "historical_pass_not_wp2_verified"
		entry.Rationale = "The historical PASS is not bound to complete accepted route, protocol, account, source, binary, and harness evidence for this capability."
	} else {
		entry.RationaleCode = "historical_fail_not_final_disposition"
		entry.Rationale = "The historical FAIL does not independently prove a reproducible sidecar defect or an account-independent unsupported combination."
	}
	if source.HistoricalStatus == TestExecutionPass {
		entry.MissingEvidence = []string{"route_protocol_account_source_binary_harness_binding"}
	} else {
		entry.MissingEvidence = []string{"reproducible_sidecar_or_unsupported_evidence"}
	}
	return entry
}

func historicalReplayMatchesPolicy(replay *HistoricalReplayEvidenceV1, policy historicalCapabilityPolicy) bool {
	return replay != nil && replay.Kind == policy.ReplayKind && replay.Scope == policy.ReplayScope && replay.ArtifactPath == policy.ReplayArtifactPath
}

func historicalAcceptedSupport(catalog ValidatedCatalogProjection, protocol string) []HistoricalAcceptedSupportV1 {
	expectedRoutes := map[string]struct{}{
		"m365-auto":                   {},
		"m365-gpt-5.5-quick-response": {},
		"m365-gpt-5.6-think-deeper":   {},
	}
	result := make([]HistoricalAcceptedSupportV1, 0, 6)
	for _, claim := range catalog.Manifest.GlobalClaims {
		if claim.Protocol != protocol {
			continue
		}
		if _, expected := expectedRoutes[claim.CanonicalRoute]; !expected {
			continue
		}
		for _, capability := range claim.Capabilities {
			if capability.CapabilityID != "basic_text_delivery" && capability.CapabilityID != "protocol_transport" {
				continue
			}
			result = append(result, HistoricalAcceptedSupportV1{
				CanonicalRoute:           claim.CanonicalRoute,
				ResolvedTone:             claim.ResolvedTone,
				Protocol:                 claim.Protocol,
				CapabilityID:             capability.CapabilityID,
				Classification:           capability.Classification,
				AccountDependent:         claim.AccountDependent,
				SupportingEvidenceSHA256: append([]string(nil), capability.SupportingEvidenceSHA256...),
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].CanonicalRoute + "\x00" + result[i].CapabilityID
		right := result[j].CanonicalRoute + "\x00" + result[j].CapabilityID
		return left < right
	})
	return result
}

func historicalSupportGloballyVerified(support []HistoricalAcceptedSupportV1) bool {
	if len(support) != 6 {
		return false
	}
	counts := make(map[string]int, 3)
	for _, item := range support {
		if item.Classification != ClassificationVerified || item.AccountDependent || len(item.SupportingEvidenceSHA256) == 0 {
			return false
		}
		counts[item.CanonicalRoute]++
	}
	return counts["m365-auto"] == 2 && counts["m365-gpt-5.5-quick-response"] == 2 && counts["m365-gpt-5.6-think-deeper"] == 2
}

func validateHistoricalBinaryIdentity(binary HistoricalBinaryIdentityV1) error {
	if err := requireSHA256(binary.SHA256, "/historical_binary/sha256"); err != nil {
		return err
	}
	if !gitCommitPattern.MatchString(binary.SourceHead) {
		return validationError("invalid_identity", "git_commit_sha", "/historical_binary/source_head")
	}
	if binary.SHA256 != historicalBinarySHA256V1 || binary.Bytes != historicalBinaryBytesV1 || binary.SourceHead != historicalBinarySourceHeadV1 || !binary.SourceModified {
		return validationError("identity_mismatch", "accepted_historical_binary", "/historical_binary")
	}
	return nil
}

func validateHistoricalSource(source ValidatedHistoricalBaselineSource) error {
	if source.Source.Schema != HistoricalBaselineSourceSchemaV1 || source.Source.NormativeADRSHA256 != historicalNormativeADRSHA256 {
		return validationError("invalid_schema", "historical_source_schema", "/schema")
	}
	if err := validateHistoricalBinaryIdentity(source.Source.HistoricalBinary); err != nil {
		return err
	}
	if len(source.expectedArtifacts) != len(historicalArtifactIdentitiesV1) || len(source.Source.Artifacts) != len(source.expectedArtifacts) || len(source.Source.Entries) != 48 || source.Source.EntryCount != 48 || source.Source.PassCount != 29 || source.Source.FailCount != 19 || source.Source.IssueRecordCount != 27 {
		return validationError("identity_mismatch", "historical_source_counts", "/")
	}
	for index, artifact := range source.Source.Artifacts {
		if artifact != source.expectedArtifacts[index] {
			return validationError("identity_mismatch", "accepted_historical_artifact", "/artifacts")
		}
		if err := requireSHA256(artifact.SHA256, "/artifacts/sha256"); err != nil {
			return err
		}
	}
	ids := historicalCapabilityIDs()
	for index, entry := range source.Source.Entries {
		if entry.CapabilityID != ids[index] || entry.HistoricalStatus != historicalCapabilityStatuses[entry.CapabilityID] || len(entry.SourceReferences) < 2 {
			return validationError("identity_mismatch", "historical_source_entry", "/entries")
		}
		if err := validateHistoricalReferences(entry.SourceReferences); err != nil {
			return err
		}
		if entry.ReplayEvidence != nil {
			policy, known := historicalCapabilityPolicies[entry.CapabilityID]
			if !known || policy.ReplayKind == "" || entry.ReplayEvidence.Kind != policy.ReplayKind || entry.ReplayEvidence.Scope != policy.ReplayScope || entry.ReplayEvidence.ArtifactPath != policy.ReplayArtifactPath {
				return validationError("identity_mismatch", "closed_historical_replay_policy", "/entries/replay_evidence")
			}
			if err := requireSHA256(entry.ReplayEvidence.RecordSHA256, "/entries/replay_evidence/evidence_sha256"); err != nil {
				return err
			}
			if !historicalReferenceExists(entry.SourceReferences, entry.ReplayEvidence.ArtifactPath, entry.ReplayEvidence.RecordSHA256) {
				return validationError("identity_mismatch", "historical_replay_source_reference", "/entries/replay_evidence")
			}
		}
	}
	canonical, checksum, err := marshalHistoricalValue(source.Source)
	if err != nil {
		return err
	}
	if checksum != source.ChecksumSHA256 || !bytes.Equal(canonical, source.CanonicalJSON) {
		return validationError("identity_mismatch", "historical_source_checksum", "/")
	}
	return nil
}

func validateHistoricalCatalog(catalog ValidatedCatalogProjection) error {
	if err := validateCatalogProjectionManifest(catalog.Manifest); err != nil {
		return err
	}
	canonical, checksum, err := marshalHistoricalValue(catalog.Manifest)
	if err != nil {
		return err
	}
	if checksum != catalog.ChecksumSHA256 || !bytes.Equal(canonical, catalog.CanonicalJSON) {
		return validationError("identity_mismatch", "accepted_catalog_checksum", "/accepted_catalog_sha256")
	}
	return nil
}

func validateHistoricalReferences(references []HistoricalSourceReferenceV1) error {
	previous := ""
	for _, reference := range references {
		key := reference.ArtifactPath + "\x00" + reference.RecordSHA256
		if previous != "" && previous >= key {
			return validationError("invalid_order", "historical_source_references", "/entries/source_references")
		}
		previous = key
		if !historicalArtifactPathKnown(reference.ArtifactPath) {
			return validationError("identity_mismatch", "historical_source_reference_path", "/entries/source_references/artifact_path")
		}
		if err := requireSHA256(reference.RecordSHA256, "/entries/source_references/evidence_sha256"); err != nil {
			return err
		}
	}
	return nil
}

func historicalArtifactPathKnown(artifactPath string) bool {
	for _, artifact := range historicalArtifactIdentitiesV1 {
		if artifact.Path == artifactPath {
			return true
		}
	}
	return false
}

func historicalReferenceExists(references []HistoricalSourceReferenceV1, artifactPath, checksum string) bool {
	for _, reference := range references {
		if reference.ArtifactPath == artifactPath && reference.RecordSHA256 == checksum {
			return true
		}
	}
	return false
}

func historicalCapabilityIDs() []string {
	ids := make([]string, 0, len(historicalCapabilityStatuses))
	for capabilityID := range historicalCapabilityStatuses {
		ids = append(ids, capabilityID)
	}
	sort.Strings(ids)
	return ids
}

func sortHistoricalReferences(references []HistoricalSourceReferenceV1) {
	sort.Slice(references, func(i, j int) bool {
		left := references[i].ArtifactPath + "\x00" + references[i].RecordSHA256
		right := references[j].ArtifactPath + "\x00" + references[j].RecordSHA256
		return left < right
	})
}

func copyHistoricalReferences(value []HistoricalSourceReferenceV1) []HistoricalSourceReferenceV1 {
	return append([]HistoricalSourceReferenceV1(nil), value...)
}

func copyHistoricalReplay(value *HistoricalReplayEvidenceV1) *HistoricalReplayEvidenceV1 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func canonicalJSONSHA256(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", validationError("invalid_json", "historical_record", "/")
	}
	if err := requireEOF(decoder); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", validationError("canonicalization_failed", "historical_record", "/")
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func marshalHistoricalValue(value any) ([]byte, string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", validationError("canonicalization_failed", "historical_inventory", "/")
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}
