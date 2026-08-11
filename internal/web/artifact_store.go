package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultArtifactLifetime   = 24 * time.Hour
	defaultArtifactMaxEntries = 1024
	defaultArtifactMaxBytes   = int64(4 << 30)
	artifactIndexMaxBytes     = int64(4 << 20)
	artifactIndexVersion      = 1
	artifactFilenameMaxBytes  = 240
	artifactCleanupRetry      = time.Minute
)

var (
	ErrArtifactNotFound = errors.New("artifact not found")
	ErrArtifactCapacity = errors.New("artifact store capacity reached")
	ErrArtifactCorrupt  = errors.New("artifact store is corrupt")
)

type artifactStoreOptions struct {
	Clock      func() time.Time
	Lifetime   time.Duration
	MaxEntries int
	MaxBytes   int64
}

type artifactRecord struct {
	Token     string    `json:"-"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type artifactIndexEntry struct {
	TokenSHA256 string    `json:"tokenSha256"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	BlobID      string    `json:"blobId"`
}

type artifactIndex struct {
	Version int                  `json:"version"`
	Entries []artifactIndexEntry `json:"entries"`
}

type artifactStore struct {
	mu                    sync.Mutex
	root                  string
	blobDir               string
	indexPath             string
	clock                 func() time.Time
	lifetime              time.Duration
	maxEntries            int
	maxBytes              int64
	entries               map[string]artifactIndexEntry
	totalBytes            int64
	pending               map[string]struct{}
	pendingBytes          int64
	verified              map[string]artifactBlobVerification
	fullVerificationCount uint64
	timer                 *time.Timer
	closed                bool
}

