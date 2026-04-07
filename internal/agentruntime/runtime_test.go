package agentruntime

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveDefaultsToCodex(t *testing.T) {
	t.Parallel()

	rt, err := Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if rt.Harness != HarnessCodex {
		t.Fatalf("Harness = %q, want %q", rt.Harness, HarnessCodex)
	}
	if rt.Command != HarnessCodex {
		t.Fatalf("Command = %q, want %q", rt.Command, HarnessCodex)
	}
	if rt.NPMPackage != "@openai/codex@latest" {
		t.Fatalf("NPMPackage = %q", rt.NPMPackage)
	}
}

func TestDefault(t *testing.T) {
	t.Parallel()

	rt := Default()
	if rt.Harness != HarnessCodex || rt.Command != HarnessCodex {
		t.Fatalf("Default() = %+v", rt)
	}
}

func TestResolveSupportsKnownHarnesses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		harness string
		command string
		pkg     string
		reqName string
	}{
		{name: "claude", harness: HarnessClaude, command: "claude", pkg: "@anthropic-ai/claude-code@latest", reqName: "claude_cli"},
		{name: "auggie", harness: HarnessAuggie, command: "auggie", pkg: "@augmentcode/auggie@latest", reqName: "auggie_cli"},
		{name: "codex", harness: HarnessCodex, command: "codex", pkg: "@openai/codex@latest", reqName: "codex_cli"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rt, err := Resolve(strings.ToUpper(tc.harness), "")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if rt.Harness != tc.harness {
				t.Fatalf("Harness = %q, want %q", rt.Harness, tc.harness)
			}
			if rt.Command != tc.command {
				t.Fatalf("Command = %q, want %q", rt.Command, tc.command)
			}
			if rt.NPMPackage != tc.pkg {
				t.Fatalf("NPMPackage = %q, want %q", rt.NPMPackage, tc.pkg)
			}
			if got := rt.RequirementName(); got != tc.reqName {
				t.Fatalf("RequirementName() = %q, want %q", got, tc.reqName)
			}
		})
	}
}

func TestResolveSupportsCommandOverride(t *testing.T) {
	t.Parallel()

	rt, err := Resolve(HarnessClaude, "claude-code")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if rt.Command != "claude-code" {
		t.Fatalf("Command = %q, want %q", rt.Command, "claude-code")
	}
}

func TestResolveRejectsUnknownHarness(t *testing.T) {
	t.Parallel()

	_, err := Resolve("unknown", "")
	if err == nil {
		t.Fatal("Resolve() error = nil, want unsupported harness error")
	}
	for _, supported := range []string{HarnessAuggie, HarnessClaude, HarnessCodex} {
		if !strings.Contains(err.Error(), supported) {
			t.Fatalf("Resolve() error = %v, want supported harness %q listed", err, supported)
		}
	}
}

func TestBuildCommandCodex(t *testing.T) {
	t.Parallel()

	rt, err := Resolve(HarnessCodex, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	cmd, err := rt.BuildCommand("/tmp/work", "ship it", RunOptions{
		SkipGitRepoCheck: true,
		ImagePaths:       []string{"a.png", "  ", "b.png"},
	})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	if cmd.Name != "codex" {
		t.Fatalf("Name = %q, want codex", cmd.Name)
	}
	if cmd.Dir != "/tmp/work" {
		t.Fatalf("Dir = %q", cmd.Dir)
	}
	if got, want := cmd.Stdin, "ship it"; got != want {
		t.Fatalf("Stdin = %q, want %q", got, want)
	}
	wantArgs := []string{"exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "--image", "a.png", "--image", "b.png"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", cmd.Args, wantArgs)
	}
}

func TestBuildCommandClaude(t *testing.T) {
	t.Parallel()

	rt, err := Resolve(HarnessClaude, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	cmd, err := rt.BuildCommand("/tmp/repo", "fix bug", RunOptions{})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	wantArgs := []string{"--print", "--output-format", "text", "--dangerously-skip-permissions", "fix bug"}
	if cmd.Name != "claude" || cmd.Dir != "/tmp/repo" || !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected claude command: %+v", cmd)
	}
	if cmd.Stdin != "" {
		t.Fatalf("Stdin = %q, want empty", cmd.Stdin)
	}
}

func TestBuildCommandAuggie(t *testing.T) {
	t.Parallel()

	rt, err := Resolve(HarnessAuggie, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	cmd, err := rt.BuildCommand("/tmp/repo", "fix bug", RunOptions{})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	wantArgs := []string{"--print", "--quiet", "fix bug"}
	if cmd.Name != "auggie" || cmd.Dir != "/tmp/repo" || !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected auggie command: %+v", cmd)
	}
}

func TestBuildCommandRejectsImagesForNonCodex(t *testing.T) {
	t.Parallel()

	for _, harness := range []string{HarnessClaude, HarnessAuggie} {
		harness := harness
		t.Run(harness, func(t *testing.T) {
			t.Parallel()

			rt, err := Resolve(harness, "")
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			_, err = rt.BuildCommand("/tmp/repo", "fix bug", RunOptions{ImagePaths: []string{"img.png"}})
			if err == nil {
				t.Fatal("BuildCommand() error = nil, want image support error")
			}
			if !strings.Contains(err.Error(), "does not support prompt images") {
				t.Fatalf("BuildCommand() error = %v, want prompt images error", err)
			}
		})
	}
}

func TestPreflightCommandUsesResolvedCommand(t *testing.T) {
	t.Parallel()

	rt := Runtime{Harness: HarnessClaude, Command: "claude-alt"}
	cmd := rt.PreflightCommand()
	if cmd.Name != "claude-alt" || !reflect.DeepEqual(cmd.Args, []string{"--help"}) {
		t.Fatalf("PreflightCommand() = %+v", cmd)
	}
}

func TestBuildCommandRejectsUnsupportedRuntimeHarness(t *testing.T) {
	t.Parallel()

	_, err := (Runtime{Harness: "unknown", Command: "agent"}).BuildCommand("/tmp/repo", "x", RunOptions{})
	if err == nil {
		t.Fatal("BuildCommand() error = nil, want unsupported runtime harness error")
	}
	if !strings.Contains(err.Error(), "unsupported runtime harness") {
		t.Fatalf("BuildCommand() error = %v", err)
	}
}

func TestBuildCommandRejectsMissingRuntimeCommand(t *testing.T) {
	t.Parallel()

	rt, err := Resolve(HarnessCodex, "")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	rt.Command = ""

	_, err = rt.BuildCommand("/tmp/repo", "x", RunOptions{})
	if err == nil {
		t.Fatal("BuildCommand() error = nil, want runtime command error")
	}
	if !strings.Contains(err.Error(), "runtime command is required") {
		t.Fatalf("BuildCommand() error = %v", err)
	}
}
