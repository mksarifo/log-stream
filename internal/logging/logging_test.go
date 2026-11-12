package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONFileMirrorsConsoleAndConcurrentChildren(t *testing.T) {
	var console bytes.Buffer
	path := filepath.Join(t.TempDir(), "logs", "backend.log")
	logger, closer, err := New(Options{Level: "info", Format: "json", File: path, MaxBytes: 1024 * 1024, Backups: 2}, &console)
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("hidden")
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			child := logger.With("worker", worker)
			for i := 0; i < 30; i++ {
				child.Info("event", "duration", time.Millisecond, "sequence", i)
			}
		}(worker)
	}
	wg.Wait()
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, console.Bytes()) {
		t.Fatal("file and console differ")
	}
	lines := strings.Split(strings.TrimSpace(console.String()), "\n")
	if len(lines) != 120 {
		t.Fatalf("got %d records", len(lines))
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["service"] != "logstream" || record["duration"] != "1ms" {
			t.Fatalf("bad structured record: %s", line)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v", info.Mode())
	}
}

func TestRotationRetainsNewestRecordsAndReopensExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.log")
	if err := os.WriteFile(path, []byte("old-record\n"), 0644); err != nil {
		t.Fatal(err)
	}
	w, err := openRotating(path, 12, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{"one-record\n", "two-record\n", "three-record\n", "four-record\n"} {
		if _, err := w.writeLocked([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{"": "four-record\n", ".1": "three-record\n", ".2": "two-record\n"} {
		got, err := os.ReadFile(path + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", suffix, got, err)
		}
		info, _ := os.Stat(path + suffix)
		if info.Mode().Perm() != 0600 {
			t.Fatal("rotated file is not private")
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("backup limit exceeded")
	}
	if _, err := w.writeLocked([]byte("closed")); err == nil {
		t.Fatal("closed file accepted write")
	}
}

func TestFileFailureStillEmitsValidJSONToConsole(t *testing.T) {
	var console bytes.Buffer
	logger, closer, err := New(Options{Format: "json", File: filepath.Join(t.TempDir(), "backend.log"), MaxBytes: 1024, Backups: 1}, &console)
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	logger.Info("first")
	logger.Info("second")
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(console.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid fallback JSON: %s", line)
		}
		records = append(records, record)
	}
	if len(records) != 3 || records[1]["msg"] != "logging.file_write_failed" || records[2]["msg"] != "second" {
		t.Fatalf("bad fallback records: %+v", records)
	}
}

func TestInvalidOptionsAndContextFallback(t *testing.T) {
	for _, options := range []Options{{Level: "trace"}, {Format: "xml"}, {File: filepath.Join(t.TempDir(), "a"), MaxBytes: 0, Backups: 1}, {File: filepath.Join(t.TempDir(), "b"), MaxBytes: 10, Backups: 101}} {
		if _, _, err := New(options, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fallback := slog.Default()
	if FromContext(WithContext(context.Background(), logger), fallback) != logger || FromContext(context.Background(), fallback) != fallback {
		t.Fatal("lost log context")
	}
}
