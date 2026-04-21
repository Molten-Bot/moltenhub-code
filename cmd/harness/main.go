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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/agentruntime"
	"github.com/Molten-Bot/moltenhub-code/internal/config"
	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/failurefollowup"
	"github.com/Molten-Bot/moltenhub-code/internal/harness"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
	"github.com/Molten-Bot/moltenhub-code/internal/library"
	"github.com/Molten-Bot/moltenhub-code/internal/multiplex"
	"github.com/Molten-Bot/moltenhub-code/internal/workspace"
)

const failureFollowUpRequiredPrompt = failurefollowup.RequiredPrompt

const hubBootRecommendation = "Recommended: connect this runtime to Molten Hub at https://molten.bot/hub so agents can dispatch work to it."
const hubPingLocalOnlyDetail = "Hub endpoint ping precheck failed; continuing in local-only mode. Use the local UI/API to submit tasks."
const hubPingRemoteContinueDetail = "Hub endpoint ping precheck failed; continuing remote startup because Hub credentials are configured and UI is disabled."
const hubPingHeadlessNoopDetail = "Hub endpoint ping precheck failed with UI disabled and no Hub credentials configured; startup completed without remote transport."
const gitHubCLIPackageLabel = "github-cli (gh)"
const gitHubCLIAuthRecommendation = "Run `gh auth login` (the GitHub CLI binary from the `github-cli` package) or set GH_TOKEN before dispatching tasks."
const followUpTaskLogArchiveSubdir = "followup"
const hubSetupLocationsURL = "https://molten.bot/hubs.json"
const localSubmitSource = "local_submit"
const rerunSource = "rerun"
const failureFollowUpSource = "failure_followup"
const noChangesFollowUpSource = "no_changes_followup"
const noChangesEscalationSource = "no_changes_escalation"

const hubBootDiagnosticTimeout = 10 * time.Second
const hubPingDiagnosticTimeout = 5 * time.Second
const hubPingRetryInterval = 12 * time.Second
const hubSetupLocationsFetchTimeout = 2 * time.Second
const hubSetupLocationsCacheTTL = 5 * time.Minute

type hubSetupLocation struct {
	Display string `json:"display"`
	Key     string `json:"key"`
	Domain  string `json:"domain"`
}

var defaultHubSetupLocations = []hubSetupLocation{
	{Display: "North America", Key: "na", Domain: "na.hub.molten.bot"},
	{Display: "Europe", Key: "eu", Domain: "eu.hub.molten.bot"},
}

var hubSetupLocationsLoader = fetchHubSetupLocations

