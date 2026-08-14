package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"m365-native/internal/outbound"
)

type modelMapping struct {
	PublicModel           string `json:"publicModel"`
	UpstreamTone          string `json:"upstreamTone"`
	DisplayName           string `json:"displayName"`
	DefaultReasoningLevel string `json:"defaultReasoningLevel"`
}

var defaultModelMappings = []modelMapping{
	{PublicModel: "gpt-5.6-sol", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "GPT-5.6-Sol", DefaultReasoningLevel: "low"},
	{PublicModel: "gpt-5.6-terra", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "GPT-5.6-Terra", DefaultReasoningLevel: "medium"},
	{PublicModel: "gpt-5.6-luna", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "GPT-5.6-Luna", DefaultReasoningLevel: "medium"},
}

var publicModelID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

const (
	chatModePrivate            = "private"
	chatModeNormal             = "normal"
	defaultTextInputLimitUTF16 = 128000
)

type runtimeSettings struct {
	ChatMode                          string         `json:"chatMode"`
	HermesCompatibilityEnabled        bool           `json:"hermesCompatibilityEnabled"`
	MemoryCompatibilityEnabled        bool           `json:"memoryCompatibilityEnabled"`
	InteractiveMaxConcurrent          int            `json:"interactiveMaxConcurrent"`
	InteractiveQueueTimeoutSeconds    int            `json:"interactiveQueueTimeoutSeconds"`
	MemoryMaxConcurrent               int            `json:"memoryMaxConcurrent"`
	MemoryQueueTimeoutSeconds         int            `json:"memoryQueueTimeoutSeconds"`
	InteractivePriorityHoldoffSeconds int            `json:"interactivePriorityHoldoffSeconds"`
	MemoryBackoffInitialSeconds       int            `json:"memoryBackoffInitialSeconds"`
	MemoryBackoffMaxSeconds           int            `json:"memoryBackoffMaxSeconds"`
	TextInputLimitUTF16               int            `json:"textInputLimitUTF16"`
	MaxToolCallsPerTurn               int            `json:"maxToolCallsPerTurn"`
	MaxToolRounds                     int            `json:"maxToolRounds"`
	HermesMaxToolRounds               int            `json:"hermesMaxToolRounds"`
	ContextWindow                     int            `json:"contextWindow"`
	MaxOutputTokens                   int            `json:"maxOutputTokens"`
	ChatTimeoutSeconds                int            `json:"chatTimeoutSeconds"`
	ImageTimeoutSeconds               int            `json:"imageTimeoutSeconds"`
	LogLevel                          string         `json:"logLevel"`
	DebugLogPath                      string         `json:"debugLogPath"`
	ListenAddress                     string         `json:"listenAddress"`
	ConfigPath                        string         `json:"configPath"`
	TokenCachePath                    string         `json:"tokenCachePath"`
	SessionCachePath                  string         `json:"sessionCachePath"`
	OutboundProxy                     string         `json:"outboundProxy"`
	ClientID                          string         `json:"clientId"`
	Authority                         string         `json:"authority"`
	RedirectURI                       string         `json:"redirectUri"`
	Scope                             string         `json:"scope"`
	ModelMappings                     []modelMapping `json:"modelMappings"`
	ToolPlanningMode                  string         `json:"toolPlanningMode"`
}

type settingsStore struct {
	mu                 sync.RWMutex
	path               string
	v                  runtimeSettings
	loadErr            error
	persist            func(string, []byte) error
	persistedFields    map[string]struct{}
	startupInjectedEnv map[string]string
}

type settingValueStatus struct {
	Configured      any    `json:"configured"`
	Effective       any    `json:"effective"`
	Source          string `json:"source"`
	Environment     string `json:"environment,omitempty"`
	Locked          bool   `json:"locked"`
	RestartRequired bool   `json:"restartRequired"`
}

func envInt(name string, fallback int) int {
	n, e := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if e == nil && n > 0 {
		return n
	}
	return fallback
}

