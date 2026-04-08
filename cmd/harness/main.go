package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jef/moltenhub-code/internal/agentruntime"
	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/failurefollowup"
	"github.com/jef/moltenhub-code/internal/harness"
	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
	"github.com/jef/moltenhub-code/internal/multiplex"
	"github.com/jef/moltenhub-code/internal/workspace"
)

const failureFollowUpRequiredPrompt = failurefollowup.RequiredPrompt

const hubBootRecommendation = "Recommended: connect this runtime to Molten Hub at https://molten.bot/hub so agents can dispatch work to it."
const hubPingLocalOnlyDetail = "Hub endpoint ping precheck failed; continuing in local-only mode. Use the local UI/API to submit tasks."
const hubPingRemoteContinueDetail = "Hub endpoint ping precheck failed; continuing remote startup because Hub credentials are configured and UI is disabled."
const hubPingHeadlessNoopDetail = "Hub endpoint ping precheck failed with UI disabled and no Hub credentials configured; startup completed without remote transport."

const hubBootDiagnosticTimeout = 10 * time.Second
const hubPingDiagnosticTimeout = 5 * time.Second

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
		if err := workspace.PrepareDefaultRoots(); err != nil {
			fmt.Fprintf(os.Stderr, "workspace init error: %v\n", err)
			return harness.ExitWorkspace
		}
		return runSingle(os.Args[2:])
	case "multiplex":
		if err := workspace.PrepareDefaultRoots(); err != nil {
			fmt.Fprintf(os.Stderr, "workspace init error: %v\n", err)
			return harness.ExitWorkspace
		}
		return runMultiplex(os.Args[2:])
	case "hub":
		if err := workspace.PrepareDefaultRoots(); err != nil {
			fmt.Fprintf(os.Stderr, "workspace init error: %v\n", err)
			return harness.ExitWorkspace
		}
		return runHub(os.Args[2:])
	default:
		printUsage()
		return harness.ExitUsage
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: harness run --config <path-to-json>")
	fmt.Fprintln(os.Stderr, "   or: harness multiplex --config <path-or-dir> [--config <path-or-dir> ...] [--parallel <n>]")
	fmt.Fprintln(os.Stderr, "   or: harness hub [--init <path-to-init-json> | --config <path-to-config-json>] [--parallel <n>] [--ui-listen <host:port>] [--ui-automatic]")
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
		writeStderrLine(logger, fmt.Sprintf("error: %v", result.Err))
		if result.WorkspaceDir != "" {
			writeStderrLine(logger, fmt.Sprintf("workspace: %s", result.WorkspaceDir))
		}
		return result.ExitCode
	}

	if result.NoChanges {
		writeStdoutLine(logger, fmt.Sprintf("status=no_changes workspace=%s branch=%s", result.WorkspaceDir, result.Branch))
		return harness.ExitSuccess
	}
	var line strings.Builder
	line.WriteString(fmt.Sprintf("status=ok workspace=%s branch=%s", result.WorkspaceDir, result.Branch))
	if result.PRURL != "" {
		line.WriteString(fmt.Sprintf(" pr_url=%s", result.PRURL))
	}
	if prURLs := joinPRURLs(result.RepoResults); prURLs != "" {
		line.WriteString(fmt.Sprintf(" pr_urls=%s", prURLs))
	}
	if changedRepos := countChangedRepos(result.RepoResults); changedRepos > 0 {
		line.WriteString(fmt.Sprintf(" changed_repos=%d", changedRepos))
	}
	writeStdoutLine(logger, line.String())

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
		var line strings.Builder
		line.WriteString(fmt.Sprintf("session=%s status=%s config=%s stage=%s", s.ID, s.State, s.ConfigPath, s.Stage))
		if s.ExitCode != harness.ExitSuccess {
			line.WriteString(fmt.Sprintf(" exit_code=%d", s.ExitCode))
		}
		if s.WorkspaceDir != "" {
			line.WriteString(fmt.Sprintf(" workspace=%s", s.WorkspaceDir))
		}
		if s.Branch != "" {
			line.WriteString(fmt.Sprintf(" branch=%s", s.Branch))
		}
		if s.PRURL != "" {
			line.WriteString(fmt.Sprintf(" pr_url=%s", s.PRURL))
		}
		if s.Error != "" {
			line.WriteString(fmt.Sprintf(" err=%q", s.Error))
		}
		writeStdoutLine(logger, line.String())
	}

	return result.ExitCode()
}

