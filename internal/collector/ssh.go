// Package collector runs one SSH transport per active host and one session per file.
package collector

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"logstream/internal/logging"
	"logstream/internal/store"
	"logstream/internal/stream"
)

type Credentials struct {
	Method     string `json:"method"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
type Status struct {
	State     string    `json:"state"`
	Detail    string    `json:"detail"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Manager struct {
	op            sync.Mutex
	mu            sync.RWMutex
	running       map[string]*runner
	statuses      map[string]Status
	hub           *stream.Hub
	ctx           context.Context
	logger        *slog.Logger
	sourceLoggers map[string]*slog.Logger
}

func New(ctx context.Context, hub *stream.Hub) *Manager {
	return &Manager{running: map[string]*runner{}, statuses: map[string]Status{}, hub: hub, ctx: ctx, logger: slog.Default(), sourceLoggers: map[string]*slog.Logger{}}
}
func (m *Manager) Statuses() map[string]Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]Status{}
	for k, v := range m.statuses {
		out[k] = v
	}
	return out
}
func (m *Manager) status(id, state, detail string) {
	m.mu.Lock()
	previous := m.statuses[id]
	m.statuses[id] = Status{state, detail, time.Now().UTC()}
	logger := m.sourceLoggers[id]
	if logger == nil {
		logger = m.logger.With("source_id", id)
	}
	m.mu.Unlock()
	if previous.State != state || previous.Detail != detail {
		level := slog.LevelInfo
		if state == "error" || state == "waiting" || state == "reconnecting" {
			level = slog.LevelWarn
		}
		logger.Log(m.ctx, level, "source.state", "previous_state", previous.State, "state", state, "detail", detail)
	}
}

func ValidateCredentials(c Credentials) error {
	if len(c.Reason) > 1024 || strings.ContainsAny(c.Reason, "\x00\r\n") {
		return errors.New("connection reason must be one line of up to 1024 characters")
	}
	switch c.Method {
	case "password":
		if c.Password == "" {
			return errors.New("enter an SSH password")
		}
	case "key":
		var err error
		if c.Passphrase != "" {
			_, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			_, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return errors.New("private key could not be parsed; check the key and passphrase")
		}
	case "agent":
		if os.Getenv("SSH_AUTH_SOCK") == "" {
			return errors.New("the backend has no SSH agent; use a key or password")
		}
	default:
		return errors.New("choose password, key, or agent authentication")
	}
	return nil
}
func (m *Manager) Start(h store.Host, sources []store.Source, c Credentials) error {
	return m.StartContext(m.ctx, h, sources, c)
}

// Carry request correlation into workers, but keep their lifetime independent
// of the HTTP request so a completed POST does not cancel collection.
func (m *Manager) StartContext(requestCtx context.Context, h store.Host, sources []store.Source, c Credentials) error {
	logger := logging.FromContext(requestCtx, m.logger).With("component", "ssh", "host_id", h.ID, "host", h.Name, "address", h.Address, "port", h.Port, "connection_id", store.ID())
	if err := ValidateCredentials(c); err != nil {
		logger.Warn("ssh.credentials_rejected", "error", err.Error())
		return err
	}
	for _, source := range sources {
		if source.Parser != "" && !stream.ValidParser(source.Parser) {
			return errors.New("unsupported source parser")
		}
	}
	m.op.Lock()
	defer m.op.Unlock()
	m.stop(h.ID, sources)
	ctx, cancel := context.WithCancel(m.ctx)
	ctx = logging.WithContext(ctx, logger)
	r := &runner{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.running[h.ID] = r
	for _, s := range sources {
		m.sourceLoggers[s.ID] = logger.With("source_id", s.ID, "path", s.Path)
	}
	m.mu.Unlock()
	for _, s := range sources {
		m.status(s.ID, "connecting", "Establishing SSH connection")
	}
	go func() { defer close(r.done); m.run(ctx, h, sources, c) }()
	return nil
}
func (m *Manager) stop(id string, sources []store.Source) {
	m.mu.Lock()
	r := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if r != nil {
		m.logger.Info("ssh.stopping", "host_id", id)
		r.cancel()
		<-r.done
		m.logger.Info("ssh.stopped", "host_id", id)
	}
	for _, s := range sources {
		m.status(s.ID, "stopped", "Collection stopped")
	}
}
func (m *Manager) Stop(id string, sources []store.Source) {
	m.op.Lock()
	defer m.op.Unlock()
	m.stop(id, sources)
}
func (m *Manager) Close() {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.RLock()
	rs := []*runner{}
	for _, r := range m.running {
		rs = append(rs, r)
	}
	m.mu.RUnlock()
	for _, r := range rs {
		r.cancel()
	}
	for _, r := range rs {
		<-r.done
	}
	m.mu.Lock()
	m.running = map[string]*runner{}
	m.mu.Unlock()
	if len(rs) > 0 {
		m.logger.Info("ssh.collectors_stopped", "hosts", len(rs))
	}
}

func auth(c Credentials) (ssh.AuthMethod, func(), error) {
	noop := func() {}
	switch c.Method {
	case "password":
		return ssh.Password(c.Password), noop, nil
	case "key":
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return nil, noop, errors.New("invalid private key")
		}
		return ssh.PublicKeys(signer), noop, nil
	case "agent":
		conn, err := net.DialTimeout("unix", os.Getenv("SSH_AUTH_SOCK"), 3*time.Second)
		if err != nil {
			return nil, noop, errors.New("SSH agent is unavailable")
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), func() { conn.Close() }, nil
	}
	return nil, noop, errors.New("unsupported authentication method")
}

