package adminnotify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCommandMailerUsesFixedArgvAndBodyOnStdin(t *testing.T) {
	command := testExecutable(t)
	mailer, err := NewCommandMailer(
		command,
		[]string{"admin@example.com", "ops@example.com"},
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("NewCommandMailer() error = %v", err)
	}
	var gotCommand string
	var gotArgs []string
	var gotBody string
	mailer.run = func(ctx context.Context, command string, args []string, stdin io.Reader) ([]byte, []byte, error) {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		body, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatalf("ReadAll(stdin) error = %v", err)
		}
		gotBody = string(body)
		return nil, nil, nil
	}

	if err := mailer.Send(context.Background(), "storage warning", "body\nline two\n"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotCommand != command {
		t.Fatalf("command = %q, want %q", gotCommand, command)
	}
	wantArgs := []string{"-s", "storage warning", "admin@example.com", "ops@example.com"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if gotBody != "body\nline two\n" {
		t.Fatalf("stdin body = %q", gotBody)
	}
}

func TestCommandMailerRejectsUnsafeConfiguration(t *testing.T) {
	command := testExecutable(t)
	link := filepath.Join(t.TempDir(), "mail-link")
	if err := os.Symlink(command, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	plain := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	controlCommand := filepath.Join(t.TempDir(), "mail\ncommand")
	if err := os.WriteFile(controlCommand, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(control command) error = %v", err)
	}

	tests := []struct {
		name       string
		command    string
		recipients []string
		timeout    time.Duration
	}{
		{name: "missing command", command: filepath.Join(t.TempDir(), "missing"), recipients: []string{"a@example.com"}, timeout: time.Second},
		{name: "relative command", command: "mail", recipients: []string{"a@example.com"}, timeout: time.Second},
		{name: "not executable", command: plain, recipients: []string{"a@example.com"}, timeout: time.Second},
		{name: "control command", command: controlCommand, recipients: []string{"a@example.com"}, timeout: time.Second},
		{name: "no recipients", command: command, timeout: time.Second},
		{name: "option recipient", command: command, recipients: []string{"-x@example.com"}, timeout: time.Second},
		{name: "display name", command: command, recipients: []string{"Admin <admin@example.com>"}, timeout: time.Second},
		{name: "whitespace", command: command, recipients: []string{"admin @example.com"}, timeout: time.Second},
		{name: "control", command: command, recipients: []string{"admin@example.com\n-bcc"}, timeout: time.Second},
		{name: "duplicate recipient", command: command, recipients: []string{"a@example.com", "a@example.com"}, timeout: time.Second},
		{name: "too many recipients", command: command, recipients: []string{"a@e.co", "b@e.co", "c@e.co", "d@e.co", "e@e.co", "f@e.co", "g@e.co", "h@e.co", "i@e.co"}, timeout: time.Second},
		{name: "zero timeout", command: command, recipients: []string{"a@example.com"}},
		{name: "too long timeout", command: command, recipients: []string{"a@example.com"}, timeout: 301 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCommandMailer(test.command, test.recipients, test.timeout); !errors.Is(err, ErrMailerConfiguration) {
				t.Fatalf("NewCommandMailer() error = %v, want ErrMailerConfiguration", err)
			}
		})
	}
}

func TestCommandMailerResolvesConfiguredSymlinkBeforeExecution(t *testing.T) {
	target := testExecutable(t)
	link := filepath.Join(t.TempDir(), "mail-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	mailer, err := NewCommandMailer(link, []string{"admin@example.com"}, time.Second)
	if err != nil {
		t.Fatalf("NewCommandMailer(symlink) error = %v", err)
	}
	if mailer.command != target {
		t.Fatalf("resolved mail command = %q, want %q", mailer.command, target)
	}
}

func TestCommandMailerBoundsSubjectBodyOutputAndTimeout(t *testing.T) {
	command := testExecutable(t)
	mailer, err := NewCommandMailer(command, []string{"admin@example.com"}, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewCommandMailer() error = %v", err)
	}
	if err := mailer.Send(context.Background(), "bad\nsubject", "body"); !errors.Is(err, ErrMailerConfiguration) {
		t.Fatalf("control subject error = %v", err)
	}
	if err := mailer.Send(context.Background(), strings.Repeat("s", 201), "body"); !errors.Is(err, ErrMailerConfiguration) {
		t.Fatalf("long subject error = %v", err)
	}
	if err := mailer.Send(context.Background(), "subject", strings.Repeat("b", 32*1024+1)); !errors.Is(err, ErrMailerConfiguration) {
		t.Fatalf("long body error = %v", err)
	}

	mailer.run = func(context.Context, string, []string, io.Reader) ([]byte, []byte, error) {
		return bytes.Repeat([]byte("x"), maximumMailerOutputBytes+1), nil, nil
	}
	if err := mailer.Send(context.Background(), "subject", "body"); err == nil || !strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("oversized output error = %v", err)
	}

	mailer.run = func(ctx context.Context, _ string, _ []string, _ io.Reader) ([]byte, []byte, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if err := mailer.Send(context.Background(), "subject", "body"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestCommandMailerRedactsCommandOutputOnFailure(t *testing.T) {
	command := testExecutable(t)
	mailer, err := NewCommandMailer(command, []string{"admin@example.com"}, time.Second)
	if err != nil {
		t.Fatalf("NewCommandMailer() error = %v", err)
	}
	mailer.run = func(context.Context, string, []string, io.Reader) ([]byte, []byte, error) {
		return []byte("stdout-secret"), []byte("stderr-secret"), errors.New("secret-token-from-command")
	}
	err = mailer.Send(context.Background(), "subject", "body")
	if err == nil {
		t.Fatal("Send() error = nil")
	}
	if strings.Contains(err.Error(), "stdout-secret") || strings.Contains(err.Error(), "stderr-secret") ||
		strings.Contains(err.Error(), "secret-token-from-command") {
		t.Fatalf("Send() leaked command output: %v", err)
	}
}

func TestLimitedBufferBoundsCaptureWithoutShortWrite(t *testing.T) {
	var buffer limitedBuffer
	buffer.maximum = 4
	input := []byte("123456")
	n, err := buffer.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(input))
	}
	if got := buffer.String(); got != "1234" {
		t.Fatalf("buffer = %q", got)
	}
}

func testExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mail")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
