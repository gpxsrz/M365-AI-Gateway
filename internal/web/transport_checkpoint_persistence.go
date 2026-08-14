package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const transportCheckpointRecordSchema = "wp6-transport-checkpoint-record/v1"

type transportCheckpointManifest struct {
	Schema     string `json:"schema"`
	Generation string `json:"generation"`
}

type transportCheckpointRecordFile struct {
	Schema string                    `json:"schema"`
	Record transportCheckpointRecord `json:"record"`
}

type checkpointGenerationTransition struct {
	oldGeneration     string
	oldRecordBytes    map[string]int64
	oldPersistedBytes int64
	newGeneration     string
}

type loadedCheckpointRecord struct {
	record *transportCheckpointRecord
	bytes  int64
	path   string
	dirty  bool
}

func (s *transportCheckpointStore) loadPersistence() error {
	raw, err := readCheckpointFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(checkpointRecordsRoot(s.path))
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read: %w", ErrCheckpointPersistence, err)
	}
	var header struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return s.replaceCheckpointPersistenceWithEmptyGeneration()
	}
	switch header.Schema {
	case transportCheckpointSchema:
		if err := s.loadGenerationManifest(raw); err != nil {
			return err
		}
	case legacyTransportCheckpointSchema:
		if err := s.loadLegacyCheckpointSnapshot(raw); err != nil {
			return err
		}
		if err := s.persistAllLocked(); err != nil {
			return err
		}
	default:
		// Historical session-cache formats may contain titles/prompts. Replace
		// them rather than carrying private plaintext into the WP6 checkpoint
		// store.
		return s.replaceCheckpointPersistenceWithEmptyGeneration()
	}
	s.recomputeNextPruneAtLocked()
	return nil
}

func (s *transportCheckpointStore) replaceCheckpointPersistenceWithEmptyGeneration() error {
	s.records = make(map[string]*transportCheckpointRecord)
	s.recordBytes = make(map[string]int64)
	s.persistedBytes = 0
	s.nextPruneAt = time.Time{}
	return s.persistAllLocked()
}

func (s *transportCheckpointStore) loadLegacyCheckpointSnapshot(raw []byte) error {
	file, err := decodeTransportCheckpointFile(raw)
	if err != nil || file.Schema != legacyTransportCheckpointSchema {
		return s.replaceCheckpointPersistenceWithEmptyGeneration()
	}
	openedAt := s.now()
	for i := range file.Records {
		record := cloneTransportCheckpointRecord(&file.Records[i])
		if record.InFlight {
			continue
		}
		// Records written before phase isolation had no separate public-sync
		// cursor because every accepted turn was also sent to ChatHub. Preserve
		// that meaning during migration instead of replaying old history.
		if record.ConversationID != "" && record.AcceptedCount > 0 && record.PublicAcceptedCount == 0 {
			record.PublicAcceptedCount = record.AcceptedCount
		}
		if len(record.CompletedToolCallDigests) == 0 && len(record.CompletedToolEvidence) > 0 {
			record.CompletedToolCallDigests = completedToolCallDigests(record.CompletedToolEvidence)
		}
		if len(record.CompletedToolIdentityDigests) == 0 && len(record.CompletedToolEvidence) > 0 {
			record.CompletedToolIdentityDigests = completedToolIdentityDigests(record.CompletedToolEvidence)
		}
		if len(s.records) >= transportCheckpointMaxRecords || !validTransportCheckpointRecord(record) || transportCheckpointExpired(record, openedAt) {
			continue
		}
		if _, duplicate := s.records[record.ID]; duplicate {
			continue
		}
		s.records[record.ID] = record
	}
	return nil
}