func runHub(args []string) int {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	initPath := fs.String("init", "", "Path to hub init JSON")
	configPath := fs.String("config", "", "Path to hub runtime config JSON")
	parallel := fs.Int("parallel", 0, "Optional override for dispatcher max parallel workers")
	uiListen := fs.String("ui-listen", "127.0.0.1:7777", "Optional monitor web UI listen address (empty to disable)")
	uiAutomatic := fs.Bool("ui-automatic", false, "Hide the browser Studio form and run the monitor UI in automatic mode")

	if err := fs.Parse(args); err != nil {
		return harness.ExitUsage
	}
	cfg, exitCode, err := loadHubBootConfig(*initPath, *configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCode
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

	runtimeCfg, runtimeErr := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand)
	var authGate agentAuthGate

	localSubmitDeduper := newLocalSubmissionDeduper(localSubmissionDedupTTL)
	localTaskController := newLocalTaskController()
	var localDispatchSeq uint64

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if runtimeErr == nil {
		authGate = newAgentAuthGate(ctx, runner, runtimeCfg, cfg, daemonLogger)
	}

	runtimeCfgLoader := func() (hub.RuntimeConfig, error) {
		return hub.LoadRuntimeConfig(cfg.RuntimeConfigPath)
	}
	hubConfigured := hubCredentialsConfigured(cfg, runtimeCfgLoader)
	bootDiag := runHubBootDiagnosticsWithRuntimeLoaderDetailed(ctx, runner, daemonLogger, cfg, runtimeCfgLoader)
	forceLocalOnlyMode := shouldRunHubInLocalOnlyMode(bootDiag.PingChecked, bootDiag.PingOK, *uiListen, hubConfigured)
	if bootDiag.PingChecked && !bootDiag.PingOK {
		if forceLocalOnlyMode {
			daemonLogger("hub.auth status=local_only detail=%q", hubPingFailureDetail(hubPingLocalOnlyDetail, bootDiag.PingErr))
		} else {
			daemonLogger("boot.diagnosis status=warn requirement=moltenhub_ping detail=%q", hubPingFailureDetail(hubPingRemoteContinueDetail, bootDiag.PingErr))
		}
	}
	maybeStartAgentAuth(ctx, runtimeCfg, authGate, daemonLogger)

	dispatchController := hub.NewAdaptiveDispatchController(cfg.Dispatcher, daemonLogger)
	dispatchController.Start(ctx)

	logRoot := ""
	if mirror, ok := logger.sink.(*taskLogMirror); ok {
		logRoot = strings.TrimSpace(mirror.rootDir)
	}

	var queueFailureFollowUp func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config)
	var enqueueLocalRun func(reqCtx context.Context, runCfg config.Config, allowFailureFollowUp bool, source string, force bool) (string, error)
	enqueueLocalRun = func(reqCtx context.Context, runCfg config.Config, allowFailureFollowUp bool, source string, force bool) (string, error) {
		if authGate != nil {
			authState, authErr := authGate.Status(reqCtx)
			if authErr != nil {
				return "", fmt.Errorf("check agent auth status: %w", authErr)
			}
			if authState.Required && !authState.Ready {
				return "", fmt.Errorf(
					"agent auth required: %s",
					firstNonEmptyString(authState.Message, "complete agent authorization in the UI"),
				)
			}
		}

		runCfg = applyDefaultAgentRuntimeConfig(runCfg, cfg)
		source = strings.TrimSpace(source)
		if source == "" {
			source = "local_submit"
		}

		dedupeKey := dedupeKeyForRunConfig(runCfg)
		if force {
			dedupeKey = ""
		}
		allowCompletedDuplicate := source == "rerun"
		if dedupeKey != "" {
			if duplicate, state, duplicateOf := localSubmitDeduper.Check(dedupeKey, allowCompletedDuplicate); duplicate {
				daemonLogger(
					"dispatch status=duplicate source=%s state=%s duplicate_of=%s",
					source,
					state,
					duplicateOf,
				)
				return "", newDuplicateSubmissionError(duplicateOf, state)
			}
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("service is shutting down")
		default:
		}
		select {
		case <-reqCtx.Done():
			return "", fmt.Errorf("request canceled")
		default:
		}

		requestID := fmt.Sprintf(
			"local-%d-%06d",
			time.Now().UTC().Unix(),
			atomic.AddUint64(&localDispatchSeq, 1),
		)
		if dedupeKey != "" {
			if accepted, state, duplicateOf := localSubmitDeduper.Begin(dedupeKey, requestID, allowCompletedDuplicate); !accepted {
				daemonLogger(
					"dispatch status=duplicate source=%s state=%s duplicate_of=%s",
					source,
					state,
					duplicateOf,
				)
				return "", newDuplicateSubmissionError(duplicateOf, state)
			}
		}
		if runConfigJSON, ok := marshalRunConfigJSON(runCfg); ok {
			monitorBroker.RecordTaskRunConfig(requestID, runConfigJSON)
		}

		runCtx, cancelRun := context.WithCancelCause(ctx)
		taskHandle := localTaskController.Register(requestID, cancelRun)

		go func(
			requestID string,
			runCfg config.Config,
			dedupeKey string,
			allowFailureFollowUp bool,
			runCtx context.Context,
			cancelRun context.CancelCauseFunc,
			taskHandle *localTaskHandle,
		) {
			finalState := ""
			if dedupeKey != "" {
				defer func() {
					localSubmitDeduper.Done(dedupeKey, requestID, finalState)
				}()
			}
			defer cancelRun(nil)
			defer localTaskController.Complete(requestID)

			for {
				if taskHandle != nil {
					if waitErr := taskHandle.WaitUntilRunnable(runCtx); waitErr != nil {
						if errors.Is(waitErr, errTaskStoppedByOperator) {
							finalState = "stopped"
							daemonLogger("dispatch status=stopped request_id=%s err=%q", requestID, waitErr)
							return
						}
						finalState = "error"
						daemonLogger("dispatch status=error request_id=%s err=%q", requestID, waitErr)
						if !errors.Is(waitErr, context.Canceled) && allowFailureFollowUp && queueFailureFollowUp != nil {
							queueFailureFollowUp(requestID, harness.Result{
								ExitCode: harness.ExitPreflight,
								Err:      fmt.Errorf("dispatch wait: %w", waitErr),
							}, runCfg)
						}
						return
					}
				}

				acquireCtx, cancelAcquire := context.WithCancel(runCtx)
				if taskHandle != nil {
					taskHandle.SetAcquireCancel(cancelAcquire)
				}
				release, acquireErr := dispatchController.Acquire(acquireCtx, requestID)
				if taskHandle != nil {
					taskHandle.ClearAcquireCancel(cancelAcquire)
				}
				cancelAcquire()

				if acquireErr != nil {
					if taskHandle != nil && taskHandle.IsStopped() {
						finalState = "stopped"
						daemonLogger("dispatch status=stopped request_id=%s err=%q", requestID, errTaskStoppedByOperator)
						return
					}
					if taskHandle != nil && taskHandle.IsPaused() && errors.Is(acquireErr, context.Canceled) && runCtx.Err() == nil {
						continue
					}
					finalState = "error"
					daemonLogger("dispatch status=error request_id=%s err=%q", requestID, acquireErr)
					if !errors.Is(acquireErr, context.Canceled) && allowFailureFollowUp && queueFailureFollowUp != nil {
						queueFailureFollowUp(requestID, harness.Result{
							ExitCode: harness.ExitPreflight,
							Err:      fmt.Errorf("dispatch acquire: %w", acquireErr),
						}, runCfg)
					}
					return
				}

				if taskHandle != nil {
					taskHandle.SetRunning(true)
				}
				outcome := runLocalDispatch(runCtx, runner, daemonLogger, cfg.Skill.Name, requestID, runCfg, func() bool {
					if taskHandle == nil {
						return false
					}
					return taskHandle.IsStopped()
				})
				if taskHandle != nil {
					taskHandle.SetRunning(false)
				}
				release()

				finalState = outcome.State
				if !allowFailureFollowUp || outcome.State != "error" {
					return
				}
				if queueFailureFollowUp != nil {
					queueFailureFollowUp(requestID, outcome.Result, runCfg)
				}
				return
			}
		}(requestID, runCfg, dedupeKey, allowFailureFollowUp, runCtx, cancelRun, taskHandle)

		return requestID, nil
	}
	queueFailureFollowUp = func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config) {
		if ok, reason := shouldQueueFailureFollowUp(failedResult); !ok {
			daemonLogger(
				"dispatch status=warn action=skip_failure_followup request_id=%s err=%q",
				failedRequestID,
				fmt.Sprintf("non-remediable failure detected: %s", reason),
			)
			return
		}

		followUpCfg := failureFollowUpRunConfig(failedRequestID, failedResult, failedRunCfg, logRoot)
		if len(followUpCfg.RepoList()) == 0 {
			daemonLogger(
				"dispatch status=warn action=queue_failure_followup request_id=%s err=%q",
				failedRequestID,
				"no failed-task repo found for follow-up",
			)
			return
		}
		followUpRequestID, followUpErr := enqueueLocalRun(ctx, followUpCfg, false, "failure_followup", false)
		if followUpErr != nil {
			daemonLogger(
				"dispatch status=warn action=queue_failure_followup request_id=%s err=%q",
				failedRequestID,
				followUpErr,
			)
			return
		}
		daemonLogger(
			"dispatch status=ok action=queue_failure_followup request_id=%s follow_up_request_id=%s",
			failedRequestID,
			followUpRequestID,
		)
	}

	if strings.TrimSpace(*uiListen) != "" {
		uiServer := hubui.NewServer(*uiListen, monitorBroker)
		uiServer.AutomaticMode = *uiAutomatic
		uiServer.ConfiguredHarness = cfg.AgentHarness
		uiServer.Logf = logger.Printf
		if authGate != nil {
			uiServer.AgentAuthStatus = authGate.Status
			uiServer.StartAgentAuth = authGate.StartDeviceAuth
			uiServer.VerifyAgentAuth = authGate.Verify
			if shouldEnableAgentAuthConfigure(runtimeCfg.Harness) {
				uiServer.ConfigureAgentAuth = authGate.Configure
			}
		}
		uiServer.SubmitLocalPrompt = func(reqCtx context.Context, body []byte) (string, error) {
			runCfg, err := hub.ParseRunConfigJSON(body)
			if err != nil {
				return "", fmt.Errorf("invalid run config: %w", err)
			}
			return enqueueLocalRun(reqCtx, runCfg, true, "local_submit", false)
		}
		uiServer.SubmitTaskRerun = func(reqCtx context.Context, _ string, body []byte, force bool) (string, error) {
			runCfg, err := hub.ParseRunConfigJSON(body)
			if err != nil {
				return "", fmt.Errorf("invalid run config: %w", err)
			}
			return enqueueLocalRun(reqCtx, runCfg, true, "rerun", force)
		}
		uiServer.CloseTask = func(_ context.Context, requestID string) error {
			logDir, ok := localTaskLogDir(logRoot, requestID)
			if !ok {
				return nil
			}
			if err := os.RemoveAll(logDir); err != nil {
				return fmt.Errorf("remove task log dir %s: %w", logDir, err)
			}
			daemonLogger("dispatch status=ok action=task_close_cleanup request_id=%s log_dir=%s", requestID, logDir)
			return nil
		}
		uiServer.PauseTask = func(_ context.Context, requestID string) error {
			if err := localTaskController.Pause(requestID); err != nil {
				return err
			}
			daemonLogger("dispatch status=paused request_id=%s", requestID)
			return nil
		}
		uiServer.RunTask = func(_ context.Context, requestID string) error {
			if err := localTaskController.Run(requestID); err != nil {
				return err
			}
			daemonLogger("dispatch status=resumed request_id=%s", requestID)
			return nil
		}
		uiServer.StopTask = func(_ context.Context, requestID string) error {
			if err := localTaskController.Stop(requestID); err != nil {
				return err
			}
			daemonLogger("dispatch status=stopped request_id=%s err=%q", requestID, errTaskStoppedByOperator)
			return nil
		}
		logger.Printf("hub.ui status=ready url=%s", monitorURL(*uiListen))
		go func() {
			if err := uiServer.Run(ctx); err != nil {
				logger.Printf("hub.ui status=error err=%q", err)
			}
		}()
	}

	if forceLocalOnlyMode {
		if strings.TrimSpace(*uiListen) == "" {
			daemonLogger("hub.auth status=local_only detail=%q", hubPingFailureDetail(hubPingHeadlessNoopDetail, bootDiag.PingErr))
			return harness.ExitSuccess
		}
		<-ctx.Done()
		return harness.ExitSuccess
	}

	if !hubConfigured {
		daemonLogger(
			"hub.auth status=local_only detail=%q",
			"No bind_token/agent_token configured; skipping remote hub connection. Use the local UI/API to submit tasks.",
		)
		if strings.TrimSpace(*uiListen) == "" {
			writeStderrLine(
				logger,
				"error: hub auth not configured and UI disabled; set bind_token/agent_token (or persisted runtime config) or enable --ui-listen",
			)
			return harness.ExitAuth
		}
		<-ctx.Done()
		return harness.ExitSuccess
	}

	daemon := hub.NewDaemon(runner)
	daemon.Logf = daemonLogger
	daemon.DispatchController = dispatchController
	daemon.TaskLogRoot = logRoot
	daemon.OnDispatchQueued = func(requestID string, runCfg config.Config) {
		if runConfigJSON, ok := marshalRunConfigJSON(runCfg); ok {
			monitorBroker.RecordTaskRunConfig(requestID, runConfigJSON)
		}
	}
	daemon.OnDispatchFailed = func(requestID string, runCfg config.Config, result harness.Result) {
		if queueFailureFollowUp != nil {
			queueFailureFollowUp(requestID, result, runCfg)
		}
	}

	if err := daemon.Run(ctx, cfg); err != nil {
		if shouldFallbackToLocalOnlyMode(*uiListen, err) {
			daemonLogger(
				"hub.auth status=local_only detail=%q",
				"Remote hub auth failed; continuing in local-only mode. Use the local UI/API to submit tasks.",
			)
			<-ctx.Done()
			return harness.ExitSuccess
		}
		writeStderrLine(logger, fmt.Sprintf("error: %v", err))
		return hubExitCode(err)
	}
	return harness.ExitSuccess
}

