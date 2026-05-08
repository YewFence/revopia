package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func executeCommand(command string, args ...string) (string, error) {
	buffer := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetOut(buffer)
	cmd.SetErr(buffer)
	cmd.SetArgs(append([]string{command}, args...))
	err := cmd.Execute()
	return buffer.String(), err
}

func TestRootCommandRequiresSubcommand(t *testing.T) {
	buffer := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetOut(buffer)
	cmd.SetErr(buffer)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing command error")
	}
	if !strings.Contains(err.Error(), "缺少命令") {
		t.Fatalf("error = %q, want missing command", err)
	}
	if got := buffer.String(); !strings.Contains(got, "Available Commands") {
		t.Fatalf("help output = %q, want available commands", got)
	}
}

func TestVersionCommand(t *testing.T) {
	oldVersion := appVersion
	appVersion = "test-version"
	t.Cleanup(func() {
		appVersion = oldVersion
	})

	got, err := executeCommand("version")
	if err != nil {
		t.Fatalf("execute version: %v", err)
	}
	if got != "volume-backup test-version\n" {
		t.Fatalf("version output = %q", got)
	}
}

func TestCommandRejectsExtraArgs(t *testing.T) {
	_, err := executeCommand("prepare", "unexpected")
	if err == nil {
		t.Fatal("expected extra argument error")
	}
	if !strings.Contains(err.Error(), "prepare 不接受额外参数") {
		t.Fatalf("error = %q, want extra argument error", err)
	}
}

func TestRestoreMissingSourceVolumeReturnsHints(t *testing.T) {
	_, err := executeCommand("restore", "--log-file", "")
	if err == nil {
		t.Fatal("expected missing source volume error")
	}
	if !strings.Contains(err.Error(), "source volume 不能为空") {
		t.Fatalf("error = %q, want missing source volume", err)
	}

	hints := strings.Join(hintsForError(err), "\n")
	if !strings.Contains(hints, "docker volume ls") {
		t.Fatalf("hints = %q, want docker volume ls hint", hints)
	}
}

func TestRestoreCleanupMissingSessionReturnsHints(t *testing.T) {
	_, err := executeCommand("restore-cleanup", "--log-file", "")
	if err == nil {
		t.Fatal("expected missing session error")
	}
	if !strings.Contains(err.Error(), "session id 不能为空") {
		t.Fatalf("error = %q, want missing session", err)
	}

	hints := strings.Join(hintsForError(err), "\n")
	if !strings.Contains(hints, "RESTORE_SESSION_ID") {
		t.Fatalf("hints = %q, want session id hint", hints)
	}
}

func TestCompletionCommand(t *testing.T) {
	buffer := new(bytes.Buffer)
	cmd := newRootCommand()

	if err := cmd.GenBashCompletionV2(buffer, true); err != nil {
		t.Fatalf("generate bash completion: %v", err)
	}
	if got := buffer.String(); !strings.Contains(got, "# bash completion V2 for volume-backup") {
		t.Fatalf("completion output = %q, want volume-backup header", got)
	}
}
