package web

import (
	"log/slog"
	"net/http"
	"time"
)

type responseStatus struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the first status sent by the handler.
func (w *responseStatus) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write records the implicit success status before writing a response body.
func (w *responseStatus) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

// Unwrap preserves ResponseController flushing, deadlines, and hijacking.
func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// WithRequestLogging logs only route templates and controller-generated metadata.
// Raw URLs, query strings, request bodies and credential headers never enter logs.
func WithRequestLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		response := &responseStatus{ResponseWriter: w}
		next.ServeHTTP(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		} else if status >= 400 {
			level = slog.LevelWarn
		}
		logger.Log(r.Context(), level, "management request",
			"requestId", RequestID(r.Context()), "method", r.Method,
			"route", route, "status", status, "duration", time.Since(start))
	})
}