func envNonNegativeInt(name string, fallback int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	n, e := strconv.Atoi(strings.TrimSpace(raw))
	if e == nil && n >= 0 {
		return n
	}
	return fallback
}

func interactivePriorityHoldoffEnvName() string {
	if _, exists := os.LookupEnv("M365_INTERACTIVE_PRIORITY_HOLDOFF_SECONDS"); exists {
		return "M365_INTERACTIVE_PRIORITY_HOLDOFF_SECONDS"
	}
	if _, exists := os.LookupEnv("M365_HERMES_PRIORITY_HOLDOFF_SECONDS"); exists {
		return "M365_HERMES_PRIORITY_HOLDOFF_SECONDS"
	}
	return "M365_INTERACTIVE_PRIORITY_HOLDOFF_SECONDS"
}

func interactivePriorityHoldoffDefault() int {
	return envNonNegativeInt(interactivePriorityHoldoffEnvName(), 30)
}

func defaultRuntimeSettings() runtimeSettings {
	return runtimeSettings{
		ChatMode:                          chatModePrivate,
		HermesCompatibilityEnabled:        true,
		MemoryCompatibilityEnabled:        false,
		InteractiveMaxConcurrent:          envInt("M365_INTERACTIVE_MAX_CONCURRENT", 2),
		InteractiveQueueTimeoutSeconds:    envInt("M365_INTERACTIVE_QUEUE_TIMEOUT_SECONDS", 300),
		MemoryMaxConcurrent:               envInt("M365_MEMORY_MAX_CONCURRENT", 2),
		MemoryQueueTimeoutSeconds:         envInt("M365_MEMORY_QUEUE_TIMEOUT_SECONDS", 60),
		InteractivePriorityHoldoffSeconds: interactivePriorityHoldoffDefault(),
		MemoryBackoffInitialSeconds:       envInt("M365_MEMORY_BACKOFF_INITIAL_SECONDS", 5),
		MemoryBackoffMaxSeconds:           envInt("M365_MEMORY_BACKOFF_MAX_SECONDS", 60),
		TextInputLimitUTF16:               defaultTextInputLimitUTF16,
		MaxToolCallsPerTurn:               envInt("M365_MAX_TOOL_CALLS_PER_TURN", 2), MaxToolRounds: envInt("M365_MAX_TOOL_ROUNDS", 16), HermesMaxToolRounds: envInt("M365_HERMES_MAX_TOOL_ROUNDS", 128),
		ContextWindow: envInt("M365_CONTEXT_WINDOW", 128000), MaxOutputTokens: envInt("M365_MAX_OUTPUT_TOKENS", 16384),
		ChatTimeoutSeconds: envInt("M365_CHAT_TIMEOUT_SECONDS", 120), ImageTimeoutSeconds: envInt("M365_IMAGE_TIMEOUT_SECONDS", 150), LogLevel: firstNonEmptySetting(os.Getenv("M365_LOG_LEVEL"), "info"),
		DebugLogPath: os.Getenv("M365_DEBUG_LOG"), ListenAddress: os.Getenv("M365_LISTEN"), ConfigPath: os.Getenv("M365_CONFIG"),
		TokenCachePath: os.Getenv("M365_TOKEN_CACHE"), SessionCachePath: os.Getenv("M365_SESSION_CACHE"), OutboundProxy: os.Getenv(outbound.EnvProxy), ClientID: os.Getenv("M365_CLIENT_ID"),
		Authority: os.Getenv("M365_AUTHORITY"), RedirectURI: os.Getenv("M365_REDIRECT_URI"), Scope: os.Getenv("M365_SCOPE"),
		ModelMappings:    append([]modelMapping(nil), defaultModelMappings...),
		ToolPlanningMode: toolPlanningMode(os.Getenv("M365_TOOL_PLANNING_MODE")),
	}
}
func settingsPath() string {
	if dir := strings.TrimSpace(os.Getenv("M365_DATA_DIR")); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	if p := strings.TrimSpace(os.Getenv("M365_SETTINGS_FILE")); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "m365-native", "settings.json")
}

var (
	sharedSettingsMu sync.Mutex
	sharedSettings   *settingsStore
)

