// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
)

// notifyCmd: `shiplino notify test` shows a sample notification, so you
// can check that they appear (and allow them where the OS asks).
func notifyCmd(ctx context.Context, e *env, args []string) int {
	if len(args) != 1 || args[0] != "test" {
		fmt.Fprintln(e.errOut, "usage: shiplino notify test")
		return 2
	}
	ok, how := notify.Available()
	if e.notifySend != nil {
		ok, how = true, "test sender"
	}
	if !ok {
		fmt.Fprintln(e.errOut, notifyMissingHint())
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := e.send(ctx, notify.Note{Title: "Shiplino notifications work", Body: "You'll hear from Shiplino when an agent is waiting on you, finishes a long turn or fails."})
	if err != nil {
		fmt.Fprintf(e.errOut, "Couldn't show a notification via %s: %v\n", how, err)
		return 1
	}
	fmt.Fprintf(e.out, "Sent a test notification via %s.\n", how)
	if c, err := config.Load(e.home); err == nil {
		if _, on := c.NotifySettings(); !on {
			fmt.Fprintf(e.out, "Note: notifications are turned off in %s ([notify] enabled = false).\n", config.Path(e.home))
		}
	}
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintln(e.out, "Don't see it? Allow notifications for Script Editor in System Settings → Notifications.")
	case "windows":
		fmt.Fprintln(e.out, "Don't see it? Check that notifications for Windows PowerShell are on in Settings → System → Notifications.")
	default:
		fmt.Fprintln(e.out, "Don't see it? Check that Do Not Disturb is off.")
	}
	return 0
}

func notifyMissingHint() string {
	if runtime.GOOS == "linux" {
		return "No notification service found: install libnotify (e.g. `sudo apt install libnotify-bin`) and make sure a notification daemon runs in your desktop session."
	}
	return "This system has no supported notification service."
}
