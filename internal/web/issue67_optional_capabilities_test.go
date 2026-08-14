package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func issue67OptionalCapability(tone string) optionalModelCapability {
	return optionalModelCapability{
		PublicModel:           "m365-future-quick-response",
		UpstreamTone:          tone,
		WebLabel:              "Future quick response",
		DisplayName:           "M365 Future quick response",
		DefaultReasoningLevel: "low",
		Enabled:               true,
		Evidence: optionalModelCapabilityEvidence{
			Schema:                     optionalModelCapabilityEvidenceSchemaV1,
			CapturedAt:                 "2026-08-15T01:30:00+08:00",
			SelectorChoiceID:           tone,
			WireTone:                   tone,
			SelectorObservationSHA256:  strings.Repeat("1", 64),
			UsabilityObservationSHA256: strings.Repeat("2", 64),
			WireObservationSHA256:      strings.Repeat("3", 64),
			TemporaryChat:              true,
			UsabilityVerified:          true,
		},
	}
}

func TestIssue67UnknownToneStillFailsClosedWithoutEvidence(t *testing.T) {
	cfg := defaultRuntimeSettings()
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel: "future-unsafe", UpstreamTone: "Gpt_9_9_Chat",
		DisplayName: "Future unsafe", DefaultReasoningLevel: "low",
	})
	if err := validateSettings(cfg); err == nil || !strings.Contains(err.Error(), "不支援") {
		t.Fatalf("validateSettings() err=%v, want unsupported tone rejection", err)
	}
}

func TestIssue67EvidenceBackedOptionalToneNeedsNoStaticAllowlistEntry(t *testing.T) {
	const tone = "Gpt_9_9_Chat"
	cfg := defaultRuntimeSettings()
	cfg.OptionalModelCapabilities = []optionalModelCapability{issue67OptionalCapability(tone)}
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("validateSettings() err=%v", err)
	}
	if validStaticUpstreamTone(tone) {
		t.Fatalf("future tone %q unexpectedly became a static allowlist entry", tone)
	}
	if !validUpstreamToneForSettings(tone, cfg) {
		t.Fatalf("future tone %q was not accepted from validated runtime evidence", tone)
	}

	resolution, err := resolveRouteForSettings("m365-future-quick-response", "", cfg)
	if err != nil {
		t.Fatalf("resolveRouteForSettings() err=%v", err)
	}
	if resolution.ResolvedTone != tone || resolution.RouteKind != routeKindWebModel || resolution.MappingEvidence != mappingWebPayloadVerified {
		t.Fatalf("resolution=%#v", resolution)
	}

	catalog := modelCatalogForSettingsAndEvidence(cfg, nil)
	var found map[string]any
	for _, model := range catalog {
		if model["id"] == "m365-future-quick-response" {
			found = model
			break
		}
	}
	if found == nil {
		t.Fatal("evidence-backed optional model missing from catalog")
	}
	if found["x_m365_optional_capability"] != true || found["x_m365_evidence_source"] != "operator_attested_web_observation" || found["x_m365_mapping_source"] != "operator_attested_web_observation" || found["x_m365_wire_observation_sha256"] != strings.Repeat("3", 64) {
		t.Fatalf("optional evidence metadata=%#v", found)
	}
}

func TestIssue67OptionalRouteNeverRemapsBeyondAttestedTone(t *testing.T) {
	const tone = "Gpt_9_9_Chat"
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability(tone)
	capability.PublicModel = "future-quick-response"
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("validateSettings() err=%v", err)
	}
	resolution, err := resolveRouteForSettings(capability.PublicModel, "high", cfg)
	if err != nil {
		t.Fatalf("resolveRouteForSettings() err=%v", err)
	}
	if resolution.ResolvedTone != tone || !resolution.ReasoningEffortIgnored {
		t.Fatalf("optional evidence tone remapped: %#v", resolution)
	}
}

func TestIssue67OptionalCapabilityEvidenceFailsClosedOnMismatch(t *testing.T) {
	cases := []struct {
		name string
		edit func(*optionalModelCapability)
	}{
		{"wire tone mismatch", func(c *optionalModelCapability) { c.Evidence.WireTone = "Other_Tone" }},
		{"selector mismatch", func(c *optionalModelCapability) { c.Evidence.SelectorChoiceID = "Other_Tone" }},
		{"missing wire hash", func(c *optionalModelCapability) { c.Evidence.WireObservationSHA256 = "" }},
		{"not temporary", func(c *optionalModelCapability) { c.Evidence.TemporaryChat = false }},
		{"usability unverified", func(c *optionalModelCapability) { c.Evidence.UsabilityVerified = false }},
		{"invalid timestamp", func(c *optionalModelCapability) { c.Evidence.CapturedAt = "yesterday" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultRuntimeSettings()
			capability := issue67OptionalCapability("Gpt_9_9_Chat")
			tc.edit(&capability)
			cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
			if err := validateSettings(cfg); err == nil {
				t.Fatal("validateSettings() unexpectedly accepted invalid evidence")
			}
		})
	}
}