func openSettingsStore() *settingsStore {
	sharedSettingsMu.Lock()
	defer sharedSettingsMu.Unlock()
	if sharedSettings != nil {
		return sharedSettings
	}
	sharedSettings = loadSettingsStore(settingsPath())
	return sharedSettings
}

func loadSettingsStore(path string) *settingsStore {
	s := &settingsStore{
		path:               path,
		v:                  defaultRuntimeSettings(),
		persistedFields:    make(map[string]struct{}),
		startupInjectedEnv: make(map[string]string),
	}
	migrateSettings := false
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.v); err != nil {
			s.loadErr = fmt.Errorf("decode runtime settings: %w", err)
		} else {
			s.persistedFields = settingsJSONFields(b)
			if !fieldPersisted(s.persistedFields, "memoryCompatibilityEnabled") {
				s.v.MemoryCompatibilityEnabled = true
				migrateSettings = true
			}
			var rawFields map[string]json.RawMessage
			if err := json.Unmarshal(b, &rawFields); err == nil {
				if _, exists := rawFields["interactiveMaxConcurrent"]; !exists {
					migrateSettings = true
				}
				if _, exists := rawFields["interactiveQueueTimeoutSeconds"]; !exists {
					migrateSettings = true
				}
				if legacy, exists := rawFields["hermesPriorityHoldoffSeconds"]; exists {
					if !fieldPersisted(s.persistedFields, "interactivePriorityHoldoffSeconds") {
						if err := json.Unmarshal(legacy, &s.v.InteractivePriorityHoldoffSeconds); err != nil {
							s.loadErr = fmt.Errorf("decode legacy interactive priority holdoff: %w", err)
						}
					}
					migrateSettings = true
				}
				if _, exists := rawFields["proxyPool"]; exists {
					migrateSettings = true
				}
			}
		}
	} else if !os.IsNotExist(err) {
		s.loadErr = fmt.Errorf("read runtime settings: %w", err)
	}
	if s.loadErr == nil {
		s.loadErr = validateSettings(s.v)
	}
	if s.loadErr == nil && migrateSettings {
		if err := s.save(s.v); err != nil {
			s.loadErr = fmt.Errorf("persist runtime settings migration: %w", err)
		}
	}
	return s
}
func firstNonEmptySetting(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func validateSettings(v runtimeSettings) error {
	if v.ChatMode != chatModePrivate && v.ChatMode != chatModeNormal {
		return fmt.Errorf("聊天模式必須為 private 或 normal")
	}
	if v.InteractiveMaxConcurrent < 1 || v.InteractiveMaxConcurrent > 16 {
		return fmt.Errorf("互動流量同時請求上限必須為 1-16")
	}
	if v.InteractiveQueueTimeoutSeconds < 1 || v.InteractiveQueueTimeoutSeconds > 600 {
		return fmt.Errorf("互動流量排隊逾時必須為 1-600 秒")
	}
	if v.MemoryMaxConcurrent < 1 || v.MemoryMaxConcurrent > 16 {
		return fmt.Errorf("背景 Memory 同時請求上限必須為 1-16")
	}
	if v.MemoryQueueTimeoutSeconds < 1 || v.MemoryQueueTimeoutSeconds > 600 {
		return fmt.Errorf("背景 Memory 排隊逾時必須為 1-600 秒")
	}
	if v.InteractivePriorityHoldoffSeconds < 0 || v.InteractivePriorityHoldoffSeconds > 300 {
		return fmt.Errorf("互動流量優先保留時間必須為 0-300 秒")
	}
	if v.MemoryBackoffInitialSeconds < 1 || v.MemoryBackoffInitialSeconds > 300 {
		return fmt.Errorf("背景 Memory 初始退避必須為 1-300 秒")
	}
	if v.MemoryBackoffMaxSeconds < v.MemoryBackoffInitialSeconds || v.MemoryBackoffMaxSeconds > 3600 {
		return fmt.Errorf("背景 Memory 最大退避必須大於等於初始退避且不超過 3600 秒")
	}
	if _, err := requestBodyLimit(v.TextInputLimitUTF16); err != nil {
		return err
	}
	if v.MaxToolCallsPerTurn < 1 || v.MaxToolCallsPerTurn > 64 {
		return fmt.Errorf("每輪工具呼叫數必須為 1-64")
	}
	if v.MaxToolRounds < 1 || v.MaxToolRounds > 512 {
		return fmt.Errorf("一般 / Memory 最大工具輪次必須為 1-512")
	}
	if v.HermesMaxToolRounds < 1 || v.HermesMaxToolRounds > 512 {
		return fmt.Errorf("專用 Hermes 最大工具輪次必須為 1-512")
	}
	if v.ContextWindow < 1024 {
		return fmt.Errorf("內容視窗不得小於 1024")
	}
	if v.MaxOutputTokens < 1 || v.MaxOutputTokens >= v.ContextWindow {
		return fmt.Errorf("最大輸出必須大於 0 且小於內容視窗")
	}
	if v.ChatTimeoutSeconds < 5 || v.ChatTimeoutSeconds > 3600 {
		return fmt.Errorf("聊天逾時必須為 5-3600 秒")
	}
	if v.ImageTimeoutSeconds < 5 || v.ImageTimeoutSeconds > 3600 {
		return fmt.Errorf("圖片逾時必須為 5-3600 秒")
	}
	if v.LogLevel != "silent" && v.LogLevel != "error" && v.LogLevel != "warn" && v.LogLevel != "info" && v.LogLevel != "debug" {
		return fmt.Errorf("日誌等級必須為 silent、error、warn、info 或 debug")
	}
	if v.ToolPlanningMode != "router" && v.ToolPlanningMode != "native" {
		return fmt.Errorf("工具規劃模式必須為 router 或 native")
	}
	if err := outbound.ValidateProxyURL(v.OutboundProxy); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(v.ModelMappings))
	for _, mapping := range v.ModelMappings {
		model := strings.TrimSpace(mapping.PublicModel)
		if !publicModelID.MatchString(model) {
			return fmt.Errorf("公開模型 ID 只能包含英文字母、數字、句點、底線或連字號，且長度必須為 1-128")
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("公開模型 ID %q 重複", model)
		}
		seen[key] = struct{}{}
		if !validUpstreamTone(strings.TrimSpace(mapping.UpstreamTone)) {
			return fmt.Errorf("上游 tone %q 不支援", mapping.UpstreamTone)
		}
		if strings.TrimSpace(mapping.DisplayName) == "" {
			return fmt.Errorf("公開模型 %q 缺少顯示名稱", model)
		}
		if _, err := normalizeReasoningEffort(mapping.DefaultReasoningLevel); err != nil || strings.TrimSpace(mapping.DefaultReasoningLevel) == "" {
			return fmt.Errorf("公開模型 %q 的預設推理等級無效", model)
		}
	}
	return nil
}
func (s *settingsStore) get() runtimeSettings {
	s.mu.RLock()
	v := s.v
	s.mu.RUnlock()
	if v.ChatMode == "" {
		v.ChatMode = chatModePrivate
	}
	if v.TextInputLimitUTF16 == 0 {
		v.TextInputLimitUTF16 = defaultTextInputLimitUTF16
	}
	return v
}
func (s *settingsStore) save(v runtimeSettings) error {
	if err := validateSettings(v); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	persist := s.persist
	if persist == nil {
		persist = atomicWriteSettingsFile
	}
	if err := persist(s.path, b); err != nil {
		return err
	}
	s.v = v
	s.loadErr = nil
	s.persistedFields = settingsJSONFields(b)
	return nil
}