func openArtifactStore(root string, options artifactStoreOptions) (*artifactStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("artifact store root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact store root: %w", err)
	}
	if err := ensurePrivateArtifactDir(absRoot); err != nil {
		return nil, err
	}
	blobDir := filepath.Join(absRoot, "blobs")
	if err := ensurePrivateArtifactDir(blobDir); err != nil {
		return nil, err
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Lifetime <= 0 {
		options.Lifetime = defaultArtifactLifetime
	}
	if options.MaxEntries <= 0 {
		options.MaxEntries = defaultArtifactMaxEntries
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = defaultArtifactMaxBytes
	}
	s := &artifactStore{
		root:       absRoot,
		blobDir:    blobDir,
		indexPath:  filepath.Join(absRoot, "index.json"),
		clock:      options.Clock,
		lifetime:   options.Lifetime,
		maxEntries: options.MaxEntries,
		maxBytes:   options.MaxBytes,
		entries:    make(map[string]artifactIndexEntry),
		pending:    make(map[string]struct{}),
		verified:   make(map[string]artifactBlobVerification),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.recoverStartupOrphansLocked(); err != nil {
		return nil, err
	}
	s.scheduleLocked()
	return s, nil
}

func (s *artifactStore) Put(filename string, raw []byte) (artifactRecord, error) {
	return s.PutReader(filename, bytes.NewReader(raw), int64(len(raw)))
}

func (s *artifactStore) PutReader(filename string, reader io.Reader, maxBytes int64) (artifactRecord, error) {
	if reader == nil {
		return artifactRecord{}, errors.New("artifact reader is required")
	}
	if maxBytes <= 0 || maxBytes > s.maxBytes {
		maxBytes = s.maxBytes
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return artifactRecord{}, errors.New("artifact store is closed")
	}
	now := s.now()
	if err := s.cleanupLocked(now); err != nil {
		s.mu.Unlock()
		return artifactRecord{}, err
	}
	if len(s.entries)+len(s.pending) >= s.maxEntries || maxBytes > s.maxBytes-s.totalBytes-s.pendingBytes {
		s.mu.Unlock()
		return artifactRecord{}, ErrArtifactCapacity
	}
	temporary, err := createPrivateArtifactTemp(s.blobDir)
	if err != nil {
		s.mu.Unlock()
		return artifactRecord{}, fmt.Errorf("create artifact staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	s.pending[filepath.Base(temporaryPath)] = struct{}{}
	s.pendingBytes += maxBytes
	s.mu.Unlock()

	size, contentDigest, writeErr := writeArtifactTemp(temporary, reader, maxBytes)
	if writeErr != nil {
		s.mu.Lock()
		delete(s.pending, filepath.Base(temporaryPath))
		s.pendingBytes -= maxBytes
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, writeErr
	}

	s.mu.Lock()
	delete(s.pending, filepath.Base(temporaryPath))
	s.pendingBytes -= maxBytes
	if s.closed {
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, errors.New("artifact store is closed")
	}
	if len(s.entries) >= s.maxEntries || size > s.maxBytes-s.totalBytes {
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, ErrArtifactCapacity
	}
	token, tokenDigest, err := s.newCapabilityToken()
	if err != nil {
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, err
	}
	blobID, err := s.newBlobID()
	if err != nil {
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, err
	}
	entry := artifactIndexEntry{
		TokenSHA256: tokenDigest,
		Filename:    sanitizeArtifactFilenameForStore(filename),
		Size:        size,
		SHA256:      contentDigest,
		CreatedAt:   s.now(),
		BlobID:      blobID,
	}
	entry.ExpiresAt = entry.CreatedAt.Add(s.lifetime)
	blobPath := s.blobPath(blobID)
	if err := os.Rename(temporaryPath, blobPath); err != nil {
		s.mu.Unlock()
		_ = os.Remove(temporaryPath)
		return artifactRecord{}, fmt.Errorf("store artifact bytes: %w", err)
	}
	_ = syncArtifactDir(s.blobDir)
	s.entries[tokenDigest] = entry
	s.totalBytes += size
	if err := s.persistLocked(); err != nil {
		delete(s.entries, tokenDigest)
		s.totalBytes -= size
		removeErr := os.Remove(blobPath)
		s.mu.Unlock()
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return artifactRecord{}, fmt.Errorf("persist artifact index: %v; rollback artifact bytes: %w", err, removeErr)
		}
		return artifactRecord{}, fmt.Errorf("persist artifact index: %w", err)
	}
	s.rememberIngestedVerificationLocked(entry)
	s.scheduleLocked()
	s.mu.Unlock()
	return recordForArtifact(entry, token), nil
}

func (s *artifactStore) Stat(token string) (artifactRecord, error) {
	entry, err := s.entry(token)
	if err != nil {
		return artifactRecord{}, err
	}
	return recordForArtifact(entry, token), nil
}

func (s *artifactStore) Open(token string) (artifactRecord, *os.File, error) {
	entry, err := s.entry(token)
	if err != nil {
		return artifactRecord{}, nil, err
	}
	file, err := s.openIntegrityCheckedBlob(entry)
	if err != nil {
		if errors.Is(err, ErrArtifactCorrupt) {
			s.quarantineCorruptArtifact(entry)
		}
		return artifactRecord{}, nil, err
	}
	return recordForArtifact(entry, token), file, nil
}

func (s *artifactStore) entry(token string) (artifactIndexEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return artifactIndexEntry{}, errors.New("artifact store is closed")
	}
	digest := capabilityTokenDigest(token)
	entry, ok := s.entries[digest]
	if !ok {
		return artifactIndexEntry{}, ErrArtifactNotFound
	}
	if !s.now().Before(entry.ExpiresAt) {
		_ = s.removeExpiredLocked(digest, entry)
		s.scheduleLocked()
		return artifactIndexEntry{}, ErrArtifactNotFound
	}
	return entry, nil
}

func (s *artifactStore) Delete(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("artifact store is closed")
	}
	digest := capabilityTokenDigest(token)
	entry, ok := s.entries[digest]
	if !ok || !s.now().Before(entry.ExpiresAt) {
		if ok {
			_ = s.removeExpiredLocked(digest, entry)
			s.scheduleLocked()
		}
		return ErrArtifactNotFound
	}
	delete(s.entries, digest)
	s.totalBytes -= entry.Size
	if err := s.persistLocked(); err != nil {
		s.entries[digest] = entry
		s.totalBytes += entry.Size
		return fmt.Errorf("persist artifact deletion: %w", err)
	}
	delete(s.verified, entry.BlobID)
	s.scheduleLocked()
	if err := removeArtifactBlob(s.blobPath(entry.BlobID)); err != nil {
		return err
	}
	return nil
}

func (s *artifactStore) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("artifact store is closed")
	}
	err := s.cleanupLocked(s.now())
	s.scheduleLocked()
	return err
}

func (s *artifactStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return nil
}

