package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransportCheckpointEvictionKeepsGenerationStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "transport.json")
	store, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	store.now = func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}

	for i := 0; i < transportCheckpointMaxRecords; i++ {
		key := fmt.Sprintf("key-%03d", i)
		turn := beginFullForTest(t, store, "hermes", "owner", key, []oaiMsg{{Role: "user", Content: key}})
		acceptForTest(t, turn, "conversation-"+key, "session-"+key, []oaiMsg{{Role: "assistant", Content: "accepted"}}, "")
	}

	generation := store.generation
	ownerDigest := checkpointDigest(transportCheckpointOwnerDomain, "owner")
	oldKeyDigest := checkpointDigest(transportCheckpointKeyDomain, "key-000")
	store.mu.Lock()
	oldMatches := store.findExplicitLocked("hermes", ownerDigest, oldKeyDigest)
	store.mu.Unlock()
	if len(oldMatches) != 1 {
		t.Fatalf("oldest checkpoint matches=%d, want 1", len(oldMatches))
	}
	oldPath := checkpointRecordPath(path, generation, oldMatches[0].ID)

	turn := beginFullForTest(t, store, "hermes", "owner", "key-new", []oaiMsg{{Role: "user", Content: "new"}})
	if store.generation != generation {
		t.Fatalf("single-record eviction rotated generation from %q to %q", generation, store.generation)
	}
	acceptForTest(t, turn, "conversation-new", "session-new", []oaiMsg{{Role: "assistant", Content: "accepted"}}, "")

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("evicted checkpoint file still exists or cannot be inspected: %v", err)
	}

	reloaded, err := openTransportCheckpointStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.records) != transportCheckpointMaxRecords {
		t.Fatalf("reloaded records=%d, want %d", len(reloaded.records), transportCheckpointMaxRecords)
	}
	newKeyDigest := checkpointDigest(transportCheckpointKeyDomain, "key-new")
	reloaded.mu.Lock()
	oldMatches = reloaded.findExplicitLocked("hermes", ownerDigest, oldKeyDigest)
	newMatches := reloaded.findExplicitLocked("hermes", ownerDigest, newKeyDigest)
	reloaded.mu.Unlock()
	if len(oldMatches) != 0 || len(newMatches) != 1 {
		t.Fatalf("reloaded eviction old=%d new=%d, want old=0 new=1", len(oldMatches), len(newMatches))
	}
}