var hubSetupLocationsCache struct {
	mu        sync.Mutex
	locations []hubSetupLocation
	fetchedAt time.Time
}

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

	if result.NoChanges && !resultHasPR(result) {
		var line strings.Builder
		line.WriteString(fmt.Sprintf("status=no_changes workspace=%s branch=%s", result.WorkspaceDir, result.Branch))
		if result.PRURL != "" {
			line.WriteString(fmt.Sprintf(" pr_url=%s", result.PRURL))
		}
		if prURLs := joinAllPRURLs(result.RepoResults); prURLs != "" {
			line.WriteString(fmt.Sprintf(" pr_urls=%s", prURLs))
		}
		writeStdoutLine(logger, line.String())
		return harness.ExitSuccess
	}
	var line strings.Builder
	line.WriteString(fmt.Sprintf("status=completed workspace=%s branch=%s", result.WorkspaceDir, result.Branch))
	if result.PRURL != "" {
		line.WriteString(fmt.Sprintf(" pr_url=%s", result.PRURL))
	}
	if prURLs := completedPRURLs(result); prURLs != "" {
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
	logLevel, err := hub.ParseLogLevel(cfg.LogLevel)
	if err != nil {
		logLevel = hub.LogLevelInfo
	}
	logRouter := newHubLogRouter(logger, monitorBroker, logLevel)
	daemonLogger := logRouter.Printf

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
	var hubPingLive <-chan struct{}
	if bootDiag.PingChecked && !bootDiag.PingOK {
		if pingURL, pingURLErr := hubPingURL(cfg.BaseURL); pingURLErr == nil {
			hubPingLive = startHubPingRetryLoop(ctx, cfg.BaseURL, pingURL, bootDiag.PingErr, daemonLogger, checkHubPing)
		}
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

	var queueFailureRerun func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config, source string)
	var queueFailureFollowUp func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config, source string)
	var queueUnexpectedNoChangesFollowUp func(requestID string, result harness.Result, runCfg config.Config)
	var queueEscalatedNoChangesFollowUp func(requestID string, result harness.Result, runCfg config.Config)
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

		activeCfg, err := effectiveHubSetupConfig(cfg)
		if err != nil {
			return "", fmt.Errorf("load runtime config: %w", err)
		}
		runCfg = applyDefaultAgentRuntimeConfig(runCfg, activeCfg)
		runCfg, err = hub.ApplyBoundAgentRuntime(runCfg, activeCfg)
		if err != nil {
			return "", err
		}
		if err := validateRunConfigPromptImages(runCfg); err != nil {
			return "", err
		}
		source = strings.TrimSpace(source)
		if source == "" {
			source = localSubmitSource
		}

		dedupeKey := dedupeKeyForRunConfig(runCfg)
		if force {
			dedupeKey = ""
		}
		allowCompletedDuplicate := source == rerunSource
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
			source string,
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
						if !errors.Is(waitErr, context.Canceled) && allowFailureFollowUp {
							failedResult := harness.Result{
								ExitCode: harness.ExitPreflight,
								Err:      fmt.Errorf("dispatch wait: %w", waitErr),
							}
							if queueFailureRerun != nil {
								queueFailureRerun(requestID, failedResult, runCfg, source)
							}
							if queueFailureFollowUp != nil {
								queueFailureFollowUp(requestID, failedResult, runCfg, source)
							}
						}
						return
					}
				}

				acquireCtx, cancelAcquire := context.WithCancel(runCtx)
				if taskHandle != nil {
					taskHandle.SetAcquireCancel(cancelAcquire)
				}
				forceAcquire := false
				if taskHandle != nil {
					forceAcquire = taskHandle.ConsumeForceAcquire()
				}
				acquire := dispatchController.Acquire
				if forceAcquire {
					acquire = dispatchController.AcquireForce
				}
				release, acquireErr := acquire(acquireCtx, requestID)
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
					if taskHandle != nil && taskHandle.HasForceAcquire() && errors.Is(acquireErr, context.Canceled) && runCtx.Err() == nil {
						continue
					}
					finalState = "error"
					daemonLogger("dispatch status=error request_id=%s err=%q", requestID, acquireErr)
					if !errors.Is(acquireErr, context.Canceled) && allowFailureFollowUp {
						failedResult := harness.Result{
							ExitCode: harness.ExitPreflight,
							Err:      fmt.Errorf("dispatch acquire: %w", acquireErr),
						}
						if queueFailureRerun != nil {
							queueFailureRerun(requestID, failedResult, runCfg, source)
						}
						if queueFailureFollowUp != nil {
							queueFailureFollowUp(requestID, failedResult, runCfg, source)
						}
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
				switch outcome.State {
				case "error":
					if allowFailureFollowUp {
						if queueFailureRerun != nil {
							queueFailureRerun(requestID, outcome.Result, runCfg, source)
						}
						if queueFailureFollowUp != nil {
							queueFailureFollowUp(requestID, outcome.Result, runCfg, source)
						}
					}
				case "no_changes":
					if source != noChangesFollowUpSource && source != noChangesEscalationSource && queueUnexpectedNoChangesFollowUp != nil {
						queueUnexpectedNoChangesFollowUp(requestID, outcome.Result, runCfg)
					}
					if queueEscalatedNoChangesFollowUp != nil {
						if ok, _ := shouldEscalateNoChangesFollowUp(source, outcome.Result, runCfg); ok {
							queueEscalatedNoChangesFollowUp(requestID, outcome.Result, runCfg)
						}
					}
				}
				return
			}
		}(requestID, runCfg, dedupeKey, source, allowFailureFollowUp, runCtx, cancelRun, taskHandle)

		return requestID, nil
	}
	queueFailureRerun = func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config, source string) {
		if ok, reason := shouldQueueFailureRerun(source, failedResult); !ok {
			daemonLogger(
				"dispatch status=warn action=skip_failure_rerun request_id=%s err=%q",
				failedRequestID,
				reason,
			)
			return
		}

		rerunRequestID, rerunErr := enqueueFailureRerun(ctx, enqueueLocalRun, failedRunCfg)
		if rerunErr != nil {
			daemonLogger(
				"dispatch status=warn action=queue_failure_rerun request_id=%s err=%q",
				failedRequestID,
				rerunErr,
			)
			return
		}
		daemonLogger(
			"dispatch status=ok action=queue_failure_rerun request_id=%s rerun_request_id=%s",
			failedRequestID,
			rerunRequestID,
		)
	}
	queueFailureFollowUp = func(failedRequestID string, failedResult harness.Result, failedRunCfg config.Config, source string) {
		if ok, reason := shouldQueueFailureFollowUp(source, failedResult); !ok {
			daemonLogger(
				"dispatch status=warn action=skip_failure_followup request_id=%s err=%q",
				failedRequestID,
				reason,
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
		followUpRequestID, followUpErr := enqueueLocalRun(ctx, followUpCfg, true, failureFollowUpSource, false)
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
	queueUnexpectedNoChangesFollowUp = func(requestID string, result harness.Result, runCfg config.Config) {
		if ok, reason := shouldQueueUnexpectedNoChangesFollowUp(result); !ok {
			daemonLogger(
				"dispatch status=warn action=skip_no_changes_followup request_id=%s err=%q",
				requestID,
				reason,
			)
			return
		}

		followUpCfg := unexpectedNoChangesFollowUpRunConfig(requestID, result, runCfg, logRoot)
		if len(followUpCfg.RepoList()) == 0 {
			daemonLogger(
				"dispatch status=warn action=queue_no_changes_followup request_id=%s err=%q",
				requestID,
				"no task repo found for no-changes follow-up",
			)
			return
		}

		followUpRequestID, followUpErr := enqueueLocalRun(ctx, followUpCfg, true, noChangesFollowUpSource, false)
		if followUpErr != nil {
			daemonLogger(
				"dispatch status=warn action=queue_no_changes_followup request_id=%s err=%q",
				requestID,
				followUpErr,
			)
			return
		}
		daemonLogger(
			"dispatch status=ok action=queue_no_changes_followup request_id=%s follow_up_request_id=%s",
			requestID,
			followUpRequestID,
		)
	}
	queueEscalatedNoChangesFollowUp = func(requestID string, result harness.Result, runCfg config.Config) {
		if ok, reason := shouldEscalateNoChangesFollowUp(noChangesFollowUpSource, result, runCfg); !ok {
			daemonLogger(
				"dispatch status=warn action=skip_no_changes_escalation request_id=%s err=%q",
				requestID,
				reason,
			)
			return
		}

		followUpCfg := escalatedNoChangesFollowUpRunConfig(requestID, result, runCfg, logRoot)
		if len(followUpCfg.RepoList()) == 0 {
			daemonLogger(
				"dispatch status=warn action=queue_no_changes_escalation request_id=%s err=%q",
				requestID,
				"no task repo found for no-changes escalation",
			)
			return
		}

		followUpRequestID, followUpErr := enqueueLocalRun(ctx, followUpCfg, false, noChangesEscalationSource, false)
		if followUpErr != nil {
			daemonLogger(
				"dispatch status=warn action=queue_no_changes_escalation request_id=%s err=%q",
				requestID,
				followUpErr,
			)
			return
		}
		daemonLogger(
			"dispatch status=ok action=queue_no_changes_escalation request_id=%s follow_up_request_id=%s",
			requestID,
			followUpRequestID,
		)
	}

	hubController := newHubDaemonController(ctx, runner)
	hubController.logf = daemonLogger
	hubController.dispatchController = dispatchController
	hubController.taskLogRoot = logRoot
	hubController.registerTaskControl = func(requestID string, cancel context.CancelCauseFunc) hub.DispatchTaskControl {
		return localTaskController.Register(requestID, cancel)
	}
	hubController.completeTaskControl = localTaskController.Complete
	hubController.onDispatchQueued = func(requestID string, runCfg config.Config) {
		if runConfigJSON, ok := marshalRunConfigJSON(runCfg); ok {
			monitorBroker.RecordTaskRunConfig(requestID, runConfigJSON)
		}
	}
	hubController.onDispatchFailed = func(requestID string, runCfg config.Config, result harness.Result) {
		if queueFailureFollowUp != nil {
			queueFailureFollowUp(requestID, result, runCfg, "hub_dispatch")
		}
	}
	hubRuntimeReloader := newHubRuntimeConfigReloader(cfg, hubController.Update, hubController.Stop, daemonLogger)
	go hubRuntimeReloader.Run(ctx, hubRuntimeConfigReloadInterval)

	waitForHubRuntime := func() int {
		pingLive := hubPingLive
		for {
			select {
			case <-ctx.Done():
				return harness.ExitSuccess
			case <-pingLive:
				pingLive = nil
				if !hubConfigured {
					continue
				}
				if err := hubController.Update(ctx, cfg); err != nil {
					if shouldFallbackToLocalOnlyMode(*uiListen, err) {
						daemonLogger(
							"hub.auth status=local_only detail=%q",
							"Remote hub auth failed; continuing in local-only mode. Use the local UI/API to submit tasks.",
						)
						continue
					}
					writeStderrLine(logger, fmt.Sprintf("error: %v", err))
					return hubExitCode(err)
				}
			case err := <-hubController.Errors():
				if err == nil {
					continue
				}
				if shouldFallbackToLocalOnlyMode(*uiListen, err) {
					daemonLogger(
						"hub.auth status=local_only detail=%q",
						"Remote hub auth failed; continuing in local-only mode. Use the local UI/API to submit tasks.",
					)
					continue
				}
				writeStderrLine(logger, fmt.Sprintf("error: %v", err))
				return hubExitCode(err)
			}
		}
	}

	if strings.TrimSpace(*uiListen) != "" {
		uiServer := hubui.NewServer(*uiListen, monitorBroker)
		uiServer.AutomaticMode = *uiAutomatic
		uiServer.ConfiguredHarness = cfg.AgentHarness
		uiServer.Logf = daemonLogger
		uiServer.LoadLibraryTasks = func() ([]library.TaskSummary, error) {
			catalog, err := library.LoadCatalog(library.DefaultDir)
			if err != nil {
				return nil, err
			}
			usage := hub.ReadRuntimeConfigLibraryTaskUsage(cfg.RuntimeConfigPath)
			return library.OrderSummariesByUsage(catalog.Summaries(), usage), nil
		}
		cleanupTaskLogs := func(_ context.Context, requestID string) error {
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
		if authGate != nil {
			uiServer.AgentAuthStatus = authGate.Status
			uiServer.StartAgentAuth = authGate.StartDeviceAuth
			uiServer.VerifyAgentAuth = authGate.Verify
			if shouldEnableAgentAuthConfigure(runtimeCfg.Harness) {
				uiServer.ConfigureAgentAuth = authGate.Configure
			}
		}
		uiServer.HubSetupStatus = func(reqCtx context.Context) (hubui.HubSetupState, error) {
			return currentHubSetupStateWithRemoteProfile(reqCtx, cfg), nil
		}
		uiServer.ConfigureHubSetup = func(reqCtx context.Context, req hubui.HubSetupRequest) (hubui.HubSetupState, error) {
			return configureHubSetup(reqCtx, cfg, req, hubController.Update)
		}
		uiServer.ConnectHubSetup = func(reqCtx context.Context) (hubui.HubSetupState, error) {
			return connectHubSetup(reqCtx, cfg, hubController.Update)
		}
		uiServer.DisconnectHubSetup = func(reqCtx context.Context) (hubui.HubSetupState, error) {
			return disconnectHubSetup(reqCtx, cfg, hubController.Stop)
		}
		recordLibraryUsage := func(runCfg config.Config) {
			if strings.TrimSpace(runCfg.LibraryTaskName) == "" {
				return
			}
			if err := hub.IncrementRuntimeConfigLibraryTaskUsage(cfg.RuntimeConfigPath, cfg, runCfg.LibraryTaskName); err != nil {
				daemonLogger("library.usage status=warn task=%s err=%q", runCfg.LibraryTaskName, err)
			}
		}
		uiServer.SubmitLocalPrompt = func(reqCtx context.Context, body []byte) (string, error) {
			runCfg, err := hub.ParseRunConfigJSON(body)
			if err != nil {
				return "", fmt.Errorf("invalid run config: %w", err)
			}
			recordLibraryUsage(runCfg)
			return enqueueLocalRun(reqCtx, runCfg, true, localSubmitSource, false)
		}
		uiServer.SubmitTaskRerun = func(reqCtx context.Context, _ string, body []byte, force bool) (string, error) {
			runCfg, err := hub.ParseRunConfigJSON(body)
			if err != nil {
				return "", fmt.Errorf("invalid run config: %w", err)
			}
			recordLibraryUsage(runCfg)
			return enqueueLocalRun(reqCtx, runCfg, true, rerunSource, force)
		}
		uiServer.CloseTask = cleanupTaskLogs
		uiServer.ResolveTaskControls = localTaskController.Controls
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
		uiServer.ForceRunTask = func(_ context.Context, requestID string) error {
			if err := localTaskController.ForceRun(requestID); err != nil {
				return err
			}
			daemonLogger("dispatch status=forced request_id=%s", requestID)
			return nil
		}
		uiServer.StopTask = func(_ context.Context, requestID string) error {
			if err := localTaskController.Stop(requestID); err != nil {
				return err
			}
			daemonLogger("dispatch status=stopped request_id=%s err=%q", requestID, errTaskStoppedByOperator)
			return nil
		}
		daemonLogger("hub.ui status=ready url=%s", monitorURL(*uiListen))
		prMonitor := &hubui.PRMergeMonitor{
			Runner:      runner,
			Broker:      monitorBroker,
			Logf:        daemonLogger,
			CleanupTask: cleanupTaskLogs,
		}
		go func() {
			if err := prMonitor.Run(ctx); err != nil {
				daemonLogger("hub.ui status=warn event=pr_monitor err=%q", err)
			}
		}()
		go func() {
			if err := uiServer.Run(ctx); err != nil {
				daemonLogger("hub.ui status=error err=%q", err)
			}
		}()
	}

	if forceLocalOnlyMode {
		if strings.TrimSpace(*uiListen) == "" {
			daemonLogger("hub.auth status=local_only detail=%q", hubPingFailureDetail(hubPingHeadlessNoopDetail, bootDiag.PingErr))
			return harness.ExitSuccess
		}
		return waitForHubRuntime()
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
		return waitForHubRuntime()
	}

	if bootDiag.PingChecked && !bootDiag.PingOK && hubPingLive != nil {
		return waitForHubRuntime()
	}

	if err := hubController.Update(ctx, cfg); err != nil {
		if shouldFallbackToLocalOnlyMode(*uiListen, err) {
			daemonLogger(
				"hub.auth status=local_only detail=%q",
				"Remote hub auth failed; continuing in local-only mode. Use the local UI/API to submit tasks.",
			)
			return waitForHubRuntime()
		}
		writeStderrLine(logger, fmt.Sprintf("error: %v", err))
		return hubExitCode(err)
	}
	return waitForHubRuntime()
}

func loadHubBootConfig(initPath, configPath string) (hub.InitConfig, int, error) {
	initPath = strings.TrimSpace(initPath)
	configPath = strings.TrimSpace(configPath)

	if initPath != "" && configPath != "" {
		return hub.InitConfig{}, harness.ExitUsage, fmt.Errorf("provide only one of --init or --config")
	}

	if configPath != "" {
		return loadHubBootRuntimeConfigOrDefault(configPath)
	}

	if initPath != "" {
		cfg, err := hub.LoadInit(initPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				runtimePath := hub.ResolveRuntimeConfigPath(initPath)
				runtimeCfg, runtimeErr := hub.LoadRuntimeConfig(runtimePath)
				if runtimeErr == nil {
					cfg := runtimeCfg.Init()
					cfg.RuntimeConfigPath = runtimeCfg.RuntimeConfigPath
					return cfg, harness.ExitSuccess, nil
				}
				return loadHubBootRuntimeConfigOrDefault(runtimePath)
			}
			return hub.InitConfig{}, harness.ExitConfig, fmt.Errorf("init config error: %w", err)
		}
		cfg.RuntimeConfigPath = hub.ResolveRuntimeConfigPath(initPath)
		cfg.LogLevel = configuredHubLogLevel(cfg.LogLevel, cfg.RuntimeConfigPath)
		return cfg, harness.ExitSuccess, nil
	}

	return loadHubBootRuntimeConfigOrDefault("")
}

func loadHubBootRuntimeConfigOrDefault(path string) (hub.InitConfig, int, error) {
	runtimeCfg, err := hub.LoadRuntimeConfig(path)
	if err != nil {
		resolvedPath := strings.TrimSpace(path)
		if resolvedPath == "" {
			resolvedPath = hub.ResolveRuntimeConfigPath("")
		}
		return defaultHubBootConfig(resolvedPath)
	}

	cfg := runtimeCfg.Init()
	cfg.RuntimeConfigPath = runtimeCfg.RuntimeConfigPath
	return cfg, harness.ExitSuccess, nil
}

func configuredHubLogLevel(preferred, runtimeConfigPath string) string {
	level := hub.NormalizeLogLevel(preferred)
	if level == "" {
		level = hub.DefaultLogLevel
	}

	runtimeConfigPath = strings.TrimSpace(runtimeConfigPath)
	if runtimeConfigPath == "" {
		return level
	}

	configured := hub.NormalizeLogLevel(hub.ReadRuntimeConfigString(runtimeConfigPath, "log_level", "logLevel", "LOG_LEVEL"))
	if configured != "" {
		return configured
	}

	if runtimeCfg, err := hub.LoadRuntimeConfig(runtimeConfigPath); err == nil {
		if configured = hub.NormalizeLogLevel(runtimeCfg.LogLevel); configured != "" {
			return configured
		}
	}

	return level
}

func defaultHubBootConfig(runtimeConfigPath string) (hub.InitConfig, int, error) {
	cfg := hub.InitConfig{
		RuntimeConfigPath: strings.TrimSpace(runtimeConfigPath),
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return hub.InitConfig{}, harness.ExitConfig, fmt.Errorf("init config error: %w", err)
	}
	return cfg, harness.ExitSuccess, nil
}

type hubLogRouter struct {
	logger *terminalLogger
	broker *hubui.Broker
	level  hub.LogLevel
}

func newHubLogRouter(logger *terminalLogger, broker *hubui.Broker, level hub.LogLevel) hubLogRouter {
	return hubLogRouter{
		logger: logger,
		broker: broker,
		level:  level,
	}
}

func (r hubLogRouter) Printf(format string, args ...any) {
	r.Log(strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (r hubLogRouter) Log(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	if r.broker != nil {
		r.broker.IngestLog(line)
	}

	eventLevel := classifyHubLogLine(line)
	if !r.level.Allows(eventLevel) {
		if r.logger != nil {
			// Keep filtered lines in the task mirror for follow-up diagnostics.
			r.logger.Capture(line)
		}
		return
	}

	if r.logger != nil {
		r.logger.Print(line)
	}
}

func classifyHubLogLine(line string) hub.LogLevel {
	line = strings.TrimSpace(line)
	if line == "" {
		return hub.LogLevelInfo
	}

	lowerLine := strings.ToLower(line)
	if strings.HasPrefix(lowerLine, "error:") {
		return hub.LogLevelError
	}
	if strings.HasPrefix(lowerLine, "warn:") {
		return hub.LogLevelWarn
	}
	if strings.HasPrefix(line, "debug ") {
		return hub.LogLevelDebug
	}

	fields := parseSimpleKVFields(line)
	status := strings.ToLower(strings.TrimSpace(fields["status"]))
	switch status {
	case "error", "failed", "invalid":
		return hub.LogLevelError
	case "warn":
		return hub.LogLevelWarn
	case "running":
		return hub.LogLevelDebug
	}

	if shouldClassifyHubDebugLine(line, lowerLine, fields, status) {
		return hub.LogLevelDebug
	}

	return hub.LogLevelInfo
}

func shouldClassifyHubDebugLine(line, lowerLine string, fields map[string]string, status string) bool {
	if isDebugCommandLogLine(line, fields) {
		return true
	}
	if strings.TrimSpace(fields["stage"]) != "" {
		return true
	}
	if strings.HasPrefix(lowerLine, "dispatch status=") && isDebugDispatchStatus(status) {
		return true
	}
	return false
}

func isDebugDispatchStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "start", "ack", "queued", "duplicate", "paused", "resumed", "forced", "running":
		return true
	default:
		return false
	}
}
func isDebugCommandLogLine(line string, fields map[string]string) bool {
	if len(fields) == 0 {
		return false
	}
	phase := strings.TrimSpace(fields["phase"])
	if phase == "" {
		return false
	}
	return strings.HasPrefix(line, "cmd ") || strings.Contains(line, " cmd ")
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
	if res.NoChanges && !resultHasPR(res) {
		logf(
			"dispatch status=no_changes request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s",
			requestID,
			res.WorkspaceDir,
			res.Branch,
			res.PRURL,
			joinAllPRURLs(res.RepoResults),
		)
		return localDispatchOutcome{State: "no_changes", Result: res}
	}
	logf(
		"dispatch status=completed request_id=%s workspace=%s branch=%s pr_url=%s pr_urls=%s changed_repos=%d",
		requestID,
		res.WorkspaceDir,
		res.Branch,
		res.PRURL,
		completedPRURLs(res),
		countChangedRepos(res.RepoResults),
	)
	return localDispatchOutcome{State: "completed", Result: res}
}

func failureFollowUpRunConfig(
	failedRequestID string,
	failedResult harness.Result,
	failedRunCfg config.Config,
	logRoot string,
) config.Config {
	logPaths := followUpTaskLogPaths(logRoot, failedRequestID)
	baseBranch, targetSubdir := failurefollowup.FollowUpTargeting(
		failedRunCfg.BaseBranch,
		failedRunCfg.TargetSubdir,
		failedResult.Branch,
	)
	return config.Config{
		Repos:        failureFollowUpRepos(failedResult, failedRunCfg),
		BaseBranch:   baseBranch,
		TargetSubdir: targetSubdir,
		Prompt:       failureFollowUpPrompt(logPaths, failedResult, failedRunCfg),
	}
}

func unexpectedNoChangesFollowUpRunConfig(
	requestID string,
	result harness.Result,
	runCfg config.Config,
	logRoot string,
) config.Config {
	runCfg.ApplyDefaults()
	logPaths := existingPaths(followUpTaskLogPaths(logRoot, requestID))
	baseBranch := strings.TrimSpace(runCfg.BaseBranch)
	return config.Config{
		Repos:        unexpectedNoChangesFollowUpRepos(runCfg),
		BaseBranch:   baseBranch,
		TargetSubdir: runCfg.TargetSubdir,
		Prompt:       unexpectedNoChangesFollowUpPrompt(logPaths, requestID, result, runCfg),
	}
}

func shouldQueueFailureFollowUp(source string, failedResult harness.Result) (bool, string) {
	source = strings.TrimSpace(source)
	if failedResult.Err == nil {
		return false, "failed task did not include an error"
	}
	if errors.Is(failedResult.Err, agentruntime.ErrPromptImagesUnsupported) {
		return false, "prompt images are unsupported for the configured harness"
	}
	if source == failureFollowUpSource || source == noChangesEscalationSource {
		return false, "run is already a failure follow-up"
	}
	if source == "hub_dispatch" {
		return false, "hub dispatch failures are already escalated by hub transport"
	}
	return true, ""
}

func shouldQueueFailureRerun(source string, failedResult harness.Result) (bool, string) {
	source = strings.TrimSpace(source)
	if failedResult.Err == nil {
		return false, "failed task did not include an error"
	}
	if errors.Is(failedResult.Err, agentruntime.ErrPromptImagesUnsupported) {
		return false, "prompt images are unsupported for the configured harness"
	}
	switch source {
	case failureFollowUpSource, noChangesEscalationSource:
		return false, "run is already a failure follow-up"
	case "hub_dispatch":
		return false, "hub dispatch failures are already rerun by hub transport"
	case rerunSource:
		return false, "run is already a failure rerun"
	}
	return true, ""
}

type localRunEnqueueFunc func(context.Context, config.Config, bool, string, bool) (string, error)

func enqueueFailureRerun(ctx context.Context, enqueue localRunEnqueueFunc, failedRunCfg config.Config) (string, error) {
	if enqueue == nil {
		return "", fmt.Errorf("local run enqueuer is required")
	}
	// The failed run still owns the dedupe key until its goroutine unwinds, so
	// the required one-time rerun must bypass duplicate suppression.
	return enqueue(ctx, failedRunCfg, false, rerunSource, true)
}

func shouldQueueUnexpectedNoChangesFollowUp(result harness.Result) (bool, string) {
	if !result.NoChanges {
		return false, "task did not complete with no changes"
	}
	if strings.TrimSpace(joinAllPRURLs(result.RepoResults)) != "" || strings.TrimSpace(result.PRURL) != "" {
		return false, "task already has a pull request"
	}
	return true, ""
}

func shouldEscalateNoChangesFollowUp(source string, result harness.Result, runCfg config.Config) (bool, string) {
	if strings.TrimSpace(source) != noChangesFollowUpSource {
		return false, "run is not a no-changes follow-up"
	}
	if !result.NoChanges {
		return false, "follow-up did not complete as no_changes"
	}
	if strings.TrimSpace(joinAllPRURLs(result.RepoResults)) != "" || strings.TrimSpace(result.PRURL) != "" {
		return false, "task already has a pull request"
	}
	if !promptRequestsRepositoryChange(runCfg.Prompt) {
		return false, "original prompt does not clearly request repository changes"
	}
	return true, ""
}

func failureFollowUpRepos(_ harness.Result, _ config.Config) []string {
	if repo := strings.TrimSpace(failurefollowup.FollowUpRepositoryURL); repo != "" {
		return []string{repo}
	}
	return nil
}

func unexpectedNoChangesFollowUpRepos(runCfg config.Config) []string {
	repos := runCfg.RepoList()
	if len(repos) > 0 {
		return repos
	}
	return failureFollowUpRepos(harness.Result{}, runCfg)
}

func unexpectedNoChangesFollowUpPrompt(logPaths []string, requestID string, result harness.Result, runCfg config.Config) string {
	const requiredPrompt = "Review the previous local task logs first. The prior run completed with no file changes and no pull request, which is unexpected for this task. Identify why the task produced no repository changes, fix the underlying issue, complete the requested work, validate locally where possible, and summarize the verified results. Only return a no-op if you can cite concrete repository evidence that the request is already satisfied; otherwise produce the smallest correct diff or return an explicit failure with blocker details."

	var contextLines []string
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		contextLines = append(contextLines, fmt.Sprintf("- request_id=%s", requestID))
	}
	if workspaceDir := strings.TrimSpace(result.WorkspaceDir); workspaceDir != "" {
		contextLines = append(contextLines, fmt.Sprintf("- workspace_dir=%s", workspaceDir))
	}
	if branch := strings.TrimSpace(result.Branch); branch != "" {
		contextLines = append(contextLines, fmt.Sprintf("- branch=%s", branch))
	}
	if repo := strings.Join(runCfg.RepoList(), ","); strings.TrimSpace(repo) != "" {
		contextLines = append(contextLines, fmt.Sprintf("- repos=%s", repo))
	}
	if targetSubdir := strings.TrimSpace(runCfg.TargetSubdir); targetSubdir != "" {
		contextLines = append(contextLines, fmt.Sprintf("- target_subdir=%s", targetSubdir))
	}

	var contextBlock strings.Builder
	if len(contextLines) > 0 {
		contextBlock.WriteString("Observed no-change context:\n")
		contextBlock.WriteString(strings.Join(contextLines, "\n"))
	}
	if prompt := strings.TrimSpace(runCfg.Prompt); prompt != "" {
		if contextBlock.Len() > 0 {
			contextBlock.WriteString("\n\n")
		}
		contextBlock.WriteString("Original task prompt:\n")
		contextBlock.WriteString(prompt)
	}

	return failurefollowup.ComposePrompt(
		requiredPrompt,
		logPaths,
		nil,
		"No local task log path was captured before the task completed without changes. Review the task history and runtime logs first.",
		contextBlock.String(),
	)
}

func escalatedNoChangesFollowUpRunConfig(
	requestID string,
	result harness.Result,
	runCfg config.Config,
	logRoot string,
) config.Config {
	runCfg.ApplyDefaults()
	logPaths := existingPaths(followUpTaskLogPaths(logRoot, requestID))
	baseBranch := strings.TrimSpace(runCfg.BaseBranch)
	return config.Config{
		Repos:        unexpectedNoChangesFollowUpRepos(runCfg),
		BaseBranch:   baseBranch,
		TargetSubdir: runCfg.TargetSubdir,
		Prompt:       escalatedNoChangesFollowUpPrompt(logPaths, requestID, result, runCfg),
	}
}

func escalatedNoChangesFollowUpPrompt(logPaths []string, requestID string, result harness.Result, runCfg config.Config) string {
	const requiredPrompt = "Review the previous local task logs first. The original task and the no-changes follow-up both completed without file changes or a pull request. For this escalation, do not return another no-op unless you can cite exact file paths and concrete repository evidence proving the requested outcome already exists. Otherwise make the smallest correct repository change that satisfies the request. If a real diff is blocked, return an explicit failure with the precise blocker details instead of another ambiguous no-op."

	var contextLines []string
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		contextLines = append(contextLines, fmt.Sprintf("- request_id=%s", requestID))
	}
	if workspaceDir := strings.TrimSpace(result.WorkspaceDir); workspaceDir != "" {
		contextLines = append(contextLines, fmt.Sprintf("- workspace_dir=%s", workspaceDir))
	}
	if branch := strings.TrimSpace(result.Branch); branch != "" {
		contextLines = append(contextLines, fmt.Sprintf("- branch=%s", branch))
	}
	if repo := strings.Join(runCfg.RepoList(), ","); strings.TrimSpace(repo) != "" {
		contextLines = append(contextLines, fmt.Sprintf("- repos=%s", repo))
	}
	if targetSubdir := strings.TrimSpace(runCfg.TargetSubdir); targetSubdir != "" {
		contextLines = append(contextLines, fmt.Sprintf("- target_subdir=%s", targetSubdir))
	}

	var contextBlock strings.Builder
	if len(contextLines) > 0 {
		contextBlock.WriteString("Observed repeated no-change context:\n")
		contextBlock.WriteString(strings.Join(contextLines, "\n"))
	}
	if prompt := strings.TrimSpace(runCfg.Prompt); prompt != "" {
		if contextBlock.Len() > 0 {
			contextBlock.WriteString("\n\n")
		}
		contextBlock.WriteString("Original task prompt:\n")
		contextBlock.WriteString(prompt)
	}

	return failurefollowup.ComposePrompt(
		requiredPrompt,
		logPaths,
		nil,
		"No local task log path was captured before repeated no-change completions. Review the task history and runtime logs first.",
		contextBlock.String(),
	)
}

