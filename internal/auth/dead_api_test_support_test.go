package auth

import (
	"context"
	"os"
	"sort"
)

func OpenStore(path string) (*Store, error) {
	return openStore(path, CurrentOAuthConfig(), true)
}

func OpenStoreWithConfig(path string, config OAuthConfig) (*Store, error) {
	return openStore(path, config, true)
}

func (s *Store) List() []AccountToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AccountToken, len(s.data.Accounts))
	copy(out, s.data.Accounts)
	return out
}

func (s *Store) EnsureValid(id string) (AccountToken, error) {
	return s.EnsureValidContext(context.Background(), id)
}

func RefreshWithConfig(config OAuthConfig, refreshToken string) (TokenSet, error) {
	return refreshWithConfigContext(context.Background(), config, refreshToken)
}

func profileStatusForTest(manager *OAuthProfileManager) (OAuthProfileStatus, error) {
	var status OAuthProfileStatus
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(manager.root)
		if err != nil {
			return err
		}
		profiles := make([]OAuthProfileSummary, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() || !validOAuthProfileID(entry.Name()) {
				continue
			}
			manifest, err := manager.readManifestLocked(entry.Name())
			if err != nil {
				return err
			}
			profiles = append(profiles, OAuthProfileSummary{
				ProfileID:  manifest.ProfileID,
				Kind:       manifest.Kind,
				Validation: manifest.Validation,
				Active:     manifest.ProfileID == pointer.ActiveProfileID,
				Previous:   manifest.ProfileID == pointer.PreviousProfileID,
			})
		}
		sort.Slice(profiles, func(i, j int) bool { return profiles[i].ProfileID < profiles[j].ProfileID })
		status = OAuthProfileStatus{
			Schema:            oauthProfileStatusSchema,
			ActiveProfileID:   pointer.ActiveProfileID,
			PreviousProfileID: pointer.PreviousProfileID,
			Generation:        pointer.Generation,
			Profiles:          profiles,
		}
		return nil
	})
	return status, err
}

func readPointerForTest(manager *OAuthProfileManager) (OAuthActiveProfilePointer, error) {
	var pointer OAuthActiveProfilePointer
	err := manager.withLock(func() error {
		var err error
		pointer, err = manager.readPointerLocked()
		return err
	})
	return pointer, err
}