func settingsJSONFields(raw []byte) map[string]struct{} {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return make(map[string]struct{})
	}
	out := make(map[string]struct{}, len(fields))
	for field := range fields {
		out[field] = struct{}{}
	}
	return out
}

func (s *settingsStore) provenanceSnapshot() (runtimeSettings, map[string]struct{}, map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	persisted := make(map[string]struct{}, len(s.persistedFields))
	for field := range s.persistedFields {
		persisted[field] = struct{}{}
	}
	injected := make(map[string]string, len(s.startupInjectedEnv))
	for name, value := range s.startupInjectedEnv {
		injected[name] = value
	}
	return s.v, persisted, injected
}

func (s *settingsStore) markStartupInjectedEnv(name, value string) {
	s.mu.Lock()
	if s.startupInjectedEnv == nil {
		s.startupInjectedEnv = make(map[string]string)
	}
	s.startupInjectedEnv[name] = value
	s.mu.Unlock()
}

func atomicWriteSettingsFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
func managementRouteIDs(mappings []modelMapping) []string {
	routes := catalogRouteDefinitions(mappings)
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	return ids
}

func fieldPersisted(fields map[string]struct{}, field string) bool {
	_, ok := fields[field]
	return ok
}

func positiveIntegerEnvActive(name string) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return err == nil && n > 0
}

