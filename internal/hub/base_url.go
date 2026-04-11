package hub

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	defaultHubRegion                = "na"
	hubRegionEU                     = "eu"
	hubRegionNA                     = "na"
	hubDomainSuffix                 = ".hub.molten.bot"
	allowNonMoltenHubBaseURLEnvName = "HARNESS_ALLOW_NON_MOLTEN_HUB_BASE_URL"
)

// AllowNonMoltenHubBaseURL reports whether non-production hub base URLs are
// explicitly allowed (for local integration/testing).
func AllowNonMoltenHubBaseURL() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(allowNonMoltenHubBaseURLEnvName)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// NormalizeHubRegion converts region into a supported value.
func NormalizeHubRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case hubRegionEU:
		return hubRegionEU
	default:
		return hubRegionNA
	}
}

// HubBaseURLForRegion returns the canonical hub API base URL for a region.
func HubBaseURLForRegion(region string) string {
	return fmt.Sprintf("https://%s.hub.molten.bot/v1", NormalizeHubRegion(region))
}

// HubRegionFromBaseURL infers region from a hub base URL.
func HubRegionFromBaseURL(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return defaultHubRegion
	}
	switch strings.ToLower(strings.TrimSpace(u.Hostname())) {
	case "eu.hub.molten.bot":
		return hubRegionEU
	default:
		return hubRegionNA
	}
}

// ValidateHubBaseURL validates baseURL according to the active policy.
func ValidateHubBaseURL(baseURL string) error {
	return validateHubBaseURL(baseURL, AllowNonMoltenHubBaseURL())
}

// ValidateHubBaseURLStrict validates baseURL against production hub constraints.
func ValidateHubBaseURLStrict(baseURL string) error {
	return validateHubBaseURL(baseURL, false)
}

// CanonicalHubBaseURL validates and normalizes baseURL according to active policy.
func CanonicalHubBaseURL(baseURL string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := ValidateHubBaseURL(baseURL); err != nil {
		return "", err
	}
	if AllowNonMoltenHubBaseURL() {
		return baseURL, nil
	}
	return HubBaseURLForRegion(HubRegionFromBaseURL(baseURL)), nil
}

func validateHubBaseURL(baseURL string, allowNonMolten bool) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("base_url is required")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url must use http or https")
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return fmt.Errorf("base_url host is required")
	}
	if allowNonMolten {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("base_url must use https")
	}
	if strings.TrimSpace(u.RawQuery) != "" || strings.TrimSpace(u.Fragment) != "" {
		return fmt.Errorf("base_url must not include query or fragment")
	}
	if strings.TrimSpace(u.Port()) != "" {
		return fmt.Errorf("base_url must not include a port")
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	region, hasSuffix := strings.CutSuffix(host, hubDomainSuffix)
	if !hasSuffix || strings.Contains(region, ".") {
		return fmt.Errorf("base_url host must be one of na.hub.molten.bot or eu.hub.molten.bot")
	}
	switch region {
	case hubRegionNA, hubRegionEU:
	default:
		return fmt.Errorf("base_url host must be one of na.hub.molten.bot or eu.hub.molten.bot")
	}

	if strings.TrimRight(strings.TrimSpace(u.Path), "/") != "/v1" {
		return fmt.Errorf("base_url path must be /v1")
	}
	return nil
}
