package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	oauthProfileManifestSchema      = "m365-oauth-profile/v1"
	oauthActiveProfilePointerSchema = "m365-oauth-active-profile/v1"
	oauthProfileStatusSchema        = "m365-oauth-profile-status/v1"
	oauthProfileKindLegacy          = "legacy"
	oauthProfileKindStaged          = "staged"
	legacyOAuthProfileID            = "legacy"
	oauthProfileIDPrefix            = "oauthp_"
	oauthProfileIDBytes             = 16
)

var (
	ErrOAuthProfileValidationIncomplete = errors.New("OAuth profile validation is incomplete")
	ErrOAuthProfileInUse                = errors.New("OAuth profile is referenced by the active pointer")
	ErrOAuthProfileNoRollback           = errors.New("OAuth profile pointer has no rollback target")
)

type OAuthProfileValidationStep string

const (
	OAuthProfileValidationChatHub OAuthProfileValidationStep = "chathub"
	OAuthProfileValidationRefresh OAuthProfileValidationStep = "refresh"
	OAuthProfileValidationRestart OAuthProfileValidationStep = "restart"
	OAuthProfileValidationRemoval OAuthProfileValidationStep = "removal"
)

type OAuthProfileValidation struct {
	ChatHub bool `json:"chathub"`
	Refresh bool `json:"refresh"`
	Restart bool `json:"restart"`
	Removal bool `json:"removal"`
}

func (validation OAuthProfileValidation) Complete() bool {
	return validation.ChatHub && validation.Refresh && validation.Restart && validation.Removal
}

func (validation *OAuthProfileValidation) record(step OAuthProfileValidationStep) (bool, error) {
	var target *bool
	switch step {
	case OAuthProfileValidationChatHub:
		target = &validation.ChatHub
	case OAuthProfileValidationRefresh:
		target = &validation.Refresh
	case OAuthProfileValidationRestart:
		target = &validation.Restart
	case OAuthProfileValidationRemoval:
		target = &validation.Removal
	default:
		return false, fmt.Errorf("unknown OAuth profile validation step %q", step)
	}
	if *target {
		return false, nil
	}
	*target = true
	return true, nil
}

