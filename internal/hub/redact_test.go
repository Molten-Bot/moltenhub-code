package hub

import (
	"strings"
	"testing"
)

func TestRedactSensitiveLogText(t *testing.T) {
	t.Parallel()

	input := `bind_token=bind_123 token=agent_123 {"agent_token":"agent_abc","authorization":"Bearer secret_xyz"} Authorization: Bearer top_secret`
	output := redactSensitiveLogText(input)

	for _, secret := range []string{"bind_123", "agent_123", "agent_abc", "secret_xyz", "top_secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("redaction failed, output still contains %q: %q", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in output: %q", output)
	}
}

func TestRedactSensitiveLogTextLeavesNonSensitiveInputUntouched(t *testing.T) {
	t.Parallel()

	input := "dispatch status=ok request_id=req-1 stage=pr workspace=/tmp/work"
	if got := redactSensitiveLogText(input); got != input {
		t.Fatalf("redactSensitiveLogText() = %q, want %q", got, input)
	}
}
