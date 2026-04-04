package hub

import (
	"regexp"
	"strings"
)

var (
	redactJSONFieldPattern = regexp.MustCompile(`(?i)("?(?:bind_token|agent_token|access_token|bearer_token|token|authorization)"?\s*[:=]\s*"?)([^",]+)("?)`)
	redactBearerPattern    = regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9._-]+)`)
	redactQueryPattern     = regexp.MustCompile(`(?i)((?:bind_token|agent_token|access_token|bearer_token|token)=)([^&\s]+)`)
)

func redactSensitiveLogText(value string) string {
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if !strings.Contains(lower, "token") && !strings.Contains(lower, "authorization") && !strings.Contains(lower, "bearer") {
		return value
	}
	value = redactQueryPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactJSONFieldPattern.ReplaceAllString(value, `${1}[REDACTED]${3}`)
	return value
}
