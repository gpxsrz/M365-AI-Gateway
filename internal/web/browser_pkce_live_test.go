package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m365-native/internal/auth"
	"m365-native/internal/chathub"
)

func TestWP5LiveEgoBrowserPKCEChatHub(t *testing.T) {
	if os.Getenv("WP5_LIVE_EGO") != "1" {
		t.Skip("live Microsoft validation is opt-in")
	}
	if strings.TrimSpace(os.Getenv("M365_EGO_BROWSER_TASK_SPACE")) == "" {
		t.Fatal("M365_EGO_BROWSER_TASK_SPACE is required for live attach validation")
	}
	tokenPath := strings.TrimSpace(os.Getenv("M365_WP5_LIVE_TOKEN_CACHE"))
	if tokenPath == "" {
		t.Fatal("M365_WP5_LIVE_TOKEN_CACHE is required for isolated live validation")
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal("LIVE_DATA_DIR=FAIL")
	}
	if err := os.Chmod(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal("LIVE_DATA_DIR_PERMISSIONS=FAIL")
	}

	manager, err := auth.OpenOAuthProfileManager(tokenPath, auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal("OAUTH_PROFILE_MANAGER=FAIL")
	}
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal("ACTIVE_STORE=FAIL")
	}
	beforeStatus, err := manager.Status()
	if err != nil {
		t.Fatal("ACTIVE_POINTER_PREFLIGHT=FAIL")
	}
	beforeBytes, beforeExists, err := readLiveOptionalFile(activeStore.Path())
	if err != nil {
		t.Fatal("ACTIVE_STORE_PREFLIGHT=FAIL")
	}

	server := &Server{
		tokens:        activeStore,
		oauthProfiles: manager,
		pkce:          map[string]pendingPKCE{},
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/browser/default/start", strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	server.startDefaultClientBrowserPKCE(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("BROWSER_PKCE_START=FAIL status=%d", recorder.Code)
	}
	var started struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&started); err != nil || strings.TrimSpace(started.State) == "" {
		t.Fatal("BROWSER_PKCE_START_RESPONSE=FAIL")
	}

	deadline := time.Now().Add(pkceTransactionTTL + 30*time.Second)
	var transaction pendingPKCE
	for time.Now().Before(deadline) {
		candidate, ok := server.peekPKCE(started.State)
		if ok {
			transaction = candidate
			switch candidate.Status {
			case "authenticated":
				goto authenticated
			case "error", "expired", "cancelled":
				t.Fatalf("BROWSER_PKCE_CAPTURE=FAIL code=%s", candidate.ErrorCode)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("BROWSER_PKCE_CAPTURE=FAIL code=timeout")

authenticated:
	t.Log("BROWSER_PKCE_CAPTURE=PASS")
	t.Log("TOKEN_EXCHANGE=PASS")
	if !transaction.Staged || strings.TrimSpace(transaction.ProfileID) == "" {
		t.Fatal("STAGED_PROFILE=FAIL")
	}

	afterStatus, err := manager.Status()
	if err != nil {
		t.Fatal("ACTIVE_POINTER_READBACK=FAIL")
	}
	if afterStatus.ActiveProfileID != beforeStatus.ActiveProfileID || afterStatus.Generation != beforeStatus.Generation {
		t.Fatal("ACTIVE_POINTER_UNCHANGED=FAIL")
	}
	afterBytes, afterExists, err := readLiveOptionalFile(activeStore.Path())
	if err != nil {
		t.Fatal("ACTIVE_STORE_READBACK=FAIL")
	}
	if beforeExists != afterExists || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("ACTIVE_STORE_UNCHANGED=FAIL")
	}
	t.Log("ACTIVE_PROFILE_UNCHANGED=PASS")

	_, stagedStore, err := manager.OpenStore(transaction.ProfileID)
	if err != nil {
		t.Fatal("STAGED_STORE=FAIL")
	}
	account, ok := stagedStore.First()
	if !ok || strings.TrimSpace(account.AccessToken) == "" || account.Status != "online" {
		t.Fatal("STAGED_ACCOUNT_ONLINE=FAIL")
	}
	t.Log("STAGED_ACCOUNT_ONLINE=PASS")

	if account.OID == "" || account.TID == "" {
		oid, tid := extractOIDTID(account.AccessToken)
		account.OID = oid
		account.TID = tid
	}
	if account.OID == "" || account.TID == "" {
		_ = manager.Discard(transaction.ProfileID)
		t.Fatal("STAGED_ACCOUNT_IDENTITY=FAIL")
	}
	chatCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := chathub.NewClient()
	_, err = client.Chat(chatCtx, chathub.Account{
		AccessToken: account.AccessToken,
		OID:         account.OID,
		TID:         account.TID,
	}, chathub.Request{
		Text: "Reply with OK only.",
		Tone: "magic",
	})
	if err != nil {
		_ = manager.Discard(transaction.ProfileID)
		t.Fatal("CHATHUB_WS_AND_REQUEST=FAIL")
	}
	if _, err := manager.RecordValidation(transaction.ProfileID, auth.OAuthProfileValidationChatHub); err != nil {
		_ = manager.Discard(transaction.ProfileID)
		t.Fatal("CHATHUB_VALIDATION_RECORD=FAIL")
	}
	t.Log("CHATHUB_WS_UPGRADE=PASS")
	t.Log("CHATHUB_MINIMAL_REQUEST=PASS")
}

func TestWP5LiveEgoBrowserLifecycleContinuation(t *testing.T) {
	if os.Getenv("WP5_LIVE_EGO") != "1" {
		t.Skip("live Microsoft validation is opt-in")
	}
	if strings.TrimSpace(os.Getenv("M365_EGO_BROWSER_TASK_SPACE")) == "" {
		t.Fatal("M365_EGO_BROWSER_TASK_SPACE is required for live attach validation")
	}
	tokenPath := strings.TrimSpace(os.Getenv("M365_WP5_LIVE_TOKEN_CACHE"))
	if tokenPath == "" {
		t.Fatal("M365_WP5_LIVE_TOKEN_CACHE is required for isolated live validation")
	}

	manager, err := auth.OpenOAuthProfileManager(tokenPath, auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal("LIFECYCLE_PROFILE_MANAGER=FAIL")
	}
	beforeStatus, err := manager.Status()
	if err != nil {
		t.Fatal("LIFECYCLE_POINTER_PREFLIGHT=FAIL")
	}
	profileID := liveValidatedStagedProfileID(t, beforeStatus)
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal("LIFECYCLE_ACTIVE_STORE=FAIL")
	}
	activeBefore, activeBeforeExists, err := readLiveOptionalFile(activeStore.Path())
	if err != nil {
		t.Fatal("LIFECYCLE_ACTIVE_STORE_PREFLIGHT=FAIL")
	}
	manifest, stagedStore, err := manager.OpenStore(profileID)
	if err != nil {
		t.Fatal("LIFECYCLE_STAGED_STORE=FAIL")
	}
	account, ok := stagedStore.First()
	if !ok || strings.TrimSpace(account.RefreshToken) == "" {
		t.Fatal("REFRESH_PREFLIGHT=FAIL")
	}

	_, err = stagedStore.Upsert(auth.TokenSet{
		AccessToken:  account.AccessToken,
		RefreshToken: account.RefreshToken,
		ExpiresAt:    time.Now().Add(-time.Minute),
		Email:        account.Email,
		DisplayName:  account.DisplayName,
		HomeOID:      firstNonEmpty(account.OID, account.ID),
		TenantID:     account.TID,
	})
	if err != nil {
		t.Fatal("REFRESH_EXPIRY_SETUP=FAIL")
	}
	refreshed, err := stagedStore.EnsureValid(account.ID)
	if err != nil || refreshed.Status != "online" || !refreshed.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		t.Fatal("REFRESH_SUCCESS=FAIL")
	}
	if _, err := manager.RecordValidation(profileID, auth.OAuthProfileValidationRefresh); err != nil {
		t.Fatal("REFRESH_VALIDATION_RECORD=FAIL")
	}
	assertLiveActiveUnchanged(t, manager, beforeStatus, activeStore.Path(), activeBefore, activeBeforeExists)
	t.Log("REFRESH_SUCCESS=PASS")

	preRestartManifest, preRestartStore, err := manager.OpenStore(profileID)
	if err != nil {
		t.Fatal("RESTART_PREFLIGHT_STAGED_STORE=FAIL")
	}
	preRestartTokenBytes, preRestartTokenExists, err := readLiveOptionalFile(preRestartStore.Path())
	if err != nil || !preRestartTokenExists {
		t.Fatal("RESTART_PREFLIGHT_TOKEN_STORE=FAIL")
	}
	preRestartStatus, err := manager.Status()
	if err != nil {
		t.Fatal("RESTART_PREFLIGHT_POINTER=FAIL")
	}

	reopened, err := auth.OpenOAuthProfileManager(tokenPath, auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal("RESTART_PROFILE_MANAGER=FAIL")
	}
	reopenedStatus, err := reopened.Status()
	if err != nil || reopenedStatus.ActiveProfileID != preRestartStatus.ActiveProfileID || reopenedStatus.PreviousProfileID != preRestartStatus.PreviousProfileID || reopenedStatus.Generation != preRestartStatus.Generation {
		t.Fatal("RESTART_POINTER_PERSISTENCE=FAIL")
	}
	reopenedManifest, reopenedStore, err := reopened.OpenStore(profileID)
	if err != nil {
		t.Fatal("RESTART_STAGED_STORE=FAIL")
	}
	if reopenedManifest.Schema != preRestartManifest.Schema || reopenedManifest.ProfileID != preRestartManifest.ProfileID || reopenedManifest.Kind != preRestartManifest.Kind || reopenedManifest.TokenCacheSchema != preRestartManifest.TokenCacheSchema || reopenedManifest.OAuth != preRestartManifest.OAuth || reopenedManifest.Validation != preRestartManifest.Validation || !reopenedManifest.CreatedAt.Equal(preRestartManifest.CreatedAt) || !reopenedManifest.UpdatedAt.Equal(preRestartManifest.UpdatedAt) {
		t.Fatal("RESTART_MANIFEST_PERSISTENCE=FAIL")
	}
	reopenedTokenBytes, reopenedTokenExists, err := readLiveOptionalFile(reopenedStore.Path())
	if err != nil || reopenedTokenExists != preRestartTokenExists || !bytes.Equal(reopenedTokenBytes, preRestartTokenBytes) {
		t.Fatal("RESTART_TOKEN_STORE_PERSISTENCE=FAIL")
	}
	persisted, ok := reopenedStore.First()
	if !ok || persisted.Status != "online" {
		t.Fatal("RESTART_ACCOUNT_PERSISTENCE=FAIL")
	}
	if _, err := reopened.RecordValidation(profileID, auth.OAuthProfileValidationRestart); err != nil {
		t.Fatal("RESTART_VALIDATION_RECORD=FAIL")
	}
	assertLiveActiveUnchanged(t, reopened, beforeStatus, activeStore.Path(), activeBefore, activeBeforeExists)
	t.Log("RESTART_PERSISTENCE=PASS")

	_, reopenedActiveStore, err := reopened.ActiveStore()
	if err != nil {
		t.Fatal("REMOVAL_ACTIVE_STORE=FAIL")
	}
	server := &Server{
		tokens:        reopenedActiveStore,
		oauthProfiles: reopened,
		pkce:          map[string]pendingPKCE{},
	}
	_, removedStore, err := reopened.OpenStore(profileID)
	if err != nil {
		t.Fatal("ACCOUNT_REMOVAL_STORE=FAIL")
	}
	removedAccount, ok := removedStore.First()
	if !ok || removedStore.Delete(removedAccount.ID) != nil {
		t.Fatal("ACCOUNT_REMOVAL=FAIL")
	}
	if len(removedStore.List()) != 0 {
		t.Fatal("ACCOUNT_REMOVAL_READBACK=FAIL")
	}
	if _, err := reopened.RecordValidation(profileID, auth.OAuthProfileValidationRemoval); err != nil {
		t.Fatal("REMOVAL_VALIDATION_RECORD=FAIL")
	}
	assertLiveActiveUnchanged(t, reopened, beforeStatus, reopenedActiveStore.Path(), activeBefore, activeBeforeExists)
	t.Log("ACCOUNT_REMOVAL=PASS")

	reauthManifest, reauthStore, err := reopened.OpenStore(profileID)
	if err != nil {
		t.Fatal("REMOVAL_REAUTH_STORE=FAIL")
	}
	target := pkceProfileTarget{
		ProfileID: reauthManifest.ProfileID,
		Kind:      reauthManifest.Kind,
		Staged:    true,
		Created:   false,
		OAuth:     reauthManifest.OAuth,
		Store:     reauthStore,
	}
	started, failure := server.beginPKCEForTarget(target)
	if failure != nil {
		t.Fatal("REMOVAL_REAUTH_START=FAIL")
	}
	captureCtx, cancel := context.WithTimeout(context.Background(), pkceTransactionTTL)
	captured, err := runBrowserPKCECapture(captureCtx, browserPKCECaptureRequest{
		AuthorizationURL: started.AuthorizationURL,
		RedirectURI:      target.OAuth.RedirectURI,
		State:            started.State,
		ProfileDir:       browserPKCEProfileDir(server.tokens.Path()),
	})
	cancel()
	if err != nil {
		t.Fatal("REMOVAL_REAUTH_CAPTURE=FAIL")
	}
	if failure := server.completeCapturedPKCEAuthorization(started.State, captured); failure != nil {
		t.Fatalf("REMOVAL_REAUTH_EXCHANGE=FAIL code=%s", failure.Code)
	}
	reauthedTransaction, ok := server.peekPKCE(started.State)
	if !ok || reauthedTransaction.Status != "authenticated" {
		t.Fatal("REMOVAL_REAUTH_STATUS=FAIL")
	}
	_, reauthedStore, err := reopened.OpenStore(profileID)
	if err != nil {
		t.Fatal("REMOVAL_REAUTH_READBACK=FAIL")
	}
	if reauthed, ok := reauthedStore.First(); !ok || reauthed.Status != "online" {
		t.Fatal("REMOVAL_REAUTH_ACCOUNT=FAIL")
	}
	assertLiveActiveUnchanged(t, reopened, beforeStatus, reopenedActiveStore.Path(), activeBefore, activeBeforeExists)
	t.Log("ACCOUNT_REMOVAL_REAUTH=PASS")

	completedManifest, completedStore, err := reopened.OpenStore(profileID)
	if err != nil || !completedManifest.Validation.Complete() {
		t.Fatal("PROMOTION_VALIDATION_PREFLIGHT=FAIL")
	}
	stagedBeforePromotion, stagedBeforeExists, err := readLiveOptionalFile(completedStore.Path())
	if err != nil || !stagedBeforeExists {
		t.Fatal("PROMOTION_STAGED_STORE_PREFLIGHT=FAIL")
	}
	promotionBefore, err := reopened.Status()
	if err != nil {
		t.Fatal("PROMOTION_POINTER_PREFLIGHT=FAIL")
	}
	promoted, err := reopened.Promote(profileID)
	if err != nil {
		t.Fatal("PROMOTION=FAIL")
	}
	if promoted.ActiveProfileID != profileID || promoted.PreviousProfileID != promotionBefore.ActiveProfileID || promoted.Generation != promotionBefore.Generation+1 {
		t.Fatal("PROMOTION_POINTER_READBACK=FAIL")
	}
	_, oldActiveAfterPromotion, err := reopened.OpenStore(promotionBefore.ActiveProfileID)
	if err != nil {
		t.Fatal("PROMOTION_OLD_ACTIVE_STORE=FAIL")
	}
	oldActiveBytes, oldActiveExists, err := readLiveOptionalFile(oldActiveAfterPromotion.Path())
	if err != nil || oldActiveExists != activeBeforeExists || !bytes.Equal(oldActiveBytes, activeBefore) {
		t.Fatal("PROMOTION_OLD_ACTIVE_BYTES=FAIL")
	}
	_, stagedAfterPromotion, err := reopened.OpenStore(profileID)
	if err != nil {
		t.Fatal("PROMOTION_STAGED_STORE=FAIL")
	}
	stagedAfterBytes, stagedAfterExists, err := readLiveOptionalFile(stagedAfterPromotion.Path())
	if err != nil || stagedAfterExists != stagedBeforeExists || !bytes.Equal(stagedAfterBytes, stagedBeforePromotion) {
		t.Fatal("PROMOTION_STAGED_BYTES=FAIL")
	}
	t.Log("PROMOTION=PASS")

	promotedRestart, err := auth.OpenOAuthProfileManager(tokenPath, auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal("PROMOTED_RESTART_MANAGER=FAIL")
	}
	promotedRestartStatus, err := promotedRestart.Status()
	if err != nil || promotedRestartStatus.ActiveProfileID != profileID || promotedRestartStatus.Generation != promoted.Generation {
		t.Fatal("PROMOTED_RESTART_POINTER=FAIL")
	}
	t.Log("PROMOTED_RESTART_PERSISTENCE=PASS")

	rolledBack, err := promotedRestart.Rollback()
	if err != nil {
		t.Fatal("ROLLBACK=FAIL")
	}
	if rolledBack.ActiveProfileID != promotionBefore.ActiveProfileID || rolledBack.PreviousProfileID != profileID || rolledBack.Generation != promoted.Generation+1 {
		t.Fatal("ROLLBACK_POINTER_READBACK=FAIL")
	}
	_, rollbackOldStore, err := promotedRestart.OpenStore(promotionBefore.ActiveProfileID)
	if err != nil {
		t.Fatal("ROLLBACK_OLD_STORE=FAIL")
	}
	rollbackOldBytes, rollbackOldExists, err := readLiveOptionalFile(rollbackOldStore.Path())
	if err != nil || rollbackOldExists != activeBeforeExists || !bytes.Equal(rollbackOldBytes, activeBefore) {
		t.Fatal("ROLLBACK_OLD_BYTES=FAIL")
	}
	_, rollbackStagedStore, err := promotedRestart.OpenStore(profileID)
	if err != nil {
		t.Fatal("ROLLBACK_STAGED_STORE=FAIL")
	}
	rollbackStagedBytes, rollbackStagedExists, err := readLiveOptionalFile(rollbackStagedStore.Path())
	if err != nil || rollbackStagedExists != stagedBeforeExists || !bytes.Equal(rollbackStagedBytes, stagedBeforePromotion) {
		t.Fatal("ROLLBACK_STAGED_BYTES=FAIL")
	}
	t.Log("ROLLBACK=PASS")
	_ = manifest
}

