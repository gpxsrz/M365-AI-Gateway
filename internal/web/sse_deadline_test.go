package web

import (
	"net/http"
	"testing"
	"time"
)

type deadlineResponseWriter struct {
	header    http.Header
	deadlines int
}

func (w *deadlineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *deadlineResponseWriter) WriteHeader(int)             {}
func (w *deadlineResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *deadlineResponseWriter) SetWriteDeadline(time.Time) error {
	w.deadlines++
	return nil
}

func TestSSEWriteDeadlineReachesWrappedHTTPWriter(t *testing.T) {
	underlying := &deadlineResponseWriter{}
	tracked := &statusTrackingResponseWriter{ResponseWriter: underlying}
	traced := &traceWriter{ResponseWriter: tracked}

	writeSSE(traced, "probe", map[string]any{"ok": true})
	if underlying.deadlines != 1 {
		t.Fatalf("named SSE write deadline calls=%d want=1", underlying.deadlines)
	}

	wrapped := wrapSSEDeadlineWriter(traced)
	wrapped.Header().Set("Content-Type", "text/event-stream")
	if _, err := wrapped.Write([]byte("data: raw\n\n")); err != nil {
		t.Fatal(err)
	}
	if underlying.deadlines != 2 {
		t.Fatalf("raw SSE write did not refresh deadline: calls=%d want=2", underlying.deadlines)
	}
}
