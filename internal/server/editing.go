package server

import (
	"errors"
	"net/http"
	"strings"

	"logstream/internal/logging"
	"logstream/internal/store"
	"logstream/internal/stream"
)

func (a *App) updateHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	old, sources, err := a.find(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errHostNotFound) {
			fail(w, 404, "Host not found")
		} else {
			a.internalError(w, r, "find_host", err, "Could not read host")
		}
		return
	}
	var request struct {
		Host         store.Host `json:"host"`
		TrustHostKey bool       `json:"trustHostKey"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Host.ID != "" && request.Host.ID != old.ID {
		fail(w, 400, "Host ID cannot be changed")
		return
	}
	host := request.Host
	host.ID = old.ID
	if err := validateHost(&host, true); err != nil {
		fail(w, 400, err.Error())
		return
	}
	identityChanged := host.Address != old.Address || host.Port != old.Port || host.Fingerprint != old.Fingerprint
	if identityChanged && !request.TrustHostKey {
		fail(w, 400, "Inspect and accept the SSH host key for the updated server before saving")
		return
	}
	changed := host != old
	if changed {
		if err := a.Store.UpdateHost(host); err != nil {
			a.internalError(w, r, "update_host", err, "Could not save host")
			return
		}
		// Commit first: a rejected/failed edit must not interrupt working collectors.
		a.Collectors.Stop(old.ID, sources)
	}
	logging.FromContext(r.Context(), a.logger).Info("host.updated", "host_id", host.ID, "changed", changed, "reconnect_required", changed)
	writeJSON(w, 200, map[string]any{"host": host, "reconnectRequired": changed})
}

func (a *App) updateSource(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	_, sources, err := a.Store.List()
	if err != nil {
		a.internalError(w, r, "list_sources", err, "Could not read sources")
		return
	}
	var original store.Source
	for _, source := range sources {
		if source.ID == r.PathValue("id") {
			original = source
			break
		}
	}
	if original.ID == "" {
		fail(w, 404, "Source not found")
		return
	}
	var request struct {
		Path   string  `json:"path"`
		Parser *string `json:"parser"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.Path = strings.TrimSpace(request.Path)
	if !validLogPath(request.Path) {
		fail(w, 400, "Enter an absolute log file path of up to 4096 characters")
		return
	}
	host, hostSources, err := a.find(original.HostID)
	if err != nil {
		a.internalError(w, r, "find_host", err, "Could not read host")
		return
	}
	updated := original
	updated.Path = request.Path
	if request.Parser != nil {
		if !stream.ValidParser(*request.Parser) {
			fail(w, 400, "Choose regular or TX application parsing")
			return
		}
		updated.Parser = *request.Parser
	}
	changed := updated != original
	if changed {
		if err := a.Store.UpdateSource(updated); err != nil {
			if errors.Is(err, store.ErrSourcePathExists) {
				fail(w, 409, "Another source on this host already uses this path")
			} else {
				a.internalError(w, r, "update_source", err, "Could not save source")
			}
			return
		}
		a.Collectors.Stop(host.ID, hostSources)
	}
	logging.FromContext(r.Context(), a.logger).Info("source.updated", "host_id", host.ID, "source_id", updated.ID, "parser", updated.Parser, "changed", changed, "reconnect_required", changed)
	writeJSON(w, 200, map[string]any{"source": updated, "host": host, "reconnectRequired": changed})
}

func validLogPath(path string) bool {
	return strings.HasPrefix(path, "/") && len(path) <= 4096 && !strings.ContainsAny(path, "\x00\r\n")
}
