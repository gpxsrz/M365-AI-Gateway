package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const apiKeyUsagePersistInterval = time.Minute

type apiKeyRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"hash"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Revoked    bool       `json:"revoked"`
}

type apiKeyStore struct {
	mu                      sync.Mutex
	Path                    string         `json:"-"`
	Keys                    []apiKeyRecord `json:"keys"`
	persist                 func(string, []byte) error
	lastUsagePersistAttempt time.Time
}

func apiKeyStorePath() string {
	if path := strings.TrimSpace(os.Getenv("M365_API_KEYS")); path != "" {
		return path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "m365-native", "api-keys.json")
}

func openAPIKeys() (*apiKeyStore, error) {
	path := apiKeyStorePath()
	store := &apiKeyStore{Path: path}
	file, err := openPrivateRegularFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open API key store: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read API key store: %w", err)
	}
	var persisted struct {
		Keys []apiKeyRecord `json:"keys"`
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return nil, fmt.Errorf("decode API key store: %w", err)
	}
	store.Keys = persisted.Keys
	return store, nil
}

func openPrivateRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("credential file must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credential file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("credential file identity changed while opening")
	}
	if opened.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, fmt.Errorf("secure credential file permissions: %w", err)
		}
	}
	return file, nil
}

func (s *apiKeyStore) save() error {
	raw, err := json.MarshalIndent(struct {
		Keys []apiKeyRecord `json:"keys"`
	}{Keys: s.Keys}, "", "  ")
	if err != nil {
		return err
	}
	if s.persist != nil {
		return s.persist(s.Path, raw)
	}
	return atomicWriteAPIKeyFile(s.Path, raw)
}

func atomicWriteAPIKeyFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("credential file must not be a symbolic link")
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("credential file must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".api-keys-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
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
	removeTemporary = false
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func keyHash(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

func (s *apiKeyStore) create(name string) (apiKeyRecord, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return apiKeyRecord{}, "", err
	}
	raw := "m365_" + hex.EncodeToString(bytes)
	record := apiKeyRecord{ID: hex.EncodeToString(bytes[:8]), Name: name, Prefix: raw[:12], Hash: keyHash(raw), CreatedAt: time.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Keys = append(s.Keys, record)
	if err := s.save(); err != nil {
		s.Keys = s.Keys[:len(s.Keys)-1]
		return apiKeyRecord{}, "", err
	}
	return record, raw, nil
}

func (s *apiKeyStore) list() []apiKeyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apiKeyRecord, len(s.Keys))
	copy(out, s.Keys)
	for i := range out {
		out[i].Hash = ""
	}
	return out
}

func (s *apiKeyStore) revoke(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Keys {
		if s.Keys[i].ID == id && !s.Keys[i].Revoked {
			s.Keys[i].Revoked = true
			if err := s.save(); err != nil {
				s.Keys[i].Revoked = false
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *apiKeyStore) authenticate(raw string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := keyHash(raw)
	for i := range s.Keys {
		if s.Keys[i].Hash != hash || s.Keys[i].Revoked {
			continue
		}
		now := time.Now()
		s.Keys[i].LastUsedAt = &now
		if s.lastUsagePersistAttempt.IsZero() || now.Sub(s.lastUsagePersistAttempt) >= apiKeyUsagePersistInterval {
			s.lastUsagePersistAttempt = now
			_ = s.save()
		}
		return s.Keys[i].ID, true
	}
	return "", false
}

func (s *apiKeyStore) valid(raw string) bool {
	_, ok := s.authenticate(raw)
	return ok
}
