package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	logDirectoryName    = ".log"
	logFileName         = "terminal.log"
	fallbackLogSubdir   = "main"
	maxLogFileOpenMode  = 0o644
	maxOpenTaskLogFiles = 128
)

type terminalLogSink interface {
	WriteLine(line string)
	Close() error
}

type taskLogMirror struct {
	mu sync.Mutex

	rootDir       string
	aggregate     *os.File
	taskFiles     map[string]*os.File
	taskFileOrder []string
}

func newDefaultTaskLogMirror() (*taskLogMirror, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	return newTaskLogMirror(filepath.Join(wd, logDirectoryName))
}

func newTaskLogMirror(rootDir string) (*taskLogMirror, error) {
	absRoot, err := filepath.Abs(strings.TrimSpace(rootDir))
	if err != nil {
		return nil, fmt.Errorf("resolve log root: %w", err)
	}
	if absRoot == "" {
		return nil, fmt.Errorf("log root is empty")
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create log root: %w", err)
	}

	aggregatePath := filepath.Join(absRoot, logFileName)
	aggregateFile, err := os.OpenFile(aggregatePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, maxLogFileOpenMode)
	if err != nil {
		return nil, fmt.Errorf("open aggregate log file: %w", err)
	}

	return &taskLogMirror{
		rootDir:   absRoot,
		aggregate: aggregateFile,
		taskFiles: make(map[string]*os.File),
	}, nil
}

func (m *taskLogMirror) WriteLine(line string) {
	if m == nil {
		return
	}
	trimmed := strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(trimmed) == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.aggregate != nil {
		_, _ = m.aggregate.WriteString(trimmed + "\n")
	}

	taskFile, err := m.taskLogFileLocked(taskLogSubdirForLine(trimmed))
	if err != nil {
		return
	}
	_, _ = taskFile.WriteString(trimmed + "\n")
}

func (m *taskLogMirror) Close() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	if m.aggregate != nil {
		if err := m.aggregate.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.aggregate = nil
	}
	for _, taskFile := range m.taskFiles {
		if err := taskFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.taskFiles = nil
	m.taskFileOrder = nil

	return firstErr
}

func (m *taskLogMirror) taskLogFileLocked(subdir string) (*os.File, error) {
	taskFilePath, err := m.taskLogFilePathLocked(subdir)
	if err != nil {
		return nil, err
	}
	if existing := m.taskFiles[taskFilePath]; existing != nil {
		return existing, nil
	}
	if m.taskFiles == nil {
		m.taskFiles = make(map[string]*os.File)
	}
	if len(m.taskFiles) >= maxOpenTaskLogFiles {
		m.closeOldestTaskFileLocked()
	}

	taskFile, err := os.OpenFile(taskFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, maxLogFileOpenMode)
	if err != nil {
		return nil, err
	}
	m.taskFiles[taskFilePath] = taskFile
	m.taskFileOrder = append(m.taskFileOrder, taskFilePath)
	return taskFile, nil
}

func (m *taskLogMirror) closeOldestTaskFileLocked() {
	if len(m.taskFileOrder) == 0 {
		return
	}
	oldestPath := m.taskFileOrder[0]
	m.taskFileOrder = m.taskFileOrder[1:]
	taskFile := m.taskFiles[oldestPath]
	delete(m.taskFiles, oldestPath)
	if taskFile != nil {
		_ = taskFile.Close()
	}
}

func (m *taskLogMirror) taskLogFilePathLocked(subdir string) (string, error) {
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		subdir = fallbackLogSubdir
	}

	dirPath := filepath.Join(m.rootDir, subdir)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return "", err
	}

	return filepath.Join(dirPath, logFileName), nil
}

func taskLogSubdirForLine(line string) string {
	fields := parseSimpleKVFields(line)

	if subdir, ok := identifierSubdir(fields["request_id"]); ok {
		return subdir
	}
	if subdir, ok := identifierSubdir(fields["session"]); ok {
		return subdir
	}
	return fallbackLogSubdir
}

func identifierSubdir(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}

	rawParts := strings.Split(id, "-")
	parts := make([]string, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part := sanitizeLogPathPart(rawPart)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return fallbackLogSubdir, true
	}
	return filepath.Join(parts...), true
}

func sanitizeLogPathPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}

	var b strings.Builder
	lastSeparator := false
	for i := 0; i < len(part); i++ {
		ch := part[i]
		switch {
		case ch >= 'a' && ch <= 'z':
			b.WriteByte(ch)
			lastSeparator = false
		case ch >= 'A' && ch <= 'Z':
			b.WriteByte(ch)
			lastSeparator = false
		case ch >= '0' && ch <= '9':
			b.WriteByte(ch)
			lastSeparator = false
		case ch == '-' || ch == '_':
			if b.Len() > 0 && !lastSeparator {
				b.WriteByte('_')
				lastSeparator = true
			}
		default:
			if b.Len() > 0 && !lastSeparator {
				b.WriteByte('_')
				lastSeparator = true
			}
		}
	}

	return strings.Trim(b.String(), "_")
}
