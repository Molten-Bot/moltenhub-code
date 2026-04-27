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

func TestHubRegionFromBaseURLFallbacks(t *testing.T) {
	t.Parallel()

	if got := HubRegionFromBaseURL("http://[::1"); got != hubRegionNA {
		t.Fatalf("HubRegionFromBaseURL(invalid) = %q, want %q", got, hubRegionNA)
	}
	if got := HubRegionFromBaseURL("https://na.hub.molten.bot/v1"); got != hubRegionNA {
		t.Fatalf("HubRegionFromBaseURL(na) = %q, want %q", got, hubRegionNA)
	}
	if got := HubRegionFromBaseURL("https://custom.example/v1"); got != hubRegionNA {
		t.Fatalf("HubRegionFromBaseURL(custom) = %q, want %q", got, hubRegionNA)
	}
}

func TestValidatePersistedHubBaseURLRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "empty", baseURL: " ", want: "required"},
		{name: "parse", baseURL: "https://[::1", want: "base_url:"},
		{name: "query", baseURL: "https://na.hub.molten.bot/v1?x=1", want: "query or fragment"},
		{name: "port", baseURL: "https://na.hub.molten.bot:443/v1", want: "port"},
		{name: "host suffix", baseURL: "https://hub.molten.bot/v1", want: "Molten Hub domain"},
		{name: "nested host", baseURL: "https://bad.na.hub.molten.bot/v1", want: "Molten Hub domain"},
		{name: "path", baseURL: "https://na.hub.molten.bot/v2", want: "/v1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidatePersistedHubBaseURL(tc.baseURL)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidatePersistedHubBaseURL(%q) error = %v, want %q", tc.baseURL, err, tc.want)
			}
		})
	}
}

func TestValidateHubBaseURLRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "empty", baseURL: " ", want: "required"},
		{name: "parse", baseURL: "https://[::1", want: "base_url:"},
		{name: "scheme", baseURL: "ftp://na.hub.molten.bot/v1", want: "http or https"},
		{name: "host", baseURL: "https:///v1", want: "host is required"},
		{name: "query", baseURL: "https://na.hub.molten.bot/v1?x=1", want: "query or fragment"},
		{name: "port", baseURL: "https://na.hub.molten.bot:443/v1", want: "port"},
		{name: "suffix", baseURL: "https://example.com/v1", want: "one of"},
		{name: "region", baseURL: "https://ap.hub.molten.bot/v1", want: "one of"},
		{name: "path", baseURL: "https://na.hub.molten.bot/v2", want: "/v1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateHubBaseURLStrict(tc.baseURL)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateHubBaseURLStrict(%q) error = %v, want %q", tc.baseURL, err, tc.want)
			}
		})
	}
}

func TestValidateHubBaseURLAllowsNonMoltenWhenConfigured(t *testing.T) {
	t.Parallel()

	if err := validateHubBaseURL("http://localhost:3000/v1?debug=1", true); err != nil {
		t.Fatalf("validateHubBaseURL(allow custom) error = %v", err)
	}
}
