package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	debugStoreSchema = "m365-debug-summary/v1"

	defaultDebugSummaryTTL          = 24 * time.Hour
	defaultDebugMaxRecords          = 500
	defaultDebugMaxBytes            = 4 << 20
	defaultDebugAuditMaxRecords     = 200
	defaultDebugSnapshotTTL         = 15 * time.Minute
	defaultDebugSnapshotMaxRecords  = 20
	defaultDebugSnapshotMaxBytes    = 512 << 10
	defaultDebugPayloadCaptureBytes = 64 << 10
)

type debugStorePolicy struct {
	SummaryTTL          time.Duration
	MaxRecords          int
	MaxBytes            int
	AuditMaxRecords     int
	SnapshotTTL         time.Duration
	SnapshotMaxRecords  int
	SnapshotMaxBytes    int
	PayloadCaptureBytes int
}

type debugPayloadShape struct {
	Objects     int `json:"objects"`
	Arrays      int `json:"arrays"`
	Fields      int `json:"fields"`
	Strings     int `json:"strings"`
	StringBytes int `json:"stringBytes"`
	Numbers     int `json:"numbers"`
	Booleans    int `json:"booleans"`
	Nulls       int `json:"nulls"`
	MaxDepth    int `json:"maxDepth"`
}

type debugPayloadSnapshot struct {
	Bytes         int64             `json:"bytes"`
	CapturedBytes int               `json:"capturedBytes"`
	Truncated     bool              `json:"truncated"`
	Format        string            `json:"format"`
	Shape         debugPayloadShape `json:"shape"`
}

type debugRedactionManifest struct {
	ScalarValuesRemoved bool     `json:"scalarValuesRemoved"`
	HeadersOmitted      bool     `json:"headersOmitted"`
	RemovedClasses      []string `json:"removedClasses"`
}

type debugSnapshot struct {
	SessionID  string                 `json:"sessionId"`
	CapturedAt time.Time              `json:"capturedAt"`
	ExpiresAt  time.Time              `json:"expiresAt"`
	Request    debugPayloadSnapshot   `json:"request"`
	Response   debugPayloadSnapshot   `json:"response"`
	Redaction  debugRedactionManifest `json:"redaction"`
}

type debugRecord struct {
	ID                string         `json:"id"`
	RequestID         string         `json:"requestId,omitempty"`
	At                time.Time      `json:"at"`
	ExpiresAt         time.Time      `json:"expiresAt"`
	Protocol          string         `json:"protocol"`
	Route             string         `json:"route"`
	Path              string         `json:"path"`
	Method            string         `json:"method"`
	Status            int            `json:"status"`
	Level             string         `json:"level"`
	DurationMS        int64          `json:"durationMs"`
	Stream            bool           `json:"stream"`
	MessageCount      int            `json:"messageCount"`
	ToolCount         int            `json:"toolCount"`
	AttachmentCount   int            `json:"attachmentCount"`
	EventCount        int            `json:"eventCount"`
	InputTokens       int            `json:"inputTokens"`
	OutputTokens      int            `json:"outputTokens"`
	TokenSource       string         `json:"tokenSource"`
	ErrorCode         string         `json:"errorCode,omitempty"`
	SnapshotAvailable bool           `json:"snapshotAvailable"`
	SnapshotExpiresAt time.Time      `json:"snapshotExpiresAt,omitempty"`
	Snapshot          *debugSnapshot `json:"snapshot,omitempty"`
}

