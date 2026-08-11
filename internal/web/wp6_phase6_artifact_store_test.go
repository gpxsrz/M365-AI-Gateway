package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type blockingArtifactReader struct {
	started chan<- struct{}
	release <-chan struct{}
	raw     []byte
}

func (reader *blockingArtifactReader) Read(buffer []byte) (int, error) {
	if reader.started != nil {
		close(reader.started)
		reader.started = nil
	}
	<-reader.release
	if len(reader.raw) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, reader.raw)
	reader.raw = reader.raw[n:]
	return n, nil
}

func TestWP6ArtifactStoreStreamsInclusiveLimitWithoutHoldingMutex(t *testing.T) {
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{MaxEntries: 3, MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seed, err := store.PutReader("seed.txt", strings.NewReader("seed"), 4)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, putErr := store.PutReader("blocked.txt", &blockingArtifactReader{started: started, release: release, raw: []byte("12345678")}, 8)
		done <- putErr
	}()
	<-started
	staged, err := os.ReadDir(store.blobDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 2 {
		t.Fatalf("blob entries during streaming=%d want committed+staged", len(staged))
	}
	foundStaging := false
	for _, entry := range staged {
		if strings.HasPrefix(entry.Name(), ".artifact-stream-") {
			foundStaging = true
			info, infoErr := entry.Info()
			if infoErr != nil {
				t.Fatal(infoErr)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("staging permissions=%#o", info.Mode().Perm())
			}
		}
	}
	if !foundStaging {
		t.Fatal("private staging file is missing")
	}

	metadataDone := make(chan error, 1)
	go func() {
		_, statErr := store.Stat(seed.Token)
		metadataDone <- statErr
	}()
	select {
	case statErr := <-metadataDone:
		if statErr != nil {
			t.Fatal(statErr)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming PutReader held the store mutex")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if _, err := store.PutReader("oversized.txt", strings.NewReader("123456789"), 8); !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("oversized stream err=%v", err)
	}
}

func TestWP6ArtifactStoreConcurrentCommitsRespectTotalCapacity(t *testing.T) {
	store, err := openArtifactStore(filepath.Join(t.TempDir(), "artifacts"), artifactStoreOptions{MaxEntries: 2, MaxBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, putErr := store.PutReader("first.txt", &blockingArtifactReader{started: firstStarted, release: firstRelease, raw: []byte("1234")}, 4)
		firstDone <- putErr
	}()
	<-firstStarted

	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	close(secondRelease)
	_, err = store.PutReader("second.txt", &blockingArtifactReader{started: secondStarted, release: secondRelease, raw: []byte("5678")}, 4)
	if !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("pending-byte capacity err=%v", err)
	}
	select {
	case <-secondStarted:
		t.Fatal("capacity-rejected stream was read into staging storage")
	default:
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("reserved stream commit: %v", err)
	}
}

func TestWP6ArtifactStorePersistsPrivateExactArtifacts(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "artifacts")
	options := artifactStoreOptions{Clock: func() time.Time { return now }, MaxEntries: 4, MaxBytes: 1024}
	store, err := openArtifactStore(root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.Put("../報告.csv", []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	secondBytes := []byte("第二個😀")
	second, err := store.Put("second.txt", secondBytes)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || first.Filename != "報告.csv" {
		t.Fatalf("artifact identities collide=%t filename=%q", first.Token == second.Token, first.Filename)
	}
	decodedToken, err := base64.RawURLEncoding.DecodeString(first.Token)
	if err != nil || len(decodedToken) != 32 {
		t.Fatalf("capability token bytes=%d err=%v", len(decodedToken), err)
	}
	if first.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || first.Size != 3 {
		t.Fatalf("unexpected exact-byte metadata: size=%d sha256=%q", first.Size, first.SHA256)
	}
	if !first.CreatedAt.Equal(now) || !first.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected lifetime: created=%s expires=%s", first.CreatedAt, first.ExpiresAt)
	}

	restarted, err := openArtifactStore(root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	got, raw, err := readStoredArtifact(restarted, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != first.Token || got.Filename != first.Filename || !bytes.Equal(raw, []byte("abc")) {
		t.Fatalf("restart readback mismatch: tokenMatch=%t filename=%q bytes=%q", got.Token == first.Token, got.Filename, raw)
	}
	_, raw, err = readStoredArtifact(restarted, second.Token)
	if err != nil || !bytes.Equal(raw, secondBytes) {
		t.Fatalf("second artifact readback=%q err=%v", raw, err)
	}

	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("permissions %s=%#o want %#o", path, info.Mode().Perm(), want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(first.Token)) || bytes.Contains(persisted, []byte(second.Token)) {
		t.Fatal("raw capability token persisted")
	}
	firstDigest := sha256.Sum256([]byte(first.Token))
	if !bytes.Contains(persisted, []byte(hex.EncodeToString(firstDigest[:]))) {
		t.Fatal("persisted index is missing the capability-token digest")
	}

	if err := restarted.Delete(first.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStoredArtifact(restarted, first.Token); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("deleted artifact err=%v", err)
	}
	if _, _, err := readStoredArtifact(restarted, second.Token); err != nil {
		t.Fatalf("deleting one artifact removed another: %v", err)
	}
}

func TestWP6ArtifactStoreExpiryAndOrphanCleanup(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{
		Clock:      func() time.Time { return now },
		Lifetime:   time.Hour,
		MaxEntries: 4,
		MaxBytes:   1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifact, err := store.Put("temporary.txt", []byte("temporary"))
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, "blobs", strings.Repeat("a", 64))
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}

	now = now.Add(time.Hour)
	_, _, expiredErr := readStoredArtifact(store, artifact.Token)
	_, _, unknownErr := readStoredArtifact(store, "unknown")
	if !errors.Is(expiredErr, ErrArtifactNotFound) || !errors.Is(unknownErr, ErrArtifactNotFound) || expiredErr.Error() != unknownErr.Error() {
		t.Fatalf("expired=%v unknown=%v", expiredErr, unknownErr)
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expired blobs remain: %v", entries)
	}
}

func TestWP6ArtifactStoreCapacityNeverEvictsUnexpiredArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{MaxEntries: 2, MaxBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Put("first.txt", []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("too-large.txt", []byte("def")); !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("total-byte capacity err=%v", err)
	}
	if _, raw, err := readStoredArtifact(store, first.Token); err != nil || string(raw) != "abc" {
		t.Fatalf("capacity error evicted first artifact: bytes=%q err=%v", raw, err)
	}
	second, err := store.Put("second.txt", []byte("de"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("too-many.txt", nil); !errors.Is(err, ErrArtifactCapacity) {
		t.Fatalf("entry capacity err=%v", err)
	}
	if _, _, err := readStoredArtifact(store, second.Token); err != nil {
		t.Fatalf("entry capacity error evicted second artifact: %v", err)
	}
}

func TestWP6ArtifactStoreRejectsCorruptOrUnsafeStorage(t *testing.T) {
	t.Run("corrupt blob", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		artifact, err := store.Put("result.txt", []byte("good"))
		if err != nil {
			t.Fatal(err)
		}
		blob := onlyArtifactBlob(t, root)
		if err := os.WriteFile(blob, []byte("evil"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readStoredArtifact(store, artifact.Token); !errors.Is(err, ErrArtifactCorrupt) {
			t.Fatalf("corrupt blob err=%v", err)
		}
		restarted, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatalf("restart should isolate corrupt blob instead of failing store open: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if _, _, err := readStoredArtifact(restarted, artifact.Token); !errors.Is(err, ErrArtifactCorrupt) && !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("corrupt artifact became readable after restart: %v", err)
		}
	})

	t.Run("symlink blob", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		artifact, err := store.Put("result.txt", []byte("safe"))
		if err != nil {
			t.Fatal(err)
		}
		blob := onlyArtifactBlob(t, root)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("stolen"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(blob); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, blob); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readStoredArtifact(store, artifact.Token); !errors.Is(err, ErrArtifactCorrupt) {
			t.Fatalf("symlink blob err=%v", err)
		}
	})

	t.Run("non-regular orphan", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if err := os.Mkdir(filepath.Join(root, "blobs", "unexpected"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.Cleanup(); !errors.Is(err, ErrArtifactCorrupt) {
			t.Fatalf("non-regular orphan err=%v", err)
		}
	})
}

func TestWP6ArtifactStoreRestartRecoveryIsArtifactScoped(t *testing.T) {
	t.Run("expired missing blob", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
		clock := func() time.Time { return now }
		store, err := openArtifactStore(root, artifactStoreOptions{Clock: clock, Lifetime: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := store.Put("expired.txt", []byte("expired"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(onlyArtifactBlob(t, root)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Hour)
		restarted, err := openArtifactStore(root, artifactStoreOptions{Clock: clock, Lifetime: time.Hour})
		if err != nil {
			t.Fatalf("expired missing blob blocked restart: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if _, err := restarted.Stat(artifact.Token); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("expired artifact survived recovery: %v", err)
		}
	})

	t.Run("live missing blob", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := store.Put("missing.txt", []byte("missing"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(onlyArtifactBlob(t, root)); err != nil {
			t.Fatal(err)
		}
		restarted, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatalf("live missing blob blocked restart: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if _, err := restarted.Stat(artifact.Token); !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("missing artifact retained a live capability: %v", err)
		}
	})

	t.Run("live digest mismatch", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := store.Put("tampered.txt", []byte("good"))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		blob := onlyArtifactBlob(t, root)
		if err := os.WriteFile(blob, []byte("evil"), 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(2 * time.Second)
		if err := os.Chtimes(blob, future, future); err != nil {
			t.Fatal(err)
		}
		restarted, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatalf("live digest mismatch blocked unrelated startup: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if _, file, err := restarted.Open(artifact.Token); !errors.Is(err, ErrArtifactCorrupt) {
			if file != nil {
				_ = file.Close()
			}
			t.Fatalf("tampered artifact became readable after restart: %v", err)
		}
	})

	t.Run("orphan blob", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		orphan := filepath.Join(root, "blobs", strings.Repeat("a", 32))
		if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		restarted, err := openArtifactStore(root, artifactStoreOptions{})
		if err != nil {
			t.Fatalf("orphan blob blocked restart: %v", err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphan blob survived restart cleanup: %v", err)
		}
	})
}

func TestWP6ArtifactStoreCachesVerifiedBlobIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	payload := bytes.Repeat([]byte("artifact-block-"), 1<<17)
	artifact, err := store.Put("large.bin", payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, file, err := store.Open(artifact.Token); err != nil {
		t.Fatal(err)
	} else {
		_ = file.Close()
	}
	store.mu.Lock()
	firstVerifications := store.fullVerificationCount
	store.mu.Unlock()
	if firstVerifications != 0 {
		t.Fatalf("freshly ingested artifact repeated a full SHA before first GET: count=%d want=0", firstVerifications)
	}
	if _, file, err := store.Open(artifact.Token); err != nil {
		t.Fatal(err)
	} else {
		_ = file.Close()
	}
	store.mu.Lock()
	secondVerifications := store.fullVerificationCount
	store.mu.Unlock()
	if secondVerifications != firstVerifications {
		t.Fatalf("unchanged second GET repeated full SHA verification: first=%d second=%d", firstVerifications, secondVerifications)
	}

	blob := onlyArtifactBlob(t, root)
	originalInfo, err := os.Stat(blob)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)/2] ^= 0xff
	if err := os.WriteFile(blob, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	// Restoring mtime reproduces a same-inode, same-size replacement that a
	// metadata cache must not mistake for the previously verified bytes.
	if err := os.Chtimes(blob, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, file, err := store.Open(artifact.Token); !errors.Is(err, ErrArtifactCorrupt) {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("tampered cached artifact err=%v", err)
	}
	store.mu.Lock()
	tamperVerifications := store.fullVerificationCount
	store.mu.Unlock()
	if tamperVerifications != firstVerifications+1 {
		t.Fatalf("tamper did not force one new full verification: got=%d want=%d", tamperVerifications, firstVerifications+1)
	}
}

func TestWP6ArtifactStoreRollsBackFailedPersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := openArtifactStore(root, artifactStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first, err := store.Put("first.txt", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "index.json")
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(indexPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("second.txt", []byte("second")); err == nil {
		t.Fatal("Put succeeded with an unwritable index target")
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed Put left %d blobs, want 1", len(entries))
	}
	if _, raw, err := readStoredArtifact(store, first.Token); err != nil || string(raw) != "first" {
		t.Fatalf("failed Put changed in-memory state: bytes=%q err=%v", raw, err)
	}
}

func TestWP6ArtifactStoreAutomaticExpiryAndClose(t *testing.T) {
	t.Run("automatic cleanup", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{Lifetime: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if _, err := store.Put("soon.txt", []byte("soon")); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			entries, err := os.ReadDir(filepath.Join(root, "blobs"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("automatic expiry did not remove artifact bytes")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("close stops timer", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := openArtifactStore(root, artifactStoreOptions{Lifetime: 40 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put("retained.txt", []byte("retained")); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		time.Sleep(80 * time.Millisecond)
		entries, err := os.ReadDir(filepath.Join(root, "blobs"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("closed store timer removed artifact: %v", entries)
		}
	})
}

func onlyArtifactBlob(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("blob count=%d want 1", len(entries))
	}
	return filepath.Join(root, "blobs", entries[0].Name())
}

func readStoredArtifact(store *artifactStore, token string) (artifactRecord, []byte, error) {
	record, file, err := store.Open(token)
	if err != nil {
		return artifactRecord{}, nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return artifactRecord{}, nil, err
	}
	return record, raw, nil
}
