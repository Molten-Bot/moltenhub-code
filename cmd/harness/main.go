package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/harness"
	"github.com/jef/how-i-want-to-code/internal/multiplex"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		printUsage()
		return harness.ExitUsage
	}

	switch os.Args[1] {
	case "run":
		return runSingle(os.Args[2:])
	case "multiplex":
		return runMultiplex(os.Args[2:])
	default:
		printUsage()
		return harness.ExitUsage
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: harness run --config <path-to-json>")
	fmt.Fprintln(os.Stderr, "   or: harness multiplex --config <path-or-dir> [--config <path-or-dir> ...] [--parallel <n>]")
}

func runSingle(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to run configuration JSON")
	if err := fs.Parse(args); err != nil {
		return harness.ExitUsage
	}
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "missing required --config flag")
		return harness.ExitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return harness.ExitConfig
	}

	logger := log.New(os.Stderr, "", 0)
	h := harness.New(execx.OSRunner{})
	h.Logf = logger.Printf

	result := h.Run(context.Background(), cfg)
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", result.Err)
		if result.WorkspaceDir != "" {
			fmt.Fprintf(os.Stderr, "workspace: %s\n", result.WorkspaceDir)
		}
		return result.ExitCode
	}

	if result.NoChanges {
		fmt.Printf("status=no_changes workspace=%s branch=%s\n", result.WorkspaceDir, result.Branch)
		return harness.ExitSuccess
	}
	fmt.Printf("status=ok workspace=%s branch=%s", result.WorkspaceDir, result.Branch)
	if result.PRURL != "" {
		fmt.Printf(" pr_url=%s", result.PRURL)
	}
	fmt.Println()

	return harness.ExitSuccess
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func runMultiplex(args []string) int {
	fs := flag.NewFlagSet("multiplex", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var configInputs stringListFlag
	fs.Var(&configInputs, "config", "Path to task config JSON file or directory (repeatable)")
	parallel := fs.Int("parallel", 2, "Maximum number of parallel sessions")

	if err := fs.Parse(args); err != nil {
		return harness.ExitUsage
	}
	configInputs = append(configInputs, fs.Args()...)
	if len(configInputs) == 0 {
		fmt.Fprintln(os.Stderr, "missing required --config flag")
		return harness.ExitUsage
	}

	configPaths, err := collectConfigPaths(configInputs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config discovery error: %v\n", err)
		return harness.ExitConfig
	}
	if len(configPaths) == 0 {
		fmt.Fprintln(os.Stderr, "no task config files found")
		return harness.ExitConfig
	}

	logger := log.New(os.Stderr, "", 0)
	mx := multiplex.New(execx.OSRunner{})
	mx.MaxParallel = *parallel
	mx.Logf = logger.Printf

	result := mx.Run(context.Background(), configPaths)
	for _, s := range result.Sessions {
		fmt.Printf("session=%s status=%s config=%s stage=%s", s.ID, s.State, s.ConfigPath, s.Stage)
		if s.ExitCode != harness.ExitSuccess {
			fmt.Printf(" exit_code=%d", s.ExitCode)
		}
		if s.WorkspaceDir != "" {
			fmt.Printf(" workspace=%s", s.WorkspaceDir)
		}
		if s.Branch != "" {
			fmt.Printf(" branch=%s", s.Branch)
		}
		if s.PRURL != "" {
			fmt.Printf(" pr_url=%s", s.PRURL)
		}
		if s.Error != "" {
			fmt.Printf(" err=%q", s.Error)
		}
		fmt.Println()
	}

	return result.ExitCode()
}

func collectConfigPaths(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	seen := map[string]struct{}{}
	var paths []string
	addPath := func(path string) {
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	for _, input := range inputs {
		in := strings.TrimSpace(input)
		if in == "" {
			continue
		}
		abs, err := filepath.Abs(in)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", in, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", abs, err)
		}

		if !st.IsDir() {
			addPath(abs)
			continue
		}

		var discovered []string
		err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".json") {
				discovered = append(discovered, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %q: %w", abs, err)
		}

		sort.Strings(discovered)
		for _, p := range discovered {
			addPath(p)
		}
	}

	return paths, nil
}
