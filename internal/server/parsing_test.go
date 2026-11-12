package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"logstream/internal/store"
	"logstream/internal/stream"
)

func TestSourceParserSelectionValidationAndSearch(t *testing.T) {
	app, srv, client := setup(t)
	host, sources := editingFixture(t, app)
	endpoint := srv.URL + "/api/sources/" + sources[0].ID
	status, _ := put(t, client, endpoint, map[string]any{"path": sources[0].Path, "parser": "unknown"})
	if status != 400 {
		t.Fatal("invalid parser accepted")
	}
	status, result := put(t, client, endpoint, map[string]any{"path": sources[0].Path, "parser": "tx"})
	if status != 200 || string(result["reconnectRequired"]) != "true" {
		t.Fatal("parser change did not require reconnect")
	}
	_, after, _ := app.Store.List()
	if after[0].Parser != "tx" || after[1].Parser != "regular" {
		t.Fatal("per-source parser not saved independently")
	}
	status, result = put(t, client, endpoint, map[string]any{"path": sources[0].Path, "parser": "tx"})
	if status != 200 || string(result["reconnectRequired"]) != "false" {
		t.Fatal("unchanged parser stopped collection")
	}
	// Older API clients omitting parser must preserve the existing selection.
	status, _ = put(t, client, endpoint, map[string]any{"path": "/new-tx.log"})
	_, after, _ = app.Store.List()
	if status != 200 || after[0].Parser != "tx" {
		t.Fatal("path-only edit reset parser")
	}
	p := stream.NewParser(stream.ParserTX, sources[0].ID, host.Name, sources[0].Path)
	p.Push("12:00:00 Decoder/EventWorker/Pool (thread no:2, pid:3) :: *** Debugging *** Request Body:")
	p.Push(`{"command":"AUTHENTICATE"}`)
	for _, event := range p.Flush() {
		app.Hub.Publish(event)
	}
	status, body := groupRequest(t, client, http.MethodGet, srv.URL+"/api/logs?q=AUTHENTICATE&level=debug", nil)
	var response struct {
		Events []stream.Event `json:"events"`
	}
	json.Unmarshal(body, &response)
	if status != 200 || len(response.Events) != 1 || response.Events[0].Fields["payload"] == nil || !strings.Contains(response.Events[0].Message, "\n{") {
		t.Fatal("multiline payload no longer searchable")
	}
}

func TestNewHostParserDefaultsAndSelection(t *testing.T) {
	app, srv, client := setup(t)
	for _, mode := range []string{"", "tx", "invalid"} {
		host := store.Host{Name: "fixture", Address: "127.0.0.1", Port: 1, Username: "user", Fingerprint: "SHA256:" + strings.Repeat("A", 43)}
		request := map[string]any{"host": host, "paths": []string{"/one.log", "/two.log"}, "credentials": map[string]string{"method": "password", "password": "example-only"}}
		if mode != "" {
			request["parser"] = mode
		}
		status, body := groupRequest(t, client, "POST", srv.URL+"/api/hosts", request)
		if mode == "invalid" {
			if status != 400 {
				t.Fatal("invalid new-host parser accepted")
			}
			continue
		}
		var result struct {
			Sources []store.Source `json:"sources"`
		}
		json.Unmarshal(body, &result)
		expected := mode
		if expected == "" {
			expected = "regular"
		}
		if status != 201 || len(result.Sources) != 2 || result.Sources[0].Parser != expected || result.Sources[1].Parser != expected {
			t.Fatal("new source parser not applied")
		}
	}
	app.Collectors.Close()
}
