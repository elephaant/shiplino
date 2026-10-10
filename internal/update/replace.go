package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// swap makes newBin the target and keeps the old target as prev. newBin
// must be in the target's directory (same filesystem).
//
// Unix: prev becomes a hard link (or copy) of the target, then newBin is
// renamed over the target in one atomic step. A hook starting at any
// moment runs either the old or the new binary, and running processes
// keep the old file.
//
// Windows can't overwrite a running .exe but can rename it, so the target
// is renamed aside to prev and newBin renamed into place. Between the two
// renames (microseconds) the path doesn't exist; a hook starting exactly
// then fails, which agents treat as a non-blocking hook error.
func swap(newBin, target, prev string, renameAside bool) error {
	dir := filepath.Dir(target)
	sweepTrash(dir)
	if renameAside {
		if err := clearPrevious(prev); err != nil {
			return err
		}
		if err := os.Rename(target, prev); err != nil {
			return err
		}
		if err := os.Rename(newBin, target); err != nil {
			if rerr := os.Rename(prev, target); rerr != nil {
				return fmt.Errorf("%w; restoring the old binary also failed: %v (it's at %s)", err, rerr, prev)
			}
			return err
		}
		return nil
	}
	tmp := prev + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Link(target, tmp); err != nil {
		if err := copyFile(target, tmp); err != nil {
			return fmt.Errorf("keeping the previous binary: %w", err)
		}
	}
	if err := os.Rename(tmp, prev); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("keeping the previous binary: %w", err)
	}
	return os.Rename(newBin, target)
}

// lockDir stops two updates (say `shiplino update` and the daemon's
// auto_install) from swapping binaries at the same time. A lock older
// than 15 minutes is left from a crash and taken over.
func lockDir(dir string) (unlock func(), err error) {
	path := filepath.Join(dir, ".shiplino-update.lock")
	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) < 15*time.Minute {
			return nil, errors.New("another update is running; try again in a minute")
		}
		os.Remove(path)
	}
	return nil, errors.New("another update is running; try again in a minute")
}

// clearPrevious removes an old previous binary. On Windows it may still be
// running (an old daemon or hook), so it's renamed to a trash name that a
// later update deletes.
func clearPrevious(prev string) error {
	err := os.Remove(prev)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	trash := filepath.Join(filepath.Dir(prev), fmt.Sprintf(".shiplino-trash-%d%s", time.Now().UnixNano(), filepath.Ext(prev)))
	if rerr := os.Rename(prev, trash); rerr != nil {
		return fmt.Errorf("removing %s: %w", prev, err)
	}
	return nil
}

// sweepTrash deletes leftovers of earlier updates (best effort).
func sweepTrash(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".shiplino-trash-") {
			_ = os.Remove(filepath.Join(dir, n))
		}
		if strings.HasPrefix(n, ".shiplino-update-") {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
				_ = os.RemoveAll(filepath.Join(dir, n))
			}
		}
	}
}

// copyFile copies src to dst with mode 0755.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := errors.Join(out.Sync(), out.Close()); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chmod(dst, 0o755)
}
