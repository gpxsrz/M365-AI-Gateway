package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyPoolAdminRouteIsRemoved(t *testing.T) {
	server := newAdminSecurityServer(t, "administrator-password")
	ts, client := adminTestClient(t, server.Routes())
	login := postJSON(t, client, ts.URL+"/api/admin/login", `{"password":"administrator-password"}`)
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", login.StatusCode)
	}
	response, err := client.Get(ts.URL + "/api/admin/proxy-pool")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("removed proxy-pool route status=%d, want 404", response.StatusCode)
	}
}

func TestLegacyProxyPoolSettingsAreDroppedDuringLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := defaultRuntimeSettings()
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["proxyPool"] = []string{"http://proxy-a.example:8080", "socks5://proxy-b.example:1080"}
	raw, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := loadSettingsStore(path)
	if store.loadErr != nil {
		t.Fatal(store.loadErr)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), `"proxyPool"`) || strings.Contains(string(persisted), "proxy-a.example") {
		t.Fatalf("legacy proxy pool remained persisted after migration: %s", persisted)
	}
}

func TestManagementUIHasNoProxyPoolSurface(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, forbidden := range []string{"/api/admin/proxy-pool", "proxyPool", "page-proxy", "M365_PROXY_POOL", "代理集區"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("management UI retained removed proxy-pool surface %q", forbidden)
		}
	}
	if !strings.Contains(page, "outboundProxy") || !strings.Contains(page, "單一 Outbound Proxy") {
		t.Fatal("management UI lost the supported single outbound proxy setting")
	}
}

func TestAdminSettingsAndVersionDoNotAdvertiseProxyPool(t *testing.T) {
	store := &settingsStore{path: filepath.Join(t.TempDir(), "settings.json"), v: defaultRuntimeSettings()}
	server := &Server{settings: store}

	settingsRecorder := httptest.NewRecorder()
	server.adminSettings(settingsRecorder, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil))
	if settingsRecorder.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", settingsRecorder.Code, settingsRecorder.Body.String())
	}
	if strings.Contains(settingsRecorder.Body.String(), `"proxyPool"`) {
		t.Fatalf("settings API still advertises proxy pool: %s", settingsRecorder.Body.String())
	}

	versionRecorder := httptest.NewRecorder()
	server.version(versionRecorder, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if versionRecorder.Code != http.StatusOK {
		t.Fatalf("version status=%d body=%s", versionRecorder.Code, versionRecorder.Body.String())
	}
	if strings.Contains(versionRecorder.Body.String(), `"proxyPool"`) {
		t.Fatalf("version API still advertises proxy pool: %s", versionRecorder.Body.String())
	}
	if got := safeServiceLogPath("/api/admin/proxy-pool"); got != "/api/other" {
		t.Fatalf("removed proxy-pool route still has a dedicated trace path: %q", got)
	}
}