func (s *artifactStore) load() error {
	raw, err := readPrivateArtifactFile(s.indexPath, artifactIndexMaxBytes)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read artifact index: %w", err)
	}
	var index artifactIndex
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return fmt.Errorf("%w: decode index", ErrArtifactCorrupt)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing index data", ErrArtifactCorrupt)
	}
	if index.Version != artifactIndexVersion {
		return ErrArtifactCorrupt
	}
	if len(index.Entries) > s.maxEntries {
		return ErrArtifactCapacity
	}
	seenBlobs := make(map[string]struct{}, len(index.Entries))
	dirty := false
	now := s.now()
	for _, entry := range index.Entries {
		if validateArtifactEntry(entry) != nil {
			dirty = true
			continue
		}
		if _, exists := s.entries[entry.TokenSHA256]; exists {
			dirty = true
			continue
		}
		if _, exists := seenBlobs[entry.BlobID]; exists {
			dirty = true
			continue
		}
		if !now.Before(entry.ExpiresAt) {
			dirty = true
			continue
		}
		if entry.Size > s.maxBytes-s.totalBytes {
			dirty = true
			continue
		}
		if !artifactBlobMetadataUsableAtStartup(s.blobPath(entry.BlobID), entry) {
			dirty = true
			continue
		}
		s.entries[entry.TokenSHA256] = entry
		seenBlobs[entry.BlobID] = struct{}{}
		s.totalBytes += entry.Size
	}
	if dirty {
		if err := s.persistLocked(); err != nil {
			return fmt.Errorf("persist recovered artifact index: %w", err)
		}
	}
	return nil
}

func (s *artifactStore) cleanupLocked(now time.Time) error {
	expired := make([]artifactIndexEntry, 0)
	for digest, entry := range s.entries {
		if !now.Before(entry.ExpiresAt) {
			expired = append(expired, entry)
			delete(s.entries, digest)
			s.totalBytes -= entry.Size
		}
	}
	if len(expired) > 0 {
		if err := s.persistLocked(); err != nil {
			for _, entry := range expired {
				s.entries[entry.TokenSHA256] = entry
				s.totalBytes += entry.Size
			}
			return fmt.Errorf("persist artifact cleanup: %w", err)
		}
	}
	for _, entry := range expired {
		delete(s.verified, entry.BlobID)
		if err := removeArtifactBlob(s.blobPath(entry.BlobID)); err != nil {
			return err
		}
	}
	return s.removeOrphanBlobsLocked()
}

func (s *artifactStore) removeExpiredLocked(digest string, entry artifactIndexEntry) error {
	delete(s.entries, digest)
	s.totalBytes -= entry.Size
	if err := s.persistLocked(); err != nil {
		s.entries[digest] = entry
		s.totalBytes += entry.Size
		return err
	}
	delete(s.verified, entry.BlobID)
	return removeArtifactBlob(s.blobPath(entry.BlobID))
}

func (s *artifactStore) removeOrphanBlobsLocked() error {
	if err := validatePrivateArtifactDir(s.blobDir); err != nil {
		return err
	}
	referenced := make(map[string]struct{}, len(s.entries))
	for _, entry := range s.entries {
		referenced[entry.BlobID] = struct{}{}
	}
	entries, err := os.ReadDir(s.blobDir)
	if err != nil {
		return fmt.Errorf("read artifact blobs: %w", err)
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: non-regular blob", ErrArtifactCorrupt)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect artifact blob: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular blob", ErrArtifactCorrupt)
		}
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}
		if _, ok := s.pending[entry.Name()]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(s.blobDir, entry.Name())); err != nil {
			return fmt.Errorf("remove orphan artifact blob: %w", err)
		}
	}
	return nil
}

func (s *artifactStore) persistLocked() error {
	entries := make([]artifactIndexEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TokenSHA256 < entries[j].TokenSHA256 })
	raw, err := json.Marshal(artifactIndex{Version: artifactIndexVersion, Entries: entries})
	if err != nil {
		return err
	}
	if int64(len(raw)) > artifactIndexMaxBytes {
		return ErrArtifactCapacity
	}
	return atomicWritePrivateArtifactFile(s.indexPath, raw)
}

func (s *artifactStore) newCapabilityToken() (string, string, error) {
	for {
		raw := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return "", "", fmt.Errorf("generate artifact capability: %w", err)
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		digest := capabilityTokenDigest(token)
		if _, exists := s.entries[digest]; !exists {
			return token, digest, nil
		}
	}
}

func (s *artifactStore) newBlobID() (string, error) {
	for {
		raw := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return "", fmt.Errorf("generate artifact blob identity: %w", err)
		}
		id := hex.EncodeToString(raw)
		if _, err := os.Lstat(s.blobPath(id)); os.IsNotExist(err) {
			return id, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect artifact blob identity: %w", err)
		}
	}
}

func (s *artifactStore) scheduleLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.closed || len(s.entries) == 0 {
		return
	}
	next := time.Time{}
	for _, entry := range s.entries {
		if next.IsZero() || entry.ExpiresAt.Before(next) {
			next = entry.ExpiresAt
		}
	}
	delay := next.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	s.timer = time.AfterFunc(delay, s.expireScheduled)
}

