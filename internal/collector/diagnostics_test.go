package collector

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"logstream/internal/logging"
	"logstream/internal/store"
	"logstream/internal/stream"
)

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func TestDiagnosticErrorRedaction(t *testing.T) {
	c := Credentials{Password: "secret-password", PrivateKey: "-----BEGIN PRIVATE KEY-----\r\nSENSITIVEKEYLINE\r\n-----END PRIVATE KEY-----", Passphrase: "secret-passphrase"}
	err := errors.New("failed for vault@target@host: secret-password secret-passphrase SENSITIVEKEYLINE\nInjected log\x1b")
	got := safeError(err, c, "vault@target@host")
	for _, secret := range []string{"secret-password", "secret-passphrase", "SENSITIVEKEYLINE", "vault@target@host", "\n", "\x1b"} {
		if strings.Contains(got, secret) {
			t.Fatalf("diagnostic leaked secret or control character %q", secret)
		}
	}
	if len(safeError(errors.New(strings.Repeat("x", 5000)), Credentials{}, "")) > 1100 {
		t.Fatal("diagnostic was not bounded")
	}
}
func TestFailureExplainsStageAndRetries(t *testing.T) {
	tests := []struct {
		err         error
		stage, text string
		terminal    bool
	}{
		{operation("tcp_connect", errors.New("connection refused")), "tcp_connect", "TCP connection", false},
		{operation("tcp_connect", &net.DNSError{Err: "no such host", Name: "missing.invalid"}), "tcp_connect", "Hostname lookup", false},
		{operation("ssh_handshake", errors.New("ssh: unable to authenticate, attempted methods [none password]")), "ssh_handshake", "connection-reason", true},
		{operation("ssh_session", &ssh.OpenChannelError{Reason: ssh.Prohibited, Message: "not allowed"}), "ssh_session", "CyberArk", true},
		{operation("remote_exec", errors.New("ssh: command failed")), "remote_exec", "tail command", false},
	}
	for _, test := range tests {
		stage, detail, terminal := failure(test.err)
		if stage != test.stage || !strings.Contains(detail, test.text) || terminal != test.terminal {
			t.Errorf("unexpected classification: %s %s %v", stage, detail, terminal)
		}
	}
}
func TestRemoteDiagnostics(t *testing.T) {
	for _, test := range []struct{ line, state, text string }{
		{"tail: cannot open '/log' for reading: Permission denied", "waiting", "permission denied"},
		{"tail: cannot open '/log': No such file or directory", "waiting", "missing"},
		{"sh: exec: tail: not found", "error", "not found"},
		{"tail: unrecognized option '--retry'", "error", "flags"},
		{"an interactive terminal is required", "error", "terminal"},
	} {
		state, detail := remoteDiagnostic(test.line)
		if state != test.state || !strings.Contains(detail, test.text) {
			t.Errorf("bad stderr classification: %s %s", state, detail)
		}
	}
}
func TestFailedLoginIsLoggedWithoutPassword(t *testing.T) {
	h, _ := testSSH(t)
	manager := New(context.Background(), stream.New(100, 32768))
	defer manager.Close()
	buffer := &logBuffer{}
	manager.logger = slog.New(slog.NewTextHandler(buffer, nil))
	if err := manager.Start(h, []store.Source{{ID: "s", HostID: h.ID, Path: "/log"}}, Credentials{Method: "password", Password: "bad-password-do-not-log"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Statuses()["s"].State != "error" {
		if time.Now().After(deadline) {
			t.Fatalf("login did not fail: %v", manager.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	text := buffer.String()
	if !strings.Contains(text, "ssh.collection_failed") || !strings.Contains(text, "stage=ssh_handshake") || !strings.Contains(text, "unable to authenticate") {
		t.Fatalf("missing actionable error: %s", text)
	}
	if strings.Contains(text, "bad-password-do-not-log") {
		t.Fatal("password logged")
	}
}

func TestRejectedCommandReportsStageInsteadOfGenericRetry(t *testing.T) {
	h, _ := testSSHScenario(t, "reject-exec")
	m := New(context.Background(), stream.New(100, 32768))
	defer m.Close()
	buffer := &logBuffer{}
	m.logger = slog.New(slog.NewTextHandler(buffer, nil))
	if err := m.Start(h, []store.Source{{ID: "source", HostID: h.ID, Path: "/log"}}, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.Statuses()["source"].State != "reconnecting" {
		if time.Now().After(deadline) {
			t.Fatalf("command failure not reported: %v", m.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(m.Statuses()["source"].Detail, "tail command") {
		t.Fatalf("non-actionable status: %v", m.Statuses())
	}
	text := buffer.String()
	if !strings.Contains(text, "stage=remote_exec") || !strings.Contains(text, "retry_in=1s") || !strings.Contains(text, "ssh.collection_failed") {
		t.Fatalf("missing command diagnostics: %s", text)
	}
}

func TestRemoteStderrDiagnosesPermissionsAndRedactsCredentials(t *testing.T) {
	h, _ := testSSHScenario(t, "permission-denied")
	m := New(context.Background(), stream.New(100, 32768))
	defer m.Close()
	buffer := &logBuffer{}
	m.logger = slog.New(slog.NewTextHandler(buffer, nil))
	if err := m.Start(h, []store.Source{{ID: "source", HostID: h.ID, Path: "/log"}}, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(buffer.String(), "ssh.remote_stderr") {
		if time.Now().After(deadline) {
			t.Fatalf("stderr not logged: %v", m.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status := m.Statuses()["source"]; status.State != "waiting" || !strings.Contains(status.Detail, "permission denied") {
		t.Fatalf("missing permission status: %+v", status)
	}
	if strings.Contains(buffer.String(), "test-password") {
		t.Fatal("remote stderr leaked credentials")
	}
}

func TestRequestCorrelationSurvivesRequestCancellation(t *testing.T) {
	h, _ := testSSH(t)
	hub := stream.New(100, 32768)
	m := New(context.Background(), hub)
	defer m.Close()
	buffer := &logBuffer{}
	m.logger = slog.New(slog.NewTextHandler(buffer, nil))
	request, cancel := context.WithCancel(logging.WithContext(context.Background(), m.logger.With("request_id", "request-123")))
	if err := m.StartContext(request, h, []store.Source{{ID: "source", HostID: h.ID, Path: "/api.log"}}, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		count, _ := hub.Stats()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request cancellation stopped worker: %v", m.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Stop(h.ID, []store.Source{{ID: "source"}})
	text := buffer.String()
	for _, field := range []string{"request_id=request-123", "connection_id=", "host_id=host", "source_id=source", "path=/api.log", "ssh.first_event", "ssh.tail_stopped", "lines=1"} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s in %s", field, text)
		}
	}
	if strings.Contains(text, "test-password") {
		t.Fatal("credential leaked")
	}
}
