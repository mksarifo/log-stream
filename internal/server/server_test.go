package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"logstream/internal/store"
	"logstream/internal/stream"
)

func setup(t *testing.T) (*App, *httptest.Server, *http.Client) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := New(context.Background(), s)
	srv := httptest.NewServer(app.Handler(t.TempDir()))
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 4 * time.Second}
	resp, err := client.Get(srv.URL + "/api/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Cleanup(func() { app.Close(); srv.Close(); s.Close() })
	return app, srv, client
}
func TestLocalSessionAndOriginProtection(t *testing.T) {
	_, srv, client := setup(t)
	resp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated config accessible")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/demo", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.Host = "evil.example"
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("DNS-rebinding hostname accepted")
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/demo", strings.NewReader(`{"enabled":true}`))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Fatal("unsafe content type accepted")
	}
}
func TestLogSearchAndSSEReplay(t *testing.T) {
	app, srv, client := setup(t)
	app.Hub.Publish(stream.Normalize("s1", "api", "/api.log", "INFO request served"))
	app.Hub.Publish(stream.Normalize("s2", "worker", "/worker.log", "ERROR upstream timeout"))
	all, _, _ := app.Hub.Read("", 0)
	resp, err := client.Get(srv.URL + "/api/logs?q=upstream&level=error")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Events  []stream.Event `json:"events"`
		Matched int            `json:"matched"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if len(result.Events) != 1 || result.Events[0].SourceID != "s2" || result.Matched != 1 {
		t.Fatalf("search failed: %+v", result)
	}
	resp, err = client.Get(srv.URL + "/api/logs?regex=true&q=%28")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("invalid regex accepted")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.Header.Set("Last-Event-ID", all[0].ID)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var payload string
	for i := 0; i < 12; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			payload = line
			break
		}
	}
	if !strings.Contains(payload, "upstream timeout") || strings.Contains(payload, "request served") {
		t.Fatalf("bad SSE replay: %s", payload)
	}
}
func TestDemoLifecycleAndSecretFreeConfig(t *testing.T) {
	app, srv, client := setup(t)
	app.setDemo(true)
	app.setDemo(true)
	resp, err := client.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"demo":true`) || strings.Contains(string(body), "password") || strings.Contains(string(body), "privateKey") {
		t.Fatalf("invalid config: %s", body)
	}
	app.setDemo(false)
	before, _ := app.Hub.Stats()
	time.Sleep(700 * time.Millisecond)
	after, _ := app.Hub.Stats()
	if before != after {
		t.Fatal("demo continued after stop")
	}
}
