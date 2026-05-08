package cmd

import (
	"bytes"
	"io"
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

func executeCommandWithInput(input string, command string, args ...string) (string, error) {
	buffer := new(bytes.Buffer)
	cmd := newRootCommand()
	cmd.SetIn(strings.NewReader(input))
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
	if !strings.Contains(err.Error(), "restore 需要一个 source-volume 位置参数") {
		t.Fatalf("error = %q, want missing source volume", err)
	}

	hints := strings.Join(hintsForError(err), "\n")
	if !strings.Contains(hints, "docker volume ls") {
		t.Fatalf("hints = %q, want docker volume ls hint", hints)
	}
}

func TestRestoreCleanupCancelledWithoutDocker(t *testing.T) {
	got, err := executeCommandWithInput("no\n", "restore-cleanup", "--log-file", "")
	if err != nil {
		t.Fatalf("execute restore-cleanup: %v", err)
	}
	if !strings.Contains(got, "已取消") {
		t.Fatalf("output = %q, want cancelled message", got)
	}
}

func TestRestoreCleanupRequiresConfirmInput(t *testing.T) {
	_, err := executeCommandWithInput("", "restore-cleanup", "--log-file", "")
	if err == nil {
		t.Fatal("expected confirm input error")
	}
	if !strings.Contains(err.Error(), "读取确认输入失败") && !strings.Contains(err.Error(), io.EOF.Error()) {
		t.Fatalf("error = %q, want confirm input error", err)
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
