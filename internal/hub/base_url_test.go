package hub

import (
	"strings"
	"testing"
)

func TestValidateHubBaseURLStrictAcceptsRegionalEndpoints(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{
		"https://na.hub.molten.bot/v1",
		"https://eu.hub.molten.bot/v1/",
	} {
		if err := ValidateHubBaseURLStrict(baseURL); err != nil {
			t.Fatalf("ValidateHubBaseURLStrict(%q) error = %v", baseURL, err)
		}
	}
}

func TestValidateHubBaseURLStrictRejectsLoopbackHost(t *testing.T) {
	t.Parallel()

	err := ValidateHubBaseURLStrict("http://127.0.0.1:37581/v1")
	if err == nil {
		t.Fatal("ValidateHubBaseURLStrict(loopback) error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "must use https") && !strings.Contains(err.Error(), "na.hub.molten.bot") {
		t.Fatalf("ValidateHubBaseURLStrict(loopback) err = %q, want https or allowed-host guidance", err.Error())
	}
}

func TestCanonicalHubBaseURLNormalizesRegionalEndpoint(t *testing.T) {
	t.Parallel()

	got, err := CanonicalHubBaseURL("https://eu.hub.molten.bot/v1/")
	if err != nil {
		t.Fatalf("CanonicalHubBaseURL() error = %v", err)
	}
	if got != "https://eu.hub.molten.bot/v1" {
		t.Fatalf("CanonicalHubBaseURL() = %q, want %q", got, "https://eu.hub.molten.bot/v1")
	}
}

func TestCanonicalHubBaseURLAllowsCustomURLWithOverride(t *testing.T) {
	t.Setenv(allowNonMoltenHubBaseURLEnvName, "1")

	got, err := CanonicalHubBaseURL("http://127.0.0.1:8080/v1")
	if err != nil {
		t.Fatalf("CanonicalHubBaseURL(override) error = %v", err)
	}
	if got != "http://127.0.0.1:8080/v1" {
		t.Fatalf("CanonicalHubBaseURL(override) = %q, want %q", got, "http://127.0.0.1:8080/v1")
	}
}

func TestValidatePersistedHubBaseURLRejectsLoopbackEvenWithOverride(t *testing.T) {
	t.Setenv(allowNonMoltenHubBaseURLEnvName, "1")

	err := ValidatePersistedHubBaseURL("http://127.0.0.1:8080/v1")
	if err == nil {
		t.Fatal("ValidatePersistedHubBaseURL(loopback) error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("ValidatePersistedHubBaseURL(loopback) err = %q, want https detail", err.Error())
	}
}

func TestHubBaseURLForRegion(t *testing.T) {
	t.Parallel()

	if got, want := HubBaseURLForRegion("na"), "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("HubBaseURLForRegion(na) = %q, want %q", got, want)
	}
	if got, want := HubBaseURLForRegion("EU"), "https://eu.hub.molten.bot/v1"; got != want {
		t.Fatalf("HubBaseURLForRegion(EU) = %q, want %q", got, want)
	}
	if got, want := HubBaseURLForRegion("other"), "https://na.hub.molten.bot/v1"; got != want {
		t.Fatalf("HubBaseURLForRegion(other) = %q, want %q", got, want)
	}
}