func loadHubBootConfig(initPath, configPath string) (hub.InitConfig, int, error) {
	initPath = strings.TrimSpace(initPath)
	configPath = strings.TrimSpace(configPath)

	if initPath != "" && configPath != "" {
		return hub.InitConfig{}, harness.ExitUsage, fmt.Errorf("provide only one of --init or --config")
	}

	if configPath != "" {
		runtimeCfg, err := hub.LoadRuntimeConfig(configPath)
		if err != nil {
			return hub.InitConfig{}, harness.ExitConfig, fmt.Errorf("runtime config error: %w", err)
		}
		cfg := runtimeCfg.Init()
		cfg.RuntimeConfigPath = runtimeCfg.RuntimeConfigPath
		return cfg, harness.ExitSuccess, nil
	}

	if initPath != "" {
		cfg, err := hub.LoadInit(initPath)
		if err != nil {
			return hub.InitConfig{}, harness.ExitConfig, fmt.Errorf("init config error: %w", err)
		}
		cfg.RuntimeConfigPath = hub.ResolveRuntimeConfigPath(initPath)
		return cfg, harness.ExitSuccess, nil
	}

	runtimeCfg, err := hub.LoadRuntimeConfig("")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return hub.InitConfig{}, harness.ExitUsage, fmt.Errorf("missing required --init or --config flag")
		}
		return hub.InitConfig{}, harness.ExitConfig, fmt.Errorf("runtime config error: %w", err)
	}

	cfg := runtimeCfg.Init()
	cfg.RuntimeConfigPath = runtimeCfg.RuntimeConfigPath
	return cfg, harness.ExitSuccess, nil
}

