package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

type artifactBlobVerification struct {
	info         os.FileInfo
	size         int64
	modTime      time.Time
	changeMarker any
	sha256       string
}

func (s *artifactStore) rememberIngestedVerificationLocked(entry artifactIndexEntry) {
	info, err := os.Lstat(s.blobPath(entry.BlobID))
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return
	}
	s.verified[entry.BlobID] = artifactBlobVerification{
		info:         info,
		size:         info.Size(),
		modTime:      info.ModTime(),
		changeMarker: artifactFileChangeMarker(info),
		sha256:       entry.SHA256,
	}
}

func (s *artifactStore) openIntegrityCheckedBlob(entry artifactIndexEntry) (*os.File, error) {
	path := s.blobPath(entry.BlobID)
	file, info, err := openRegularArtifactFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: artifact blob unavailable", ErrArtifactCorrupt)
	}
	if info.Size() != entry.Size {
		file.Close()
		return nil, fmt.Errorf("%w: artifact size mismatch", ErrArtifactCorrupt)
	}

	s.mu.Lock()
	verification, cached := s.verified[entry.BlobID]
	cacheHit := cached && artifactVerificationMatches(verification, info, entry)
	if !cacheHit {
		s.fullVerificationCount++
	}
	s.mu.Unlock()
	if cacheHit {
		return file, nil
	}

	if err := verifyOpenedArtifactBlob(file, info, path, entry); err != nil {
		file.Close()
		return nil, err
	}
	verifiedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("%w: stat verified artifact", ErrArtifactCorrupt)
	}
	s.mu.Lock()
	if current, ok := s.entries[entry.TokenSHA256]; ok && current.BlobID == entry.BlobID && current.SHA256 == entry.SHA256 && current.Size == entry.Size {
		s.verified[entry.BlobID] = artifactBlobVerification{
			info:         verifiedInfo,
			size:         verifiedInfo.Size(),
			modTime:      verifiedInfo.ModTime(),
			changeMarker: artifactFileChangeMarker(verifiedInfo),
			sha256:       entry.SHA256,
		}
	}
	s.mu.Unlock()
	return file, nil
}

func verifyOpenedArtifactBlob(file *os.File, before os.FileInfo, path string, entry artifactIndexEntry) error {
	digest := sha256.New()
	count, err := io.Copy(digest, io.LimitReader(file, entry.Size+1))
	if err != nil || count != entry.Size || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("%w: artifact digest mismatch", ErrArtifactCorrupt)
	}
	afterFile, statErr := file.Stat()
	afterPath, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || afterPath.Mode()&os.ModeSymlink != 0 || !afterPath.Mode().IsRegular() || !os.SameFile(before, afterFile) || !os.SameFile(before, afterPath) || afterFile.Size() != before.Size() || !afterFile.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%w: artifact file changed during verification", ErrArtifactCorrupt)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("open artifact bytes: %w", err)
	}
	return nil
}

func artifactVerificationMatches(verification artifactBlobVerification, info os.FileInfo, entry artifactIndexEntry) bool {
	return verification.info != nil &&
		verification.sha256 == entry.SHA256 &&
		verification.size == entry.Size &&
		info.Size() == entry.Size &&
		verification.modTime.Equal(info.ModTime()) &&
		os.SameFile(verification.info, info) &&
		artifactChangeMarkerMatches(verification.changeMarker, info)
}

// artifactFileChangeMarker extracts the filesystem change timestamp when the
// platform exposes one (Ctim on Linux, Ctimespec on Darwin/BSD). Unlike access
// time, this changes when bytes or metadata change and cannot be restored by
// os.Chtimes, so it can safely strengthen the in-process verification cache
// without forcing a new full SHA after every normal read.
func artifactFileChangeMarker(info os.FileInfo) any {
	if info == nil || info.Sys() == nil {
		return nil
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return field.Interface()
		}
	}
	return nil
}

func artifactChangeMarkerMatches(previous any, info os.FileInfo) bool {
	current := artifactFileChangeMarker(info)
	if previous == nil || current == nil {
		return false
	}
	return reflect.DeepEqual(previous, current)
}

func artifactBlobMetadataUsableAtStartup(path string, entry artifactIndexEntry) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return false
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return false
	}
	after, err := os.Lstat(path)
	return err == nil && after.Mode()&os.ModeSymlink == 0 && after.Mode().IsRegular() && after.Size() == entry.Size && os.SameFile(info, after)
}

func (s *artifactStore) quarantineCorruptArtifact(entry artifactIndexEntry) {
	s.mu.Lock()
	current, ok := s.entries[entry.TokenSHA256]
	if !ok || current.BlobID != entry.BlobID || current.SHA256 != entry.SHA256 {
		s.mu.Unlock()
		return
	}
	delete(s.entries, entry.TokenSHA256)
	delete(s.verified, entry.BlobID)
	s.totalBytes -= entry.Size
	if err := s.persistLocked(); err != nil {
		s.entries[entry.TokenSHA256] = entry
		s.totalBytes += entry.Size
		s.mu.Unlock()
		return
	}
	s.scheduleLocked()
	s.mu.Unlock()
	_ = removeArtifactBlob(s.blobPath(entry.BlobID))
}

func (s *artifactStore) recoverStartupOrphansLocked() error {
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
	changed := false
	for _, entry := range entries {
		if _, ok := referenced[entry.Name()]; ok {
			continue
		}
		if _, ok := s.pending[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(s.blobDir, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove unsafe orphan artifact blob: %w", err)
			}
			changed = true
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect orphan artifact blob: %w", err)
		}
		if !info.Mode().IsRegular() {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove non-regular orphan artifact blob: %w", err)
			}
			changed = true
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphan artifact blob: %w", err)
		}
		changed = true
	}
	if changed {
		_ = syncArtifactDir(s.blobDir)
	}
	return nil
}
