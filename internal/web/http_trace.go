package web

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type traceWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *traceWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *traceWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
func (w *traceWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func safeServiceLogMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func safeServiceLogPath(path string) string {
	if _, artifact := artifactCapabilityToken(path); artifact {
		return "/v1/artifacts/{redacted}/content"
	}
	if strings.HasPrefix(path, "/v1/") {
		_, safePath := debugProtocolAndPath(path)
		return safePath
	}
	switch path {
	case "/api/admin/login", "/api/admin/logout", "/api/admin/session", "/api/admin/change-password",
		"/api/admin/keys", "/api/admin/settings", "/api/admin/proxy-pool", "/api/admin/deployments",
		"/api/admin/deployment", "/api/admin/deployment/check", "/api/admin/debug/logs",
		"/api/admin/debug/detail", "/api/admin/debug/session", "/api/admin/debug/export",
		"/api/health", "/api/version", "/api/update", "/api/account", "/api/account/refresh", "/api/account/logout",
		"/api/auth/start", "/api/auth/status", "/api/auth/callback", "/api/auth/browser/default/start", "/api/auth/candidate/chat",
		"/api/chat", "/api/chat/stream", "/api/conversations", "/api/conversations/delete":
		return path
	default:
		return "/api/other"
	}
}

func httpTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		tw := &traceWriter{ResponseWriter: w}
		log.Printf("[http-trace] id=%s stage=start method=%s path=%s", requestIDFrom(r), safeServiceLogMethod(r.Method), safeServiceLogPath(r.URL.Path))
		next.ServeHTTP(tw, r)
		status := tw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("[http-trace] id=%s stage=end status=%d bytes=%d total_ms=%d", requestIDFrom(r), status, tw.bytes, time.Since(start).Milliseconds())
	})
}