func (s *transportCheckpointStore) loadGenerationManifest(raw []byte) error {
	manifest, err := decodeTransportCheckpointManifest(raw)
	if err != nil || manifest.Schema != transportCheckpointSchema || !validCheckpointGeneration(manifest.Generation) {
		return s.replaceCheckpointPersistenceWithEmptyGeneration()
	}
	if err := secureCheckpointPath(s.path); err != nil {
		return err
	}
	if err := ensureCheckpointRecordDirectories(s.path, manifest.Generation); err != nil {
		return err
	}
	s.generation = manifest.Generation
	s.recordBytes = make(map[string]int64)
	s.persistedBytes = 0

	generationDir := checkpointGenerationPath(s.path, manifest.Generation)
	entries, err := os.ReadDir(generationDir)
	if err != nil {
		return fmt.Errorf("%w: read checkpoint generation: %v", ErrCheckpointPersistence, err)
	}
	openedAt := s.now()
	candidates := make([]loadedCheckpointRecord, 0, len(entries))
	for _, entry := range entries {
		fullPath := filepath.Join(generationDir, entry.Name())
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			_ = os.RemoveAll(fullPath)
			continue
		}
		recordFile, size, err := readCheckpointRecordFile(fullPath)
		if err != nil || recordFile.Schema != transportCheckpointRecordSchema {
			_ = os.Remove(fullPath)
			continue
		}
		record := cloneTransportCheckpointRecord(&recordFile.Record)
		dirty := false
		if record.ConversationID != "" && record.AcceptedCount > 0 && record.PublicAcceptedCount == 0 {
			record.PublicAcceptedCount = record.AcceptedCount
			dirty = true
		}
		if checkpointRecordFilename(record.ID) != entry.Name() || record.InFlight || !validTransportCheckpointRecord(record) || transportCheckpointExpired(record, openedAt) {
			_ = os.Remove(fullPath)
			continue
		}
		if len(record.CompletedToolCallDigests) == 0 && len(record.CompletedToolEvidence) > 0 {
			record.CompletedToolCallDigests = completedToolCallDigests(record.CompletedToolEvidence)
			dirty = true
		}
		if len(record.CompletedToolIdentityDigests) == 0 && len(record.CompletedToolEvidence) > 0 {
			record.CompletedToolIdentityDigests = completedToolIdentityDigests(record.CompletedToolEvidence)
			dirty = true
		}
		candidates = append(candidates, loadedCheckpointRecord{record: record, bytes: size, path: fullPath, dirty: dirty})
	}

	// A valid writer never exceeds these limits. If external corruption or an
	// interrupted older build leaves excess files, keep the newest safe subset
	// and remove the rest instead of failing unrelated API startup.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].record.UpdatedAt.Equal(candidates[j].record.UpdatedAt) {
			return candidates[i].record.ID < candidates[j].record.ID
		}
		return candidates[i].record.UpdatedAt.After(candidates[j].record.UpdatedAt)
	})
	for _, candidate := range candidates {
		if len(s.records) >= transportCheckpointMaxRecords || s.persistedBytes+candidate.bytes > transportCheckpointMaxFileBytes {
			_ = os.Remove(candidate.path)
			continue
		}
		if _, duplicate := s.records[candidate.record.ID]; duplicate {
			_ = os.Remove(candidate.path)
			continue
		}
		s.records[candidate.record.ID] = candidate.record
		s.recordBytes[candidate.record.ID] = candidate.bytes
		s.persistedBytes += candidate.bytes
	}
	for _, candidate := range candidates {
		if !candidate.dirty {
			continue
		}
		if current, ok := s.records[candidate.record.ID]; ok {
			if err := s.persistRecordLocked(current); err != nil {
				return err
			}
		}
	}
	_ = syncCheckpointDirectory(generationDir)
	cleanupCheckpointGenerations(s.path, s.generation)
	return nil
}

func (s *transportCheckpointStore) persistRecordLocked(record *transportCheckpointRecord) error {
	if record == nil {
		return ErrCheckpointPersistence
	}
	if err := s.ensureGenerationLocked(); err != nil {
		return err
	}
	raw, err := encodeCheckpointRecordFile(record)
	if err != nil {
		return err
	}
	oldSize := s.recordBytes[record.ID]
	newTotal := s.persistedBytes - oldSize + int64(len(raw))
	if newTotal > transportCheckpointMaxFileBytes {
		return ErrCheckpointCapacity
	}
	if err := writeCheckpointFileAtomic(checkpointRecordPath(s.path, s.generation, record.ID), raw); err != nil {
		return fmt.Errorf("%w: write record: %v", ErrCheckpointPersistence, err)
	}
	if s.recordBytes == nil {
		s.recordBytes = make(map[string]int64)
	}
	s.recordBytes[record.ID] = int64(len(raw))
	s.persistedBytes = newTotal
	s.noteCheckpointExpiryLocked(record)
	return nil
}

