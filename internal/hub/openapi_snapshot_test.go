package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var requiredRuntimeIntegrationRoutes = []string{
	"/agents/me/metadata:",
	"/agents/me:",
	"/messages/publish:",
	"/messages/pull:",
	"/messages/ack:",
	"/messages/nack:",
	"/openclaw/messages/publish:",
	"/openclaw/messages/pull:",
	"/openclaw/messages/ack:",
	"/openclaw/messages/nack:",
	"/openclaw/messages/ws:",
	"/openclaw/messages/offline:",
}

func TestOpenAPISnapshotIncludesRuntimeIntegrationRoutes(t *testing.T) {
	t.Parallel()

	content, source, err := loadOpenAPIContractForTest()
	if err != nil {
		t.Skipf("skipping OpenAPI route validation: %v", err)
	}
	t.Logf("validated OpenAPI contract from %s", source)

	assertOpenAPIRoutes(t, source, content)
	if !strings.Contains(content, "/messages/pull:\n    get:") {
		t.Fatalf("%s missing expected GET method for /messages/pull", source)
	}

	if strings.Contains(content, "/openclaw/messages/register-plugin:") {
		t.Fatalf("%s unexpectedly contains undocumented route /openclaw/messages/register-plugin", source)
	}
}

func TestOpenAPISnapshotFileExistsForOfflineReview(t *testing.T) {
	t.Parallel()

	content, source, err := loadOpenAPIContractForTest()
	if err != nil {
		t.Fatal(err)
	}
	assertOpenAPIRoutes(t, source, content)
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

func assertOpenAPIRoutes(t *testing.T, source, content string) {
	t.Helper()
	for _, route := range requiredRuntimeIntegrationRoutes {
		if !strings.Contains(content, route) {
			t.Fatalf("%s missing required route %q", source, route)
		}
	}
}