func TestIssue67RejectsConfiguredMappingThatShadowsOptionalRoute(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_9_9_Chat")
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel: capability.PublicModel, UpstreamTone: capability.UpstreamTone,
		DisplayName: "Shadow", DefaultReasoningLevel: "low",
	})
	if err := validateSettings(cfg); err == nil || !strings.Contains(err.Error(), "同時出現在") {
		t.Fatalf("validateSettings() err=%v, want cross-registry collision rejection", err)
	}
}

func TestIssue67RejectsOptionalToneThatCollidesWithExistingTone(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_5_6_Reasoning")
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	if err := validateSettings(cfg); err == nil || !strings.Contains(err.Error(), "內建 route") {
		t.Fatalf("validateSettings() err=%v, want built-in tone collision rejection", err)
	}

	cfg = defaultRuntimeSettings()
	capability = issue67OptionalCapability("Gpt_9_9_Chat")
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel: "configured-future", UpstreamTone: capability.UpstreamTone,
		DisplayName: "Configured future", DefaultReasoningLevel: "low",
	})
	if err := validateSettings(cfg); err == nil || !strings.Contains(err.Error(), "同時出現在") {
		t.Fatalf("validateSettings() err=%v, want configured/optional tone collision rejection", err)
	}
}

func TestIssue67DisabledOptionalCapabilityIsEvidenceOnly(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_9_9_Chat")
	capability.Enabled = false
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("validateSettings() err=%v", err)
	}
	if validUpstreamToneForSettings(capability.UpstreamTone, cfg) {
		t.Fatalf("disabled optional tone %q became routable", capability.UpstreamTone)
	}
	if _, err := resolveRouteForSettings(capability.PublicModel, "", cfg); err == nil {
		t.Fatalf("disabled optional route %q resolved", capability.PublicModel)
	}
}

func TestIssue67DisabledOptionalCapabilityCanRetainObserveOnlyWireEvidence(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_9_9_Chat")
	capability.Enabled = false
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	cfg.WebRequestCapabilityEvidence = webRequestCapabilityEvidence{
		Schema: webRequestCapabilityEvidenceSchemaV1, CapturedAt: "2026-08-15T01:40:00+08:00",
		Tone: capability.UpstreamTone, StreamingMode: "ConciseWithPadding",
		OptionsSets: []string{"observed-option"}, AllowedMessageTypes: []string{"Chat"},
		ObservationSHA256: strings.Repeat("4", 64), TemporaryChat: true, DisableMemoryObserved: true,
	}
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("validateSettings() err=%v", err)
	}
	if validUpstreamToneForSettings(capability.UpstreamTone, cfg) {
		t.Fatal("disabled optional capability became routable")
	}
	if !requestCapabilityDrift(cfg, currentRequestCapabilityBaseline()).Observed {
		t.Fatal("disabled optional capability lost its observe-only request evidence")
	}
}

func TestIssue67RequestCapabilityEvidenceProducesObserveOnlyDrift(t *testing.T) {
	cfg := defaultRuntimeSettings()
	cfg.WebRequestCapabilityEvidence = webRequestCapabilityEvidence{
		Schema:                webRequestCapabilityEvidenceSchemaV1,
		CapturedAt:            time.Date(2026, 8, 15, 1, 40, 0, 0, time.FixedZone("TST", 8*60*60)).Format(time.RFC3339),
		Tone:                  "Gpt_9_9_Chat",
		StreamingMode:         "ConciseV2",
		OptionsSets:           []string{"baseline-option", "web-only-option"},
		AllowedMessageTypes:   []string{"Chat", "FutureMessage"},
		ObservationSHA256:     strings.Repeat("4", 64),
		TemporaryChat:         true,
		DisableMemoryObserved: true,
	}
	cfg.OptionalModelCapabilities = []optionalModelCapability{issue67OptionalCapability("Gpt_9_9_Chat")}
	if err := validateSettings(cfg); err != nil {
		t.Fatalf("validateSettings() err=%v", err)
	}

	drift := requestCapabilityDrift(cfg, requestCapabilityBaseline{
		StreamingMode:       "ConciseWithPadding",
		OptionsSets:         []string{"baseline-option", "sidecar-only-option"},
		AllowedMessageTypes: []string{"Chat", "SidecarMessage"},
	})
	if !drift.Observed || drift.StreamingModeMatch || drift.ObservationSHA256 != strings.Repeat("4", 64) {
		t.Fatalf("drift=%#v", drift)
	}
	if strings.Join(drift.OptionsSets.WebOnly, ",") != "web-only-option" || strings.Join(drift.OptionsSets.SidecarOnly, ",") != "sidecar-only-option" {
		t.Fatalf("options drift=%#v", drift.OptionsSets)
	}
	if strings.Join(drift.AllowedMessageTypes.WebOnly, ",") != "FutureMessage" || strings.Join(drift.AllowedMessageTypes.SidecarOnly, ",") != "SidecarMessage" {
		t.Fatalf("allowed-message drift=%#v", drift.AllowedMessageTypes)
	}
	if drift.ProjectionPolicy != "observe_only" {
		t.Fatalf("projection policy=%q", drift.ProjectionPolicy)
	}
}