func promptRequestsRepositoryChange(prompt string) bool {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return true
	}

	if strings.Contains(text, "?") {
		questionPrefixes := []string{"what ", "why ", "how ", "when ", "where ", "who "}
		for _, prefix := range questionPrefixes {
			if strings.HasPrefix(text, prefix) {
				return false
			}
		}
	}

	changeMarkers := []string{
		"fix", "change", "update", "modify", "refactor", "add", "remove", "create",
		"implement", "make", "rename", "replace", "convert", "style", "color",
		"pink", "build", "ship", "wire", "hook up", "repair",
	}
	for _, marker := range changeMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}

	readOnlyMarkers := []string{
		"review", "investigate", "analysis", "analyze", "summarize", "summary",
		"explain", "inspect", "read", "show", "list", "find", "compare", "audit",
		"why", "what", "how",
	}
	for _, marker := range readOnlyMarkers {
		if strings.Contains(text, marker) {
			return false
		}
	}

	if strings.Contains(text, "?") {
		return false
	}
	return true
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
	if targetSubdir := strings.TrimSpace(failedRunCfg.TargetSubdir); targetSubdir != "" {
		lines = append(lines, fmt.Sprintf("- target_subdir=%s", targetSubdir))
	}
	if prompt := strings.TrimSpace(failedRunCfg.Prompt); prompt != "" {
		lines = append(lines, "Original task prompt:")
		lines = append(lines, prompt)
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

func existingPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	existing := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		seen[path] = struct{}{}
		existing = append(existing, path)
	}
	if len(existing) == 0 {
		return nil
	}
	return existing
}

