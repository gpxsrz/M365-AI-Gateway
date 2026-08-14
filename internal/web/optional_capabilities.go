package web

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"m365-native/internal/chathub"
)

const (
	optionalModelCapabilityEvidenceSchemaV1 = "m365-web-model-capability-evidence/v1"
	webRequestCapabilityEvidenceSchemaV1    = "m365-web-request-capability-evidence/v1"
)

var (
	optionalToneID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	capabilityName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	evidenceSHA256 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

type optionalModelCapabilityEvidence struct {
	Schema                     string `json:"schema"`
	CapturedAt                 string `json:"capturedAt"`
	SelectorChoiceID           string `json:"selectorChoiceId"`
	WireTone                   string `json:"wireTone"`
	SelectorObservationSHA256  string `json:"selectorObservationSha256"`
	UsabilityObservationSHA256 string `json:"usabilityObservationSha256"`
	WireObservationSHA256      string `json:"wireObservationSha256"`
	TemporaryChat              bool   `json:"temporaryChat"`
	UsabilityVerified          bool   `json:"usabilityVerified"`
}

type optionalModelCapability struct {
	PublicModel           string                          `json:"publicModel"`
	UpstreamTone          string                          `json:"upstreamTone"`
	WebLabel              string                          `json:"webLabel"`
	DisplayName           string                          `json:"displayName"`
	DefaultReasoningLevel string                          `json:"defaultReasoningLevel"`
	Enabled               bool                            `json:"enabled"`
	Evidence              optionalModelCapabilityEvidence `json:"evidence"`
}

type webRequestCapabilityEvidence struct {
	Schema                string   `json:"schema"`
	CapturedAt            string   `json:"capturedAt"`
	Tone                  string   `json:"tone"`
	StreamingMode         string   `json:"streamingMode"`
	OptionsSets           []string `json:"optionsSets"`
	AllowedMessageTypes   []string `json:"allowedMessageTypes"`
	ObservationSHA256     string   `json:"observationSha256"`
	TemporaryChat         bool     `json:"temporaryChat"`
	DisableMemoryObserved bool     `json:"disableMemoryObserved"`
}

func validateOptionalModelCapabilities(capabilities []optionalModelCapability) error {
	seenModels := make(map[string]struct{}, len(capabilities))
	seenTones := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if err := validateOptionalModelCapability(capability); err != nil {
			return err
		}
		modelKey := strings.ToLower(strings.TrimSpace(capability.PublicModel))
		if _, exists := seenModels[modelKey]; exists {
			return fmt.Errorf("可選 Web 模型 %q 重複", capability.PublicModel)
		}
		for _, builtIn := range builtInRouteRegistry {
			if strings.EqualFold(builtIn.ID, capability.PublicModel) {
				return fmt.Errorf("可選 Web 模型 %q 與內建 route 衝突", capability.PublicModel)
			}
			if strings.EqualFold(builtIn.Tone, capability.UpstreamTone) {
				return fmt.Errorf("可選 Web tone %q 與內建 route %q 衝突", capability.UpstreamTone, builtIn.ID)
			}
		}
		seenModels[modelKey] = struct{}{}

		toneKey := strings.ToLower(strings.TrimSpace(capability.UpstreamTone))
		if _, exists := seenTones[toneKey]; exists {
			return fmt.Errorf("可選 Web tone %q 重複", capability.UpstreamTone)
		}
		seenTones[toneKey] = struct{}{}
	}
	return nil
}

