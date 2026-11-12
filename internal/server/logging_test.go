package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestLogsExcludeBodiesQueriesAndCookies(t *testing.T) {
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	handler := app.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	req := httptest.NewRequest("POST", "http://localhost/api/hosts/private-id/start?q=secret-query", strings.NewReader(`{"password":"secret-password","privateKey":"secret-key"}`))
	req.Header.Set("Cookie", "logstream_session=secret-cookie")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	text := buffer.String()
	if !strings.Contains(text, "http.request") || !strings.Contains(text, "status=400") || !strings.Contains(text, "/api/hosts/{id}/start") || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("missing request context: %s", text)
	}
	for _, secret := range []string{"secret-query", "secret-password", "secret-key", "secret-cookie", "private-id"} {
		if strings.Contains(text, secret) {
			t.Fatalf("request log leaked %s", secret)
		}
	}
}

func TestOneCorrelatedErrorRecordAndResponse(t *testing.T) {
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewTextHandler(&buffer, nil))}
	response := httptest.NewRecorder()
	app.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail(w, 400, "Invalid request") })).ServeHTTP(response, httptest.NewRequest("POST", "http://localhost/api/hosts", nil))
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id := response.Header().Get("X-Request-ID")
	if id == "" || body["requestId"] != id || strings.Count(buffer.String(), "http.request") != 1 || strings.Contains(buffer.String(), "http.response_error") || !strings.Contains(buffer.String(), `detail="Invalid request"`) {
		t.Fatalf("missing/duplicate error correlation: %s", buffer.String())
	}
}

func TestPanicRecoveryDoesNotExposePanicValue(t *testing.T) {
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	response := httptest.NewRecorder()
	app.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("private-key-do-not-log") })).ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/api/config", nil))
	if response.Code != 500 || !strings.Contains(buffer.String(), "http.panic") || !strings.Contains(buffer.String(), "http.panic_stack") {
		t.Fatal("panic not recovered and diagnosed")
	}
	if strings.Contains(buffer.String(), "private-key-do-not-log") || strings.Contains(response.Body.String(), "private-key-do-not-log") {
		t.Fatal("panic value exposed")
	}
}

func TestPanicAfterStreamStartsAbortsConnection(t *testing.T) {
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewTextHandler(&buffer, nil))}
	handler := app.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("partial stream")); panic("sensitive") }))
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("a partial stream must be aborted")
		}
		if strings.Contains(buffer.String(), "sensitive") {
			t.Fatal("panic value leaked")
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/api/events", nil))
}

type informationalWriter struct {
	streamingWriter
	statuses []int
}

func (w *informationalWriter) WriteHeader(status int) { w.statuses = append(w.statuses, status) }
func TestInformationalResponseDoesNotHideFinalStatus(t *testing.T) {
	underlying := &informationalWriter{streamingWriter: streamingWriter{header: make(http.Header)}}
	w := &loggedResponse{ResponseWriter: underlying}
	w.WriteHeader(103)
	w.WriteHeader(201)
	w.Write([]byte("created"))
	if w.status != 201 || len(underlying.statuses) != 2 || underlying.statuses[1] != 201 {
		t.Fatal("informational response suppressed final status")
	}
}

type streamingWriter struct {
	header   http.Header
	flushed  bool
	deadline time.Time
}

func (w *streamingWriter) Header() http.Header         { return w.header }
func (w *streamingWriter) WriteHeader(int)             {}
func (w *streamingWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *streamingWriter) Flush()                      { w.flushed = true }
func (w *streamingWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func TestLoggingPreservesStreamingAndDeadlines(t *testing.T) {
	underlying := &streamingWriter{header: make(http.Header)}
	wrapped := &loggedResponse{ResponseWriter: underlying}
	controller := http.NewResponseController(wrapped)
	deadline := time.Now().Add(time.Second)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := controller.Flush(); err != nil {
		t.Fatal(err)
	}
	if !underlying.flushed || !underlying.deadline.Equal(deadline) || wrapped.status != 200 {
		t.Fatal("logging wrapper broke SSE")
	}
}

func TestSuccessfulPollingIsQuietAtInfo(t *testing.T) {
	var buffer bytes.Buffer
	app := &App{logger: slog.New(slog.NewTextHandler(&buffer, nil))}
	app.requestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://localhost/api/config", nil).WithContext(context.Background()))
	if buffer.Len() != 0 {
		t.Fatal("successful polling should require debug verbosity")
	}
}
