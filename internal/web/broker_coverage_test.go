package web

import "testing"

func TestBrokerHelperParsersAndUniqueListMerging(t *testing.T) {
	t.Parallel()

	if got := parseFieldValue(`dispatch status=error err="decode run config payload: json: unknown field \"branch\""`, "err"); got != `decode run config payload: json: unknown field "branch"` {
		t.Fatalf("parseFieldValue(quoted escaped) = %q", got)
	}
	if got := parseFieldValue(`dispatch status=error err=boom`, "err"); got != "boom" {
		t.Fatalf("parseFieldValue(unquoted) = %q, want boom", got)
	}
	if got := parseFieldValue(`dispatch status=ok`, "missing"); got != "" {
		t.Fatalf("parseFieldValue(missing) = %q, want empty", got)
	}

	fields := parseKVFields(`dispatch request_id=local-1712345678-000001 cmd phase=codex text="dispatch status=error request_id=req-err-ws err=\"clone failed\""`)
	if got, want := fields["request_id"], "local-1712345678-000001"; got != want {
		t.Fatalf("parseKVFields(request_id) = %q, want %q", got, want)
	}
	if got := fields["status"]; got != "" {
		t.Fatalf("parseKVFields(status) = %q, want empty (nested status should not be parsed)", got)
	}

	if _, ok := parseQuotedToken("not-quoted"); ok {
		t.Fatal("parseQuotedToken(non-quoted) ok = true, want false")
	}
	if token, ok := parseQuotedToken(`"a\"b"`); !ok || token != `"a\"b"` {
		t.Fatalf("parseQuotedToken(escaped) = (%q, %v)", token, ok)
	}

	if _, ok := parseFloatField(" "); ok {
		t.Fatal("parseFloatField(blank) ok = true, want false")
	}
	if _, ok := parseFloatField("nan-not-number"); ok {
		t.Fatal("parseFloatField(invalid) ok = true, want false")
	}
	if got, ok := parseFloatField(" 3.14 "); !ok || got != 3.14 {
		t.Fatalf("parseFloatField(valid) = (%v, %v), want (3.14, true)", got, ok)
	}

	if got := hubDomainFromBaseURL(" ://bad"); got != "" {
		t.Fatalf("hubDomainFromBaseURL(invalid) = %q, want empty", got)
	}
	if got := hubDomainFromBaseURL(" https://na.hub.molten.bot/v1 "); got != "na.hub.molten.bot" {
		t.Fatalf("hubDomainFromBaseURL(valid) = %q", got)
	}

	merged := appendNonEmptyUnique([]string{"a", " ", "a", "b"}, "b", " c ", "", "a")
	if got, want := len(merged), 3; got != want {
		t.Fatalf("len(appendNonEmptyUnique) = %d, want %d (%v)", got, want, merged)
	}
	if merged[0] != "a" || merged[1] != "b" || merged[2] != "c" {
		t.Fatalf("appendNonEmptyUnique() = %v, want [a b c]", merged)
	}
}

func TestBrokerHubConnectionUpdateAdditionalStates(t *testing.T) {
	t.Parallel()

	b := NewBroker()
	b.mu.Lock()
	b.updateHubConnectionFromLineLocked("hub.connection status=offline domain=custom.example", map[string]string{
		"status": "offline",
		"domain": "custom.example",
	})
	b.mu.Unlock()

	snap := b.Snapshot()
	if snap.Connection.HubConnected {
		t.Fatal("hub.connected = true, want false")
	}
	if got, want := snap.Connection.HubTransport, hubTransportDisconnected; got != want {
		t.Fatalf("hub.transport = %q, want %q", got, want)
	}
	if got, want := snap.Connection.HubDomain, "custom.example"; got != want {
		t.Fatalf("hub.domain = %q, want %q", got, want)
	}
}
