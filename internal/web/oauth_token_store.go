package web

import "m365-native/internal/auth"

type storedOAuthAccount struct {
	Manifest auth.OAuthProfileManifest
	Account  managementAccountView
}

func (s *Server) storeOAuthTokenSet(profileID string, tokenSet auth.TokenSet) (storedOAuthAccount, *oauthCallbackFailure) {
	manifest, tokenStore, err := s.oauthStoreForProfile(profileID)
	if err != nil || tokenStore == nil {
		return storedOAuthAccount{}, &oauthCallbackFailure{
			Code:    "oauth_profile_store_failed",
			Message: "OAuth profile 的權杖儲存區無法使用，請重新開始授權",
		}
	}
	return s.storeOAuthTokenSetInStore(manifest, tokenStore, tokenSet)
}

func (s *Server) storeOAuthTokenSetInStore(manifest auth.OAuthProfileManifest, tokenStore *auth.Store, tokenSet auth.TokenSet) (storedOAuthAccount, *oauthCallbackFailure) {
	s.checkpointLifecycle.Lock()
	defer s.checkpointLifecycle.Unlock()
	active := sameTokenStore(tokenStore, s.activeTokenStore())
	var (
		account       auth.AccountToken
		tokenStoreErr error
		commitStarted bool
	)
	commit := func() error {
		commitStarted = true
		account, tokenStoreErr = tokenStore.Upsert(tokenSet)
		return tokenStoreErr
	}
	var err error
	if active && s.checkpoints != nil {
		err = s.checkpoints.ClearThen(commit)
	} else {
		err = commit()
	}
	if err != nil {
		if !commitStarted {
			return storedOAuthAccount{}, &oauthCallbackFailure{
				Code:    "transport_checkpoint_clear_failed",
				Message: "無法安全更新 Microsoft 帳號的聊天連線狀態",
			}
		}
		return storedOAuthAccount{}, &oauthCallbackFailure{
			Code:    "oauth_token_store_failed",
			Message: "無法安全儲存帳號權杖，請檢查資料目錄權限後重試",
		}
	}
	if active {
		// Profile-manager opens return independent Store objects for the same
		// private path. Keep the running server on the object just updated.
		s.setActiveTokenStore(tokenStore)
	}
	accountView := managementAccountView{
		Status:    account.Status,
		ExpiresAt: account.ExpiresAt,
		UpdatedAt: account.UpdatedAt,
	}
	if active {
		accountView, err = s.managementAccount(account)
		if err != nil {
			return storedOAuthAccount{}, &oauthCallbackFailure{
				Code:    "oauth_account_profile_failed",
				Message: "帳號已授權，但帳號設定檔參照無法建立",
			}
		}
	}
	return storedOAuthAccount{Manifest: manifest, Account: accountView}, nil
}

func sameTokenStore(left, right *auth.Store) bool {
	return left != nil && right != nil && (left == right || left.Path() == right.Path())
}
