package web

import "net/http"

type sseDeadlineResponseWriter struct {
	http.ResponseWriter
}

func wrapSSEDeadlineWriter(w http.ResponseWriter) http.ResponseWriter {
	if w == nil {
		return w
	}
	if _, already := w.(*sseDeadlineResponseWriter); already {
		return w
	}
	return &sseDeadlineResponseWriter{ResponseWriter: w}
}

func (w *sseDeadlineResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *sseDeadlineResponseWriter) Write(p []byte) (int, error) {
	setSSEWriteDeadline(w)
	return w.ResponseWriter.Write(p)
}

func (w *sseDeadlineResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *sseDeadlineResponseWriter) setOutcomeStatus(status int) {
	if tracker, ok := w.ResponseWriter.(interface{ setOutcomeStatus(int) }); ok {
		tracker.setOutcomeStatus(status)
	}
}
