package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type apiSession struct {
	ID       string  `json:"id"`
	Agent    string  `json:"agent"`
	ParentID string  `json:"parent_id"`
	Project  string  `json:"project_id"`
	Status   string  `json:"status"`
	Turns    int     `json:"turns"`
	Cost     float64 `json:"best_cost_usd"`
	Plan     int     `json:"plan_total"`
	Waiting  string  `json:"waiting_reason"`
	Links    []struct {
		Kind string `json:"kind"`
	} `json:"links"`
}

// TestDemo runs the whole demo: the seeded board has every column, agent
// and project, the live sessions keep working, and stopping deletes it.
func TestDemo(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("USERPROFILE", realHome)
	t.Setenv("SHIPLINO_HOME", filepath.Join(realHome, ".shiplino"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type started struct{ url, home string }
	ready := make(chan started, 1)
	done := make(chan error, 1)
	var out strings.Builder
	go func() {
		done <- Run(ctx, Config{Version: "test", Out: &out, Pace: 5 * time.Millisecond, Ready: func(u, h string) { ready <- started{u, h} }})
	}()
	var st started
	t0 := time.Now()
	defer func() { t.Logf("took %s; output:\n%s", time.Since(t0), out.String()) }()
	select {
	case st = <-ready:
		t.Logf("ready after %s", time.Since(t0))
	case err := <-done:
		t.Fatalf("demo stopped early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the demo didn't start within 30s")
	}
	tok, err := os.ReadFile(filepath.Join(st.home, "token"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, v any) {
		t.Helper()
		req, _ := http.NewRequest("GET", st.url+path, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %s", path, resp.Status)
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}

	var status struct{ Demo bool }
	get("/api/v1/status", &status)
	if !status.Demo {
		t.Error("/api/v1/status doesn't report demo mode")
	}

	var list struct{ Sessions []apiSession }
	get("/api/v1/sessions?limit=1000", &list)
	statuses, agents, projects, links := map[string]int{}, map[string]float64{}, map[string]bool{}, map[string]int{}
	todos := map[string]bool{}
	subagents := 0
	for _, s := range list.Sessions {
		if s.ParentID != "" {
			subagents++
			continue
		}
		statuses[s.Status]++
		if s.Status == "waiting" {
			statuses["waiting:"+s.Waiting]++
		}
		agents[s.Agent] += s.Cost
		todos[s.Agent] = todos[s.Agent] || s.Plan > 0
		projects[s.Project] = true
		for _, l := range s.Links {
			links[l.Kind]++
		}
	}
	for _, want := range []string{"running", "waiting", "review", "done", "failed", "waiting:permission", "waiting:question", "waiting:idle"} {
		if statuses[want] == 0 {
			t.Errorf("no %s session: %v", want, statuses)
		}
	}
	for _, a := range []string{claude, codex, cursor, gemini} {
		if agents[a] <= 0 {
			t.Errorf("no cost recorded for %s: %v", a, agents)
		}
		if !todos[a] {
			t.Errorf("no todo list recorded for %s", a)
		}
	}
	if len(projects) != 4 || !projects["example.com/acme/storefront"] {
		t.Errorf("projects = %v", projects)
	}
	if subagents == 0 || links["commit"] == 0 || links["pr"] == 0 {
		t.Errorf("subagents %d, links %v", subagents, links)
	}

	var limits struct {
		Windows []struct {
			Agent       string   `json:"agent"`
			Window      string   `json:"window"`
			UsedPercent *float64 `json:"used_percent"`
		}
		Plans map[string]string
	}
	get("/api/v1/limits", &limits)
	reported := false
	for _, w := range limits.Windows {
		reported = reported || w.Agent == codex && w.UsedPercent != nil
	}
	if !reported || limits.Plans[claude] != "plan" {
		t.Errorf("limits = %+v", limits)
	}

	var board struct {
		Columns []struct {
			ID    string `json:"id"`
			Cards []any  `json:"cards"`
		}
	}
	get("/api/v1/projects/"+url.PathEscape("example.com/acme/storefront")+"/board", &board)
	backlog := 0
	for _, c := range board.Columns {
		if c.ID == "backlog" {
			backlog = len(c.Cards)
		}
	}
	if backlog == 0 {
		t.Errorf("no backlog cards: %+v", board)
	}

	// The live sessions keep taking new prompts.
	deadline := time.Now().Add(20 * time.Second)
	for {
		get("/api/v1/sessions?limit=1000", &list)
		live := 0
		for _, s := range list.Sessions {
			if s.ParentID == "" && s.Turns >= 3 && strings.Contains(s.ID, "demo-") {
				live++
			}
		}
		if live > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no live session moved on within 20s")
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("demo: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the demo didn't stop within 30s")
	}
	if _, err := os.Stat(filepath.Dir(st.home)); !os.IsNotExist(err) {
		t.Errorf("the demo folder is still there: %v", err)
	}
	if os.Getenv("HOME") != realHome {
		t.Error("HOME wasn't restored")
	}
	if entries, _ := os.ReadDir(realHome); len(entries) != 0 {
		t.Errorf("the demo wrote to the real home: %v", entries)
	}
	if !strings.Contains(out.String(), st.url) || !strings.Contains(out.String(), "deleted") {
		t.Errorf("output = %q", out.String())
	}
}

// TestSeedIsDeterministic: the same seed gives the same sessions, so
// screenshots and tests are stable.
func TestSeedIsDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	seed := func() []string {
		dir := t.TempDir()
		live, events, err := newScript(&writer{spool: filepath.Join(dir, "spool"), home: filepath.Join(dir, "home")}).seed(now)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, s := range live {
			out = append(out, s.id+" "+s.project.name)
		}
		for _, e := range events {
			out = append(out, e.SessionID+" "+string(e.Kind)+" "+e.TS.String())
		}
		return out
	}
	a, b := seed(), seed()
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("seeds differ:\n%v\n%v", a, b)
	}
	if len(a) < 20 {
		t.Errorf("only %d live sessions and git events", len(a))
	}
}

func TestFakeGit(t *testing.T) {
	g := fakeGit{}
	for _, tc := range []struct{ dir, remote string }{
		{"/home/dev/code/storefront", "https://example.com/acme/storefront.git"},
		{"/home/dev/code/storefront/src/lib", "https://example.com/acme/storefront.git"},
		{"/home/dev/code/storefront-old", ""},
		{"/home/dev", ""},
	} {
		if got := g.Remote(tc.dir); got != tc.remote {
			t.Errorf("Remote(%s) = %q, want %q", tc.dir, got, tc.remote)
		}
		if (g.CommonDir(tc.dir) != "") != (tc.remote != "") {
			t.Errorf("CommonDir(%s) = %q", tc.dir, g.CommonDir(tc.dir))
		}
	}
}