func followUpTaskLogPaths(logRoot, requestID string) []string {
	paths := taskLogPaths(logRoot, requestID)
	if _, ok := localTaskLogDir(logRoot, requestID); !ok {
		return paths
	}

	archived := archiveLocalTaskLogsForFollowUp(logRoot, requestID)
	if len(archived) > 0 {
		return archived
	}
	return paths
}

func archiveLocalTaskLogsForFollowUp(logRoot, requestID string) []string {
	sourceDir, ok := localTaskLogDir(logRoot, requestID)
	if !ok {
		return nil
	}
	sourceDir = strings.TrimSpace(sourceDir)
	if sourceDir == "" {
		return nil
	}
	if stat, err := os.Stat(sourceDir); err != nil || !stat.IsDir() {
		return nil
	}

	archiveDir, ok := followUpTaskLogArchiveDir(logRoot, requestID)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return nil
	}

	paths := []string{archiveDir}
	for _, fileName := range []string{legacyTaskLogFileName, logFileName} {
		sourcePath := filepath.Join(sourceDir, fileName)
		archivePath := filepath.Join(archiveDir, fileName)
		copied, err := copyFileIfPresent(sourcePath, archivePath)
		if err != nil || !copied {
			continue
		}
		paths = append(paths, archivePath)
	}
	if len(paths) == 1 {
		return nil
	}
	return paths
}

