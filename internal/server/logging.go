package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"logstream/internal/logging"
	"logstream/internal/store"
)

type loggedResponse struct {
	http.ResponseWriter
	status int
	bytes  int
	detail string
}

func (w *loggedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *loggedResponse) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *loggedResponse) setErrorDetail(detail string) { w.detail = logging.Clean(detail) }
func (w *loggedResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Preserve streaming and ResponseController deadlines through the logging wrapper.
func (w *loggedResponse) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *loggedResponse) Flush() { _ = w.FlushError() }

// Never record raw URLs (search queries), request bodies, headers, or cookies.
func logRoute(path string) string {
	switch path {
	case "/api/bootstrap", "/api/health", "/api/config", "/api/events", "/api/logs", "/api/demo", "/api/hosts", "/api/hosts/probe", "/api/groups":
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "hosts" {
		return "/api/hosts/{id}"
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "sources" {
		return "/api/sources/{id}"
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "groups" {
		return "/api/groups/{id}"
	}
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "hosts" && (parts[3] == "start" || parts[3] == "stop" || parts[3] == "duplicate") {
		return "/api/hosts/{id}/" + parts[3]
	}
	if strings.HasPrefix(path, "/api/") {
		return "/api/[unknown]"
	}
	return "/[static]"
}
func (a *App) requestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := store.ID()
		out := &loggedResponse{ResponseWriter: w}
		out.Header().Set("X-Request-ID", id)
		route := logRoute(r.URL.Path)
		base := a.logger.With("request_id", id)
		r = r.WithContext(logging.WithContext(r.Context(), base))
		logger := base.With("component", "http")
		defer func() {
			panicked := recover()
			abort := false
			if panicked != nil {
				abort = out.status != 0 || panicked == http.ErrAbortHandler
				if panicked != http.ErrAbortHandler {
					// Panic values can include credentials; record location at debug only.
					logger.Error("http.panic", "route", route, "response_started", out.status != 0)
					logger.Debug("http.panic_stack", "stack", string(debug.Stack()))
					if !abort {
						fail(out, http.StatusInternalServerError, "Internal server error")
					}
				}
			}
			status := out.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelDebug
			if r.Method != "GET" && r.Method != "HEAD" {
				level = slog.LevelInfo
			}
			if time.Since(started) > time.Second && route != "/api/events" {
				level = slog.LevelWarn
			}
			if status >= 400 {
				level = slog.LevelWarn
			}
			if status >= 500 {
				level = slog.LevelError
			}
			attrs := []any{"method", r.Method, "route", route, "status", status, "duration", time.Since(started), "bytes", out.bytes}
			if out.detail != "" {
				attrs = append(attrs, "detail", out.detail)
			}
			if r.Context().Err() != nil {
				attrs = append(attrs, "canceled", true)
			}
			if panicked != nil {
				attrs = append(attrs, "aborted", abort)
			}
			logger.Log(r.Context(), level, "http.request", attrs...)
			if abort {
				panic(http.ErrAbortHandler)
			}
		}()
		next.ServeHTTP(out, r)
	})
}
