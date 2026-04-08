package hub

import (
	"regexp"
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
	if !containsSensitiveMarker(value) {
		return value
	}
	value = redactQueryPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactBearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = redactJSONFieldPattern.ReplaceAllString(value, `${1}[REDACTED]${3}`)
	return value
}

func containsSensitiveMarker(value string) bool {
	return containsASCIIFold(value, "token") ||
		containsASCIIFold(value, "authorization") ||
		containsASCIIFold(value, "bearer")
}

func containsASCIIFold(value, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(value) < len(needle) {
		return false
	}
	limit := len(value) - len(needle)
	for i := 0; i <= limit; i++ {
		if hasASCIIFoldPrefix(value[i:], needle) {
			return true
		}
	}
	return false
}

func hasASCIIFoldPrefix(value, prefix string) bool {
	if len(value) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if lowerASCII(value[i]) != prefix[i] {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
