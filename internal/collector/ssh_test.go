package collector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"logstream/internal/store"
	"logstream/internal/stream"
)

func testSSH(t *testing.T) (store.Host, <-chan string) {
	return testSSHScenario(t, "")
}

func testSSHScenario(t *testing.T, scenario string) (store.Host, <-chan string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) != "test-password" {
			return nil, errors.New("bad password")
		}
		return nil, nil
	}}
	if strings.HasPrefix(scenario, "interactive-") {
		cfg.PasswordCallback = nil
		cfg.KeyboardInteractiveCallback = func(meta ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			if scenario == "interactive-bundled" {
				answers, err := challenge(meta.User(), "", []string{"Password:", "Reason for connection:"}, []bool{false, true})
				if err != nil {
					return nil, err
				}
				if len(answers) != 2 || answers[0] != "test-password" || answers[1] != "connection-reason-not-for-logs" {
					return nil, errors.New("incorrect responses")
				}
				return nil, nil
			}
			answers, err := challenge(meta.User(), "", []string{"Password:"}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "test-password" {
				return nil, errors.New("bad password")
			}
			if scenario == "interactive-reason" {
				answers, err = challenge(meta.User(), "", []string{"Enter your reason for connection:"}, []bool{true})
				if err != nil {
					return nil, err
				}
				if len(answers) != 1 || answers[0] != "connection-reason-not-for-logs" {
					return nil, errors.New("bad reason")
				}
			}
			if scenario == "interactive-otp" {
				_, err = challenge(meta.User(), "", []string{"Verification code:"}, []bool{false})
				if err != nil {
					return nil, err
				}
				return nil, errors.New("unexpected OTP answer")
			}
			// Successful services may finish with an informational, zero-question round.
			if _, err = challenge(meta.User(), "Authenticated", nil, nil); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	if scenario == "publickey-only" {
		cfg.PasswordCallback = nil
		cfg.PublicKeyCallback = func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("key rejected")
		}
	}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan string, 16)
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
				cc, channels, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer cc.Close()
				go func() {
					for r := range requests {
						r.Reply(false, nil)
					}
				}()
				for ch := range channels {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, reqs, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range reqs {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							ssh.Unmarshal(req.Payload, &payload)
							commands <- payload.Command
							if scenario == "reject-exec" {
								req.Reply(false, nil)
								return
							}
							req.Reply(true, nil)
							if scenario == "permission-denied" {
								io.WriteString(channel.Stderr(), "tail: cannot open '/log': Permission denied test-password\n")
								continue
							}
							if scenario == "tx-format" {
								io.WriteString(channel, "12:00:00 Decoder/EventWorker/Pool (thread no:2, pid:3) :: *** Debugging *** Request Body:\n{\"command\":\"fixture\"}\n12:00:01 TX Link check successful\n")
								continue
							}
							io.WriteString(channel, "{\"level\":\"info\",\"message\":\"connected\"}\n")
						}
					}()
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return store.Host{ID: "host", Name: "integration", Address: host, Port: p, Username: "deploy", Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}, commands
}

func TestSSHCollectorsAndStop(t *testing.T) {
	h, commands := testSSH(t)
	fingerprint, err := Probe(context.Background(), h)
	if err != nil || fingerprint != h.Fingerprint {
		t.Fatalf("probe: %q %v", fingerprint, err)
	}
	hub := stream.New(100, 32768)
	manager := New(context.Background(), hub)
	defer manager.Close()
	sources := []store.Source{{ID: "one", HostID: h.ID, Path: "/var/log/api's $(touch unwanted).log"}, {ID: "two", HostID: h.ID, Path: "/var/log/worker.log"}}
	if err = manager.Start(h, sources, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		count, _ := hub.Stats()
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not receive logs: %v", manager.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	seen := map[string]bool{}
	for range sources {
		select {
		case command := <-commands:
			seen[command] = true
		case <-time.After(time.Second):
			t.Fatal("missing command")
		}
	}
	for _, s := range sources {
		expected := fmt.Sprintf("exec tail -n 200 -F -- %s", QuotePath(s.Path))
		if !seen[expected] {
			t.Fatalf("unsafe or missing command: %q", expected)
		}
	}
	done := make(chan struct{})
	go func() { manager.Stop(h.ID, sources); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("collectors did not shut down")
	}
	for _, s := range sources {
		if manager.Statuses()[s.ID].State != "stopped" {
			t.Fatal("source not stopped")
		}
	}
}

func TestSSHRejectsChangedKeyAndBadCredentials(t *testing.T) {
	h, _ := testSSH(t)
	hub := stream.New(100, 32768)
	manager := New(context.Background(), hub)
	defer manager.Close()
	sources := []store.Source{{ID: "one", HostID: h.ID, Path: "/log"}}
	h.Fingerprint = "SHA256:wrong"
	if err := manager.Start(h, sources, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Statuses()["one"].State != "error" {
		if time.Now().After(deadline) {
			t.Fatalf("changed key accepted: %v", manager.Statuses())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count, _ := hub.Stats(); count != 0 {
		t.Fatal("logs received from untrusted server")
	}
	if err := ValidateCredentials(Credentials{Method: "key", PrivateKey: "not a key"}); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestOversizedLines(t *testing.T) {
	var lines []string
	err := readLines(strings.NewReader(strings.Repeat("x", maxLine*5)+"\nnext\n"), func(s string) { lines = append(lines, s) })
	if err != nil || len(lines) != 2 || len(lines[0]) > maxLine || !strings.Contains(lines[0], "truncated") || lines[1] != "next" {
		t.Fatalf("line limit failed: lengths %d, err %v", len(lines), err)
	}
}

func TestSSHUsesSelectedTXParser(t *testing.T) {
	h, _ := testSSHScenario(t, "tx-format")
	hub := stream.New(100, 32768)
	m := New(context.Background(), hub)
	defer m.Close()
	if err := m.Start(h, []store.Source{{ID: "tx", HostID: h.ID, Path: "/tx.log", Parser: stream.ParserTX}}, Credentials{Method: "password", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		events, _, _ := hub.Read("", 0)
		if len(events) == 2 {
			if events[0].Level != "debug" || events[0].Fields["payload"] == nil || events[1].Fields["kind"] != "message" {
				t.Fatal("SSH collector did not use TX parsing")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parsed SSH records did not arrive")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
