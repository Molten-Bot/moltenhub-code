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

func TestRedactSensitiveLogTextHandlesEmptyInput(t *testing.T) {
	t.Parallel()

	if got := redactSensitiveLogText(""); got != "" {
		t.Fatalf("redactSensitiveLogText(empty) = %q, want empty", got)
	}
}

func TestRedactSensitiveLogTextLeavesNonSensitiveInputUntouched(t *testing.T) {
	t.Parallel()

	input := "dispatch status=ok request_id=req-1 stage=pr workspace=/tmp/work"
	if got := redactSensitiveLogText(input); got != input {
		t.Fatalf("redactSensitiveLogText() = %q, want %q", got, input)
	}
}

func TestRedactSensitiveLogTextDetectsUppercaseMarkers(t *testing.T) {
	t.Parallel()

	input := `AUTHORIZATION=Bearer SECRET_TOKEN`
	output := redactSensitiveLogText(input)
	if strings.Contains(output, "SECRET_TOKEN") {
		t.Fatalf("redaction failed for uppercase marker: %q", output)
	}
}

func TestContainsSensitiveMarker(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"token=abc", "Authorization: Bearer abc", "BEARER xyz"} {
		if !containsSensitiveMarker(input) {
			t.Fatalf("containsSensitiveMarker(%q) = false, want true", input)
		}
	}
	if containsSensitiveMarker("dispatch status=ok") {
		t.Fatal("containsSensitiveMarker(non-sensitive) = true, want false")
	}
}

func TestASCIIFoldHelpersEdgeCases(t *testing.T) {
	t.Parallel()

	if !containsASCIIFold("anything", "") {
		t.Fatal("containsASCIIFold(empty needle) = false, want true")
	}
	if containsASCIIFold("tok", "token") {
		t.Fatal("containsASCIIFold(short value) = true, want false")
	}
	if hasASCIIFoldPrefix("tok", "token") {
		t.Fatal("hasASCIIFoldPrefix(short value) = true, want false")
	}
}