func TestIssue67AdminSettingsProjectsOptionalCapabilityAndDrift(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_9_9_Chat")
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	cfg.WebRequestCapabilityEvidence = webRequestCapabilityEvidence{
		Schema: webRequestCapabilityEvidenceSchemaV1, CapturedAt: "2026-08-15T01:40:00+08:00",
		Tone: capability.UpstreamTone, StreamingMode: "ConciseV2",
		OptionsSets: []string{"observed-option"}, AllowedMessageTypes: []string{"Chat"},
		ObservationSHA256: strings.Repeat("4", 64), TemporaryChat: true, DisableMemoryObserved: true,
	}
	store := &settingsStore{v: cfg, persistedFields: map[string]struct{}{}, startupInjectedEnv: map[string]string{}}
	server := &Server{settings: store, compatTraffic: newCompatibilityTrafficController()}
	recorder := httptest.NewRecorder()
	server.adminSettings(recorder, httptest.NewRequest("GET", "/api/admin/settings", nil))
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		CodexModels   []string                       `json:"codexModels"`
		UpstreamTones []string                       `json:"upstreamTones"`
		Drift         requestCapabilityDriftSnapshot `json:"webRequestCapabilityDrift"`
		Baseline      requestCapabilityBaseline      `json:"chatHubRequestCapabilityBaseline"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !containsString(body.CodexModels, capability.PublicModel) || !containsString(body.UpstreamTones, capability.UpstreamTone) {
		t.Fatalf("admin model/tone projection missing: %#v %#v", body.CodexModels, body.UpstreamTones)
	}
	if !body.Drift.Observed || body.Drift.ProjectionPolicy != "observe_only" || body.Baseline.StreamingMode == "" || len(body.Baseline.AllowedMessageTypes) == 0 {
		t.Fatalf("admin evidence projection drift=%#v baseline=%#v", body.Drift, body.Baseline)
	}
}

func TestIssue67PartialSettingsPUTPreservesEvidenceRegistry(t *testing.T) {
	cfg := defaultRuntimeSettings()
	capability := issue67OptionalCapability("Gpt_9_9_Chat")
	cfg.OptionalModelCapabilities = []optionalModelCapability{capability}
	cfg.WebRequestCapabilityEvidence = webRequestCapabilityEvidence{
		Schema: webRequestCapabilityEvidenceSchemaV1, CapturedAt: "2026-08-15T01:40:00+08:00",
		Tone: capability.UpstreamTone, StreamingMode: "ConciseWithPadding",
		OptionsSets: []string{"observed-option"}, AllowedMessageTypes: []string{"Chat"},
		ObservationSHA256: strings.Repeat("4", 64), TemporaryChat: true, DisableMemoryObserved: true,
	}
	store := &settingsStore{
		v: cfg, persistedFields: map[string]struct{}{}, startupInjectedEnv: map[string]string{},
		persist: func(string, []byte) error { return nil },
	}
	server := &Server{settings: store}
	recorder := httptest.NewRecorder()
	server.adminSettings(recorder, httptest.NewRequest("PUT", "/api/admin/settings", strings.NewReader(`{"chatTimeoutSeconds":180}`)))
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	after := store.get()
	if after.ChatTimeoutSeconds != 180 || len(after.OptionalModelCapabilities) != 1 || after.OptionalModelCapabilities[0].UpstreamTone != capability.UpstreamTone || after.WebRequestCapabilityEvidence.ObservationSHA256 != strings.Repeat("4", 64) {
		t.Fatalf("partial PUT lost evidence registry: %#v", after)
	}
}

func TestIssue67ConcurrentPartialSettingsPUTsMergeAgainstLatestState(t *testing.T) {
	cfg := defaultRuntimeSettings()
	store := &settingsStore{v: cfg, persistedFields: map[string]struct{}{}, startupInjectedEnv: map[string]string{}}
	firstPersistStarted := make(chan struct{})
	releaseFirstPersist := make(chan struct{})
	var persistCalls atomic.Int32
	store.persist = func(string, []byte) error {
		if persistCalls.Add(1) == 1 {
			close(firstPersistStarted)
			<-releaseFirstPersist
		}
		return nil
	}
	server := &Server{settings: store}

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.adminSettings(recorder, httptest.NewRequest("PUT", "/api/admin/settings", strings.NewReader(`{"chatTimeoutSeconds":180}`)))
		firstDone <- recorder
	}()
	<-firstPersistStarted

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		server.adminSettings(recorder, httptest.NewRequest("PUT", "/api/admin/settings", strings.NewReader(`{"imageTimeoutSeconds":240}`)))
		secondDone <- recorder
	}()
	time.Sleep(20 * time.Millisecond)
	close(releaseFirstPersist)

	if recorder := <-firstDone; recorder.Code != 200 {
		t.Fatalf("first status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := <-secondDone; recorder.Code != 200 {
		t.Fatalf("second status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	after := store.get()
	if after.ChatTimeoutSeconds != 180 || after.ImageTimeoutSeconds != 240 {
		t.Fatalf("concurrent partial PUT lost an update: chat=%d image=%d", after.ChatTimeoutSeconds, after.ImageTimeoutSeconds)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
