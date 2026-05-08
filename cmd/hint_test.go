package cmd

import (
	"errors"
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
