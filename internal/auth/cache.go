package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AccountToken struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"displayName,omitempty"`
	Status       string    `json:"status"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	OID          string    `json:"oid,omitempty"`
	TID          string    `json:"tid,omitempty"`
	ClientID     string    `json:"clientId,omitempty"`
}

const TokenCacheSchema = "m365-oauth-token-cache/v1"

type Cache struct {
	Schema   string         `json:"schema,omitempty"`
	Accounts []AccountToken `json:"accounts"`
}

type Store struct {
	mu             sync.Mutex
	resourceMu     sync.Mutex
	path           string
	config         OAuthConfig
	data           Cache
	resourceTokens map[string]resourceAccessToken
}

func CachePath() string {
	if dir := os.Getenv("M365_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "accounts.json")
	}
	if p := os.Getenv("M365_CONFIG"); p != "" {
		return p
	}
	if p := os.Getenv("M365_TOKEN_CACHE"); p != "" {
		return p
	}
	if p := os.Getenv("M365_TOKEN_FILE"); p != "" {
		return p
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return filepath.Join(".", ".config", "m365-native", "accounts.json")
	}
	return filepath.Join(h, ".config", "m365-native", "accounts.json")
}

func OpenStore(path string) (*Store, error) {
	return openStore(path, CurrentOAuthConfig(), true)
}

func OpenStoreWithConfig(path string, config OAuthConfig) (*Store, error) {
	return openStore(path, config, true)
}

func openStore(path string, config OAuthConfig, allowLegacySchema bool) (*Store, error) {
	if path == "" {
		path = CachePath()
	}
	config, err := normalizeOAuthConfig(config)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, config: config, data: Cache{Schema: TokenCacheSchema, Accounts: []AccountToken{}}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure token cache permissions: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Schema == "" {
		if !allowLegacySchema {
			return nil, errors.New("token cache schema is required")
		}
		s.data.Schema = TokenCacheSchema
	}
	if s.data.Schema != TokenCacheSchema {
		return nil, fmt.Errorf("unsupported token cache schema %q", s.data.Schema)
	}
	if s.data.Accounts == nil {
		s.data.Accounts = []AccountToken{}
	}
	// Normalize oid/tid for older cache entries.
	for i := range s.data.Accounts {
		a := &s.data.Accounts[i]
		if a.OID == "" {
			a.OID = a.ID
		}
		if a.ID == "" {
			a.ID = a.OID
		}
	}
	if len(s.data.Accounts) > 1 {
		active := s.data.Accounts[0]
		for _, candidate := range s.data.Accounts[1:] {
			if candidate.UpdatedAt.After(active.UpdatedAt) || (candidate.UpdatedAt.Equal(active.UpdatedAt) && candidate.ID > active.ID) {
				active = candidate
			}
		}
		s.data.Accounts = []AccountToken{active}
		if err := s.saveLocked(); err != nil {
			return nil, fmt.Errorf("migrate token cache to single account: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Config() OAuthConfig {
	return s.config
}

func (s *Store) saveLocked() error {
	s.data.Schema = TokenCacheSchema
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(s.path, b)
}

func (s *Store) initialize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func atomicWritePrivateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".m365-private-*")
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
	return nil
}

func (s *Store) List() []AccountToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AccountToken, len(s.data.Accounts))
	copy(out, s.data.Accounts)
	return out
}

func (s *Store) Upsert(tok TokenSet) (AccountToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := tok.HomeOID
	if id == "" {
		id = tok.Email
	}
	if id == "" {
		id = "account-" + time.Now().Format("150405")
	}
	acc := AccountToken{
		ID:           id,
		Email:        tok.Email,
		DisplayName:  tok.DisplayName,
		Status:       "online",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt,
		UpdatedAt:    time.Now(),
		OID:          firstNonEmpty(tok.HomeOID, id),
		TID:          tok.TenantID,
		ClientID:     s.config.ClientID,
	}
	for _, existing := range s.data.Accounts {
		if existing.ID == acc.ID || (acc.Email != "" && existing.Email == acc.Email) {
			if acc.RefreshToken == "" {
				acc.RefreshToken = existing.RefreshToken
			}
			if acc.TID == "" {
				acc.TID = existing.TID
			}
			if acc.OID == "" {
				acc.OID = existing.OID
			}
			break
		}
	}
	s.data.Accounts = []AccountToken{acc}
	return acc, s.saveLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data.Accounts[:0]
	for _, a := range s.data.Accounts {
		if a.ID != id {
			next = append(next, a)
		}
	}
	s.data.Accounts = next
	return s.saveLocked()
}

func (s *Store) Get(id string) (AccountToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.data.Accounts {
		if a.ID == id || a.OID == id || a.Email == id {
			return a, true
		}
	}
	return AccountToken{}, false
}

func (s *Store) First() (AccountToken, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Accounts) == 0 {
		return AccountToken{}, false
	}
	return s.data.Accounts[0], true
}

func (s *Store) EnsureValid(id string) (AccountToken, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	acc, ok := s.Get(id)
	if !ok {
		return AccountToken{}, os.ErrNotExist
	}
	if time.Now().Before(acc.ExpiresAt.Add(-30 * time.Second)) {
		return acc, nil
	}
	if acc.RefreshToken == "" {
		acc.Status = "expired"
		s.mu.Lock()
		for i, a := range s.data.Accounts {
			if a.ID == acc.ID {
				s.data.Accounts[i] = acc
				_ = s.saveLocked()
				break
			}
		}
		s.mu.Unlock()
		return acc, fmtExpired()
	}
	tok, err := RefreshWithConfig(s.config, acc.RefreshToken)
	if err != nil {
		acc.Status = "expired"
		s.mu.Lock()
		for i, a := range s.data.Accounts {
			if a.ID == acc.ID {
				s.data.Accounts[i] = acc
				_ = s.saveLocked()
				break
			}
		}
		s.mu.Unlock()
		return acc, err
	}
	if tok.Email == "" {
		tok.Email = acc.Email
	}
	if tok.DisplayName == "" {
		tok.DisplayName = acc.DisplayName
	}
	if tok.HomeOID == "" {
		tok.HomeOID = firstNonEmpty(acc.OID, acc.ID)
	}
	if tok.TenantID == "" {
		tok.TenantID = acc.TID
	}
	return s.Upsert(tok)
}

func fmtExpired() error {
	return errors.New("token_expired: refresh token missing or expired")
}
