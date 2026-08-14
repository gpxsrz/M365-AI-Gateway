package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOAuthProfileManagerPreservesLegacyStoreAndPinsPrivateMetadata(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "accounts.json")
	legacyRaw := []byte(`{"accounts":[{"id":"legacy-oid","email":"person@example.test","status":"online","accessToken":"legacy-access-secret","refreshToken":"legacy-refresh-secret","expiresAt":"2030-01-02T03:04:05Z","updatedAt":"2026-08-06T12:00:00Z","oid":"legacy-oid","tid":"legacy-tid","clientId":"legacy-client"}]}`)
	if err := os.WriteFile(basePath, legacyRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 6, 12, 34, 56, 0, time.UTC)
	manager, err := openOAuthProfileManager(basePath, testOAuthConfig("legacy-client"), func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	manifest, store, err := manager.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProfileID != legacyOAuthProfileID || manifest.Kind != oauthProfileKindLegacy {
		t.Fatalf("active legacy manifest = %#v", manifest)
	}
	if manifest.Schema != oauthProfileManifestSchema || manifest.TokenCacheSchema != TokenCacheSchema {
		t.Fatalf("legacy schema = %#v", manifest)
	}
	if got := store.Config(); got != testOAuthConfig("legacy-client") {
		t.Fatalf("legacy OAuth config = %#v", got)
	}
	accounts := store.List()
	if len(accounts) != 1 || accounts[0].AccessToken != "legacy-access-secret" || accounts[0].RefreshToken != "legacy-refresh-secret" {
		t.Fatalf("legacy accounts = %#v", accounts)
	}
	if got, err := os.ReadFile(basePath); err != nil || !bytes.Equal(got, legacyRaw) {
		t.Fatalf("legacy token bytes changed: err=%v\n got=%s\nwant=%s", err, got, legacyRaw)
	}

	pointer, err := readPointerForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	if pointer.Schema != oauthActiveProfilePointerSchema || pointer.ActiveProfileID != legacyOAuthProfileID || pointer.PreviousProfileID != "" || pointer.Generation != 1 || !pointer.UpdatedAt.Equal(now) {
		t.Fatalf("initial pointer = %#v", pointer)
	}
	assertMode(t, manager.root, 0o700)
	assertMode(t, manager.profileDir(legacyOAuthProfileID), 0o700)
	assertMode(t, manager.pointerPath, 0o600)
	assertMode(t, manager.manifestPath(legacyOAuthProfileID), 0o600)
	assertMode(t, basePath, 0o600)

	status, err := profileStatusForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"legacy-access-secret", "legacy-refresh-secret", "person@example.test", "legacy-oid", "legacy-tid"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, raw)
		}
	}
	if status.Schema != oauthProfileStatusSchema || len(status.Profiles) != 1 || status.Profiles[0].ProfileID != legacyOAuthProfileID {
		t.Fatalf("status = %#v", status)
	}
}

