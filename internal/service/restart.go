package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNotInstalled means no service is registered, so there's nothing to
// restart (the user runs `shiplino daemon` themselves, or setup used
// --no-service).
var ErrNotInstalled = errors.New("the background service isn't installed")

// Restart restarts the registered daemon so it runs the current binary
// (after `shiplino update`). It returns a short description of how.
func Restart(ctx context.Context, c Config) (string, error) {
	switch c.goos() {
	case "linux":
		if exists(c.unitPath()) {
			if out, err := c.run(ctx, "systemctl", "--user", "restart", unitName); err != nil {
				return "", fmt.Errorf("systemctl restart: %v: %s", err, out)
			}
			return "systemd user service " + unitName, nil
		}
		if exists(c.autostartPath()) {
			if err := c.stopDaemon(ctx); err != nil {
				return "", err
			}
			if err := startDetached(c); err != nil {
				return "", err
			}
			return "background process (XDG autostart)", nil
		}
	case "darwin":
		if exists(c.plistPath()) {
			target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + launchLabel
			if out, err := c.run(ctx, "launchctl", "kickstart", "-k", target); err != nil {
				return "", fmt.Errorf("launchctl kickstart: %v: %s", err, out)
			}
			return "LaunchAgent " + launchLabel, nil
		}
	case "windows":
		if _, err := c.run(ctx, "schtasks", "/Query", "/TN", taskName); err == nil {
			_, _ = c.run(ctx, "schtasks", "/End", "/TN", taskName)
			// A daemon that restarted itself isn't the task's process.
			if err := c.stopDaemon(ctx); err != nil {
				return "", err
			}
			if out, err := c.run(ctx, "schtasks", "/Run", "/TN", taskName); err != nil {
				return "", fmt.Errorf("schtasks /Run: %v: %s", err, out)
			}
			return "Task Scheduler logon task " + taskName, nil
		}
	}
	return "", ErrNotInstalled
}

// stopDaemon stops the daemon named in home/daemon.lock: politely first
// (SIGTERM), then for good after 5 seconds. Windows has no SIGTERM, so
// there it's stopped right away, as Task Scheduler's /End does. The pid
// must belong to a shiplino process, so a stale lock never gets an
// unrelated process killed.
func (c *Config) stopDaemon(ctx context.Context) error {
	lock := filepath.Join(c.Home, "daemon.lock")
	b, err := os.ReadFile(lock)
	if err != nil {
		return nil // not running
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return nil
	}
	var out []byte
	if c.goos() == "windows" {
		out, err = c.run(ctx, "tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	} else {
		out, err = c.run(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	}
	if err != nil || !strings.Contains(strings.ToLower(string(out)), "shiplino") {
		return nil // stale lock: that pid isn't a daemon; the new one takes the lock over
	}
	if c.Run != nil { // tests: record instead of signalling
		_, err := c.Run(ctx, "kill", strconv.Itoa(pid))
		return err
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := p.Signal(syscall.SIGTERM); err == nil {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			if !exists(lock) {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping the old daemon (pid %d): %w", pid, err)
	}
	_ = os.Remove(lock)
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
