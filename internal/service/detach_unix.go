//go:build !windows

package service

import (
	"os/exec"
	"syscall"
)

// detach starts the process in its own session so it outlives the parent.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