type OAuthProfileManifest struct {
	Schema           string                 `json:"schema"`
	ProfileID        string                 `json:"profile_id"`
	Kind             string                 `json:"kind"`
	TokenCacheSchema string                 `json:"token_cache_schema"`
	OAuth            OAuthConfig            `json:"oauth"`
	Validation       OAuthProfileValidation `json:"validation"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

type OAuthActiveProfilePointer struct {
	Schema            string    `json:"schema"`
	ActiveProfileID   string    `json:"active_profile_id"`
	PreviousProfileID string    `json:"previous_profile_id,omitempty"`
	Generation        uint64    `json:"generation"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type OAuthProfileSummary struct {
	ProfileID  string                 `json:"profile_id"`
	Kind       string                 `json:"kind"`
	Validation OAuthProfileValidation `json:"validation"`
	Active     bool                   `json:"active"`
	Previous   bool                   `json:"previous"`
}

type OAuthProfileStatus struct {
	Schema            string                `json:"schema"`
	ActiveProfileID   string                `json:"active_profile_id"`
	PreviousProfileID string                `json:"previous_profile_id,omitempty"`
	Generation        uint64                `json:"generation"`
	Profiles          []OAuthProfileSummary `json:"profiles"`
}

type OAuthProfileManager struct {
	mu            sync.Mutex
	baseTokenPath string
	root          string
	pointerPath   string
	lockPath      string
	now           func() time.Time
	random        io.Reader
}

func OpenOAuthProfileManager(baseTokenPath string, activeConfig OAuthConfig) (*OAuthProfileManager, error) {
	return openOAuthProfileManager(baseTokenPath, activeConfig, func() time.Time { return time.Now().UTC() }, rand.Reader)
}

func openOAuthProfileManager(baseTokenPath string, activeConfig OAuthConfig, now func() time.Time, random io.Reader) (*OAuthProfileManager, error) {
	if strings.TrimSpace(baseTokenPath) == "" {
		baseTokenPath = CachePath()
	}
	absoluteTokenPath, err := filepath.Abs(baseTokenPath)
	if err != nil {
		return nil, fmt.Errorf("resolve base token cache path: %w", err)
	}
	activeConfig, err = normalizeOAuthConfig(activeConfig)
	if err != nil {
		return nil, err
	}
	if now == nil {
		return nil, errors.New("OAuth profile clock is required")
	}
	if random == nil {
		return nil, errors.New("OAuth profile random source is required")
	}
	root := oauthProfileRootPath(absoluteTokenPath)
	manager := &OAuthProfileManager{
		baseTokenPath: absoluteTokenPath,
		root:          root,
		pointerPath:   filepath.Join(root, "active-profile.json"),
		lockPath:      filepath.Join(root, ".profile.lock"),
		now:           now,
		random:        random,
	}
	if err := manager.withLock(func() error {
		return manager.initializeLocked(activeConfig)
	}); err != nil {
		return nil, err
	}
	return manager, nil
}

func oauthProfileRootPath(baseTokenPath string) string {
	base := filepath.Base(baseTokenPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == "." {
		stem = "accounts"
	}
	return filepath.Join(filepath.Dir(baseTokenPath), stem+"-oauth-profiles")
}

func (manager *OAuthProfileManager) profileDir(profileID string) string {
	return filepath.Join(manager.root, profileID)
}

func (manager *OAuthProfileManager) manifestPath(profileID string) string {
	return filepath.Join(manager.profileDir(profileID), "profile.json")
}

func (manager *OAuthProfileManager) tokenPath(profileID string) string {
	if profileID == legacyOAuthProfileID {
		return manager.baseTokenPath
	}
	return filepath.Join(manager.profileDir(profileID), "accounts.json")
}

func (manager *OAuthProfileManager) withLock(operation func() error) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := ensurePrivateDir(manager.root); err != nil {
		return err
	}
	lock, err := os.OpenFile(manager.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open OAuth profile lock: %w", err)
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("secure OAuth profile lock: %w", err)
	}
	if err := lockOAuthProfileFile(lock); err != nil {
		return fmt.Errorf("lock OAuth profiles: %w", err)
	}
	defer func() { _ = unlockOAuthProfileFile(lock) }()
	return operation()
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}

func (manager *OAuthProfileManager) initializeLocked(activeConfig OAuthConfig) error {
	if err := ensurePrivateDir(manager.profileDir(legacyOAuthProfileID)); err != nil {
		return fmt.Errorf("create legacy OAuth profile directory: %w", err)
	}
	legacyPath := manager.manifestPath(legacyOAuthProfileID)
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		now := manager.now().UTC()
		legacy := OAuthProfileManifest{
			Schema:           oauthProfileManifestSchema,
			ProfileID:        legacyOAuthProfileID,
			Kind:             oauthProfileKindLegacy,
			TokenCacheSchema: TokenCacheSchema,
			OAuth:            activeConfig,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := manager.writeManifestLocked(legacy); err != nil {
			return fmt.Errorf("create legacy OAuth profile manifest: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat legacy OAuth profile manifest: %w", err)
	} else if _, err := manager.readManifestLocked(legacyOAuthProfileID); err != nil {
		return err
	}

	if _, err := os.Stat(manager.pointerPath); os.IsNotExist(err) {
		pointer := OAuthActiveProfilePointer{
			Schema:          oauthActiveProfilePointerSchema,
			ActiveProfileID: legacyOAuthProfileID,
			Generation:      1,
			UpdatedAt:       manager.now().UTC(),
		}
		if err := manager.writePointerLocked(pointer); err != nil {
			return fmt.Errorf("create active OAuth profile pointer: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("stat active OAuth profile pointer: %w", err)
	}
	pointer, err := manager.readPointerLocked()
	if err != nil {
		return err
	}
	return manager.validatePointerTargetsLocked(pointer)
}

func (manager *OAuthProfileManager) ActiveStore() (OAuthProfileManifest, *Store, error) {
	var manifest OAuthProfileManifest
	var store *Store
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		manifest, err = manager.readManifestLocked(pointer.ActiveProfileID)
		if err != nil {
			return err
		}
		store, err = manager.openStoreLocked(manifest)
		return err
	})
	return manifest, store, err
}

func (manager *OAuthProfileManager) OpenStore(profileID string) (OAuthProfileManifest, *Store, error) {
	var manifest OAuthProfileManifest
	var store *Store
	err := manager.withLock(func() error {
		var err error
		manifest, err = manager.readManifestLocked(profileID)
		if err != nil {
			return err
		}
		store, err = manager.openStoreLocked(manifest)
		return err
	})
	return manifest, store, err
}

func (manager *OAuthProfileManager) Stage(config OAuthConfig) (OAuthProfileManifest, *Store, error) {
	config, err := normalizeOAuthConfig(config)
	if err != nil {
		return OAuthProfileManifest{}, nil, err
	}
	var manifest OAuthProfileManifest
	var store *Store
	err = manager.withLock(func() error {
		var err error
		manifest, store, err = manager.stageLocked(config, nil)
		return err
	})
	return manifest, store, err
}

// StageFromActive creates a private reauthorization candidate with the active
// OAuth configuration and a canonical snapshot of the active account credential.
// The accepted token-store bytes and active pointer remain untouched.
func (manager *OAuthProfileManager) StageFromActive() (OAuthProfileManifest, *Store, error) {
	var manifest OAuthProfileManifest
	var store *Store
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		activeManifest, err := manager.readManifestLocked(pointer.ActiveProfileID)
		if err != nil {
			return err
		}
		activeStore, err := manager.openStoreLocked(activeManifest)
		if err != nil {
			return err
		}
		activeStore.mu.Lock()
		seed := Cache{
			Schema:   TokenCacheSchema,
			Accounts: append([]AccountToken(nil), activeStore.data.Accounts...),
		}
		activeStore.mu.Unlock()
		manifest, store, err = manager.stageLocked(activeManifest.OAuth, &seed)
		return err
	})
	return manifest, store, err
}

