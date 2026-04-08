package agentruntime

import "testing"

func TestDisplayNameFallsBackForUnknownHarness(t *testing.T) {
	t.Parallel()

	if got := DisplayName("unknown-runtime"); got != "Codex" {
		t.Fatalf("DisplayName(unknown) = %q, want Codex", got)
	}
}
