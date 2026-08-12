package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"m365-native/internal/chathub"
)

func parallelToolForTest(name, description string, parameters map[string]any) chathub.Tool {
	definition, _ := json.Marshal(map[string]any{"name": name, "description": description, "parameters": parameters})
	return chathub.Tool{Type: "function", Function: definition}
}

func parallelRawToolForTest(function map[string]any) chathub.Tool {
	definition, _ := json.Marshal(function)
	return chathub.Tool{Type: "function", Function: definition}
}

func explicitReadOnlyToolForTest(name, description string, parameters map[string]any) chathub.Tool {
	return parallelRawToolForTest(map[string]any{
		"name":        name,
		"description": description,
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false},
		"parameters":  parameters,
	})
}

func TestMemoryCompatibilityFreshInstallDefaultsOff(t *testing.T) {
	store := loadSettingsStore(filepath.Join(t.TempDir(), "missing-settings.json"))
	if store.loadErr != nil {
		t.Fatal(store.loadErr)
	}
	if store.get().MemoryCompatibilityEnabled {
		t.Fatal("fresh install implicitly enabled Memory compatibility")
	}
}

func TestMemoryCompatibilityMigratesExistingMissingFieldToExplicitEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := defaultRuntimeSettings()
	legacy.MemoryCompatibilityEnabled = false
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "memoryCompatibilityEnabled")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store := loadSettingsStore(path)
	if store.loadErr != nil {
		t.Fatal(store.loadErr)
	}
	if !store.get().MemoryCompatibilityEnabled {
		t.Fatal("existing installation missing the field was not migration-enabled")
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var migrated map[string]any
	if err := json.Unmarshal(persisted, &migrated); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := migrated["memoryCompatibilityEnabled"].(bool); !ok || !enabled {
		t.Fatalf("migration did not persist explicit enabled state: %#v", migrated["memoryCompatibilityEnabled"])
	}
}

func TestHermesToolRoundLimitDefaultsWhenExistingSettingsLackNewField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := defaultRuntimeSettings()
	legacy.HermesMaxToolRounds = 0
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "hermesMaxToolRounds")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store := loadSettingsStore(path)
	if store.loadErr != nil {
		t.Fatal(store.loadErr)
	}
	if got := store.get().HermesMaxToolRounds; got != 128 {
		t.Fatalf("Hermes max tool rounds=%d want=128", got)
	}
}

func TestMemoryCompatibilityExplicitDisabledSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := &settingsStore{path: path, v: defaultRuntimeSettings()}
	settings := store.v
	settings.MemoryCompatibilityEnabled = false
	if err := store.save(settings); err != nil {
		t.Fatal(err)
	}
	reloaded := loadSettingsStore(path)
	if reloaded.loadErr != nil {
		t.Fatal(reloaded.loadErr)
	}
	if reloaded.get().MemoryCompatibilityEnabled {
		t.Fatal("explicit disabled Memory compatibility was overridden")
	}
}

func TestInteractivePriorityHoldoffEnvAcceptsZero(t *testing.T) {
	t.Setenv("M365_INTERACTIVE_PRIORITY_HOLDOFF_SECONDS", "0")
	if got := defaultRuntimeSettings().InteractivePriorityHoldoffSeconds; got != 0 {
		t.Fatalf("holdoff=%d", got)
	}
}

func TestInteractivePriorityHoldoffMigratesLegacyPersistedName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	settings := defaultRuntimeSettings()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "interactivePriorityHoldoffSeconds")
	fields["hermesPriorityHoldoffSeconds"] = float64(17)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := loadSettingsStore(path)
	if store.loadErr != nil {
		t.Fatal(store.loadErr)
	}
	if got := store.get().InteractivePriorityHoldoffSeconds; got != 17 {
		t.Fatalf("migrated holdoff=%d want=17", got)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("hermesPriorityHoldoffSeconds")) || !bytes.Contains(persisted, []byte(`"interactivePriorityHoldoffSeconds": 17`)) {
		t.Fatalf("legacy holdoff key was not migrated: %s", persisted)
	}
}