func copyFileIfPresent(sourcePath, targetPath string) (bool, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	targetPath = strings.TrimSpace(targetPath)
	if sourcePath == "" || targetPath == "" {
		return false, nil
	}

	in, err := os.Open(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer in.Close()

	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, maxLogFileOpenMode)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return false, err
	}
	if err := out.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func followUpTaskLogArchiveDir(logRoot, requestID string) (string, bool) {
	logRoot = strings.TrimSpace(logRoot)
	if logRoot == "" {
		return "", false
	}
	subdir, ok := failurefollowup.IdentifierSubdir(requestID)
	if !ok {
		return "", false
	}
	subdir = filepath.Clean(subdir)
	if subdir == "." || subdir == "" || subdir == ".." {
		return "", false
	}
	if filepath.IsAbs(subdir) || strings.HasPrefix(subdir, ".."+string(filepath.Separator)) {
		return "", false
	}

	return filepath.Join(logRoot, followUpTaskLogArchiveSubdir, subdir), true
}

func taskLogPaths(logRoot, requestID string) []string {
	return failurefollowup.TaskLogPaths(logRoot, requestID)
}

func taskLogDir(logRoot, requestID string) (string, bool) {
	return failurefollowup.TaskLogDir(logRoot, requestID)
}

func localTaskLogDir(logRoot, requestID string) (string, bool) {
	subdir, ok := failurefollowup.IdentifierSubdir(requestID)
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

func joinAllPRURLs(results []harness.RepoResult) string {
	if len(results) == 0 {
		return ""
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		if strings.TrimSpace(result.PRURL) == "" {
			continue
		}
		urls = append(urls, strings.TrimSpace(result.PRURL))
	}
	return strings.Join(urls, ",")
}

func resultHasPR(result harness.Result) bool {
	if strings.TrimSpace(result.PRURL) != "" {
		return true
	}
	return strings.TrimSpace(joinAllPRURLs(result.RepoResults)) != ""
}

func completedPRURLs(result harness.Result) string {
	if result.NoChanges {
		return joinAllPRURLs(result.RepoResults)
	}
	return joinPRURLs(result.RepoResults)
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
			gitHubCLIAuthRecommendation,
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
			"Install missing tools before running tasks: git, %s, %s.",
			gitHubCLIPackageLabel,
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

func validateRunConfigPromptImages(runCfg config.Config) error {
	if len(runCfg.Images) == 0 {
		return nil
	}
	runtime, err := agentruntime.Resolve(runCfg.AgentHarness, runCfg.AgentCommand)
	if err != nil {
		return err
	}
	if agentruntime.SupportsPromptImages(runtime.Harness) {
		return nil
	}
	return agentruntime.UnsupportedPromptImagesError(runtime.Harness)
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
	case agentruntime.HarnessCodex, agentruntime.HarnessClaude, agentruntime.HarnessAuggie, agentruntime.HarnessPi:
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

func startHubPingRetryLoop(
	ctx context.Context,
	baseURL string,
	pingURL string,
	initialErr error,
	logf func(string, ...any),
	pingCheck func(context.Context, string) (string, error),
) <-chan struct{} {
	return startHubPingRetryLoopWithInterval(ctx, baseURL, pingURL, initialErr, hubPingRetryInterval, logf, pingCheck)
}

func startHubPingRetryLoopWithInterval(
	ctx context.Context,
	baseURL string,
	pingURL string,
	initialErr error,
	interval time.Duration,
	logf func(string, ...any),
	pingCheck func(context.Context, string) (string, error),
) <-chan struct{} {
	live := make(chan struct{}, 1)
	if logf == nil {
		logf = func(string, ...any) {}
	}
	baseURL = strings.TrimSpace(baseURL)
	pingURL = strings.TrimSpace(pingURL)
	if interval <= 0 {
		interval = hubPingRetryInterval
	}
	if pingURL == "" {
		return live
	}
	if pingCheck == nil {
		pingCheck = checkHubPing
	}

	go func() {
		logHubPingRetryState(logf, baseURL, interval, initialErr)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			pingCtx, cancel := context.WithTimeout(ctx, hubPingDiagnosticTimeout)
			detail, err := pingCheck(pingCtx, pingURL)
			cancel()
			if err != nil {
				logHubPingRetryState(logf, baseURL, interval, err)
				continue
			}

			logf(
				"hub.connection status=reachable base_url=%s detail=%q",
				baseURL,
				firstNonEmptyString(strings.TrimSpace(detail), "Hub endpoint is live."),
			)
			select {
			case live <- struct{}{}:
			default:
			}
			return
		}
	}()

	return live
}

func logHubPingRetryState(logf func(string, ...any), baseURL string, interval time.Duration, pingErr error) {
	if logf == nil {
		return
	}
	logf(
		"hub.connection status=retrying base_url=%s detail=%q",
		strings.TrimSpace(baseURL),
		hubPingFailureDetail(
			fmt.Sprintf("Hub endpoint ping failed; retrying every %s until live.", interval),
			pingErr,
		),
	)
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

func currentHubSetupLocations() []hubSetupLocation {
	hubSetupLocationsCache.mu.Lock()
	defer hubSetupLocationsCache.mu.Unlock()

	if len(hubSetupLocationsCache.locations) > 0 && time.Since(hubSetupLocationsCache.fetchedAt) < hubSetupLocationsCacheTTL {
		return cloneHubSetupLocations(hubSetupLocationsCache.locations)
	}

	locations := cloneHubSetupLocations(defaultHubSetupLocations)
	if hubSetupLocationsLoader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), hubSetupLocationsFetchTimeout)
		defer cancel()
		if loaded, err := hubSetupLocationsLoader(ctx); err == nil && len(loaded) > 0 {
			locations = cloneHubSetupLocations(loaded)
		}
	}

	hubSetupLocationsCache.locations = cloneHubSetupLocations(locations)
	hubSetupLocationsCache.fetchedAt = time.Now()
	return cloneHubSetupLocations(locations)
}

