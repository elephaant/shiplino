//go:build windows

package daemon

import "os"

// On Windows, FindProcess opens a handle and fails if the process is gone.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
