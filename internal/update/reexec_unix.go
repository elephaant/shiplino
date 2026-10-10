//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Reexec replaces the running process with bin args. The pid stays the
// same, so systemd, launchd and anyone watching the pid don't notice.
func Reexec(bin string, args []string) error {
	return syscall.Exec(bin, append([]string{bin}, args...), os.Environ())
}