func nonNegativeIntegerEnvActive(name string) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	return err == nil && n >= 0
}

func seededSettingStatus(configured any, field, environment string, persisted map[string]struct{}, environmentActive bool) settingValueStatus {
	source := "default"
	if fieldPersisted(persisted, field) {
		source = "file"
	} else if environmentActive {
		source = "env"
	}
	return settingValueStatus{
		Configured:  configured,
		Effective:   configured,
		Source:      source,
		Environment: environment,
	}
}

func directOverrideSettingStatus(configured, effective any, field, environment string, persisted map[string]struct{}) settingValueStatus {
	if _, exists := os.LookupEnv(environment); exists {
		return settingValueStatus{Configured: configured, Effective: effective, Source: "env", Environment: environment, Locked: true}
	}
	source := "default"
	if fieldPersisted(persisted, field) {
		source = "file"
	}
	return settingValueStatus{Configured: configured, Effective: effective, Source: source, Environment: environment}
}

func restartSettingStatus(configured any, field, environment string, persisted map[string]struct{}, injected map[string]string, parseEnvironment func(string) any) settingValueStatus {
	status := settingValueStatus{
		Configured:      configured,
		Effective:       configured,
		Source:          "default",
		Environment:     environment,
		RestartRequired: true,
	}
	if fieldPersisted(persisted, field) {
		status.Source = "file"
	}
	if raw, exists := os.LookupEnv(environment); exists {
		if parseEnvironment != nil {
			status.Effective = parseEnvironment(raw)
		} else {
			status.Effective = raw
		}
		if injectedValue, fromFile := injected[environment]; fromFile && raw == injectedValue {
			status.Source = "file"
			return status
		}
		status.Source = "env"
		status.Locked = true
	}
	return status
}

