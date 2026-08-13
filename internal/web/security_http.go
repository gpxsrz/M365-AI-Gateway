package web

import (
	"bytes"
	_ "embed"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed assets/compat-settings.js
var compatibilitySettingsAsset []byte

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self' 'unsafe-inline' https://unpkg.com")
		if r.URL.Path == "/" || r.URL.Path == "/debug" || r.URL.Path == "/assets/compat-settings.js" || strings.HasPrefix(r.URL.Path, "/api/admin/debug/") || strings.HasPrefix(r.URL.Path, "/api/auth/") || r.URL.Path == "/api/admin/login" || r.URL.Path == "/api/admin/logout" || r.URL.Path == "/api/admin/session" || r.URL.Path == "/api/admin/change-password" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Pragma", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) debugPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/debug" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	serveWebFile(w, r, "web/debug.html")
}

func serveWebFile(w http.ResponseWriter, r *http.Request, name string) {
	serveStaticFile(w, r, name, "text/html; charset=utf-8")
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	raw, err := os.ReadFile(name)
	if err != nil {
		http.Error(w, "管理介面無法使用", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(name)
	if err != nil {
		http.Error(w, "管理介面無法使用", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(raw))
}

func serveWebFileWithScript(w http.ResponseWriter, r *http.Request, name, scriptPath string) {
	raw, err := os.ReadFile(name)
	if err != nil {
		http.Error(w, "管理介面無法使用", http.StatusInternalServerError)
		return
	}
	st, err := os.Stat(name)
	if err != nil {
		http.Error(w, "管理介面無法使用", http.StatusInternalServerError)
		return
	}
	closingBody := []byte("</body>")
	if !bytes.Contains(raw, closingBody) {
		http.Error(w, "管理介面無法使用", http.StatusInternalServerError)
		return
	}
	injection := []byte(`<script src="` + scriptPath + `"></script></body>`)
	raw = bytes.Replace(raw, closingBody, injection, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(raw))
}

func (s *Server) compatibilitySettingsScript(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/assets/compat-settings.js" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	http.ServeContent(w, r, "compat-settings.js", time.Time{}, bytes.NewReader(compatibilitySettingsAsset))
}

func (s *Server) rootPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "不支援此 HTTP 方法")
		return
	}
	name := "web/login.html"
	if s.validAdminSession(r) {
		s.mu.Lock()
		mustChange := s.mustChangePassword
		s.mu.Unlock()
		if !mustChange {
			name = "web/index.html"
		}
	}
	if name == "web/index.html" {
		serveWebFileWithScript(w, r, name, "/assets/compat-settings.js")
		return
	}
	serveWebFile(w, r, name)
}
