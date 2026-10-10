//go:build windows

package update

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// Reexec starts bin args as a detached process; the caller then exits.
// Windows has no exec, so the new process gets a new pid.
func Reexec(bin string, args []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
