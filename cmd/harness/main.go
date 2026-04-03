package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jef/how-i-want-to-code/internal/config"
	"github.com/jef/how-i-want-to-code/internal/execx"
	"github.com/jef/how-i-want-to-code/internal/harness"
	"github.com/jef/how-i-want-to-code/internal/hub"
	"github.com/jef/how-i-want-to-code/internal/hubui"
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
	case "hub":
		return runHub(os.Args[2:])
	default:
		printUsage()
		return harness.ExitUsage
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: harness run --config <path-to-json>")
	fmt.Fprintln(os.Stderr, "   or: harness multiplex --config <path-or-dir> [--config <path-or-dir> ...] [--parallel <n>]")
	fmt.Fprintln(os.Stderr, "   or: harness hub --init <path-to-init-json> [--parallel <n>] [--ui-listen <host:port>]")
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

	logger := newDefaultTerminalLogger()
	defer logger.Close()
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
	if prURLs := joinPRURLs(result.RepoResults); prURLs != "" {
		fmt.Printf(" pr_urls=%s", prURLs)
	}
	if changedRepos := countChangedRepos(result.RepoResults); changedRepos > 0 {
		fmt.Printf(" changed_repos=%d", changedRepos)
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

	logger := newDefaultTerminalLogger()
	defer logger.Close()
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

func runHub(args []string) int {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	initPath := fs.String("init", "", "Path to hub init JSON")
	parallel := fs.Int("parallel", 0, "Optional override for dispatcher max parallel workers")
	uiListen := fs.String("ui-listen", "127.0.0.1:7777", "Optional monitor web UI listen address (empty to disable)")

	if err := fs.Parse(args); err != nil {
		return harness.ExitUsage
	}
	if *initPath == "" {
		fmt.Fprintln(os.Stderr, "missing required --init flag")
		return harness.ExitUsage
	}

	cfg, err := hub.LoadInit(*initPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init config error: %v\n", err)
		return harness.ExitConfig
	}
	if *parallel > 0 {
		cfg.Dispatcher.MaxParallel = *parallel
	}

	logger := newDefaultTerminalLogger()
	defer logger.Close()
	runner := execx.OSRunner{}
	monitorBroker := hubui.NewBroker()
	daemonLogger := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logger.Print(line)
		monitorBroker.IngestLog(line)
	}
	localDispatchSem := make(chan struct{}, cfg.Dispatcher.MaxParallel)
	var localDispatchSeq uint64

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(*uiListen) != "" {
		uiServer := hubui.NewServer(*uiListen, monitorBroker)
		uiServer.Logf = logger.Printf
		uiServer.SubmitLocalPrompt = func(reqCtx context.Context, body []byte) (string, error) {
			runCfg, err := hub.ParseRunConfigJSON(body)
			if err != nil {
				return "", fmt.Errorf("invalid run config: %w", err)
			}

			select {
			case <-ctx.Done():
				return "", fmt.Errorf("service is shutting down")
			default:
			}

			select {
			case localDispatchSem <- struct{}{}:
			case <-reqCtx.Done():
				return "", fmt.Errorf("request canceled")
			default:
				return "", fmt.Errorf("dispatcher is busy (max_parallel=%d)", cap(localDispatchSem))
			}

			requestID := fmt.Sprintf(
				"local-%d-%06d",
				time.Now().UTC().Unix(),
				atomic.AddUint64(&localDispatchSeq, 1),
			)
			go func(requestID string, runCfg config.Config) {
				defer func() { <-localDispatchSem }()
				runLocalDispatch(ctx, runner, daemonLogger, cfg.Skill.Name, requestID, runCfg)
			}(requestID, runCfg)

			return requestID, nil
		}
		logger.Printf("hub.ui status=ready url=%s", monitorURL(*uiListen))
		go func() {
			if err := uiServer.Run(ctx); err != nil {
				logger.Printf("hub.ui status=error err=%q", err)
			}
		}()
	}

	daemon := hub.NewDaemon(runner)
	daemon.Logf = daemonLogger

	if err := daemon.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return hubExitCode(err)
	}
	return harness.ExitSuccess
}

func runLocalDispatch(
	ctx context.Context,
	runner execx.Runner,
	logf func(string, ...any),
	skill string,
	requestID string,
	runCfg config.Config,
) {
	logf(
		"dispatch status=start request_id=%s skill=%s repo=%s repos=%s",
		requestID,
		skill,
		runCfg.RepoURL,
		strings.Join(runCfg.RepoList(), ","),
	)

	h := harness.New(runner)
	h.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logf("dispatch request_id=%s %s", requestID, line)
	}

	res := h.Run(ctx, runCfg)
	if res.Err != nil {
		logf("dispatch status=error request_id=%s exit_code=%d err=%q", requestID, res.ExitCode, res.Err)
		return
	}
	if res.NoChanges {
		logf("dispatch status=no_changes request_id=%s workspace=%s branch=%s", requestID, res.WorkspaceDir, res.Branch)
		return
	}
	logf(
		"dispatch status=ok request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s changed_repos=%d",
		requestID,
		res.WorkspaceDir,
		res.Branch,
		res.PRURL,
		joinPRURLs(res.RepoResults),
		countChangedRepos(res.RepoResults),
	)
}

func monitorURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	return "http://" + addr
}

func hubExitCode(err error) int {
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.HasPrefix(text, "init config:"):
		return harness.ExitConfig
	case strings.HasPrefix(text, "hub auth:"):
		return harness.ExitAuth
	case strings.HasPrefix(text, "hub profile:"):
		return harness.ExitAuth
	case strings.HasPrefix(text, "hub websocket url:"):
		return harness.ExitConfig
	default:
		return harness.ExitPreflight
	}
}

func joinPRURLs(results []harness.RepoResult) string {
	if len(results) == 0 {
		return ""
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		if !result.Changed || strings.TrimSpace(result.PRURL) == "" {
			continue
		}
		urls = append(urls, strings.TrimSpace(result.PRURL))
	}
	return strings.Join(urls, ",")
}

func countChangedRepos(results []harness.RepoResult) int {
	count := 0
	for _, result := range results {
		if result.Changed {
			count++
		}
	}
	return count
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
