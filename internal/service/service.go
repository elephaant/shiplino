package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// Runner executes OS commands; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultRunner runs real commands with a timeout.
func DefaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Config describes the daemon service for one user.
type Config struct {
	Bin      string // absolute path of the shiplino binary
	Home     string // Shiplino home (~/.shiplino)
	UserHome string
	GOOS     string // defaults to runtime.GOOS
	Run      Runner // defaults to DefaultRunner
}

func (c *Config) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Config) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, name, args...)
	}
	return DefaultRunner(ctx, name, args...)
}

// LogPath is where the service writes daemon logs.
func (c *Config) LogPath() string { return filepath.Join(c.Home, "logs", "daemon.log") }

// Install registers the daemon to start at login and restart on crash,
// then starts it now. It needs no admin rights. It returns a short
// description of the mechanism used.
func Install(ctx context.Context, c Config) (string, error) {
	if !filepath.IsAbs(c.Bin) {
		return "", fmt.Errorf("binary path must be absolute: %s", c.Bin)
	}
	if err := os.MkdirAll(filepath.Dir(c.LogPath()), 0o700); err != nil {
		return "", err
	}
	switch c.goos() {
	case "linux":
		return installLinux(ctx, c)
	case "darwin":
		return installMac(ctx, c)
	case "windows":
		return installWindows(ctx, c)
	}
	return "", fmt.Errorf("unsupported OS %s: run `shiplino daemon` yourself", c.goos())
}

// Uninstall stops the daemon and removes its registration. Missing pieces
// are not errors.
func Uninstall(ctx context.Context, c Config) error {
	switch c.goos() {
	case "linux":
		_, _ = c.run(ctx, "systemctl", "--user", "disable", "--now", unitName)
		_ = os.Remove(c.unitPath())
		_, _ = c.run(ctx, "systemctl", "--user", "daemon-reload")
		_ = os.Remove(c.autostartPath())
	case "darwin":
		_, _ = c.run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+launchLabel)
		_ = os.Remove(c.plistPath())
	case "windows":
		_, _ = c.run(ctx, "schtasks", "/End", "/TN", taskName)
		_, _ = c.run(ctx, "schtasks", "/Delete", "/TN", taskName, "/F")
	}
	return nil
}

// --- Linux: systemd --user, falling back to XDG autostart ---

const unitName = "shiplino.service"

func (c *Config) unitPath() string {
	return filepath.Join(c.UserHome, ".config", "systemd", "user", unitName)
}

func (c *Config) autostartPath() string {
	return filepath.Join(c.UserHome, ".config", "autostart", "shiplino.desktop")
}

var unitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=Shiplino daemon (records AI coding agent activity)
After=default.target

[Service]
Type=simple
ExecStart="{{.Bin}}" daemon
Environment="SHIPLINO_HOME={{.Home}}"
Restart=on-failure
RestartSec=2
StandardOutput=append:{{.Log}}
StandardError=append:{{.Log}}

