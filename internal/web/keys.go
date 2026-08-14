package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"m365-native/internal/privatefile"
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
	file, err := privatefile.OpenRegular(path, "credential file")
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
	return privatefile.WriteAtomic(s.Path, "credential file", ".api-keys-*", raw)
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
