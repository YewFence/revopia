package cmd

import (
	"errors"
	"strings"
	"testing"
)

func TestHintsForErrorCollectsNestedHints(t *testing.T) {
	err := errors.Join(
		withHints(errors.New("first"), "hint one"),
		withHints(errors.New("second"), "hint one", "hint two"),
	)

	got := hintsForError(err)
	if len(got) != 2 {
		t.Fatalf("hint count = %d, want 2, hints=%v", len(got), got)
	}
	if got[0] != "hint one" || got[1] != "hint two" {
		t.Fatalf("hints = %v, want stable de-duplicated hints", got)
	}
}

func TestRestoreTargetVolumeNotEmptyErrorHasActionableHints(t *testing.T) {
	err := restoreTargetVolumeNotEmptyError(restoreSession{TargetVolume: "app data"})
	hints := strings.Join(hintsForError(err), "\n")

	if !strings.Contains(hints, "docker volume rm 'app data'") {
		t.Fatalf("hints = %q, want docker volume rm command", hints)
	}
	if !strings.Contains(hints, "--dangerously-allow-non-empty-target") {
		t.Fatalf("hints = %q, want dangerous reuse flag", hints)
	}
	if !strings.Contains(hints, "find /target") {
		t.Fatalf("hints = %q, want clear volume command", hints)
	}
}
