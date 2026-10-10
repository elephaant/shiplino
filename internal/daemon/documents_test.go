package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// copyFile copies a fixture to dst and sets its modification time.
func copyFile(t *testing.T, src, dst string, mod time.Time) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(dst), 0o700)
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dst, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func fastDocs(t *testing.T) {
	every, recheck := docEvery, docRecheck
	docEvery, docRecheck = 0, 0
	t.Cleanup(func() { docEvery, docRecheck = every, recheck })
}

const clineFixtures = "../../pkg/adapters/cline/testdata/source-2026-10-10/data"

// Cline's classic task file is rewritten in place: its calls are counted
// once each, the last one when it's final, and never again on rereads.
func TestClineTaskFileUsage(t *testing.T) {
	home := withHome(t)
	fastDocs(t)
	e := newEnv(t)
	task := filepath.Join(home, ".cline", "data", "tasks", "1791640000000", "ui_messages.json")
	copyFile(t, filepath.Join(clineFixtures, "tasks", "1791640000000", "ui_messages.json"), task, time.Now())
	e.d.Backfill(time.Now().Add(-time.Hour))
	e.poll()

	s := e.session("cline:1791640000000")
	if s.Usage != "tokens" || s.OutputTokens != 460 || s.CacheWriteTokens != 8200 || s.CostSource == "" {
		t.Fatalf("busy file: out=%d cw=%d usage=%s cost=%v", s.OutputTokens, s.CacheWriteTokens, s.Usage, s.BestCostUSD)
	}
	// Reread unchanged, then once quiet: the last call is added, once.
	e.poll()
	quiet := time.Now().Add(-3 * time.Minute)
	os.Chtimes(task, quiet, quiet)
	e.poll()
	e.poll()
	s = e.session("cline:1791640000000")
	if s.OutputTokens != 1070 || s.CacheWriteTokens != 8600 {
		t.Fatalf("quiet file: out=%d cw=%d", s.OutputTokens, s.CacheWriteTokens)
	}
	n := e.eventCount()
	e.restart()
	e.d.Backfill(time.Now().Add(-time.Hour))
	e.poll()
	if got := e.eventCount(); got != n {
		t.Fatalf("events after restart: %d, want %d", got, n)
	}
}

func TestClineSessionFileUsage(t *testing.T) {
	home := withHome(t)
	fastDocs(t)
	e := newEnv(t)
	for _, f := range []string{"sess-a1.messages.json", "explorer.messages.json"} {
		copyFile(t, filepath.Join(clineFixtures, "sessions", "sess-a1", f), filepath.Join(home, ".cline", "data", "sessions", "sess-a1", f), time.Now())
	}
	e.poll() // discovery runs on the first pass

	s := e.session("cline:sess-a1")
	if s.OutputTokens != 305 || s.CostSource == "" || s.TreeCostUSD <= 0.0373 {
		t.Fatalf("lead: out=%d cost=%v tree=%v source=%s", s.OutputTokens, s.BestCostUSD, s.TreeCostUSD, s.CostSource)
	}
	sub := e.session("cline:sess-a1/sub:explorer")
	if sub.ParentID != "cline:sess-a1" || sub.InputTokens != 1500 {
		t.Fatalf("subagent: %+v", sub)
	}
}

func TestDocumentTooBig(t *testing.T) {
	home := withHome(t)
	fastDocs(t)
	big := maxDoc
	maxDoc = 10
	t.Cleanup(func() { maxDoc = big })
	e := newEnv(t)
	copyFile(t, filepath.Join(clineFixtures, "sessions", "sess-a1", "sess-a1.messages.json"), filepath.Join(home, ".cline", "data", "sessions", "sess-a1", "sess-a1.messages.json"), time.Now())
	e.poll()
	e.poll()
	if st := e.d.Stats(); st.Bad != 1 {
		t.Fatalf("bad = %d, want 1 (reported once)", st.Bad)
	}
}

// Copilot CLI's session log: usage from its shutdown records.
func TestCopilotSessionLogUsage(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	const id = "3f0c9a52-7d1e-4b8a-9c11-2a6e5f4d8b70"
	copyFile(t, filepath.Join("../../pkg/adapters/copilotcli/testdata/docs-2026.10/session-state", id, "events.jsonl"),
		filepath.Join(home, ".copilot", "session-state", id, "events.jsonl"), time.Now())
	e.poll()
	s := e.session("copilot-cli:" + id)
	if s.InputTokens != 5000 || s.OutputTokens != 1700 || s.CacheReadTokens != 15000 || s.CostSource != "reported" || s.BestCostUSD < 0.0699 {
		t.Fatalf("session: in=%d out=%d cr=%d cost=%v (%s)", s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.BestCostUSD, s.CostSource)
	}
}
