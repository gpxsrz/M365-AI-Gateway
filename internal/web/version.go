package web

import (
	"net/http"
	"runtime"
	"strings"
	"time"
)

var (
	Version     = "dev"
	Commit      = "unknown"
	BuildTime   = "unknown"
	startedAt   = time.Now()
	updateCheck uint32
)

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	connected := false
	if store := s.activeTokenStore(); store != nil {
		_, connected = store.First()
	}
	jsonOut(w, map[string]any{"version": Version, "commit": Commit, "buildTime": BuildTime, "go": runtime.Version(), "uptimeSeconds": int(time.Since(startedAt).Seconds()), "accountConnected": connected})
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, 405, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	// Read-only endpoint: release automation remains the only publisher/upgrader.
	stable := strings.TrimSpace(Version) != "" && Version != "dev"
	jsonOut(w, map[string]any{"current": Version, "channel": map[bool]string{true: "stable", false: "development"}[stable], "updateAvailable": false, "recommendUpdate": false, "message": map[bool]string{true: "目前為穩定版，可檢查穩定版更新", false: "目前為開發版，不建議更新"}[stable]})
}