func writeStdoutLine(logger *terminalLogger, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	fmt.Fprintln(os.Stdout, line)
	if logger != nil {
		logger.Capture(line)
	}
}

func writeStderrLine(logger *terminalLogger, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	fmt.Fprintln(os.Stderr, line)
	if logger != nil {
		logger.Capture(line)
	}
}

type localDispatchOutcome struct {
	State  string
	Result harness.Result
}

func runLocalDispatch(
	ctx context.Context,
	runner execx.Runner,
	logf func(string, ...any),
	skill string,
	requestID string,
	runCfg config.Config,
	wasStopped func() bool,
) localDispatchOutcome {
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
		if wasStopped != nil && wasStopped() {
			stopErr := context.Cause(ctx)
			if stopErr == nil {
				stopErr = errTaskStoppedByOperator
			}
			logf(
				"dispatch status=stopped request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
				requestID,
				res.ExitCode,
				res.WorkspaceDir,
				res.Branch,
				res.PRURL,
				stopErr,
			)
			return localDispatchOutcome{State: "stopped", Result: res}
		}
		logf(
			"dispatch status=error request_id=%s exit_code=%d workspace=%s branch=%s pr_url=%s err=%q",
			requestID,
			res.ExitCode,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			res.Err,
		)
		return localDispatchOutcome{State: "error", Result: res}
	}
	if res.NoChanges {
		logf("dispatch status=no_changes request_id=%s workspace=%s branch=%s", requestID, res.WorkspaceDir, res.Branch)
		return localDispatchOutcome{State: "no_changes", Result: res}
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
	return localDispatchOutcome{State: "ok", Result: res}
}

