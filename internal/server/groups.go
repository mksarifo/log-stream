package server

import (
	"errors"
	"net/http"
	"strings"
	"unicode"

	"logstream/internal/logging"
	"logstream/internal/store"
)

func (a *App) saveGroup(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	var request struct {
		Name    string   `json:"name"`
		HostIDs []string `json:"hostIds"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 80 || strings.ContainsFunc(request.Name, unicode.IsControl) {
		fail(w, 400, "Enter a group name of up to 80 characters without control characters")
		return
	}
	if len(request.HostIDs) > 32 {
		fail(w, 400, "A group can contain up to 32 hosts")
		return
	}
	create := r.Method == http.MethodPost
	group := store.Group{ID: r.PathValue("id"), Name: request.Name, HostIDs: []string{}}
	seen := map[string]bool{}
	for _, id := range request.HostIDs {
		if id == "" || len(id) > 80 {
			fail(w, 400, "Select valid saved hosts")
			return
		}
		if !seen[id] {
			group.HostIDs = append(group.HostIDs, id)
			seen[id] = true
		}
	}
	if create {
		groups, err := a.Store.Groups()
		if err != nil {
			a.internalError(w, r, "list_groups", err, "Could not read groups")
			return
		}
		if len(groups) >= 64 {
			fail(w, 400, "Limit reached: 64 server groups")
			return
		}
		group.ID = store.ID()
	}
	if err := a.Store.SaveGroup(group, create); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			fail(w, 404, "Group not found")
		case errors.Is(err, store.ErrGroupNameExists):
			fail(w, 409, "Another group already uses this name")
		case errors.Is(err, store.ErrGroupHostMissing):
			fail(w, 400, "A selected host no longer exists. Refresh and try again.")
		default:
			a.internalError(w, r, "save_group", err, "Could not save group")
		}
		return
	}
	status, event := 200, "group.updated"
	if create {
		status, event = 201, "group.created"
	}
	logging.FromContext(r.Context(), a.logger).Info(event, "group_id", group.ID, "hosts", len(group.HostIDs))
	writeJSON(w, status, group)
}

func (a *App) deleteGroup(w http.ResponseWriter, r *http.Request) {
	a.mutate.Lock()
	defer a.mutate.Unlock()
	id := r.PathValue("id")
	if err := a.Store.DeleteGroup(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(w, 404, "Group not found")
		} else {
			a.internalError(w, r, "delete_group", err, "Could not delete group")
		}
		return
	}
	logging.FromContext(r.Context(), a.logger).Info("group.deleted", "group_id", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