func TestOAuthProfileManagerStagesCanonicalCopyFromActiveWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "accounts.json")
	activeRaw := []byte(`{"schema":"` + TokenCacheSchema + `","accounts":[{"id":"active-oid","email":"active@example.test","status":"online","accessToken":"active-access-secret","refreshToken":"active-refresh-secret","expiresAt":"2030-01-02T03:04:05Z","updatedAt":"2026-08-06T12:00:00Z","oid":"active-oid","tid":"active-tid","clientId":"active-client"}]}`)
	if err := os.WriteFile(basePath, activeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	fixedNow := func() time.Time { return time.Date(2026, 8, 6, 12, 45, 0, 0, time.UTC) }
	manager, err := openOAuthProfileManager(basePath, testOAuthConfig("active-client"), fixedNow, bytes.NewReader(bytes.Repeat([]byte{0x12}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	pointerBefore, err := readPointerForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	activeBefore := mustReadFile(t, basePath)

	manifest, stagedStore, err := manager.StageFromActive()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProfileID != "oauthp_"+strings.Repeat("12", 16) || manifest.Kind != oauthProfileKindStaged || manifest.OAuth != testOAuthConfig("active-client") {
		t.Fatalf("staged manifest=%#v", manifest)
	}
	accounts := stagedStore.List()
	if len(accounts) != 1 || accounts[0].ID != "active-oid" || accounts[0].AccessToken != "active-access-secret" || accounts[0].RefreshToken != "active-refresh-secret" {
		t.Fatalf("staged accounts=%#v", accounts)
	}
	if got := mustReadFile(t, basePath); !bytes.Equal(got, activeBefore) {
		t.Fatal("StageFromActive changed active token-store bytes")
	}
	pointerAfter, err := readPointerForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	if pointerAfter != pointerBefore {
		t.Fatalf("StageFromActive changed active pointer: before=%#v after=%#v", pointerBefore, pointerAfter)
	}
	assertMode(t, manager.profileDir(manifest.ProfileID), 0o700)
	assertMode(t, manager.manifestPath(manifest.ProfileID), 0o600)
	assertMode(t, stagedStore.Path(), 0o600)
}

func TestOAuthProfileValidationDoesNotChangeActivePointerOrCredentialBytes(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "accounts.json")
	legacyRaw := []byte(`{"schema":"` + TokenCacheSchema + `","accounts":[{"id":"legacy","email":"legacy@example.test","status":"online","accessToken":"accepted-access-secret","refreshToken":"accepted-refresh-secret","expiresAt":"2030-01-02T03:04:05Z","updatedAt":"2026-08-06T12:00:00Z","oid":"legacy","tid":"tenant","clientId":"accepted-client"}]}`)
	if err := os.WriteFile(basePath, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	times := []time.Time{
		time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 1, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 2, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 3, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 4, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 5, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 6, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 13, 7, 0, 0, time.UTC),
	}
	var timeMu sync.Mutex
	timeIndex := 0
	now := func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		if timeIndex >= len(times) {
			return times[len(times)-1]
		}
		value := times[timeIndex]
		timeIndex++
		return value
	}
	manager, err := openOAuthProfileManager(basePath, testOAuthConfig("accepted-client"), now, bytes.NewReader(bytes.Repeat([]byte{0x22}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	stagedConfig := testOAuthConfig("candidate-client")
	staged, stagedStore, err := manager.Stage(stagedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if staged.ProfileID != "oauthp_"+strings.Repeat("22", 16) || staged.Kind != oauthProfileKindStaged {
		t.Fatalf("staged profile = %#v", staged)
	}
	if _, err := stagedStore.Upsert(TokenSet{
		AccessToken:  "candidate-access-secret",
		RefreshToken: "candidate-refresh-secret",
		Email:        "candidate@example.test",
		HomeOID:      "candidate-oid",
		TenantID:     "candidate-tid",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	staged, err = manager.RecordValidation(staged.ProfileID, OAuthProfileValidationChatHub)
	if err != nil {
		t.Fatalf("record ChatHub validation: %v", err)
	}
	if staged.Validation.Complete() {
		t.Fatalf("ChatHub-only validation unexpectedly completed lifecycle: %#v", staged.Validation)
	}
	for _, step := range []OAuthProfileValidationStep{
		OAuthProfileValidationRefresh,
		OAuthProfileValidationRestart,
		OAuthProfileValidationRemoval,
	} {
		staged, err = manager.RecordValidation(staged.ProfileID, step)
		if err != nil {
			t.Fatalf("record %s validation: %v", step, err)
		}
	}
	if !staged.Validation.Complete() {
		t.Fatalf("complete lifecycle validation was not recorded: %#v", staged.Validation)
	}

	legacyBefore := mustReadFile(t, basePath)
	stagedTokenPath := manager.tokenPath(staged.ProfileID)
	stagedBefore := mustReadFile(t, stagedTokenPath)
	manifestBefore := mustReadFile(t, manager.manifestPath(staged.ProfileID))
	pointer, err := readPointerForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	if pointer.ActiveProfileID != legacyOAuthProfileID || pointer.PreviousProfileID != "" || pointer.Generation != 1 {
		t.Fatalf("validation changed active pointer = %#v", pointer)
	}
	if got := mustReadFile(t, basePath); !bytes.Equal(got, legacyBefore) {
		t.Fatal("validation changed accepted legacy token bytes")
	}
	if got := mustReadFile(t, stagedTokenPath); !bytes.Equal(got, stagedBefore) {
		t.Fatal("validation changed staged token bytes")
	}
	if got := mustReadFile(t, manager.manifestPath(staged.ProfileID)); !bytes.Equal(got, manifestBefore) {
		t.Fatal("validation unexpectedly rewrote staged manifest after snapshot")
	}

	reopened, err := openOAuthProfileManager(basePath, testOAuthConfig("ignored-new-process-config"), func() time.Time { return times[len(times)-1] }, bytes.NewReader(bytes.Repeat([]byte{0x33}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	activeManifest, activeStore, err := reopened.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	if activeManifest.ProfileID != legacyOAuthProfileID || activeStore.Config().ClientID != "accepted-client" {
		t.Fatalf("reopened active profile = %#v config=%#v", activeManifest, activeStore.Config())
	}
	if list := activeStore.List(); len(list) != 1 || list[0].AccessToken != "accepted-access-secret" {
		t.Fatalf("reopened active accounts = %#v", list)
	}
	stagedManifest, reopenedStagedStore, err := reopened.OpenStore(staged.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if stagedManifest.ProfileID != staged.ProfileID || reopenedStagedStore.Config() != stagedConfig {
		t.Fatalf("reopened staged profile = %#v config=%#v", stagedManifest, reopenedStagedStore.Config())
	}
	if list := reopenedStagedStore.List(); len(list) != 1 || list[0].AccessToken != "candidate-access-secret" {
		t.Fatalf("reopened staged accounts = %#v", list)
	}
	reopenedPointer, err := readPointerForTest(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedPointer != pointer {
		t.Fatalf("restart changed active pointer: before=%#v after=%#v", pointer, reopenedPointer)
	}

	assertMode(t, manager.profileDir(staged.ProfileID), 0o700)
	assertMode(t, manager.manifestPath(staged.ProfileID), 0o600)
	assertMode(t, stagedTokenPath, 0o600)
	metadata := string(manifestBefore)
	for _, forbidden := range []string{"candidate-access-secret", "candidate-refresh-secret", "candidate@example.test", "candidate-oid", "candidate-tid"} {
		if strings.Contains(metadata, forbidden) {
			t.Fatalf("profile manifest leaked %q: %s", forbidden, metadata)
		}
	}
}

func TestOAuthProfileManagerDiscardsFailedStagingButProtectsPointerTargets(t *testing.T) {
	manager, err := openOAuthProfileManager(filepath.Join(t.TempDir(), "accounts.json"), testOAuthConfig("legacy"), func() time.Time { return time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC) }, bytes.NewReader(bytes.Repeat([]byte{0x44}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := manager.Stage(testOAuthConfig("failed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Discard(failed.ProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(manager.profileDir(failed.ProfileID)); !os.IsNotExist(err) {
		t.Fatalf("discarded profile still exists: %v", err)
	}
	pointer, err := readPointerForTest(manager)
	if err != nil {
		t.Fatal(err)
	}
	if pointer.ActiveProfileID != legacyOAuthProfileID || pointer.Generation != 1 {
		t.Fatalf("discard changed pointer = %#v", pointer)
	}
	if err := manager.Discard(legacyOAuthProfileID); !errors.Is(err, ErrOAuthProfileInUse) {
		t.Fatalf("discard active legacy error = %v", err)
	}
}

func TestOAuthProfileManagerRejectsUnsupportedMetadataSchemas(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "accounts.json")
	manager, err := openOAuthProfileManager(basePath, testOAuthConfig("legacy"), func() time.Time { return time.Date(2026, 8, 6, 16, 0, 0, 0, time.UTC) }, bytes.NewReader(bytes.Repeat([]byte{0x88}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	pointer := mustReadFile(t, manager.pointerPath)
	badPointer := bytes.Replace(pointer, []byte(oauthActiveProfilePointerSchema), []byte("m365-oauth-active-profile/v999"), 1)
	if err := os.WriteFile(manager.pointerPath, badPointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openOAuthProfileManager(basePath, testOAuthConfig("legacy"), time.Now, bytes.NewReader(bytes.Repeat([]byte{0x99}, 32))); err == nil || !strings.Contains(err.Error(), "active profile pointer schema") {
		t.Fatalf("unsupported pointer schema error = %v", err)
	}
}

func testOAuthConfig(clientID string) OAuthConfig {
	return OAuthConfig{
		ClientID:          clientID,
		Authority:         "https://login.example.test/common",
		RedirectURI:       "https://login.example.test/common/oauth2/nativeclient",
		Scope:             "openid profile offline_access scope.read",
		AuthorizeEndpoint: "https://login.example.test/common/oauth2/v2.0/authorize",
		TokenEndpoint:     "https://login.example.test/common/oauth2/v2.0/token",
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
