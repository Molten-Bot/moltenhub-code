package execx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Command describes a subprocess execution.
type Command struct {
	Dir  string
	Name string
	Args []string
	// Stdin is optional input piped to the command's stdin.
	Stdin string
}

// Result is subprocess output.
type Result struct {
	Stdout string
	Stderr string
}

// StreamLineHandler receives one output line from a subprocess stream.
type StreamLineHandler func(stream, line string)

// Runner executes subprocesses.
type Runner interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}

// StreamRunner is an optional runner that can stream line-by-line output.
type StreamRunner interface {
	RunStream(ctx context.Context, cmd Command, handler StreamLineHandler) (Result, error)
}

// OSRunner executes commands via os/exec.
type OSRunner struct{}

// Run executes cmd and captures output.
func (OSRunner) Run(ctx context.Context, cmd Command) (Result, error) {
	return runWithStream(ctx, cmd, nil)
}

// RunStream executes cmd, streams output lines to handler, and captures output.
func (OSRunner) RunStream(ctx context.Context, cmd Command, handler StreamLineHandler) (Result, error) {
	return runWithStream(ctx, cmd, handler)
}

func runWithStream(ctx context.Context, cmd Command, handler StreamLineHandler) (Result, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stdoutEmitter := lineEmitter{stream: "stdout", handler: handler}
	stderrEmitter := lineEmitter{stream: "stderr", handler: handler}
	c.Stdout = io.MultiWriter(&stdout, &stdoutEmitter)
	c.Stderr = io.MultiWriter(&stderr, &stderrEmitter)

	err := c.Run()
	stdoutEmitter.Flush()
	stderrEmitter.Flush()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if detail := commandFailureDetail(res); detail != "" {
			return res, fmt.Errorf("run %s %v: %w (%s)", cmd.Name, cmd.Args, err, detail)
		}
		return res, fmt.Errorf("run %s %v: %w", cmd.Name, cmd.Args, err)
	}
	return res, nil
}

func commandFailureDetail(res Result) string {
	if detail := summarizeOutputTail(res.Stderr); detail != "" {
		return detail
	}
	return summarizeOutputTail(res.Stdout)
}

func summarizeOutputTail(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	tail := make([]string, 0, 3)
	for i := len(lines) - 1; i >= 0 && len(tail) < 3; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		tail = append(tail, line)
	}
	if len(tail) == 0 {
		return ""
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}

	summary := strings.Join(tail, " | ")
	const maxSummaryChars = 320
	if len(summary) > maxSummaryChars {
		summary = summary[:maxSummaryChars-3] + "..."
	}
	return summary
}

type lineEmitter struct {
	stream  string
	handler StreamLineHandler
	pending bytes.Buffer
}

func (w *lineEmitter) Write(p []byte) (int, error) {
	if w == nil || w.handler == nil {
		return len(p), nil
	}

	w.pending.Write(p)
	for {
		data := w.pending.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}

		line := string(data[:idx])
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
		}
		w.handler(w.stream, line)
		w.pending.Next(idx + 1)
	}

	return len(p), nil
}

func (w *lineEmitter) Flush() {
	if w == nil || w.handler == nil || w.pending.Len() == 0 {
		return
	}
	line := w.pending.String()
	if strings.HasSuffix(line, "\r") {
		line = strings.TrimSuffix(line, "\r")
	}
	w.pending.Reset()
	w.handler(w.stream, line)
}
