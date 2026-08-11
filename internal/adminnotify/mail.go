package adminnotify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maximumMailerOutputBytes = 4096
	maximumMailerRecipients  = 8
	maximumMailerTimeout     = 300 * time.Second
)

var ErrMailerConfiguration = errors.New("administrator mailer configuration is invalid")

// Mailer is the narrow no-shell notification boundary consumed by the storage
// growth guard.
type Mailer interface {
	Send(context.Context, string, string) error
}

// CommandMailer executes one validated local mail command directly.
type CommandMailer struct {
	command    string
	recipients []string
	timeout    time.Duration
	run        func(context.Context, string, []string, io.Reader) ([]byte, []byte, error)
}

func NewCommandMailer(command string, recipients []string, timeout time.Duration) (*CommandMailer, error) {
	if command == "" || !filepath.IsAbs(command) || filepath.Clean(command) != command ||
		containsControl(command) ||
		len(recipients) == 0 || len(recipients) > maximumMailerRecipients ||
		timeout <= 0 || timeout > maximumMailerTimeout {
		return nil, ErrMailerConfiguration
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved ||
		containsControl(resolved) {
		return nil, ErrMailerConfiguration
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, ErrMailerConfiguration
	}
	copied := append([]string(nil), recipients...)
	seen := make(map[string]struct{}, len(copied))
	for _, recipient := range copied {
		parsed, err := mail.ParseAddress(recipient)
		if recipient == "" || len(recipient) > 254 || strings.HasPrefix(recipient, "-") ||
			strings.ContainsAny(recipient, " \t\r\n") || containsControl(recipient) ||
			err != nil || parsed.Name != "" || parsed.Address != recipient {
			return nil, ErrMailerConfiguration
		}
		if _, duplicate := seen[recipient]; duplicate {
			return nil, ErrMailerConfiguration
		}
		seen[recipient] = struct{}{}
	}
	mailer := &CommandMailer{
		command:    resolved,
		recipients: copied,
		timeout:    timeout,
	}
	mailer.run = runCommand
	return mailer, nil
}

func (mailer *CommandMailer) Send(ctx context.Context, subject, body string) error {
	if mailer == nil || mailer.run == nil || ctx == nil || subject == "" ||
		containsControl(subject) || len(subject) > 200 || len(body) > 32*1024 {
		return ErrMailerConfiguration
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, mailer.timeout)
	defer cancel()

	args := make([]string, 0, 2+len(mailer.recipients))
	args = append(args, "-s", subject)
	args = append(args, mailer.recipients...)
	stdout, stderr, err := mailer.run(timeoutCtx, mailer.command, args, strings.NewReader(body))
	if len(stdout) > maximumMailerOutputBytes || len(stderr) > maximumMailerOutputBytes {
		return errors.New("administrator mailer output exceeded limit")
	}
	if err != nil {
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			return errors.New("administrator mailer timed out")
		}
		return errors.New("administrator mailer failed")
	}
	return nil
}

func runCommand(
	ctx context.Context,
	command string,
	args []string,
	stdin io.Reader,
) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = stdin

	var stdout, stderr limitedBuffer
	stdout.maximum = maximumMailerOutputBytes + 1
	stderr.maximum = maximumMailerOutputBytes + 1
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

type limitedBuffer struct {
	bytes.Buffer
	maximum int
}

func (buffer *limitedBuffer) Write(p []byte) (int, error) {
	if buffer.maximum <= 0 {
		return 0, errors.New("invalid output limit")
	}
	remaining := buffer.maximum - buffer.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = buffer.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	return buffer.Buffer.Write(p)
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
