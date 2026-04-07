//go:build unix

package main

import (
	"os"
	"syscall"
)

func pauseProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Signal(syscall.SIGSTOP)
}

func resumeProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Signal(syscall.SIGCONT)
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Signal(syscall.SIGKILL)
}
