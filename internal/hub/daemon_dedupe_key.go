package hub

import (
	"strings"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

func dedupeKeyForRunConfig(cfg config.Config) string {
	if len(cfg.RepoList()) == 0 || strings.TrimSpace(cfg.Prompt) == "" {
		return ""
	}
	return config.DedupeKey(cfg)
}