type debugCaptureSession struct {
	ID              string    `json:"id"`
	StartedAt       time.Time `json:"startedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	MaxRecords      int       `json:"maxRecords"`
	MaxBytes        int       `json:"maxBytes"`
	CapturedRecords int       `json:"capturedRecords"`
	CapturedBytes   int       `json:"capturedBytes"`
}

type debugAuditEvent struct {
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	SessionID string    `json:"sessionId,omitempty"`
	RecordID  string    `json:"recordId,omitempty"`
	Result    string    `json:"result"`
}

type debugStoreData struct {
	Schema  string               `json:"schema"`
	Records []debugRecord        `json:"records"`
	Session *debugCaptureSession `json:"session,omitempty"`
	Audit   []debugAuditEvent    `json:"audit"`
}

type debugSessionStatus struct {
	Active           bool      `json:"active"`
	ID               string    `json:"id,omitempty"`
	StartedAt        time.Time `json:"startedAt,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt,omitempty"`
	RemainingSeconds int64     `json:"remainingSeconds,omitempty"`
	MaxRecords       int       `json:"maxRecords,omitempty"`
	MaxBytes         int       `json:"maxBytes,omitempty"`
	CapturedRecords  int       `json:"capturedRecords,omitempty"`
	CapturedBytes    int       `json:"capturedBytes,omitempty"`
	Warning          string    `json:"warning"`
}

type debugStore struct {
	mu          sync.Mutex
	path        string
	data        debugStoreData
	policy      debugStorePolicy
	now         func() time.Time
	random      io.Reader
	autoExpire  bool
	expiryTimer *time.Timer
}

type debugCapturedPayloads struct {
	Request       []byte
	RequestBytes  int64
	RequestCut    bool
	Response      []byte
	ResponseBytes int64
	ResponseCut   bool
}

func defaultDebugStorePolicy() debugStorePolicy {
	return debugStorePolicy{
		SummaryTTL:          defaultDebugSummaryTTL,
		MaxRecords:          defaultDebugMaxRecords,
		MaxBytes:            defaultDebugMaxBytes,
		AuditMaxRecords:     defaultDebugAuditMaxRecords,
		SnapshotTTL:         defaultDebugSnapshotTTL,
		SnapshotMaxRecords:  defaultDebugSnapshotMaxRecords,
		SnapshotMaxBytes:    defaultDebugSnapshotMaxBytes,
		PayloadCaptureBytes: defaultDebugPayloadCaptureBytes,
	}
}

func normalizeDebugStorePolicy(policy debugStorePolicy) debugStorePolicy {
	defaults := defaultDebugStorePolicy()
	if policy.SummaryTTL <= 0 {
		policy.SummaryTTL = defaults.SummaryTTL
	}
	if policy.MaxRecords <= 0 {
		policy.MaxRecords = defaults.MaxRecords
	}
	if policy.MaxBytes <= 0 {
		policy.MaxBytes = defaults.MaxBytes
	}
	if policy.AuditMaxRecords <= 0 {
		policy.AuditMaxRecords = defaults.AuditMaxRecords
	}
	if policy.SnapshotTTL <= 0 {
		policy.SnapshotTTL = defaults.SnapshotTTL
	}
	if policy.SnapshotMaxRecords <= 0 {
		policy.SnapshotMaxRecords = defaults.SnapshotMaxRecords
	}
	if policy.SnapshotMaxBytes <= 0 {
		policy.SnapshotMaxBytes = defaults.SnapshotMaxBytes
	}
	if policy.PayloadCaptureBytes <= 0 {
		policy.PayloadCaptureBytes = defaults.PayloadCaptureBytes
	}
	return policy
}

func debugStorePath() string {
	path := strings.TrimSpace(os.Getenv("M365_DEBUG_LOG"))
	if path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(settingsPath()), "debug-logs.json")
}

func openDebugStore() *debugStore {
	store := openDebugStoreWithPolicy(debugStorePath(), defaultDebugStorePolicy())
	store.startAutoExpiry()
	return store
}

func openDebugStoreWithPolicy(path string, policy debugStorePolicy) *debugStore {
	store := &debugStore{
		path:   path,
		policy: normalizeDebugStorePolicy(policy),
		now:    func() time.Time { return time.Now().UTC() },
		random: rand.Reader,
		data: debugStoreData{
			Schema:  debugStoreSchema,
			Records: []debugRecord{},
			Audit:   []debugAuditEvent{},
		},
	}
	data, exists, valid, err := readDebugStoreData(path)
	if err != nil {
		log.Printf("[debug-store] code=read_failed")
		return store
	}
	if !exists {
		return store
	}
	if !valid {
		store.path = legacySafeDebugSummaryPath(path)
		safeData, safeExists, safeValid, safeErr := readDebugStoreData(store.path)
		switch {
		case safeErr != nil:
			store.path = legacySafeDebugSummaryPath(store.path)
			log.Printf("[debug-store] code=safe_summary_read_failed")
		case safeExists && safeValid:
			store.data = safeData
		case safeExists:
			store.path = legacySafeDebugSummaryPath(store.path)
			log.Printf("[debug-store] code=invalid_safe_summary_preserved")
		}
		log.Printf("[debug-store] code=legacy_or_invalid_store_preserved")
	} else {
		store.data = data
	}
	store.mu.Lock()
	changed := store.pruneLocked()
	if changed {
		_ = store.persistLocked()
	}
	store.mu.Unlock()
	return store
}

func readDebugStoreData(path string) (debugStoreData, bool, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return debugStoreData{}, false, false, nil
	}
	if err != nil {
		return debugStoreData{}, true, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var data debugStoreData
	if err := decoder.Decode(&data); err != nil || data.Schema != debugStoreSchema {
		return debugStoreData{}, true, false, nil
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return debugStoreData{}, true, false, nil
	}
	return data, true, true, nil
}

func legacySafeDebugSummaryPath(path string) string {
	if strings.HasSuffix(path, ".summary.json") {
		return path + ".safe"
	}
	return path + ".summary.json"
}

func (d *debugStore) startAutoExpiry() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	d.autoExpire = true
	d.scheduleExpiryLocked()
}

func (d *debugStore) scheduleExpiryLocked() {
	if !d.autoExpire {
		return
	}
	if d.expiryTimer != nil {
		d.expiryTimer.Stop()
		d.expiryTimer = nil
	}
	var earliest time.Time
	consider := func(candidate time.Time) {
		if candidate.IsZero() || (!earliest.IsZero() && !candidate.Before(earliest)) {
			return
		}
		earliest = candidate
	}
	for _, record := range d.data.Records {
		consider(record.ExpiresAt)
		if record.Snapshot != nil {
			consider(record.Snapshot.ExpiresAt)
		}
	}
	if d.data.Session != nil {
		consider(d.data.Session.ExpiresAt)
	}
	for _, event := range d.data.Audit {
		consider(event.At.Add(d.policy.SummaryTTL))
	}
	if earliest.IsZero() {
		return
	}
	delay := earliest.Sub(d.now().UTC())
	if delay < 0 {
		delay = 0
	}
	d.expiryTimer = time.AfterFunc(delay, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.pruneLocked() {
			if err := d.persistLocked(); err != nil {
				log.Printf("[debug-store] code=expiry_persist_failed")
			}
		}
		d.scheduleExpiryLocked()
	})
}

func (d *debugStore) ensureDefaultsLocked() {
	if d.policy.SummaryTTL <= 0 {
		d.policy = normalizeDebugStorePolicy(d.policy)
	}
	if d.now == nil {
		d.now = func() time.Time { return time.Now().UTC() }
	}
	if d.random == nil {
		d.random = rand.Reader
	}
	if d.data.Schema == "" {
		d.data.Schema = debugStoreSchema
	}
	if d.data.Records == nil {
		d.data.Records = []debugRecord{}
	}
	if d.data.Audit == nil {
		d.data.Audit = []debugAuditEvent{}
	}
}

func newDebugID(prefix string, random io.Reader) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw), nil
}

func debugLevel(status int) string {
	if status >= 500 {
		return "error"
	}
	if status >= 400 {
		return "warn"
	}
	return "info"
}

func debugLevelRank(level string) int {
	switch level {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	case "silent":
		return 4
	default:
		return 1
	}
}

func estimateDebugTokens(bytes int64) int {
	if bytes <= 0 {
		return 0
	}
	return int((bytes + 3) / 4)
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func (d *debugStore) startSession(requestedTTL time.Duration) (debugSessionStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	d.pruneLocked()
	if requestedTTL <= 0 || requestedTTL > d.policy.SnapshotTTL {
		requestedTTL = d.policy.SnapshotTTL
	}
	now := d.now().UTC()
	id, err := newDebugID("diag_", d.random)
	if err != nil {
		return debugSessionStatus{}, err
	}
	for i := range d.data.Records {
		d.data.Records[i].Snapshot = nil
		d.data.Records[i].SnapshotAvailable = false
		d.data.Records[i].SnapshotExpiresAt = time.Time{}
	}
	d.data.Session = &debugCaptureSession{
		ID:         id,
		StartedAt:  now,
		ExpiresAt:  now.Add(requestedTTL),
		MaxRecords: d.policy.SnapshotMaxRecords,
		MaxBytes:   d.policy.SnapshotMaxBytes,
	}
	d.auditLocked("enabled", id, "", "ok")
	if err := d.persistLocked(); err != nil {
		return debugSessionStatus{}, err
	}
	d.scheduleExpiryLocked()
	return d.sessionStatusLocked(now), nil
}

func (d *debugStore) clearSession() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	d.pruneLocked()
	sessionID := ""
	if d.data.Session != nil {
		sessionID = d.data.Session.ID
	}
	for i := range d.data.Records {
		d.data.Records[i].Snapshot = nil
		d.data.Records[i].SnapshotAvailable = false
		d.data.Records[i].SnapshotExpiresAt = time.Time{}
	}
	d.data.Session = nil
	d.auditLocked("cleared", sessionID, "", "ok")
	if err := d.persistLocked(); err != nil {
		return err
	}
	d.scheduleExpiryLocked()
	return nil
}

func (d *debugStore) sessionStatus() debugSessionStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	changed := d.pruneLocked()
	if changed {
		_ = d.persistLocked()
	}
	return d.sessionStatusLocked(d.now().UTC())
}

func (d *debugStore) sessionStatusLocked(now time.Time) debugSessionStatus {
	status := debugSessionStatus{
		Warning: "診斷快照會移除所有純量內容與請求標頭，並在到期後自動清除。",
	}
	if d.data.Session == nil || !now.Before(d.data.Session.ExpiresAt) {
		return status
	}
	session := d.data.Session
	status.Active = true
	status.ID = session.ID
	status.StartedAt = session.StartedAt
	status.ExpiresAt = session.ExpiresAt
	status.RemainingSeconds = int64(time.Until(session.ExpiresAt).Seconds())
	if d.now != nil {
		status.RemainingSeconds = int64(session.ExpiresAt.Sub(now).Seconds())
	}
	if status.RemainingSeconds < 0 {
		status.RemainingSeconds = 0
	}
	status.MaxRecords = session.MaxRecords
	status.MaxBytes = session.MaxBytes
	status.CapturedRecords = session.CapturedRecords
	status.CapturedBytes = session.CapturedBytes
	return status
}

func (d *debugStore) auditLocked(action, sessionID, recordID, result string) {
	d.data.Audit = append(d.data.Audit, debugAuditEvent{
		At:        d.now().UTC(),
		Action:    action,
		SessionID: sessionID,
		RecordID:  recordID,
		Result:    result,
	})
	if len(d.data.Audit) > d.policy.AuditMaxRecords {
		d.data.Audit = append([]debugAuditEvent(nil), d.data.Audit[len(d.data.Audit)-d.policy.AuditMaxRecords:]...)
	}
}

func (d *debugStore) recomputeSessionUsageLocked() {
	if d.data.Session == nil {
		return
	}
	records := 0
	bytes := 0
	for _, record := range d.data.Records {
		if record.Snapshot == nil || record.Snapshot.SessionID != d.data.Session.ID {
			continue
		}
		records++
		raw, _ := json.Marshal(record.Snapshot)
		bytes += len(raw)
	}
	d.data.Session.CapturedRecords = records
	d.data.Session.CapturedBytes = bytes
}

func (d *debugStore) pruneLocked() bool {
	d.ensureDefaultsLocked()
	now := d.now().UTC()
	changed := false
	kept := d.data.Records[:0]
	for _, record := range d.data.Records {
		if record.ExpiresAt.IsZero() {
			record.ExpiresAt = record.At.Add(d.policy.SummaryTTL)
			changed = true
		}
		if !now.Before(record.ExpiresAt) {
			changed = true
			continue
		}
		if record.Snapshot != nil && !now.Before(record.Snapshot.ExpiresAt) {
			record.Snapshot = nil
			record.SnapshotAvailable = false
			record.SnapshotExpiresAt = time.Time{}
			changed = true
		}
		kept = append(kept, record)
	}
	d.data.Records = kept
	if d.data.Session != nil && !now.Before(d.data.Session.ExpiresAt) {
		for i := range d.data.Records {
			if d.data.Records[i].Snapshot != nil {
				d.data.Records[i].Snapshot = nil
				d.data.Records[i].SnapshotAvailable = false
				d.data.Records[i].SnapshotExpiresAt = time.Time{}
				changed = true
			}
		}
		d.auditLocked("expired", d.data.Session.ID, "", "ok")
		d.data.Session = nil
		changed = true
	}
	if len(d.data.Records) > d.policy.MaxRecords {
		d.data.Records = append([]debugRecord(nil), d.data.Records[len(d.data.Records)-d.policy.MaxRecords:]...)
		changed = true
	}
	audit := d.data.Audit[:0]
	for _, event := range d.data.Audit {
		if !now.Before(event.At.Add(d.policy.SummaryTTL)) {
			changed = true
			continue
		}
		audit = append(audit, event)
	}
	d.data.Audit = audit
	if len(d.data.Audit) > d.policy.AuditMaxRecords {
		d.data.Audit = append([]debugAuditEvent(nil), d.data.Audit[len(d.data.Audit)-d.policy.AuditMaxRecords:]...)
		changed = true
	}
	d.recomputeSessionUsageLocked()
	return changed
}

func cloneDebugRecord(record debugRecord) debugRecord {
	clone := record
	if record.Snapshot != nil {
		snapshot := *record.Snapshot
		snapshot.Redaction.RemovedClasses = append([]string(nil), record.Snapshot.Redaction.RemovedClasses...)
		clone.Snapshot = &snapshot
	}
	return clone
}

func (d *debugStore) list() []debugRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	changed := d.pruneLocked()
	if changed {
		_ = d.persistLocked()
	}
	out := make([]debugRecord, len(d.data.Records))
	for i, record := range d.data.Records {
		out[len(d.data.Records)-1-i] = cloneDebugRecord(record)
	}
	return out
}

func (d *debugStore) get(id string) (debugRecord, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	changed := d.pruneLocked()
	for _, record := range d.data.Records {
		if record.ID != id {
			continue
		}
		sessionID := ""
		if record.Snapshot != nil {
			sessionID = record.Snapshot.SessionID
		}
		d.auditLocked("viewed", sessionID, record.ID, "ok")
		changed = true
		if changed {
			_ = d.persistLocked()
		}
		return cloneDebugRecord(record), true
	}
	if changed {
		_ = d.persistLocked()
	}
	return debugRecord{}, false
}

func (d *debugStore) export() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	d.pruneLocked()
	sessionID := ""
	if d.data.Session != nil {
		sessionID = d.data.Session.ID
	}
	d.auditLocked("exported", sessionID, "", "ok")
	_ = d.persistLocked()
	d.scheduleExpiryLocked()
	records := make([]debugRecord, len(d.data.Records))
	for i, record := range d.data.Records {
		records[i] = cloneDebugRecord(record)
	}
	return map[string]any{
		"schema":      "m365-debug-redacted-export/v1",
		"generatedAt": d.now().UTC(),
		"redaction":   debugSnapshotRedactionManifest(),
		"records":     records,
		"audit":       append([]debugAuditEvent(nil), d.data.Audit...),
	}
}

func (d *debugStore) add(record debugRecord, captured debugCapturedPayloads) {
	configured := currentSettings().LogLevel
	if configured == "silent" || debugLevelRank(record.Level) < debugLevelRank(configured) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	d.pruneLocked()
	now := d.now().UTC()
	if record.ID == "" {
		id, err := newDebugID("dbg_", d.random)
		if err != nil {
			log.Printf("[debug-store] code=id_generation_failed")
			return
		}
		record.ID = id
	}
	if record.At.IsZero() {
		record.At = now
	}
	record.At = record.At.UTC()
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = record.At.Add(d.policy.SummaryTTL)
	}
	if d.data.Session != nil && now.Before(d.data.Session.ExpiresAt) {
		snapshot := debugSnapshot{
			SessionID:  d.data.Session.ID,
			CapturedAt: now,
			ExpiresAt:  minTime(d.data.Session.ExpiresAt, now.Add(d.policy.SnapshotTTL)),
			Request:    summarizeDebugPayload(captured.Request, captured.RequestBytes, captured.RequestCut),
			Response:   summarizeDebugPayload(captured.Response, captured.ResponseBytes, captured.ResponseCut),
			Redaction:  debugSnapshotRedactionManifest(),
		}
		raw, _ := json.Marshal(snapshot)
		if d.data.Session.CapturedRecords < d.data.Session.MaxRecords && d.data.Session.CapturedBytes+len(raw) <= d.data.Session.MaxBytes {
			record.Snapshot = &snapshot
			record.SnapshotAvailable = true
			record.SnapshotExpiresAt = snapshot.ExpiresAt
			d.data.Session.CapturedRecords++
			d.data.Session.CapturedBytes += len(raw)
		}
	}
	d.data.Records = append(d.data.Records, record)
	if len(d.data.Records) > d.policy.MaxRecords {
		d.data.Records = append([]debugRecord(nil), d.data.Records[len(d.data.Records)-d.policy.MaxRecords:]...)
		d.recomputeSessionUsageLocked()
	}
	if err := d.persistLocked(); err != nil {
		log.Printf("[debug-store] code=persist_failed")
	}
	d.scheduleExpiryLocked()
}

func (d *debugStore) persistLocked() error {
	d.ensureDefaultsLocked()
	d.pruneLocked()
	for {
		raw, err := json.MarshalIndent(d.data, "", "  ")
		if err != nil {
			return err
		}
		if len(raw) <= d.policy.MaxBytes {
			return atomicWriteDebugStore(d.path, raw)
		}
		if len(d.data.Records) > 0 {
			d.data.Records = append([]debugRecord(nil), d.data.Records[1:]...)
			d.recomputeSessionUsageLocked()
			continue
		}
		if len(d.data.Audit) > 0 {
			d.data.Audit = append([]debugAuditEvent(nil), d.data.Audit[1:]...)
			continue
		}
		return errors.New("debug summary store exceeds configured maximum size")
	}
}

func atomicWriteDebugStore(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".debug-summary-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func debugSnapshotRedactionManifest() debugRedactionManifest {
	return debugRedactionManifest{
		ScalarValuesRemoved: true,
		HeadersOmitted:      true,
		RemovedClasses: []string{
			"authorization",
			"cookie",
			"api_key",
			"oauth_code_token_verifier",
			"email",
			"oid",
			"tid",
			"prompt_and_body_text",
			"tool_names_and_arguments",
			"attachment_names_urls_and_content",
			"unknown_field_names",
			"all_scalar_values",
		},
	}
}

func summarizeDebugPayload(captured []byte, total int64, truncated bool) debugPayloadSnapshot {
	snapshot := debugPayloadSnapshot{
		Bytes:         total,
		CapturedBytes: len(captured),
		Truncated:     truncated,
	}
	if len(captured) == 0 {
		snapshot.Format = "empty"
		return snapshot
	}
	var value any
	if !truncated && json.Unmarshal(captured, &value) == nil {
		snapshot.Format = "json"
		walkDebugPayloadShape(value, 1, &snapshot.Shape)
		return snapshot
	}
	if truncated {
		snapshot.Format = "truncated"
	} else if utf8.Valid(captured) {
		snapshot.Format = "text"
	} else {
		snapshot.Format = "binary"
	}
	snapshot.Shape.Strings = 1
	snapshot.Shape.StringBytes = len(captured)
	snapshot.Shape.MaxDepth = 1
	return snapshot
}

func walkDebugPayloadShape(value any, depth int, shape *debugPayloadShape) {
	if depth > shape.MaxDepth {
		shape.MaxDepth = depth
	}
	if depth > 64 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		shape.Objects++
		shape.Fields += len(typed)
		for _, child := range typed {
			walkDebugPayloadShape(child, depth+1, shape)
		}
	case []any:
		shape.Arrays++
		for _, child := range typed {
			walkDebugPayloadShape(child, depth+1, shape)
		}
	case string:
		shape.Strings++
		shape.StringBytes += len(typed)
	case float64:
		shape.Numbers++
	case bool:
		shape.Booleans++
	case nil:
		shape.Nulls++
	default:
		shape.Strings++
	}
}

func debugProtocolAndPath(path string) (string, string) {
	if _, artifact := artifactCapabilityToken(path); artifact {
		return "artifact_download", "/v1/artifacts/{redacted}/content"
	}
	switch path {
	case "/v1/models":
		return "openai_models", path
	case "/v1/chat/completions":
		return "openai_chat_completions", path
	case "/hermes/v1/models":
		return "hermes_models", path
	case "/hermes/v1/chat/completions":
		return "hermes_chat_completions", path
	case "/memory/v1/models":
		return "memory_models", path
	case "/memory/v1/chat/completions":
		return "memory_chat_completions", path
	case "/v1/responses":
		return "openai_responses", path
	case "/v1/messages":
		return "anthropic_messages", path
	case "/v1/images/generations":
		return "openai_images", path
	case "/v1/mcp", "/v1/mcp/sse", "/v1/mcp/message":
		return "mcp", path
	default:
		return "unknown", "/v1/other"
	}
}

type debugRequestSummary struct {
	Route           string
	Stream          bool
	MessageCount    int
	ToolCount       int
	AttachmentCount int
}

func summarizeDebugRequest(server *Server, raw []byte) debugRequestSummary {
	summary := debugRequestSummary{Route: "unresolved"}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return summary
	}
	if modelRaw, ok := object["model"]; ok {
		var model string
		if json.Unmarshal(modelRaw, &model) == nil {
			if route, exists := registeredRoute(model, serverRuntimeSettings(server).ModelMappings); exists {
				if route.ConfiguredMapping {
					summary.Route = "configured_mapping"
				} else {
					summary.Route = route.ID
				}
			}
		}
	}
	if streamRaw, ok := object["stream"]; ok {
		_ = json.Unmarshal(streamRaw, &summary.Stream)
	}
	summary.MessageCount = debugArrayLength(object["messages"])
	if summary.MessageCount == 0 {
		summary.MessageCount = debugArrayLength(object["input"])
	}
	summary.ToolCount = debugArrayLength(object["tools"])
	summary.AttachmentCount = debugArrayLength(object["attachments"])
	return summary
}

func debugArrayLength(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return 0
	}
	return len(values)
}

func safeToolChoiceLog(mode string) string {
	switch mode {
	case "auto", "none", "required":
		return mode
	default:
		if strings.HasPrefix(mode, "named:") {
			return "named"
		}
		return "unknown"
	}
}

func debugErrorCode(status int) string {
	switch {
	case status >= 500:
		return "upstream_error"
	case status >= 400:
		return "request_error"
	default:
		return ""
	}
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitedBuffer) Write(p []byte) (int, error) {
	if buffer.limit <= 0 {
		return len(p), nil
	}
	if buffer.Len() >= buffer.limit {
		buffer.truncated = true
		return len(p), nil
	}
	remaining := buffer.limit - buffer.Len()
	if len(p) > remaining {
		_, _ = buffer.Buffer.Write(p[:remaining])
		buffer.truncated = true
		return len(p), nil
	}
	return buffer.Buffer.Write(p)
}

type captureWriter struct {
	http.ResponseWriter
	status     int
	bytes      int64
	eventCount int
	body       limitedBuffer
}

type captureReadCloser struct {
	io.ReadCloser
	bytes int64
	body  limitedBuffer
}

func (reader *captureReadCloser) Read(p []byte) (int, error) {
	n, err := reader.ReadCloser.Read(p)
	reader.bytes += int64(n)
	_, _ = reader.body.Write(p[:n])
	return n, err
}

func (writer *captureWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *captureWriter) Flush() {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *captureWriter) Header() http.Header { return writer.ResponseWriter.Header() }

func (writer *captureWriter) Write(raw []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	writer.bytes += int64(len(raw))
	writer.eventCount += bytes.Count(raw, []byte("data:"))
	_, _ = writer.body.Write(raw)
	return writer.ResponseWriter.Write(raw)
}

func (server *Server) debugMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, artifact := artifactCapabilityToken(r.URL.Path); artifact {
			// Capability tokens and private artifact bytes never enter debug
			// snapshots. The outer access trace records only a redacted path.
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") && !hermesCompatibilityRequest(r.URL.Path) && !memoryCompatibilityRequest(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		bodyLimit, err := requestBodyLimit(serverRuntimeSettings(server).TextInputLimitUTF16)
		if err != nil {
			http.Error(w, "Sidecar request policy is unavailable", http.StatusInternalServerError)
			return
		}
		if r.ContentLength > bodyLimit {
			writeRequestBodyTooLarge(w, r.URL.Path, bodyLimit)
			return
		}
		captureLimit := 0
		activeCapture := server.debug != nil && server.debug.sessionStatus().Active
		if activeCapture {
			captureLimit = server.debug.policy.PayloadCaptureBytes
		}
		summaryLimit := defaultDebugPayloadCaptureBytes
		if server.debug != nil && server.debug.policy.PayloadCaptureBytes > 0 {
			summaryLimit = server.debug.policy.PayloadCaptureBytes
		}
		input := &captureReadCloser{
			ReadCloser: http.MaxBytesReader(w, r.Body, bodyLimit),
			body:       limitedBuffer{limit: summaryLimit},
		}
		r.Body = input
		writer := &captureWriter{
			ResponseWriter: w,
			body: limitedBuffer{
				limit: captureLimit,
			},
		}
		start := time.Now()
		if server.debug != nil && server.debug.now != nil {
			start = server.debug.now().UTC()
		}
		next.ServeHTTP(writer, r)
		if writer.status == 0 {
			writer.status = http.StatusOK
		}
		protocol, safePath := debugProtocolAndPath(r.URL.Path)
		requestSummary := summarizeDebugRequest(server, input.body.Bytes())
		var requestCapture []byte
		if activeCapture {
			requestCapture = append([]byte(nil), input.body.Bytes()...)
		}
		requestTruncated := activeCapture && input.body.truncated
		duration := time.Since(start).Milliseconds()
		if server.debug != nil && server.debug.now != nil {
			duration = server.debug.now().UTC().Sub(start).Milliseconds()
			if duration < 0 {
				duration = 0
			}
		}
		eventCount := writer.eventCount
		if eventCount == 0 && writer.bytes > 0 {
			eventCount = 1
		}
		record := debugRecord{
			RequestID:       requestIDFrom(r),
			At:              start,
			Protocol:        protocol,
			Route:           requestSummary.Route,
			Path:            safePath,
			Method:          safeServiceLogMethod(r.Method),
			Status:          writer.status,
			Level:           debugLevel(writer.status),
			DurationMS:      duration,
			Stream:          requestSummary.Stream,
			MessageCount:    requestSummary.MessageCount,
			ToolCount:       requestSummary.ToolCount,
			AttachmentCount: requestSummary.AttachmentCount,
			EventCount:      eventCount,
			InputTokens:     estimateDebugTokens(input.bytes),
			OutputTokens:    estimateDebugTokens(writer.bytes),
			TokenSource:     "byte_estimate",
			ErrorCode:       debugErrorCode(writer.status),
		}
		server.debug.add(record, debugCapturedPayloads{
			Request:       requestCapture,
			RequestBytes:  input.bytes,
			RequestCut:    requestTruncated,
			Response:      append([]byte(nil), writer.body.Bytes()...),
			ResponseBytes: writer.bytes,
			ResponseCut:   writer.body.truncated,
		})
	})
}

func (d *debugStore) auditEvents() []debugAuditEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ensureDefaultsLocked()
	changed := d.pruneLocked()
	if changed {
		_ = d.persistLocked()
	}
	out := append([]debugAuditEvent(nil), d.data.Audit...)
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func (server *Server) debugList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	records := server.debug.list()
	for i := range records {
		records[i].Snapshot = nil
	}
	jsonOut(w, map[string]any{
		"records": records,
		"session": server.debug.sessionStatus(),
		"audit":   server.debug.auditEvents(),
	})
}

func (server *Server) debugDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	if record, ok := server.debug.get(r.URL.Query().Get("id")); ok {
		jsonOut(w, record)
		return
	}
	writeOpenAIError(w, http.StatusNotFound, "not_found", "找不到診斷記錄")
}

func (server *Server) debugSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOut(w, server.debug.sessionStatus())
	case http.MethodPost:
		var body struct {
			TTLSeconds int `json:"ttlSeconds"`
		}
		if r.Body != nil {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "JSON 格式錯誤")
				return
			}
		}
		if body.TTLSeconds < 0 {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "ttlSeconds 必須為正數")
			return
		}
		status, err := server.debug.startSession(time.Duration(body.TTLSeconds) * time.Second)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "無法啟動診斷工作階段")
			return
		}
		jsonOut(w, status)
	case http.MethodDelete:
		if err := server.debug.clearSession(); err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "無法清除診斷工作階段")
			return
		}
		jsonOut(w, server.debug.sessionStatus())
	default:
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
	}
}

func (server *Server) debugExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="m365-debug-redacted.json"`)
	jsonOut(w, server.debug.export())
}

func logChathubTrace(meta map[string]any) {
	stage, _ := meta["stage"].(string)
	switch stage {
	case "chathub_payload", "upload_start", "upload_success":
	default:
		stage = "unknown"
	}
	attachmentCount := integerMeta(meta["attachment_count"])
	payloadHasAttachments, _ := meta["payload_has_attachments"].(bool)
	base64Length := integerMeta(meta["base64_length"])
	tokenPresent, _ := meta["token_present"].(bool)
	if privateMode, ok := meta["private_mode"].(bool); ok {
		log.Printf("[chathub-trace] stage=%s attachment_count=%d payload_has_attachments=%t base64_length=%d token_present=%t private_mode=%t", stage, attachmentCount, payloadHasAttachments, base64Length, tokenPresent, privateMode)
		return
	}
	log.Printf("[chathub-trace] stage=%s attachment_count=%d payload_has_attachments=%t base64_length=%d token_present=%t", stage, attachmentCount, payloadHasAttachments, base64Length, tokenPresent)
}

func integerMeta(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}