func validateOptionalModelCapability(capability optionalModelCapability) error {
	model := strings.TrimSpace(capability.PublicModel)
	tone := strings.TrimSpace(capability.UpstreamTone)
	if !publicModelID.MatchString(model) {
		return fmt.Errorf("可選 Web 模型 ID 只能包含英文字母、數字、句點、底線或連字號，且長度必須為 1-128")
	}
	if !optionalToneID.MatchString(tone) {
		return fmt.Errorf("可選 Web 模型 %q 的 tone 格式無效", model)
	}
	if strings.TrimSpace(capability.WebLabel) == "" || strings.TrimSpace(capability.DisplayName) == "" {
		return fmt.Errorf("可選 Web 模型 %q 缺少顯示名稱或 Web label", model)
	}
	if effort, err := normalizeReasoningEffort(capability.DefaultReasoningLevel); err != nil || effort == "" {
		return fmt.Errorf("可選 Web 模型 %q 的預設推理等級無效", model)
	}
	evidence := capability.Evidence
	if evidence.Schema != optionalModelCapabilityEvidenceSchemaV1 {
		return fmt.Errorf("可選 Web 模型 %q 缺少受支援的 evidence schema", model)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(evidence.CapturedAt)); err != nil {
		return fmt.Errorf("可選 Web 模型 %q 的 evidence capturedAt 無效", model)
	}
	if strings.TrimSpace(evidence.SelectorChoiceID) != tone || strings.TrimSpace(evidence.WireTone) != tone {
		return fmt.Errorf("可選 Web 模型 %q 的 selector / wire tone evidence 不一致", model)
	}
	for name, digest := range map[string]string{
		"selector":  evidence.SelectorObservationSHA256,
		"usability": evidence.UsabilityObservationSHA256,
		"wire":      evidence.WireObservationSHA256,
	} {
		if !evidenceSHA256.MatchString(strings.TrimSpace(digest)) {
			return fmt.Errorf("可選 Web 模型 %q 的 %s evidence SHA-256 無效", model, name)
		}
	}
	if !evidence.TemporaryChat || !evidence.UsabilityVerified {
		return fmt.Errorf("可選 Web 模型 %q 尚未通過 Temporary Chat usability evidence", model)
	}
	return nil
}

func validStaticUpstreamTone(tone string) bool {
	for _, known := range knownUpstreamTones() {
		if tone == known {
			return true
		}
	}
	return false
}

func validUpstreamToneForSettings(tone string, cfg runtimeSettings) bool {
	tone = strings.TrimSpace(tone)
	if validStaticUpstreamTone(tone) {
		return true
	}
	for _, capability := range cfg.OptionalModelCapabilities {
		if !capability.Enabled || strings.TrimSpace(capability.UpstreamTone) != tone {
			continue
		}
		if validateOptionalModelCapability(capability) == nil {
			return true
		}
	}
	return false
}

func observedUpstreamToneForSettings(tone string, cfg runtimeSettings) bool {
	tone = strings.TrimSpace(tone)
	if validStaticUpstreamTone(tone) {
		return true
	}
	for _, capability := range cfg.OptionalModelCapabilities {
		if strings.TrimSpace(capability.UpstreamTone) == tone && validateOptionalModelCapability(capability) == nil {
			return true
		}
	}
	return false
}

func knownUpstreamTonesForSettings(cfg runtimeSettings) []string {
	out := append([]string(nil), knownUpstreamTones()...)
	seen := make(map[string]struct{}, len(out)+len(cfg.OptionalModelCapabilities))
	for _, tone := range out {
		seen[tone] = struct{}{}
	}
	for _, capability := range cfg.OptionalModelCapabilities {
		if !capability.Enabled || validateOptionalModelCapability(capability) != nil {
			continue
		}
		tone := strings.TrimSpace(capability.UpstreamTone)
		if _, exists := seen[tone]; exists {
			continue
		}
		seen[tone] = struct{}{}
		out = append(out, tone)
	}
	return out
}

