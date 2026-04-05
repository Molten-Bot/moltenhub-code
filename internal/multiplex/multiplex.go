package multiplex

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jef/moltenhub-code/internal/config"
	"github.com/jef/moltenhub-code/internal/execx"
	"github.com/jef/moltenhub-code/internal/harness"
)

type logFn func(string, ...any)

// SessionState describes the current state of one multiplexed task.
type SessionState string

const (
	SessionQueued    SessionState = "queued"
	SessionRunning   SessionState = "running"
	SessionOK        SessionState = "ok"
	SessionNoChanges SessionState = "no_changes"
	SessionError     SessionState = "error"
)

// Session captures status and output metadata for one config execution.
type Session struct {
	ID           string
	ConfigPath   string
	State        SessionState
	Stage        string
	StageStatus  string
	ExitCode     int
	Error        string
	WorkspaceDir string
	Branch       string
	PRURL        string
	NoChanges    bool
}

// Result is the aggregated outcome of a multiplex run.
type Result struct {
	Sessions []Session
}

// ExitCode returns the first non-zero exit code, if any.
func (r Result) ExitCode() int {
	for _, s := range r.Sessions {
		if s.ExitCode != harness.ExitSuccess {
			if s.ExitCode > 0 {
				return s.ExitCode
			}
			return 1
		}
	}
	return harness.ExitSuccess
}

type runSessionFn func(context.Context, config.Config, logFn) harness.Result

// Multiplexer runs many harness sessions concurrently.
type Multiplexer struct {
	Runner      execx.Runner
	MaxParallel int
	Logf        logFn
	RunSession  runSessionFn
}

// New returns a multiplexer with safe defaults.
func New(runner execx.Runner) Multiplexer {
	return Multiplexer{
		Runner:      runner,
		MaxParallel: 1,
		Logf:        func(string, ...any) {},
	}
}

// Run executes all configs with bounded parallelism.
func (m Multiplexer) Run(ctx context.Context, configPaths []string) Result {
	if len(configPaths) == 0 {
		return Result{}
	}
	if m.MaxParallel < 1 {
		m.MaxParallel = 1
	}
	if m.Logf == nil {
		m.Logf = func(string, ...any) {}
	}
	if m.RunSession == nil {
		runner := m.Runner
		m.RunSession = func(ctx context.Context, cfg config.Config, logf logFn) harness.Result {
			h := harness.New(runner)
			h.Logf = func(format string, args ...any) {
				logf(format, args...)
			}
			return h.Run(ctx, cfg)
		}
	}

	sessions := make([]Session, len(configPaths))
	for i, path := range configPaths {
		sessions[i] = Session{
			ID:          sessionID(i),
			ConfigPath:  path,
			State:       SessionQueued,
			Stage:       "queued",
			StageStatus: "queued",
		}
	}

	sem := make(chan struct{}, m.MaxParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := range configPaths {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				s := sessions[i]
				s.State = SessionError
				s.Stage = "context"
				s.StageStatus = "error"
				s.ExitCode = 1
				s.Error = ctx.Err().Error()
				sessions[i] = s
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			m.runOne(ctx, &mu, sessions, i, configPaths[i])
		}()
	}

	wg.Wait()
	return Result{Sessions: sessions}
}

func (m Multiplexer) runOne(ctx context.Context, mu *sync.Mutex, sessions []Session, idx int, configPath string) {
	sessionID := sessions[idx].ID

	mu.Lock()
	s := sessions[idx]
	s.State = SessionRunning
	s.Stage = "config"
	s.StageStatus = "start"
	sessions[idx] = s
	mu.Unlock()

	m.logf("session=%s state=running config=%s", sessionID, configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		mu.Lock()
		s := sessions[idx]
		s.State = SessionError
		s.Stage = "config"
		s.StageStatus = "error"
		s.ExitCode = harness.ExitConfig
		s.Error = err.Error()
		sessions[idx] = s
		mu.Unlock()

		m.logf("session=%s stage=config status=error err=%q", sessionID, err)
		return
	}

	logf := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		m.logf("session=%s %s", sessionID, line)
		stage, status, ok := parseStageStatus(line)
		if !ok {
			return
		}

		mu.Lock()
		s := sessions[idx]
		if stage != "" {
			s.Stage = stage
		}
		if status != "" {
			s.StageStatus = status
			if status == "error" {
				s.State = SessionError
			}
		}
		sessions[idx] = s
		mu.Unlock()
	}

	res := m.RunSession(ctx, cfg, logf)

	mu.Lock()
	s = sessions[idx]
	s.ExitCode = res.ExitCode
	s.WorkspaceDir = res.WorkspaceDir
	s.Branch = res.Branch
	s.PRURL = res.PRURL
	s.NoChanges = res.NoChanges

	if res.Err != nil {
		s.State = SessionError
		s.Error = res.Err.Error()
		if s.StageStatus == "" {
			s.StageStatus = "error"
		}
		if s.Stage == "" {
			s.Stage = "unknown"
		}
	} else if res.NoChanges {
		s.State = SessionNoChanges
		s.Stage = "git"
		s.StageStatus = "no_changes"
	} else {
		s.State = SessionOK
		if s.Stage == "" {
			s.Stage = "pr"
		}
		if s.StageStatus == "" {
			s.StageStatus = "ok"
		}
	}
	sessions[idx] = s
	mu.Unlock()

	if res.Err != nil {
		m.logf("session=%s state=error exit_code=%d err=%q", sessionID, res.ExitCode, res.Err)
		return
	}
	if res.NoChanges {
		m.logf(
			"session=%s state=no_changes exit_code=%d workspace=%s branch=%s",
			sessionID,
			res.ExitCode,
			res.WorkspaceDir,
			res.Branch,
		)
		return
	}
	m.logf(
		"session=%s state=ok exit_code=%d workspace=%s branch=%s pr_url=%s",
		sessionID,
		res.ExitCode,
		res.WorkspaceDir,
		res.Branch,
		res.PRURL,
	)
}

func (m Multiplexer) logf(format string, args ...any) {
	m.Logf(format, args...)
}

func parseStageStatus(line string) (stage string, status string, ok bool) {
	for _, field := range strings.Fields(line) {
		k, v, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch k {
		case "stage":
			stage = v
		case "status":
			status = v
		}
	}
	return stage, status, stage != "" || status != ""
}

func sessionID(index int) string {
	return fmt.Sprintf("task-%03d", index+1)
}
