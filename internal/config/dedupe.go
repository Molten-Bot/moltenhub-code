package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// DedupeKey returns the stable payload used to identify duplicate task submissions.
func DedupeKey(cfg Config) string {
	baseBranch := normalizeBranchRefForDeduper(cfg.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}

	repos := normalizeRepoListForDeduper(cfg.RepoList())
	targetSubdir := normalizeTargetSubdirForDeduper(cfg.TargetSubdir)
	payload := struct {
		Repos        []string `json:"repos"`
		BaseBranch   string   `json:"baseBranch"`
		TargetSubdir string   `json:"targetSubdir"`
		AgentHarness string   `json:"agentHarness,omitempty"`
		AgentCommand string   `json:"agentCommand,omitempty"`
		PromptHash   string   `json:"promptHash"`
	}{
		Repos:        repos,
		BaseBranch:   baseBranch,
		TargetSubdir: targetSubdir,
		AgentHarness: strings.ToLower(strings.TrimSpace(cfg.AgentHarness)),
		AgentCommand: strings.TrimSpace(cfg.AgentCommand),
		PromptHash:   promptHashForDeduper(cfg.Prompt),
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizeBranchRefForDeduper(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")
	return branch
}

func normalizeRepoListForDeduper(repos []string) []string {
	if len(repos) == 0 {
		return nil
	}
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

func normalizeTargetSubdirForDeduper(subdir string) string {
	subdir = strings.TrimSpace(subdir)
	if subdir == "" {
		return "."
	}
	clean := filepath.Clean(subdir)
	if clean == "" {
		return "."
	}
	return clean
}

func promptHashForDeduper(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}
