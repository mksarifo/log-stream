package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"logstream/internal/collector"
	"logstream/internal/store"
	"logstream/internal/stream"
)

func editingFixture(t *testing.T, app *App) (store.Host, []store.Source) {
	t.Helper()
	h := store.Host{ID: "editable", Name: "before", Address: "127.0.0.1", Port: 22, Username: "vault@target@host", Fingerprint: "SHA256:" + strings.Repeat("A", 43)}
	sources, err := app.Store.Add(h, []string{"/one.log", "/two.log"})
	if err != nil {
		t.Fatal(err)
	}
	return h, sources
}
func put(t *testing.T, client *http.Client, url string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("PUT", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result map[string]json.RawMessage
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}
func TestEditHostPreservesSourcesAndBufferedLogs(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	app.Hub.Publish(stream.Normalize(sources[0].ID, host.Name, sources[0].Path, "existing log"))
	host.Name = "after"
	host.Username = "new-vault@target@host"
	status, result := put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": host, "trustHostKey": false})
	if status != 200 || string(result["reconnectRequired"]) != "true" {
		t.Fatalf("edit failed: %d %s", status, result)
	}
	hosts, after, err := app.Store.List()
	if err != nil || hosts[0] != host || len(after) != 2 || after[0] != sources[0] || after[1] != sources[1] {
		t.Fatal("host edit lost attached sources")
	}
	events, _, _ := app.Hub.Read("", 0)
	if len(events) != 1 || events[0].Host != "before" || events[0].Message != "existing log" {
		t.Fatal("editing rewrote or removed history")
	}
	status, result = put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": host})
	if status != 200 || string(result["reconnectRequired"]) != "false" {
		t.Fatal("unchanged edit requires reconnect")
	}
}
func TestChangedEndpointRequiresExplicitTrustAndValidHost(t *testing.T) {
	app, srv, client := setup(t)
	host, _ := editingFixture(t, app)
	updated := host
	updated.Address = "new.example"
	status, _ := put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": updated, "trustHostKey": false})
	if status != 400 {
		t.Fatal("new endpoint implicitly trusted")
	}
	hosts, _, _ := app.Store.List()
	if hosts[0] != host {
		t.Fatal("rejected edit changed config")
	}
	updated.Fingerprint = "SHA256:" + strings.Repeat("B", 43)
	status, _ = put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": updated, "trustHostKey": true})
	if status != 200 {
		t.Fatal("verified new endpoint rejected")
	}
	updated.ID = "another"
	status, _ = put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": updated, "trustHostKey": true})
	if status != 400 {
		t.Fatal("primary key edit accepted")
	}
}
func TestEditSourceValidatesPathAndPreservesOtherFiles(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	url := srv.URL + "/api/sources/" + sources[0].ID
	for _, path := range []string{"relative.log", "/bad\npath", "/bad\x00path", strings.Repeat("x", 4097)} {
		status, _ := put(t, client, url, map[string]any{"path": path})
		if status != 400 {
			t.Errorf("invalid path accepted: status %d", status)
		}
	}
	status, _ := put(t, client, url, map[string]any{"path": sources[1].Path})
	if status != 409 {
		t.Fatal("duplicate source accepted")
	}
	status, _ = put(t, client, url, map[string]any{"path": "/new.log", "hostId": "other"})
	if status != 400 {
		t.Fatal("source ownership field accepted")
	}
	status, result := put(t, client, url, map[string]any{"path": " /new.log "})
	if status != 200 || string(result["reconnectRequired"]) != "true" {
		t.Fatal("source edit failed")
	}
	_, after, _ := app.Store.List()
	if after[0].ID != sources[0].ID || after[0].HostID != host.ID || after[0].Path != "/new.log" || after[1] != sources[1] {
		t.Fatal("source edit affected another file or identity")
	}
	status, result = put(t, client, url, map[string]any{"path": "/new.log"})
	if status != 200 || string(result["reconnectRequired"]) != "false" {
		t.Fatal("unchanged source edit requires reconnect")
	}
	status, _ = put(t, client, srv.URL+"/api/sources/missing", map[string]any{"path": "/new.log"})
	if status != 404 {
		t.Fatal("missing source created")
	}
	status, _ = put(t, http.DefaultClient, url, map[string]any{"path": "/private.log"})
	if status != 401 {
		t.Fatal("unauthenticated edit accepted")
	}
}

func TestRejectedAndUnchangedEditsKeepCollectorsRunning(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	host.Port = listener.Addr().(*net.TCPAddr).Port
	if err = app.Store.UpdateHost(host); err != nil {
		t.Fatal(err)
	}
	if err = app.Collectors.Start(host, sources, collector.Credentials{Method: "password", Password: "not-logged"}); err != nil {
		t.Fatal(err)
	}
	status, _ := put(t, client, srv.URL+"/api/hosts/"+host.ID, map[string]any{"host": host})
	if status != 200 {
		t.Fatal("no-op host edit failed")
	}
	status, _ = put(t, client, srv.URL+"/api/sources/"+sources[0].ID, map[string]any{"path": sources[1].Path})
	if status != 409 {
		t.Fatal("duplicate edit accepted")
	}
	for _, source := range sources {
		if app.Collectors.Statuses()[source.ID].State == "stopped" {
			t.Fatal("rejected or unchanged edit stopped collection")
		}
	}
	status, _ = put(t, client, srv.URL+"/api/sources/"+sources[0].ID, map[string]any{"path": "/updated.log"})
	if status != 200 {
		t.Fatal("valid edit failed")
	}
	for _, source := range sources {
		if app.Collectors.Statuses()[source.ID].State != "stopped" {
			t.Fatal("configuration changed while old collector remained active")
		}
	}
}
