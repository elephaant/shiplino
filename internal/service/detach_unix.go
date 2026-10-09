// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

//go:build !windows

package service

import (
	"os/exec"
	"syscall"
)

// detach starts the process in its own session so it outlives the parent.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
