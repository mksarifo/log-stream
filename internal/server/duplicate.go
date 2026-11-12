package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"logstream/internal/logging"
	"logstream/internal/store"
)

func duplicateName(name string, hosts []store.Host) string {
	used := map[string]bool{}
	for _, host := range hosts {
		used[strings.ToLower(host.Name)] = true
	}
	for n := 1; ; n++ {
		suffix := " (copy)"
		if n > 1 {
			suffix = fmt.Sprintf(" (copy %d)", n)
		}
		base := name
		for len(base)+len(suffix) > 80 {
			_, size := utf8.DecodeLastRuneInString(base)
			base = base[:len(base)-size]
		}
		candidate := strings.TrimSpace(base) + suffix
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func (a *App) duplicateHost(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	var request struct{}
	if !decode(w, r, &request) {
		return
	}
	host, sources, err := a.find(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, errHostNotFound) {
			fail(w, 404, "Host not found")
		} else {
			a.internalError(w, r, "find_host", err, "Could not read host")
		}
		return
	}
	hosts, allSources, err := a.Store.List()
	if err != nil {
		a.internalError(w, r, "list_configuration", err, "Could not read configuration")
		return
	}
	if len(hosts) >= 32 || len(allSources)+len(sources) > 128 {
		fail(w, 400, "Limit reached: 32 hosts or 128 files")
		return
	}
	copy, added, err := a.Store.DuplicateHost(host.ID, duplicateName(host.Name, hosts))
	if err != nil {
		a.internalError(w, r, "duplicate_host", err, "Could not duplicate host")
		return
	}
	logging.FromContext(r.Context(), a.logger).Info("host.duplicated", "original_host_id", host.ID, "host_id", copy.ID, "sources", len(added))
	writeJSON(w, 201, map[string]any{"host": copy, "sources": added})
}
