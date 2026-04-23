//go:build windows

package execx

import "os/exec"

func configureCommandProcessGroup(_ *exec.Cmd) {}

func terminateCommandProcessGroup(_ *exec.Cmd) {}
