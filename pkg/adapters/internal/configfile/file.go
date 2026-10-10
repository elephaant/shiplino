// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package configfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Backup copies path into dir as <timestamp>-<name> and returns the copy's
// path. A missing source is not an error (nothing to back up).
func Backup(path, dir string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000Z"), filepath.Base(path)))
	return dst, os.WriteFile(dst, b, 0o600)
}

// WriteAtomic replaces path with data: write a temp file in the same
// directory, fsync, then rename, so readers never see a half-written
// config. The existing file's permissions are kept.
func WriteAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	clean := func(e error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return e
	}
	if _, err := tmp.Write(data); err != nil {
		return clean(err)
	}
	if err := tmp.Sync(); err != nil {
		return clean(err)
	}
	if err := tmp.Close(); err != nil {
		return clean(err)
	}
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
