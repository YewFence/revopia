package bridge

import (
	"fmt"
	"io"
	"time"
)

type Logger struct {
	out io.Writer
}

func NewLogger(out io.Writer) Logger {
	return Logger{out: out}
}

func (l Logger) Printf(format string, args ...any) {
	if l.out == nil {
		return
	}
	values := append([]any{time.Now().Format(time.RFC3339Nano)}, args...)
	_, _ = fmt.Fprintf(l.out, "ts=%s "+format+"\n", values...)
}