func failureFollowUpRunConfig(
	failedRequestID string,
	failedResult harness.Result,
	failedRunCfg config.Config,
	logRoot string,
) config.Config {
	logPaths := failurefollowup.TaskLogPaths(logRoot, failedRequestID)
	return config.Config{
		Repos:        failureFollowUpRepos(failedResult, failedRunCfg),
		BaseBranch:   "main",
		TargetSubdir: ".",
		Prompt:       failureFollowUpPrompt(logPaths, failedResult, failedRunCfg),
	}
}

var failureFollowUpNonRemediableMarkers = []string{
	"quota exceeded",
	"insufficient_quota",
	"billing",
	"401 unauthorized",
	"missing bearer or basic authentication",
	"invalid api key",
	"invalid_authentication",
	"authentication error",
	"no delta from",
	"no commits between",
	"head sha can't be blank",
	"base sha can't be blank",
	"head ref must be a branch",
}

func shouldQueueFailureFollowUp(failedResult harness.Result) (bool, string) {
	if failedResult.Err == nil {
		return false, "failed task did not include an error"
	}

	errText := strings.ToLower(strings.TrimSpace(failedResult.Err.Error()))
	if errText == "" {
		return false, "failed task error was empty"
	}

	for _, marker := range failureFollowUpNonRemediableMarkers {
		if strings.Contains(errText, marker) {
			return false, marker
		}
	}
	return true, ""
}

