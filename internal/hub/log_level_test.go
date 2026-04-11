package hub

import "testing"

func TestParseLogLevelSupportsCanonicalValuesAndAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   string
		want  LogLevel
		canon string
	}{
		{name: "error", raw: "error", want: LogLevelError, canon: "error"},
		{name: "warn", raw: "warning", want: LogLevelWarn, canon: "warn"},
		{name: "info", raw: "INFO", want: LogLevelInfo, canon: "info"},
		{name: "debug", raw: "dbg", want: LogLevelDebug, canon: "debug"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLogLevel(tt.raw)
			if err != nil {
				t.Fatalf("ParseLogLevel(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseLogLevel(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			if got.String() != tt.canon {
				t.Fatalf("String() = %q, want %q", got.String(), tt.canon)
			}
		})
	}
}

func TestParseLogLevelRejectsUnknown(t *testing.T) {
	t.Parallel()

	if _, err := ParseLogLevel("chatty"); err == nil {
		t.Fatal("ParseLogLevel(chatty) error = nil, want non-nil")
	}
}

func TestLogLevelAllows(t *testing.T) {
	t.Parallel()

	if !LogLevelInfo.Allows(LogLevelWarn) {
		t.Fatal("LogLevelInfo should allow warn")
	}
	if LogLevelWarn.Allows(LogLevelInfo) {
		t.Fatal("LogLevelWarn should not allow info")
	}
}