func (s *transportCheckpointStore) persistRecordReplacingEvictedLocked(record, evicted *transportCheckpointRecord) error {
	if record == nil || evicted == nil {
		return ErrCheckpointPersistence
	}
	if err := s.ensureGenerationLocked(); err != nil {
		return err
	}
	raw, err := encodeCheckpointRecordFile(record)
	if err != nil {
		return err
	}
	evictedSize := s.recordBytes[evicted.ID]
	newTotal := s.persistedBytes - evictedSize + int64(len(raw))
	if newTotal > transportCheckpointMaxFileBytes {
		return ErrCheckpointCapacity
	}

	// Publish the in-flight replacement before removing the evicted record.
	// If writing fails, the old record and all accounting stay untouched. If a
	// crash lands between the write and delete, startup discards the in-flight
	// replacement and keeps the old accepted record. A crash after the delete
	// can only leave one fewer reusable checkpoint.
	newPath := checkpointRecordPath(s.path, s.generation, record.ID)
	if err := writeCheckpointFileAtomic(newPath, raw); err != nil {
		return fmt.Errorf("%w: write replacement record: %v", ErrCheckpointPersistence, err)
	}
	evictedPath := checkpointRecordPath(s.path, s.generation, evicted.ID)
	if err := os.Remove(evictedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(newPath)
		_ = syncCheckpointDirectory(checkpointGenerationPath(s.path, s.generation))
		return fmt.Errorf("%w: delete evicted record: %v", ErrCheckpointPersistence, err)
	}
	if s.recordBytes == nil {
		s.recordBytes = make(map[string]int64)
	}
	delete(s.recordBytes, evicted.ID)
	s.recordBytes[record.ID] = int64(len(raw))
	s.persistedBytes = newTotal
	s.noteCheckpointExpiryLocked(record)
	_ = syncCheckpointDirectory(checkpointGenerationPath(s.path, s.generation))
	return nil
}

func (s *transportCheckpointStore) deleteRecordLocked(recordID string) error {
	if s.generation == "" {
		delete(s.recordBytes, recordID)
		return nil
	}
	path := checkpointRecordPath(s.path, s.generation, recordID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: delete record: %v", ErrCheckpointPersistence, err)
	}
	if size, ok := s.recordBytes[recordID]; ok {
		s.persistedBytes -= size
		if s.persistedBytes < 0 {
			s.persistedBytes = 0
		}
		delete(s.recordBytes, recordID)
	}
	_ = syncCheckpointDirectory(checkpointGenerationPath(s.path, s.generation))
	return nil
}

func (s *transportCheckpointStore) persistAllLocked() error {
	transition, err := s.switchCheckpointGenerationLocked(s.records)
	if err != nil {
		return err
	}
	s.commitCheckpointGenerationLocked(transition)
	s.recomputeNextPruneAtLocked()
	return nil
}

func (s *transportCheckpointStore) ensureGenerationLocked() error {
	if s.generation != "" {
		return nil
	}
	transition, err := s.switchCheckpointGenerationLocked(map[string]*transportCheckpointRecord{})
	if err != nil {
		return err
	}
	s.commitCheckpointGenerationLocked(transition)
	return nil
}

