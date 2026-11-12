package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"logstream/internal/store"
	"logstream/internal/stream"
)

func TestDuplicateConnectionIsDisconnectedAndPreservesOriginal(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	app.Collectors.Stop(host.ID, sources)
	before := app.Collectors.Statuses()
	app.Hub.Publish(stream.Normalize(sources[0].ID, host.Name, sources[0].Path, "original event"))
	endpoint := srv.URL + "/api/hosts/" + host.ID + "/duplicate"
	status, _ := groupRequest(t, http.DefaultClient, "POST", endpoint, map[string]any{})
	if status != 401 {
		t.Fatal("unauthenticated duplicate accepted")
	}
	status, body := groupRequest(t, client, "POST", endpoint, map[string]any{})
	var result struct {
		Host    store.Host     `json:"host"`
		Sources []store.Source `json:"sources"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if status != 201 || result.Host.Name != host.Name+" (copy)" || result.Host.ID == host.ID || len(result.Sources) != 2 {
		t.Fatalf("duplicate failed: %d", status)
	}
	for _, source := range sources {
		if app.Collectors.Statuses()[source.ID] != before[source.ID] {
			t.Fatal("original collector state changed")
		}
	}
	for _, source := range result.Sources {
		if _, ok := app.Collectors.Statuses()[source.ID]; ok {
			t.Fatal("duplicate started a collector")
		}
	}
	events, _, _ := app.Hub.Read("", 0)
	if len(events) != 1 || events[0].SourceID != sources[0].ID {
		t.Fatal("history changed")
	}
	status, body = groupRequest(t, client, "POST", endpoint, map[string]any{})
	json.Unmarshal(body, &result)
	if status != 201 || result.Host.Name != host.Name+" (copy 2)" {
		t.Fatal("duplicate labels collide")
	}
	status, _ = groupRequest(t, client, "POST", srv.URL+"/api/hosts/missing/duplicate", map[string]any{})
	if status != 404 {
		t.Fatal("missing host duplicated")
	}
	status, _ = groupRequest(t, client, "POST", endpoint, map[string]any{"credentials": "not accepted"})
	if status != 400 {
		t.Fatal("unexpected credential fields accepted")
	}
}

func TestDuplicateRespectsConfigurationLimits(t *testing.T) {
	for _, kind := range []string{"hosts", "sources"} {
		t.Run(kind, func(t *testing.T) {
			app, srv, client := setup(t)
			host, _ := editingFixture(t, app)
			count := 31
			if kind == "sources" {
				count = 8
			}
			for i := 0; i < count; i++ {
				paths := []string{"/log"}
				if kind == "sources" {
					paths = nil
					n := 16
					if i == 7 {
						n = 14
					}
					for j := 0; j < n; j++ {
						paths = append(paths, fmt.Sprintf("/%d.log", j))
					}
				}
				if _, err := app.Store.Add(store.Host{ID: fmt.Sprintf("fixture-%d", i)}, paths); err != nil {
					t.Fatal(err)
				}
			}
			status, _ := groupRequest(t, client, "POST", srv.URL+"/api/hosts/"+host.ID+"/duplicate", map[string]any{})
			if status != 400 {
				t.Fatal("duplicate exceeded configuration limits")
			}
		})
	}
}

func TestDuplicateNameBoundsUTF8AndAvoidsCollisions(t *testing.T) {
	name := strings.Repeat("é", 40)
	first := duplicateName(name, nil)
	second := duplicateName(name, []store.Host{{Name: first}})
	if len(first) > 80 || len(second) > 80 || !utf8.ValidString(first) || !utf8.ValidString(second) || first == second {
		t.Fatal("copy names invalid or collide")
	}
}