var errHostKey = errors.New("server host key changed; remove and re-add the host after verifying its fingerprint")

func dial(ctx context.Context, h store.Host, c Credentials) (*ssh.Client, net.Conn, error) {
	logger := logging.FromContext(ctx, slog.Default())
	started := time.Now()
	a, cleanup, err := auth(c)
	if err != nil {
		return nil, nil, operation("credentials", err)
	}
	defer cleanup()
	address := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, operation("tcp_connect", err)
	}
	logger.Debug("ssh.tcp_connected", "duration", time.Since(started))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	cfg := &ssh.ClientConfig{User: h.Username, Auth: []ssh.AuthMethod{a}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(h.Fingerprint)) != 1 {
			return errHostKey
		}
		return nil
	}}
	interactive := &interactiveAuth{credentials: c, logger: logger}
	if c.Method == "password" {
		cfg.Auth = append(cfg.Auth, ssh.KeyboardInteractive(interactive.challenge))
	}
	cfg.AuthCallback = interactive.observe
	cc, ch, req, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		conn.Close()
		return nil, nil, operation("ssh_handshake", err)
	}
	conn.SetDeadline(time.Time{})
	logger.Debug("ssh.handshake_completed", "duration", time.Since(started))
	return ssh.NewClient(cc, ch, req), conn, nil
}

// Probe stops at host-key exchange. It never authenticates or trusts the key.
func Probe(ctx context.Context, h store.Host) (string, error) {
	address := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	logger := logging.FromContext(ctx, slog.Default()).With("component", "ssh", "address", address)
	started := time.Now()
	logger.Info("ssh.probe_started")
	conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		logger.Warn("ssh.probe_failed", "stage", "tcp_connect", "error", safeError(err, Credentials{}, h.Username), "duration", time.Since(started))
		_, detail, _ := failure(operation("tcp_connect", err))
		return "", errors.New(detail)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fingerprint := ""
	cfg := &ssh.ClientConfig{User: h.Username, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return errors.New("probe complete")
	}}
	_, _, _, handshakeErr := ssh.NewClientConn(conn, address, cfg)
	if fingerprint == "" {
		logger.Warn("ssh.probe_failed", "stage", "ssh_handshake", "error", safeError(handshakeErr, Credentials{}, h.Username), "duration", time.Since(started))
		return "", errors.New("Could not read SSH host key. See ssh.probe_failed in the backend log.")
	}
	logger.Info("ssh.probe_succeeded", "fingerprint", fingerprint, "duration", time.Since(started))
	return fingerprint, nil
}

