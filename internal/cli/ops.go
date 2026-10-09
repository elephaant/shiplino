// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/service"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/engine"
)

// client talks to the local daemon API.
type client struct {
	base, token string
	http        http.Client
}

var errDaemonDown = errors.New("the daemon isn't running (start it with `shiplino setup` or `shiplino daemon`)")

func (e *env) client() (*client, error) {
	pb, err := os.ReadFile(filepath.Join(e.home, "port"))
	if err != nil {
		return nil, errDaemonDown
	}
	tb, err := os.ReadFile(filepath.Join(e.home, "token"))
	if err != nil {
		return nil, errDaemonDown
	}
	c := &client{base: "http://127.0.0.1:" + strings.TrimSpace(string(pb)), token: strings.TrimSpace(string(tb)), http: http.Client{Timeout: 3 * time.Second}}
	return c, nil
}

func (c *client) get(ctx context.Context, path string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return errDaemonDown
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned %s for %s", resp.Status, path)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

type statusResp struct {
	Version string `json:"version"`
	Daemon  struct {
		Lines             int64 `json:"lines"`
		Events            int64 `json:"events"`
		Unknown           int64 `json:"unknown"`
		Bad               int64 `json:"bad"`
		SpoolBacklogBytes int64 `json:"spool_backlog_bytes"`
		Transcripts       int   `json:"transcripts"`
		Paused            bool  `json:"paused"`
	} `json:"daemon"`
}

func (c *client) sessions(ctx context.Context, limit int) ([]engine.Session, error) {
	var r struct{ Sessions []engine.Session }
	err := c.get(ctx, "/api/v1/sessions?limit="+strconv.Itoa(limit), &r)
	return r.Sessions, err
}

// status prints daemon state and what's live right now.
func status(ctx context.Context, e *env, args []string) int {
	c, err := e.client()
	var st statusResp
	if err == nil {
		err = c.get(ctx, "/api/v1/status", &st)
	}
	if err != nil {
		fmt.Fprintln(e.out, "○ "+err.Error())
		return 1
	}
	fmt.Fprintf(e.out, "● daemon %s running at %s\n", st.Version, strings.Replace(c.base, "127.0.0.1", "localhost", 1))
	if spool.Paused(e.home, time.Now()) {
		fmt.Fprintln(e.out, "⏸  recording is paused (`shiplino resume`)")
	}
	list, err := c.sessions(ctx, 200)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	var running, waiting []engine.Session
	for _, s := range list {
		if s.ParentID != "" {
			continue
		}
		switch s.Status {
		case engine.StatusRunning:
			running = append(running, s)
		case engine.StatusWaiting:
			waiting = append(waiting, s)
		}
	}
	fmt.Fprintf(e.out, "  %d running · %d waiting on you\n", len(running), len(waiting))
	for _, s := range append(waiting, running...) {
		mark := "●"
		if s.Status == engine.StatusWaiting {
			mark = "⚠"
		}
		fmt.Fprintf(e.out, "  %s %-12s %-40s %s\n", mark, s.Agent, clip(title(s), 40), clip(s.NowDoing, 50))
	}
	return 0
}

// ls lists recent sessions.
func ls(ctx context.Context, e *env, args []string) int {
	c, err := e.client()
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	list, err := c.sessions(ctx, 1000)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	onlyRunning, today := hasFlag(args, "--running"), hasFlag(args, "--today")
	midnight := time.Now().Truncate(24 * time.Hour)
	n := 0
	fmt.Fprintf(e.out, "%-10s %-12s %-42s %8s %9s  %s\n", "STATUS", "AGENT", "TITLE", "COST", "FILES", "LAST")
	for _, s := range list {
		if s.ParentID != "" ||
			onlyRunning && s.Status != engine.StatusRunning && s.Status != engine.StatusWaiting ||
			today && s.LastEventAt.Before(midnight) {
			continue
		}
		cost := "—"
		if s.BestCostUSD > 0 {
			cost = fmt.Sprintf("$%.2f", s.BestCostUSD)
		}
		fmt.Fprintf(e.out, "%-10s %-12s %-42s %8s %9s  %s\n", s.Status, s.Agent, clip(title(s), 42), cost,
			fmt.Sprintf("%d +%d", len(s.Files), s.LinesAdded), ago(s.LastEventAt))
		if n++; n >= 50 {
			break
		}
	}
	if n == 0 {
		fmt.Fprintln(e.out, "(no sessions yet: start any agent)")
	}
	return 0
}

// pause stops recording until resume, or for a duration (--for 1h).
func pause(ctx context.Context, e *env, args []string) int {
	content := ""
	for i, a := range args {
		if a == "--for" && i+1 < len(args) {
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				fmt.Fprintf(e.errOut, "invalid duration %q (try 30m or 2h)\n", args[i+1])
				return 2
			}
			content = strconv.FormatInt(time.Now().Add(d).Unix(), 10)
		}
	}
	if err := os.MkdirAll(e.home, 0o700); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(e.home, "paused"), []byte(content), 0o600); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if content == "" {
		fmt.Fprintln(e.out, "⏸  recording paused until `shiplino resume` (hooks stay installed and still cost nothing)")
	} else {
		fmt.Fprintf(e.out, "⏸  recording paused for %s\n", args[len(args)-1])
	}
	return 0
}

func resume(ctx context.Context, e *env, args []string) int {
	if err := os.Remove(filepath.Join(e.home, "paused")); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	fmt.Fprintln(e.out, "▶  recording resumed")
	return 0
}

