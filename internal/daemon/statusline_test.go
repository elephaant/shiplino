package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/agents"
	"github.com/elephaant/shiplino/internal/limits"
	"github.com/elephaant/shiplino/internal/statusline"
)

// Claude Code's status line input, recorded by the wrapper, becomes the
// reported plan windows: the header shows Claude's own percentages and
// the token estimates step aside. It doesn't count as session activity.
func TestStatusLineLimits(t *testing.T) {
	e := newEnv(t)
	e.hookFixture()
	e.poll()
	before := e.session(sid)

	resets5h := time.Now().Add(2 * time.Hour).Unix()
	resets7d := time.Now().Add(72 * time.Hour).Unix()
	in := `{"session_id":"sess-0001","version":"2.1.300","model":{"id":"claude-sonnet-5-5"},"cwd":"/home/dev/app",` +
		`"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":` + strconv.FormatInt(resets5h, 10) + `},"seven_day":{"used_percentage":41,"resets_at":` + strconv.FormatInt(resets7d, 10) + `}}}`
	for range 3 { // a status line runs after every response, often with the same numbers
		if code := statusline.Run(nil, strings.NewReader(in), io.Discard, io.Discard); code != 0 {
			t.Fatalf("exit = %d", code)
		}
	}
	n := e.eventCount()
	e.poll()
	if got := e.eventCount() - n; got != 2 {
		t.Errorf("%d limit events stored, want 2 (one per window, repeats deduplicated)", got)
	}
	after := e.session(sid)
	if !after.LastEventAt.Equal(before.LastEventAt) || after.Status != before.Status {
		t.Errorf("session touched: %v %s → %v %s", before.LastEventAt, before.Status, after.LastEventAt, after.Status)
	}

	st, err := limits.New(limits.Config{}, e.st, nil).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Windows) != 2 {
		t.Fatalf("windows: %+v", st.Windows)
	}
	for i, want := range []struct {
		window string
		used   float64
		resets int64
	}{{"5h", 23.5, resets5h}, {"7d", 41, resets7d}} {
		w := st.Windows[i]
		if w.Agent != "claude-code" || w.Window != want.window || w.Source != "reported" || w.UsedPercent == nil || *w.UsedPercent != want.used || w.ResetsAt.Unix() != want.resets {
			t.Errorf("window %d: %+v", i, w)
		}
	}
	if st.Plans["claude-code"] != limits.PlanSubscription {
		t.Errorf("plans: %v", st.Plans)
	}
}

// The settings page turns the opt-in status line wrapper on and off with
// the same preview → connect → remove flow as an agent's hooks.
func TestAdminStatusLineToggle(t *testing.T) {
	home := withHome(t)
	t.Setenv("PATH", "")
	e := newEnv(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	original := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"~/.claude/line.sh\"\n  }\n}\n"
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(original), 0o600)
	a := &admin{d: e.d, home: e.home, version: "test"}
	os.MkdirAll(filepath.Dir(a.bin()), 0o700)
	os.WriteFile(a.bin(), []byte("bin"), 0o700)
	const key = "claude-code-status-line"

	state := func() agents.Status {
		for _, s := range a.Settings(ctx).(SettingsView).Agents {
			if s.Key == key {
				return s
			}
		}
		t.Fatal("no status line entry")
		return agents.Status{}
	}
	if s := state(); !s.OptIn || s.About == "" || s.Connected || !s.Found {
		t.Fatalf("before: %+v", s)
	}
	v, err := a.AgentPreview(ctx, key, true)
	plan := v.(AgentPlan)
	if err != nil || len(plan.Changes) != 1 || !strings.Contains(plan.Changes[0].Diff, "-    \"command\": \"~/.claude/line.sh\"") ||
		!strings.Contains(plan.Changes[0].Diff, " statusline --wrap ") {
		t.Fatalf("preview: %v %+v", err, plan)
	}
	if err := a.AgentChange(ctx, key, true); err != nil {
		t.Fatal(err)
	}
	if s := state(); !s.Connected || !s.Current {
		t.Fatalf("after connect: %+v", s)
	}
	if err := a.AgentChange(ctx, key, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(settings); string(b) != original {
		t.Fatalf("not restored:\n%s", b)
	}
}