func (manager *OAuthProfileManager) stageLocked(config OAuthConfig, seed *Cache) (OAuthProfileManifest, *Store, error) {
	profileID, err := manager.newProfileIDLocked()
	if err != nil {
		return OAuthProfileManifest{}, nil, err
	}
	profileDir := manager.profileDir(profileID)
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		return OAuthProfileManifest{}, nil, fmt.Errorf("create staged OAuth profile directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(profileDir)
		}
	}()
	if err := os.Chmod(profileDir, 0o700); err != nil {
		return OAuthProfileManifest{}, nil, err
	}
	now := manager.now().UTC()
	manifest := OAuthProfileManifest{
		Schema:           oauthProfileManifestSchema,
		ProfileID:        profileID,
		Kind:             oauthProfileKindStaged,
		TokenCacheSchema: TokenCacheSchema,
		OAuth:            config,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	store, err := openStore(manager.tokenPath(profileID), config, false)
	if err != nil {
		return OAuthProfileManifest{}, nil, err
	}
	if seed != nil {
		store.mu.Lock()
		store.data = Cache{Schema: TokenCacheSchema, Accounts: append([]AccountToken(nil), seed.Accounts...)}
		err = store.saveLocked()
		store.mu.Unlock()
	} else {
		err = store.initialize()
	}
	if err != nil {
		return OAuthProfileManifest{}, nil, fmt.Errorf("initialize staged OAuth token store: %w", err)
	}
	if err := manager.writeManifestLocked(manifest); err != nil {
		return OAuthProfileManifest{}, nil, fmt.Errorf("write staged OAuth profile manifest: %w", err)
	}
	cleanup = false
	return manifest, store, nil
}

func (manager *OAuthProfileManager) RecordValidation(profileID string, step OAuthProfileValidationStep) (OAuthProfileManifest, error) {
	var manifest OAuthProfileManifest
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		if pointer.ActiveProfileID == profileID {
			return ErrOAuthProfileInUse
		}
		manifest, err = manager.readManifestLocked(profileID)
		if err != nil {
			return err
		}
		if manifest.Kind != oauthProfileKindStaged {
			return errors.New("only staged OAuth profiles accept validation records")
		}
		changed, err := manifest.Validation.record(step)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		manifest.UpdatedAt = manager.now().UTC()
		return manager.writeManifestLocked(manifest)
	})
	return manifest, err
}

