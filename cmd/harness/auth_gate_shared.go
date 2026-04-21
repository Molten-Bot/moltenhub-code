package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jef/moltenhub-code/internal/hub"
	"github.com/jef/moltenhub-code/internal/hubui"
)

const githubTokenPasteConfigureMessage = "GitHub token is required."

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
	if env := strings.TrimSpace(os.Getenv("GH_TOKEN")); env != "" {
		return env, "environment"
	}
	if env := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); env != "" {
		return env, "environment"
	}
	if init := strings.TrimSpace(initCfg.GitHubToken); init != "" {
		return init, "init config"
	}
	if persisted := hub.ReadRuntimeConfigString(runtimeConfigPath, "github_token", "githubToken", "GITHUB_TOKEN"); persisted != "" {
		return persisted, "runtime config"
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

func configureGitHubToken(
	harness, runtimeConfigPath string,
	initCfg hub.InitConfig,
	rawInput, requiredMessage string,
) (string, hubui.AgentAuthState, error) {
	token := strings.TrimSpace(rawInput)
	requiredMessage = firstNonEmptyString(requiredMessage, githubTokenPasteConfigureMessage)
	if token == "" {
		state := githubTokenNeedsConfigureState(harness, requiredMessage)
		return "", state, fmt.Errorf("github token is required")
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