// open opens the board in the default browser.
func open(ctx context.Context, e *env, args []string) int {
	c, err := e.client()
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	url := strings.Replace(c.base, "127.0.0.1", "localhost", 1)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(e.out, "Open %s in your browser.\n", url)
		return 0
	}
	fmt.Fprintf(e.out, "Opening %s\n", url)
	return 0
}

// check is one doctor finding.
type check struct {
	ok      bool
	warn    bool
	name    string
	detail  string
	fix     func(context.Context) error
	fixHint string
}

// doctor checks every part of the installation and explains problems.
func doctor(ctx context.Context, e *env, args []string) int {
	fix := hasFlag(args, "--fix")
	var checks []check

	bin := e.binPath()
	if fi, err := os.Stat(bin); err != nil {
		checks = append(checks, check{name: "Binary", detail: bin + " is missing", fixHint: "shiplino setup"})
	} else {
		checks = append(checks, check{ok: true, name: "Binary", detail: fmt.Sprintf("%s (%.1f MB)", tilde(bin, e.userHome), float64(fi.Size())/1e6)})
	}

	det := claudecode.Detect(ctx, e.userHome)
	if det.Installed {
		ok, cmd, err := claudecode.Installed(det.SettingsPath)
		reinstall := func(ctx context.Context) error {
			_, err := claudecode.Install(det.SettingsPath, bin, det.Version, e.backupDir(claudecode.Name))
			return err
		}
		switch {
		case errors.Is(err, claudecode.ErrUnparseable):
			checks = append(checks, check{name: "Claude Code", detail: tilde(det.SettingsPath, e.userHome) + " isn't plain JSON; Shiplino won't edit it", fixHint: "remove comments, then run shiplino setup"})
		case err != nil:
			checks = append(checks, check{name: "Claude Code", detail: err.Error()})
		case !ok:
			checks = append(checks, check{name: "Claude Code", detail: "hooks missing (an update may have reset the settings)", fix: reinstall, fixHint: "shiplino doctor --fix"})
		case cmd != bin:
			checks = append(checks, check{name: "Claude Code", detail: "hooks point at " + cmd + ", not " + bin, fix: reinstall, fixHint: "shiplino doctor --fix"})
		default:
			checks = append(checks, check{ok: true, name: "Claude Code", detail: strings.TrimSpace(det.Version + " hooks installed")})
		}
	} else {
		checks = append(checks, check{ok: true, warn: true, name: "Claude Code", detail: "not found"})
	}

	c, err := e.client()
	var st statusResp
	if err == nil {
		err = c.get(ctx, "/api/v1/status", &st)
	}
	startService := func(ctx context.Context) error {
		if _, err := service.Install(ctx, e.serviceConfig()); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		_, err := service.WaitHealthy(wctx, e.home, healthy)
		return err
	}
	if err != nil {
		checks = append(checks, check{name: "Daemon", detail: "not running", fix: startService, fixHint: "shiplino doctor --fix"})
	} else {
		checks = append(checks, check{ok: true, name: "Daemon", detail: "running at " + strings.Replace(c.base, "127.0.0.1", "localhost", 1)})
		d := st.Daemon
		switch {
		case d.SpoolBacklogBytes > 1<<20:
			checks = append(checks, check{ok: true, warn: true, name: "Backlog", detail: fmt.Sprintf("%.1f MB of events waiting to be processed", float64(d.SpoolBacklogBytes)/1e6)})
		default:
			checks = append(checks, check{ok: true, name: "Backlog", detail: "up to date"})
		}
		if d.Bad > 0 || d.Unknown > 0 {
			checks = append(checks, check{ok: true, warn: true, name: "Parsing", detail: fmt.Sprintf("%d unreadable and %d unknown lines since start (an agent update may have changed its format)", d.Bad, d.Unknown)})
		}
		if list, err := c.sessions(ctx, 1000); err == nil {
			last := map[string]time.Time{}
			for _, s := range list {
				if s.LastEventAt.After(last[s.Agent]) {
					last[s.Agent] = s.LastEventAt
				}
			}
			agents := make([]string, 0, len(last))
			for a := range last {
				agents = append(agents, a)
			}
			sort.Strings(agents)
			for _, a := range agents {
				checks = append(checks, check{ok: true, name: "Last event", detail: fmt.Sprintf("%s %s", a, ago(last[a]))})
			}
		}
	}
	if spool.Paused(e.home, time.Now()) {
		checks = append(checks, check{ok: true, warn: true, name: "Recording", detail: "paused", fixHint: "shiplino resume"})
	}

	failed := 0
	for _, ch := range checks {
		if !ch.ok && fix && ch.fix != nil {
			if err := ch.fix(ctx); err != nil {
				ch.detail += " — fix failed: " + err.Error()
			} else {
				ch.ok, ch.detail = true, ch.detail+" — fixed"
			}
		}
		mark := "✅"
		switch {
		case !ch.ok:
			mark = "❌"
			failed++
		case ch.warn:
			mark = "⚠️ "
		}
		line := fmt.Sprintf("  %s %-12s %s", mark, ch.name, ch.detail)
		if !ch.ok && ch.fixHint != "" {
			line += "  → " + ch.fixHint
		}
		fmt.Fprintln(e.out, line)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func title(s engine.Session) string {
	if s.Title != "" {
		return s.Title
	}
	return "Session " + s.ID
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "never"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
