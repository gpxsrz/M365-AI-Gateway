package web

import (
	"errors"
	"m365-native/internal/auth"
	"time"
)

type managementAccountView struct {
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

func (s *Server) activeTokenStore() *auth.Store {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	return s.tokens
}

func (s *Server) setActiveTokenStore(store *auth.Store) {
	s.tokenMu.Lock()
	s.tokens = store
	s.tokenMu.Unlock()
}

func (s *Server) managementAccount(account auth.AccountToken) (managementAccountView, error) {
	return managementAccountView{
		Status:    account.Status,
		ExpiresAt: account.ExpiresAt,
		UpdatedAt: account.UpdatedAt,
	}, nil
}

func (s *Server) activeAccount() (auth.AccountToken, error) {
	store := s.activeTokenStore()
	if store == nil {
		return auth.AccountToken{}, errors.New("account token store is unavailable")
	}
	account, ok := store.First()
	if !ok {
		return auth.AccountToken{}, errors.New("no account; login first")
	}
	account, err := store.EnsureValid(account.ID)
	if err != nil {
		return auth.AccountToken{}, err
	}
	return account, nil
}
