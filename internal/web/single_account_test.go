package web

import (
	"context"
	"encoding/json"
	"fmt"
	"m365-native/internal/auth"
	"m365-native/internal/chathub"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleAccountManagementContractAndLegacyRoutes(t *testing.T) {
	s := newAdminSecurityServer(t, "administrator-password")
	if _, err := s.tokens.Upsert(testTokenSet("active-account")); err != nil {
		t.Fatal(err)
	}
	ts, client := adminTestClient(t, s.Routes())
	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"administrator-password"}`)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", login.StatusCode)
	}

	response, err := client.Get(ts.URL + "/api/account")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("account status = %d", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	account, _ := body["account"].(map[string]any)
	if account["status"] != "online" {
		t.Fatalf("account response = %#v", body)
	}
	for _, forbidden := range []string{"accounts", "profileRef", "profileRefVersion", "accountId"} {
		if _, found := body[forbidden]; found {
			t.Fatalf("selector field %q leaked in %#v", forbidden, body)
		}
		if _, found := account[forbidden]; found {
			t.Fatalf("selector field %q leaked in %#v", forbidden, account)
		}
	}

	for _, legacyPath := range []string{"/api/accounts", "/api/accounts/refresh", "/api/accounts/delete"} {
		legacy, err := client.Get(ts.URL + legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		legacy.Body.Close()
		if legacy.StatusCode != http.StatusNotFound {
			t.Fatalf("legacy selector route %s status = %d", legacyPath, legacy.StatusCode)
		}
	}
}

func testTokenSet(id string) auth.TokenSet {
	return auth.TokenSet{AccessToken: "token-" + id, RefreshToken: "refresh-" + id, HomeOID: "oid-" + id, TenantID: "tid", Email: id + "@example.test", ExpiresAt: time.Now().Add(time.Hour)}
}

type captureSingleAccountChat struct {
	accounts []chathub.Account
	requests []chathub.Request
	next     int
}

func (f *captureSingleAccountChat) result(req chathub.Request) chathub.Result {
	f.requests = append(f.requests, req)
	f.next++
	return chathub.Result{Text: "ok", ConversationID: "conversation-" + string(rune('0'+f.next)), SessionID: "session-" + string(rune('0'+f.next)), RequestID: "request"}
}

func (f *captureSingleAccountChat) Chat(_ context.Context, account chathub.Account, req chathub.Request) (chathub.Result, error) {
	f.accounts = append(f.accounts, account)
	return f.result(req), nil
}

func (f *captureSingleAccountChat) ChatWithDelta(ctx context.Context, account chathub.Account, req chathub.Request, _ func(string) error) (chathub.Result, error) {
	return f.Chat(ctx, account, req)
}

func (f *captureSingleAccountChat) ChatWithEvents(ctx context.Context, account chathub.Account, req chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return f.Chat(ctx, account, req)
}

func TestSingleAccountRoutesHaveBoundedTracePaths(t *testing.T) {
	for _, path := range []string{"/api/account", "/api/account/refresh", "/api/account/logout", "/api/auth/candidate/chat"} {
		if got := safeServiceLogPath(path); got != path {
			t.Fatalf("safeServiceLogPath(%q) = %q", path, got)
		}
	}
	if got := safeServiceLogPath("/api/accounts"); got != "/api/other" {
		t.Fatalf("legacy account pool trace path = %q", got)
	}
}

func TestLegacyAccountIDIsIgnoredAndConversationsStayIsolated(t *testing.T) {
	chat := &captureSingleAccountChat{}
	s := newWP1CandidateServer(t, nil)
	s.chat = chat
	var err error
	s.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints", "transport.json"))
	if err != nil {
		t.Fatal(err)
	}

	for _, request := range []string{
		`{"model":"m365-auto","accountId":"legacy-selector-a","session_key":"hermes-a","messages":[{"role":"user","content":"alpha"}]}`,
		`{"model":"m365-auto","accountId":"legacy-selector-b","session_key":"hermes-b","messages":[{"role":"user","content":"beta"}]}`,
	} {
		recorder := httptest.NewRecorder()
		s.openaiChat(recorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(request)), "single-account-test"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("chat status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if len(chat.accounts) != 2 || chat.accounts[0].AccessToken != chat.accounts[1].AccessToken {
		t.Fatalf("upstream accounts = %#v", chat.accounts)
	}
	bindings := checkpointViewsForTest(t, s.checkpoints)
	if len(bindings) != 2 || bindings[0].ConversationID == "" || bindings[1].ConversationID == "" || bindings[0].ConversationID == bindings[1].ConversationID {
		t.Fatalf("conversation bindings: %#v", bindings)
	}
}

func TestCandidateChatValidationKeepsStagedCredentialInactive(t *testing.T) {
	manager, err := auth.OpenOAuthProfileManager(filepath.Join(t.TempDir(), "accounts.json"), auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeStore.Upsert(testTokenSet("active")); err != nil {
		t.Fatal(err)
	}
	staged, stagedStore, err := manager.StageFromActive()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stagedStore.Upsert(testTokenSet("candidate")); err != nil {
		t.Fatal(err)
	}
	chat := &captureSingleAccountChat{}
	checkpoints := openCheckpointForTest(t)
	turn := beginFullForTest(t, checkpoints, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before account switch"}})
	acceptForTest(t, turn, "old-conversation", "old-session", nil, "")
	s := &Server{tokens: activeStore, oauthProfiles: manager, chat: chat, settings: &settingsStore{v: defaultRuntimeSettings()}, checkpoints: checkpoints}
	recorder := httptest.NewRecorder()
	body := `{"profileId":"` + staged.ProfileID + `"}`
	s.validateOAuthCandidateChat(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/candidate/chat", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("candidate validation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(chat.accounts) != 1 || chat.accounts[0].AccessToken != "token-candidate" {
		t.Fatalf("candidate validation used accounts=%#v", chat.accounts)
	}
	var response map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "validated" {
		t.Fatalf("candidate validation response=%#v", response)
	}
	manifest, activeAfter, err := manager.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProfileID == staged.ProfileID {
		t.Fatalf("candidate ChatHub probe activated staged profile: %#v", manifest)
	}
	activeAccount, ok := activeAfter.First()
	if !ok || activeAccount.AccessToken != "token-active" {
		t.Fatalf("active account after validation = %#v", activeAccount)
	}
	stagedManifest, _, err := manager.OpenStore(staged.ProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if !stagedManifest.Validation.ChatHub || stagedManifest.Validation.Complete() {
		t.Fatalf("candidate validation evidence=%#v", stagedManifest.Validation)
	}
	resolved, err := s.activeAccount()
	if err != nil || resolved.AccessToken != "token-active" {
		t.Fatalf("running server changed active account: account=%#v err=%v", resolved, err)
	}
	if got := checkpointViewsForTest(t, checkpoints); len(got) != 1 {
		t.Fatalf("candidate ChatHub probe changed active-account checkpoints: %#v", got)
	}
}

func TestLogoutClearsTransportCheckpoints(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	if _, err := server.tokens.Upsert(testTokenSet("active-account")); err != nil {
		t.Fatal(err)
	}
	server.checkpoints = openCheckpointForTest(t)
	turn := beginFullForTest(t, server.checkpoints, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before logout"}})
	acceptForTest(t, turn, "old-conversation", "old-session", nil, "")

	recorder := httptest.NewRecorder()
	server.logoutSingleAccount(recorder, httptest.NewRequest(http.MethodPost, "/api/account/logout", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, ok := server.tokens.First(); ok {
		t.Fatal("logout retained Microsoft account token")
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("logout retained transport checkpoints: %#v", got)
	}
}

func TestActiveOAuthTokenReplacementClearsTransportCheckpoints(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	if _, err := server.tokens.Upsert(testTokenSet("before")); err != nil {
		t.Fatal(err)
	}
	server.checkpoints = openCheckpointForTest(t)
	turn := beginFullForTest(t, server.checkpoints, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before relogin"}})
	acceptForTest(t, turn, "old-conversation", "old-session", nil, "")

	stored, failure := server.storeOAuthTokenSet("", testTokenSet("after"))
	if failure != nil {
		t.Fatalf("storeOAuthTokenSet failure = %#v", failure)
	}
	if stored.Account.Status != "online" {
		t.Fatalf("stored account = %#v", stored.Account)
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("re-login retained transport checkpoints: %#v", got)
	}
}

func TestChatModeChangesClearTransportCheckpointsInBothDirections(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	server.checkpoints = openCheckpointForTest(t)
	changeMode := func(from, to string) {
		t.Helper()
		current := server.settings.get()
		if current.ChatMode != from {
			t.Fatalf("current chat mode=%q want=%q", current.ChatMode, from)
		}
		turn := beginFullForTest(t, server.checkpoints, "chat", "owner", "key-"+from, []oaiMsg{{Role: "user", Content: "mode switch"}})
		acceptForTest(t, turn, "conversation-"+from, "session", nil, "")
		current.ChatMode = to
		recorder := httptest.NewRecorder()
		server.adminSettings(recorder, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(mustJSON(current))))
		if recorder.Code != http.StatusOK {
			t.Fatalf("mode %s -> %s status=%d body=%s", from, to, recorder.Code, recorder.Body.String())
		}
		if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
			t.Fatalf("mode %s -> %s retained checkpoints: %#v", from, to, got)
		}
	}
	changeMode(chatModePrivate, chatModeNormal)
	changeMode(chatModeNormal, chatModePrivate)
}

func TestOAuthTokenStoreRechecksActiveProfileInsideCheckpointLifecycle(t *testing.T) {
	manager, err := auth.OpenOAuthProfileManager(filepath.Join(t.TempDir(), "accounts.json"), auth.CurrentOAuthConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, activeStore, err := manager.ActiveStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeStore.Upsert(testTokenSet("active")); err != nil {
		t.Fatal(err)
	}
	staged, stagedStore, err := manager.StageFromActive()
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []auth.OAuthProfileValidationStep{
		auth.OAuthProfileValidationChatHub,
		auth.OAuthProfileValidationRefresh,
		auth.OAuthProfileValidationRestart,
		auth.OAuthProfileValidationRemoval,
	} {
		if _, err := manager.RecordValidation(staged.ProfileID, step); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{tokens: activeStore, oauthProfiles: manager, checkpoints: openCheckpointForTest(t)}
	turn := beginFullForTest(t, server.checkpoints, "chat", "owner", "key", []oaiMsg{{Role: "user", Content: "before promotion"}})
	acceptForTest(t, turn, "old-conversation", "old-session", nil, "")

	server.checkpointLifecycle.Lock()
	done := make(chan *oauthCallbackFailure, 1)
	go func() {
		_, failure := server.storeOAuthTokenSetInStore(staged, stagedStore, testTokenSet("promoted-refresh"))
		done <- failure
	}()
	// The callback resolves the staged store before waiting on the lifecycle
	// lock. Promote it while holding that same lock to reproduce the race that
	// requires an active-store recheck after serialization.
	if _, err := manager.Promote(staged.ProfileID); err != nil {
		server.checkpointLifecycle.Unlock()
		t.Fatal(err)
	}
	_, promotedStore, err := manager.ActiveStore()
	if err != nil {
		server.checkpointLifecycle.Unlock()
		t.Fatal(err)
	}
	server.setActiveTokenStore(promotedStore)
	server.checkpointLifecycle.Unlock()

	if failure := <-done; failure != nil {
		t.Fatalf("storeOAuthTokenSet failure = %#v", failure)
	}
	if got := checkpointViewsForTest(t, server.checkpoints); len(got) != 0 {
		t.Fatalf("promoted-store refresh retained checkpoint: %#v", got)
	}
	account, ok := server.activeTokenStore().First()
	if !ok || account.AccessToken != "token-promoted-refresh" {
		t.Fatalf("promoted active account = %#v", account)
	}
}

func TestOpenAIModelsDoesNotExposeMultiAccountMetadata(t *testing.T) {
	harness, cleanup, err := newWP2RouteProtocolHarnessServer(&wp2HarnessChat{result: chathub.Result{Text: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	projection, err := defaultAcceptedWP2CatalogProjection(serverRuntimeSettings(harness.server))
	if err != nil {
		t.Fatal(err)
	}
	harness.server.catalogEvidence = projection
	var body map[string]any
	if err := json.Unmarshal(requestCatalog(t, harness), &body); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"account_dependent",
		"x_m365_profile_set_sha256",
		"eligible_profile_count",
		"unavailable_profile_count",
		"x_m365_protocol_claims",
	} {
		if containsJSONKey(body, forbidden) {
			t.Fatalf("/v1/models exposed historical multi-account field %q", forbidden)
		}
	}
}

func containsJSONKey(value any, target string) bool {
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			if key == target || containsJSONKey(nested, target) {
				return true
			}
		}
	case []any:
		for _, nested := range value {
			if containsJSONKey(nested, target) {
				return true
			}
		}
	}
	return false
}

type concurrentSingleAccountChat struct {
	next     atomic.Int64
	mu       sync.Mutex
	accounts []chathub.Account
}

func (chat *concurrentSingleAccountChat) Chat(_ context.Context, account chathub.Account, _ chathub.Request) (chathub.Result, error) {
	chat.mu.Lock()
	chat.accounts = append(chat.accounts, account)
	chat.mu.Unlock()
	n := chat.next.Add(1)
	return chathub.Result{Text: "ok", ConversationID: fmt.Sprintf("conversation-%d", n), SessionID: fmt.Sprintf("session-%d", n)}, nil
}

func (chat *concurrentSingleAccountChat) ChatWithDelta(ctx context.Context, account chathub.Account, request chathub.Request, _ func(string) error) (chathub.Result, error) {
	return chat.Chat(ctx, account, request)
}

func (chat *concurrentSingleAccountChat) ChatWithEvents(ctx context.Context, account chathub.Account, request chathub.Request, _ chathub.StreamHandler) (chathub.Result, error) {
	return chat.Chat(ctx, account, request)
}

func TestConcurrentAPIRequestsShareOneAccountAndKeepConversationBindingsDistinct(t *testing.T) {
	const requests = 12
	chat := &concurrentSingleAccountChat{}
	server := newWP1CandidateServer(t, nil)
	server.chat = chat
	var err error
	server.checkpoints, err = openTransportCheckpointStore(filepath.Join(t.TempDir(), "checkpoints", "transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	expected, ok := server.activeTokenStore().First()
	if !ok {
		t.Fatal("test server has no active account")
	}
	statuses := make(chan int, requests)
	var wait sync.WaitGroup
	for i := 0; i < requests; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			body := fmt.Sprintf(`{"model":"m365-auto","accountId":"ignored-%d","session_key":"hermes-%d","messages":[{"role":"user","content":"request %d"}]}`, i, i, i)
			recorder := httptest.NewRecorder()
			server.openaiChat(recorder, withAPIKeyOwner(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), "single-account-concurrency-test"))
			statuses <- recorder.Code
		}(i)
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent status = %d", status)
		}
	}
	chat.mu.Lock()
	defer chat.mu.Unlock()
	if len(chat.accounts) != requests {
		t.Fatalf("upstream calls = %d", len(chat.accounts))
	}
	for _, account := range chat.accounts {
		if account.AccessToken != expected.AccessToken {
			t.Fatalf("concurrent request used another account: %#v", account)
		}
	}
	bindings := checkpointViewsForTest(t, server.checkpoints)
	if len(bindings) != requests {
		t.Fatalf("conversation bindings=%d want=%d", len(bindings), requests)
	}
	seen := map[string]bool{}
	for i, binding := range bindings {
		if binding.ConversationID == "" || seen[binding.ConversationID] {
			t.Fatalf("invalid conversation binding %d: %#v", i, binding)
		}
		seen[binding.ConversationID] = true
	}
}
