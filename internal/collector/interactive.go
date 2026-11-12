package collector

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

var errReasonRequired = errors.New("SSH proxy requires a connection reason. Enter Connection reason and reconnect")
var errInteractivePrompt = errors.New("SSH proxy requested an unsupported interactive prompt. OTP/MFA, password changes, and repeated password prompts require an interactive SSH client")
var errAuthUnavailable = errors.New("SSH server does not offer an authentication method supported by the selected mode")

type interactiveAuth struct {
	credentials                      Credentials
	logger                           *slog.Logger
	passwordAnswered, reasonAnswered bool
	rounds                           int
	challengeError                   error
}

func safeMethods(methods []string) []string {
	result := []string{}
	for _, method := range methods {
		switch method {
		case "none", "password", "publickey", "keyboard-interactive", "gssapi-with-mic", "hostbased":
		default:
			method = "<other>"
		}
		if !slices.Contains(result, method) {
			result = append(result, method)
		}
		if len(result) >= 8 {
			break
		}
	}
	return result
}

// Observe the server's methods without logging prompts or supplied answers.
// A rejected password is not submitted again through another auth mechanism.
func (i *interactiveAuth) observe(ctx *ssh.ClientAuthContext) (ssh.AuthMethod, error) {
	i.logger.Debug("ssh.auth_methods", "offered", safeMethods(ctx.AllowedMethods), "tried", safeMethods(ctx.TriedMethods), "partial_success", safeMethods(ctx.PartialSuccessMethods))
	if i.challengeError != nil {
		return nil, i.challengeError
	}
	if i.credentials.Method == "password" {
		if slices.Contains(ctx.PartialSuccessMethods, "keyboard-interactive") {
			return nil, errInteractivePrompt
		}
		if slices.Contains(ctx.TriedMethods, "keyboard-interactive") && i.passwordAnswered {
			return nil, errors.New("ssh: unable to authenticate: server rejected interactive password authentication")
		}
		if slices.Contains(ctx.TriedMethods, "password") {
			return nil, errors.New("ssh: unable to authenticate: server rejected password authentication")
		}
		if slices.Contains(ctx.PartialSuccessMethods, "password") {
			i.passwordAnswered = true
		}
	}
	if len(ctx.TriedMethods) == 1 && ctx.TriedMethods[0] == "none" && len(ctx.PartialSuccessMethods) == 0 {
		compatible := slices.Contains(ctx.AllowedMethods, "publickey")
		if i.credentials.Method == "password" {
			compatible = slices.Contains(ctx.AllowedMethods, "password") || slices.Contains(ctx.AllowedMethods, "keyboard-interactive")
		}
		if !compatible {
			return nil, fmt.Errorf("%w; offered methods: %s", errAuthUnavailable, strings.Join(safeMethods(ctx.AllowedMethods), ", "))
		}
	}
	return nil, nil
}

func promptKind(prompt string) string {
	prompt = strings.ToLower(prompt)
	for _, marker := range []string{"otp", "one-time", "one time", "token", "passcode", "verification", "authenticator", "new password", "change password", "confirm password", "retype", "pin code"} {
		if strings.Contains(prompt, marker) {
			return "unsupported"
		}
	}
	password := strings.Contains(prompt, "password")
	reason := strings.Contains(prompt, "reason") || strings.Contains(prompt, "justification")
	if reason && !password {
		return "connection_reason"
	}
	if password && !reason {
		return "password"
	}
	return "unsupported"
}

func (i *interactiveAuth) challenge(_ string, instruction string, questions []string, echoes []bool) (answers []string, err error) {
	defer func() {
		if err != nil {
			i.challengeError = err
		}
	}()
	i.rounds++
	if i.rounds > 8 || len(questions) != len(echoes) || len(questions) > 2 {
		return nil, errInteractivePrompt
	}
	for _, marker := range []string{"one-time", "one time password", "otp", "verification code", "authenticator code", "new password"} {
		if strings.Contains(strings.ToLower(instruction), marker) {
			return nil, errInteractivePrompt
		}
	}
	answers = make([]string, len(questions))
	password, reason := i.passwordAnswered, i.reasonAnswered
	kinds := make([]string, len(questions))
	for n, question := range questions {
		kind := promptKind(question)
		kinds[n] = kind
		switch kind {
		case "password":
			if password || echoes[n] || i.credentials.Password == "" {
				return nil, errInteractivePrompt
			}
			answers[n] = i.credentials.Password
			password = true
		case "connection_reason":
			if reason {
				return nil, errInteractivePrompt
			}
			if strings.TrimSpace(i.credentials.Reason) == "" {
				return nil, errReasonRequired
			}
			answers[n] = strings.TrimSpace(i.credentials.Reason)
			reason = true
		default:
			i.logger.Warn("ssh.auth_prompt_rejected", "kind", "unsupported", "questions", len(questions))
			return nil, errInteractivePrompt
		}
	}
	i.passwordAnswered = password
	i.reasonAnswered = reason
	for _, kind := range kinds {
		i.logger.Info("ssh.auth_prompt_answered", "kind", kind)
	}
	return answers, nil
}