func failureFollowUpRepos(failedResult harness.Result, failedRunCfg config.Config) []string {
	if repo := singleRepoFromResults(failedResult.RepoResults); repo != "" {
		return []string{repo}
	}
	for _, repo := range failedRunCfg.RepoList() {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		return []string{repo}
	}
	for _, repoResult := range failedResult.RepoResults {
		repo := strings.TrimSpace(repoResult.RepoURL)
		if repo == "" {
			continue
		}
		return []string{repo}
	}
	return nil
}

func singleRepoFromResults(results []harness.RepoResult) string {
	var repo string
	for _, result := range results {
		candidate := strings.TrimSpace(result.RepoURL)
		if candidate == "" {
			continue
		}
		if repo == "" {
			repo = candidate
			continue
		}
		if repo != candidate {
			return ""
		}
	}
	return repo
}

func failureFollowUpPrompt(logPaths []string, failedResult harness.Result, failedRunCfg config.Config) string {
	return failurefollowup.ComposePrompt(
		failureFollowUpRequiredPrompt,
		logPaths,
		[]string{
			".log/local/<request timestamp>/<request sequence>",
			".log/local/<request timestamp>/<request sequence>/term",
			".log/local/<request timestamp>/<request sequence>/terminal.log",
		},
		"",
		failureFollowUpFailureContext(failedResult, failedRunCfg),
	)
}

func failureFollowUpFailureContext(failedResult harness.Result, failedRunCfg config.Config) string {
	lines := []string{
		"Observed failure context:",
		fmt.Sprintf("- exit_code=%d", failedResult.ExitCode),
	}
	if failedResult.Err != nil {
		lines = append(lines, fmt.Sprintf("- error=%q", failedResult.Err.Error()))
	}
	if workspaceDir := strings.TrimSpace(failedResult.WorkspaceDir); workspaceDir != "" {
		lines = append(lines, fmt.Sprintf("- workspace_dir=%s", workspaceDir))
	}
	if branch := strings.TrimSpace(failedResult.Branch); branch != "" {
		lines = append(lines, fmt.Sprintf("- branch=%s", branch))
	}
	if prURL := strings.TrimSpace(failedResult.PRURL); prURL != "" {
		lines = append(lines, fmt.Sprintf("- pr_url=%s", prURL))
	}
	if repos := failureFollowUpContextRepos(failedResult, failedRunCfg); len(repos) > 0 {
		lines = append(lines, fmt.Sprintf("- repos=%s", strings.Join(repos, ",")))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func failureFollowUpContextRepos(failedResult harness.Result, failedRunCfg config.Config) []string {
	var repos []string
	seen := make(map[string]struct{})
	appendRepo := func(repo string) {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			return
		}
		if _, ok := seen[repo]; ok {
			return
		}
		seen[repo] = struct{}{}
		repos = append(repos, repo)
	}
	for _, repo := range failedRunCfg.RepoList() {
		appendRepo(repo)
	}
	for _, repoResult := range failedResult.RepoResults {
		appendRepo(repoResult.RepoURL)
	}
	return repos
}

func taskLogPaths(logRoot, requestID string) []string {
	return failurefollowup.TaskLogPaths(logRoot, requestID)
}

func taskLogDir(logRoot, requestID string) (string, bool) {
	return failurefollowup.TaskLogDir(logRoot, requestID)
}

func localTaskLogDir(logRoot, requestID string) (string, bool) {
	subdir, ok := identifierSubdir(requestID)
	if !ok {
		return "", false
	}
	subdir = filepath.Clean(subdir)
	localPrefix := "local" + string(filepath.Separator)
	if subdir != "local" && !strings.HasPrefix(subdir, localPrefix) {
		return "", false
	}

	return taskLogDir(logRoot, requestID)
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

func shouldFallbackToLocalOnlyMode(uiListen string, err error) bool {
	if strings.TrimSpace(uiListen) == "" || err == nil {
		return false
	}
	return hubExitCode(err) == harness.ExitAuth
}

func shouldRunHubInLocalOnlyMode(pingPrecheckChecked bool, pingPrecheckOK bool, uiListen string, hubConfigured bool) bool {
	if !pingPrecheckChecked || pingPrecheckOK {
		return false
	}
	if strings.TrimSpace(uiListen) != "" {
		return true
	}
	return !hubConfigured
}

func hubPingFailureDetail(base string, pingErr error) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "Hub endpoint ping precheck failed."
	}
	if pingErr == nil {
		return base
	}
	return fmt.Sprintf("%s Error: %v", base, pingErr)
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

func marshalRunConfigJSON(cfg config.Config) ([]byte, bool) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, false
	}
	return payload, true
}