func settingsStatus(store *settingsStore) map[string]settingValueStatus {
	if store == nil {
		return map[string]settingValueStatus{}
	}
	cfg, persisted, injected := store.provenanceSnapshot()
	status := map[string]settingValueStatus{
		"interactiveMaxConcurrent":          seededSettingStatus(cfg.InteractiveMaxConcurrent, "interactiveMaxConcurrent", "M365_INTERACTIVE_MAX_CONCURRENT", persisted, positiveIntegerEnvActive("M365_INTERACTIVE_MAX_CONCURRENT")),
		"interactiveQueueTimeoutSeconds":    seededSettingStatus(cfg.InteractiveQueueTimeoutSeconds, "interactiveQueueTimeoutSeconds", "M365_INTERACTIVE_QUEUE_TIMEOUT_SECONDS", persisted, positiveIntegerEnvActive("M365_INTERACTIVE_QUEUE_TIMEOUT_SECONDS")),
		"memoryMaxConcurrent":               seededSettingStatus(cfg.MemoryMaxConcurrent, "memoryMaxConcurrent", "M365_MEMORY_MAX_CONCURRENT", persisted, positiveIntegerEnvActive("M365_MEMORY_MAX_CONCURRENT")),
		"memoryQueueTimeoutSeconds":         seededSettingStatus(cfg.MemoryQueueTimeoutSeconds, "memoryQueueTimeoutSeconds", "M365_MEMORY_QUEUE_TIMEOUT_SECONDS", persisted, positiveIntegerEnvActive("M365_MEMORY_QUEUE_TIMEOUT_SECONDS")),
		"interactivePriorityHoldoffSeconds": seededSettingStatus(cfg.InteractivePriorityHoldoffSeconds, "interactivePriorityHoldoffSeconds", interactivePriorityHoldoffEnvName(), persisted, nonNegativeIntegerEnvActive(interactivePriorityHoldoffEnvName())),
		"memoryBackoffInitialSeconds":       seededSettingStatus(cfg.MemoryBackoffInitialSeconds, "memoryBackoffInitialSeconds", "M365_MEMORY_BACKOFF_INITIAL_SECONDS", persisted, positiveIntegerEnvActive("M365_MEMORY_BACKOFF_INITIAL_SECONDS")),
		"memoryBackoffMaxSeconds":           seededSettingStatus(cfg.MemoryBackoffMaxSeconds, "memoryBackoffMaxSeconds", "M365_MEMORY_BACKOFF_MAX_SECONDS", persisted, positiveIntegerEnvActive("M365_MEMORY_BACKOFF_MAX_SECONDS")),
		"maxToolCallsPerTurn":               directOverrideSettingStatus(cfg.MaxToolCallsPerTurn, configuredToolCallLimit(store), "maxToolCallsPerTurn", "M365_MAX_TOOL_CALLS_PER_TURN", persisted),
		"maxToolRounds":                     directOverrideSettingStatus(cfg.MaxToolRounds, configuredMaxToolRounds(store), "maxToolRounds", "M365_MAX_TOOL_ROUNDS", persisted),
		"hermesMaxToolRounds":               directOverrideSettingStatus(cfg.HermesMaxToolRounds, configuredHermesMaxToolRounds(store), "hermesMaxToolRounds", "M365_HERMES_MAX_TOOL_ROUNDS", persisted),
		"contextWindow":                     seededSettingStatus(cfg.ContextWindow, "contextWindow", "M365_CONTEXT_WINDOW", persisted, positiveIntegerEnvActive("M365_CONTEXT_WINDOW")),
		"maxOutputTokens":                   seededSettingStatus(cfg.MaxOutputTokens, "maxOutputTokens", "M365_MAX_OUTPUT_TOKENS", persisted, positiveIntegerEnvActive("M365_MAX_OUTPUT_TOKENS")),
		"chatTimeoutSeconds":                seededSettingStatus(cfg.ChatTimeoutSeconds, "chatTimeoutSeconds", "M365_CHAT_TIMEOUT_SECONDS", persisted, positiveIntegerEnvActive("M365_CHAT_TIMEOUT_SECONDS")),
		"imageTimeoutSeconds":               seededSettingStatus(cfg.ImageTimeoutSeconds, "imageTimeoutSeconds", "M365_IMAGE_TIMEOUT_SECONDS", persisted, positiveIntegerEnvActive("M365_IMAGE_TIMEOUT_SECONDS")),
		"logLevel":                          seededSettingStatus(cfg.LogLevel, "logLevel", "M365_LOG_LEVEL", persisted, strings.TrimSpace(os.Getenv("M365_LOG_LEVEL")) != ""),
		"toolPlanningMode":                  seededSettingStatus(cfg.ToolPlanningMode, "toolPlanningMode", "M365_TOOL_PLANNING_MODE", persisted, strings.TrimSpace(os.Getenv("M365_TOOL_PLANNING_MODE")) != ""),
	}
	for field, spec := range map[string]struct {
		configured  any
		environment string
		parser      func(string) any
	}{
		"debugLogPath":     {cfg.DebugLogPath, "M365_DEBUG_LOG", nil},
		"listenAddress":    {cfg.ListenAddress, "M365_LISTEN", nil},
		"configPath":       {cfg.ConfigPath, "M365_CONFIG", nil},
		"tokenCachePath":   {cfg.TokenCachePath, "M365_TOKEN_CACHE", nil},
		"sessionCachePath": {cfg.SessionCachePath, "M365_SESSION_CACHE", nil},
		"outboundProxy":    {cfg.OutboundProxy, outbound.EnvProxy, nil},
		"clientId":         {cfg.ClientID, "M365_CLIENT_ID", nil},
		"authority":        {cfg.Authority, "M365_AUTHORITY", nil},
		"redirectUri":      {cfg.RedirectURI, "M365_REDIRECT_URI", nil},
		"scope":            {cfg.Scope, "M365_SCOPE", nil},
	} {
		status[field] = restartSettingStatus(spec.configured, field, spec.environment, persisted, injected, spec.parser)
	}
	return status
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.settings.get()
		traffic := s.compatibilityTrafficRuntime().snapshot()
		checkpointPersistence := transportCheckpointPersistenceSnapshot{}
		if s.checkpoints != nil {
			checkpointPersistence = s.checkpoints.persistenceSnapshot()
		}
		genericRounds := configuredMaxToolRounds(s.settings)
		hermesRounds := configuredHermesMaxToolRounds(s.settings)
		jsonOut(w, map[string]any{
			"settings":              cfg,
			"settingStatus":         settingsStatus(s.settings),
			"compatibilityTraffic":  traffic,
			"checkpointPersistence": checkpointPersistence,
			"toolRoundPolicy": map[string]int{
				"generic": genericRounds,
				"hermes":  hermesRounds,
				"memory":  genericRounds,
			},
			"codexModels":           managementRouteIDs(cfg.ModelMappings),
			"upstreamTones":         knownUpstreamTones(),
			"restartRequiredFields": []string{"listenAddress", "configPath", "tokenCachePath", "sessionCachePath", "outboundProxy", "clientId", "authority", "redirectUri", "scope", "debugLogPath"},
		})
	case http.MethodPut:
		current := s.settings.get()
		v := current
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			writeOpenAIError(w, 400, "invalid_request_error", "JSON 格式錯誤")
			return
		}
		if strings.TrimSpace(v.ToolPlanningMode) == "" {
			v.ToolPlanningMode = current.ToolPlanningMode
		}
		if e := validateSettings(v); e != nil {
			writeOpenAIError(w, 400, "invalid_request_error", e.Error())
			return
		}
		s.checkpointLifecycle.Lock()
		var e error
		commit := func() error { return s.settings.save(v) }
		if current.ChatMode != v.ChatMode && s.checkpoints != nil {
			e = s.checkpoints.ClearThen(commit)
		} else {
			e = commit()
		}
		s.checkpointLifecycle.Unlock()
		if e != nil {
			writeOpenAIError(w, 400, "invalid_request_error", e.Error())
			return
		}
		jsonOut(w, map[string]any{"ok": true, "settings": v})
	default:
		writeOpenAIError(w, 405, "invalid_request_error", "不支援此 HTTP 方法")
	}
}
func configuredToolCallLimit(s *settingsStore) int {
	if raw, ok := os.LookupEnv("M365_MAX_TOOL_CALLS_PER_TURN"); ok {
		if n, e := strconv.Atoi(strings.TrimSpace(raw)); e == nil && n >= 1 && n <= 64 {
			return n
		}
		return 1
	}
	return s.get().MaxToolCallsPerTurn
}

