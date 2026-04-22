package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/execx"
	"github.com/Molten-Bot/moltenhub-code/internal/hub"
	"github.com/Molten-Bot/moltenhub-code/internal/hubui"
)

const githubTokenPasteConfigureMessage = "GitHub token is required."
const githubTokenValidationTimeout = 12 * time.Second

var githubTokenValidationMu sync.Mutex

func readyAgentAuthState() hubui.AgentAuthState {
	return hubui.AgentAuthState{
		Required: false,
		Ready:    true,
		State:    "ready",
		Message:  "Agent auth is ready.",
	}
}

type configurableAgentAuthState struct {
	required             bool
	ready                bool
	state                string
	message              string
	configureCommand     string
	configurePlaceholder string
	configureOptions     []hubui.AgentAuthOption
	updatedAt            time.Time
}

func (s *configurableAgentAuthState) touch() {
	s.updatedAt = time.Now().UTC()
}

func (s *configurableAgentAuthState) setNeedsConfigure(message string) {
	s.required = true
	s.ready = false
	s.state = "needs_configure"
	s.message = strings.TrimSpace(message)
	s.touch()
}

func (s *configurableAgentAuthState) setError(message string) {
	s.required = true
	s.ready = false
	s.state = "error"
	s.message = strings.TrimSpace(message)
	s.touch()
}

func (s *configurableAgentAuthState) applySnapshot(snapshot hubui.AgentAuthState) {
	s.required = snapshot.Required
	s.ready = snapshot.Ready
	s.state = strings.TrimSpace(snapshot.State)
	s.message = strings.TrimSpace(snapshot.Message)
	s.configureCommand = strings.TrimSpace(snapshot.ConfigureCommand)
	s.configurePlaceholder = strings.TrimSpace(snapshot.ConfigurePlaceholder)
	s.configureOptions = append([]hubui.AgentAuthOption(nil), snapshot.ConfigureOptions...)
	s.touch()
}

