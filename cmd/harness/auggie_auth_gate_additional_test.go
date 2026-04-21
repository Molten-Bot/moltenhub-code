package main

import (
	"context"
	"testing"

	"github.com/Molten-Bot/moltenhub-code/internal/hub"
)

func TestAuggieAuthGateNilStartAndVerifyReturnReady(t *testing.T) {
	t.Parallel()

	var g *auggieAuthGate
	startState, err := g.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if !startState.Ready || startState.Required || startState.State != "ready" {
		t.Fatalf("StartDeviceAuth() = %+v", startState)
	}

	verifyState, err := g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !verifyState.Ready || verifyState.Required || verifyState.State != "ready" {
		t.Fatalf("Verify() = %+v", verifyState)
	}
}

func TestAuggieAuthGateStartDeviceAuthAndVerifyRefreshState(t *testing.T) {
	t.Setenv(auggieSessionAuthEnv, "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	g := newAuggieAuthGate("", hub.InitConfig{})

	startState, err := g.StartDeviceAuth(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceAuth() error = %v", err)
	}
	if startState.State != "needs_configure" || startState.Ready {
		t.Fatalf("StartDeviceAuth() = %+v", startState)
	}
	if startState.Message != "" {
		t.Fatalf("StartDeviceAuth() message = %q, want empty", startState.Message)
	}

	verifyState, err := g.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verifyState.State != "needs_configure" || verifyState.Ready {
		t.Fatalf("Verify() = %+v", verifyState)
	}
}

func TestDecodeAuggieSessionAuthSupportsWrappedJSONAndDecodeStrictErrors(t *testing.T) {
	t.Parallel()

	decoded, err := decodeAuggieSessionAuth(`"{\"accessToken\":\"token\",\"tenantURL\":\"https://tenant.example/\",\"scopes\":[\"email\"]}"`)
	if err != nil {
		t.Fatalf("decodeAuggieSessionAuth(wrapped) error = %v", err)
	}
	if decoded.AccessToken != "token" || decoded.TenantURL != "https://tenant.example/" || len(decoded.Scopes) != 1 {
		t.Fatalf("decodeAuggieSessionAuth(wrapped) = %+v", decoded)
	}

	var payload map[string]any
	if err := decodeJSONStrict(`{"a":1} {"b":2}`, &payload); err == nil {
		t.Fatal("decodeJSONStrict(trailing JSON) error = nil, want non-nil")
	}
	if err := decodeJSONStrict(`{"a":1}`, &struct {
		B int `json:"b"`
	}{}); err == nil {
		t.Fatal("decodeJSONStrict(unknown field) error = nil, want non-nil")
	}
}