[Install]
WantedBy=default.target
`))

// UnitFile renders the systemd user unit.
func UnitFile(c Config) string {
	var b bytes.Buffer
	_ = unitTmpl.Execute(&b, map[string]string{"Bin": c.Bin, "Home": c.Home, "Log": c.LogPath()})
	return b.String()
}

func installLinux(ctx context.Context, c Config) (string, error) {
	if _, err := c.run(ctx, "systemctl", "--user", "show-environment"); err == nil {
		if err := writeFile(c.unitPath(), UnitFile(c)); err != nil {
			return "", err
		}
		if out, err := c.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return "", fmt.Errorf("systemctl daemon-reload: %v: %s", err, out)
		}
		if out, err := c.run(ctx, "systemctl", "--user", "enable", "--now", unitName); err != nil {
			return "", fmt.Errorf("systemctl enable: %v: %s", err, out)
		}
		// Pick up a replaced binary when setup runs again.
		_, _ = c.run(ctx, "systemctl", "--user", "restart", unitName)
		return "systemd user service " + unitName, nil
	}
	// No user systemd (containers, WSL1, minimal desktops): start at
	// desktop login via XDG autostart, and start it now in the background.
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Shiplino\nExec=env SHIPLINO_HOME=%q %q daemon\nX-GNOME-Autostart-enabled=true\nNoDisplay=true\n", c.Home, c.Bin)
	if err := writeFile(c.autostartPath(), desktop); err != nil {
		return "", err
	}
	if err := startDetached(c); err != nil {
		return "", err
	}
	return "XDG autostart entry (no systemd user session found)", nil
}

// --- macOS: LaunchAgent ---

const launchLabel = "dev.shiplino.daemon"

func (c *Config) plistPath() string {
	return filepath.Join(c.UserHome, "Library", "LaunchAgents", launchLabel+".plist")
}

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array><string>{{.Bin}}</string><string>daemon</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>SHIPLINO_HOME</key><string>{{.Home}}</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardOutPath</key><string>{{.Log}}</string>
  <key>StandardErrorPath</key><string>{{.Log}}</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`))

// Plist renders the macOS LaunchAgent. Values are XML-escaped.
func Plist(c Config) string {
	var b bytes.Buffer
	_ = plistTmpl.Execute(&b, map[string]string{"Label": launchLabel, "Bin": xmlEscape(c.Bin), "Home": xmlEscape(c.Home), "Log": xmlEscape(c.LogPath())})
	return b.String()
}

func installMac(ctx context.Context, c Config) (string, error) {
	if err := writeFile(c.plistPath(), Plist(c)); err != nil {
		return "", err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_, _ = c.run(ctx, "launchctl", "bootout", domain+"/"+launchLabel) // reload if present
	if out, err := c.run(ctx, "launchctl", "bootstrap", domain, c.plistPath()); err != nil {
		// Older macOS: fall back to the legacy command.
		if out2, err2 := c.run(ctx, "launchctl", "load", "-w", c.plistPath()); err2 != nil {
			return "", fmt.Errorf("launchctl: %v: %s / %s", err, out, out2)
		}
	}
	return "LaunchAgent " + launchLabel, nil
}

// --- Windows: Task Scheduler logon task ---

const taskName = "Shiplino"

func installWindows(ctx context.Context, c Config) (string, error) {
	tr := fmt.Sprintf(`"%s" daemon`, c.Bin)
	if out, err := c.run(ctx, "schtasks", "/Create", "/TN", taskName, "/TR", tr, "/SC", "ONLOGON", "/RL", "LIMITED", "/F"); err != nil {
		return "", fmt.Errorf("schtasks /Create: %v: %s", err, out)
	}
	_, _ = c.run(ctx, "schtasks", "/End", "/TN", taskName)
	if out, err := c.run(ctx, "schtasks", "/Run", "/TN", taskName); err != nil {
		return "", fmt.Errorf("schtasks /Run: %v: %s", err, out)
	}
	return "Task Scheduler logon task " + taskName, nil
}

// startDetached starts the daemon in the background, outliving setup.
func startDetached(c Config) error {
	if c.Run != nil { // tests: record instead of spawning
		_, err := c.Run(context.Background(), c.Bin, "daemon")
		return err
	}
	logf, err := os.OpenFile(c.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(c.Bin, "daemon")
	cmd.Env = append(os.Environ(), "SHIPLINO_HOME="+c.Home)
	cmd.Stdout, cmd.Stderr = logf, logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// WaitHealthy waits until the daemon answers on its port, or ctx expires.
func WaitHealthy(ctx context.Context, home string, check func(port int) bool) (int, error) {
	for {
		if b, err := os.ReadFile(filepath.Join(home, "port")); err == nil {
			if port, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && check(port) {
				return port, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, errors.New("the daemon didn't start in time; see " + filepath.Join(home, "logs", "daemon.log"))
		case <-time.After(100 * time.Millisecond):
		}
	}
}
