package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

const maxDecodedCommandLogText = 240

type terminalLogMode int

const (
	terminalLogModeNormal terminalLogMode = iota
	terminalLogModeProgress
	terminalLogModeSinkOnly
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
	case terminalLogModeSinkOnly:
		l.clearProgressLocked()
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
	if strings.HasPrefix(line, "debug ") {
		return line, terminalLogModeSinkOnly
	}

	if progress, ok := compactAgentProgressLine(line); ok {
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

func compactAgentProgressLine(line string) (string, bool) {
	if !strings.Contains(line, "status=running") {
		return "", false
	}

	fields := parseSimpleKVFields(line)
	stage := strings.ToLower(strings.TrimSpace(fields["stage"]))
	if !isCompactableAgentStage(stage) || fields["status"] != "running" {
		return "", false
	}
	if fields["request_id"] != "" || fields["session"] != "" {
		return "", false
	}

	elapsed := strings.TrimSpace(fields["elapsed_s"])
	if elapsed == "" {
		return fmt.Sprintf("%s running...", stage), true
	}
	return fmt.Sprintf("%s running... %ss", stage, elapsed), true
}

func isCompactableAgentStage(stage string) bool {
	switch stage {
	case "agent", "codex", "claude", "auggie", "pi":
		return true
	default:
		return false
	}
}

func decodeCommandLogLine(line string) (text string, handled bool, drop bool) {
	hasCmdMarker := strings.HasPrefix(line, "cmd ") || strings.Contains(line, " cmd ")
	if !hasCmdMarker || !strings.Contains(line, "b64=") {
		return "", false, false
	}

	fields := parseSimpleKVFields(line)
	encoded := strings.TrimSpace(fields["b64"])
	if encoded == "" {
		return "", true, true
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
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return false
	}
	// Suppress nested harness log echoes to avoid recursive log amplification
	// during follow-up investigations.
	if looksLikeNestedHarnessLogLine(lower) {
		return false
	}

	if strings.HasPrefix(lower, "/bin/bash -lc") || strings.HasPrefix(lower, "bash -lc") {
		return false
	}

	if looksLikeSourceSearchResultLine(trimmed) {
		return containsCompilerStyleFailureSignal(lower)
	}

	markers := []string{
		"error:",
		"fatal:",
		"panic:",
		"traceback",
		"exception in thread",
		"unhandled exception",
		"exception:",
		"timed out",
		"timeout",
		"permission denied",
		"no such file or directory",
		"no such file",
		"not found",
		"segmentation fault",
		"failed to ",
		"task failed",
		"unable to ",
		"cannot ",
		"can't ",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.HasPrefix(lower, "failure:") || strings.HasPrefix(lower, "error ") {
		return true
	}
	return false
}

func looksLikeNestedHarnessLogLine(lower string) bool {
	lower = strings.TrimSpace(lower)
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "dispatch ") || strings.HasPrefix(lower, "stage=") || strings.HasPrefix(lower, "cmd phase=") {
		return true
	}
	// rg search output often prefixes matched lines with "<line>:dispatch ...".
	// Match the embedded harness line so those echoes stay suppressed too.
	if strings.Contains(lower, "dispatch request_id=") &&
		(strings.Contains(lower, " stage=") || strings.Contains(lower, " cmd phase=")) {
		return true
	}
	return false
}

func looksLikeSourceSearchResultLine(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	first := strings.IndexByte(text, ':')
	if first <= 0 {
		return false
	}
	secondRel := strings.IndexByte(text[first+1:], ':')
	if secondRel < 0 {
		return false
	}
	second := first + 1 + secondRel
	if !simpleKVDigitsOnly(text[first+1 : second]) {
		return false
	}
	path := text[:first]
	return strings.ContainsAny(path, `/\.`)
}

func containsCompilerStyleFailureSignal(lower string) bool {
	signals := []string{
		" error:",
		" undefined:",
		" imported and not used",
		" syntax error",
		" mismatched types",
		" no required module provides package",
		" build failed",
	}
	for _, signal := range signals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

func simpleKVDigitsOnly(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

func parseSimpleKVFields(line string) map[string]string {
	if !strings.Contains(line, "=") {
		return nil
	}
	out := make(map[string]string, 8)
	for _, token := range parseSimpleKVTokens(line) {
		out[token.key] = token.value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type simpleKVToken struct {
	key   string
	value string
}

func parseSimpleKVTokens(line string) []simpleKVToken {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	tokens := make([]simpleKVToken, 0, 8)
	for idx := 0; idx < len(line); {
		for idx < len(line) && isSimpleKVSpace(line[idx]) {
			idx++
		}
		if idx >= len(line) {
			break
		}

		keyStart := idx
		for idx < len(line) && !isSimpleKVSpace(line[idx]) && line[idx] != '=' {
			idx++
		}
		if idx >= len(line) || line[idx] != '=' {
			for idx < len(line) && !isSimpleKVSpace(line[idx]) {
				idx++
			}
			continue
		}

		key := strings.TrimSpace(line[keyStart:idx])
		idx++
		if key == "" {
			continue
		}

		value, next := parseSimpleKVValue(line, idx)
		tokens = append(tokens, simpleKVToken{key: key, value: value})
		idx = next
	}

	return tokens
}

func parseSimpleKVValue(line string, idx int) (string, int) {
	if idx >= len(line) {
		return "", idx
	}

	if line[idx] == '"' {
		quoted, next, ok := scanSimpleKVQuotedValue(line, idx)
		if ok {
			if decoded, err := strconv.Unquote(quoted); err == nil {
				return strings.TrimSpace(decoded), next
			}
			return strings.TrimSpace(strings.Trim(quoted, `"`)), next
		}
	}

	start := idx
	for idx < len(line) && !isSimpleKVSpace(line[idx]) {
		idx++
	}
	return strings.TrimSpace(line[start:idx]), idx
}

func scanSimpleKVQuotedValue(line string, start int) (string, int, bool) {
	if start >= len(line) || line[start] != '"' {
		return "", start, false
	}
	for idx := start + 1; idx < len(line); idx++ {
		if line[idx] != '"' {
			continue
		}
		if isSimpleKVEscaped(line, idx) {
			continue
		}
		return line[start : idx+1], idx + 1, true
	}
	return "", start, false
}

func isSimpleKVEscaped(text string, idx int) bool {
	backslashes := 0
	for pos := idx - 1; pos >= 0 && text[pos] == '\\'; pos-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func isSimpleKVSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
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