func toolDefinitionClearlyReadOnly(function map[string]any) bool {
	name, _ := function["name"].(string)
	description, _ := function["description"].(string)
	parameters, _ := json.Marshal(function["parameters"])
	if toolLooksMutating(name) || toolLooksMutating(description) || toolLooksMutating(string(parameters)) {
		return false
	}

	readOnlyHint := false
	if raw, present := function["annotations"]; present {
		annotations, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if rawHint, present := annotations["readOnlyHint"]; present {
			hint, ok := rawHint.(bool)
			if !ok || !hint {
				return false
			}
			readOnlyHint = true
		}
		if rawHint, present := annotations["destructiveHint"]; present {
			hint, ok := rawHint.(bool)
			if !ok || hint {
				return false
			}
		}
	}
	return readOnlyHint
}

// configuredRequestToolCallLimit fixes the caller-visible ceiling before any
// upstream model decision. Router prompts, native requests, and returned-call
// count validation must all use this same value so checkpoint/tool state cannot
// advance under one contract and then be rejected under a stricter one.
func configuredRequestToolCallLimit(request oaiReq, settings *settingsStore) int {
	configured := configuredToolCallLimit(settings)
	if configured < 2 || (request.ParallelToolCalls != nil && !*request.ParallelToolCalls) {
		return 1
	}
	if !requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(request.Tools), request.ToolChoice) {
		return 1
	}
	return configured
}

