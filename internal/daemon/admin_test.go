package daemon

import (
	"context"
	"errors"
	"github.com/elephaant/shiplino/internal/budget"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
)

func TestAdmin(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700) // Codex is "installed", not connected
	var sent []notify.Note
	a := &admin{d: e.d, home: e.home, cfg: config.Config{}, version: "test", port: 4777,
		send: func(_ context.Context, n notify.Note) error { sent = append(sent, n); return nil }}

	v := a.Settings(ctx).(SettingsView)
	if v.CaptureLevel != "standard" || !v.Notify.Enabled || v.Notify.MinTurnMS != 30_000 || v.Paused {
		t.Fatalf("settings: %+v", v)
	}
	var codex bool
	for _, s := range v.Agents {
		if s.ID == "codex" {
			codex = s.Found && !s.Connected
		}
	}
	if !codex {
		t.Fatalf("agents: %+v", v.Agents)
	}
	a.Pause(time.Now().Add(time.Hour))
	if v := a.Settings(ctx).(SettingsView); !v.Paused || v.PausedUntil.IsZero() {
		t.Fatalf("paused: %+v", v)
	}
	a.Resume()
	if v := a.Settings(ctx).(SettingsView); v.Paused {
		t.Fatal("still paused")
	}
	if v := a.Settings(ctx).(SettingsView); len(v.Budget.Spends) != 0 {
		t.Fatalf("no budgets configured: %+v", v.Budget)
	}
	a.budget = budget.New(budget.Config{DailyUSD: 10, Digest: "18:00"}, e.st, func(context.Context, notify.Note) error { return nil })
	a.cfg.Budget.Digest = "18:00"
	if v := a.Settings(ctx).(SettingsView); len(v.Budget.Spends) != 1 || v.Budget.Spends[0].LimitUSD != 10 || v.Budget.Digest != "18:00" {
		t.Fatalf("budget view: %+v", v.Budget)
	}
	if err := a.TestNotification(ctx); err != nil || len(sent) != 1 {
		t.Fatalf("notify: %v %v", err, sent)
	}
}

func TestAdminConnectAndRemoveAgent(t *testing.T) {
	home := withHome(t)
	t.Setenv("PATH", "")
	e := newEnv(t)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700) // Codex is installed
	hooks := filepath.Join(home, ".codex", "hooks.json")
	a := &admin{d: e.d, home: e.home, version: "test"}

	// Setup hasn't installed the binary yet: nothing to point hooks at.
	if _, err := a.AgentPreview(ctx, "codex", true); err == nil || !strings.Contains(err.Error(), "shiplino setup") {
		t.Fatalf("no binary: %v", err)
	}
	os.MkdirAll(filepath.Dir(a.bin()), 0o700)
	os.WriteFile(a.bin(), []byte("bin"), 0o700)
	if _, err := a.AgentPreview(ctx, "nope", true); !errors.Is(err, api.ErrUnknownAgent) {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, err := a.AgentPreview(ctx, "cursor", true); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("missing agent: %v", err)
	}

	v, err := a.AgentPreview(ctx, "codex", true)
	plan := v.(AgentPlan)
	if err != nil || plan.Action != "connect" || len(plan.Changes) != 1 || plan.Note == "" {
		t.Fatalf("preview: %v %+v", err, plan)
	}
	want := filepath.Join("~", ".codex", "hooks.json")
	if c := plan.Changes[0]; c.Path != want || !strings.HasPrefix(c.Diff, "--- /dev/null\n+++ "+want+"\n") || !strings.Contains(c.Diff, "hook --agent codex") {
		t.Fatalf("diff: %+v", c)
	}
	if _, err := os.Stat(hooks); !os.IsNotExist(err) {
		t.Fatal("preview wrote the config")
	}

	if err := a.AgentChange(ctx, "codex", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hooks); !strings.Contains(plan.Changes[0].Diff, "+"+strings.SplitN(string(b), "\n", 2)[0]) {
		t.Fatalf("connect wrote something else:\n%s", b)
	}
	for _, s := range a.Settings(ctx).(SettingsView).Agents {
		if s.Key == "codex" && (!s.Connected || !s.Current) {
			t.Fatalf("not connected: %+v", s)
		}
	}

	v, _ = a.AgentPreview(ctx, "codex", false)
	if plan := v.(AgentPlan); len(plan.Changes) != 1 || !strings.Contains(plan.Changes[0].Diff, "-") {
		t.Fatalf("remove preview: %+v", plan)
	}
	if err := a.AgentChange(ctx, "codex", false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(hooks); err == nil && strings.Contains(string(b), "shiplino") {
		t.Fatalf("hooks left:\n%s", b)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.home, "backups", "codex")); len(entries) != 1 {
		t.Fatalf("remove backed up %d files", len(entries))
	}

	// A config Shiplino won't edit is reported and left alone.
	os.WriteFile(hooks, []byte("{ // mine\n}\n"), 0o600)
	v, _ = a.AgentPreview(ctx, "codex", true)
	if plan := v.(AgentPlan); len(plan.Changes) != 0 || !strings.Contains(plan.Problem, "isn't plain JSON") {
		t.Fatalf("commented preview: %+v", plan)
	}
	if err := a.AgentChange(ctx, "codex", true); err == nil || !strings.Contains(err.Error(), "untouched") {
		t.Fatalf("commented connect: %v", err)
	}
	if b, _ := os.ReadFile(hooks); string(b) != "{ // mine\n}\n" {
		t.Fatal("commented config changed")
	}
}
