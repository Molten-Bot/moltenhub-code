package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEntrypointSeedsRailsmithCodexSkill(t *testing.T) {
	env := newEntrypointTestEnv(t)
	guide := "# railsmith Agent Guide\n\nRun `railsmith doctor --root .`."
	writeEntrypointStub(t, env, "npm", "#!/bin/sh\nset -eu\nif [ \"${1:-}\" = \"root\" ] && [ \"${2:-}\" = \"-g\" ]; then\n  printf '%s\\n' \"${RAILSMITH_NPM_ROOT}\"\n  exit 0\nfi\nexit 1\n")
	writeEntrypointStub(t, env, "git", "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, env, "gh", "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, env, "true", "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(env.npmRoot, "@moltenbot", "railsmith", "AGENT_GUIDE.md"), guide)

	output, err := runEntrypointScript(t, env, nil, "true")
	if err != nil {
		t.Fatalf("entrypoint error: %v\noutput: %s", err, output)
	}

	skillPath := filepath.Join(env.homeDir, ".codex", "skills", "railsmith", "SKILL.md")
	content := readFileTrimmed(t, skillPath)
	for _, want := range []string{
		"name: railsmith",
		"Use Railsmith to create, maintain, validate, or improve AGENTS.md",
		"# railsmith Agent Guide",
		"railsmith doctor --root .",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("skill missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestEntrypointDoesNotOverwriteExistingRailsmithCodexSkill(t *testing.T) {
	env := newEntrypointTestEnv(t)
	writeEntrypointStub(t, env, "npm", "#!/bin/sh\nset -eu\nif [ \"${1:-}\" = \"root\" ] && [ \"${2:-}\" = \"-g\" ]; then\n  printf '%s\\n' \"${RAILSMITH_NPM_ROOT}\"\n  exit 0\nfi\nexit 1\n")
	writeEntrypointStub(t, env, "git", "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, env, "gh", "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, env, "true", "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(env.npmRoot, "@moltenbot", "railsmith", "AGENT_GUIDE.md"), "new package guide")
	skillPath := filepath.Join(env.homeDir, ".codex", "skills", "railsmith", "SKILL.md")
	writeFile(t, skillPath, "existing user skill")

	output, err := runEntrypointScript(t, env, nil, "true")
	if err != nil {
		t.Fatalf("entrypoint error: %v\noutput: %s", err, output)
	}

	if got := readFileTrimmed(t, skillPath); got != "existing user skill" {
		t.Fatalf("skill content = %q, want existing user skill", got)
	}
}

type entrypointTestEnv struct {
	root      string
	binDir    string
	configDir string
	homeDir   string
	npmRoot   string
}

func newEntrypointTestEnv(t *testing.T) entrypointTestEnv {
	t.Helper()

	root := t.TempDir()
	env := entrypointTestEnv{
		root:      root,
		binDir:    filepath.Join(root, "bin"),
		configDir: filepath.Join(root, "config"),
		homeDir:   filepath.Join(root, "home"),
		npmRoot:   filepath.Join(root, "npm-global"),
	}
	for _, dir := range []string{env.binDir, env.configDir, env.homeDir, env.npmRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return env
}

func writeEntrypointStub(t *testing.T, env entrypointTestEnv, name, content string) {
	t.Helper()
	path := filepath.Join(env.binDir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runEntrypointScript(t *testing.T, env entrypointTestEnv, extra map[string]string, args ...string) (string, error) {
	t.Helper()

	cmdArgs := append([]string{entrypointScriptPath(t)}, args...)
	cmd := exec.Command("sh", cmdArgs...)
	cmd.Env = []string{
		"PATH=" + env.binDir + ":" + os.Getenv("PATH"),
		"HARNESS_CONFIG_DIR=" + env.configDir,
		"HOME=" + env.homeDir,
		"RAILSMITH_NPM_ROOT=" + env.npmRoot,
	}
	for key, value := range extra {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func entrypointScriptPath(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "docker", "entrypoint.sh")
}
