package agents

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Change is one file that setup or uninstall would write. Before is nil
// when the file would be created, After when it would be removed.
type Change struct {
	Path             string
	Before, After    []byte
	Created, Removed bool
}

// PreviewInstall reports what Install would change, without writing
// anything at path. The real Install runs on a scratch copy, so the diff
// is exactly what setup would write.
func (a Hooks) PreviewInstall(path, bin, version string) ([]Change, error) {
	return preview(path, func(p, backups string) error {
		_, _, err := a.Install(p, bin, version, backups)
		return err
	})
}

// PreviewUninstall reports what Uninstall would change, without writing
// anything at path.
func (a Hooks) PreviewUninstall(path string) ([]Change, error) {
	return preview(path, func(p, backups string) error {
		_, err := a.Uninstall(p, backups)
		return err
	})
}

// preview copies path (a config file, or a directory of hook files) to
// a scratch directory, runs op on the copy, and compares. Backups go to
// the scratch directory too, which is deleted afterwards. op's error is
// returned with whatever it changed before failing.
func preview(path string, op func(path, backupDir string) error) ([]Change, error) {
	before, err := snapshot(path)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "shiplino-preview-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	scratch := filepath.Join(tmp, "target", filepath.Base(path))
	if err := before.restore(scratch); err != nil {
		return nil, err
	}
	opErr := op(scratch, filepath.Join(tmp, "backups"))
	after, err := snapshot(scratch)
	if err != nil {
		return nil, err
	}

	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var out []Change
	for _, n := range sorted {
		b, hadB := before[n]
		a, hasA := after[n]
		if hadB == hasA && bytes.Equal(a, b) {
			continue
		}
		p := path
		if n != "" {
			p = filepath.Join(path, n)
		}
		out = append(out, Change{Path: p, Before: b, After: a, Created: !hadB, Removed: !hasA})
	}
	return out, opErr
}

// files maps names to contents: "" for a single file, or the regular
// files directly inside a directory. Missing paths are empty.
type files map[string][]byte

func snapshot(path string) (files, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return files{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(path)
		return files{"": b}, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := files{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = b
	}
	return out, nil
}

// restore writes the snapshot to path: a file, or a directory when the
// snapshot came from one.
func (f files) restore(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if b, ok := f[""]; ok {
		return os.WriteFile(path, b, 0o600)
	}
	if len(f) == 0 {
		return nil
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	for n, b := range f {
		if err := os.WriteFile(filepath.Join(path, n), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}
