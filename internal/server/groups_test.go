package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"logstream/internal/store"
	"logstream/internal/stream"
)

func groupRequest(t *testing.T, client *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(method, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result json.RawMessage
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}

func TestGroupCRUDAndValidation(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	endpoint := srv.URL + "/api/groups"
	for _, name := range []string{"", "   ", strings.Repeat("a", 81), "invalid\nname"} {
		status, _ := groupRequest(t, client, "POST", endpoint, map[string]any{"name": name})
		if status != 400 {
			t.Fatalf("invalid name accepted: %d", status)
		}
	}
	status, _ := groupRequest(t, http.DefaultClient, "POST", endpoint, map[string]any{"name": "Private"})
	if status != 401 {
		t.Fatal("unauthenticated group creation allowed")
	}
	status, _ = groupRequest(t, client, "POST", endpoint, map[string]any{"name": "Unknown", "hostIds": []string{"missing"}})
	if status != 400 {
		t.Fatal("missing host allowed")
	}
	status, body := groupRequest(t, client, "POST", endpoint, map[string]any{"name": " Production ", "hostIds": []string{host.ID, host.ID}})
	var group store.Group
	json.Unmarshal(body, &group)
	if status != 201 || group.ID == "" || group.Name != "Production" || len(group.HostIDs) != 1 {
		t.Fatalf("invalid creation: %d %s", status, body)
	}
	status, _ = groupRequest(t, client, "POST", endpoint, map[string]any{"name": "production"})
	if status != 409 {
		t.Fatal("duplicate name allowed")
	}
	// Stop states are preserved; group management must not reset a collector.
	app.Collectors.Stop(host.ID, sources)
	before := app.Collectors.Statuses()
	status, _ = groupRequest(t, client, "PUT", endpoint+"/"+group.ID, map[string]any{"name": "Payments", "hostIds": []string{host.ID}})
	if status != 200 {
		t.Fatal("rename failed")
	}
	for _, source := range sources {
		if app.Collectors.Statuses()[source.ID] != before[source.ID] {
			t.Fatal("group edit changed collector state")
		}
	}
	status, body = groupRequest(t, client, "GET", srv.URL+"/api/config", nil)
	var config struct {
		Groups []store.Group `json:"groups"`
	}
	json.Unmarshal(body, &config)
	if status != 200 || len(config.Groups) != 1 || config.Groups[0].Name != "Payments" || len(config.Groups[0].HostIDs) != 1 {
		t.Fatalf("group missing from config: %s", body)
	}
	status, _ = groupRequest(t, client, "DELETE", endpoint+"/"+group.ID, nil)
	if status != 200 {
		t.Fatal("delete failed")
	}
	hosts, after, _ := app.Store.List()
	if len(hosts) != 1 || len(after) != len(sources) {
		t.Fatal("group deletion removed server")
	}
	status, _ = groupRequest(t, client, "PUT", endpoint+"/missing", map[string]any{"name": "Missing"})
	if status != 404 {
		t.Fatal("missing update accepted")
	}
	status, _ = groupRequest(t, client, "DELETE", endpoint+"/missing", nil)
	if status != 404 {
		t.Fatal("missing deletion accepted")
	}
}

func TestGroupSearchIncludesAllMemberSourcesAndIntersectsOtherFilters(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	other := store.Host{ID: "other", Name: "other", Address: "localhost", Port: 22, Username: "user"}
	otherSources, err := app.Store.Add(other, []string{"/other.log"})
	if err != nil {
		t.Fatal(err)
	}
	group := store.Group{ID: "group", Name: "Production", HostIDs: []string{host.ID}}
	if err = app.Store.SaveGroup(group, true); err != nil {
		t.Fatal(err)
	}
	app.Hub.Publish(stream.Normalize(sources[0].ID, host.Name, sources[0].Path, "INFO request served"))
	app.Hub.Publish(stream.Normalize(sources[1].ID, host.Name, sources[1].Path, "ERROR request failed"))
	app.Hub.Publish(stream.Normalize(otherSources[0].ID, other.Name, otherSources[0].Path, "ERROR request failed elsewhere"))
	app.Collectors.Stop(host.ID, sources)
	for _, tc := range []struct {
		query string
		count int
	}{
		{"groupId=group", 2},
		{"groupId=group&regex=true&q=request.*failed&level=error", 1},
		{"groupId=group&source=" + url.QueryEscape(sources[1].ID), 1},
		{"groupId=group&hostId=other", 0},
		{"groupId=group&source=" + url.QueryEscape(otherSources[0].ID), 0},
		{"groupId=missing", 0},
	} {
		status, body := groupRequest(t, client, "GET", srv.URL+"/api/logs?"+tc.query, nil)
		var result struct {
			Events []stream.Event `json:"events"`
		}
		json.Unmarshal(body, &result)
		if status != 200 || len(result.Events) != tc.count {
			t.Fatalf("%s returned %d events: %s", tc.query, len(result.Events), body)
		}
	}
	// Replace membership: history is filtered using the current saved group.
	group.HostIDs = []string{other.ID}
	if err = app.Store.SaveGroup(group, false); err != nil {
		t.Fatal(err)
	}
	_, body := groupRequest(t, client, "GET", srv.URL+"/api/logs?groupId=group", nil)
	if !strings.Contains(string(body), "elsewhere") || strings.Contains(string(body), "served") {
		t.Fatal("search ignored updated membership")
	}
	if app.Collectors.Statuses()[sources[0].ID].State != "stopped" {
		t.Fatal("group edit changed collector")
	}
}
