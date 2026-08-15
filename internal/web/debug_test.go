package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var debugSensitiveSentinels = []string{
	"token-sentinel-20",
	"cookie-sentinel-20",
	"person-sentinel-20@example.test",
	"oid-sentinel-20",
	"tid-sentinel-20",
	"prompt-sentinel-20",
	"body-sentinel-20",
	"tool-sentinel-20",
	"attachment-sentinel-20",
}

func assertNoDebugSentinels(t *testing.T, raw []byte) {
	t.Helper()
	for _, sentinel := range debugSensitiveSentinels {
		if bytes.Contains(raw, []byte(sentinel)) {
			t.Fatalf("sensitive sentinel persisted: %q in %s", sentinel, raw)
		}
	}
}

func testDebugPolicy() debugStorePolicy {
	return debugStorePolicy{
		SummaryTTL:          time.Hour,
		MaxRecords:          20,
		MaxBytes:            256 << 10,
		AuditMaxRecords:     20,
		SnapshotTTL:         5 * time.Minute,
		SnapshotMaxRecords:  4,
		SnapshotMaxBytes:    64 << 10,
		PayloadCaptureBytes: 8 << 10,
	}
}

func TestDebugStorePathFollowsDurablePathPrecedence(t *testing.T) {
	dataDir := t.TempDir()
	explicitPath := filepath.Join(t.TempDir(), "explicit-debug.json")
	settingsFile := filepath.Join(t.TempDir(), "settings", "settings.json")
	t.Setenv("M365_DATA_DIR", dataDir)
	t.Setenv("M365_SETTINGS_FILE", settingsFile)
	t.Setenv("M365_DEBUG_LOG", explicitPath)
	if got := debugStorePath(); got != explicitPath {
		t.Fatalf("explicit debug path=%q want=%q", got, explicitPath)
	}

	t.Setenv("M365_DEBUG_LOG", "")
	if got, want := debugStorePath(), filepath.Join(dataDir, "debug-logs.json"); got != want {
		t.Fatalf("data-dir debug path=%q want=%q", got, want)
	}

	t.Setenv("M365_DATA_DIR", "")
	if got, want := debugStorePath(), filepath.Join(filepath.Dir(settingsFile), "debug-logs.json"); got != want {
		t.Fatalf("settings-dir debug path=%q want=%q", got, want)
	}
}

func TestDefaultDebugStorePersistsInDataDirectoryAndReopens(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("M365_DEBUG_LOG", "")
	t.Setenv("M365_DATA_DIR", dataDir)
	path := filepath.Join(dataDir, "debug-logs.json")
	now := time.Now().UTC()

	store := openDebugStore()
	defer store.stopAutoExpiry()
	store.mu.Lock()
	store.data.Records = append(store.data.Records, debugRecord{
		ID:        "dbg_default_path_test",
		Level:     "info",
		Protocol:  "test",
		At:        now,
		ExpiresAt: now.Add(time.Hour),
	})
	err := store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("debug store mode=%#o want=0600", info.Mode().Perm())
	}

	reopened := openDebugStoreWithPolicy(path, defaultDebugStorePolicy())
	records := reopened.list()
	if len(records) != 1 || records[0].ID != "dbg_default_path_test" {
		t.Fatalf("reopened records=%+v", records)
	}
}

func TestDebugDefaultPersistsOnlySummaryWithoutSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	settings := defaultRuntimeSettings()
	settings.ModelMappings = []modelMapping{{PublicModel: debugSensitiveSentinels[2], UpstreamTone: "magic", DisplayName: "synthetic", DefaultReasoningLevel: "none"}}
	server := &Server{debug: store, settings: &settingsStore{v: settings}}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		jsonOut(w, map[string]any{"body": "body-sentinel-20", "prompt": "prompt-sentinel-20"})
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"person-sentinel-20@example.test","messages":[{"role":"user","content":"prompt-sentinel-20"}]}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	records := store.list()
	if len(records) != 1 || records[0].Snapshot != nil || records[0].SnapshotAvailable || records[0].Route != "configured_mapping" {
		t.Fatalf("default capture was not summary-only and route-safe: %+v", records)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoDebugSentinels(t, raw)
}

