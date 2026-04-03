package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultRAMBase  = "/dev/shm"
	defaultDiskBase = "/tmp"
	defaultRunRoot  = "temp"
	agentsSeedPath  = "library/AGENTS.md"
	agentsFileName  = "AGENTS.md"
	agentsSeedEnv   = "HARNESS_AGENTS_SEED_PATH"
)

// Manager creates isolated run directories.
type Manager struct {
	PathExists func(string) bool
	MkdirAll   func(string, os.FileMode) error
	NewGUID    func() string
	ReadFile   func(string) ([]byte, error)
	WriteFile  func(string, []byte, os.FileMode) error
}

// NewManager returns a manager backed by os functions.
func NewManager() Manager {
	return Manager{
		PathExists: pathExists,
		MkdirAll:   os.MkdirAll,
		NewGUID:    newGUID,
		ReadFile:   os.ReadFile,
		WriteFile:  os.WriteFile,
	}
}

// SelectBase chooses /dev/shm when available, else /tmp.
func (m Manager) SelectBase() string {
	exists := m.PathExists
	if exists == nil {
		exists = pathExists
	}
	if exists(defaultRAMBase) {
		return defaultRAMBase
	}
	return defaultDiskBase
}

// CreateRunDir creates a GUID-named run directory under <base>/temp.
func (m Manager) CreateRunDir() (string, string, error) {
	mkdirAll := m.MkdirAll
	if mkdirAll == nil {
		mkdirAll = os.MkdirAll
	}
	guidFn := m.NewGUID
	if guidFn == nil {
		guidFn = newGUID
	}

	guid := guidFn()
	if guid == "" {
		return "", "", fmt.Errorf("generated empty guid")
	}

	preferredBase := m.SelectBase()
	fallbackBase := defaultDiskBase
	if preferredBase == defaultDiskBase {
		fallbackBase = ""
	}

	candidates := []string{preferredBase}
	if fallbackBase != "" && fallbackBase != preferredBase {
		candidates = append(candidates, fallbackBase)
	}

	var lastErr error
	for _, base := range candidates {
		runDir := filepath.Join(base, defaultRunRoot, guid)
		if err := mkdirAll(runDir, 0o755); err != nil {
			lastErr = err
			continue
		}
		return runDir, guid, nil
	}

	if lastErr != nil {
		return "", "", fmt.Errorf("create run dir: %w", lastErr)
	}
	return "", "", fmt.Errorf("create run dir: no workspace base candidates")
}

// SeedAgentsFile copies the local library AGENTS seed into a run directory.
func (m Manager) SeedAgentsFile(runDir string) (string, error) {
	readFile := m.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	writeFile := m.WriteFile
	if writeFile == nil {
		writeFile = os.WriteFile
	}

	content, err := readFile(agentsSeedPath)
	if err != nil {
		fallbackPath := resolveAgentsSeedPath()
		if fallbackPath != "" && fallbackPath != agentsSeedPath {
			content, err = readFile(fallbackPath)
		}
		if err != nil {
			return "", fmt.Errorf("read agents seed: %w", err)
		}
	}
	dst := filepath.Join(runDir, agentsFileName)
	if err := writeFile(dst, content, 0o644); err != nil {
		return "", fmt.Errorf("write agents seed: %w", err)
	}
	return dst, nil
}

func pathExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.IsDir()
}

func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func resolveAgentsSeedPath() string {
	if configured := strings.TrimSpace(os.Getenv(agentsSeedEnv)); configured != "" {
		if st, err := os.Stat(configured); err == nil && !st.IsDir() {
			return configured
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if path, ok := findPathUpward(wd, agentsSeedPath); ok {
			return path
		}
	}
	if exePath, err := os.Executable(); err == nil {
		if path, ok := findPathUpward(filepath.Dir(exePath), agentsSeedPath); ok {
			return path
		}
	}
	return ""
}

func findPathUpward(startDir, relPath string) (string, bool) {
	startDir = strings.TrimSpace(startDir)
	relPath = strings.TrimSpace(relPath)
	if startDir == "" || relPath == "" {
		return "", false
	}

	current := filepath.Clean(startDir)
	for {
		candidate := filepath.Join(current, relPath)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}
