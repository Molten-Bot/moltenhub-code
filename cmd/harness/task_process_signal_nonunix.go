//go:build !unix

package main

import (
	"fmt"
	"os"
)

func pauseProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return fmt.Errorf("pause is not supported on this platform")
}

func resumeProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return fmt.Errorf("resume is not supported on this platform")
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}
