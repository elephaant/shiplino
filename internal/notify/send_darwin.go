// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package notify

import (
	"context"
	"strings"
)

// macOS: Notification Center through AppleScript's `display
// notification` (osascript ships with the OS).

func osAvailable() (bool, string) { return true, "Notification Center" }

func osSend(ctx context.Context, n Note) error {
	script := "display notification " + appleString(n.Body) + " with title " + appleString(n.Title)
	return run(ctx, "osascript", "-e", script)
}

// appleString quotes s as an AppleScript string literal.
func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