func (s *transportCheckpointStore) switchCheckpointGenerationLocked(records map[string]*transportCheckpointRecord) (checkpointGenerationTransition, error) {
	startedAt := time.Now()
	transition := checkpointGenerationTransition{
		oldGeneration:     s.generation,
		oldRecordBytes:    cloneCheckpointRecordBytes(s.recordBytes),
		oldPersistedBytes: s.persistedBytes,
	}
	generation, err := newCheckpointID()
	if err != nil {
		return transition, err
	}
	if err := ensureCheckpointRecordDirectories(s.path, generation); err != nil {
		return transition, err
	}
	generationDir := checkpointGenerationPath(s.path, generation)
	newSizes := make(map[string]int64, len(records))
	var total int64
	reusedRecords := 0
	writtenRecords := 0
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, err := encodeCheckpointRecordFile(records[id])
		if err != nil {
			_ = os.RemoveAll(generationDir)
			return transition, err
		}
		total += int64(len(raw))
		if total > transportCheckpointMaxFileBytes {
			_ = os.RemoveAll(generationDir)
			return transition, ErrCheckpointCapacity
		}
		destination := checkpointRecordPath(s.path, generation, id)
		reused := false
		if transition.oldGeneration != "" && s.recordBytes[id] == int64(len(raw)) {
			source := checkpointRecordPath(s.path, transition.oldGeneration, id)
			reused = reuseCheckpointRecordFile(source, destination, raw)
		}
		if !reused {
			if err := writeCheckpointFileAtomic(destination, raw); err != nil {
				_ = os.RemoveAll(generationDir)
				return transition, fmt.Errorf("%w: write generation record: %v", ErrCheckpointPersistence, err)
			}
			writtenRecords++
		} else {
			reusedRecords++
		}
		newSizes[id] = int64(len(raw))
	}
	// Unchanged records may have been hard-linked from the active generation.
	// Flush the new directory once before publishing its manifest so a crash
	// cannot expose a generation whose directory entries were never durable.
	if err := syncCheckpointDirectory(generationDir); err != nil {
		_ = os.RemoveAll(generationDir)
		return transition, fmt.Errorf("%w: sync generation directory: %v", ErrCheckpointPersistence, err)
	}
	if err := writeCheckpointManifest(s.path, generation); err != nil {
		_ = os.RemoveAll(generationDir)
		return transition, err
	}
	transition.newGeneration = generation
	s.generation = generation
	s.recordBytes = newSizes
	s.persistedBytes = total
	s.generationSwitchCount++
	s.lastGenerationRecordCount = len(records)
	s.lastGenerationReusedRecordCount = reusedRecords
	s.lastGenerationWrittenRecordCount = writtenRecords
	s.lastGenerationDuration = time.Since(startedAt)
	if len(records) > 0 || s.lastGenerationDuration >= 100*time.Millisecond {
		log.Printf("[checkpoint-trace] operation=generation_switch records=%d reused=%d written=%d bytes=%d total_ms=%d", len(records), reusedRecords, writtenRecords, total, s.lastGenerationDuration.Milliseconds())
	}
	return transition, nil
}

func reuseCheckpointRecordFile(source, destination string, expected []byte) bool {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(expected)) {
		return false
	}
	raw, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(raw, expected) {
		return false
	}
	if err := os.Link(source, destination); err != nil {
		return false
	}
	linked, err := os.ReadFile(destination)
	if err == nil && bytes.Equal(linked, expected) {
		return true
	}
	_ = os.Remove(destination)
	return false
}

func (s *transportCheckpointStore) rollbackCheckpointGenerationLocked(transition checkpointGenerationTransition) error {
	if transition.oldGeneration == "" {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: rollback manifest: %v", ErrCheckpointPersistence, err)
		}
		_ = syncCheckpointDirectory(filepath.Dir(s.path))
		s.generation = ""
	} else {
		if err := writeCheckpointManifest(s.path, transition.oldGeneration); err != nil {
			return err
		}
		s.generation = transition.oldGeneration
	}
	s.recordBytes = cloneCheckpointRecordBytes(transition.oldRecordBytes)
	s.persistedBytes = transition.oldPersistedBytes
	if transition.newGeneration != "" {
		_ = os.RemoveAll(checkpointGenerationPath(s.path, transition.newGeneration))
		_ = syncCheckpointDirectory(checkpointRecordsRoot(s.path))
	}
	return nil
}

func (s *transportCheckpointStore) commitCheckpointGenerationLocked(transition checkpointGenerationTransition) {
	if transition.oldGeneration != "" && transition.oldGeneration != s.generation {
		_ = os.RemoveAll(checkpointGenerationPath(s.path, transition.oldGeneration))
		_ = syncCheckpointDirectory(checkpointRecordsRoot(s.path))
	}
	cleanupCheckpointGenerations(s.path, s.generation)
}

