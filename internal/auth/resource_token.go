package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxResourceTokenEntries = 8
	maxResourceScopeBytes   = 4096
)

type resourceAccessToken struct {
	AccessToken      string
	ExpiresAt        time.Time
	LastUsed         time.Time
	AccountID        string
	AccountUpdatedAt time.Time
}

// ResourceAccessToken obtains an access token for a fixed Microsoft resource
// scope without replacing the Store's primary ChatHub access token.
func (s *Store) ResourceAccessToken(ctx context.Context, scope string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope, err := normalizeResourceScope(scope)
	if err != nil {
		return "", err
	}

	// Resource refreshes share one persisted refresh token. Serializing this
	// small path coalesces same-scope callers and prevents rotated-token races.
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	account, ok := s.First()
	if !ok {
		return "", errors.New("resource token: active account unavailable")
	}
	if strings.TrimSpace(account.RefreshToken) == "" {
		return "", errors.New("resource token: refresh credential unavailable")
	}
	now := time.Now()
	if cached, ok := s.resourceTokens[scope]; ok &&
		cached.AccountID == account.ID &&
		cached.AccountUpdatedAt.Equal(account.UpdatedAt) &&
		now.Before(cached.ExpiresAt.Add(-30*time.Second)) {
		cached.LastUsed = now
		s.resourceTokens[scope] = cached
		return cached.AccessToken, nil
	}

	config := s.config
	config.Scope = scope
	token, err := refreshWithConfigContext(ctx, config, account.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("resource token refresh failed: %w", err)
	}
	if err := s.acceptResourceRefresh(account, token.RefreshToken); err != nil {
		return "", err
	}
	if s.resourceTokens == nil {
		s.resourceTokens = make(map[string]resourceAccessToken)
	}
	if _, exists := s.resourceTokens[scope]; !exists && len(s.resourceTokens) >= maxResourceTokenEntries {
		s.evictResourceToken()
	}
	s.resourceTokens[scope] = resourceAccessToken{
		AccessToken:      token.AccessToken,
		ExpiresAt:        token.ExpiresAt,
		LastUsed:         now,
		AccountID:        account.ID,
		AccountUpdatedAt: account.UpdatedAt,
	}
	return token.AccessToken, nil
}

// InvalidateResourceAccessToken removes a cached token only when it is still
// the token rejected by Microsoft. This avoids deleting a concurrent refresh.
func (s *Store) InvalidateResourceAccessToken(scope, rejectedToken string) {
	scope, err := normalizeResourceScope(scope)
	if err != nil || rejectedToken == "" {
		return
	}
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if cached, ok := s.resourceTokens[scope]; ok && cached.AccessToken == rejectedToken {
		delete(s.resourceTokens, scope)
	}
}

func normalizeResourceScope(scope string) (string, error) {
	scope = strings.Join(strings.Fields(scope), " ")
	if scope == "" {
		return "", errors.New("resource token: scope is required")
	}
	if len(scope) > maxResourceScopeBytes {
		return "", errors.New("resource token: scope is too large")
	}
	return scope, nil
}

func (s *Store) acceptResourceRefresh(account AccountToken, rotatedRefreshToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Accounts) != 1 {
		return errors.New("resource token: active account changed during refresh")
	}
	current := &s.data.Accounts[0]
	if current.ID != account.ID || !current.UpdatedAt.Equal(account.UpdatedAt) || current.RefreshToken != account.RefreshToken {
		return errors.New("resource token: active account changed during refresh")
	}
	if rotatedRefreshToken == "" || rotatedRefreshToken == current.RefreshToken {
		return nil
	}
	previous := current.RefreshToken
	current.RefreshToken = rotatedRefreshToken
	if err := s.saveLocked(); err != nil {
		current.RefreshToken = previous
		return fmt.Errorf("persist rotated resource refresh credential: %w", err)
	}
	return nil
}

func (s *Store) evictResourceToken() {
	var (
		oldestScope string
		oldest      time.Time
	)
	for scope, token := range s.resourceTokens {
		if oldestScope == "" || token.LastUsed.Before(oldest) {
			oldestScope = scope
			oldest = token.LastUsed
		}
	}
	delete(s.resourceTokens, oldestScope)
}
