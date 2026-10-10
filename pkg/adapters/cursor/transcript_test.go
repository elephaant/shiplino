package cursor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var (
	convDir    = filepath.Join("testdata", "2026.09", "projects", "home-dev-shop", "agent-transcripts", "cv-7")
	mainFile   = filepath.Join(convDir, "cv-7.jsonl")
	subFile    = filepath.Join(convDir, "subagents", "sa-3.jsonl")
	fixtureMod = time.Date(2026, 8, 6, 6, 0, 0, 0, time.UTC)
)

// parseTranscript parses a transcript file the way the daemon does; lines
// before `from` are replayed as warmup into the same state.
func parseTranscript(t *testing.T, path string, from int) []model.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := map[string]string{}
	var all []model.Event
	var off int64
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		meta := adapters.TranscriptMeta{ReceivedAt: fixtureMod, Ref: "transcript:" + path + "#" + strconv.FormatInt(off, 10),
			Path: path, ModTime: fixtureMod, State: st, Warmup: i < from}
		off += int64(len(sc.Bytes())) + 1
		evs, err := Adapter{}.ParseTranscriptLine(sc.Bytes(), meta)
		if err != nil {
			t.Fatalf("%s line %d: %v", path, i+1, err)
		}
		if i < from && len(evs) > 0 {
			t.Fatalf("warmup line %d produced events", i+1)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatalf("line %d: %v", i+1, err)
			}
		}
		all = append(all, evs...)
	}
	return all
}