func (manager *OAuthProfileManager) Promote(profileID string) (OAuthActiveProfilePointer, error) {
	var result OAuthActiveProfilePointer
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		if pointer.ActiveProfileID == profileID {
			result = pointer
			return nil
		}
		manifest, err := manager.readManifestLocked(profileID)
		if err != nil {
			return err
		}
		if manifest.Kind != oauthProfileKindStaged {
			return errors.New("only staged OAuth profiles can be promoted")
		}
		if !manifest.Validation.Complete() {
			return ErrOAuthProfileValidationIncomplete
		}
		if _, err := manager.openStoreLocked(manifest); err != nil {
			return err
		}
		result = OAuthActiveProfilePointer{
			Schema:            oauthActiveProfilePointerSchema,
			ActiveProfileID:   profileID,
			PreviousProfileID: pointer.ActiveProfileID,
			Generation:        pointer.Generation + 1,
			UpdatedAt:         manager.now().UTC(),
		}
		return manager.writePointerLocked(result)
	})
	return result, err
}

func (manager *OAuthProfileManager) Rollback() (OAuthActiveProfilePointer, error) {
	var result OAuthActiveProfilePointer
	err := manager.withLock(func() error {
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		if pointer.PreviousProfileID == "" {
			return ErrOAuthProfileNoRollback
		}
		manifest, err := manager.readManifestLocked(pointer.PreviousProfileID)
		if err != nil {
			return err
		}
		if _, err := manager.openStoreLocked(manifest); err != nil {
			return err
		}
		result = OAuthActiveProfilePointer{
			Schema:            oauthActiveProfilePointerSchema,
			ActiveProfileID:   pointer.PreviousProfileID,
			PreviousProfileID: pointer.ActiveProfileID,
			Generation:        pointer.Generation + 1,
			UpdatedAt:         manager.now().UTC(),
		}
		return manager.writePointerLocked(result)
	})
	return result, err
}

func (manager *OAuthProfileManager) Discard(profileID string) error {
	return manager.withLock(func() error {
		if !validStagedOAuthProfileID(profileID) {
			if profileID == legacyOAuthProfileID {
				return ErrOAuthProfileInUse
			}
			return os.ErrNotExist
		}
		pointer, err := manager.readPointerLocked()
		if err != nil {
			return err
		}
		if pointer.ActiveProfileID == profileID || pointer.PreviousProfileID == profileID {
			return ErrOAuthProfileInUse
		}
		if _, err := manager.readManifestLocked(profileID); err != nil {
			return err
		}
		if err := os.RemoveAll(manager.profileDir(profileID)); err != nil {
			return fmt.Errorf("discard staged OAuth profile: %w", err)
		}
		return nil
	})
}

func (manager *OAuthProfileManager) Status() (OAuthProfileStatus, error) {
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

func (manager *OAuthProfileManager) newProfileIDLocked() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, oauthProfileIDBytes)
		if _, err := io.ReadFull(manager.random, raw); err != nil {
			return "", fmt.Errorf("generate OAuth profile ID: %w", err)
		}
		profileID := oauthProfileIDPrefix + hex.EncodeToString(raw)
		if _, err := os.Stat(manager.profileDir(profileID)); os.IsNotExist(err) {
			return profileID, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("unable to allocate unique OAuth profile ID")
}

func validOAuthProfileID(profileID string) bool {
	return profileID == legacyOAuthProfileID || validStagedOAuthProfileID(profileID)
}

func validStagedOAuthProfileID(profileID string) bool {
	if !strings.HasPrefix(profileID, oauthProfileIDPrefix) || len(profileID) != len(oauthProfileIDPrefix)+oauthProfileIDBytes*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(profileID, oauthProfileIDPrefix))
	return err == nil
}

func (manager *OAuthProfileManager) readPointer() (OAuthActiveProfilePointer, error) {
	var pointer OAuthActiveProfilePointer
	err := manager.withLock(func() error {
		var err error
		pointer, err = manager.readPointerLocked()
		return err
	})
	return pointer, err
}

func (manager *OAuthProfileManager) readPointerLocked() (OAuthActiveProfilePointer, error) {
	var pointer OAuthActiveProfilePointer
	if err := readStrictJSON(manager.pointerPath, &pointer); err != nil {
		return OAuthActiveProfilePointer{}, fmt.Errorf("read active OAuth profile pointer: %w", err)
	}
	if pointer.Schema != oauthActiveProfilePointerSchema {
		return OAuthActiveProfilePointer{}, fmt.Errorf("active profile pointer schema %q is unsupported", pointer.Schema)
	}
	if !validOAuthProfileID(pointer.ActiveProfileID) {
		return OAuthActiveProfilePointer{}, errors.New("active OAuth profile pointer has invalid active profile ID")
	}
	if pointer.PreviousProfileID != "" && (!validOAuthProfileID(pointer.PreviousProfileID) || pointer.PreviousProfileID == pointer.ActiveProfileID) {
		return OAuthActiveProfilePointer{}, errors.New("active OAuth profile pointer has invalid previous profile ID")
	}
	if pointer.Generation == 0 || pointer.UpdatedAt.IsZero() {
		return OAuthActiveProfilePointer{}, errors.New("active OAuth profile pointer is incomplete")
	}
	return pointer, nil
}

