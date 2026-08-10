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
	ChatMode                     string         `json:"chatMode"`
	HermesCompatibilityEnabled   bool           `json:"hermesCompatibilityEnabled"`
	MemoryCompatibilityEnabled   bool           `json:"memoryCompatibilityEnabled"`
	MemoryMaxConcurrent          int            `json:"memoryMaxConcurrent"`
	MemoryQueueTimeoutSeconds    int            `json:"memoryQueueTimeoutSeconds"`
	HermesPriorityHoldoffSeconds int            `json:"hermesPriorityHoldoffSeconds"`
	MemoryBackoffInitialSeconds  int            `json:"memoryBackoffInitialSeconds"`
	MemoryBackoffMaxSeconds      int            `json:"memoryBackoffMaxSeconds"`
	TextInputLimitUTF16          int            `json:"textInputLimitUTF16"`
	MaxToolCallsPerTurn          int            `json:"maxToolCallsPerTurn"`
	MaxToolRounds                int            `json:"maxToolRounds"`
	ContextWindow                int            `json:"contextWindow"`
	MaxOutputTokens              int            `json:"maxOutputTokens"`
	ChatTimeoutSeconds           int            `json:"chatTimeoutSeconds"`
	ImageTimeoutSeconds          int            `json:"imageTimeoutSeconds"`
	LogLevel                     string         `json:"logLevel"`
	DebugLogPath                 string         `json:"debugLogPath"`
	ListenAddress                string         `json:"listenAddress"`
	ConfigPath                   string         `json:"configPath"`
	TokenCachePath               string         `json:"tokenCachePath"`
	SessionCachePath             string         `json:"sessionCachePath"`
	OutboundProxy                string         `json:"outboundProxy"`
	ProxyPool                    []string       `json:"proxyPool,omitempty"`
	ClientID                     string         `json:"clientId"`
	Authority                    string         `json:"authority"`
	RedirectURI                  string         `json:"redirectUri"`
	Scope                        string         `json:"scope"`
	ModelMappings                []modelMapping `json:"modelMappings"`
	ToolPlanningMode             string         `json:"toolPlanningMode"`
}

