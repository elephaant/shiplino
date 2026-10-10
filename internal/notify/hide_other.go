// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

//go:build !windows

package notify

import "os/exec"

func hideWindow(*exec.Cmd) {}
