// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrLocked means another daemon is already running for this home.
var ErrLocked = errors.New("another shiplino daemon is running")

// Lock is a single-instance lock file holding the owner's pid.
type Lock struct{ path string }

// AcquireLock takes home/daemon.lock. A lock left by a process that no
// longer exists is taken over.
func AcquireLock(home string) (*Lock, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(home, "daemon.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()))
			cerr := f.Close()
			if err := errors.Join(werr, cerr); err != nil {
				os.Remove(path)
				return nil, err
			}
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		b, _ := os.ReadFile(path)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if perr == nil && pid != os.Getpid() && processAlive(pid) {
			return nil, fmt.Errorf("%w (pid %d)", ErrLocked, pid)
		}
		_ = os.Remove(path) // stale: owner is gone
	}
	return nil, ErrLocked
}

// Release removes the lock file.
func (l *Lock) Release() error { return os.Remove(l.path) }