func encodeCheckpointRecordFile(record *transportCheckpointRecord) ([]byte, error) {
	raw, err := json.Marshal(transportCheckpointRecordFile{Schema: transportCheckpointRecordSchema, Record: *cloneTransportCheckpointRecord(record)})
	if err != nil {
		return nil, fmt.Errorf("%w: encode record: %v", ErrCheckpointPersistence, err)
	}
	raw = append(raw, '\n')
	if int64(len(raw)) > transportCheckpointMaxFileBytes {
		return nil, ErrCheckpointCapacity
	}
	return raw, nil
}

func readCheckpointRecordFile(path string) (transportCheckpointRecordFile, int64, error) {
	var file transportCheckpointRecordFile
	handle, err := os.Open(path)
	if err != nil {
		return file, 0, err
	}
	defer handle.Close()
	limited := io.LimitReader(handle, transportCheckpointMaxFileBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return file, 0, err
	}
	if int64(len(raw)) > transportCheckpointMaxFileBytes {
		return file, 0, ErrCheckpointCapacity
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return file, 0, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return file, 0, err
	}
	return file, int64(len(raw)), nil
}

func decodeTransportCheckpointManifest(raw []byte) (transportCheckpointManifest, error) {
	var manifest transportCheckpointManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return manifest, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func writeCheckpointManifest(path, generation string) error {
	raw, err := json.MarshalIndent(transportCheckpointManifest{Schema: transportCheckpointSchema, Generation: generation}, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode manifest: %v", ErrCheckpointPersistence, err)
	}
	raw = append(raw, '\n')
	if err := writeCheckpointFileAtomic(path, raw); err != nil {
		return fmt.Errorf("%w: write manifest: %v", ErrCheckpointPersistence, err)
	}
	return nil
}

func checkpointRecordsRoot(path string) string {
	return path + ".records"
}

func checkpointGenerationPath(path, generation string) string {
	return filepath.Join(checkpointRecordsRoot(path), generation)
}

func checkpointRecordFilename(recordID string) string {
	digest := sha256.Sum256([]byte("m365/wp6/transport-checkpoint/record-file/v1\x00" + recordID))
	return hex.EncodeToString(digest[:]) + ".json"
}

func checkpointRecordPath(path, generation, recordID string) string {
	return filepath.Join(checkpointGenerationPath(path, generation), checkpointRecordFilename(recordID))
}

func validCheckpointGeneration(generation string) bool {
	decoded, err := hex.DecodeString(generation)
	return err == nil && len(decoded) == 16
}

func ensureCheckpointRecordDirectories(path, generation string) error {
	root := checkpointRecordsRoot(path)
	if err := rejectCheckpointSymlink(root); err != nil {
		return err
	}
	if err := secureCheckpointDirectory(root); err != nil {
		return err
	}
	generationDir := checkpointGenerationPath(path, generation)
	if err := rejectCheckpointSymlink(generationDir); err != nil {
		return err
	}
	return secureCheckpointDirectory(generationDir)
}

func rejectCheckpointSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect checkpoint path: %v", ErrCheckpointPersistence, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: checkpoint persistence path must not be a symbolic link", ErrCheckpointPersistence)
	}
	return nil
}

func cleanupCheckpointGenerations(path, active string) {
	root := checkpointRecordsRoot(path)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == active {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
	_ = syncCheckpointDirectory(root)
}

func syncCheckpointDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func cloneCheckpointRecordBytes(source map[string]int64) map[string]int64 {
	if len(source) == 0 {
		return make(map[string]int64)
	}
	clone := make(map[string]int64, len(source))
	for id, size := range source {
		clone[id] = size
	}
	return clone
}

func (s *transportCheckpointStore) recomputeNextPruneAtLocked() {
	s.nextPruneAt = time.Time{}
	for _, record := range s.records {
		s.noteCheckpointExpiryLocked(record)
	}
}

func (s *transportCheckpointStore) noteCheckpointExpiryLocked(record *transportCheckpointRecord) {
	if record == nil {
		return
	}
	expires := record.UpdatedAt.Add(transportCheckpointTTL)
	if s.nextPruneAt.IsZero() || expires.Before(s.nextPruneAt) {
		s.nextPruneAt = expires
	}
}
