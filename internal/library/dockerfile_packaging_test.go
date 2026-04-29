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
	if !strings.Contains(content, "HARNESS_AGENTS_SEED_PATH=/opt/moltenhub/library/AGENTS.md") {
		t.Fatalf("%s does not configure the runtime agents seed path", dockerfilePath)
	}
	if !strings.Contains(content, "COPY library /opt/moltenhub/library") {
		t.Fatalf("%s does not copy the full library directory into the runtime image", dockerfilePath)
	}
	if !containsAny(content,
		"COPY library /workspace/library",
		"ln -s /opt/moltenhub/library /workspace/library",
	) {
		t.Fatalf("%s does not make the full library directory available at /workspace/library for hub runtime loading", dockerfilePath)
	}
	if strings.Contains(content, "COPY library/AGENTS.md /opt/moltenhub/library/AGENTS.md") {
		t.Fatalf("%s still only copies library/AGENTS.md into the runtime image", dockerfilePath)
	}
}

func TestRuntimeDockerfileCopiesSkillsCatalog(t *testing.T) {
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
	if !strings.Contains(content, "COPY skills /opt/moltenhub/skills") {
		t.Fatalf("%s does not copy the full skills directory into the runtime image", dockerfilePath)
	}
	if !containsAny(content,
		"COPY skills /workspace/skills",
		"ln -s /opt/moltenhub/skills /workspace/skills",
	) {
		t.Fatalf("%s does not make the full skills directory available at /workspace/skills for hub runtime inspection", dockerfilePath)
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

func TestRuntimeDockerfileInstallsPlaywrightTest(t *testing.T) {
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
	for _, want := range []string{
		"playwright@latest",
		"@playwright/test@latest",
		"NODE_PATH=/usr/local/lib/node_modules",
		"PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright",
		"PLAYWRIGHT_SKIP_BROWSER_GC=1",
		"playwright install --with-deps chromium",
		"chown -R node:node /workspace /opt/ms-playwright",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s does not install Playwright runtime requirement %q", dockerfilePath, want)
		}
	}
	for _, forbidden := range []string{
		"PLAYWRIGHT_BROWSERS_PATH=/workspace/config",
		"PLAYWRIGHT_BROWSERS_PATH=/workspace",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s stores Playwright browsers under a persisted workspace path: %q", dockerfilePath, forbidden)
		}
	}
}

func TestRuntimeDockerfileInstallsPythonTooling(t *testing.T) {
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
	for _, want := range []string{
		"python3",
		"python3-pip",
		"python3-venv",
		"ln -sf /usr/bin/python3 /usr/local/bin/python",
		"ln -sf /usr/bin/pip3 /usr/local/bin/pip",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s does not install Python runtime requirement %q", dockerfilePath, want)
		}
	}
}

func TestRuntimeDockerfileInstallsOpenAIPythonSDK(t *testing.T) {
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
	for _, want := range []string{
		"python3 -m pip install",
		"--upgrade openai",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s does not install latest OpenAI Python SDK requirement %q", dockerfilePath, want)
		}
	}
}

func containsAny(content string, want ...string) bool {
	for _, candidate := range want {
		if strings.Contains(content, candidate) {
			return true
		}
	}
	return false
}

func TestRuntimeDockerfileUsesDebianBaseImages(t *testing.T) {
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
	for _, want := range []string{
		"FROM golang:1.26.1-bookworm AS build",
		"FROM node:25.8.1-bookworm-slim AS runtime",
		"apt-get update",
		"apt-get install -y --no-install-recommends",
		"file",
		"gh",
		"openssh-client",
		"HARNESS_WORKSPACE_RAM_BASE=/workspace",
		"HARNESS_WORKSPACE_DISK_BASE=/workspace",
		"HOME=/workspace/config/home",
		"mkdir -p /workspace/config/home /workspace/moltenhub-code/tasks",
		"chown -R node:node /workspace",
		"ln -sf /usr/local/go/bin/go /usr/local/bin/go",
		"ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt",
		"USER node",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing Debian runtime requirement %q", dockerfilePath, want)
		}
	}

	for _, forbidden := range []string{
		"alpine",
		"apk add",
		"github-cli",
		"openssh-client-default",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s still contains Alpine-specific token %q", dockerfilePath, forbidden)
		}
	}
}