type settingsStore struct {
	mu      sync.RWMutex
	path    string
	v       runtimeSettings
	loadErr error
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
func defaultRuntimeSettings() runtimeSettings {
	return runtimeSettings{
		ChatMode:                     chatModePrivate,
		HermesCompatibilityEnabled:   true,
		MemoryCompatibilityEnabled:   true,
		MemoryMaxConcurrent:          envInt("M365_MEMORY_MAX_CONCURRENT", 2),
		MemoryQueueTimeoutSeconds:    envInt("M365_MEMORY_QUEUE_TIMEOUT_SECONDS", 60),
		HermesPriorityHoldoffSeconds: envNonNegativeInt("M365_HERMES_PRIORITY_HOLDOFF_SECONDS", 30),
		MemoryBackoffInitialSeconds:  envInt("M365_MEMORY_BACKOFF_INITIAL_SECONDS", 5),
		MemoryBackoffMaxSeconds:      envInt("M365_MEMORY_BACKOFF_MAX_SECONDS", 60),
		TextInputLimitUTF16:          defaultTextInputLimitUTF16,
		MaxToolCallsPerTurn:          envInt("M365_MAX_TOOL_CALLS_PER_TURN", 2), MaxToolRounds: envInt("M365_MAX_TOOL_ROUNDS", 16),
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

var sharedSettings *settingsStore

func openSettingsStore() *settingsStore {
	if sharedSettings != nil {
		return sharedSettings
	}
	sharedSettings = loadSettingsStore(settingsPath())
	return sharedSettings
}

func loadSettingsStore(path string) *settingsStore {
	s := &settingsStore{path: path, v: defaultRuntimeSettings()}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.v); err != nil {
			s.loadErr = fmt.Errorf("decode runtime settings: %w", err)
		}
	} else if !os.IsNotExist(err) {
		s.loadErr = fmt.Errorf("read runtime settings: %w", err)
	}
	if s.loadErr == nil {
		s.loadErr = validateSettings(s.v)
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
	if v.MemoryMaxConcurrent < 1 || v.MemoryMaxConcurrent > 16 {
		return fmt.Errorf("Memory 同時請求上限必須為 1-16")
	}
	if v.MemoryQueueTimeoutSeconds < 1 || v.MemoryQueueTimeoutSeconds > 600 {
		return fmt.Errorf("Memory 排隊逾時必須為 1-600 秒")
	}
	if v.HermesPriorityHoldoffSeconds < 0 || v.HermesPriorityHoldoffSeconds > 300 {
		return fmt.Errorf("Hermes 優先保留時間必須為 0-300 秒")
	}
	if v.MemoryBackoffInitialSeconds < 1 || v.MemoryBackoffInitialSeconds > 300 {
		return fmt.Errorf("Memory 初始退避必須為 1-300 秒")
	}
	if v.MemoryBackoffMaxSeconds < v.MemoryBackoffInitialSeconds || v.MemoryBackoffMaxSeconds > 3600 {
		return fmt.Errorf("Memory 最大退避必須大於等於初始退避且不超過 3600 秒")
	}
	if _, err := requestBodyLimit(v.TextInputLimitUTF16); err != nil {
		return err
	}
	if v.MaxToolCallsPerTurn < 1 || v.MaxToolCallsPerTurn > 64 {
		return fmt.Errorf("每輪工具呼叫數必須為 1-64")
	}
	if v.MaxToolRounds < 1 || v.MaxToolRounds > 512 {
		return fmt.Errorf("最大工具輪次必須為 1-512")
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
	for _, proxyURL := range v.ProxyPool {
		if err := outbound.ValidateProxyURL(strings.TrimSpace(proxyURL)); err != nil {
			return err
		}
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
	if e := validateSettings(v); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	if e := os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return e
	}
	if e := os.WriteFile(s.path, b, 0600); e != nil {
		return e
	}
	s.mu.Lock()
	s.v = v
	s.loadErr = nil
	s.mu.Unlock()
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

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.settings.get()
		traffic := compatibilityTrafficSnapshot{}
		if s.compatTraffic != nil {
			traffic = s.compatTraffic.snapshot()
		}
		jsonOut(w, map[string]any{"settings": cfg, "compatibilityTraffic": traffic, "codexModels": managementRouteIDs(cfg.ModelMappings), "upstreamTones": knownUpstreamTones(), "restartRequiredFields": []string{"listenAddress", "configPath", "tokenCachePath", "sessionCachePath", "outboundProxy", "proxyPool", "clientId", "authority", "redirectUri", "scope", "debugLogPath"}})
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
		if current.ChatMode != v.ChatMode && s.checkpoints != nil {
			e = s.checkpoints.Clear()
		}
		if e == nil {
			e = s.settings.save(v)
		}
		s.checkpointLifecycle.Unlock()
		if e != nil {
			writeOpenAIError(w, 400, "invalid_request_error", e.Error())
			return
		}
		if e := outbound.ConfigurePool(v.ProxyPool); e != nil {
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

// adaptiveToolCallLimit permits parallel calls only when every call is a
// read-only, independently addressable operation. Any write, execution,
// mutation, or ambiguous tool is serialized conservatively.
func adaptiveToolCallLimit(c []detectedToolCall, definitions []map[string]any, configured int) int {
	if len(c) < 2 || configured < 2 {
		return 1
	}
	seenIDs := make(map[string]struct{}, len(c))
	for _, call := range c {
		if call.ID == "" {
			return 1
		}
		if _, duplicate := seenIDs[call.ID]; duplicate {
			return 1
		}
		seenIDs[call.ID] = struct{}{}
		definition, ok := uniqueParallelToolDefinition(call, definitions)
		if !ok || !toolDefinitionClearlyReadOnly(definition) {
			return 1
		}
	}
	return configured
}

func requestToolCallLimit(request oaiReq, calls []detectedToolCall, settings *settingsStore) int {
	return adaptiveToolCallLimit(calls, toolDefinitionMaps(request.Tools), configuredRequestToolCallLimit(request, settings))
}

func uniqueParallelToolDefinition(call detectedToolCall, definitions []map[string]any) (map[string]any, bool) {
	if strings.TrimSpace(call.Name) == "" || call.Type != "function" {
		return nil, false
	}
	var match map[string]any
	for _, definition := range definitions {
		if typ, _ := definition["type"].(string); typ != "function" {
			continue
		}
		function, _ := definition["function"].(map[string]any)
		name, _ := function["name"].(string)
		if name != call.Name {
			continue
		}
		if match != nil {
			return nil, false
		}
		match = function
	}
	return match, match != nil
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
	return readOnlyHint || toolLooksReadOnly(description)
}

func configuredRequestToolCallLimit(request oaiReq, settings *settingsStore) int {
	if request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return 1
	}
	return configuredToolCallLimit(settings)
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

func limitToolCalls(c []detectedToolCall, n int) []detectedToolCall {
	if n < 1 {
		n = 1
	}
	if len(c) > n {
		return c[:n]
	}
	return c
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
	values := map[string]string{"M365_LISTEN": s.ListenAddress, "M365_CONFIG": s.ConfigPath, "M365_TOKEN_CACHE": s.TokenCachePath, "M365_SESSION_CACHE": s.SessionCachePath, outbound.EnvProxy: s.OutboundProxy, "M365_PROXY_POOL": strings.Join(s.ProxyPool, "\n"), "M365_CLIENT_ID": s.ClientID, "M365_AUTHORITY": s.Authority, "M365_REDIRECT_URI": s.RedirectURI, "M365_SCOPE": s.Scope, "M365_DEBUG_LOG": s.DebugLogPath}
	for k, v := range values {
		if _, exists := os.LookupEnv(k); !exists && strings.TrimSpace(v) != "" {
			_ = os.Setenv(k, v)
		}
	}
	return nil
}
