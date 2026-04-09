package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		"/agents/me/status:",
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
	if !strings.Contains(content, "/openclaw/messages/pull:\n    get:") {
		t.Fatalf("%s missing expected GET method for /openclaw/messages/pull", source)
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
		"/agents/me/status:",
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
	data, readErr := os.ReadFile(snapshotPath)
	if readErr != nil {
		return "", "", fmt.Errorf("read OpenAPI snapshot %s: %w", snapshotPath, readErr)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "", "", fmt.Errorf("OpenAPI snapshot %s is empty", snapshotPath)
	}
	return string(data), snapshotPath, nil
}
