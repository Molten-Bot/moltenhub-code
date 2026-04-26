//go:build !windows

package execx

import (
	"os/exec"
	"testing"
)

func TestProcessGroupHelpersHandleNilInputs(t *testing.T) {
	t.Parallel()

	configureCommandProcessGroup(nil)
	terminateCommandProcessGroup(nil)
	terminateCommandProcessGroup(&exec.Cmd{})
}
