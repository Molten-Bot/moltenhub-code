package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
)

const (
	logDirectoryName    = ".log"
	maxLogFileOpenMode  = 0o644
	maxOpenTaskLogFiles = 128
)

const (
	logFileName           = failurefollowup.LogFileName
	legacyTaskLogFileName = failurefollowup.LegacyTaskLogFileName
	fallbackLogSubdir     = failurefollowup.FallbackLogSubdir
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
	rootDir, err := defaultLogRoot()
	if err != nil {
		return nil, err
	}
	return newTaskLogMirror(rootDir)
}

func newTaskLogMirror(rootDir string) (*taskLogMirror, error) {
	absRoot, err := filepath.Abs(strings.TrimSpace(rootDir))
	if err != nil {
		return nil, fmt.Errorf("resolve log root: %w", err)
	}
	if absRoot == "" {
		return nil, fmt.Errorf("log root is empty")
	}
	if err := resetLogRoot(absRoot); err != nil {
		return nil, fmt.Errorf("reset log root: %w", err)
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

func resetLogRoot(absRoot string) error {
	absRoot = strings.TrimSpace(absRoot)
	if absRoot == "" {
		return fmt.Errorf("log root is empty")
	}
	if filepath.Base(absRoot) != logDirectoryName {
		return fmt.Errorf("refusing to reset non-%s directory: %s", logDirectoryName, absRoot)
	}
	if err := os.RemoveAll(absRoot); err != nil {
		return err
	}
	return os.MkdirAll(absRoot, 0o755)
}

func defaultLogRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return defaultLogRootForWorkingDir(wd, resetLogRoot)
}

func defaultLogRootForWorkingDir(wd string, reset func(string) error) (string, error) {
	primary := filepath.Join(wd, logDirectoryName)
	if err := reset(primary); err == nil {
		return primary, nil
	} else {
		fallback := filepath.Join(os.TempDir(), "moltenhub-code", "logs", logRootHash(wd), logDirectoryName)
		if fallbackErr := reset(fallback); fallbackErr == nil {
			return fallback, nil
		} else {
			return "", fmt.Errorf("reset log root: %w; fallback %s: %v", err, fallback, fallbackErr)
		}
	}
}

func logRootHash(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(text)))
	return fmt.Sprintf("%016x", h.Sum64())
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

	subdir := taskLogSubdirForLine(trimmed)
	for _, fileName := range []string{logFileName, legacyTaskLogFileName} {
		taskFile, err := m.taskLogFileLocked(subdir, fileName)
		if err != nil {
			continue
		}
		_, _ = taskFile.WriteString(trimmed + "\n")
	}
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

func (m *taskLogMirror) taskLogFileLocked(subdir, fileName string) (*os.File, error) {
	taskFilePath, err := m.taskLogFilePathLocked(subdir, fileName)
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

func (m *taskLogMirror) taskLogFilePathLocked(subdir, fileName string) (string, error) {
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		subdir = fallbackLogSubdir
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = logFileName
	}

	dirPath := filepath.Join(m.rootDir, subdir)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return "", err
	}

	return filepath.Join(dirPath, fileName), nil
}

func taskLogSubdirForLine(line string) string {
	fields := parseSimpleKVFields(line)

	if subdir, ok := failurefollowup.IdentifierSubdir(fields["request_id"]); ok {
		return subdir
	}
	if subdir, ok := failurefollowup.IdentifierSubdir(fields["session"]); ok {
		return subdir
	}
	return fallbackLogSubdir
}