func QuotePath(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'" }

func (m *Manager) run(ctx context.Context, h store.Host, sources []store.Source, c Credentials) {
	logger := logging.FromContext(ctx, m.logger)
	started := time.Now()
	defer func() {
		logger.Info("ssh.worker_stopped", "duration", time.Since(started), "canceled", ctx.Err() != nil)
	}()
	backoff := time.Second
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return
		}
		for _, s := range sources {
			m.status(s.ID, "connecting", "Establishing SSH connection")
		}
		attemptLogger := logger.With("attempt", attempt+1)
		attemptCtx := logging.WithContext(ctx, attemptLogger)
		attemptStarted := time.Now()
		attemptLogger.Info("ssh.connecting", "auth_method", c.Method, "sources", len(sources))
		err := m.collect(attemptCtx, h, sources, c, attempt == 0)
		if ctx.Err() != nil {
			return
		}
		stage, detail, terminal := failure(err)
		attrs := []any{"stage", stage, "error", safeError(err, c, h.Username), "duration", time.Since(attemptStarted), "terminal", terminal}
		var sourceErr *sourceFailure
		if errors.As(err, &sourceErr) {
			attrs = append(attrs, "source_id", sourceErr.sourceID, "path", sourceErr.path)
		}
		if !terminal {
			attrs = append(attrs, "retry_in", backoff)
			detail += " Retrying; logs during the outage may be missing."
		}
		if terminal {
			attemptLogger.Error("ssh.collection_failed", attrs...)
		} else {
			attemptLogger.Warn("ssh.collection_failed", attrs...)
		}
		state := "reconnecting"
		if terminal {
			state = "error"
		}
		for _, s := range sources {
			m.status(s.ID, state, detail)
		}
		if terminal {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (m *Manager) collect(ctx context.Context, h store.Host, sources []store.Source, c Credentials, history bool) error {
	logger := logging.FromContext(ctx, m.logger)
	client, conn, err := dial(ctx, h, c)
	if err != nil {
		return err
	}
	defer client.Close()
	logger.Info("ssh.connected")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	errs := make(chan error, len(sources)+1)
	// All channel reads share the transport; a deadline also bounds hung keepalives.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				conn.SetDeadline(time.Now().Add(40 * time.Second))
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					if ctx.Err() == nil {
						errs <- operation("ssh_keepalive", err)
					}
					client.Close()
					return
				}
			}
		}
	}()
	for _, src := range sources {
		wg.Add(1)
		go func(s store.Source) {
			defer wg.Done()
			err := m.tail(ctx, client, h, s, c, history)
			if err != nil {
				err = &sourceFailure{s.ID, s.Path, err}
			}
			errs <- err
		}(src)
	}
	select {
	case <-ctx.Done():
		client.Close()
		cancel()
		return ctx.Err()
	case err := <-errs:
		client.Close()
		cancel()
		return err
	}
}

const maxLine = 32 * 1024

// readLines discards oversized line fragments without allocating an unbounded token.
func readLines(r io.Reader, emit func(string)) error {
	reader := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			prefix := string(line[:maxLine-32])
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = reader.ReadSlice('\n')
			}
			emit(prefix + " … [line truncated]")
		} else if len(line) > 0 {
			emit(strings.TrimRight(string(line), "\r\n"))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
func (m *Manager) tail(ctx context.Context, client *ssh.Client, h store.Host, s store.Source, c Credentials, history bool) error {
	logger := logging.FromContext(ctx, m.logger).With("source_id", s.ID, "path", s.Path)
	started := time.Now()
	var lines, bytesRead, diagnostics atomic.Int64
	defer func() {
		logger.Info("ssh.tail_stopped", "duration", time.Since(started), "lines", lines.Load(), "bytes", bytesRead.Load(), "stderr_lines", diagnostics.Load(), "canceled", ctx.Err() != nil)
	}()
	session, err := client.NewSession()
	if err != nil {
		return operation("ssh_session", err)
	}
	defer session.Close()
	stdout, err := session.StdoutPipe()
	if err != nil {
		return operation("remote_exec", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return operation("remote_exec", err)
	}
	n := 0
	if history {
		n = 200
	}
	if err = session.Start(fmt.Sprintf("exec tail -n %d -F -- %s", n, QuotePath(s.Path))); err != nil {
		logger.Warn("ssh.tail_rejected", "error", safeError(err, c, h.Username))
		return operation("remote_exec", err)
	}
	parserMode := s.Parser
	if parserMode == "" {
		parserMode = stream.ParserRegular
	}
	logger.Info("ssh.tail_started", "initial_lines", n, "parser", parserMode)
	m.status(s.ID, "streaming", "Following file over SSH")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		diagnosticsLogged := 0
		readLines(stderr, func(line string) {
			diagnostics.Add(1)
			state, detail := remoteDiagnostic(line)
			if state != "" {
				m.status(s.ID, state, detail)
			}
			if diagnosticsLogged < 4 {
				diagnosticsLogged++
				logger.Warn("ssh.remote_stderr", "diagnostic", safeError(errors.New(line), c, h.Username))
			} else if diagnosticsLogged == 4 {
				diagnosticsLogged++
				logger.Warn("ssh.remote_stderr_suppressed", "limit", 4, "detail", "Further stderr is counted in the session summary")
			}
		})
	}()
	parser := stream.NewParser(parserMode, s.ID, h.Name, s.Path)
	firstEvent := true
	readErr := readEvents(ctx, stdout, parser, parserMode == stream.ParserTX, func(line string) {
		lines.Add(1)
		bytesRead.Add(int64(len(line)))
		m.status(s.ID, "streaming", "Following file over SSH")
	}, func(event stream.Event) {
		if firstEvent {
			logger.Info("ssh.first_event", "wait_duration", time.Since(started))
			firstEvent = false
		}
		m.hub.Publish(event)
	})
	err = session.Wait()
	session.Close()
	wg.Wait()
	if readErr != nil {
		return operation("remote_read", readErr)
	}
	if err == nil {
		return operation("remote_command", errors.New("remote tail exited"))
	}
	return operation("remote_command", err)
}
