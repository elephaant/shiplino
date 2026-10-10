package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		goos      string
		setup     func(c Config)
		fail      map[string]bool
		psOut     string // what `ps`/`tasklist` says about the locked pid
		wantCalls []string
		notCalls  []string
		wantErr   error
	}{
		{name: "nothing installed (linux)", goos: "linux", wantErr: ErrNotInstalled},
		{name: "nothing installed (mac)", goos: "darwin", wantErr: ErrNotInstalled},
		{name: "nothing installed (windows)", goos: "windows", fail: map[string]bool{"schtasks /Query": true}, wantErr: ErrNotInstalled},
		{name: "systemd", goos: "linux", setup: func(c Config) { writeFile(c.unitPath(), "x") },
			wantCalls: []string{"systemctl --user restart shiplino.service"}},
		{name: "launchd", goos: "darwin", setup: func(c Config) { writeFile(c.plistPath(), "x") },
			wantCalls: []string{"launchctl kickstart -k gui/"}},
		{name: "task scheduler", goos: "windows",
			wantCalls: []string{"schtasks /End /TN Shiplino", "schtasks /Run /TN Shiplino"}},
		{name: "xdg autostart stops the old daemon", goos: "linux", psOut: "shiplino\n",
			setup: func(c Config) {
				writeFile(c.autostartPath(), "x")
				writeFile(filepath.Join(c.Home, "daemon.lock"), "4242")
			},
			wantCalls: []string{"ps -p 4242 -o comm=", "kill 4242", "daemon"}},
		{name: "stale lock never kills another program", goos: "linux", psOut: "firefox\n",
			setup: func(c Config) {
				writeFile(c.autostartPath(), "x")
				writeFile(filepath.Join(c.Home, "daemon.lock"), "4242")
			},
			wantCalls: []string{"ps -p 4242 -o comm="}, notCalls: []string{"kill"}},
		{name: "windows stops a self-restarted daemon", goos: "windows", psOut: `"shiplino.exe","4242"`,
			setup:     func(c Config) { writeFile(filepath.Join(c.Home, "daemon.lock"), "4242") },
			wantCalls: []string{"schtasks /End", "tasklist /FI PID eq 4242", "kill 4242", "schtasks /Run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{fail: tc.fail}
			c := cfg(t, tc.goos, r)
			c.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				out, err := r.run(ctx, name, args...)
				if name == "ps" || name == "tasklist" {
					return []byte(tc.psOut), err
				}
				return out, err
			}
			if tc.setup != nil {
				tc.setup(c)
			}
			_, err := Restart(context.Background(), c)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			for _, want := range tc.wantCalls {
				if !r.ran(want) && !ranSuffix(r, want) {
					t.Errorf("missing call %q in %v", want, r.calls)
				}
			}
			for _, not := range tc.notCalls {
				if r.ran(not) {
					t.Errorf("unexpected call %q in %v", not, r.calls)
				}
			}
		})
	}
}

func ranSuffix(r *recorder, suffix string) bool {
	for _, c := range r.calls {
		if strings.HasSuffix(strings.Join(c, " "), suffix) {
			return true
		}
	}
	return false
}
