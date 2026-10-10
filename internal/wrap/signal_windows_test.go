// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build windows

package wrap

import "os"

// helperMode: the signal tests are Unix only.
func helperMode(string) { os.Exit(99) }