type runtimeConfigLoader func() (hub.RuntimeConfig, error)

type hubBootDiagnosticsResult struct {
	PingChecked bool
	PingOK      bool
	PingErr     error
}

func runHubBootDiagnostics(ctx context.Context, runner execx.Runner, logf func(string, ...any), cfg hub.InitConfig) bool {
	result := runHubBootDiagnosticsWithRuntimeLoaderDetailed(ctx, runner, logf, cfg, func() (hub.RuntimeConfig, error) {
		return hub.LoadRuntimeConfig(cfg.RuntimeConfigPath)
	})
	return result.PingChecked && result.PingOK
}

func runHubBootDiagnosticsWithRuntimeLoader(
	ctx context.Context,
	runner execx.Runner,
	logf func(string, ...any),
	cfg hub.InitConfig,
	loadRuntimeConfig runtimeConfigLoader,
) bool {
	result := runHubBootDiagnosticsWithRuntimeLoaderDetailed(ctx, runner, logf, cfg, loadRuntimeConfig)
	return result.PingChecked && result.PingOK
}

func runHubBootDiagnosticsWithRuntimeLoaderDetailed(
	ctx context.Context,
	runner execx.Runner,
	logf func(string, ...any),
	cfg hub.InitConfig,
	loadRuntimeConfig runtimeConfigLoader,
) hubBootDiagnosticsResult {
	if runner == nil || logf == nil {
		return hubBootDiagnosticsResult{}
	}
	runtime, err := agentruntime.Resolve(cfg.AgentHarness, cfg.AgentCommand)
	if err != nil {
		logf("boot.diagnosis status=error requirement=agent_runtime err=%q", err)
		return hubBootDiagnosticsResult{}
	}

	checks := []struct {
		requirement string
		cmd         execx.Command
	}{
		{
			requirement: "git_cli",
			cmd:         execx.Command{Name: "git", Args: []string{"--version"}},
		},
		{
			requirement: "gh_cli",
			cmd:         execx.Command{Name: "gh", Args: []string{"--version"}},
		},
		{
			requirement: runtime.RequirementName(),
			cmd:         runtime.PreflightCommand(),
		},
	}
	checkNames := []string{"git_cli", "gh_cli", runtime.RequirementName(), "gh_auth", "moltenhub_ping", "moltenhub_hub"}
	logf("boot.diagnosis status=start checks=%s", strings.Join(checkNames, ","))

	failedRequiredChecks := 0
	pingChecked := false
	pingOK := true
	var pingFailureErr error
	for _, check := range checks {
		checkCtx, cancel := context.WithTimeout(ctx, hubBootDiagnosticTimeout)
		res, err := runner.Run(checkCtx, check.cmd)
		cancel()
		if err != nil {
			failedRequiredChecks++
			logf("boot.diagnosis status=error requirement=%s err=%q", check.requirement, err)
			continue
		}
		logf(
			"boot.diagnosis status=ok requirement=%s detail=%q",
			check.requirement,
			diagnosticDetailForResult(res),
		)
	}

	authCtx, cancel := context.WithTimeout(ctx, hubBootDiagnosticTimeout)
	authRes, authErr := runner.Run(authCtx, execx.Command{Name: "gh", Args: []string{"auth", "status"}})
	cancel()
	if authErr != nil {
		logf(
			"boot.diagnosis status=warn requirement=gh_auth detail=%q recommendation=%q",
			diagnosticDetailForResult(authRes),
			"Run `gh auth login` (or set GH_TOKEN) before dispatching tasks.",
		)
	} else {
		logf("boot.diagnosis status=ok requirement=gh_auth detail=%q", diagnosticDetailForResult(authRes))
	}

	pingURL, pingURLErr := hubPingURL(cfg.BaseURL)
	if pingURLErr != nil {
		pingChecked = true
		pingOK = false
		pingFailureErr = pingURLErr
		logf("boot.diagnosis status=error requirement=moltenhub_ping err=%q", pingURLErr)
	} else {
		pingChecked = true
		pingCtx, pingCancel := context.WithTimeout(ctx, hubPingDiagnosticTimeout)
		pingDetail, pingErr := checkHubPing(pingCtx, pingURL)
		pingCancel()
		if pingErr != nil {
			pingOK = false
			pingFailureErr = pingErr
			logf("boot.diagnosis status=error requirement=moltenhub_ping err=%q", pingErr)
		} else {
			logf("boot.diagnosis status=ok requirement=moltenhub_ping detail=%q", pingDetail)
		}
	}

	if hubCredentialsConfigured(cfg, loadRuntimeConfig) {
		logf(
			"boot.diagnosis status=ok requirement=moltenhub_hub detail=%q",
			fmt.Sprintf("Hub endpoint configured: %s", strings.TrimSpace(cfg.BaseURL)),
		)
	} else {
		logf(
			"boot.diagnosis status=recommendation requirement=moltenhub_hub detail=%q",
			hubBootRecommendation,
		)
	}

	result := hubBootDiagnosticsResult{
		PingChecked: pingChecked,
		PingOK:      pingOK,
		PingErr:     pingFailureErr,
	}
	if failedRequiredChecks == 0 {
		logf("boot.diagnosis status=complete required_checks=ok")
		return result
	}
	logf(
		"boot.diagnosis status=warn required_checks=failed count=%d recommendation=%q",
		failedRequiredChecks,
		fmt.Sprintf(
			"Install missing tools before running tasks: git, gh, %s.",
			strings.TrimSpace(runtime.Command),
		),
	)
	return result
}