func (s *configurableAgentAuthState) snapshot(harness string) hubui.AgentAuthState {
	return hubui.AgentAuthState{
		Harness:              harness,
		Required:             s.required,
		Ready:                s.ready,
		State:                strings.TrimSpace(s.state),
		Message:              strings.TrimSpace(s.message),
		ConfigureCommand:     strings.TrimSpace(s.configureCommand),
		ConfigurePlaceholder: strings.TrimSpace(s.configurePlaceholder),
		ConfigureOptions:     append([]hubui.AgentAuthOption(nil), s.configureOptions...),
		UpdatedAt:            s.updatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func githubTokenNeedsConfigureState(harness, message string) hubui.AgentAuthState {
	message = firstNonEmptyString(
		message,
		"GitHub token is required.",
	)
	return hubui.AgentAuthState{
		Harness:              harness,
		Required:             true,
		Ready:                false,
		State:                "needs_configure",
		Message:              message,
		ConfigureCommand:     claudeGitHubConfigureCommand,
		ConfigurePlaceholder: claudeGitHubConfigurePlaceholder,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func firstConfiguredGitHubToken(runtimeConfigPath string, initCfg hub.InitConfig) (value string, source string) {
	if persisted := hub.ReadRuntimeConfigString(runtimeConfigPath, "github_token", "githubToken", "GITHUB_TOKEN"); persisted != "" {
		return persisted, "runtime config"
	}
	if init := strings.TrimSpace(initCfg.GitHubToken); init != "" {
		return init, "init config"
	}
	if env := strings.TrimSpace(os.Getenv("GH_TOKEN")); env != "" {
		return env, "environment"
	}
	if env := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); env != "" {
		return env, "environment"
	}
	return "", ""
}

func setGitHubTokenEnvironment(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("github token is required")
	}
	if err := os.Setenv("GITHUB_TOKEN", token); err != nil {
		return err
	}
	if err := os.Setenv("GH_TOKEN", token); err != nil {
		return err
	}
	return nil
}

func isLikelyGitHubToken(value string) bool {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func githubTokenRequirementState(harness, runtimeConfigPath string, initCfg hub.InitConfig) (bool, hubui.AgentAuthState) {
	githubToken, _ := firstConfiguredGitHubToken(runtimeConfigPath, initCfg)
	if strings.TrimSpace(githubToken) == "" {
		return true, githubTokenNeedsConfigureState(harness, "")
	}
	if err := setGitHubTokenEnvironment(githubToken); err != nil {
		return true, githubTokenNeedsConfigureState(harness, fmt.Sprintf("set github token env: %v", err))
	}
	return false, hubui.AgentAuthState{}
}

func applyGitHubTokenRequirementState(state *configurableAgentAuthState, harness, runtimeConfigPath string, initCfg hub.InitConfig) (string, bool) {
	githubToken, _ := firstConfiguredGitHubToken(runtimeConfigPath, initCfg)
	if strings.TrimSpace(githubToken) == "" {
		state.applySnapshot(githubTokenNeedsConfigureState(harness, ""))
		return "", true
	}
	if err := setGitHubTokenEnvironment(githubToken); err != nil {
		state.applySnapshot(githubTokenNeedsConfigureState(harness, fmt.Sprintf("set github token env: %v", err)))
		return "", true
	}
	return strings.TrimSpace(githubToken), false
}

func configureGitHubToken(
	ctx context.Context,
	harness, runtimeConfigPath string,
	initCfg hub.InitConfig,
	runner execx.Runner,
	rawInput, requiredMessage string,
) (string, hubui.AgentAuthState, error) {
	token := strings.TrimSpace(rawInput)
	requiredMessage = firstNonEmptyString(requiredMessage, githubTokenPasteConfigureMessage)
	if token == "" {
		state := githubTokenNeedsConfigureState(harness, requiredMessage)
		return "", state, fmt.Errorf("github token is required")
	}
	if err := validateGitHubToken(ctx, runner, token); err != nil {
		state := githubTokenNeedsConfigureState(harness, err.Error())
		return "", state, err
	}

	if err := hub.SaveRuntimeConfigGitHubToken(runtimeConfigPath, initCfg, token); err != nil {
		state := githubTokenNeedsConfigureState(harness, fmt.Sprintf("save github token: %v", err))
		return "", state, err
	}
	if err := setGitHubTokenEnvironment(token); err != nil {
		state := githubTokenNeedsConfigureState(harness, fmt.Sprintf("set github token env: %v", err))
		return "", state, err
	}

	return token, hubui.AgentAuthState{}, nil
}

func configureGitHubTokenAndApply(
	ctx context.Context,
	harness, runtimeConfigPath string,
	initCfg hub.InitConfig,
	runner execx.Runner,
	rawInput string,
	onFailure func(hubui.AgentAuthState, error) (hubui.AgentAuthState, error),
	onSuccess func(string) (hubui.AgentAuthState, error),
) (hubui.AgentAuthState, error) {
	token, failureState, err := configureGitHubToken(
		ctx,
		harness,
		runtimeConfigPath,
		initCfg,
		runner,
		rawInput,
		githubTokenPasteConfigureMessage,
	)
	if err != nil {
		if onFailure != nil {
			return onFailure(failureState, err)
		}
		return failureState, err
	}
	if onSuccess == nil {
		return hubui.AgentAuthState{}, nil
	}
	return onSuccess(token)
}

func validateGitHubToken(ctx context.Context, runner execx.Runner, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("github token is required")
	}
	if runner == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, githubTokenValidationTimeout)
	defer cancel()

	if err := withTemporaryGitHubTokenEnvironment(token, func() error {
		_, runErr := runner.Run(probeCtx, execx.Command{Name: "gh", Args: []string{"auth", "status"}})
		return runErr
	}); err != nil {
		return fmt.Errorf("validate github token: %w", err)
	}
	return nil
}

func withTemporaryGitHubTokenEnvironment(token string, run func() error) error {
	if run == nil {
		return nil
	}

	githubTokenValidationMu.Lock()
	defer githubTokenValidationMu.Unlock()

	previousGH, hadGH := os.LookupEnv("GH_TOKEN")
	previousGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")

	restore := func() error {
		var restoreErr error
		if hadGH {
			restoreErr = errors.Join(restoreErr, os.Setenv("GH_TOKEN", previousGH))
		} else {
			restoreErr = errors.Join(restoreErr, os.Unsetenv("GH_TOKEN"))
		}
		if hadGitHub {
			restoreErr = errors.Join(restoreErr, os.Setenv("GITHUB_TOKEN", previousGitHub))
		} else {
			restoreErr = errors.Join(restoreErr, os.Unsetenv("GITHUB_TOKEN"))
		}
		return restoreErr
	}

	if err := os.Setenv("GH_TOKEN", token); err != nil {
		return fmt.Errorf("set temporary GH_TOKEN: %w", err)
	}
	if err := os.Setenv("GITHUB_TOKEN", token); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("set temporary GITHUB_TOKEN: %v (restore env failed: %v)", err, restoreErr)
		}
		return fmt.Errorf("set temporary GITHUB_TOKEN: %w", err)
	}

	runErr := run()
	restoreErr := restore()
	if runErr != nil && restoreErr != nil {
		return fmt.Errorf("%v (restore env failed: %v)", runErr, restoreErr)
	}
	if runErr != nil {
		return runErr
	}
	if restoreErr != nil {
		return fmt.Errorf("restore github token env: %w", restoreErr)
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func decodeJSONStrict(raw string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON data")
		}
		return err
	}
	return nil
}

func decodeJSONOrWrappedString(raw string, dst any) error {
	if err := decodeJSONStrict(raw, dst); err == nil {
		return nil
	} else {
		var wrapped string
		if wrappedErr := decodeJSONStrict(raw, &wrapped); wrappedErr == nil {
			return decodeJSONOrWrappedString(wrapped, dst)
		}
		return err
	}
}
