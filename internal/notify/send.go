// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ErrUnavailable means this system has no way to show notifications.
var ErrUnavailable = errors.New("no desktop notification service found")

// Send shows n with the operating system's notification service.
func Send(ctx context.Context, n Note) error { return osSend(ctx, n) }

// Available reports whether notifications can be shown, and how.
func Available() (bool, string) { return osAvailable() }

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(name + ": " + msg)
		}
		return err
	}
	return nil
}