func cloneHubSetupLocations(locations []hubSetupLocation) []hubSetupLocation {
	if len(locations) == 0 {
		return nil
	}
	cloned := make([]hubSetupLocation, 0, len(locations))
	for _, location := range locations {
		key := strings.ToLower(strings.TrimSpace(location.Key))
		domain := normalizeHubSetupLocationDomain(location.Domain)
		if key == "" || domain == "" {
			continue
		}
		display := strings.TrimSpace(location.Display)
		if display == "" {
			display = strings.ToUpper(key)
		}
		cloned = append(cloned, hubSetupLocation{
			Display: display,
			Key:     key,
			Domain:  domain,
		})
	}
	return cloned
}

func fetchHubSetupLocations(ctx context.Context) ([]hubSetupLocation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubSetupLocationsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build hub locations request: %w", err)
	}

	resp, err := (&http.Client{Timeout: hubSetupLocationsFetchTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch hub locations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch hub locations returned status=%d", resp.StatusCode)
	}

	var locations []hubSetupLocation
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&locations); err != nil {
		return nil, fmt.Errorf("decode hub locations: %w", err)
	}
	locations = cloneHubSetupLocations(locations)
	if len(locations) == 0 {
		return nil, fmt.Errorf("hub locations registry returned no usable locations")
	}
	return locations, nil
}

func normalizeHubSetupLocationDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if strings.Contains(domain, "://") {
		if parsed, err := url.Parse(domain); err == nil {
			domain = parsed.Host
		}
	}
	domain = strings.TrimSpace(strings.TrimRight(domain, "/"))
	return strings.ToLower(domain)
}

func hubSetupLocationByKey(locations []hubSetupLocation, key string) (hubSetupLocation, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, location := range locations {
		if strings.EqualFold(strings.TrimSpace(location.Key), key) {
			return location, true
		}
	}
	return hubSetupLocation{}, false
}

func hubSetupLocationBaseURL(location hubSetupLocation) string {
	domain := normalizeHubSetupLocationDomain(location.Domain)
	if domain == "" {
		return ""
	}
	return "https://" + domain + "/v1"
}

func hubSetupLocationForBaseURL(locations []hubSetupLocation, baseURL string) (hubSetupLocation, bool) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return hubSetupLocation{}, false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return hubSetupLocation{}, false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	if host == "" {
		return hubSetupLocation{}, false
	}
	for _, location := range locations {
		if normalizeHubSetupLocationDomain(location.Domain) == host {
			return location, true
		}
	}
	return hubSetupLocation{}, false
}

func resetHubSetupLocationsCache() {
	hubSetupLocationsCache.mu.Lock()
	defer hubSetupLocationsCache.mu.Unlock()
	hubSetupLocationsCache.locations = nil
	hubSetupLocationsCache.fetchedAt = time.Time{}
}

func currentHubSetupState(cfg hub.InitConfig) hubui.HubSetupState {
	state := hubui.HubSetupState{
		ConnectURL:      "https://app.molten.bot/signin?target=hub",
		DashboardURL:    "https://app.molten.bot/hub",
		AgentMode:       "existing",
		TokenType:       "agent",
		Region:          "na",
		Onboarding:      hubui.DefaultHubSetupOnboarding("existing"),
		OnboardingStage: "bind",
	}
	locations := currentHubSetupLocations()
	state.Region = normalizeHubSetupRegion("")

	activeCfg, err := effectiveHubSetupConfig(cfg)
	if err != nil {
		state.Message = fmt.Sprintf("Runtime config load failed: %v", err)
	}
	storedCfg, storedFound, storedErr := loadHubSetupRuntimeConfig(cfg.RuntimeConfigPath)
	if storedErr != nil && state.Message == "" {
		state.Message = fmt.Sprintf("Runtime config load failed: %v", storedErr)
	}

	persistedBindToken := ""
	persistedAgentToken := ""
	if storedFound {
		persistedBindToken = strings.TrimSpace(storedCfg.BindToken)
		persistedAgentToken = strings.TrimSpace(storedCfg.AgentToken)
	}
	state.Configured = persistedBindToken != "" || persistedAgentToken != ""
	if storedFound && strings.TrimSpace(storedCfg.BaseURL) != "" {
		state.Region = hubSetupRegionForBaseURLWithLocations(storedCfg.BaseURL, locations)
	} else {
		state.Region = hubSetupRegionForBaseURLWithLocations(activeCfg.BaseURL, locations)
	}
	state.Handle = strings.TrimSpace(activeCfg.Handle)
	state.Profile.ProfileText = strings.TrimSpace(activeCfg.Profile.ProfileText)
	state.Profile.DisplayName = strings.TrimSpace(activeCfg.Profile.DisplayName)
	state.Profile.Emoji = strings.TrimSpace(activeCfg.Profile.Emoji)
	if state.Configured {
		if persistedBindToken != "" {
			state.AgentMode = "new"
			state.TokenType = "bind"
		} else {
			state.TokenType = "agent"
		}
	}
	state.Onboarding = hubui.DefaultHubSetupOnboarding(state.AgentMode)
	state.ActivationReady = state.Configured
	return state
}

func loadHubSetupRuntimeConfig(path string) (hub.InitConfig, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return hub.InitConfig{}, false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return hub.InitConfig{}, false, nil
		}
		return hub.InitConfig{}, false, fmt.Errorf("read runtime config: %w", err)
	}

	var runtimeCfg hub.RuntimeConfig
	if err := json.Unmarshal(data, &runtimeCfg); err != nil {
		return hub.InitConfig{}, false, fmt.Errorf("parse runtime config: %w", err)
	}

	runtimeCfg.RuntimeConfigPath = path
	initCfg := runtimeCfg.Init()
	initCfg.RuntimeConfigPath = path
	return initCfg, true, nil
}

