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

	if !strings.Contains(string(data), "@playwright/test@latest") {
		t.Fatalf("%s does not install @playwright/test in the runtime image", dockerfilePath)
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
		"py3-pip",
		"py3-virtualenv",
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

func TestRuntimeDockerfileUsesAlpineBaseImages(t *testing.T) {
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
		"FROM golang:1.26.1-alpine3.23 AS build",
		"FROM node:25.8.1-alpine3.23 AS runtime",
		"apk add --no-cache",
		"github-cli",
		"openssh-client-default",
		"HARNESS_WORKSPACE_RAM_BASE=/workspace",
		"HARNESS_WORKSPACE_DISK_BASE=/workspace",
		"mkdir -p /workspace/config /workspace/moltenhub-code/tasks",
		"chown -R node:node /workspace",
		"USER node",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("%s missing Alpine runtime requirement %q", dockerfilePath, want)
		}
	}

	for _, forbidden := range []string{
		"bookworm",
		"apt-get",
		"useradd --create-home",
	} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("%s still contains Debian-specific token %q", dockerfilePath, forbidden)
		}
	}
}
