package hub

import "testing"

func TestWebsocketURLFromHTTPSBase(t *testing.T) {
	t.Parallel()

	got, err := WebsocketURL("https://na.hub.molten.bot/v1", "main")
	if err != nil {
		t.Fatalf("WebsocketURL() error = %v", err)
	}
	want := "wss://na.hub.molten.bot/v1/openclaw/messages/ws?sessionKey=main&session_key=main"
	if got != want {
		t.Fatalf("WebsocketURL() = %q, want %q", got, want)
	}
}

func TestExtractTokenFromJSONNested(t *testing.T) {
	t.Parallel()

	body := []byte(`{"data":{"agent":{"access_token":"agent_123"}}}`)
	got := extractTokenFromJSON(body)
	if got != "agent_123" {
		t.Fatalf("extractTokenFromJSON() = %q", got)
	}
}

func TestExtractAPIBaseFromJSONNested(t *testing.T) {
	t.Parallel()

	body := []byte(`{"result":{"api_base":"https://na.hub.molten.bot/v1"}}`)
	got := extractAPIBaseFromJSON(body)
	if got != "https://na.hub.molten.bot/v1" {
		t.Fatalf("extractAPIBaseFromJSON() = %q", got)
	}
}