func TestValidateToolCallLimit(t *testing.T) {
	calls := []detectedToolCall{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if err := validateToolCallLimit(calls, 1); err == nil {
		t.Fatal("expected excess calls to be rejected")
	}
	if err := validateToolCallLimit(calls, 3); err != nil {
		t.Fatalf("calls at the configured limit were rejected: %v", err)
	}
	if err := validateToolCallLimit(calls, 99); err != nil {
		t.Fatalf("calls below the configured limit were rejected: %v", err)
	}
}
func TestOpenSettingsStoreConcurrentInitializationUsesSingleStore(t *testing.T) {
	previous := sharedSettings
	sharedSettings = nil
	t.Cleanup(func() { sharedSettings = previous })
	t.Setenv("M365_DATA_DIR", t.TempDir())

	const callers = 64
	start := make(chan struct{})
	stores := make(chan *settingsStore, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer workers.Done()
			<-start
			stores <- openSettingsStore()
		}()
	}
	close(start)
	workers.Wait()
	close(stores)

	unique := map[*settingsStore]struct{}{}
	for store := range stores {
		unique[store] = struct{}{}
	}
	if len(unique) != 1 {
		t.Fatalf("concurrent initialization created %d shared settings stores", len(unique))
	}
}

func TestAdminSettingsReportsConfiguredEffectiveAndSource(t *testing.T) {
	t.Setenv("M365_LISTEN", ":external")
	t.Setenv("M365_MAX_TOOL_CALLS_PER_TURN", "3")
	t.Setenv("M365_HERMES_MAX_TOOL_ROUNDS", "96")
	t.Setenv("M365_CHAT_TIMEOUT_SECONDS", "120")
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := &settingsStore{path: path, v: defaultRuntimeSettings()}
	persisted := seed.v
	persisted.ListenAddress = ":persisted"
	persisted.MaxToolCallsPerTurn = 1
	persisted.HermesMaxToolRounds = 128
	persisted.ChatTimeoutSeconds = 1800
	if err := seed.save(persisted); err != nil {
		t.Fatal(err)
	}
	store := loadSettingsStore(path)
	server := &Server{settings: store}
	recorder := httptest.NewRecorder()
	server.adminSettings(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		SettingStatus   map[string]settingValueStatus `json:"settingStatus"`
		ToolRoundPolicy map[string]int                `json:"toolRoundPolicy"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	listen := response.SettingStatus["listenAddress"]
	if listen.Configured != ":persisted" || listen.Effective != ":external" || listen.Source != "env" || !listen.Locked || listen.Environment != "M365_LISTEN" {
		t.Fatalf("listen status=%#v", listen)
	}
	toolCalls := response.SettingStatus["maxToolCallsPerTurn"]
	if toolCalls.Configured != float64(1) || toolCalls.Effective != float64(3) || toolCalls.Source != "env" || !toolCalls.Locked {
		t.Fatalf("tool-call status=%#v", toolCalls)
	}
	hermesRounds := response.SettingStatus["hermesMaxToolRounds"]
	if hermesRounds.Configured != float64(128) || hermesRounds.Effective != float64(96) || hermesRounds.Source != "env" || !hermesRounds.Locked || hermesRounds.Environment != "M365_HERMES_MAX_TOOL_ROUNDS" {
		t.Fatalf("Hermes round status=%#v", hermesRounds)
	}
	if response.ToolRoundPolicy["generic"] != 16 || response.ToolRoundPolicy["hermes"] != 96 || response.ToolRoundPolicy["memory"] != 16 {
		t.Fatalf("effective tool round policy=%#v", response.ToolRoundPolicy)
	}
	chatTimeout := response.SettingStatus["chatTimeoutSeconds"]
	if chatTimeout.Configured != float64(1800) || chatTimeout.Effective != float64(1800) || chatTimeout.Source != "file" || chatTimeout.Locked {
		t.Fatalf("chat-timeout status=%#v", chatTimeout)
	}
}

func TestAdminSettingsDistinguishesStartupInjectedFileEnvFromExternalLock(t *testing.T) {
	oldListen, hadListen := os.LookupEnv("M365_LISTEN")
	if err := os.Unsetenv("M365_LISTEN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadListen {
			_ = os.Setenv("M365_LISTEN", oldListen)
		} else {
			_ = os.Unsetenv("M365_LISTEN")
		}
	})
	dir := t.TempDir()
	t.Setenv("M365_DATA_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	seed := &settingsStore{path: path, v: defaultRuntimeSettings()}
	persisted := seed.v
	persisted.ListenAddress = ":from-file"
	if err := seed.save(persisted); err != nil {
		t.Fatal(err)
	}

	sharedSettingsMu.Lock()
	previous := sharedSettings
	sharedSettings = nil
	sharedSettingsMu.Unlock()
	t.Cleanup(func() {
		sharedSettingsMu.Lock()
		sharedSettings = previous
		sharedSettingsMu.Unlock()
	})
	if err := ApplyStartupSettingsEnv(); err != nil {
		t.Fatal(err)
	}
	store := openSettingsStore()
	updated := store.get()
	updated.ListenAddress = ":next-restart"
	if err := store.save(updated); err != nil {
		t.Fatal(err)
	}
	server := &Server{settings: store}
	recorder := httptest.NewRecorder()
	server.adminSettings(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	var response struct {
		SettingStatus map[string]settingValueStatus `json:"settingStatus"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	listen := response.SettingStatus["listenAddress"]
	if listen.Configured != ":next-restart" || listen.Effective != ":from-file" || listen.Source != "file" || listen.Locked || !listen.RestartRequired {
		t.Fatalf("startup-injected listen status=%#v", listen)
	}
}

func TestLongestRequestTimeoutIncludesMemoryQueueBeforeChat(t *testing.T) {
	settings := defaultRuntimeSettings()
	settings.MemoryCompatibilityEnabled = true
	settings.MemoryQueueTimeoutSeconds = 600
	settings.ChatTimeoutSeconds = 3600
	settings.ImageTimeoutSeconds = 150
	server := &Server{settings: &settingsStore{v: settings}}
	if got := server.LongestRequestTimeout(); got != 4200*time.Second {
		t.Fatalf("longest request timeout=%v want=%v", got, 4200*time.Second)
	}
}

func TestSettingsPersistAndValidate(t *testing.T) {
	s := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	v := s.v
	v.MaxToolCallsPerTurn = 1
	v.MaxToolRounds = 32
	v.ChatTimeoutSeconds = 60
	v.ImageTimeoutSeconds = 90
	if err := s.save(v); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path); err != nil {
		t.Fatal(err)
	}
	v.MaxToolCallsPerTurn = 0
	if err := s.save(v); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSettingsSaveFailurePreservesPreviousDiskAndRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := &settingsStore{path: path, v: defaultRuntimeSettings()}
	previous := s.v
	previous.MaxToolRounds = 24
	if err := s.save(previous); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	s.persist = func(string, []byte) error { return errors.New("forced settings persistence failure") }
	next := previous
	next.MaxToolRounds = 48
	if err := s.save(next); err == nil {
		t.Fatal("expected persistence failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("failed save changed persisted settings:\nbefore=%s\nafter=%s", before, after)
	}
	if got := s.get().MaxToolRounds; got != previous.MaxToolRounds {
		t.Fatalf("failed save changed runtime settings: got=%d want=%d", got, previous.MaxToolRounds)
	}
}

func TestSettingsSaveSerializesPersistenceAndRuntimeCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := &settingsStore{path: path, v: defaultRuntimeSettings()}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	var (
		mu    sync.Mutex
		calls int
	)
	s.persist = func(path string, raw []byte) error {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		} else if call == 2 {
			secondEntered <- struct{}{}
		}
		return atomicWriteSettingsFile(path, raw)
	}

	first := s.v
	first.MaxToolRounds = 24
	second := s.v
	second.MaxToolRounds = 48
	errCh := make(chan error, 2)
	go func() { errCh <- s.save(first) }()
	<-firstStarted
	go func() { errCh <- s.save(second) }()
	select {
	case <-secondEntered:
		t.Fatal("a second save entered persistence before the first save committed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}

	persisted := loadSettingsStore(path)
	if persisted.loadErr != nil {
		t.Fatal(persisted.loadErr)
	}
	if got, want := persisted.get().MaxToolRounds, s.get().MaxToolRounds; got != want {
		t.Fatalf("disk/runtime settings diverged after concurrent saves: disk=%d runtime=%d", got, want)
	}
}

func TestModelMappingsValidate(t *testing.T) {
	v := defaultRuntimeSettings()
	v.ModelMappings = []modelMapping{{PublicModel: "gpt-5.6-sol", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "GPT-5.6-Sol", DefaultReasoningLevel: "low"}}
	if err := validateSettings(v); err != nil {
		t.Fatal(err)
	}
	v.ModelMappings[0].UpstreamTone = "unknown"
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted unknown upstream tone")
	}
	v.ModelMappings[0].UpstreamTone = "Gpt_5_6_Reasoning"
	v.ModelMappings = append(v.ModelMappings, v.ModelMappings[0])
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted duplicate public model")
	}
	v.ModelMappings = []modelMapping{{PublicModel: "custom-codex-route", UpstreamTone: "Gpt_5_6_Reasoning", DisplayName: "Custom Codex Route", DefaultReasoningLevel: "medium"}}
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected custom public model: %v", err)
	}
}

