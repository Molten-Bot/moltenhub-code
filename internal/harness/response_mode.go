package harness

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/jef/moltenhub-code/internal/config"
)

const (
	cavemanSkillRelativePath = "skills/caveman/SKILL.md"
	workspaceSkillsDir       = "/workspace/skills"
	runtimeSkillsDir         = "/opt/moltenhub/skills"
)

func withResponseModePrompt(prompt, responseMode string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	responseMode = config.NormalizeResponseMode(responseMode)
	if responseMode == config.DisabledResponseMode {
		return prompt, nil
	}

	skillBody, err := loadResponseModeSkillBody(responseMode)
	if err != nil {
		return "", err
	}

	block := strings.TrimSpace(strings.Join([]string{
		"Caveman response mode is enabled for this run only.",
		fmt.Sprintf("Selected intensity: %s.", cavemanIntensityLabel(responseMode)),
		"Apply these response-style instructions to all non-code, user-facing prose unless safety or clarity requires normal prose:",
		skillBody,
	}, "\n\n"))
	if prompt == "" {
		return block, nil
	}
	return block + "\n\n" + prompt, nil
}

func loadResponseModeSkillBody(responseMode string) (string, error) {
	path, err := resolveResponseModeSkillPath()
	if err != nil {
		return "", fmt.Errorf("load %s instructions: %w", responseMode, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	body := stripSkillFrontMatter(string(data))
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("skill file %s is empty", path)
	}
	return body, nil
}

func resolveResponseModeSkillPath() (string, error) {
	candidates := responseModeSkillPathCandidates()
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("unable to find %s", cavemanSkillRelativePath)
}

func responseModeSkillPathCandidates() []string {
	candidates := []string{
		cavemanSkillRelativePath,
		filepath.Join(workspaceSkillsDir, "caveman", "SKILL.md"),
		filepath.Join(runtimeSkillsDir, "caveman", "SKILL.md"),
	}

	if seedPath := strings.TrimSpace(os.Getenv("HARNESS_AGENTS_SEED_PATH")); seedPath != "" {
		baseDir := filepath.Dir(filepath.Dir(seedPath))
		candidates = append(candidates, filepath.Join(baseDir, "skills", "caveman", "SKILL.md"))
	}

	if _, file, _, ok := goruntime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		candidates = append(candidates, filepath.Join(repoRoot, cavemanSkillRelativePath))
	}

	seen := make(map[string]struct{}, len(candidates))
	deduped := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		deduped = append(deduped, candidate)
	}
	return deduped
}

func stripSkillFrontMatter(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return content
	}

	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return content
}

func cavemanIntensityLabel(responseMode string) string {
	switch config.NormalizeResponseMode(responseMode) {
	case "caveman-lite":
		return "lite"
	case "caveman-ultra":
		return "ultra"
	case "caveman-wenyan-lite":
		return "wenyan-lite"
	case "caveman-wenyan-full":
		return "wenyan-full"
	case "caveman-wenyan-ultra":
		return "wenyan-ultra"
	default:
		return "full"
	}
}