func TestWP5LiveEgoBrowserNegativeAndRefreshRecovery(t *testing.T) {
	if os.Getenv("WP5_LIVE_EGO") != "1" {
		t.Skip("live Microsoft validation is opt-in")
	}
	if strings.TrimSpace(os.Getenv("M365_EGO_BROWSER_TASK_SPACE")) == "" {
		t.Fatal("M365_EGO_BROWSER_TASK_SPACE is required for live attach validation")
	}
	tokenPath := strings.TrimSpace(os.Getenv("M365_WP5_LIVE_TOKEN_CACHE"))
	if tokenPath == "" {
		t.Fatal("M365_WP5_LIVE_TOKEN_CACHE is required for isolated live validation")
	}
	manager, err := auth.OpenOAuthProfileManager(tokenPath, auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal("NEGATIVE_PROFILE_MANAGER=FAIL")
	}
	beforeStatus, err := manager.Status()
	if err != nil {
		t.Fatal("NEGATIVE_POINTER_PREFLIGHT=FAIL")
	}
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal("NEGATIVE_ACTIVE_STORE=FAIL")
	}
	activeBefore, activeBeforeExists, err := readLiveOptionalFile(activeStore.Path())
	if err != nil {
		t.Fatal("NEGATIVE_ACTIVE_STORE_PREFLIGHT=FAIL")
	}
	server := &Server{
		tokens:        activeStore,
		oauthProfiles: manager,
		pkce:          map[string]pendingPKCE{},
	}

	failedRefresh := liveStartAttachedCandidate(t, server)
	_, failedStore, err := manager.OpenStore(failedRefresh.ProfileID)
	if err != nil {
		t.Fatal("REFRESH_FAILURE_STORE=FAIL")
	}
	failedAccount, ok := failedStore.First()
	if !ok || strings.TrimSpace(failedAccount.RefreshToken) == "" {
		t.Fatal("REFRESH_FAILURE_ACCOUNT=FAIL")
	}
	_, err = failedStore.Upsert(auth.TokenSet{
		AccessToken:  failedAccount.AccessToken,
		RefreshToken: "invalid-refresh-token-for-live-negative-test",
		ExpiresAt:    time.Now().Add(-time.Minute),
		Email:        failedAccount.Email,
		DisplayName:  failedAccount.DisplayName,
		HomeOID:      firstNonEmpty(failedAccount.OID, failedAccount.ID),
		TenantID:     failedAccount.TID,
	})
	if err != nil {
		t.Fatal("REFRESH_FAILURE_SETUP=FAIL")
	}
	if _, err := failedStore.EnsureValid(failedAccount.ID); err == nil {
		t.Fatal("REFRESH_FAILURE_CODE=FAIL")
	}
	if err := manager.Discard(failedRefresh.ProfileID); err != nil {
		t.Fatal("REFRESH_FAILURE_DISCARD=FAIL")
	}
	if _, _, err := manager.OpenStore(failedRefresh.ProfileID); !os.IsNotExist(err) {
		t.Fatal("REFRESH_FAILURE_DISCARD=FAIL")
	}
	assertLiveActiveUnchanged(t, manager, beforeStatus, activeStore.Path(), activeBefore, activeBeforeExists)
	t.Log("REFRESH_FAILURE_MICROSOFT_ENDPOINT=PASS")

	recovery := liveStartAttachedCandidate(t, server)
	_, recoveryStore, err := manager.OpenStore(recovery.ProfileID)
	if err != nil {
		t.Fatal("REFRESH_RECOVERY_STORE=FAIL")
	}
	if recovered, ok := recoveryStore.First(); !ok || recovered.Status != "online" {
		t.Fatal("REFRESH_RECOVERY_ACCOUNT=FAIL")
	}
	assertLiveActiveUnchanged(t, manager, beforeStatus, activeStore.Path(), activeBefore, activeBeforeExists)
	t.Log("REFRESH_RECOVERY_BROWSER_PKCE=PASS")
	if err := manager.Discard(recovery.ProfileID); err != nil {
		t.Fatal("REFRESH_RECOVERY_CLEANUP=FAIL")
	}

	cancelStarted, cancelTarget := liveBeginNegativePKCE(t, server, manager)
	cancelFailure := server.completeCapturedPKCEAuthorization(cancelStarted.State, browserPKCECapturedAuthorization{
		State: cancelStarted.State,
		Error: "access_denied",
	})
	if cancelFailure == nil || cancelFailure.Code != "oauth_authorization_cancelled" {
		t.Fatal("CALLBACK_CANCEL_STATE_MACHINE=FAIL")
	}
	if _, _, err := manager.OpenStore(cancelTarget.ProfileID); !os.IsNotExist(err) {
		t.Fatal("CALLBACK_CANCEL_CLEANUP=FAIL")
	}
	replayFailure := server.completeCapturedPKCEAuthorization(cancelStarted.State, browserPKCECapturedAuthorization{
		State: cancelStarted.State,
		Error: "access_denied",
	})
	if replayFailure == nil || replayFailure.Code != "oauth_state_replayed" {
		t.Fatal("CALLBACK_REPLAY_STATE_MACHINE=FAIL")
	}
	t.Log("CALLBACK_CANCEL_STATE_MACHINE=PASS")
	t.Log("CALLBACK_REPLAY_STATE_MACHINE=PASS")

	mismatchStarted, mismatchTarget := liveBeginNegativePKCE(t, server, manager)
	mismatchFailure := server.completeCapturedPKCEAuthorization(mismatchStarted.State, browserPKCECapturedAuthorization{
		Code:  "unused-negative-test-code",
		State: "mismatched-state",
	})
	if mismatchFailure == nil || mismatchFailure.Code != "oauth_state_mismatch" {
		t.Fatal("CALLBACK_MISMATCH_STATE_MACHINE=FAIL")
	}
	if _, _, err := manager.OpenStore(mismatchTarget.ProfileID); !os.IsNotExist(err) {
		t.Fatal("CALLBACK_MISMATCH_CLEANUP=FAIL")
	}
	t.Log("CALLBACK_MISMATCH_STATE_MACHINE=PASS")

	timeoutStarted, timeoutTarget := liveBeginNegativePKCE(t, server, manager)
	server.mu.Lock()
	timeoutTransaction := server.pkce[timeoutStarted.State]
	timeoutTransaction.Created = server.oauthNow().Add(-pkceTransactionTTL - time.Second)
	server.pkce[timeoutStarted.State] = timeoutTransaction
	server.mu.Unlock()
	timeoutFailure := server.completeCapturedPKCEAuthorization(timeoutStarted.State, browserPKCECapturedAuthorization{
		Code:  "unused-negative-test-code",
		State: timeoutStarted.State,
	})
	if timeoutFailure == nil || timeoutFailure.Code != "oauth_state_expired" {
		t.Fatal("CALLBACK_TIMEOUT_STATE_MACHINE=FAIL")
	}
	timeoutTransaction, ok = server.peekPKCE(timeoutStarted.State)
	if !ok || timeoutTransaction.Status != "expired" || timeoutTransaction.ErrorCode != "oauth_state_expired" {
		t.Fatal("CALLBACK_TIMEOUT_STATUS=FAIL")
	}
	if _, _, err := manager.OpenStore(timeoutTarget.ProfileID); !os.IsNotExist(err) {
		t.Fatal("CALLBACK_TIMEOUT_CLEANUP=FAIL")
	}
	t.Log("CALLBACK_TIMEOUT_STATE_MACHINE=PASS")
	assertLiveActiveUnchanged(t, manager, beforeStatus, activeStore.Path(), activeBefore, activeBeforeExists)
}

