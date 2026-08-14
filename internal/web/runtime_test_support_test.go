package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"m365-native/internal/evidence"
)

func testOpenAuthStore(t *testing.T, path string, config auth.OAuthConfig) *auth.Store {
	t.Helper()
	store, err := openAuthStoreForTest(path, config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openAuthStoreForTest(path string, config auth.OAuthConfig) (*auth.Store, error) {
	manager, err := auth.OpenOAuthProfileManager(path, config)
	if err != nil {
		return nil, err
	}
	_, store, err := manager.ActiveStore()
	if err != nil {
		return nil, err
	}
	return store, nil
}

func testStoreAccounts(store *auth.Store) []auth.AccountToken {
	account, ok := store.First()
	if !ok {
		return nil
	}
	return []auth.AccountToken{account}
}

func testOAuthProfileRoot(tokenPath string) string {
	base := filepath.Base(tokenPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == "." {
		stem = "accounts"
	}
	return filepath.Join(filepath.Dir(tokenPath), stem+"-oauth-profiles")
}

func testOAuthProfileStatus(t *testing.T, tokenPath string) auth.OAuthProfileStatus {
	t.Helper()
	root := testOAuthProfileRoot(tokenPath)
	pointerRaw, err := os.ReadFile(filepath.Join(root, "active-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pointer auth.OAuthActiveProfilePointer
	if err := json.Unmarshal(pointerRaw, &pointer); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	profiles := make([]auth.OAuthProfileSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "profile.json"))
		if err != nil {
			continue
		}
		var manifest auth.OAuthProfileManifest
		if json.Unmarshal(raw, &manifest) != nil || manifest.ProfileID == "" {
			continue
		}
		profiles = append(profiles, auth.OAuthProfileSummary{
			ProfileID: manifest.ProfileID, Kind: manifest.Kind, Validation: manifest.Validation,
			Active: manifest.ProfileID == pointer.ActiveProfileID, Previous: manifest.ProfileID == pointer.PreviousProfileID,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ProfileID < profiles[j].ProfileID })
	return auth.OAuthProfileStatus{
		ActiveProfileID: pointer.ActiveProfileID, PreviousProfileID: pointer.PreviousProfileID,
		Generation: pointer.Generation, Profiles: profiles,
	}
}

func testActivateOAuthProfile(t *testing.T, tokenPath, profileID string) auth.OAuthActiveProfilePointer {
	t.Helper()
	root := testOAuthProfileRoot(tokenPath)
	pointerPath := filepath.Join(root, "active-profile.json")
	raw, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	var pointer auth.OAuthActiveProfilePointer
	if err := json.Unmarshal(raw, &pointer); err != nil {
		t.Fatal(err)
	}
	next := auth.OAuthActiveProfilePointer{
		Schema: pointer.Schema, ActiveProfileID: profileID, PreviousProfileID: pointer.ActiveProfileID,
		Generation: pointer.Generation + 1, UpdatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointerPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return next
}

func testRollbackOAuthProfile(t *testing.T, tokenPath string) auth.OAuthActiveProfilePointer {
	t.Helper()
	root := testOAuthProfileRoot(tokenPath)
	pointerPath := filepath.Join(root, "active-profile.json")
	raw, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	var pointer auth.OAuthActiveProfilePointer
	if err := json.Unmarshal(raw, &pointer); err != nil {
		t.Fatal(err)
	}
	if pointer.PreviousProfileID == "" {
		t.Fatal("test OAuth profile pointer has no rollback target")
	}
	next := auth.OAuthActiveProfilePointer{
		Schema: pointer.Schema, ActiveProfileID: pointer.PreviousProfileID, PreviousProfileID: pointer.ActiveProfileID,
		Generation: pointer.Generation + 1, UpdatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pointerPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return next
}

func (s *Server) activeAccount() (auth.AccountToken, error) {
	return s.activeAccountContext(context.Background())
}

func compactToolResult(value string, limit int) string {
	return boundedUTF8Preview(strings.TrimSpace(value), limit)
}

func (s *artifactStore) Put(filename string, raw []byte) (artifactRecord, error) {
	return s.PutReader(filename, bytes.NewReader(raw), int64(len(raw)))
}

func (s *artifactStore) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("artifact store is closed")
	}
	err := s.cleanupLocked(s.now())
	s.scheduleLocked()
	return err
}

func configureCatalogEvidence(server *Server, raw []byte, expected evidence.CatalogProjectionExpected) error {
	projection, err := validateAndBindAcceptedWP2CatalogProjection(serverRuntimeSettings(server), raw, expected)
	if err != nil {
		return err
	}
	server.catalogEvidence = projection
	return nil
}

func validateAndBindAcceptedWP2CatalogProjection(cfg runtimeSettings, raw []byte, expected evidence.CatalogProjectionExpected) (*catalogEvidenceProjection, error) {
	validated, err := evidence.ValidateCatalogProjectionManifest(raw, expected)
	if err != nil {
		return nil, err
	}
	return bindCatalogEvidence(cfg, validated)
}

func configuredModelMapping(model string, mappings []modelMapping) (modelMapping, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, mapping := range mappings {
		if strings.EqualFold(strings.TrimSpace(mapping.PublicModel), model) {
			return mapping, true
		}
	}
	return modelMapping{}, false
}

func configuredModelTone(model string, mappings []modelMapping) (string, bool) {
	mapping, ok := configuredModelMapping(model, mappings)
	if !ok {
		return "", false
	}
	return mapping.UpstreamTone, true
}

func configuredModelLimits() modelLimits {
	return configuredModelLimitsForSettings(currentSettings())
}

func reasoningTone(model, effort string) (string, error) {
	resolution, err := resolveRoute(model, effort, currentSettings().ModelMappings)
	if err != nil {
		return "", err
	}
	return resolution.ResolvedTone, nil
}

func modelCatalog() []map[string]any {
	return modelCatalogForSettings(currentSettings())
}

func modelCatalogForSettings(cfg runtimeSettings) []map[string]any {
	return modelCatalogForSettingsAndEvidence(cfg, nil)
}

func writeAnthropicResult(w http.ResponseWriter, model string, stream bool, src map[string]any) error {
	projection, err := projectAnthropicResult(src)
	if err != nil {
		return err
	}
	writeAnthropicProjection(w, model, stream, src, projection)
	return nil
}

func builtInRoute(model string) (routeDefinition, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, route := range builtInRouteRegistry {
		if strings.EqualFold(route.ID, model) {
			return cloneRouteDefinition(route), true
		}
	}
	return routeDefinition{}, false
}

func writeToolResponse(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, result chathub.Result, preambleSent ...bool) error {
	return writeToolResponseWithRoute(w, id, model, stream, calls, result, routeResolution{}, preambleSent...)
}

func writeToolResponseWithRoute(w http.ResponseWriter, id, model string, stream bool, calls []detectedToolCall, result chathub.Result, route routeResolution, preambleSent ...bool) error {
	return writeToolResponseWithPolicy(w, id, model, stream, calls, result, route, nativePolicySnapshot{}, preambleSent...)
}

func validateToolConversation(messages []oaiMsg) error {
	return validateToolConversationWithPrior(messages, nil)
}

func validateToolConversationWithPrior(messages []oaiMsg, priorCallIDs []string, priorSeenCallIDs ...[]string) error {
	var seenDigests []string
	if len(priorSeenCallIDs) > 0 {
		seenDigests = make([]string, 0, len(priorSeenCallIDs[0]))
		for _, id := range priorSeenCallIDs[0] {
			seenDigests = append(seenDigests, toolCallIDDigest(id))
		}
	}
	return validateToolConversationWithPriorDigests(messages, priorCallIDs, seenDigests)
}

func canonicalCheckpointMessage(message oaiMsg) ([]byte, error) {
	return canonicalCheckpointMessageWithToolName(message, "")
}

func (d *debugStore) stopAutoExpiry() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.autoExpire = false
	if d.expiryTimer != nil {
		d.expiryTimer.Stop()
		d.expiryTimer = nil
	}
}

func BuildAcceptedWP2CatalogProjection(repoRoot string) ([]byte, evidence.CatalogProjectionExpected, error) {
	return BuildAcceptedWP2CatalogProjectionFromFS(os.DirFS(repoRoot), "docs/wp2/evidence")
}