func currentHubSetupStateWithRemoteProfile(ctx context.Context, cfg hub.InitConfig) hubui.HubSetupState {
	state := currentHubSetupState(cfg)
	if !hubSetupProfileNeedsRemoteHydration(state) {
		return state
	}

	activeCfg, err := effectiveHubSetupConfig(cfg)
	if err != nil {
		return state
	}
	token := strings.TrimSpace(activeCfg.AgentToken)
	if token == "" {
		return state
	}

	client := hub.NewAPIClient(activeCfg.BaseURL)
	remoteProfile, err := client.AgentProfile(ctx, token)
	if err != nil {
		return state
	}

	if strings.TrimSpace(remoteProfile.Handle) != "" {
		state.Handle = strings.TrimSpace(remoteProfile.Handle)
	}
	mergedProfile := mergeProfileConfig(
		remoteProfile.Profile,
		hub.ProfileConfig{
			DisplayName: strings.TrimSpace(state.Profile.DisplayName),
			Emoji:       strings.TrimSpace(state.Profile.Emoji),
			ProfileText: strings.TrimSpace(state.Profile.ProfileText),
		},
	)
	state.Profile.DisplayName = strings.TrimSpace(mergedProfile.DisplayName)
	state.Profile.Emoji = strings.TrimSpace(mergedProfile.Emoji)
	state.Profile.ProfileText = strings.TrimSpace(mergedProfile.ProfileText)
	return state
}

func hubSetupProfileNeedsRemoteHydration(state hubui.HubSetupState) bool {
	if !state.Configured {
		return false
	}
	if strings.TrimSpace(state.Handle) == "" {
		return true
	}
	return strings.TrimSpace(state.Profile.DisplayName) == "" &&
		strings.TrimSpace(state.Profile.Emoji) == "" &&
		strings.TrimSpace(state.Profile.ProfileText) == ""
}

func effectiveHubSetupConfig(cfg hub.InitConfig) (hub.InitConfig, error) {
	activeCfg := cfg
	activeCfg.ApplyDefaults()

	storedCfg, found, err := loadHubSetupRuntimeConfig(cfg.RuntimeConfigPath)
	if err != nil {
		return activeCfg, err
	}
	if !found {
		return activeCfg, nil
	}

	if strings.TrimSpace(storedCfg.BindToken) == "" {
		storedCfg.BindToken = strings.TrimSpace(activeCfg.BindToken)
	}
	if strings.TrimSpace(storedCfg.AgentToken) == "" {
		storedCfg.AgentToken = strings.TrimSpace(activeCfg.AgentToken)
	}
	if strings.TrimSpace(storedCfg.Handle) == "" {
		storedCfg.Handle = strings.TrimSpace(activeCfg.Handle)
	}
	storedCfg.Profile = mergeProfileConfig(storedCfg.Profile, activeCfg.Profile)
	if strings.TrimSpace(storedCfg.AgentHarness) == "" {
		storedCfg.AgentHarness = strings.TrimSpace(activeCfg.AgentHarness)
	}
	if strings.TrimSpace(storedCfg.AgentCommand) == "" {
		storedCfg.AgentCommand = strings.TrimSpace(activeCfg.AgentCommand)
	}
	if strings.TrimSpace(storedCfg.BaseURL) == "" && strings.TrimSpace(activeCfg.BaseURL) != "" {
		storedCfg.BaseURL = strings.TrimSpace(activeCfg.BaseURL)
	}
	storedCfg.ApplyDefaults()
	return storedCfg, nil
}

func configureHubSetup(ctx context.Context, cfg hub.InitConfig, req hubui.HubSetupRequest, applyLive func(context.Context, hub.InitConfig) error) (hubui.HubSetupState, error) {
	state := currentHubSetupState(cfg)
	state.AgentMode = normalizeHubSetupMode(req.AgentMode)
	state.TokenType = hubSetupTokenTypeForMode(state.AgentMode)
	state.Region = normalizeHubSetupRegion(req.Region)
	state.Handle = strings.TrimSpace(req.Handle)
	state.Profile.ProfileText = strings.TrimSpace(req.Profile.ProfileText)
	state.Profile.DisplayName = strings.TrimSpace(req.Profile.DisplayName)
	state.Profile.Emoji = strings.TrimSpace(req.Profile.Emoji)
	state.Onboarding = hubui.DefaultHubSetupOnboarding(state.AgentMode)
	hubSetupMarkStep(&state, "bind", "current", "")
	bindError := func(err error) (hubui.HubSetupState, error) {
		hubSetupMarkStep(&state, "bind", "error", err.Error())
		return state, err
	}

	token := strings.TrimSpace(req.Token)

	activeCfg, err := effectiveHubSetupConfig(cfg)
	if err != nil {
		return bindError(fmt.Errorf("load runtime config: %w", err))
	}
	activeCfg.BaseURL = hubSetupBaseURL(activeCfg.BaseURL, state.Region)

	useSavedCredentials := token == ""
	if !useSavedCredentials {
		if err := validateHubSetupToken(token); err != nil {
			return bindError(err)
		}
	}
	if useSavedCredentials {
		if strings.TrimSpace(activeCfg.AgentToken) == "" && strings.TrimSpace(activeCfg.BindToken) == "" {
			return bindError(fmt.Errorf("%s token is required", state.TokenType))
		}
	} else {
		activeCfg.BindToken = ""
		activeCfg.AgentToken = ""
		if state.AgentMode == "new" {
			activeCfg.BindToken = token
		} else {
			activeCfg.AgentToken = token
		}
	}
	if strings.TrimSpace(activeCfg.AgentHarness) == "" {
		return bindError(fmt.Errorf("agent harness is not configured; complete agent selection in the UI"))
	}

	client := hub.NewAPIClient(activeCfg.BaseURL)
	resolvedToken, err := client.ResolveAgentToken(ctx, activeCfg)
	if err != nil {
		hubSetupMarkStep(&state, "bind", "error", err.Error())
		return state, fmt.Errorf("resolve hub token: %w", err)
	}
	hubSetupMarkStep(&state, "bind", "completed", "")
	hubSetupMarkStep(&state, "work_bind", "completed", "Molten Hub credentials verified.")

	finalCfg := activeCfg
	if baseURL := strings.TrimRight(strings.TrimSpace(client.BaseURL), "/"); baseURL != "" {
		finalCfg.BaseURL = baseURL
	}
	finalCfg.BaseURL = hubSetupPersistedBaseURL(finalCfg.BaseURL, state.Region)
	finalCfg.AgentToken = resolvedToken
	profileUpdateRequested := strings.TrimSpace(state.Handle) != strings.TrimSpace(activeCfg.Handle) ||
		strings.TrimSpace(state.Profile.ProfileText) != strings.TrimSpace(activeCfg.Profile.ProfileText) ||
		strings.TrimSpace(state.Profile.DisplayName) != strings.TrimSpace(activeCfg.Profile.DisplayName) ||
		strings.TrimSpace(state.Profile.Emoji) != strings.TrimSpace(activeCfg.Profile.Emoji)
	if profileUpdateRequested {
		finalCfg.Handle = state.Handle
		finalCfg.Profile.ProfileText = state.Profile.ProfileText
		finalCfg.Profile.DisplayName = state.Profile.DisplayName
		finalCfg.Profile.Emoji = state.Profile.Emoji
		hubSetupMarkStep(&state, "profile_set", "current", "")
		if err := client.SyncProfile(ctx, resolvedToken, finalCfg); err != nil {
			hubSetupMarkStep(&state, "profile_set", "error", err.Error())
			return state, fmt.Errorf("sync hub profile: %w", err)
		}
		hubSetupMarkStep(&state, "profile_set", "completed", "Molten Hub profile saved.")
	} else {
		_ = client.UpdateAgentStatus(ctx, resolvedToken, "online")
		hubSetupMarkStep(&state, "profile_set", "completed", "Existing Molten Hub profile confirmed.")
	}
	hubSetupMarkStep(&state, "work_activate", "current", "")

	// Always read back profile after token resolution so config initialization
	// stays accurate for first-time binds and re-binds.
	profile, err := client.AgentProfile(ctx, resolvedToken)
	if err != nil {
		hubSetupMarkStep(&state, "work_activate", "error", err.Error())
		return state, fmt.Errorf("load hub profile: %w", err)
	}
	if profileUpdateRequested {
		if strings.TrimSpace(finalCfg.Handle) == "" {
			finalCfg.Handle = strings.TrimSpace(profile.Handle)
		}
		finalCfg.Profile = mergeProfileConfig(finalCfg.Profile, profile.Profile)
	} else {
		if strings.TrimSpace(profile.Handle) != "" {
			finalCfg.Handle = strings.TrimSpace(profile.Handle)
		}
		finalCfg.Profile = mergeProfileConfig(profile.Profile, finalCfg.Profile)
	}
	finalCfg.ApplyDefaults()

	if err := hub.SaveRuntimeConfigHubSettings(cfg.RuntimeConfigPath, finalCfg, resolvedToken); err != nil {
		hubSetupMarkStep(&state, "work_activate", "error", err.Error())
		return state, fmt.Errorf("save runtime config: %w", err)
	}

	state.Configured = true
	state.Region = hubSetupRegionForBaseURL(finalCfg.BaseURL)
	state.Handle = strings.TrimSpace(finalCfg.Handle)
	state.Profile.ProfileText = strings.TrimSpace(finalCfg.Profile.ProfileText)
	state.Profile.DisplayName = strings.TrimSpace(finalCfg.Profile.DisplayName)
	state.Profile.Emoji = strings.TrimSpace(finalCfg.Profile.Emoji)
	state.Message = "Molten Hub setup saved and applied live."
	state.NeedsRestart = false
	state.ActivationReady = true
	if applyLive != nil {
		if err := applyLive(ctx, finalCfg); err != nil {
			state.Message = fmt.Sprintf("Molten Hub setup was saved, but live apply failed: %v", err)
			hubSetupMarkStep(&state, "work_activate", "error", err.Error())
			return state, fmt.Errorf("apply live hub setup: %w", err)
		}
	}
	hubSetupMarkStep(&state, "work_activate", "completed", "Molten Hub activation confirmed.")
	state.OnboardingActive = false
	return state, nil
}

