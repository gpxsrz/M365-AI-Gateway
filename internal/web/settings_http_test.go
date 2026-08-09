package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdminSettingsHTTP(t *testing.T) {
	st := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	s := &Server{settings: st}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	w := httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("GET=%d %s", w.Code, w.Body.String())
	}
	var getBody struct {
		Settings              runtimeSettings `json:"settings"`
		CodexModels           []string        `json:"codexModels"`
		UpstreamTones         []string        `json:"upstreamTones"`
		RestartRequiredFields []string        `json:"restartRequiredFields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &getBody); err != nil {
		t.Fatal(err)
	}
	if len(getBody.Settings.ModelMappings) == 0 || len(getBody.CodexModels) == 0 || len(getBody.UpstreamTones) == 0 {
		t.Fatalf("missing model mapping settings: %#v", getBody)
	}
	if getBody.Settings.ChatMode != chatModePrivate || getBody.Settings.TextInputLimitUTF16 != defaultTextInputLimitUTF16 {
		t.Fatalf("missing WP6 defaults: %#v", getBody.Settings)
	}
	for _, field := range getBody.RestartRequiredFields {
		if field == "chatMode" || field == "textInputLimitUTF16" {
			t.Fatalf("hot setting marked restart-required: %s", field)
		}
	}
	v := st.get()
	v.ChatMode = chatModeNormal
	v.TextInputLimitUTF16 = 262144
	v.MaxToolCallsPerTurn = 1
	v.MaxToolRounds = 24
	v.ChatTimeoutSeconds = 75
	v.ImageTimeoutSeconds = 180
	b, _ := json.Marshal(v)
	r = httptest.NewRequest(http.MethodPut, "/api/admin/settings", bytes.NewReader(b))
	w = httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("PUT=%d %s", w.Code, w.Body.String())
	}
	if st.get().ChatTimeoutSeconds != 75 || st.get().ChatMode != chatModeNormal || st.get().TextInputLimitUTF16 != 262144 {
		t.Fatal("hot setting not updated")
	}
	beforeInvalid := st.get()
	v = beforeInvalid
	v.ChatMode = "invalid"
	b, _ = json.Marshal(v)
	r = httptest.NewRequest(http.MethodPut, "/api/admin/settings", bytes.NewReader(b))
	w = httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != http.StatusBadRequest || st.get().ChatMode != beforeInvalid.ChatMode {
		t.Fatalf("invalid mode status=%d settings=%#v", w.Code, st.get())
	}
	v = beforeInvalid
	v.MaxToolCallsPerTurn = 0
	b, _ = json.Marshal(v)
	r = httptest.NewRequest(http.MethodPut, "/api/admin/settings", bytes.NewReader(b))
	w = httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid PUT=%d", w.Code)
	}
}

func TestAdminSettingsHTTPPreservesNonUIPlanningMode(t *testing.T) {
	st := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	st.v.ToolPlanningMode = "native"
	s := &Server{settings: st}

	v := st.get()
	v.ChatMode = chatModeNormal
	v.TextInputLimitUTF16 = 128001
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var uiPayload map[string]any
	if err := json.Unmarshal(b, &uiPayload); err != nil {
		t.Fatal(err)
	}
	delete(uiPayload, "toolPlanningMode")
	b, err = json.Marshal(uiPayload)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodPut, "/api/admin/settings", bytes.NewReader(b))
	w := httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UI-shaped PUT=%d %s", w.Code, w.Body.String())
	}
	got := st.get()
	if got.ChatMode != chatModeNormal || got.TextInputLimitUTF16 != 128001 || got.ToolPlanningMode != "native" {
		t.Fatalf("settings=%#v", got)
	}
}

func TestAdminSettingsCodexModelsMatchPublicCatalogProjection(t *testing.T) {
	s := newAdminSecurityServer(t, "correct-password")
	cfg := s.settings.get()
	cfg.ModelMappings = append(cfg.ModelMappings, modelMapping{
		PublicModel:           "server-local-route",
		UpstreamTone:          "Gpt_5_6_Reasoning",
		DisplayName:           "Server Local Route",
		DefaultReasoningLevel: "low",
	})
	if err := s.settings.save(cfg); err != nil {
		t.Fatal(err)
	}
	_, rawKey, err := s.apiKeys.create("projection-test")
	if err != nil {
		t.Fatal(err)
	}

	ts, client := adminTestClient(t, s.Routes())
	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"correct-password"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login=%d", login.StatusCode)
	}
	login.Body.Close()

	settingsRes, err := client.Get(ts.URL + "/api/admin/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer settingsRes.Body.Close()
	if settingsRes.StatusCode != http.StatusOK {
		t.Fatalf("settings GET=%d", settingsRes.StatusCode)
	}
	var settingsBody struct {
		CodexModels []string `json:"codexModels"`
	}
	if err := json.NewDecoder(settingsRes.Body).Decode(&settingsBody); err != nil {
		t.Fatal(err)
	}

	catalogReq, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/models", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	catalogReq.Header.Set("Authorization", "Bearer "+rawKey)
	catalogRes, err := client.Do(catalogReq)
	if err != nil {
		t.Fatal(err)
	}
	defer catalogRes.Body.Close()
	if catalogRes.StatusCode != http.StatusOK {
		t.Fatalf("catalog GET=%d", catalogRes.StatusCode)
	}
	var catalogBody struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(catalogRes.Body).Decode(&catalogBody); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(catalogBody.Data))
	for _, model := range catalogBody.Data {
		want = append(want, model.ID)
	}
	if !reflect.DeepEqual(settingsBody.CodexModels, want) {
		t.Fatalf("codexModels=%#v want public catalog ids=%#v", settingsBody.CodexModels, want)
	}
	for _, hidden := range []string{"claude", "gpt-5.4-quick", "gpt-5.3-think-deeper", "quick", "think-deeper"} {
		for _, got := range settingsBody.CodexModels {
			if got == hidden {
				t.Fatalf("hidden/request-only route leaked into admin codexModels: %s", hidden)
			}
		}
	}
}
