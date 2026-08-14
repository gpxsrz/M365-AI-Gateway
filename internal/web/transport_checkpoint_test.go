package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransportCheckpointExactPrefixSendsOnlyDeltaAndPolicy(t *testing.T) {
	store, err := openTransportCheckpointStore(t.TempDir() + "/checkpoints.json")
	if err != nil {
		t.Fatal(err)
	}

	first := []oaiMsg{
		{Role: "system", Content: "policy"},
		{Role: "user", Content: "first"},
	}
	turn, err := store.BeginFull("chat", "owner-a", "conversation-a", first, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.Accept(checkpointBinding{ConversationID: "conv-1", SessionID: "session-1"}, []oaiMsg{{Role: "assistant", Content: "answer"}}, ""); err != nil {
		t.Fatal(err)
	}

	active := append(append([]oaiMsg{}, first...),
		oaiMsg{Role: "assistant", Content: "answer"},
		oaiMsg{Role: "user", Content: "second"},
	)
	turn, err = store.BeginFull("chat", "owner-a", "conversation-a", active, false)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Rebound {
		t.Fatal("exact prefix unexpectedly rebound")
	}
	if turn.Binding.ConversationID != "conv-1" || turn.Binding.SessionID != "session-1" {
		t.Fatalf("binding = %#v", turn.Binding)
	}
	if len(turn.Outbound) != 2 || turn.Outbound[0].Role != "system" || turn.Outbound[1].Content != "second" {
		t.Fatalf("outbound = %#v", turn.Outbound)
	}
	if err := turn.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportCheckpointClearIsRestartSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "private text"}})
	acceptForTest(t, turn, "conversation", "session", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if got := checkpointViewsForTest(t, store); len(got) != 0 {
		t.Fatalf("checkpoint list after clear = %#v", got)
	}
	reopened, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkpointViewsForTest(t, reopened); len(got) != 0 {
		t.Fatalf("checkpoint list after restart = %#v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private text") || strings.Contains(string(raw), "conversation") {
		t.Fatalf("cleared checkpoint retained private state: %s", raw)
	}
}

func TestTransportCheckpointExistingTurnPersistsOnlyTouchedRecordNearCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := []oaiMsg{{Role: "user", Content: "one"}}
	initial := beginFullForTest(t, store, "chat", "owner-target", "target-key", first)
	targetID := initial.RecordID()
	acceptForTest(t, initial, "conversation-target", "session-target", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")

	store.mu.Lock()
	now := store.now()
	var fillerID string
	for i := 0; i < transportCheckpointMaxRecords-2; i++ {
		id := fmt.Sprintf("filler-%03d", i)
		if fillerID == "" {
			fillerID = id
		}
		store.records[id] = &transportCheckpointRecord{
			ID:               id,
			Namespace:        "chat",
			OwnerDigest:      checkpointDigest(transportCheckpointOwnerDomain, fmt.Sprintf("owner-%03d", i)),
			ConversationID:   "conversation-" + id,
			CurrentSessionID: "session-" + id,
			MessageDigests:   []string{},
			HashChain:        []string{},
			CreatedAt:        now,
			UpdatedAt:        now,
			Revision:         2,
		}
	}
	if err := store.persistLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	generation := store.generation
	store.mu.Unlock()

	manifestRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifestBefore := string(manifestRaw)
	fillerPath := checkpointRecordPath(path, generation, fillerID)
	fillerBefore, err := os.ReadFile(fillerPath)
	if err != nil {
		t.Fatal(err)
	}
	targetPath := checkpointRecordPath(path, generation, targetID)
	targetBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	active := []oaiMsg{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "two"},
	}
	turn, err := store.BeginFull("chat", "owner-target", "target-key", active, false)
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(manifestRaw); got != manifestBefore {
		t.Fatal("existing-turn update rewrote the global checkpoint manifest")
	}
	fillerAfter, err := os.ReadFile(fillerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(fillerAfter) != string(fillerBefore) {
		t.Fatal("existing-turn update rewrote an unrelated checkpoint record")
	}
	targetAfter, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetAfter) == string(targetBefore) {
		t.Fatal("existing-turn update did not durably update the touched record")
	}
	_ = turn.Abort()
}

