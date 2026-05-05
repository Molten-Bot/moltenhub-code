package hub

import "testing"

func TestPublishResultA2ARoutingMetadata(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"status":       "completed",
		"to_agent_uri": "https://na.hub.molten.bot/agents/target",
	}
	got := publishResultA2ARoutingMetadata(payload)
	if got["to_agent_uri"] != "https://na.hub.molten.bot/agents/target" {
		t.Fatalf("publishResultA2ARoutingMetadata() = %#v, want to_agent_uri", got)
	}

	if got := publishResultA2ARoutingMetadata(map[string]any{"status": "ok"}); got != nil {
		t.Fatalf("publishResultA2ARoutingMetadata(unrouted) = %#v, want nil", got)
	}
}
