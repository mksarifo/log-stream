package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"logstream/internal/store"
	"logstream/internal/stream"
)

func TestInteractivePasswordAndReasonStreamLogs(t *testing.T) {
	for _, scenario := range []string{"interactive-password", "interactive-reason", "interactive-bundled"} {
		t.Run(scenario, func(t *testing.T) {
			h, _ := testSSHScenario(t, scenario)
			hub := stream.New(100, 32768)
			m := New(context.Background(), hub)
			defer m.Close()
			buffer := &logBuffer{}
			m.logger = slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
			c := Credentials{Method: "password", Password: "test-password", Reason: "connection-reason-not-for-logs"}
			if err := m.Start(h, []store.Source{{ID: "s", HostID: h.ID, Path: "/log"}}, c); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				count, _ := hub.Stats()
				if count > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("interactive login failed: %v\n%s", m.Statuses(), buffer.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			text := buffer.String()
			if !strings.Contains(text, "ssh.auth_methods") || !strings.Contains(text, "keyboard-interactive") || !strings.Contains(text, "ssh.first_event") {
				t.Fatalf("missing auth diagnostics: %s", text)
			}
			if strings.Contains(text, c.Password) || strings.Contains(text, c.Reason) {
				t.Fatal("interactive answers logged")
			}
		})
	}
}

func TestMissingReasonAndUnsupportedMFAStopWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		scenario string
		reason   string
		want     error
	}{
		{"interactive-reason", "", errReasonRequired}, {"interactive-otp", "connection-reason-not-for-logs", errInteractivePrompt}, {"publickey-only", "", errAuthUnavailable},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			h, _ := testSSHScenario(t, test.scenario)
			client, _, err := dial(context.Background(), h, Credentials{Method: "password", Password: "test-password", Reason: test.reason})
			if client != nil {
				client.Close()
				t.Fatal("unsupported authentication succeeded")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			_, detail, terminal := failure(err)
			if !terminal || detail == "" {
				t.Fatal("interactive failure would retry automatically")
			}
		})
	}
}

func TestInteractiveNeverSendsPasswordToOtherPrompts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, test := range []struct {
		prompt, instruction string
		echo                bool
	}{
		{"OTP:", "", false}, {"Verification code:", "", false}, {"Username:", "", true}, {"Password:", "", true}, {"New password:", "", false}, {"Password:", "Enter your OTP", false},
	} {
		i := &interactiveAuth{credentials: Credentials{Password: "secret", Reason: "reason"}, logger: logger}
		answers, err := i.challenge("", test.instruction, []string{test.prompt}, []bool{test.echo})
		if !errors.Is(err, errInteractivePrompt) || answers != nil {
			t.Fatalf("answered unsupported prompt: %+v", test)
		}
	}
	i := &interactiveAuth{credentials: Credentials{Password: "secret"}, logger: logger}
	if _, err := i.challenge("", "", []string{"Password:"}, []bool{false}); err != nil {
		t.Fatal(err)
	}
	if answers, err := i.challenge("", "", []string{"Password:"}, []bool{false}); !errors.Is(err, errInteractivePrompt) || answers != nil {
		t.Fatal("repeated password challenge answered")
	}
	if err := ValidateCredentials(Credentials{Method: "password", Password: "secret", Reason: "line\nbreak"}); err == nil {
		t.Fatal("multiline reason accepted")
	}
	if strings.Contains(safeError(errors.New("echoed secret reason"), Credentials{Password: "secret", Reason: "reason"}, ""), "reason") {
		t.Fatal("reason not redacted from server errors")
	}
}

func TestInteractiveDoesNotRetryRejectedPasswordWithAnotherMethod(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, method := range []string{"password", "keyboard-interactive"} {
		i := &interactiveAuth{credentials: Credentials{Method: "password", Password: "secret"}, logger: logger, passwordAnswered: true}
		_, err := i.observe(&ssh.ClientAuthContext{AllowedMethods: []string{"password", "keyboard-interactive"}, TriedMethods: []string{"none", method}})
		if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
			t.Fatal("rejected password would be retried via another method")
		}
	}
	i := &interactiveAuth{credentials: Credentials{Method: "password", Password: "secret"}, logger: logger}
	i.challenge("", "", []string{"Connection reason:"}, []bool{true})
	if _, err := i.observe(&ssh.ClientAuthContext{TriedMethods: []string{"none", "keyboard-interactive"}}); !errors.Is(err, errReasonRequired) {
		t.Fatal("specific missing-reason error was lost")
	}
}
