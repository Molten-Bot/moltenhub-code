package main

import (
	"fmt"
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
