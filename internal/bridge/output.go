package bridge

import (
	"fmt"
	"io"
)

func writeLine(out io.Writer, text string) error {
	_, err := fmt.Fprintln(out, text)
	return err
}

func writef(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, args...)
	return err
}
