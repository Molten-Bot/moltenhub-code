package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const maxDecodedCommandLogText = 240

type terminalLogMode int

const (
	terminalLogModeNormal terminalLogMode = iota
	terminalLogModeProgress
	terminalLogModeDrop
)

type terminalLogger struct {
	mu sync.Mutex

	out io.Writer
	tty bool

	sink terminalLogSink

	progressActive bool
	progressWidth  int
}

func newDefaultTerminalLogger() *terminalLogger {
	logger := newTerminalLogger(os.Stderr, isTerminalFile(os.Stderr))
	sink, err := newDefaultTaskLogMirror()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to initialize %s log mirror: %v\n", logDirectoryName, err)
		return logger
	}
	logger.sink = sink
	return logger
}

func newTerminalLogger(out io.Writer, tty bool) *terminalLogger {
	return &terminalLogger{
		out: out,
		tty: tty,
	}
}

func isTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func (l *terminalLogger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	l.Print(fmt.Sprintf(format, args...))
}

func (l *terminalLogger) Print(line string) {
	if l == nil {
		return
	}

	rendered, mode := formatTerminalLogLine(strings.TrimSpace(line))
	if mode == terminalLogModeDrop || rendered == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	switch mode {
	case terminalLogModeProgress:
		if l.tty {
			l.writeSinkLocked(rendered)
			l.renderProgressLocked(rendered)
			return
		}
		l.clearProgressLocked()
		_, _ = fmt.Fprintln(l.out, rendered)
		l.writeSinkLocked(rendered)
	default:
		l.clearProgressLocked()
		_, _ = fmt.Fprintln(l.out, rendered)
		l.writeSinkLocked(rendered)
	}
}

func (l *terminalLogger) Capture(line string) {
	if l == nil {
		return
	}
	rendered := strings.TrimSpace(line)
	if rendered == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeSinkLocked(rendered)
}

func (l *terminalLogger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.progressActive {
		_, _ = fmt.Fprintln(l.out)
		l.progressActive = false
		l.progressWidth = 0
	}
	if l.sink != nil {
		_ = l.sink.Close()
		l.sink = nil
	}
}

func (l *terminalLogger) writeSinkLocked(line string) {
	if l.sink == nil {
		return
	}
	l.sink.WriteLine(line)
}

func (l *terminalLogger) renderProgressLocked(text string) {
	pad := ""
	if l.progressWidth > len(text) {
		pad = strings.Repeat(" ", l.progressWidth-len(text))
	}
	_, _ = fmt.Fprintf(l.out, "\r%s%s", text, pad)
	l.progressActive = true
	l.progressWidth = len(text)
}

func (l *terminalLogger) clearProgressLocked() {
	if !l.progressActive {
		return
	}
	if l.tty {
		clear := strings.Repeat(" ", l.progressWidth)
		_, _ = fmt.Fprintf(l.out, "\r%s\r", clear)
	}
	l.progressActive = false
	l.progressWidth = 0
}

func formatTerminalLogLine(line string) (string, terminalLogMode) {
	if line == "" {
		return "", terminalLogModeDrop
	}

	if progress, ok := compactCodexProgressLine(line); ok {
		return progress, terminalLogModeProgress
	}

	if decoded, handled, drop := decodeCommandLogLine(line); handled {
		if drop {
			return "", terminalLogModeDrop
		}
		return decoded, terminalLogModeNormal
	}

	return line, terminalLogModeNormal
}

func compactCodexProgressLine(line string) (string, bool) {
	if !strings.Contains(line, "stage=codex") || !strings.Contains(line, "status=running") {
		return "", false
	}

	fields := parseSimpleKVFields(line)
	if fields["stage"] != "codex" || fields["status"] != "running" {
		return "", false
	}
	if fields["request_id"] != "" || fields["session"] != "" {
		return "", false
	}

	elapsed := strings.TrimSpace(fields["elapsed_s"])
	if elapsed == "" {
		return "codex running...", true
	}
	return fmt.Sprintf("codex running... %ss", elapsed), true
}

func decodeCommandLogLine(line string) (text string, handled bool, drop bool) {
	hasCmdMarker := strings.HasPrefix(line, "cmd ") || strings.Contains(line, " cmd ")
	if !hasCmdMarker || !strings.Contains(line, "b64=") {
		return "", false, false
	}

	fields := parseSimpleKVFields(line)
	encoded := strings.TrimSpace(fields["b64"])
	if encoded == "" {
		return "", false, false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false, false
	}

	decodedText := strings.TrimSpace(string(decoded))
	if decodedText == "" {
		return "", true, true
	}

	if suppressCodexCommandOutput(fields, decodedText) {
		return "", true, true
	}

	if len(decodedText) > maxDecodedCommandLogText {
		decodedText = decodedText[:maxDecodedCommandLogText-3] + "..."
	}

	prefix := stripSimpleKVField(line, "b64")
	if prefix == "" {
		prefix = "cmd"
	}
	return fmt.Sprintf("%s text=%q", prefix, decodedText), true, false
}

func suppressCodexCommandOutput(fields map[string]string, text string) bool {
	phase := strings.ToLower(strings.TrimSpace(fields["phase"]))
	name := strings.ToLower(strings.TrimSpace(fields["name"]))
	if phase != "codex" && name != "codex" {
		return false
	}
	return !isImportantCodexCommandText(text)
}

func isImportantCodexCommandText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}

	keywords := []string{
		"error",
		"failed",
		"panic",
		"fatal",
		"exception",
		"traceback",
		"timed out",
		"timeout",
		"permission denied",
		"denied",
		"not found",
		"no such file",
		"unable",
		"cannot",
		"can't",
		"invalid",
		"refused",
		"segmentation fault",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func parseSimpleKVFields(line string) map[string]string {
	if !strings.Contains(line, "=") {
		return nil
	}
	out := make(map[string]string, 8)
	for _, field := range strings.Fields(line) {
		key, val, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		out[key] = strings.Trim(val, "\"")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stripSimpleKVField(line, key string) string {
	if key == "" {
		return strings.TrimSpace(line)
	}

	needle := key + "="
	parts := strings.Fields(line)
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, needle) {
			continue
		}
		kept = append(kept, part)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}
