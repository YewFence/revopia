package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/yewfence/revopia/internal/bridge"
)

type hintedError struct {
	err   error
	hints []string
}

func withHints(err error, hints ...string) error {
	if err == nil {
		return nil
	}
	return hintedError{
		err:   err,
		hints: compactHints(hints),
	}
}

func (e hintedError) Error() string {
	return e.err.Error()
}

func (e hintedError) Unwrap() error {
	return e.err
}

func (e hintedError) Hints() []string {
	return e.hints
}

func writeErrorWithHints(out io.Writer, err error) {
	_, _ = fmt.Fprintln(out, err)
	hints := hintsForError(err)
	if len(hints) == 0 {
		return
	}
	_, _ = fmt.Fprintln(out, "提示")
	for _, hint := range hints {
		_, _ = fmt.Fprintf(out, "  %s\n", hint)
	}
}

func hintsForError(err error) []string {
	return compactHints(append(collectHints(err, nil), bridge.HintsForError(err)...))
}

func collectHints(err error, hints []string) []string {
	if err == nil {
		return hints
	}

	var hinted interface {
		Hints() []string
	}
	if errors.As(err, &hinted) {
		hints = append(hints, hinted.Hints()...)
	}

	type multiUnwrapper interface {
		Unwrap() []error
	}
	if unwrapped, ok := err.(multiUnwrapper); ok {
		for _, child := range unwrapped.Unwrap() {
			hints = collectHints(child, hints)
		}
		return hints
	}

	type unwrapper interface {
		Unwrap() error
	}
	if unwrapped, ok := err.(unwrapper); ok {
		return collectHints(unwrapped.Unwrap(), hints)
	}
	return hints
}

func compactHints(hints []string) []string {
	seen := make(map[string]struct{}, len(hints))
	result := make([]string, 0, len(hints))
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		if _, ok := seen[hint]; ok {
			continue
		}
		seen[hint] = struct{}{}
		result = append(result, hint)
	}
	return result
}
