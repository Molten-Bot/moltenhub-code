package library

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeDockerfileCopiesFullLibraryCatalog(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	dockerfilePath := filepath.Join(repoRoot, "Dockerfile")

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", dockerfilePath, err)
	}

	content := string(data)
	if !strings.Contains(content, "COPY library /opt/moltenhub/library") {
		t.Fatalf("%s does not copy the full library directory into the runtime image", dockerfilePath)
	}
	if !strings.Contains(content, "COPY library /workspace/library") {
		t.Fatalf("%s does not copy the full library directory into /workspace/library for hub runtime loading", dockerfilePath)
	}
	if strings.Contains(content, "COPY library/AGENTS.md /opt/moltenhub/library/AGENTS.md") {
		t.Fatalf("%s still only copies library/AGENTS.md into the runtime image", dockerfilePath)
	}
}

func TestRuntimeDockerfileInstallsRipgrep(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	dockerfilePath := filepath.Join(repoRoot, "Dockerfile")

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", dockerfilePath, err)
	}

	if !strings.Contains(string(data), "ripgrep") {
		t.Fatalf("%s does not install ripgrep in the runtime image", dockerfilePath)
	}
}