func TestTranscriptGolden(t *testing.T) {
	all := append(parseTranscript(t, mainFile, 0), parseTranscript(t, subFile, 0)...)
	for i := range all {
		all[i].ID, all[i].Raw = "", nil // the ref holds the OS-specific path
		if p, ok := all[i].Data["transcript_path"].(string); ok {
			all[i].Data["transcript_path"] = filepath.ToSlash(p)
		}
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	path := filepath.Join("testdata", "2026.09", "transcript.golden.json")
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

func TestTranscriptMapping(t *testing.T) {
	evs := parseTranscript(t, mainFile, 0)
	by := map[model.Kind][]model.Event{}
	for _, e := range evs {
		by[e.Kind] = append(by[e.Kind], e)
		if e.SessionID != "cursor:cv-7" || e.ActorID != "cursor:cv-7" || e.Collector != model.CollectorTranscript {
			t.Fatalf("ids: %+v", e)
		}
		if e.Project == nil || e.Project.CWD != "/home/dev/shop" {
			t.Fatalf("workspace from .workspace-trusted: %+v", e.Project)
		}
	}
	// 10:41 at UTC+5:30 is 05:11 UTC; each line adds a millisecond.
	at := time.Date(2026, 8, 6, 5, 11, 0, 0, time.UTC)
	ts := by[model.KindTurnStart]
	if len(ts) != 3 || ts[0].Data["prompt"] != "fix the cart total rounding" || !ts[0].TS.Equal(at.Add(time.Millisecond)) {
		t.Fatalf("turn starts: %+v", ts)
	}
	// The second prompt's timestamp is the same minute: the clock keeps going.
	if !ts[1].TS.After(ts[0].TS) || ts[1].Data["prompt"] != "run the linter" {
		t.Errorf("second turn went back in time: %v", ts[1].TS)
	}
	if want := time.Date(2026, 8, 6, 5, 35, 0, 2*1e6, time.UTC); !ts[2].TS.Equal(want) {
		t.Errorf("third turn at %v, want %v (timestamp-only line, then the prompt)", ts[2].TS, want)
	}
	var statuses []any
	for _, e := range by[model.KindTurnEnd] {
		statuses = append(statuses, e.Data["status"])
	}
	if !reflect.DeepEqual(statuses, []any{"ok", "interrupted", "error"}) || by[model.KindTurnEnd][2].Data["error"] != "Connection lost" {
		t.Errorf("turn ends: %v", by[model.KindTurnEnd])
	}
	starts, ends := by[model.KindToolStart], by[model.KindToolEnd]
	if len(starts) != 10 || len(ends) != 10 {
		t.Fatalf("tools: %d starts, %d ends", len(starts), len(ends))
	}
	for i := range starts {
		if starts[i].Data["tool_call_id"] != ends[i].Data["tool_call_id"] || ends[i].Data["ok"] != true {
			t.Errorf("tool %d: %v / %v", i, starts[i].Data, ends[i].Data)
		}
	}
	sh := by[model.KindShellExec]
	if len(sh) != 2 || sh[0].Data["command"] != "npm test" || sh[0].Data["cwd"] != "/home/dev/shop/web" || sh[1].Data["cwd"] != "/home/dev/shop" {
		t.Errorf("shell: %+v", sh)
	}
	for _, e := range sh {
		if _, ok := e.Data["exit_code"]; ok {
			t.Errorf("exit code invented: %v", e.Data)
		}
	}
	type edit struct {
		path, op string
		a, r     any
	}
	var edits []edit
	for _, e := range by[model.KindFileEdit] {
		edits = append(edits, edit{e.Data["path"].(string), e.Data["op"].(string), e.Data["lines_added"], e.Data["lines_removed"]})
		if _, counted := e.Data["lines_added"]; counted && e.Data["lines_source"] != "computed" {
			t.Errorf("lines_source: %v", e.Data)
		}
	}
	want := []edit{
		{"/home/dev/shop/src/cart.ts", "modify", 2, 1},
		{"/home/dev/shop/src/round.ts", "create", 2, 0},
		{"/home/dev/shop/src/util.ts", "modify", 2, 1},
		{"/home/dev/shop/src/money.ts", "create", 1, 0},
		{"/home/dev/shop/src/legacy.ts", "delete", nil, nil},
		{"/home/dev/shop/src/old-cart.ts", "delete", nil, nil},
	}
	if !reflect.DeepEqual(edits, want) {
		t.Errorf("file edits:\n got %v\nwant %v", edits, want)
	}
	m := by[model.KindMCPCall]
	if len(m) != 2 || m[0].Data["server"] != "linear" || m[0].Data["tool"] != "get_issue" || m[1].Data["server"] != "github" {
		t.Errorf("mcp: %+v", m)
	}
	keys := map[string]bool{}
	for _, e := range evs {
		if keys[e.DedupKey] {
			t.Errorf("duplicate dedup key %s", e.DedupKey)
		}
		keys[e.DedupKey] = true
	}
}

func TestTranscriptSubagent(t *testing.T) {
	evs := parseTranscript(t, subFile, 0)
	if len(evs) != 4 {
		t.Fatalf("events: %d", len(evs))
	}
	for _, e := range evs {
		if e.SessionID != "cursor:cv-7" || e.ActorID != "cursor:cv-7/sub:sa-3" || e.ParentActor != "cursor:cv-7" || e.ActorType != "subagent" {
			t.Fatalf("subagent ids: %+v", e)
		}
	}
	if !evs[0].TS.Equal(time.Date(2026, 8, 6, 5, 12, 0, 1e6, time.UTC)) {
		t.Errorf("subagent clock: %v", evs[0].TS)
	}
}

// Replaying the first lines as warmup, then the rest, gives the same
// events as one pass: the clock survives a daemon restart.
func TestTranscriptWarmup(t *testing.T) {
	full := parseTranscript(t, mainFile, 0)
	for _, from := range []int{1, 7, 11} {
		resumed := parseTranscript(t, mainFile, from)
		tail := full[len(full)-len(resumed):]
		for i := range resumed {
			r, f := resumed[i], tail[i]
			if r.DedupKey != f.DedupKey || !r.TS.Equal(f.TS) {
				t.Fatalf("from line %d, event %d: %s %v, want %s %v", from+1, i, r.DedupKey, r.TS, f.DedupKey, f.TS)
			}
		}
	}
}

func TestTranscriptWithoutTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-transcripts", "cv-2", "cv-2.jsonl")
	mod := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	st := map[string]string{"cwd": ""} // workspace already looked up
	var got []time.Time
	for _, l := range []string{`{"type":"turn_ended","status":"success"}`, `{"type":"turn_ended","status":"success"}`} {
		evs, _ := Adapter{}.ParseTranscriptLine([]byte(l), adapters.TranscriptMeta{Path: path, ModTime: mod, State: st, ReceivedAt: time.Now()})
		got = append(got, evs[0].TS)
	}
	if !got[0].Equal(mod) || !got[1].Equal(mod.Add(time.Millisecond)) {
		t.Errorf("modification-time fallback: %v", got)
	}
}

func TestTranscriptPaths(t *testing.T) {
	cases := []struct {
		path, session, actor string
	}{
		{"/home/dev/.cursor/projects/p/agent-transcripts/c1/c1.jsonl", "cursor:c1", "cursor:c1"},
		{"/home/dev/.cursor/projects/p/agent-transcripts/c1/subagents/s9.jsonl", "cursor:c1", "cursor:c1/sub:s9"},
		{`C:\Users\dev\.cursor\projects\p\agent-transcripts\c1\subagents\s9.jsonl`, "cursor:c1", "cursor:c1/sub:s9"},
		{"/home/dev/.cursor/projects/p/agent-transcripts/c3.jsonl", "cursor:c3", "cursor:c3"},
		{"/home/dev/.cursor/projects/p/agent-transcripts/c1/other.jsonl", "", ""},
		{"/home/dev/notes.jsonl", "", ""},
	}
	for _, c := range cases {
		evs, _ := Adapter{}.ParseTranscriptLine([]byte(`{"type":"turn_ended","status":"success"}`),
			adapters.TranscriptMeta{Path: c.path, State: map[string]string{"cwd": ""}, ReceivedAt: fixtureMod})
		var session, actor string
		if len(evs) == 1 {
			session, actor = evs[0].SessionID, evs[0].ActorID
		}
		if session != c.session || actor != c.actor {
			t.Errorf("%s: %q %q, want %q %q", c.path, session, actor, c.session, c.actor)
		}
	}
	// The hook adapter derives the same ids from transcript_path.
	hook, _ := Adapter{}.ParseHook([]byte(`{"conversation_id":"s9","hook_event_name":"stop","status":"completed",
		"transcript_path":"/home/dev/.cursor/projects/p/agent-transcripts/c1/subagents/s9.jsonl"}`), adapters.HookMeta{ReceivedAt: fixtureMod})
	if hook[0].SessionID != "cursor:c1" || hook[0].ActorID != "cursor:c1/sub:s9" {
		t.Errorf("hook ids: %s %s", hook[0].SessionID, hook[0].ActorID)
	}
}

func TestParseStamp(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"Thursday, Aug 6, 2026, 10:41 AM (UTC+5:30)", time.Date(2026, 8, 6, 5, 11, 0, 0, time.UTC), true},
		{"Friday, Oct 9, 2026, 11:59 PM (UTC-7)", time.Date(2026, 10, 10, 6, 59, 0, 0, time.UTC), true},
		{"Monday, Dec 21, 2026, 12:05 AM (UTC)", time.Date(2026, 12, 21, 0, 5, 0, 0, time.UTC), true},
		{"Saturday, Sep 5, 2026, 9:00 PM (UTC+13:45)", time.Date(2026, 9, 5, 7, 15, 0, 0, time.UTC), true},
		{"Aug 6, 2026, 10:41 AM (UTC+5:30)", time.Date(2026, 8, 6, 5, 11, 0, 0, time.UTC), true},
		{"Thursday, Aug 6, 2026, 10:41 AM", time.Time{}, false},
		{"Thursday, Aug 6, 2026, 10:41 AM (PST)", time.Time{}, false},
		{"yesterday (UTC)", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := parseStamp(c.in)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("parseStamp(%q) = %v %v, want %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParsePatch(t *testing.T) {
	got := parsePatch("*** Begin Patch\n*** Update File: a.go\n*** Move to: b.go\n@@\n-x\n+y\n+z\n*** End Patch")
	if len(got) != 1 || got[0] != (patchFile{path: "b.go", op: "modify", added: 2, removed: 1}) {
		t.Errorf("parsePatch: %+v", got)
	}
}

func TestWorkspaceFromSlug(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "my.app", "web-ui")
	os.MkdirAll(want, 0o700)
	os.MkdirAll(filepath.Join(root, "my", "app-web"), 0o700) // a dead end for part of the slug
	budget := 100
	if got := walkSlug(root, []string{"my", "app", "web", "ui"}, &budget); got != want {
		t.Errorf("walkSlug = %q, want %q", got, want)
	}
	budget = 100
	if got := walkSlug(root, []string{"my", "gone"}, &budget); got != "" {
		t.Errorf("missing folder resolved to %q", got)
	}
}

func TestTranscriptRoots(t *testing.T) {
	home := t.TempDir()
	conv := filepath.Join(home, ".cursor", "projects", "home-dev-shop", "agent-transcripts", "c1")
	files := []string{filepath.Join(conv, "c1.jsonl"), filepath.Join(conv, "subagents", "s1.jsonl")}
	for _, f := range files {
		os.MkdirAll(filepath.Dir(f), 0o700)
		os.WriteFile(f, []byte("{}\n"), 0o600)
	}
	var found []string
	for _, g := range (Adapter{}).TranscriptRoots(home, time.Now(), time.Now()) {
		m, _ := filepath.Glob(g)
		found = append(found, m...)
	}
	if !reflect.DeepEqual(found, files) {
		t.Errorf("found %v, want %v", found, files)
	}
}