func TestDebugSummaryPersistsOnlySafeIngressExtensionMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	server := &Server{debug: store, settings: &settingsStore{v: defaultRuntimeSettings()}}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-M365-Preserved-Extension-Counts", "top=1,message=1,item=0,content=1,tool=1,format=0,reasoning=0")
		w.Header().Set("X-M365-Preserved-Extension-Names", "top:future_top,message:future_message,content:future_block,tool:future_tool,bad kind:PRIVATE_VALUE")
		jsonOut(w, map[string]any{"ok": true})
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m365-auto","messages":[{"role":"user","content":"PRIVATE_VALUE"}]}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	records := store.list()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	record := records[0]
	if record.PreservedExtensionCounts != "top=1,message=1,item=0,content=1,tool=1,format=0,reasoning=0" {
		t.Fatalf("counts=%q", record.PreservedExtensionCounts)
	}
	if got := strings.Join(record.PreservedExtensionNames, ","); got != "top:future_top,message:future_message,content:future_block,tool:future_tool" {
		t.Fatalf("names=%q", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("PRIVATE_VALUE")) {
		t.Fatalf("private caller scalar leaked into debug summary: %s", raw)
	}
}

func TestDebugPersistenceIsSummaryFirstAndDeeplyRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.startSession(2 * time.Minute); err != nil {
		t.Fatal(err)
	}

	server := &Server{debug: store}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		jsonOut(w, map[string]any{
			"body":       debugSensitiveSentinels[6],
			"email":      debugSensitiveSentinels[2],
			"oid":        debugSensitiveSentinels[3],
			"tid":        debugSensitiveSentinels[4],
			"tool":       debugSensitiveSentinels[7],
			"attachment": debugSensitiveSentinels[8],
		})
	}))
	body := `{
		"model":"m365-auto",
		"messages":[{"role":"user","content":"prompt-sentinel-20"}],
		"tools":[{"type":"function","function":{"name":"tool-sentinel-20","arguments":{"body":"body-sentinel-20"}}}],
		"attachments":[{"name":"attachment-sentinel-20","url":"data:text/plain;base64,Y29va2llLXNlbnRpbmVsLTIw"}],
		"email":"person-sentinel-20@example.test",
		"oid":"oid-sentinel-20",
		"tid":"tid-sentinel-20",
		"access_token":"token-sentinel-20"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-sentinel-20")
	req.Header.Set("Cookie", "session=cookie-sentinel-20")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	records := store.list()
	if len(records) != 1 {
		t.Fatalf("records=%d want=1", len(records))
	}
	record := records[0]
	if record.Protocol != "openai_chat_completions" || record.Route != "m365-auto" || record.Path != "/v1/chat/completions" {
		t.Fatalf("summary routing=%+v", record)
	}
	if record.MessageCount != 1 || record.ToolCount != 1 || record.AttachmentCount != 1 || record.InputTokens == 0 || record.OutputTokens == 0 || record.EventCount == 0 {
		t.Fatalf("summary counts=%+v", record)
	}
	if record.Snapshot == nil || !record.SnapshotAvailable || record.SnapshotExpiresAt.IsZero() {
		t.Fatalf("diagnostic snapshot missing: %+v", record)
	}
	if record.Snapshot.Request.Shape.Strings == 0 || record.Snapshot.Response.Shape.Objects == 0 {
		t.Fatalf("snapshot shape missing: %+v", record.Snapshot)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoDebugSentinels(t, raw)
	var persisted debugStoreData
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode persisted summary: %v; raw=%s", err, raw)
	}
	if persisted.Schema != debugStoreSchema || len(persisted.Records) != 1 {
		t.Fatalf("persisted=%+v", persisted)
	}
}

func TestDebugStoreEnforcesTTLSizeAndRecordLimits(t *testing.T) {
	policy := testDebugPolicy()
	policy.SummaryTTL = time.Minute
	policy.MaxRecords = 2
	policy.MaxBytes = 12 << 10
	policy.SnapshotTTL = 30 * time.Second
	policy.SnapshotMaxRecords = 1
	policy.SnapshotMaxBytes = 4 << 10
	policy.PayloadCaptureBytes = 512
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, policy)
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if _, err := store.startSession(policy.SnapshotTTL); err != nil {
		t.Fatal(err)
	}
	server := &Server{debug: store}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"status": "ok", "body": strings.Repeat("x", 2048)})
	}))
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m365-auto","messages":[{"role":"user","content":"safe synthetic probe"}]}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		now = now.Add(time.Second)
	}
	records := store.list()
	if len(records) != policy.MaxRecords {
		t.Fatalf("record limit=%d want=%d records=%+v", len(records), policy.MaxRecords, records)
	}
	snapshots := 0
	for _, record := range records {
		if record.Snapshot != nil {
			snapshots++
		}
	}
	if snapshots > policy.SnapshotMaxRecords {
		t.Fatalf("snapshot records=%d max=%d", snapshots, policy.SnapshotMaxRecords)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > int64(policy.MaxBytes) {
		t.Fatalf("debug store bytes=%d max=%d", st.Size(), policy.MaxBytes)
	}

	now = now.Add(2 * time.Minute)
	if records := store.list(); len(records) != 0 {
		t.Fatalf("expired records retained: %+v", records)
	}
	if status := store.sessionStatus(); status.Active {
		t.Fatalf("expired diagnostic session remained active: %+v", status)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted debugStoreData
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Records) != 0 || persisted.Session != nil {
		t.Fatalf("expired data persisted: %+v", persisted)
	}
}

func TestDebugStoreAutomaticallyExpiresWithoutAReadTrigger(t *testing.T) {
	policy := testDebugPolicy()
	policy.SummaryTTL = 80 * time.Millisecond
	policy.SnapshotTTL = 40 * time.Millisecond
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, policy)
	store.startAutoExpiry()
	t.Cleanup(store.stopAutoExpiry)
	if _, err := store.startSession(policy.SnapshotTTL); err != nil {
		t.Fatal(err)
	}
	server := &Server{debug: store}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"status": "ok"})
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m365-auto","messages":[{"role":"user","content":"safe synthetic probe"}]}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var persisted debugStoreData
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		if len(persisted.Records) == 0 && persisted.Session == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expired debug data remained without a read trigger: %+v", persisted)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDebugDiagnosticSessionIsPrivilegedAndExpires(t *testing.T) {
	server := newAdminSecurityServer(t, "correct horse battery staple")
	handler := server.Routes()
	anonymousServer, anonymousClient := adminTestClient(t, handler)
	blocked := postJSON(t, anonymousClient, anonymousServer.URL+"/api/admin/debug/session", `{"ttlSeconds":60}`)
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous start status=%d", blocked.StatusCode)
	}

	testServer, client := adminTestClient(t, handler)
	login := postJSON(t, client, testServer.URL+"/api/admin/login", `{"password":"correct horse battery staple"}`)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", login.StatusCode)
	}
	start := postJSON(t, client, testServer.URL+"/api/admin/debug/session", `{"ttlSeconds":60}`)
	defer start.Body.Close()
	if start.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", start.StatusCode)
	}
	var started debugSessionStatus
	if err := json.NewDecoder(start.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	if !started.Active || started.ExpiresAt.IsZero() || started.Warning == "" {
		t.Fatalf("started session=%+v", started)
	}

	server.debug.now = func() time.Time { return started.ExpiresAt.Add(time.Second) }
	statusResponse, err := client.Get(testServer.URL + "/api/admin/debug/session")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResponse.Body.Close()
	if statusResponse.StatusCode != http.StatusOK {
		t.Fatalf("status code=%d", statusResponse.StatusCode)
	}
	var expired debugSessionStatus
	if err := json.NewDecoder(statusResponse.Body).Decode(&expired); err != nil {
		t.Fatal(err)
	}
	if expired.Active {
		t.Fatalf("expired session active=%+v", expired)
	}
}

func TestDebugStorePreservesLegacyCaptureDuringSummaryMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.jsonl")
	legacy := []byte(`{"client":"body-sentinel-20"}` + "\n")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	if store.path == path {
		t.Fatalf("legacy capture path reused: %q", store.path)
	}
	started, err := store.startSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preserved, legacy) {
		t.Fatalf("legacy capture changed: got=%q want=%q", preserved, legacy)
	}
	summary, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	assertNoDebugSentinels(t, summary)

	reopened := openDebugStoreWithPolicy(path, testDebugPolicy())
	if reopened.path != store.path {
		t.Fatalf("safe summary path changed across restart: first=%q reopened=%q", store.path, reopened.path)
	}
	reopenedStatus := reopened.sessionStatus()
	if !reopenedStatus.Active || reopenedStatus.ID != started.ID {
		t.Fatalf("safe summary state lost across restart: started=%+v reopened=%+v", started, reopenedStatus)
	}
}

func TestServiceLogSanitizersNeverEmitSensitiveValues(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	_ = upstreamError(errors.New("body-sentinel-20 token-sentinel-20 person-sentinel-20@example.test"))
	logChathubTrace(map[string]any{
		"stage":            "upload_start",
		"attachment_count": 1,
		"file_name":        "attachment-sentinel-20",
		"conversation_id":  "oid-sentinel-20",
	})
	logChathubTrace(map[string]any{"stage": "attachment-sentinel-20"})
	logChathubTrace(map[string]any{"stage": "chathub_payload", "private_mode": true})
	log.Printf("tool choice=%s", safeToolChoiceLog(normalizedToolChoiceMode(map[string]any{"name": debugSensitiveSentinels[7]})))
	trace := httpTrace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	trace.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("prompt-sentinel-20", "/v1/person-sentinel-20@example.test", nil))
	assertNoDebugSentinels(t, logs.Bytes())
	if !strings.Contains(logs.String(), "code=upstream_error") || !strings.Contains(logs.String(), "stage=unknown") {
		t.Fatalf("safe diagnostic codes missing: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "stage=chathub_payload") || !strings.Contains(logs.String(), "private_mode=true") {
		t.Fatalf("private WebSocket trace missing: %s", logs.String())
	}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "stage=upload_start") && strings.Contains(line, "private_mode=") {
			t.Fatalf("upload trace fabricated private mode: %s", line)
		}
	}
}

func TestCompatibilityRoutesEnterStructuredDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	server := newAdminSecurityServer(t, "administrator-password")
	server.debug = store
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, target := range []string{"/hermes/v1/chat/completions", "/memory/v1/chat/completions"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`)))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("route %s status=%d", target, recorder.Code)
		}
		if got := safeServiceLogPath(target); got != target {
			t.Fatalf("safe service path %q=%q", target, got)
		}
	}
	records := store.list()
	if len(records) != 2 {
		t.Fatalf("compatibility route records=%d want=2", len(records))
	}
	protocols := map[string]bool{}
	for _, record := range records {
		protocols[record.Protocol] = true
		if record.Path != "/hermes/v1/chat/completions" && record.Path != "/memory/v1/chat/completions" {
			t.Fatalf("unexpected compatibility diagnostic path: %#v", record)
		}
	}
	if !protocols["hermes_chat_completions"] || !protocols["memory_chat_completions"] {
		t.Fatalf("compatibility protocols were not classified: %#v", protocols)
	}

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	trace := httpTrace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	trace.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/hermes/v1/chat/completions", nil))
	if !strings.Contains(logs.String(), "path=/hermes/v1/chat/completions") {
		t.Fatalf("Hermes route was omitted from HTTP trace: %s", logs.String())
	}
	logs.Reset()
	trace.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, hindsightWebhookPath, nil))
	if !strings.Contains(logs.String(), "path="+hindsightWebhookPath) {
		t.Fatalf("Hindsight durable webhook route was omitted from HTTP trace: %s", logs.String())
	}
}

