//go:build !windows

package execx

import (
	"os/exec"
	"syscall"
)

func configureCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Kill the whole process group so child subprocesses do not outlive canceled tasks.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
