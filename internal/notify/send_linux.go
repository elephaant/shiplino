// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package notify

import (
	"context"
	"os/exec"
	"strings"
)

// Linux and other freedesktop systems: the org.freedesktop.Notifications
// D-Bus service (GNOME, KDE, XFCE, dunst, mako…), through notify-send or,
// where libnotify's tool isn't installed, gdbus (part of GLib).

func osAvailable() (bool, string) {
	if _, err := exec.LookPath("notify-send"); err == nil {
		return true, "notify-send"
	}
	if _, err := exec.LookPath("gdbus"); err == nil {
		return true, "gdbus (D-Bus)"
	}
	return false, ""
}

func osSend(ctx context.Context, n Note) error {
	if _, err := exec.LookPath("notify-send"); err == nil {
		return run(ctx, "notify-send", "--app-name=Shiplino", "--icon=dialog-information", "--", n.Title, n.Body)
	}
	if _, err := exec.LookPath("gdbus"); err == nil {
		return run(ctx, "gdbus", "call", "--session",
			"--dest=org.freedesktop.Notifications", "--object-path=/org/freedesktop/Notifications",
			"--method=org.freedesktop.Notifications.Notify",
			"'Shiplino'", "uint32 0", "'dialog-information'", gvariantString(n.Title), gvariantString(n.Body),
			"@as []", "@a{sv} {}", "int32 -1")
	}
	return ErrUnavailable
}

// gvariantString quotes s as a GVariant text-format string.
func gvariantString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`)
	return "'" + r.Replace(s) + "'"
}