func liveStartAttachedCandidate(t *testing.T, server *Server) pendingPKCE {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/browser/default/start", strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	server.startDefaultClientBrowserPKCE(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("LIVE_BROWSER_CANDIDATE_START=FAIL status=%d", recorder.Code)
	}
	var started struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&started); err != nil || started.State == "" {
		t.Fatal("LIVE_BROWSER_CANDIDATE_RESPONSE=FAIL")
	}
	deadline := time.Now().Add(pkceTransactionTTL + 30*time.Second)
	for time.Now().Before(deadline) {
		transaction, ok := server.peekPKCE(started.State)
		if ok {
			switch transaction.Status {
			case "authenticated":
				return transaction
			case "error", "expired", "cancelled":
				t.Fatalf("LIVE_BROWSER_CANDIDATE=FAIL code=%s", transaction.ErrorCode)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("LIVE_BROWSER_CANDIDATE=FAIL code=timeout")
	return pendingPKCE{}
}

func liveBeginNegativePKCE(t *testing.T, server *Server, manager *auth.OAuthProfileManager) (pkceStartResult, pkceProfileTarget) {
	t.Helper()
	manifest, store, err := manager.Stage(defaultClientOAuthConfig())
	if err != nil {
		t.Fatal("NEGATIVE_STAGE=FAIL")
	}
	target := pkceProfileTarget{
		ProfileID: manifest.ProfileID,
		Kind:      manifest.Kind,
		Staged:    true,
		Created:   true,
		OAuth:     manifest.OAuth,
		Store:     store,
	}
	started, failure := server.beginPKCEForTarget(target)
	if failure != nil {
		_ = manager.Discard(manifest.ProfileID)
		t.Fatal("NEGATIVE_PKCE_START=FAIL")
	}
	return started, target
}

func liveValidatedStagedProfileID(t *testing.T, status auth.OAuthProfileStatus) string {
	t.Helper()
	profileID := ""
	for _, profile := range status.Profiles {
		if profile.Active || !profile.Validation.ChatHub {
			continue
		}
		if profileID != "" {
			t.Fatal("LIFECYCLE_STAGED_PROFILE_AMBIGUOUS=FAIL")
		}
		profileID = profile.ProfileID
	}
	if profileID == "" {
		t.Fatal("LIFECYCLE_STAGED_PROFILE_MISSING=FAIL")
	}
	return profileID
}

func assertLiveActiveUnchanged(t *testing.T, manager *auth.OAuthProfileManager, before auth.OAuthProfileStatus, activePath string, beforeBytes []byte, beforeExists bool) {
	t.Helper()
	after, err := manager.Status()
	if err != nil || after.ActiveProfileID != before.ActiveProfileID || after.Generation != before.Generation {
		t.Fatal("ACTIVE_POINTER_UNCHANGED=FAIL")
	}
	afterBytes, afterExists, err := readLiveOptionalFile(activePath)
	if err != nil || afterExists != beforeExists || !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatal("ACTIVE_STORE_UNCHANGED=FAIL")
	}
}

func readLiveOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