func optionalCapabilityRoutes(cfg runtimeSettings) []routeDefinition {
	routes := make([]routeDefinition, 0, len(cfg.OptionalModelCapabilities))
	for _, capability := range cfg.OptionalModelCapabilities {
		if !capability.Enabled || validateOptionalModelCapability(capability) != nil {
			continue
		}
		evidence := capability.Evidence
		routes = append(routes, routeDefinition{
			ID: strings.TrimSpace(capability.PublicModel), CanonicalRoute: strings.TrimSpace(capability.PublicModel),
			Tone: strings.TrimSpace(capability.UpstreamTone), WebLabel: strings.TrimSpace(capability.WebLabel),
			Kind: routeKindWebModel, OperationalStatus: operationalEnabled, MappingEvidence: mappingWebPayloadVerified,
			IdentityStatus: identityAcceptedUnverified, CatalogVisibility: catalogPublic, Experimental: true,
			Owner: "microsoft-365", DisplayName: strings.TrimSpace(capability.DisplayName),
			DefaultReasoningLevel: strings.TrimSpace(capability.DefaultReasoningLevel), OptionalCapability: true,
			RuntimeEvidence: &runtimeModelCapabilityEvidence{
				CapturedAt: evidence.CapturedAt, SelectorObservationSHA256: evidence.SelectorObservationSHA256,
				UsabilityObservationSHA256: evidence.UsabilityObservationSHA256, WireObservationSHA256: evidence.WireObservationSHA256,
			},
		})
	}
	return routes
}

func routeRegistryForSettings(cfg runtimeSettings) []routeDefinition {
	routes := routeRegistry(cfg.ModelMappings)
	existing := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		existing[strings.ToLower(route.ID)] = struct{}{}
	}
	for _, route := range optionalCapabilityRoutes(cfg) {
		if _, exists := existing[strings.ToLower(route.ID)]; exists {
			continue
		}
		routes = append(routes, route)
	}
	return routes
}

func registeredRouteForSettings(model string, cfg runtimeSettings) (routeDefinition, bool) {
	return registeredRouteFromRegistry(model, routeRegistryForSettings(cfg))
}

func resolveRouteForSettings(model, effort string, cfg runtimeSettings) (routeResolution, error) {
	return resolveRouteFromRegistry(model, effort, routeRegistryForSettings(cfg))
}

func resolveChatRouteForSettings(model, tone, effort string, cfg runtimeSettings) (routeResolution, error) {
	return resolveChatRouteFromRegistry(model, tone, effort, routeRegistryForSettings(cfg))
}

func catalogRouteDefinitionsForSettings(cfg runtimeSettings) []routeDefinition {
	routes := routeRegistryForSettings(cfg)
	out := make([]routeDefinition, 0, len(routes))
	for _, route := range routes {
		if route.OperationalStatus != operationalEnabled || route.CatalogVisibility == catalogHidden {
			continue
		}
		out = append(out, cloneRouteDefinition(route))
	}
	return out
}

func hasWebRequestCapabilityEvidence(evidence webRequestCapabilityEvidence) bool {
	return evidence.Schema != "" || evidence.CapturedAt != "" || evidence.Tone != "" || evidence.StreamingMode != "" ||
		len(evidence.OptionsSets) > 0 || len(evidence.AllowedMessageTypes) > 0 || evidence.ObservationSHA256 != "" ||
		evidence.TemporaryChat || evidence.DisableMemoryObserved
}

func validateWebRequestCapabilityEvidence(evidence webRequestCapabilityEvidence, cfg runtimeSettings) error {
	if !hasWebRequestCapabilityEvidence(evidence) {
		return nil
	}
	if evidence.Schema != webRequestCapabilityEvidenceSchemaV1 {
		return fmt.Errorf("Web request capability evidence schema 不支援")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(evidence.CapturedAt)); err != nil {
		return fmt.Errorf("Web request capability evidence capturedAt 無效")
	}
	if !observedUpstreamToneForSettings(strings.TrimSpace(evidence.Tone), cfg) {
		return fmt.Errorf("Web request capability evidence tone %q 尚未被接受", evidence.Tone)
	}
	if !capabilityName.MatchString(strings.TrimSpace(evidence.StreamingMode)) {
		return fmt.Errorf("Web request capability evidence streamingMode 無效")
	}
	if !evidenceSHA256.MatchString(strings.TrimSpace(evidence.ObservationSHA256)) {
		return fmt.Errorf("Web request capability evidence SHA-256 無效")
	}
	if !evidence.TemporaryChat || !evidence.DisableMemoryObserved {
		return fmt.Errorf("Web request capability evidence 缺少 Temporary Chat / disableMemory 驗證")
	}
	if err := validateCapabilityNames("optionsSets", evidence.OptionsSets); err != nil {
		return err
	}
	if err := validateCapabilityNames("allowedMessageTypes", evidence.AllowedMessageTypes); err != nil {
		return err
	}
	return nil
}