// requestToolCallLimit is retained at returned-call validation sites, but the
// calls are deliberately ignored: safety may not be tightened after the model
// has already acted under configuredRequestToolCallLimit.
func requestToolCallLimit(request oaiReq, _ []detectedToolCall, settings *settingsStore) int {
	return configuredRequestToolCallLimit(request, settings)
}

func requestToolDefinitionsClearlyParallelSafe(definitions []map[string]any, choice any) bool {
	seen := map[string]struct{}{}
	selectable := 0
	for _, definition := range definitions {
		typ, _ := definition["type"].(string)
		function, _ := definition["function"].(map[string]any)
		name, _ := function["name"].(string)
		name = strings.TrimSpace(name)
		if typ != "function" || name == "" || !toolChoiceAllows(choice, name) {
			continue
		}
		selectable++
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
		if !toolDefinitionClearlyReadOnly(function) {
			return false
		}
	}
	return selectable > 0
}

func toolLooksMutating(name string) bool {
	for _, token := range toolNameTokens(name) {
		switch token {
		case "exec", "execute", "shell", "command", "write", "edit", "update", "delete", "remove", "move", "rename", "create", "patch", "apply", "install", "run", "set", "reset", "put", "post", "send", "upload", "publish", "append", "insert", "start", "stop", "restart", "kill", "grant", "revoke", "mutate", "modify", "deploy", "submit", "add", "copy", "replace", "commit", "push", "merge", "enable", "disable", "approve", "reject", "cancel", "archive", "restore", "assign", "invite", "rotate":
			return true
		}
	}
	return false
}

func toolLooksReadOnly(name string) bool {
	for _, token := range toolNameTokens(name) {
		switch token {
		case "read", "list", "search", "find", "get", "fetch", "lookup", "inspect", "stat", "status", "describe", "info":
			return true
		}
	}
	return false
}

func toolNameTokens(name string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(r rune) bool {
		return r < 'a' || r > 'z'
	})
}

func validateToolCallLimit(c []detectedToolCall, n int) error {
	if n < 1 {
		n = 1
	}
	if len(c) > n {
		return fmt.Errorf("model returned %d tool calls, exceeding the allowed limit of %d; refusing to truncate upstream tool state", len(c), n)
	}
	return nil
}

func currentSettings() runtimeSettings { return openSettingsStore().get() }

// ApplyStartupSettingsEnv loads persisted restart-required fields before the
// rest of the application initializes. Explicit process environment variables
// always win over values saved from the web console.
func ApplyStartupSettingsEnv() error {
	store := openSettingsStore()
	if store.loadErr != nil {
		return store.loadErr
	}
	s := store.get()
	values := map[string]string{"M365_LISTEN": s.ListenAddress, "M365_CONFIG": s.ConfigPath, "M365_TOKEN_CACHE": s.TokenCachePath, "M365_SESSION_CACHE": s.SessionCachePath, outbound.EnvProxy: s.OutboundProxy, "M365_CLIENT_ID": s.ClientID, "M365_AUTHORITY": s.Authority, "M365_REDIRECT_URI": s.RedirectURI, "M365_SCOPE": s.Scope, "M365_DEBUG_LOG": s.DebugLogPath}
	for k, v := range values {
		if _, exists := os.LookupEnv(k); exists || strings.TrimSpace(v) == "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("apply persisted setting %s: %w", k, err)
		}
		store.markStartupInjectedEnv(k, v)
	}
	return nil
}