func TestOutboundProxySettingValidation(t *testing.T) {
	v := defaultRuntimeSettings()
	v.OutboundProxy = "socks5://proxy.example:1080"
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected SOCKS5 proxy: %v", err)
	}
	v.OutboundProxy = "https://proxy.example:8443"
	if err := validateSettings(v); err != nil {
		t.Fatalf("rejected HTTPS proxy: %v", err)
	}
	v.OutboundProxy = "ftp://proxy.example:21"
	if err := validateSettings(v); err == nil {
		t.Fatal("accepted unsupported proxy scheme")
	}
}

func TestRequestToolDefinitionsClearlyParallelSafeRequiresExplicitReadOnlyCatalog(t *testing.T) {
	safeTools := []chathub.Tool{
		explicitReadOnlyToolForTest("read_file", "Read file contents without changing state.", map[string]any{"type": "object"}),
		explicitReadOnlyToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object"}),
	}
	if !requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(safeTools), "auto") {
		t.Fatal("explicitly read-only catalog was serialized")
	}
	unsafeTools := []chathub.Tool{
		safeTools[0],
		parallelToolForTest("exec_command", "Execute a command.", map[string]any{"type": "object"}),
	}
	if requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(unsafeTools), "auto") {
		t.Fatal("mutating or ambiguous catalog was allowed to advertise parallel calls")
	}
}

