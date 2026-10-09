// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct {
	calls [][]string
	fail  map[string]bool // command line prefix → fail
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, append([]string{name}, args...))
	for prefix := range r.fail {
		if strings.HasPrefix(line, prefix) {
			return []byte("nope"), errors.New("exit status 1")
		}
	}
	return nil, nil
}

func (r *recorder) ran(prefix string) bool {
	for _, c := range r.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func cfg(t *testing.T, goos string, r *recorder) Config {
	u := t.TempDir()
	return Config{Bin: filepath.Join(u, ".shiplino", "bin", "shiplino"), Home: filepath.Join(u, ".shiplino"), UserHome: u, GOOS: goos, Run: r.run}
}

func TestLinuxSystemd(t *testing.T) {
	r := &recorder{}
	c := cfg(t, "linux", r)
	how, err := Install(context.Background(), c)
	if err != nil || !strings.Contains(how, "systemd") {
		t.Fatalf("%q %v", how, err)
	}
	unit, err := os.ReadFile(c.unitPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`ExecStart="` + c.Bin + `" daemon`, "Restart=on-failure", "WantedBy=default.target", `Environment="SHIPLINO_HOME=` + c.Home + `"`} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if !r.ran("systemctl --user enable --now shiplino.service") {
		t.Fatalf("calls: %v", r.calls)
	}
	Uninstall(context.Background(), c)
	if _, err := os.Stat(c.unitPath()); !os.IsNotExist(err) {
		t.Fatal("unit not removed")
	}
	if !r.ran("systemctl --user disable --now shiplino.service") {
		t.Fatal("not disabled")
	}
}

func TestLinuxWithoutSystemdFallsBack(t *testing.T) {
	r := &recorder{fail: map[string]bool{"systemctl": true}}
	c := cfg(t, "linux", r)
	how, err := Install(context.Background(), c)
	if err != nil || !strings.Contains(how, "autostart") {
		t.Fatalf("%q %v", how, err)
	}
	desktop, _ := os.ReadFile(c.autostartPath())
	if !strings.Contains(string(desktop), "shiplino") || !strings.Contains(string(desktop), " daemon") {
		t.Fatalf("desktop entry:\n%s", desktop)
	}
	if !r.ran(c.Bin + " daemon") {
		t.Fatal("daemon not started now")
	}
}

func TestLinuxEnableFailureIsReported(t *testing.T) {
	r := &recorder{fail: map[string]bool{"systemctl --user enable": true}}
	if _, err := Install(context.Background(), cfg(t, "linux", r)); err == nil {
		t.Fatal("enable failure swallowed")
	}
}

func TestMacLaunchAgent(t *testing.T) {
	r := &recorder{}
	c := cfg(t, "darwin", r)
	c.Bin = filepath.Join(c.UserHome, "a&b", "shiplino") // absolute on any host
	how, err := Install(context.Background(), c)
	if err != nil || !strings.Contains(how, "LaunchAgent") {
		t.Fatalf("%q %v", how, err)
	}
	plist, _ := os.ReadFile(c.plistPath())
	for _, want := range []string{"<string>dev.shiplino.daemon</string>", "a&amp;b", "<key>RunAtLoad</key><true/>", "<key>KeepAlive</key>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if !r.ran("launchctl bootstrap gui/") {
		t.Fatalf("calls: %v", r.calls)
	}
	// Older macOS without bootstrap: legacy load.
	r2 := &recorder{fail: map[string]bool{"launchctl bootstrap": true}}
	c2 := cfg(t, "darwin", r2)
	if _, err := Install(context.Background(), c2); err != nil || !r2.ran("launchctl load -w") {
		t.Fatalf("fallback: %v %v", err, r2.calls)
	}
}

func TestWindowsTask(t *testing.T) {
	r := &recorder{}
	c := cfg(t, "windows", r)
	how, err := Install(context.Background(), c)
	if err != nil || !strings.Contains(how, "Task Scheduler") {
		t.Fatalf("%q %v", how, err)
	}
	if !r.ran("schtasks /Create /TN Shiplino /TR \""+c.Bin+"\" daemon /SC ONLOGON /RL LIMITED /F") || !r.ran("schtasks /Run /TN Shiplino") {
		t.Fatalf("calls: %v", r.calls)
	}
	Uninstall(context.Background(), c)
	if !r.ran("schtasks /Delete /TN Shiplino /F") {
		t.Fatal("task not deleted")
	}
}

func TestRelativeBinRejectedAndUnknownOS(t *testing.T) {
	r := &recorder{}
	c := cfg(t, "linux", r)
	c.Bin = "shiplino"
	if _, err := Install(context.Background(), c); err == nil {
		t.Fatal("relative bin accepted")
	}
	c = cfg(t, "plan9", r)
	if _, err := Install(context.Background(), c); err == nil {
		t.Fatal("unknown OS accepted")
	}
}

func TestWaitHealthy(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WaitHealthy(ctx, home, func(int) bool { return true }); err == nil {
		t.Fatal("expected timeout without a port file")
	}
	os.WriteFile(filepath.Join(home, "port"), []byte("4777\n"), 0o600)
	if p, err := WaitHealthy(context.Background(), home, func(p int) bool { return p == 4777 }); err != nil || p != 4777 {
		t.Fatalf("%d %v", p, err)
	}
}
