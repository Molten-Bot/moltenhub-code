package hub

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenAPISnapshotIncludesRuntimeIntegrationRoutes(t *testing.T) {
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
	content := string(data)

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
			t.Fatalf("%s missing required route %q", snapshotPath, route)
		}
	}

	if strings.Contains(content, "/openclaw/messages/register-plugin:") {
		t.Fatalf("%s unexpectedly contains undocumented route /openclaw/messages/register-plugin", snapshotPath)
	}
}
