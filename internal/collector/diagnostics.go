package collector

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
	"logstream/internal/logging"
)

type operationError struct {
	stage string
	err   error
}

type sourceFailure struct {
	sourceID, path string
	err            error
}

func (e *sourceFailure) Error() string { return e.err.Error() }
func (e *sourceFailure) Unwrap() error { return e.err }

func (e *operationError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *operationError) Unwrap() error { return e.err }
func operation(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &operationError{stage: stage, err: err}
}

// Errors can contain server-supplied text. Redact supplied secrets before bounding
// and flattening them; never log credentials, request bodies, or application lines.
func safeError(err error, c Credentials, username string) string {
	if err == nil {
		return "remote command ended"
	}
	message := err.Error()
	secrets := []string{c.Password, c.PrivateKey, c.Passphrase, c.Reason, strings.TrimSpace(c.Reason), username}
	// Also remove individual lines from a pasted key if echoed by a remote service.
	for _, line := range strings.Split(c.PrivateKey, "\n") {
		secrets = append(secrets, strings.TrimSpace(line))
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return logging.Clean(message)
}

func failure(err error) (stage, detail string, terminal bool) {
	stage = "ssh_connection"
	var op *operationError
	if errors.As(err, &op) {
		stage = op.stage
	}
	if errors.Is(err, errHostKey) {
		return stage, errHostKey.Error(), true
	}
	if errors.Is(err, errReasonRequired) {
		return stage, errReasonRequired.Error(), true
	}
	if errors.Is(err, errInteractivePrompt) {
		return stage, errInteractivePrompt.Error(), true
	}
	if errors.Is(err, errAuthUnavailable) {
		return stage, "SSH server offers no compatible authentication method. Check the selected authentication mode and SSH username; see ssh.auth_methods in the backend log.", true
	}
	if err != nil && strings.Contains(err.Error(), "unable to authenticate") {
		return stage, "SSH authentication failed. Check credentials and the selected authentication mode. Password and connection-reason prompts are supported; OTP/MFA prompts require an interactive SSH client.", true
	}
	if stage == "credentials" {
		return stage, "SSH credentials or the backend SSH agent are unavailable. Reconnect with valid credentials.", true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return stage, "Hostname lookup failed. Check the SSH hostname and DNS/VPN connection.", false
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return stage, "Connection timed out during " + strings.ReplaceAll(stage, "_", " ") + ". Check network/VPN access and SSH proxy settings.", false
	}
	var channel *ssh.OpenChannelError
	if errors.As(err, &channel) {
		return stage, "SSH server rejected the command session (" + channel.Reason.String() + "). Check CyberArk remote-command policy and session limits.", true
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitStatus()
		if code == 126 || code == 127 {
			return stage, fmt.Sprintf("Remote log command exited with status %d. Check that GNU tail is installed and command execution is permitted.", code), true
		}
		return stage, fmt.Sprintf("Remote log command exited with status %d. Check file access and SSH/CyberArk command policy.", code), false
	}
	switch stage {
	case "ssh_keepalive":
		return stage, "The SSH server stopped responding to keepalive requests. Check proxy timeouts and network connectivity.", false
	case "tcp_connect":
		return stage, "TCP connection to the SSH server failed. Check the hostname, port, VPN, and firewall.", false
	case "ssh_handshake":
		return stage, "SSH negotiation or login failed. Check proxy authentication requirements and the backend error log.", false
	case "ssh_session":
		return stage, "Could not open an SSH command session. Check proxy session limits and the backend error log.", false
	case "remote_exec":
		return stage, "SSH server rejected or interrupted the remote tail command. Check command policy and the backend error log.", false
	case "remote_read":
		return stage, "The SSH log stream was interrupted. See the backend error log for the transport error.", false
	default:
		return stage, "Remote log command ended or the SSH transport closed. See the backend error log.", false
	}
}

// Classify stderr without copying arbitrary remote output into diagnostic logs.
func remoteDiagnostic(line string) (state, detail string) {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "permission denied"):
		return "waiting", "SSH account cannot read this file (permission denied). Check file and directory permissions."
	case strings.Contains(lower, "no such file"), strings.Contains(lower, "cannot open"):
		return "waiting", "Log file is missing or unavailable; tail is waiting for it to appear. Check the absolute path."
	case strings.Contains(lower, "command not found"), strings.Contains(lower, "tail: not found"), strings.Contains(lower, "exec: tail: not found"):
		return "error", "GNU tail was not found on the remote server."
	case strings.Contains(lower, "invalid option"), strings.Contains(lower, "illegal option"), strings.Contains(lower, "unrecognized option"):
		return "error", "The remote tail command does not support the required flags. GNU tail is required."
	case strings.Contains(lower, "pty"), strings.Contains(lower, "terminal is required"):
		return "error", "The SSH proxy requires an interactive terminal; this collector uses non-interactive commands."
	case strings.Contains(lower, "has appeared"), strings.Contains(lower, "following new file"):
		return "streaming", "Following file over SSH"
	default:
		return "", ""
	}
}
