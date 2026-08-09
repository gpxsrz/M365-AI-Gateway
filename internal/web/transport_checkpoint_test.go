package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
