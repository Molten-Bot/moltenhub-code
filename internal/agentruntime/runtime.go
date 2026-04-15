package agentruntime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jef/moltenhub-code/internal/execx"
)

const (
	HarnessCodex  = "codex"
	HarnessClaude = "claude"
	HarnessAuggie = "auggie"
	HarnessPi     = "pi"
)

const defaultHarness = HarnessCodex

var harnessDisplayNames = map[string]string{
	HarnessAuggie: "Auggie",
	HarnessClaude: "Claude",
	HarnessCodex:  "Codex",
	HarnessPi:     "Pi",
}

// RunOptions controls provider-specific execution behavior.
type RunOptions struct {
	SkipGitRepoCheck bool
	ImagePaths       []string
}

// Runtime describes one executable LLM harness runtime.
type Runtime struct {
	Harness    string
	Command    string
	NPMPackage string
}

type definition struct {
	defaultCommand string
	defaultPackage string
	build          func(targetDir, prompt string, opts RunOptions) (execx.Command, error)
}

var definitions = map[string]definition{
	HarnessCodex: {
		defaultCommand: HarnessCodex,
		defaultPackage: "@openai/codex@latest",
		build:          buildCodexCommand,
	},
	HarnessClaude: {
		defaultCommand: HarnessClaude,
		defaultPackage: "@anthropic-ai/claude-code@latest",
		build:          buildClaudeCommand,
	},
	HarnessAuggie: {
		defaultCommand: HarnessAuggie,
		defaultPackage: "@augmentcode/auggie@latest",
		build:          buildAuggieCommand,
	},
	HarnessPi: {
		defaultCommand: HarnessPi,
		defaultPackage: "@mariozechner/pi-coding-agent@latest",
		build:          buildPiCommand,
	},
}

// Resolve validates harness selection and applies defaults.
func Resolve(harness, commandOverride string) (Runtime, error) {
	normalized := normalizeHarness(harness)
	def, ok := definitions[normalized]
	if !ok {
		return Runtime{}, fmt.Errorf(
			"unsupported agentHarness %q; supported values: %s",
			strings.TrimSpace(harness),
			strings.Join(SupportedHarnesses(), ", "),
		)
	}

	command := strings.TrimSpace(commandOverride)
	if command == "" {
		command = def.defaultCommand
	}

	return Runtime{
		Harness:    normalized,
		Command:    command,
		NPMPackage: def.defaultPackage,
	}, nil
}

// Default returns the default runtime selection.
func Default() Runtime {
	def := definitions[defaultHarness]
	return Runtime{
		Harness:    defaultHarness,
		Command:    def.defaultCommand,
		NPMPackage: def.defaultPackage,
	}
}

// SupportedHarnesses returns supported harness names in stable order.
func SupportedHarnesses() []string {
	keys := make([]string, 0, len(definitions))
	for key := range definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// DisplayName returns the user-facing harness label.
func DisplayName(harness string) string {
	normalized := normalizeHarness(harness)
	if label, ok := harnessDisplayNames[normalized]; ok {
		return label
	}
	return harnessDisplayNames[defaultHarness]
}

// RequirementName returns the boot diagnostic requirement key for this runtime.
func (r Runtime) RequirementName() string {
	return normalizeHarness(r.Harness) + "_cli"
}

// PreflightCommand returns the command used to verify CLI availability.
func (r Runtime) PreflightCommand() execx.Command {
	if normalizeHarness(r.Harness) == HarnessPi {
		return execx.Command{Name: strings.TrimSpace(r.Command), Args: []string{"--version"}}
	}
	return execx.Command{Name: strings.TrimSpace(r.Command), Args: []string{"--help"}}
}

// BuildCommand builds an execution command for this runtime.
func (r Runtime) BuildCommand(targetDir, prompt string, opts RunOptions) (execx.Command, error) {
	def, ok := definitions[normalizeHarness(r.Harness)]
	if !ok {
		return execx.Command{}, fmt.Errorf("unsupported runtime harness %q", r.Harness)
	}

	cmd, err := def.build(targetDir, prompt, opts)
	if err != nil {
		return execx.Command{}, err
	}
	cmd.Name = strings.TrimSpace(r.Command)
	if cmd.Name == "" {
		return execx.Command{}, fmt.Errorf("runtime command is required")
	}
	return cmd, nil
}

func normalizeHarness(harness string) string {
	normalized := strings.ToLower(strings.TrimSpace(harness))
	if normalized == "" {
		return defaultHarness
	}
	return normalized
}

func buildCodexCommand(targetDir, prompt string, opts RunOptions) (execx.Command, error) {
	args := []string{"exec", "--sandbox", "workspace-write"}
	if opts.SkipGitRepoCheck {
		args = append(args, "--skip-git-repo-check")
	}
	for _, imagePath := range opts.ImagePaths {
		imagePath = strings.TrimSpace(imagePath)
		if imagePath == "" {
			continue
		}
		args = append(args, "--image", imagePath)
	}

	return execx.Command{
		Dir:   targetDir,
		Args:  args,
		Stdin: prompt,
	}, nil
}

func buildClaudeCommand(targetDir, prompt string, opts RunOptions) (execx.Command, error) {
	if imageCount := countNonEmptyStrings(opts.ImagePaths); imageCount > 0 {
		return execx.Command{}, fmt.Errorf("agent harness %q does not support prompt images", HarnessClaude)
	}

	args := []string{
		"--print",
		"--output-format", "text",
		"--dangerously-skip-permissions",
		prompt,
	}
	return execx.Command{Dir: targetDir, Args: args}, nil
}

func buildAuggieCommand(targetDir, prompt string, opts RunOptions) (execx.Command, error) {
	if imageCount := countNonEmptyStrings(opts.ImagePaths); imageCount > 0 {
		return execx.Command{}, fmt.Errorf("agent harness %q does not support prompt images", HarnessAuggie)
	}

	args := []string{"--print", "--quiet", prompt}
	return execx.Command{Dir: targetDir, Args: args}, nil
}

func buildPiCommand(targetDir, prompt string, opts RunOptions) (execx.Command, error) {
	if imageCount := countNonEmptyStrings(opts.ImagePaths); imageCount > 0 {
		return execx.Command{}, fmt.Errorf("agent harness %q does not support prompt images", HarnessPi)
	}

	args := []string{"--print", "--mode", "text", "--no-session", prompt}
	return execx.Command{Dir: targetDir, Args: args}, nil
}

// SupportsPromptImages reports whether this runtime accepts image attachments.
func (r Runtime) SupportsPromptImages() bool {
	return normalizeHarness(r.Harness) == HarnessCodex
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}
