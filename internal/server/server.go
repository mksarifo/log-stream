package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"logstream/internal/collector"
	"logstream/internal/logging"
	"logstream/internal/store"
	"logstream/internal/stream"
)

type App struct {
	Store      *store.Store
	Hub        *stream.Hub
	Collectors *collector.Manager
	ctx        context.Context
	token      string
	mutate     sync.Mutex
	demoMu     sync.Mutex
	demoCancel context.CancelFunc
	demoDone   chan struct{}
	demo       bool
	logger     *slog.Logger
	closeOnce  sync.Once
}

func New(ctx context.Context, s *store.Store) *App {
	h := stream.New(10000, 16*1024*1024)
	return &App{Store: s, Hub: h, Collectors: collector.New(ctx, h), ctx: ctx, token: store.ID(), logger: slog.Default()}
}
func (a *App) Close() {
	a.closeOnce.Do(func() { a.setDemo(false); a.Collectors.Close(); a.logger.Info("app.resources_closed") })
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	if out, ok := w.(interface{ setErrorDetail(string) }); ok {
		out.setErrorDetail(message)
	}
	writeJSON(w, status, map[string]string{"error": message, "requestId": w.Header().Get("X-Request-ID")})
}
func (a *App) internalError(w http.ResponseWriter, r *http.Request, operation string, err error, message string) {
	logging.FromContext(r.Context(), a.logger).Error("store.operation_failed", "operation", operation, "error", logging.Clean(err.Error()))
	fail(w, http.StatusInternalServerError, message)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "Invalid JSON request or request too large")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "Expected one JSON object")
		return false
	}
	return true
}

