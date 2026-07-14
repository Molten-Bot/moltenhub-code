package hub

import "testing"

func TestWebsocketURLFromHTTPSBase(t *testing.T) {
	t.Parallel()

	got, err := WebsocketURL("https://na.hub.molten.bot/v1", "main")
	if err != nil {
		t.Fatalf("WebsocketURL() error = %v", err)
	}
	want := "wss://na.hub.molten.bot/v1/runtime/messages/ws?session_key=main"
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

	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "documented snake case", body: `{"result":{"api_base_url":"https://na.hub.molten.bot/v1"}}`},
		{name: "documented camel case", body: `{"result":{"apiBaseUrl":"https://na.hub.molten.bot/v1"}}`},
		{name: "legacy alias", body: `{"result":{"api_base":"https://na.hub.molten.bot/v1"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAPIBaseFromJSON([]byte(tt.body))
			if got != "https://na.hub.molten.bot/v1" {
				t.Fatalf("extractAPIBaseFromJSON() = %q", got)
			}
		})
	}
}