func applyDefaultAgentRuntimeConfig(runCfg config.Config, initCfg hub.InitConfig) config.Config {
	if strings.TrimSpace(runCfg.AgentHarness) == "" {
		runCfg.AgentHarness = strings.TrimSpace(initCfg.AgentHarness)
	}
	if strings.TrimSpace(runCfg.AgentCommand) == "" {
		runCfg.AgentCommand = strings.TrimSpace(initCfg.AgentCommand)
	}
	return runCfg
}

func maybeStartAgentAuth(ctx context.Context, runtime agentruntime.Runtime, gate agentAuthGate, logf func(string, ...any)) {
	if gate == nil || logf == nil {
		return
	}
	if strings.TrimSpace(runtime.Harness) != agentruntime.HarnessClaude {
		return
	}

	status, err := gate.Status(ctx)
	if err != nil {
		logf("hub.auth status=warn harness=%s action=check err=%q", runtime.Harness, err)
		return
	}
	if !status.Required || status.Ready {
		return
	}
	switch strings.TrimSpace(status.State) {
	case "needs_configure", "pending_browser_login":
		return
	}
	if strings.TrimSpace(status.AuthURL) != "" {
		return
	}

	started, startErr := gate.StartDeviceAuth(ctx)
	if startErr != nil {
		logf(
			"hub.auth status=warn harness=%s action=start_device_auth err=%q detail=%q",
			runtime.Harness,
			startErr,
			firstNonEmptyString(started.Message, status.Message),
		)
		return
	}
	logf(
		"hub.auth status=start harness=%s action=start_device_auth state=%s",
		runtime.Harness,
		firstNonEmptyString(started.State, "pending_browser_login"),
	)
}

func shouldEnableAgentAuthConfigure(harness string) bool {
	switch strings.TrimSpace(strings.ToLower(harness)) {
	case agentruntime.HarnessCodex, agentruntime.HarnessClaude, agentruntime.HarnessAuggie:
		return true
	default:
		return false
	}
}

func diagnosticDetailForResult(res execx.Result) string {
	candidates := []string{res.Stdout, res.Stderr}
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			line = strings.Join(strings.Fields(line), " ")
			const maxChars = 200
			if len(line) > maxChars {
				return line[:maxChars-3] + "..."
			}
			return line
		}
	}
	return "check completed"
}

func hubPingURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base url must use http or https")
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("base url host is required")
	}
	u.Path = "/ping"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func checkHubPing(ctx context.Context, pingURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
	if err != nil {
		return "", fmt.Errorf("build ping request: %w", err)
	}

	client := &http.Client{Timeout: hubPingDiagnosticTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s failed: %w", pingURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	detail := fmt.Sprintf("%s status=%d", pingURL, resp.StatusCode)
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		trimmed = strings.Join(strings.Fields(trimmed), " ")
		if len(trimmed) > 120 {
			trimmed = trimmed[:117] + "..."
		}
		detail += fmt.Sprintf(" body=%q", trimmed)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("GET %s returned status=%d", pingURL, resp.StatusCode)
	}
	return detail, nil
}

func hubCredentialsConfigured(cfg hub.InitConfig, loadRuntimeConfig runtimeConfigLoader) bool {
	if strings.TrimSpace(cfg.AgentToken) != "" || strings.TrimSpace(cfg.BindToken) != "" {
		return true
	}
	if loadRuntimeConfig == nil {
		return false
	}
	stored, err := loadRuntimeConfig()
	if err != nil {
		return false
	}
	return strings.TrimSpace(stored.AgentToken) != "" || strings.TrimSpace(stored.BindToken) != ""
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
