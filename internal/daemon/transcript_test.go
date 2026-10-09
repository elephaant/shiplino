// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const transcriptFixture = "../../pkg/adapters/claudecode/testdata/2.1/transcript.jsonl"

// withHome points the user's home directory at a temp dir, so transcript
// paths under it pass the "inside home" check.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeTranscripts splits the fixture: subagent lines go to the session's
// subagents/ folder, everything else to the main transcript.
func writeTranscripts(t *testing.T, home string) (main string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-home-dev-demo")
	main = filepath.Join(dir, "sess-0001.jsonl")
	sub := filepath.Join(dir, "sess-0001", "subagents", "agent-ag-7.jsonl")
	if err := os.MkdirAll(filepath.Dir(sub), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(transcriptFixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var mainLines, subLines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"agentId"`) {
			subLines = append(subLines, sc.Text())
		} else {
			mainLines = append(mainLines, sc.Text())
		}
	}
	os.WriteFile(main, []byte(strings.Join(mainLines, "\n")+"\n"), 0o600)
	os.WriteFile(sub, []byte(strings.Join(subLines, "\n")+"\n"), 0o600)
	return main
}

func startHook(path string) string {
	b, _ := json.Marshal(map[string]string{
		"session_id": "sess-0001", "hook_event_name": "SessionStart", "source": "startup", "transcript_path": path,
	})
	return string(b)
}

func TestTranscriptTokensAndCost(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	main := writeTranscripts(t, home)
	e.hook(startHook(main))
	e.poll()

	s := e.session(sid)
	// msg_A appears on two lines but must count once; msg_X is unpriced.
	if s.InputTokens != 2+5+10+1000 || s.OutputTokens != 191+420+10+100 || s.CacheReadTokens != 23147+41579 || s.CacheWriteTokens != 18432+3000 {
		t.Fatalf("tokens: in=%d out=%d read=%d write=%d", s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens)
	}
	if math.Abs(s.CostUSD-(0.1006174+0.0377358+0.0432)) > 1e-9 {
		t.Fatalf("cost = %.9f", s.CostUSD)
	}
	// Claude Code reported $0.25 for the process (it includes a background
	// call the transcript never shows): that is the figure to display.
	if s.ReportedCostUSD != 0.25 || s.BestCostUSD != 0.25 || s.CostSource != "reported" {
		t.Fatalf("reported=%v best=%v source=%q tree=%v", s.ReportedCostUSD, s.BestCostUSD, s.CostSource, s.TreeCostUSD)
	}
	if s.AgentVersion != "2.1.290" || s.Model != "claude-opus-5-5" {
		t.Fatalf("version/model: %q %q", s.AgentVersion, s.Model)
	}
	child := e.session(sid + "/sub:ag-7")
	if child.InputTokens != 1200 || child.OutputTokens != 80 || child.Model != "claude-haiku-5-5" {
		t.Fatalf("subagent usage: %+v", child)
	}

	// Appended lines are picked up; a restart doesn't double count.
	f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"assistant","sessionId":"sess-0001","message":{"id":"msg_C","model":"claude-opus-5-5","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n")
	f.Close()
	e.poll()
	e.restart()
	e.poll()
	if s := e.session(sid); s.InputTokens != 1117 || s.OutputTokens != 771 || s.ReportedCostUSD != 0.25 {
		t.Fatalf("after append+restart: in=%d out=%d", s.InputTokens, s.OutputTokens)
	}
}

func TestTranscriptOutsideHomeIsIgnored(t *testing.T) {
	withHome(t)
	e := newEnv(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl") // not under $HOME
	os.WriteFile(outside, []byte(`{"type":"assistant","sessionId":"sess-0001","message":{"id":"m","model":"claude-opus-5-5","usage":{"input_tokens":999,"output_tokens":1}}}`+"\n"), 0o600)
	e.hook(startHook(outside))
	e.hook(startHook("relative/path.jsonl"))
	e.poll()
	if s := e.session(sid); s.InputTokens != 0 {
		t.Fatalf("read a transcript outside home: %+v", s)
	}
	if len(e.d.transcripts) != 0 {
		t.Fatalf("registered: %v", e.d.transcripts)
	}
}

func TestOversizedLineIsSkipped(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	old := maxRead
	maxRead = 1024
	t.Cleanup(func() { maxRead = old })

	path := filepath.Join(home, ".claude", "projects", "p", "big.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	huge := `{"type":"user","pad":"` + strings.Repeat("x", 5000) + `"}`
	ok := `{"type":"assistant","sessionId":"sess-0001","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":7,"output_tokens":3}}}`
	// The giant line isn't finished yet: nothing to do, no error.
	os.WriteFile(path, []byte(huge), 0o600)
	e.hook(startHook(path))
	e.poll()
	// Once it ends, it is skipped and the next line is read.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("\n" + ok + "\n")
	f.Close()
	e.poll()
	if s := e.session(sid); s.InputTokens != 7 {
		t.Fatalf("line after the giant one not read: in=%d", s.InputTokens)
	}
	if e.d.Stats().Bad != 1 {
		t.Fatalf("stats = %+v", e.d.Stats())
	}
}
