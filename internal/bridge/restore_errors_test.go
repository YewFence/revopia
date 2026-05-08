package bridge

import (
	"strings"
	"testing"
)

func TestRestoreTargetVolumeNotEmptyErrorHasActionableHints(t *testing.T) {
	err := restoreTargetVolumeNotEmptyError(restoreSession{TargetVolume: "app data"})
	hints := strings.Join(HintsForError(err), "\n")

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