func validateCapabilityNames(label string, values []string) error {
	if len(values) == 0 || len(values) > 256 {
		return fmt.Errorf("Web request capability evidence %s 數量必須為 1-256", label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !capabilityName.MatchString(value) {
			return fmt.Errorf("Web request capability evidence %s 包含無效名稱", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("Web request capability evidence %s 包含重複名稱 %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

type requestCapabilityBaseline struct {
	StreamingMode       string   `json:"streamingMode"`
	OptionsSets         []string `json:"optionsSets"`
	AllowedMessageTypes []string `json:"allowedMessageTypes"`
}

func currentRequestCapabilityBaseline() requestCapabilityBaseline {
	baseline := chathub.CurrentRequestCapabilityBaseline()
	return requestCapabilityBaseline{
		StreamingMode: baseline.StreamingMode, OptionsSets: baseline.OptionsSets, AllowedMessageTypes: baseline.AllowedMessageTypes,
	}
}

type stringSetDrift struct {
	WebOnly     []string `json:"webOnly"`
	SidecarOnly []string `json:"sidecarOnly"`
	Common      []string `json:"common"`
}

type requestCapabilityDriftSnapshot struct {
	Observed             bool           `json:"observed"`
	CapturedAt           string         `json:"capturedAt,omitempty"`
	Tone                 string         `json:"tone,omitempty"`
	ObservationSHA256    string         `json:"observationSha256,omitempty"`
	ProjectionPolicy     string         `json:"projectionPolicy"`
	StreamingMode        string         `json:"streamingMode,omitempty"`
	SidecarStreamingMode string         `json:"sidecarStreamingMode"`
	StreamingModeMatch   bool           `json:"streamingModeMatch"`
	OptionsSets          stringSetDrift `json:"optionsSets"`
	AllowedMessageTypes  stringSetDrift `json:"allowedMessageTypes"`
}

func requestCapabilityDrift(cfg runtimeSettings, baseline requestCapabilityBaseline) requestCapabilityDriftSnapshot {
	evidence := cfg.WebRequestCapabilityEvidence
	if !hasWebRequestCapabilityEvidence(evidence) {
		return requestCapabilityDriftSnapshot{ProjectionPolicy: "observe_only", SidecarStreamingMode: baseline.StreamingMode}
	}
	return requestCapabilityDriftSnapshot{
		Observed: true, CapturedAt: evidence.CapturedAt, Tone: evidence.Tone, ObservationSHA256: evidence.ObservationSHA256,
		ProjectionPolicy: "observe_only", StreamingMode: evidence.StreamingMode, SidecarStreamingMode: baseline.StreamingMode,
		StreamingModeMatch:  evidence.StreamingMode == baseline.StreamingMode,
		OptionsSets:         setDrift(evidence.OptionsSets, baseline.OptionsSets),
		AllowedMessageTypes: setDrift(evidence.AllowedMessageTypes, baseline.AllowedMessageTypes),
	}
}

func setDrift(webValues, sidecarValues []string) stringSetDrift {
	web := make(map[string]struct{}, len(webValues))
	sidecar := make(map[string]struct{}, len(sidecarValues))
	for _, value := range webValues {
		web[value] = struct{}{}
	}
	for _, value := range sidecarValues {
		sidecar[value] = struct{}{}
	}
	var drift stringSetDrift
	for value := range web {
		if _, ok := sidecar[value]; ok {
			drift.Common = append(drift.Common, value)
		} else {
			drift.WebOnly = append(drift.WebOnly, value)
		}
	}
	for value := range sidecar {
		if _, ok := web[value]; !ok {
			drift.SidecarOnly = append(drift.SidecarOnly, value)
		}
	}
	sort.Strings(drift.WebOnly)
	sort.Strings(drift.SidecarOnly)
	sort.Strings(drift.Common)
	return drift
}