func (a *App) Handler(dist string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "logstream_session", Value: a.token, Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/config", a.config)
	mux.HandleFunc("POST /api/groups", a.saveGroup)
	mux.HandleFunc("PUT /api/groups/{id}", a.saveGroup)
	mux.HandleFunc("DELETE /api/groups/{id}", a.deleteGroup)
	mux.HandleFunc("POST /api/hosts/probe", a.probe)
	mux.HandleFunc("POST /api/hosts", a.addHost)
	mux.HandleFunc("DELETE /api/hosts/{id}", a.deleteHost)
	mux.HandleFunc("PUT /api/hosts/{id}", a.updateHost)
	mux.HandleFunc("POST /api/hosts/{id}/duplicate", a.duplicateHost)
	mux.HandleFunc("PUT /api/sources/{id}", a.updateSource)
	mux.HandleFunc("POST /api/hosts/{id}/start", a.startHost)
	mux.HandleFunc("POST /api/hosts/{id}/stop", a.stopHost)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/logs", a.logs)
	mux.HandleFunc("POST /api/demo", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if !decode(w, r, &req) {
			return
		}
		a.setDemo(req.Enabled)
		writeJSON(w, 200, map[string]bool{"enabled": req.Enabled})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "Endpoint not found") })
	files := http.FileServer(http.Dir(dist))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, 405, "Method not allowed")
			return
		}
		if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
			http.Error(w, "Frontend not built. Run: cd web && npm install && npm run build", 503)
			return
		}
		files.ServeHTTP(w, r)
	})
	return a.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if !loopback(host) {
			fail(w, 403, "Logstream is a local application")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, "Cross-site requests are not allowed")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				allowed := err == nil && loopback(u.Hostname()) && (u.Scheme == "http" || u.Scheme == "https") && (u.Host == r.Host || u.Port() == "5173" || u.Port() == "5174")
				if !allowed {
					fail(w, 403, "Origin is not allowed")
					return
				}
			}
			if r.URL.Path != "/api/bootstrap" && r.URL.Path != "/api/health" {
				cookie, err := r.Cookie("logstream_session")
				if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.token)) != 1 {
					fail(w, 401, "Initialize your local session first")
					return
				}
			}
			if r.Method != "GET" && r.Method != "HEAD" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, 415, "Use application/json")
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
}
func loopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func validateHost(h *store.Host, trust bool) error {
	h.Name = strings.TrimSpace(h.Name)
	h.Address = strings.TrimSpace(h.Address)
	h.Username = strings.TrimSpace(h.Username)
	if h.Port == 0 {
		h.Port = 22
	}
	if h.Name == "" || len(h.Name) > 80 {
		return errors.New("Enter a host label of up to 80 characters")
	}
	if h.Address == "" || len(h.Address) > 253 || strings.ContainsAny(h.Address, " /\t\r\n\x00") {
		return errors.New("Enter a hostname or IP address without a port")
	}
	if h.Port < 1 || h.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	if h.Username == "" || len(h.Username) > 80 || strings.ContainsAny(h.Username, "\x00\r\n") {
		return errors.New("Enter an SSH username")
	}
	if trust && !fingerprintPattern.MatchString(h.Fingerprint) {
		return errors.New("Inspect and accept the server's SSH fingerprint first")
	}
	return nil
}
func (a *App) config(w http.ResponseWriter, r *http.Request) {
	hosts, sources, err := a.Store.List()
	if err != nil {
		a.internalError(w, r, "list_configuration", err, "Could not read configuration")
		return
	}
	groups, err := a.Store.Groups()
	if err != nil {
		a.internalError(w, r, "list_groups", err, "Could not read groups")
		return
	}
	a.demoMu.Lock()
	demo := a.demo
	a.demoMu.Unlock()
	statuses := a.Collectors.Statuses()
	if demo {
		hosts = append(hosts, demoHosts...)
		sources = append(sources, demoSources...)
		for _, s := range demoSources {
			statuses[s.ID] = collector.Status{State: "streaming", Detail: "Simulated demo source"}
		}
	}
	count, bytes := a.Hub.Stats()
	writeJSON(w, 200, map[string]any{"hosts": hosts, "sources": sources, "groups": groups, "statuses": statuses, "demo": demo, "buffer": map[string]int{"count": count, "bytes": bytes, "maxBytes": 16 * 1024 * 1024}})
}
func (a *App) probe(w http.ResponseWriter, r *http.Request) {
	var h store.Host
	if !decode(w, r, &h) {
		return
	}
	if err := validateHost(&h, false); err != nil {
		fail(w, 400, err.Error())
		return
	}
	fingerprint, err := collector.Probe(r.Context(), h)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"fingerprint": fingerprint})
}
func (a *App) addHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	var req struct {
		Host        store.Host            `json:"host"`
		Paths       []string              `json:"paths"`
		Parser      string                `json:"parser"`
		Credentials collector.Credentials `json:"credentials"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Parser == "" {
		req.Parser = stream.ParserRegular
	}
	if !stream.ValidParser(req.Parser) {
		fail(w, 400, "Choose regular or TX application parsing")
		return
	}
	if err := validateHost(&req.Host, true); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := collector.ValidateCredentials(req.Credentials); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > 16 {
		fail(w, 400, "Add between 1 and 16 log file paths")
		return
	}
	seen := map[string]bool{}
	for i, p := range req.Paths {
		p = strings.TrimSpace(p)
		if !validLogPath(p) || seen[p] {
			fail(w, 400, "Use unique absolute file paths, one per line")
			return
		}
		seen[p] = true
		req.Paths[i] = p
	}
	hosts, sources, err := a.Store.List()
	if err != nil {
		a.internalError(w, r, "list_configuration", err, "Could not read configuration")
		return
	}
	if len(hosts) >= 32 || len(sources)+len(req.Paths) > 128 {
		fail(w, 400, "Limit reached: 32 hosts or 128 files")
		return
	}
	req.Host.ID = store.ID()
	added, err := a.Store.AddWithParser(req.Host, req.Paths, req.Parser)
	if err != nil {
		a.internalError(w, r, "add_host", err, "Could not save host")
		return
	}
	logging.FromContext(r.Context(), a.logger).Info("host.created", "host_id", req.Host.ID, "host", req.Host.Name, "address", req.Host.Address, "sources", len(added))
	if err = a.Collectors.StartContext(r.Context(), req.Host, added, req.Credentials); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"host": req.Host, "sources": added})
}

var errHostNotFound = errors.New("host not found")

func (a *App) find(id string) (store.Host, []store.Source, error) {
	hosts, sources, err := a.Store.List()
	if err != nil {
		return store.Host{}, nil, err
	}
	for _, h := range hosts {
		if h.ID == id {
			out := []store.Source{}
			for _, s := range sources {
				if s.HostID == id {
					out = append(out, s)
				}
			}
			return h, out, nil
		}
	}
	return store.Host{}, nil, errHostNotFound
}
func (a *App) startHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	h, ss, err := a.find(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errHostNotFound) {
			fail(w, 404, "Host not found")
		} else {
			a.internalError(w, r, "find_host", err, "Could not read host")
		}
		return
	}
	var c collector.Credentials
	if !decode(w, r, &c) {
		return
	}
	if err = a.Collectors.StartContext(r.Context(), h, ss, c); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) stopHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	h, ss, err := a.find(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errHostNotFound) {
			fail(w, 404, "Host not found")
		} else {
			a.internalError(w, r, "find_host", err, "Could not read host")
		}
		return
	}
	a.Collectors.Stop(h.ID, ss)
	logging.FromContext(r.Context(), a.logger).Info("host.stopped", "host_id", h.ID, "sources", len(ss))
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) deleteHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	h, ss, err := a.find(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errHostNotFound) {
			fail(w, 404, "Host not found")
		} else {
			a.internalError(w, r, "find_host", err, "Could not read host")
		}
		return
	}
	a.Collectors.Stop(h.ID, ss)
	if err = a.Store.Delete(h.ID); err != nil {
		a.internalError(w, r, "delete_host", err, "Could not delete host")
		return
	}
	logging.FromContext(r.Context(), a.logger).Info("host.deleted", "host_id", h.ID, "sources", len(ss))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) events(w http.ResponseWriter, r *http.Request) {
	_, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, "Streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("since")
	}
	logger := logging.FromContext(r.Context(), a.logger).With("component", "stream")
	started := time.Now()
	reason := "client_disconnected"
	var streamErr error
	var batches, delivered, gaps int
	defer func() {
		if a.ctx.Err() != nil {
			reason = "server_shutdown"
		}
		attrs := []any{"reason", reason, "duration", time.Since(started), "batches", batches, "events", delivered, "gaps", gaps}
		if streamErr != nil && r.Context().Err() == nil && a.ctx.Err() == nil {
			attrs = append(attrs, "error", logging.Clean(streamErr.Error()))
			logger.Warn("stream.closed", attrs...)
		} else {
			logger.Info("stream.closed", attrs...)
		}
	}()
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, streamErr = fmt.Fprint(w, "retry: 1500\n\n"); streamErr != nil {
		reason = "write_failed"
		return
	}
	if streamErr = rc.Flush(); streamErr != nil {
		reason = "flush_failed"
		return
	}
	logger.Info("stream.opened", "resume_requested", cursor != "")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	write := func(kind, id string, data any) error {
		rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if id != "" {
			if _, err = fmt.Fprintf(w, "id: %s\n", id); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
		if err == nil {
			err = rc.Flush()
		}
		return err
	}
	send := func(kind, id string, data any) error {
		err := write(kind, id, data)
		if err != nil {
			streamErr = err
			reason = "write_failed"
		}
		return err
	}
	for {
		events, gap, latest := a.Hub.Read(cursor, 250)
		if gap {
			gaps++
			count, bytes := a.Hub.Stats()
			logger.Warn("stream.replay_gap", "retained_events", count, "buffer_bytes", bytes)
			if send("gap", "", map[string]string{"message": "Some logs are no longer available. The buffer expired or the backend restarted."}) != nil {
				return
			}
		}
		if len(events) > 0 {
			cursor = events[len(events)-1].ID
			if send("logs", cursor, events) != nil {
				return
			}
			batches++
			delivered += len(events)
		} else if cursor == "" || gap {
			cursor = latest
			if send("cursor", cursor, map[string]string{"cursor": cursor}) != nil {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			reason = "server_shutdown"
			return
		case <-ticker.C:
		case <-heartbeat.C:
			if send("heartbeat", "", map[string]string{"time": time.Now().UTC().Format(time.RFC3339)}) != nil {
				return
			}
		}
	}
}
func (a *App) logs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	q := query.Get("q")
	if len(q) > 1000 {
		fail(w, 400, "Search is too long")
		return
	}
	var pattern *regexp.Regexp
	var err error
	if query.Get("regex") == "true" {
		pattern, err = regexp.Compile("(?i)" + q)
		if err != nil {
			fail(w, 400, "Invalid regular expression")
			return
		}
	}
	limit := 3000
	if v, e := strconv.Atoi(query.Get("limit")); e == nil && v > 0 && v <= 3000 {
		limit = v
	}
	events, _, cursor := a.Hub.Read("", 0)
	out := []stream.Event{}
	var hostSources map[string]bool
	hostID, groupID := query.Get("hostId"), query.Get("groupId")
	if hostID != "" || groupID != "" {
		_, sources, err := a.Store.List()
		if err != nil {
			a.internalError(w, r, "list_sources", err, "Could not read sources")
			return
		}
		hostSources = map[string]bool{}
		var groupHosts map[string]bool
		if groupID != "" {
			groups, err := a.Store.Groups()
			if err != nil {
				a.internalError(w, r, "list_groups", err, "Could not read groups")
				return
			}
			groupHosts = map[string]bool{}
			for _, group := range groups {
				if group.ID == groupID {
					for _, id := range group.HostIDs {
						groupHosts[id] = true
					}
					break
				}
			}
		}
		for _, s := range append(sources, demoSources...) {
			if (hostID == "" || s.HostID == hostID) && (groupHosts == nil || groupHosts[s.HostID]) {
				hostSources[s.ID] = true
			}
		}
	}
	for _, e := range events {
		if hostSources != nil && !hostSources[e.SourceID] {
			continue
		}
		if s := query.Get("source"); s != "" && e.SourceID != s {
			continue
		}
		if l := query.Get("level"); l != "" && e.Level != l {
			continue
		}
		if q != "" {
			if pattern != nil {
				if !pattern.MatchString(e.Message) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(e.Message), strings.ToLower(q)) {
				continue
			}
		}
		out = append(out, e)
	}
	total := len(out)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	writeJSON(w, 200, map[string]any{"events": out, "cursor": cursor, "matched": total, "limit": limit})
}