func TestDebugAuditEventsAreRedactedAndExposed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug-summary.json")
	store := openDebugStoreWithPolicy(path, testDebugPolicy())
	if _, err := store.startSession(time.Minute); err != nil {
		t.Fatal(err)
	}
	server := &Server{debug: store}
	handler := server.debugMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"status": "ok", "body": "body-sentinel-20"})
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m365-auto","messages":[{"role":"user","content":"prompt-sentinel-20"}]}`)))
	records := store.list()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}

	detail := httptest.NewRecorder()
	server.debugDetail(detail, httptest.NewRequest(http.MethodGet, "/api/admin/debug/detail?id="+records[0].ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	export := httptest.NewRecorder()
	server.debugExport(export, httptest.NewRequest(http.MethodPost, "/api/admin/debug/export", strings.NewReader(`{}`)))
	if export.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", export.Code, export.Body.String())
	}
	clear := httptest.NewRecorder()
	server.debugSession(clear, httptest.NewRequest(http.MethodDelete, "/api/admin/debug/session", nil))
	if clear.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", clear.Code, clear.Body.String())
	}
	list := httptest.NewRecorder()
	server.debugList(list, httptest.NewRequest(http.MethodGet, "/api/admin/debug/logs", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	assertNoDebugSentinels(t, detail.Body.Bytes())
	assertNoDebugSentinels(t, export.Body.Bytes())
	assertNoDebugSentinels(t, list.Body.Bytes())

	var response struct {
		Audit []debugAuditEvent `json:"audit"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range response.Audit {
		actions[event.Action] = true
	}
	for _, required := range []string{"enabled", "viewed", "exported", "cleared"} {
		if !actions[required] {
			t.Fatalf("audit action %q missing: %+v", required, response.Audit)
		}
	}
}

func TestDebugUIUsesSummaryAndExpiringSnapshotFields(t *testing.T) {
	raw, err := os.ReadFile("../../web/debug.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"clientPayload", "upstreamPayload", "gatewayPayload", "客户端发送的信息", "上游返回的信息", "网关处理后的信息"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("capture-first UI field retained: %q", forbidden)
		}
	}
	for _, required := range []string{"protocol", "route", "inputTokens", "outputTokens", "snapshotExpiresAt", "/api/admin/debug/session", "audit"} {
		if !bytes.Contains(raw, []byte(required)) {
			t.Fatalf("summary UI field missing: %q", required)
		}
	}
}