func (manager *OAuthProfileManager) writePointerLocked(pointer OAuthActiveProfilePointer) error {
	if pointer.Schema != oauthActiveProfilePointerSchema {
		return errors.New("refusing to write unsupported active OAuth profile pointer schema")
	}
	return writePrivateJSON(manager.pointerPath, pointer)
}

func (manager *OAuthProfileManager) readManifestLocked(profileID string) (OAuthProfileManifest, error) {
	if !validOAuthProfileID(profileID) {
		return OAuthProfileManifest{}, os.ErrNotExist
	}
	var manifest OAuthProfileManifest
	if err := readStrictJSON(manager.manifestPath(profileID), &manifest); err != nil {
		if os.IsNotExist(err) {
			return OAuthProfileManifest{}, os.ErrNotExist
		}
		return OAuthProfileManifest{}, fmt.Errorf("read OAuth profile manifest %q: %w", profileID, err)
	}
	if manifest.Schema != oauthProfileManifestSchema {
		return OAuthProfileManifest{}, fmt.Errorf("OAuth profile manifest schema %q is unsupported", manifest.Schema)
	}
	if manifest.ProfileID != profileID {
		return OAuthProfileManifest{}, errors.New("OAuth profile manifest ID does not match its directory")
	}
	if manifest.TokenCacheSchema != TokenCacheSchema {
		return OAuthProfileManifest{}, fmt.Errorf("OAuth profile token cache schema %q is unsupported", manifest.TokenCacheSchema)
	}
	if profileID == legacyOAuthProfileID {
		if manifest.Kind != oauthProfileKindLegacy {
			return OAuthProfileManifest{}, errors.New("legacy OAuth profile manifest has invalid kind")
		}
	} else if manifest.Kind != oauthProfileKindStaged {
		return OAuthProfileManifest{}, errors.New("staged OAuth profile manifest has invalid kind")
	}
	normalized, err := normalizeOAuthConfig(manifest.OAuth)
	if err != nil || normalized != manifest.OAuth {
		return OAuthProfileManifest{}, errors.New("OAuth profile manifest has invalid OAuth configuration")
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() || manifest.UpdatedAt.Before(manifest.CreatedAt) {
		return OAuthProfileManifest{}, errors.New("OAuth profile manifest has invalid timestamps")
	}
	return manifest, nil
}

func (manager *OAuthProfileManager) writeManifestLocked(manifest OAuthProfileManifest) error {
	if manifest.Schema != oauthProfileManifestSchema || manifest.TokenCacheSchema != TokenCacheSchema || !validOAuthProfileID(manifest.ProfileID) {
		return errors.New("refusing to write invalid OAuth profile manifest")
	}
	return writePrivateJSON(manager.manifestPath(manifest.ProfileID), manifest)
}

func (manager *OAuthProfileManager) openStoreLocked(manifest OAuthProfileManifest) (*Store, error) {
	path := manager.tokenPath(manifest.ProfileID)
	allowLegacy := manifest.Kind == oauthProfileKindLegacy
	if !allowLegacy {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat staged OAuth token store: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("staged OAuth token store is not a regular file")
		}
	}
	store, err := openStore(path, manifest.OAuth, allowLegacy)
	if err != nil {
		return nil, fmt.Errorf("open OAuth token store for profile %q: %w", manifest.ProfileID, err)
	}
	return store, nil
}

func (manager *OAuthProfileManager) validatePointerTargetsLocked(pointer OAuthActiveProfilePointer) error {
	for _, profileID := range []string{pointer.ActiveProfileID, pointer.PreviousProfileID} {
		if profileID == "" {
			continue
		}
		manifest, err := manager.readManifestLocked(profileID)
		if err != nil {
			return err
		}
		if profileID != legacyOAuthProfileID {
			if _, err := manager.openStoreLocked(manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func readStrictJSON(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(path, raw)
}
