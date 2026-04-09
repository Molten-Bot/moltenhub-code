package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenAPISnapshotIncludesRuntimeIntegrationRoutes(t *testing.T) {
	t.Parallel()

	content, source, err := loadOpenAPIContractForTest()
	if err != nil {
		t.Skipf("skipping OpenAPI route validation: %v", err)
	}
	t.Logf("validated OpenAPI contract from %s", source)

	requiredRoutes := []string{
		"/agents/me/metadata:",
		"/agents/me:",
		"/openclaw/messages/publish:",
		"/openclaw/messages/pull:",
		"/openclaw/messages/ack:",
		"/openclaw/messages/nack:",
		"/openclaw/messages/ws:",
		"/openclaw/messages/offline:",
	}
	for _, route := range requiredRoutes {
		if !strings.Contains(content, route) {
			t.Fatalf("%s missing required route %q", source, route)
		}
	}

	if strings.Contains(content, "/openclaw/messages/register-plugin:") {
		t.Fatalf("%s unexpectedly contains undocumented route /openclaw/messages/register-plugin", source)
	}
}

func TestOpenAPISnapshotFileExistsForOfflineReview(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	snapshotPath := filepath.Join(repoRoot, "na.hub.molten.bot.openapi.yaml")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", snapshotPath, err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		t.Fatalf("%s is empty", snapshotPath)
	}
	for _, route := range []string{
		"/agents/me/metadata:",
		"/agents/me:",
		"/openclaw/messages/publish:",
		"/openclaw/messages/pull:",
		"/openclaw/messages/ack:",
		"/openclaw/messages/nack:",
		"/openclaw/messages/ws:",
		"/openclaw/messages/offline:",
	} {
		if !strings.Contains(content, route) {
			t.Fatalf("%s missing required route %q", snapshotPath, route)
		}
	}
}

func loadOpenAPIContractForTest() (content string, source string, err error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", fmt.Errorf("runtime.Caller(0) failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	snapshotPath := filepath.Join(repoRoot, "na.hub.molten.bot.openapi.yaml")
	if data, readErr := os.ReadFile(snapshotPath); readErr == nil {
		return string(data), snapshotPath, nil
	}

	openAPIURL := strings.TrimSpace(os.Getenv("MOLTENHUB_OPENAPI_URL"))
	if openAPIURL == "" {
		openAPIURL = "https://na.hub.molten.bot/openapi.yaml"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAPIURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build OpenAPI request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch OpenAPI from %s: %w", openAPIURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("fetch OpenAPI from %s returned status=%d", openAPIURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", "", fmt.Errorf("read OpenAPI from %s: %w", openAPIURL, err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", "", fmt.Errorf("OpenAPI payload from %s is empty", openAPIURL)
	}
	return string(body), openAPIURL, nil
}