func (s *artifactStore) expireScheduled() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if s.closed {
		return
	}
	if err := s.cleanupLocked(s.now()); err != nil {
		s.timer = time.AfterFunc(artifactCleanupRetry, s.expireScheduled)
		return
	}
	s.scheduleLocked()
}

func (s *artifactStore) now() time.Time { return s.clock().UTC() }

func (s *artifactStore) blobPath(id string) string { return filepath.Join(s.blobDir, id) }

func recordForArtifact(entry artifactIndexEntry, token string) artifactRecord {
	return artifactRecord{
		Token:     token,
		Filename:  entry.Filename,
		Size:      entry.Size,
		SHA256:    entry.SHA256,
		CreatedAt: entry.CreatedAt,
		ExpiresAt: entry.ExpiresAt,
	}
}

func capabilityTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func validateArtifactEntry(entry artifactIndexEntry) error {
	if !validArtifactDigest(entry.TokenSHA256) || !validArtifactDigest(entry.SHA256) || !validArtifactDigest(entry.BlobID) {
		return fmt.Errorf("%w: invalid artifact identity", ErrArtifactCorrupt)
	}
	if entry.Filename == "" || entry.Filename != sanitizeArtifactFilenameForStore(entry.Filename) {
		return fmt.Errorf("%w: unsafe artifact filename", ErrArtifactCorrupt)
	}
	if entry.Size < 0 || entry.CreatedAt.IsZero() || entry.ExpiresAt.IsZero() || !entry.ExpiresAt.After(entry.CreatedAt) {
		return fmt.Errorf("%w: invalid artifact metadata", ErrArtifactCorrupt)
	}
	return nil
}

func validArtifactDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func sanitizeArtifactFilenameForStore(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.Base(value)
	var b strings.Builder
	for _, r := range value {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("/\\\"<>|:*?", r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	value = strings.Trim(b.String(), " .")
	if value == "" || value == "." || value == ".." {
		value = "artifact.bin"
	}
	if len(value) <= artifactFilenameMaxBytes {
		return value
	}
	value = value[:artifactFilenameMaxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimRight(value, " .")
}

func ensurePrivateArtifactDir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create artifact directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect artifact directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: non-directory storage", ErrArtifactCorrupt)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect artifact directory: %w", err)
	}
	return nil
}

func validatePrivateArtifactDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect artifact directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: non-directory storage", ErrArtifactCorrupt)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: unsafe directory permissions", ErrArtifactCorrupt)
	}
	return nil
}

func atomicWritePrivateArtifactFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := validatePrivateArtifactDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular persistence target", ErrArtifactCorrupt)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
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
	// The renamed file inherited the temporary file's 0600 mode. Directory
	// sync is best effort because some supported filesystems reject it after
	// the atomic rename has already committed and cannot safely be rolled back.
	_ = syncArtifactDir(dir)
	return nil
}

func createPrivateArtifactTemp(dir string) (*os.File, error) {
	if err := validatePrivateArtifactDir(dir); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(dir, ".artifact-stream-*")
	if err != nil {
		return nil, err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, err
	}
	return temporary, nil
}

func writeArtifactTemp(temporary *os.File, reader io.Reader, maxBytes int64) (int64, string, error) {
	readLimit := maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(reader, readLimit))
	if err != nil {
		temporary.Close()
		return 0, "", fmt.Errorf("stream artifact bytes: %w", err)
	}
	if size > maxBytes {
		temporary.Close()
		return 0, "", ErrArtifactCapacity
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return 0, "", fmt.Errorf("sync artifact bytes: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return 0, "", fmt.Errorf("close artifact bytes: %w", err)
	}
	return size, hex.EncodeToString(digest.Sum(nil)), nil
}

func syncArtifactDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readPrivateArtifactFile(path string, limit int64) ([]byte, error) {
	file, info, err := openRegularArtifactFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > limit {
		return nil, ErrArtifactCorrupt
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrArtifactCorrupt
	}
	return raw, nil
}

func openRegularArtifactFile(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: non-regular file", ErrArtifactCorrupt)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, statErr := file.Stat()
	after, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		file.Close()
		return nil, nil, fmt.Errorf("%w: artifact file changed during open", ErrArtifactCorrupt)
	}
	if opened.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, nil, err
		}
		opened, err = file.Stat()
		if err != nil {
			file.Close()
			return nil, nil, err
		}
	}
	return file, opened, nil
}

func removeArtifactBlob(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: non-regular blob", ErrArtifactCorrupt)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove artifact blob: %w", err)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}