func TestRequestToolDefinitionsClearlyParallelSafeFailsClosedOnCatalogAmbiguity(t *testing.T) {
	safeTools := []chathub.Tool{
		explicitReadOnlyToolForTest("read_file", "Read file contents without changing state.", map[string]any{"type": "object"}),
		explicitReadOnlyToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object"}),
	}
	tests := []struct {
		name  string
		tools []chathub.Tool
	}{
		{name: "misleading read name", tools: []chathub.Tool{safeTools[0], parallelToolForTest("get_account", "Delete an account permanently.", map[string]any{"type": "object"})}},
		{name: "missing safety evidence", tools: []chathub.Tool{safeTools[0], parallelToolForTest("search_code", "", map[string]any{"type": "object"})}},
		{name: "duplicate definition", tools: append(append([]chathub.Tool{}, safeTools...), safeTools[1])},
		{name: "conflicting annotations", tools: []chathub.Tool{safeTools[0], parallelRawToolForTest(map[string]any{"name": "search_code", "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": true}, "parameters": map[string]any{"type": "object"}})}},
		{name: "false read-only annotation", tools: []chathub.Tool{safeTools[0], parallelRawToolForTest(map[string]any{"name": "search_code", "annotations": map[string]any{"readOnlyHint": false}, "parameters": map[string]any{"type": "object"}})}},
		{name: "schema mutation signal", tools: []chathub.Tool{safeTools[0], parallelToolForTest("search_code", "Search source code without changing state.", map[string]any{"type": "object", "properties": map[string]any{"delete": map[string]any{"type": "boolean"}}})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(test.tools), "auto") {
				t.Fatal("unsafe or ambiguous catalog was allowed to advertise parallel calls")
			}
		})
	}

	mixed := []chathub.Tool{safeTools[0], parallelToolForTest("terminal", "Run a command.", map[string]any{"type": "object"})}
	namedRead := map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}}
	if !requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(mixed), namedRead) {
		t.Fatal("named explicit read-only selection was serialized because of an unselectable unsafe tool")
	}
	if requestToolDefinitionsClearlyParallelSafe(toolDefinitionMaps(mixed), "auto") {
		t.Fatal("mixed auto-selectable catalog was allowed to advertise parallel calls")
	}
}