func TestTransportCheckpointCapacityReplacesOnlyEvictedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := checkpointDigest(transportCheckpointOwnerDomain, "owner")
	now := store.now()
	stableID := fmt.Sprintf("record-%03d", transportCheckpointMaxRecords-1)

	store.mu.Lock()
	for i := 0; i < transportCheckpointMaxRecords; i++ {
		id := fmt.Sprintf("record-%03d", i)
		store.records[id] = &transportCheckpointRecord{
			ID:             id,
			Namespace:      "hermes",
			OwnerDigest:    owner,
			KeyDigest:      checkpointDigest(transportCheckpointKeyDomain, id),
			ConversationID: "conversation-" + id,
			MessageDigests: []string{},
			HashChain:      []string{},
			CreatedAt:      now.Add(time.Duration(i) * time.Second),
			UpdatedAt:      now.Add(time.Duration(i) * time.Second),
			Revision:       2,
		}
	}
	if err := store.persistLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	oldGeneration := store.generation
	stableBefore, err := os.Stat(checkpointRecordPath(path, oldGeneration, stableID))
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	snapshotBefore := store.persistenceSnapshot()

	turn, err := store.BeginFull("hermes", "owner", "new-key", []oaiMsg{{Role: "user", Content: "new"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	newGeneration := store.generation
	store.mu.Unlock()
	if newGeneration != oldGeneration {
		t.Fatalf("capacity eviction rotated checkpoint generation from %q to %q", oldGeneration, newGeneration)
	}
	stableAfter, err := os.Stat(checkpointRecordPath(path, oldGeneration, stableID))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(stableBefore, stableAfter) {
		t.Fatal("capacity eviction rewrote an unchanged checkpoint record")
	}
	snapshot := store.persistenceSnapshot()
	if snapshot.RecordCount != transportCheckpointMaxRecords || snapshot.GenerationSwitchCount != snapshotBefore.GenerationSwitchCount {
		t.Fatalf("unexpected checkpoint persistence snapshot after capacity eviction: %#v", snapshot)
	}
	acceptForTest(t, turn, "conversation-new", "session-new", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")

	reopened, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	views := checkpointViewsForTest(t, reopened)
	if len(views) != transportCheckpointMaxRecords {
		t.Fatalf("checkpoint count after restart=%d want=%d", len(views), transportCheckpointMaxRecords)
	}
	foundStable := false
	for _, view := range views {
		if view.ID == stableID {
			foundStable = true
			break
		}
	}
	if !foundStable {
		t.Fatal("unchanged checkpoint record was lost during capacity eviction")
	}
}

func TestTransportCheckpointCapacityEvictsRequestingOwnerBeforeOtherOwners(t *testing.T) {
	store := openCheckpointForTest(t)
	now := store.now()
	ownerA := checkpointDigest(transportCheckpointOwnerDomain, "owner-a")
	ownerB := checkpointDigest(transportCheckpointOwnerDomain, "owner-b")

	store.mu.Lock()
	for i := 0; i < transportCheckpointMaxRecords-1; i++ {
		id := fmt.Sprintf("owner-a-%03d", i)
		store.records[id] = &transportCheckpointRecord{
			ID:             id,
			Namespace:      "responses",
			OwnerDigest:    ownerA,
			KeyDigest:      checkpointDigest(transportCheckpointKeyDomain, fmt.Sprintf("a-key-%03d", i)),
			ConversationID: "conversation-" + id,
			MessageDigests: []string{},
			HashChain:      []string{},
			CreatedAt:      now.Add(-time.Minute),
			UpdatedAt:      now.Add(-time.Minute),
			Revision:       2,
		}
	}
	const ownerBRecordID = "owner-b-protected"
	store.records[ownerBRecordID] = &transportCheckpointRecord{
		ID:             ownerBRecordID,
		Namespace:      "responses",
		OwnerDigest:    ownerB,
		KeyDigest:      checkpointDigest(transportCheckpointKeyDomain, "b-key"),
		ConversationID: "conversation-b",
		MessageDigests: []string{},
		HashChain:      []string{},
		ResponseCursors: []checkpointResponseCursor{{
			Digest:   checkpointDigest(transportCheckpointCursorDomain, "response-b"),
			Revision: 2,
		}},
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Hour),
		Revision:  2,
	}
	if err := store.persistLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	turn, err := store.BeginFull("responses", "owner-a", "new-owner-a-key", []oaiMsg{{Role: "user", Content: "new"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	_, ownerBStillPresent := store.records[ownerBRecordID]
	ownerACount := 0
	for _, record := range store.records {
		if record.OwnerDigest == ownerA {
			ownerACount++
		}
	}
	store.mu.Unlock()
	if !ownerBStillPresent {
		t.Fatal("owner A churn evicted owner B while owner A had evictable records")
	}
	if ownerACount != transportCheckpointMaxRecords-1 {
		t.Fatalf("owner A record count=%d want=%d after owner-local eviction and replacement", ownerACount, transportCheckpointMaxRecords-1)
	}
	continuation, err := store.BeginResponse("owner-b", "response-b", nil)
	if err != nil {
		t.Fatalf("owner B response cursor became unusable after owner A churn: %v", err)
	}
	_ = continuation.Abort()
	_ = turn.Abort()
}

func TestTransportCheckpointMigratesV1SnapshotWithoutLosingContinuation(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source", "transport.json")
	source, err := openTransportCheckpointStore(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	first := []oaiMsg{{Role: "user", Content: "one"}}
	turn := beginFullForTest(t, source, "chat", "owner", "key", first)
	acceptForTest(t, turn, "conversation-v1", "session-v1", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	logical := readCheckpointForTest(t, sourcePath)
	file, err := decodeTransportCheckpointFile(logical)
	if err != nil {
		t.Fatal(err)
	}
	file.Schema = legacyTransportCheckpointSchema
	legacyRaw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw = append(legacyRaw, '\n')

	legacyPath := filepath.Join(t.TempDir(), "upgrade", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := openTransportCheckpointStore(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	views := checkpointViewsForTest(t, migrated)
	if len(views) != 1 || views[0].ConversationID != "conversation-v1" || views[0].SessionID != "session-v1" {
		t.Fatalf("v1 migration lost checkpoint: %#v", views)
	}
	manifest, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), transportCheckpointSchema) || strings.Contains(string(manifest), legacyTransportCheckpointSchema) {
		t.Fatalf("v1 snapshot was not replaced by v2 manifest: %s", manifest)
	}
	active := []oaiMsg{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "two"},
	}
	continued, err := migrated.BeginFull("chat", "owner", "key", active, false)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Rebound || continued.Binding.ConversationID != "conversation-v1" || continued.Binding.SessionID != "session-v1" {
		t.Fatalf("migrated continuation rebound unexpectedly: %#v", continued)
	}
	_ = continued.Abort()
}

func TestTransportCheckpointClearThenRestoresOnChangeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before change"}})
	acceptForTest(t, turn, "conversation", "session", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	wantErr := errors.New("dependent persistence failed")
	if err := store.ClearThen(func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("ClearThen error=%v, want dependent failure", err)
	}
	if got := checkpointViewsForTest(t, store); len(got) != 1 {
		t.Fatalf("failed dependent change did not restore checkpoint: %#v", got)
	}
	reopened, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkpointViewsForTest(t, reopened); len(got) != 1 {
		t.Fatalf("restored checkpoint did not survive restart: %#v", got)
	}
}

func TestTransportCheckpointClearThenDoesNotCommitChangeWhenInvalidationFails(t *testing.T) {
	dir := t.TempDir()
	store, err := openTransportCheckpointStore(filepath.Join(dir, "transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before change"}})
	acceptForTest(t, turn, "conversation", "session", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.path = filepath.Join(blocker, "transport.json")
	committed := false
	if err := store.ClearThen(func() error { committed = true; return nil }); err == nil {
		t.Fatal("expected checkpoint invalidation failure")
	}
	if committed {
		t.Fatal("dependent change committed after checkpoint invalidation failed")
	}
	if got := checkpointViewsForTest(t, store); len(got) != 1 {
		t.Fatalf("failed invalidation changed in-memory checkpoints: %#v", got)
	}
}
func TestRemoveLegacyDefaultSessionCache(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "m365-native-sessions.json")
	if err := os.WriteFile(legacy, []byte(`{"sessions":[{"title":"PRIVATE","prompt":"FULL-PRIVATE-PROMPT"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyDefaultSessionCache(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy cache still exists: %v", err)
	}
	if err := removeLegacyDefaultSessionCache(legacy); err != nil {
		t.Fatalf("missing legacy cache should be idempotent: %v", err)
	}

	directory := filepath.Join(t.TempDir(), "legacy-directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyDefaultSessionCache(directory); err == nil {
		t.Fatal("legacy cache directory was removed or accepted")
	}
}

func TestTransportCheckpointExpiresAfterExplicitTTLAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	current := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return current }
	store, err := openTransportCheckpointStoreWithClock(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "expires"}})
	acceptForTest(t, turn, "conversation-ttl", "session-ttl", nil, "")

	current = current.Add(transportCheckpointTTL - time.Second)
	reopened, err := openTransportCheckpointStoreWithClock(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkpointViewsForTest(t, reopened); len(got) != 1 {
		t.Fatalf("checkpoint expired before TTL: %#v", got)
	}

	current = current.Add(time.Second)
	reopened, err = openTransportCheckpointStoreWithClock(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkpointViewsForTest(t, reopened); len(got) != 0 {
		t.Fatalf("expired checkpoint survived restart: %#v", got)
	}
	raw := readCheckpointForTest(t, path)
	if strings.Contains(string(raw), "conversation-ttl") {
		t.Fatalf("expired checkpoint remained on disk: %s", raw)
	}
}

func TestTransportCheckpointFileAndIdentifiersAreByteBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints", "transport.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(transportCheckpointMaxFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openTransportCheckpointStore(path); !errors.Is(err, ErrCheckpointCapacity) {
		t.Fatalf("oversized checkpoint error = %v", err)
	}

	store := openCheckpointForTest(t)
	overlong := strings.Repeat("x", transportCheckpointMaxIdentity+1)
	if _, err := store.BeginFull("chat", "owner", overlong, []oaiMsg{{Role: "user", Content: "x"}}, false); !errors.Is(err, ErrCheckpointIdentity) {
		t.Fatalf("overlong key error = %v", err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "x"}})
	if err := turn.Accept(checkpointBinding{ConversationID: overlong, SessionID: "session"}, nil, ""); !errors.Is(err, ErrCheckpointInvalidBinding) {
		t.Fatalf("overlong binding error = %v", err)
	}
	if got := checkpointViewsForTest(t, store); len(got) != 0 {
		t.Fatalf("invalid binding left a checkpoint: %#v", got)
	}
}

func TestTransportCheckpointCanonicalIdentity(t *testing.T) {
	t.Run("JSON key order and argument formatting are stable", func(t *testing.T) {
		store := openCheckpointForTest(t)
		firstAssistant := oaiMsg{Role: "assistant", ToolCalls: []map[string]any{{
			"id": "c1", "type": "function",
			"function": map[string]any{"name": "lookup", "arguments": `{"b":2, "a":1}`},
		}}}
		turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: " USER ", Content: map[string]any{"b": 2, "a": 1}}})
		acceptForTest(t, turn, "conv-json", "s1", []oaiMsg{firstAssistant}, "")

		active := []oaiMsg{
			{Role: "user", Content: map[string]any{"a": 1, "b": 2}},
			{Role: "assistant", ToolCalls: []map[string]any{{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "lookup", "arguments": ` { "a" : 1 , "b" : 2 } `},
			}}},
			{Role: "user", Content: "next"},
		}
		turn = beginFullForTest(t, store, "chat", "owner", "key", active)
		if turn.Rebound || turn.Binding.ConversationID != "conv-json" {
			t.Fatalf("canonical equivalent history did not reuse: %#v", turn)
		}
		if got := turn.Outbound; len(got) != 1 || got[0].Content != "next" {
			t.Fatalf("outbound = %#v", got)
		}
		_ = turn.Abort()
	})

	t.Run("nil and empty structured values remain distinct", func(t *testing.T) {
		store := openCheckpointForTest(t)
		turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: nil}})
		acceptForTest(t, turn, "conv-nil", "s1", nil, "")
		turn = beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: []any{}}})
		if !turn.Rebound || turn.Binding.ConversationID != "" {
			t.Fatalf("nil/empty mismatch reused: %#v", turn)
		}
		_ = turn.Abort()
	})
}

func TestTransportCheckpointToolResultIdentityMismatchRebinds(t *testing.T) {
	base := []oaiMsg{
		{Role: "user", Content: "call lookup"},
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id": "call-1", "type": "function",
			"function": map[string]any{"name": "lookup", "arguments": `{"query":"one"}`},
		}}},
		{Role: "tool", Name: "lookup", ToolCallID: "call-1", Content: "result-one"},
	}
	cases := map[string]oaiMsg{
		"content":      {Role: "tool", ToolCallID: "call-1", Content: "result-two"},
		"tool call id": {Role: "tool", ToolCallID: "call-2", Content: "result-one"},
	}
	for name, toolResult := range cases {
		t.Run(name, func(t *testing.T) {
			store := openCheckpointForTest(t)
			turn := beginFullForTest(t, store, "chat", "owner", "key", base)
			acceptForTest(t, turn, "conv-old", "session-old", nil, "")
			active := append(append([]oaiMsg{}, base[:2]...), toolResult, oaiMsg{Role: "user", Content: "next"})
			turn = beginFullForTest(t, store, "chat", "owner", "key", active)
			if !turn.Rebound || turn.Binding.ConversationID != "" || !reflect.DeepEqual(turn.Outbound, active) {
				t.Fatalf("tool result mismatch did not fully rebind: %#v", turn)
			}
			_ = turn.Abort()
		})
	}
}

func TestTransportCheckpointReusesLegacyToolNameDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	base := []oaiMsg{
		{Role: "user", Content: "call lookup"},
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id": "call-1", "type": "function",
			"function": map[string]any{"name": "lookup", "arguments": `{"query":"one"}`},
		}}},
		{Role: "tool", Name: "lookup", ToolCallID: "call-1", Content: "result-one"},
	}
	legacy, err := canonicalCheckpointMessages(base)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalCheckpointMessage(base[2])
	if err != nil {
		t.Fatal(err)
	}
	var envelope canonicalCheckpointMessageEnvelope
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Name = "lookup"
	canonical, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	legacy.digests[2] = checkpointDigest(transportCheckpointHashDomain, string(canonical))
	legacy.chains, err = extendCheckpointHashChain(nil, legacy.digests)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	store.records["legacy"] = &transportCheckpointRecord{
		ID:               "legacy",
		Namespace:        "chat",
		OwnerDigest:      checkpointDigest(transportCheckpointOwnerDomain, "owner"),
		KeyDigest:        checkpointDigest(transportCheckpointKeyDomain, "key"),
		ConversationID:   "conv-legacy",
		CurrentSessionID: "session-legacy",
		AcceptedCount:    len(base),
		MessageDigests:   legacy.digests,
		HashChain:        legacy.chains,
		CreatedAt:        now,
		UpdatedAt:        now,
		Revision:         1,
	}

	active := append([]oaiMsg{}, base...)
	active[2].Name = ""
	active = append(active, oaiMsg{Role: "user", Content: "next"})
	turn := beginFullForTest(t, store, "chat", "owner", "key", active)
	if turn.Rebound || turn.Binding.ConversationID != "conv-legacy" || len(turn.Outbound) != 1 || turn.Outbound[0].Content != "next" {
		t.Fatalf("legacy tool-name checkpoint did not reuse: %#v", turn)
	}
	acceptForTest(t, turn, "conv-legacy", "session-migrated", []oaiMsg{{Role: "assistant", Content: "next-answer"}}, "")

	migrated := store.records["legacy"]
	current, err := canonicalCheckpointMessages(active)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.MessageDigests[2] != current.digests[2] {
		t.Fatal("accepted legacy match did not migrate to the current tool-result digest")
	}

	store, err = openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart := append(append([]oaiMsg{}, active...), oaiMsg{Role: "assistant", Content: "next-answer"}, oaiMsg{Role: "user", Content: "after restart"})
	turn = beginFullForTest(t, store, "chat", "owner", "key", afterRestart)
	if turn.Rebound || turn.Binding.ConversationID != "conv-legacy" || turn.Binding.SessionID != "session-migrated" || len(turn.Outbound) != 1 || turn.Outbound[0].Content != "after restart" {
		t.Fatalf("migrated checkpoint did not survive restart: %#v", turn)
	}
	_ = turn.Abort()
}

func TestTransportCheckpointMismatchRebindsFullHistory(t *testing.T) {
	base := []oaiMsg{
		{Role: "system", Content: "policy"},
		{Role: "user", Content: "alpha"},
		{Role: "assistant", ToolCalls: []map[string]any{{
			"id": "c1", "type": "function",
			"function": map[string]any{"name": "read", "arguments": `{"path":"a"}`},
		}}},
	}
	cases := map[string][]oaiMsg{
		"one character": {
			{Role: "system", Content: "policy"}, {Role: "user", Content: "alphA"}, base[2],
		},
		"role": {
			{Role: "system", Content: "policy"}, {Role: "assistant", Content: "alpha"}, base[2],
		},
		"tool call": {
			base[0], base[1], {Role: "assistant", ToolCalls: []map[string]any{{
				"id": "c2", "type": "function",
				"function": map[string]any{"name": "read", "arguments": `{"path":"a"}`},
			}}},
		},
		"tool name": {
			base[0], base[1], {Role: "assistant", ToolCalls: []map[string]any{{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "write", "arguments": `{"path":"a"}`},
			}}},
		},
		"tool arguments": {
			base[0], base[1], {Role: "assistant", ToolCalls: []map[string]any{{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "read", "arguments": `{"path":"b"}`},
			}}},
		},
		"compression rewrite": {
			{Role: "system", Content: "policy"}, {Role: "user", Content: "summary of alpha"},
		},
	}
	for name, active := range cases {
		t.Run(name, func(t *testing.T) {
			store := openCheckpointForTest(t)
			turn := beginFullForTest(t, store, "chat", "owner", "key", base)
			acceptForTest(t, turn, "conv-old", "s1", nil, "")
			turn = beginFullForTest(t, store, "chat", "owner", "key", active)
			if !turn.Rebound || turn.Binding.ConversationID != "" || !reflect.DeepEqual(turn.Outbound, active) {
				t.Fatalf("mismatch did not fully rebind: %#v", turn)
			}
			_ = turn.Abort()
		})
	}
}

func TestTransportCheckpointAnonymousHermesReuseAndIsolation(t *testing.T) {
	store := openCheckpointForTest(t)
	history := []oaiMsg{{Role: "user", Content: "anonymous marker"}}
	turn := beginFullForTest(t, store, "chat", "owner-a", "", history)
	acceptForTest(t, turn, "conv-anon", "s1", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	active := append(append([]oaiMsg{}, history...), oaiMsg{Role: "assistant", Content: "answer"}, oaiMsg{Role: "user", Content: "next"})

	turn = beginFullForTest(t, store, "chat", "owner-a", "", active)
	if turn.Binding.ConversationID != "conv-anon" || turn.Rebound {
		t.Fatalf("anonymous exact prefix did not reuse: %#v", turn)
	}

	for _, identity := range []struct{ namespace, owner string }{{"chat", "owner-b"}, {"responses", "owner-a"}} {
		isolated := beginFullForTest(t, store, identity.namespace, identity.owner, "", active)
		if isolated.Binding.ConversationID != "" {
			t.Fatalf("cross-boundary reuse for %#v: %#v", identity, isolated.Binding)
		}
		_ = isolated.Abort()
	}
	_ = turn.Abort()
}

func TestTransportCheckpointAmbiguousAnonymousPrefixStartsFresh(t *testing.T) {
	store := openCheckpointForTest(t)
	first := []oaiMsg{{Role: "user", Content: "same"}}
	for _, key := range []string{"key-a", "key-b"} {
		turn := beginFullForTest(t, store, "chat", "owner", key, first)
		acceptForTest(t, turn, "conv-"+key, "s1", []oaiMsg{{Role: "assistant", Content: "same-answer"}}, "")
	}
	active := []oaiMsg{{Role: "user", Content: "same"}, {Role: "assistant", Content: "same-answer"}, {Role: "user", Content: "next"}}
	turn := beginFullForTest(t, store, "chat", "owner", "", active)
	if !turn.Rebound || turn.Binding.ConversationID != "" || !reflect.DeepEqual(turn.Outbound, active) {
		t.Fatalf("ambiguous prefix did not start distinct fresh binding: %#v", turn)
	}
	_ = turn.Abort()
}

func TestTransportCheckpointAnonymousSelectsUniqueLongestPrefix(t *testing.T) {
	store := openCheckpointForTest(t)
	short := []oaiMsg{{Role: "user", Content: "one"}}
	turn := beginFullForTest(t, store, "chat", "owner", "short", short)
	acceptForTest(t, turn, "conv-short", "s1", nil, "")
	long := []oaiMsg{{Role: "user", Content: "one"}, {Role: "assistant", Content: "one-a"}, {Role: "user", Content: "two"}}
	turn = beginFullForTest(t, store, "chat", "owner", "long", long)
	acceptForTest(t, turn, "conv-long", "s1", []oaiMsg{{Role: "assistant", Content: "two-a"}}, "")

	active := append(append([]oaiMsg{}, long...), oaiMsg{Role: "assistant", Content: "two-a"}, oaiMsg{Role: "user", Content: "three"})
	turn = beginFullForTest(t, store, "chat", "owner", "", active)
	if turn.Rebound || turn.Binding.ConversationID != "conv-long" || len(turn.Outbound) != 1 || turn.Outbound[0].Content != "three" {
		t.Fatalf("unique longest prefix selection = %#v", turn)
	}
	_ = turn.Abort()
}

func TestTransportCheckpointRestartAndSessionRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "checkpoints.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "one"}})
	acceptForTest(t, turn, "conv-restart", "session-1", []oaiMsg{{Role: "assistant", Content: "one-a"}}, "")

	store, err = openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.BeginDelta("chat", "owner", "key", []oaiMsg{{Role: "user", Content: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Binding != (checkpointBinding{ConversationID: "conv-restart", SessionID: "session-1"}) {
		t.Fatalf("restart binding = %#v", turn.Binding)
	}
	acceptForTest(t, turn, "conv-restart", "session-2", []oaiMsg{{Role: "assistant", Content: "two-a"}}, "")

	store, err = openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.BeginDelta("chat", "owner", "key", []oaiMsg{{Role: "user", Content: "three"}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Binding.SessionID != "session-2" {
		t.Fatalf("rotated SessionID = %q", turn.Binding.SessionID)
	}

	raw := readCheckpointForTest(t, path)
	if !strings.Contains(string(raw), `"lastSessionId": "session-1"`) {
		t.Fatalf("last SessionID not preserved: %s", raw)
	}
	_ = turn.Abort()
}

func TestTransportCheckpointEightDeltaTurnsRemainBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		turn, err := store.BeginDelta("legacy", "owner", "conversation", []oaiMsg{{Role: "user", Content: "turn-" + string(rune('0'+i))}})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && turn.Binding.ConversationID != "conv-eight" {
			t.Fatalf("turn %d lost binding: %#v", i, turn.Binding)
		}
		acceptForTest(t, turn, "conv-eight", "session", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	}
	raw := readCheckpointForTest(t, path)
	if strings.Contains(string(raw), "turn-") || strings.Contains(string(raw), "answer") {
		t.Fatalf("checkpoint persisted conversation text: %s", raw)
	}
	var file transportCheckpointFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Records) != 1 || file.Records[0].AcceptedCount != 16 || len(file.Records[0].MessageDigests) != 16 || len(file.Records[0].HashChain) != 16 {
		t.Fatalf("bounded checkpoint = %#v", file.Records)
	}
}

func TestTransportCheckpointConcurrencyAndAbortInvalidation(t *testing.T) {
	store := openCheckpointForTest(t)
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "one"}})
	acceptForTest(t, turn, "conv-one", "s1", nil, "")

	first, err := store.BeginDelta("chat", "owner", "key", []oaiMsg{{Role: "user", Content: "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelta("chat", "owner", "key", []oaiMsg{{Role: "user", Content: "racing"}}); !errors.Is(err, ErrCheckpointBusy) {
		t.Fatalf("same-record concurrent error = %v", err)
	}
	different, err := store.BeginDelta("chat", "owner", "other-key", []oaiMsg{{Role: "user", Content: "independent"}})
	if err != nil {
		t.Fatalf("different record was blocked: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = first.Abort() }()
	go func() { defer wg.Done(); _ = different.Abort() }()
	wg.Wait()

	fresh, err := store.BeginDelta("chat", "owner", "key", []oaiMsg{{Role: "user", Content: "after abort"}})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Binding.ConversationID != "" {
		t.Fatalf("aborted reused binding survived: %#v", fresh.Binding)
	}
	_ = fresh.Abort()
}

func TestTransportCheckpointHistoryCapFailsSafe(t *testing.T) {
	store := openCheckpointForTest(t)
	turn := beginFullForTest(t, store, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "accepted"}})
	acceptForTest(t, turn, "conv-old", "s1", nil, "")
	oversized := make([]oaiMsg, transportCheckpointMaxMessages+1)
	for i := range oversized {
		oversized[i] = oaiMsg{Role: "user", Content: i}
	}
	turn = beginFullForTest(t, store, "chat", "owner", "key", oversized)
	if !turn.Rebound || turn.RecordID() != "" || turn.Binding.ConversationID != "" || len(turn.Outbound) != len(oversized) {
		t.Fatalf("history cap did not force untracked fresh turn: %#v", turn)
	}
	_ = turn.Abort()
	if _, err := store.BeginDelta("chat", "owner", "", nil); !errors.Is(err, ErrCheckpointKeyRequired) {
		t.Fatalf("empty delta key error = %v", err)
	}
}

func TestTransportCheckpointResponseCursorAndPendingToolMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn := beginFullForTest(t, store, "responses", "owner", "conversation", []oaiMsg{{Role: "user", Content: "ask"}})
	toolCall := oaiMsg{Role: "assistant", ToolCalls: []map[string]any{{
		"id": "c1", "type": "function",
		"function": map[string]any{"name": "read_file", "arguments": `{"private":"ARGUMENT-SENTINEL"}`},
	}}}
	acceptForTest(t, turn, "conv-response", "s1", []oaiMsg{toolCall}, "resp-1")

	raw := readCheckpointForTest(t, path)
	for _, forbidden := range []string{"ARGUMENT-SENTINEL", `"private"`, "ask"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("checkpoint leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"callId": "c1"`) || !strings.Contains(string(raw), `"name": "read_file"`) {
		t.Fatalf("bounded pending tool identity missing: %s", raw)
	}

	store, err = openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.BeginResponse("owner", "resp-1", []oaiMsg{{Role: "tool", ToolCallID: "c1", Content: "RESULT-SENTINEL"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(turn.AllowedPriorToolCallIDs, []string{"c1"}) {
		t.Fatalf("allowed prior calls = %#v", turn.AllowedPriorToolCallIDs)
	}
	if _, err := store.BeginResponse("owner", "resp-1", []oaiMsg{{Role: "tool", ToolCallID: "c1", Content: "racing"}}); !errors.Is(err, ErrCheckpointBusy) {
		t.Fatalf("concurrent response parent error = %v", err)
	}
	for _, id := range turn.AllowedPriorToolCallIDs {
		if id == "c2" {
			t.Fatal("wrong call c2 was exposed")
		}
	}
	if strings.Contains(string(readCheckpointForTest(t, path)), "RESULT-SENTINEL") {
		t.Fatal("tool result plaintext persisted while in flight")
	}
	acceptForTest(t, turn, "conv-response", "s2", []oaiMsg{{Role: "assistant", Content: "done"}}, "resp-2")

	if _, err := store.BeginResponse("owner", "resp-1", nil); !errors.Is(err, ErrCheckpointUnknownCursor) {
		t.Fatalf("stale response parent error = %v", err)
	}
	turn, err = store.BeginResponse("owner", "resp-2", []oaiMsg{{Role: "user", Content: "next"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.AllowedPriorToolCallIDs) != 0 {
		t.Fatalf("resolved call remained pending: %#v", turn.AllowedPriorToolCallIDs)
	}
	_ = turn.Abort()
	if _, err := store.BeginResponse("other-owner", "resp-2", nil); !errors.Is(err, ErrCheckpointUnknownCursor) {
		t.Fatalf("cross-owner cursor error = %v", err)
	}
}

func TestTransportCheckpointPrivacyPermissionsAndLegacyReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "checkpoints.json")
	legacy := `{"legacy":{"id":"old","title":"FULL-PRIVATE-PROMPT","conversationId":"old-conv"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := readCheckpointForTest(t, path)
	if !strings.Contains(string(raw), transportCheckpointSchema) || strings.Contains(string(raw), "FULL-PRIVATE-PROMPT") || strings.Contains(string(raw), `"title"`) {
		t.Fatalf("legacy private state survived replacement: %s", raw)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("modes dir=%o file=%o", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}

	turn := beginFullForTest(t, store, "chat", "OWNER-SENTINEL", "RAW-KEY-SENTINEL", []oaiMsg{{Role: "user", Content: "CONTENT-SENTINEL"}})
	acceptForTest(t, turn, "conv-safe", "s-safe", nil, "")
	raw = readCheckpointForTest(t, path)
	for _, forbidden := range []string{"OWNER-SENTINEL", "RAW-KEY-SENTINEL", "CONTENT-SENTINEL", `"title"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("checkpoint leaked %q: %s", forbidden, raw)
		}
	}
}

func TestTransportCheckpointAdminViewAndDeleteArePrivacySafe(t *testing.T) {
	store := openCheckpointForTest(t)
	turn := beginFullForTest(t, store, "responses", "PRIVATE-OWNER", "PRIVATE-KEY", []oaiMsg{{Role: "user", Content: "PRIVATE-CONTENT"}})
	recordID := turn.RecordID()
	acceptForTest(t, turn, "conv-admin", "session-admin", []oaiMsg{{Role: "assistant", Content: "PRIVATE-ANSWER"}}, "resp-admin")

	views := checkpointViewsForTest(t, store)
	if len(views) != 1 || views[0].ID != recordID || views[0].ConversationID != "conv-admin" || views[0].SessionID != "session-admin" || views[0].CreatedAt.IsZero() || views[0].UpdatedAt.IsZero() {
		t.Fatalf("admin views = %#v", views)
	}
	viewJSON, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE-OWNER", "PRIVATE-KEY", "PRIVATE-CONTENT", "PRIVATE-ANSWER", "ownerDigest", "keyDigest", "messageDigests"} {
		if strings.Contains(string(viewJSON), forbidden) {
			t.Fatalf("admin view leaked %q: %s", forbidden, viewJSON)
		}
	}
	deleted, err := store.Delete(recordID)
	if err != nil || !deleted {
		t.Fatalf("Delete() = %v, %v", deleted, err)
	}
	if views := checkpointViewsForTest(t, store); len(views) != 0 {
		t.Fatalf("deleted checkpoint remained: %#v", views)
	}
	if _, err := store.BeginResponse("PRIVATE-OWNER", "resp-admin", nil); !errors.Is(err, ErrCheckpointUnknownCursor) {
		t.Fatalf("deleted cursor error = %v", err)
	}
	deleted, err = store.Delete(recordID)
	if err != nil || deleted {
		t.Fatalf("second Delete() = %v, %v", deleted, err)
	}
}

func TestTransportCheckpointSidecarPolicyIsResentButNotCheckpointed(t *testing.T) {
	store := openCheckpointForTest(t)
	first := []oaiMsg{{Role: "system", Content: "caller policy"}, {Role: "system", Content: "generated-v1", SidecarGenerated: true}, {Role: "user", Content: "one"}}
	turn := beginFullForTest(t, store, "chat", "owner", "key", first)
	acceptForTest(t, turn, "conv-policy", "s1", []oaiMsg{{Role: "assistant", Content: "answer"}}, "")
	active := []oaiMsg{{Role: "system", Content: "caller policy"}, {Role: "system", Content: "generated-v2", SidecarGenerated: true}, {Role: "user", Content: "one"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "two"}}
	turn = beginFullForTest(t, store, "chat", "owner", "key", active)
	if turn.Rebound || turn.Binding.ConversationID != "conv-policy" {
		t.Fatalf("generated policy affected exact history: %#v", turn)
	}
	if len(turn.Outbound) != 3 || turn.Outbound[0].Content != "caller policy" || turn.Outbound[1].Content != "generated-v2" || turn.Outbound[2].Content != "two" {
		t.Fatalf("policy/delta outbound = %#v", turn.Outbound)
	}
	_ = turn.Abort()
}

func openCheckpointForTest(t *testing.T) *transportCheckpointStore {
	t.Helper()
	store, err := openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func checkpointViewsForTest(t *testing.T, store *transportCheckpointStore) []transportCheckpointView {
	t.Helper()
	views, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	return views
}

func beginFullForTest(t *testing.T, store *transportCheckpointStore, namespace, owner, key string, messages []oaiMsg) *checkpointTurn {
	t.Helper()
	turn, err := store.BeginFull(namespace, owner, key, messages, false)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func acceptForTest(t *testing.T, turn *checkpointTurn, conversationID, sessionID string, produced []oaiMsg, responseID string) {
	t.Helper()
	if err := turn.Accept(checkpointBinding{ConversationID: conversationID, SessionID: sessionID}, produced, responseID); err != nil {
		t.Fatal(err)
	}
}

func readCheckpointForTest(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Schema != transportCheckpointSchema {
		return raw
	}
	manifest, err := decodeTransportCheckpointManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(checkpointGenerationPath(path, manifest.Generation))
	if err != nil {
		t.Fatal(err)
	}
	records := make([]transportCheckpointRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		recordFile, _, err := readCheckpointRecordFile(filepath.Join(checkpointGenerationPath(path, manifest.Generation), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, recordFile.Record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	logical, err := json.MarshalIndent(transportCheckpointFile{Schema: transportCheckpointSchema, Records: records}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(logical, '\n')
}
