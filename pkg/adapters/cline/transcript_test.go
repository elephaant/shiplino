package cline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var docs = filepath.Join("testdata", fixtures, "data")

// parseDoc parses one fixture document as if it was last written at mod.
func parseDoc(t *testing.T, rel string, mod time.Time) ([]model.Event, bool) {
	t.Helper()
	path := filepath.Join(docs, rel)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	evs, recheck, err := Adapter{}.ParseTranscriptDocument(b, adapters.TranscriptMeta{ReceivedAt: t0, Path: path, ModTime: mod, State: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	return evs, recheck
}

var docFiles = []string{
	filepath.Join("tasks", "1791640000000", "ui_messages.json"),
	filepath.Join("sessions", "sess-a1", "sess-a1.messages.json"),
	filepath.Join("sessions", "sess-a1", "explorer.messages.json"),
	filepath.Join("sessions", "sess-b2", "sess-b2.messages.json"),
}

func TestUsageGolden(t *testing.T) {
	var all []model.Event
	for _, f := range docFiles {
		evs, _ := parseDoc(t, f, t0.Add(-time.Hour)) // quiet: every call is final
		all = append(all, evs...)
	}
	for i := range all {
		all[i].ID, all[i].ReceivedAt, all[i].Raw = "", time.Time{}, nil
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	path := filepath.Join("testdata", fixtures, "usage.golden.json")
	if *update {
		os.WriteFile(path, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run with -update and review the diff", path)
	}
}

func TestClassicUsage(t *testing.T) {
	// Written just now: the last call may still be streaming.
	evs, recheck := parseDoc(t, docFiles[0], t0.Add(-10*time.Second))
	if !recheck || len(evs) != 2 {
		t.Fatalf("busy file: %d events, recheck=%v; want the 2 finished calls and a recheck", len(evs), recheck)
	}
	first, cancelled := evs[0], evs[1]
	if first.SessionID != "cline:1791640000000" || first.DedupKey != "cline:usage:1791640000000:1791640001000" ||
		first.Data["cost_source"] != "reported" || first.Data["cost_usd"] != 0.0361 || first.Data["model"] != "claude-sonnet-4-6" {
		t.Fatalf("first call: %+v", first)
	}
	if first.Data["input_tokens"] != int64(12) || first.Data["cache_write_tokens"] != int64(8200) || first.Data["output_tokens"] != int64(340) {
		t.Fatalf("first call tokens: %v", first.Data)
	}
	// Cline's cost of the cancelled call is 0: priced from the table.
	if cancelled.Data["cost_source"] != "computed" || cancelled.Data["cost_usd"].(float64) <= 0 {
		t.Fatalf("cancelled call: %v", cancelled.Data)
	}
	// Once quiet, the last call is final; keys don't change between reads.
	again, recheck := parseDoc(t, docFiles[0], t0.Add(-settleAfter))
	if recheck || len(again) != 3 || again[0].DedupKey != first.DedupKey || again[2].Data["output_tokens"] != int64(610) {
		t.Fatalf("quiet file: %d events, recheck=%v", len(again), recheck)
	}
}

func TestSDKUsage(t *testing.T) {
	evs, recheck := parseDoc(t, docFiles[1], t0)
	if recheck || len(evs) != 2 {
		t.Fatalf("lead: %d events, recheck=%v", len(evs), recheck)
	}
	a, b := evs[0], evs[1]
	// inputTokens include the cache tokens: only the uncached part is input.
	if a.SessionID != "cline:sess-a1" || a.ActorID != a.SessionID || a.DedupKey != "cline:usage:msg_a1" ||
		a.Data["input_tokens"] != int64(200) || a.Data["cache_write_tokens"] != int64(8800) || a.Data["cost_source"] != "reported" {
		t.Fatalf("first call: %+v", a)
	}
	if b.Data["input_tokens"] != int64(100) || b.Data["cost_source"] != "computed" {
		t.Fatalf("zero-cost call: %v", b.Data)
	}

	sub, _ := parseDoc(t, docFiles[2], t0)
	if len(sub) != 1 || sub[0].ActorID != "cline:sess-a1/sub:explorer" || sub[0].ParentActor != "cline:sess-a1" || sub[0].Data["cost_source"] != "unpriced" {
		t.Fatalf("subagent: %+v", sub)
	}

	// A resumed classic task: its copied totals were counted from the old files.
	resumed, _ := parseDoc(t, docFiles[3], t0)
	if len(resumed) != 1 || resumed[0].DedupKey != "cline:usage:msg_l5" {
		t.Fatalf("resumed task: %+v", resumed)
	}
}

func TestSessionActor(t *testing.T) {
	for _, c := range []struct{ id, path, root, agent string }{
		{"s1", "", "s1", ""},
		{"s1__worker", "", "s1", "worker"},
		{"s1__teamtask__reviewer__x7Yz09", "", "s1", "reviewer"},
		{"", filepath.Join("sessions", "s2", "s2.messages.json"), "s2", ""},
		{"", filepath.Join("sessions", "s2", "helper.messages.json"), "s2", "helper"},
		{"", filepath.Join("sessions", "s2", "helper__t1.messages.json"), "s2", "helper"},
	} {
		if root, agent := sessionActor(c.id, c.path); root != c.root || agent != c.agent {
			t.Errorf("sessionActor(%q, %q) = %q, %q", c.id, c.path, root, agent)
		}
	}
}

func TestUsageHasNoContent(t *testing.T) {
	for _, f := range docFiles {
		evs, _ := parseDoc(t, f, t0.Add(-time.Hour))
		for _, e := range evs {
			b, _ := json.Marshal(e.Data)
			for _, s := range []string{"price formatter", "config loader", "Renaming", "Old answer", "read_file", "callers"} {
				if bytes.Contains(b, []byte(s)) {
					t.Fatalf("%s: usage event carries content %q: %s", f, s, b)
				}
			}
		}
	}
}

func TestBadDocuments(t *testing.T) {
	for _, name := range []string{"ui_messages.json", "s.messages.json"} {
		_, _, err := Adapter{}.ParseTranscriptDocument([]byte("{not json"), adapters.TranscriptMeta{Path: filepath.Join("x", name)})
		if err == nil {
			t.Errorf("%s: no error for bad JSON", name)
		}
	}
	evs, recheck, err := Adapter{}.ParseTranscriptDocument([]byte("{}"), adapters.TranscriptMeta{Path: "task_metadata.json"})
	if err != nil || recheck || evs != nil {
		t.Errorf("other file: %v %v %v", evs, recheck, err)
	}
}

func TestTranscriptRoots(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"CLINE_DATA_DIR", "CLINE_DIR", "CLINE_SESSION_DATA_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	roots := Adapter{}.TranscriptRoots(home, t0, t0)
	for _, want := range []string{
		filepath.Join(home, ".cline", "data", "sessions", "*", "*.messages.json"),
		filepath.Join(home, ".cline", "data", "tasks", "*", "ui_messages.json"),
	} {
		if !slices.Contains(roots, want) {
			t.Errorf("roots %v lack %s", roots, want)
		}
	}
	// VS Code and its forks keep tasks in their global storage.
	task := filepath.Join(editorConfigDirs(home)[0], "Cursor", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks", "17", "ui_messages.json")
	os.MkdirAll(filepath.Dir(task), 0o700)
	os.WriteFile(task, []byte("[]"), 0o600)
	var found bool
	for _, g := range roots {
		m, _ := filepath.Glob(g)
		found = found || slices.Contains(m, task)
	}
	if !found {
		t.Errorf("roots %v don't find %s", roots, task)
	}
	t.Setenv("CLINE_DIR", filepath.Join(home, "elsewhere"))
	if r := (Adapter{}).TranscriptRoots(home, t0, t0); r[0] != filepath.Join(home, "elsewhere", "data", "sessions", "*", "*.messages.json") {
		t.Errorf("CLINE_DIR: %v", r)
	}
}
