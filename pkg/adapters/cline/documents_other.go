// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !windows

package cline

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// documentsDir is the Documents folder the extension uses: on Linux what
// `xdg-user-dir DOCUMENTS` prints when that tool exists, else ~/Documents.
func documentsDir(ctx context.Context, home string) string {
	if runtime.GOOS == "linux" {
		if bin, err := exec.LookPath("xdg-user-dir"); err == nil {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, bin, "DOCUMENTS").Output(); err == nil {
				if p := strings.TrimSpace(string(out)); p != "" {
					return p
				}
			}
		}
	}
	return filepath.Join(home, "Documents")
}
