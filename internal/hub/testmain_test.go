package hub

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("HARNESS_ALLOW_NON_MOLTEN_HUB_BASE_URL", "1")
	os.Exit(m.Run())
}
