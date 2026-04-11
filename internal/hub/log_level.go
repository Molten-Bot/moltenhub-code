package hub

import (
	"fmt"
	"strings"
)

const (
	logLevelErrorValue = "error"
	logLevelWarnValue  = "warn"
	logLevelInfoValue  = "info"
	logLevelDebugValue = "debug"
)

// DefaultLogLevel is used when runtime config omits log_level.
const DefaultLogLevel = logLevelInfoValue

// LogLevel controls log emission severity filtering.
type LogLevel int

const (
	LogLevelDebug LogLevel = iota
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// ParseLogLevel returns a canonical level enum for a configured value.
func ParseLogLevel(raw string) (LogLevel, error) {
	switch NormalizeLogLevel(raw) {
	case logLevelErrorValue:
		return LogLevelError, nil
	case logLevelWarnValue:
		return LogLevelWarn, nil
	case logLevelInfoValue:
		return LogLevelInfo, nil
	case logLevelDebugValue:
		return LogLevelDebug, nil
	default:
		return LogLevelInfo, fmt.Errorf("log_level must be one of error, warn, info, debug")
	}
}

// NormalizeLogLevel canonicalizes supported spellings/aliases.
func NormalizeLogLevel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "err", "error":
		return logLevelErrorValue
	case "warning", "warn":
		return logLevelWarnValue
	case "information", "info":
		return logLevelInfoValue
	case "dbg", "debug":
		return logLevelDebugValue
	default:
		return ""
	}
}

// String returns the canonical lower-case string.
func (l LogLevel) String() string {
	switch l {
	case LogLevelError:
		return logLevelErrorValue
	case LogLevelWarn:
		return logLevelWarnValue
	case LogLevelInfo:
		return logLevelInfoValue
	default:
		return logLevelDebugValue
	}
}

// Allows reports whether an event at eventLevel should be emitted.
func (l LogLevel) Allows(eventLevel LogLevel) bool {
	return eventLevel >= l
}
