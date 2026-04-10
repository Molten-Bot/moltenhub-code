package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEntrypointScriptExportsAugmentSessionAuthFromInitConfig(t *testing.T) {
	t.Parallel()

	env := newEntrypointTestEnv(t)
	initPath := filepath.Join(env.configDir, "init.json")
	if err := os.WriteFile(initPath, []byte(`{
  "base_url": "https://na.hub.molten.bot/v1",
  "agent_token": "agent_token",
  "augment_session_auth": "{\"accessToken\":\"token_from_init\",\"tenantURL\":\"https://tenant.example\",\"scopes\":[\"email\"]}"
}`), 0o644); err != nil {
		t.Fatalf("write init config: %v", err)
	}

	output, err := runEntrypointScript(t, env, nil)
	if err != nil {
		t.Fatalf("entrypoint error: %v\noutput: %s", err, output)
	}

	got, err := os.ReadFile(env.augmentPath)
	if err != nil {
		t.Fatalf("read augment env file: %v", err)
	}
	want := `{"accessToken":"token_from_init","tenantURL":"https://tenant.example","scopes":["email"]}`
	if string(got) != want {
		t.Fatalf("AUGMENT_SESSION_AUTH = %q, want %q", string(got), want)
	}
}

func TestEntrypointScriptExportsAugmentSessionAuthFromRunConfig(t *testing.T) {
	t.Parallel()

	env := newEntrypointTestEnv(t)
	configPath := filepath.Join(env.configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "test prompt",
  "augmentSessionAuth": "{\"accessToken\":\"token_from_run\",\"tenantURL\":\"https://tenant.example\",\"scopes\":[\"email\"]}"
}`), 0o644); err != nil {
		t.Fatalf("write run config: %v", err)
	}

	output, err := runEntrypointScript(t, env, nil)
	if err != nil {
		t.Fatalf("entrypoint error: %v\noutput: %s", err, output)
	}

	got, err := os.ReadFile(env.augmentPath)
	if err != nil {
		t.Fatalf("read augment env file: %v", err)
	}
	want := `{"accessToken":"token_from_run","tenantURL":"https://tenant.example","scopes":["email"]}`
	if string(got) != want {
		t.Fatalf("AUGMENT_SESSION_AUTH = %q, want %q", string(got), want)
	}
}

type entrypointTestEnv struct {
	root        string
	configDir   string
	augmentPath string
	piPath      string
}

func newEntrypointTestEnv(t *testing.T) entrypointTestEnv {
	t.Helper()

	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	writeEntrypointStub(t, filepath.Join(binDir, "git"), "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, filepath.Join(binDir, "gh"), "#!/bin/sh\nexit 0\n")
	writeEntrypointStub(t, filepath.Join(binDir, "envdump"), "#!/bin/sh\nset -eu\nprintf '%s' \"${AUGMENT_SESSION_AUTH:-}\" > \"${HARNESS_STUB_AUGMENT_FILE}\"\nprintf '%s' \"${OPENAI_API_KEY:-}\" > \"${HARNESS_STUB_PI_FILE}\"\n")

	return entrypointTestEnv{
		root:        root,
		configDir:   configDir,
		augmentPath: filepath.Join(root, "augment-session-auth.txt"),
		piPath:      filepath.Join(root, "pi-provider-auth.txt"),
	}
}

func writeEntrypointStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
}

func runEntrypointScript(t *testing.T, env entrypointTestEnv, extra map[string]string) (string, error) {
	t.Helper()

	cmd := exec.Command("sh", entrypointScriptPath(t), "envdump")
	pathValue := filepath.Join(env.root, "bin") + ":" + os.Getenv("PATH")
	cmd.Env = []string{
		"PATH=" + pathValue,
		"HARNESS_CONFIG_DIR=" + env.configDir,
		"HARNESS_STUB_AUGMENT_FILE=" + env.augmentPath,
		"HARNESS_STUB_PI_FILE=" + env.piPath,
		"GITHUB_TOKEN=ghp_test",
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

func TestEntrypointScriptExportsPiProviderAuthFromRunConfig(t *testing.T) {
	t.Parallel()

	env := newEntrypointTestEnv(t)
	configPath := filepath.Join(env.configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "repo": "git@github.com:acme/repo.git",
  "prompt": "test prompt",
  "pi_provider_auth": "{\"env_var\":\"OPENAI_API_KEY\",\"value\":\"sk-pi-from-run\"}"
}`), 0o644); err != nil {
		t.Fatalf("write run config: %v", err)
	}

	output, err := runEntrypointScript(t, env, nil)
	if err != nil {
		t.Fatalf("entrypoint error: %v\noutput: %s", err, output)
	}

	got, err := os.ReadFile(env.piPath)
	if err != nil {
		t.Fatalf("read pi env file: %v", err)
	}
	if want := "sk-pi-from-run"; string(got) != want {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", string(got), want)
	}
}