func connectHubSetup(ctx context.Context, cfg hub.InitConfig, applyLive func(context.Context, hub.InitConfig) error) (hubui.HubSetupState, error) {
	state := currentHubSetupState(cfg)
	if !state.Configured {
		return state, fmt.Errorf("molten hub is not configured")
	}
	if applyLive == nil {
		return state, fmt.Errorf("live hub connect is unavailable")
	}

	activeCfg, err := effectiveHubSetupConfig(cfg)
	if err != nil {
		return state, fmt.Errorf("load runtime config: %w", err)
	}
	if strings.TrimSpace(activeCfg.AgentToken) == "" && strings.TrimSpace(activeCfg.BindToken) == "" {
		return state, fmt.Errorf("saved Molten Hub credentials are unavailable")
	}
	if err := applyLive(ctx, activeCfg); err != nil {
		state.Message = fmt.Sprintf("Molten Hub connect failed: %v", err)
		return state, fmt.Errorf("apply live hub setup: %w", err)
	}

	state.Message = "Molten Hub connected."
	state.NeedsRestart = false
	state.ActivationReady = true
	return state, nil
}

func disconnectHubSetup(ctx context.Context, cfg hub.InitConfig, stopLive func(context.Context) error) (hubui.HubSetupState, error) {
	state := currentHubSetupState(cfg)
	if !state.Configured {
		return state, fmt.Errorf("molten hub is not configured")
	}
	if stopLive == nil {
		return state, fmt.Errorf("live hub disconnect is unavailable")
	}
	if err := stopLive(ctx); err != nil {
		state.Message = fmt.Sprintf("Molten Hub disconnect failed: %v", err)
		return state, fmt.Errorf("disconnect live hub: %w", err)
	}

	state.Message = "Molten Hub disconnected."
	state.NeedsRestart = false
	state.ActivationReady = false
	return state, nil
}

func hubSetupMarkStep(state *hubui.HubSetupState, stepID, status, detail string) {
	if state == nil {
		return
	}
	stepID = strings.TrimSpace(stepID)
	status = strings.TrimSpace(status)
	if stepID == "" || status == "" {
		return
	}
	state.OnboardingActive = status == "current"
	state.OnboardingStage = stepID
	for i := range state.Onboarding {
		if status == "current" && state.Onboarding[i].ID != stepID && state.Onboarding[i].Status == "current" {
			state.Onboarding[i].Status = "completed"
		}
		if status == "error" && state.Onboarding[i].ID != stepID && state.Onboarding[i].Status == "current" {
			state.Onboarding[i].Status = "completed"
		}
		if state.Onboarding[i].ID != stepID {
			continue
		}
		state.Onboarding[i].Status = status
		if strings.TrimSpace(detail) != "" {
			state.Onboarding[i].Detail = strings.TrimSpace(detail)
		}
		return
	}
}

func validateHubSetupToken(token string) error {
	token = strings.TrimSpace(token)
	switch {
	case len(token) < 40:
		return fmt.Errorf("token looks too short; expected roughly 40 or more characters")
	case len(token) > 128:
		return fmt.Errorf("token looks too long; expected roughly 128 or fewer characters")
	}

	for _, ch := range token {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			continue
		}
		return fmt.Errorf("token contains unsupported characters; use letters, numbers, '-' or '_'")
	}
	return nil
}

func normalizeHubSetupMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "new":
		return "new"
	default:
		return "existing"
	}
}

func normalizeHubSetupTokenType(tokenType string) string {
	switch strings.ToLower(strings.TrimSpace(tokenType)) {
	case "agent":
		return "agent"
	default:
		return "bind"
	}
}

func normalizeHubSetupRegion(region string) string {
	normalized := strings.ToLower(strings.TrimSpace(region))
	locations := currentHubSetupLocations()
	if location, ok := hubSetupLocationByKey(locations, normalized); ok {
		return location.Key
	}
	normalized = hub.NormalizeHubRegion(normalized)
	if location, ok := hubSetupLocationByKey(locations, normalized); ok {
		return location.Key
	}
	if len(locations) > 0 {
		return locations[0].Key
	}
	return normalized
}

func hubSetupRegionForBaseURL(baseURL string) string {
	return hubSetupRegionForBaseURLWithLocations(baseURL, currentHubSetupLocations())
}

func hubSetupRegionForBaseURLWithLocations(baseURL string, locations []hubSetupLocation) string {
	if location, ok := hubSetupLocationForBaseURL(locations, baseURL); ok {
		return location.Key
	}
	return normalizeHubSetupRegion(hub.HubRegionFromBaseURL(baseURL))
}

func hubSetupBaseURL(baseURL, region string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	locations := currentHubSetupLocations()
	region = normalizeHubSetupRegion(region)

	selected, ok := hubSetupLocationByKey(locations, region)
	if !ok {
		if len(locations) > 0 {
			selected = locations[0]
			ok = true
		}
	}

	if baseURL != "" {
		if _, matched := hubSetupLocationForBaseURL(locations, baseURL); matched {
			if ok {
				if selectedBaseURL := hubSetupLocationBaseURL(selected); selectedBaseURL != "" {
					return selectedBaseURL
				}
			}
			return hub.HubBaseURLForRegion(region)
		}
		if hub.AllowNonMoltenHubBaseURL() {
			if err := hub.ValidateHubBaseURLStrict(baseURL); err != nil {
				return baseURL
			}
		}
	}

	if ok {
		if selectedBaseURL := hubSetupLocationBaseURL(selected); selectedBaseURL != "" {
			return selectedBaseURL
		}
	}
	return hub.HubBaseURLForRegion(region)
}

func hubSetupPersistedBaseURL(baseURL, region string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	locations := currentHubSetupLocations()
	if location, ok := hubSetupLocationForBaseURL(locations, baseURL); ok {
		if selectedBaseURL := hubSetupLocationBaseURL(location); selectedBaseURL != "" {
			return selectedBaseURL
		}
	}
	selected, ok := hubSetupLocationByKey(locations, region)
	if !ok {
		selected, _ = hubSetupLocationByKey(locations, hub.HubRegionFromBaseURL(baseURL))
	}
	if selectedBaseURL := hubSetupLocationBaseURL(selected); selectedBaseURL != "" {
		return selectedBaseURL
	}
	return hub.HubBaseURLForRegion(region)
}

func hubSetupTokenTypeForMode(mode string) string {
	if normalizeHubSetupMode(mode) == "new" {
		return normalizeHubSetupTokenType("bind")
	}
	return normalizeHubSetupTokenType("agent")
}

func mergeProfileConfig(primary, secondary hub.ProfileConfig) hub.ProfileConfig {
	if strings.TrimSpace(primary.DisplayName) == "" {
		primary.DisplayName = strings.TrimSpace(secondary.DisplayName)
	}
	if strings.TrimSpace(primary.Emoji) == "" {
		primary.Emoji = strings.TrimSpace(secondary.Emoji)
	}
	if strings.TrimSpace(primary.ProfileText) == "" {
		primary.ProfileText = strings.TrimSpace(secondary.ProfileText)
	}
	if strings.TrimSpace(primary.LLM) == "" {
		primary.LLM = strings.TrimSpace(secondary.LLM)
	}
	if strings.TrimSpace(primary.Harness) == "" {
		primary.Harness = strings.TrimSpace(secondary.Harness)
	}
	if len(primary.Skills) == 0 && len(secondary.Skills) > 0 {
		primary.Skills = append([]string(nil), secondary.Skills...)
	}
	return primary
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
