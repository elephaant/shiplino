package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/model"
)

func TestForSyncSendsMetadataOnly(t *testing.T) {
	r := Default
	cases := []struct {
		kind model.Kind
		data map[string]any
		want map[string]any
	}{
		{model.KindTurnStart,
			map[string]any{"prompt": "secret plan", "prompt_chars": 11, "custom_note": "from an SDK"},
			map[string]any{"prompt_chars": 11}},
		{model.KindTurnEnd,
			map[string]any{"assistant_summary": "Done: changed auth", "last_assistant_message": "x", "duration_ms": 900},
			map[string]any{"duration_ms": 900}},
		{model.KindShellExec,
			map[string]any{"command": "psql -c 'select * from users'", "exit_code": 1, "output": "rows", "tool": "shell"},
			map[string]any{"exit_code": 1, "program": "psql", "tool": "shell"}},
		{model.KindShellExec, // only a checked program token, never the command or a forged program
			map[string]any{"command": "cd /home/dev/api && GITHUB_TOKEN=x npm publish --otp 123456", "exit_code": 1},
			map[string]any{"exit_code": 1, "program": "npm"}},
		{model.KindShellExec,
			map[string]any{"program": "curl https://example.com/?key=abc", "exit_code": 6},
			map[string]any{"exit_code": 6}},
		{model.KindToolEnd,
			map[string]any{"tool": "shell", "ok": false, "denied": true, "error": "blocked by policy: rm -rf /"},
			map[string]any{"denied": true, "ok": false, "tool": "shell"}},
		{model.KindWaitingEnd,
			map[string]any{"resolution": "reject", "denied": true},
			map[string]any{"denied": true}},
		{model.KindToolStart,
			map[string]any{"tool": "search", "input_summary": "password reset", "tool_input": map[string]any{"q": "x"}},
			map[string]any{"tool": "search"}},
		{model.KindToolStart,
			map[string]any{"tool": "edit", "input_summary": "src/a.go"},
			map[string]any{"tool": "edit", "input_summary": "src/a.go"}},
		{model.KindToolEnd,
			map[string]any{"tool": "edit", "ok": false, "error": "permission denied: /etc/x", "tool_response": "…"},
			map[string]any{"tool": "edit", "ok": false}},
		{model.KindFileEdit,
			map[string]any{"path": "src/a.go", "lines_added": 3, "patch": "+secret", "old_string": "a", "new_string": "b"},
			map[string]any{"path": "src/a.go", "lines_added": 3}},
		{model.KindGitCommit,
			map[string]any{"sha": "abc", "message": "fix: customer X", "author": "Dev Name", "files_changed": 2},
			map[string]any{"sha": "abc", "files_changed": 2}},
		{model.KindToolStart,
			map[string]any{"tool": "web", "url": "https://example.com/reset?token=abc"},
			map[string]any{"tool": "web"}},
		{model.KindGitPR,
			map[string]any{"url": "https://github.com/acme/api/pull/3", "number": 3, "title": "PR title"},
			map[string]any{"number": 3, "url": "https://github.com/acme/api/pull/3"}},
		{model.KindWaitingStart,
			map[string]any{"reason": "permission", "message": "Claude wants to run rm -rf build"},
			map[string]any{"reason": "permission", "message": waitingText("permission")}},
		{model.KindSessionStart,
			map[string]any{"title": "Fix the login bug", "title_source": "agent", "permission_mode": "default"},
			map[string]any{"permission_mode": "default"}},
		{model.KindUsage,
			map[string]any{"model": "m", "input_tokens": 10, "cost_usd": 0.5, "model_usage": map[string]any{"m": map[string]any{"inputTokens": 10.0, "note": "x"}}},
			map[string]any{"model": "m", "input_tokens": 10, "cost_usd": 0.5, "model_usage": map[string]any{"m": map[string]any{"inputTokens": 10.0}}}},
	}
	for _, c := range cases {
		e := model.Event{Kind: c.kind, User: "dev", Raw: &model.RawRef{Ref: "x"}, Data: c.data}
		r.ForSync(&e, false)
		got, _ := json.Marshal(e.Data)
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%s: got %s, want %s", c.kind, got, want)
		}
		if e.User != "" || e.Raw != nil {
			t.Errorf("%s: user or raw sent", c.kind)
		}
	}
}

func TestForSyncTitles(t *testing.T) {
	r := Default
	e := model.Event{Kind: model.KindSessionUpdate, Data: map[string]any{
		"title": "Rotate ghp_0123456789abcdefghijklmnopqrstuvwxyzAB " + strings.Repeat("x", 300), "title_source": "agent"}}
	r.ForSync(&e, true)
	title, _ := e.Data["title"].(string)
	if strings.Contains(title, "ghp_") || len([]rune(title)) > maxSyncTitle+1 || e.Data["title_source"] != "agent" {
		t.Fatalf("title: %q %v", title, e.Data)
	}
	// Titles of other kinds never pass.
	e = model.Event{Kind: model.KindGitPR, Data: map[string]any{"title": "PR title", "number": 3}}
	r.ForSync(&e, true)
	if e.Data["title"] != nil || e.Data["number"] != 3 {
		t.Fatalf("pr: %v", e.Data)
	}
}

func TestForSyncCapsStrings(t *testing.T) {
	e := model.Event{Kind: model.KindFileEdit, Data: map[string]any{"path": strings.Repeat("é", 2000), "files": []any{strings.Repeat("a", 900)}}}
	Default.ForSync(&e, false)
	if len([]rune(e.Data["path"].(string))) != maxSyncString || len(e.Data["files"].([]any)[0].(string)) != maxSyncString {
		t.Fatalf("not capped: %d", len([]rune(e.Data["path"].(string))))
	}
}

func TestRelPathWindows(t *testing.T) {
	if got := relPath(`C:\Users\dev\api`, `D:\other\file.go`); got != "…/file.go" {
		t.Errorf("got %q", got)
	}
}

func TestForSyncPathsAndEnvelope(t *testing.T) {
	e := model.Event{Kind: model.KindToolStart, SessionID: "sdk:s1", TurnID: "please deploy with ghp_0123456789abcdefghijklmnopqrstuvwxyzAB",
		Agent:    model.Agent{Name: "sdk", Version: "fix the login page now"},
		DedupKey: "claude-code:s1:title:ab12cd34",
		Project:  &model.Project{ID: "github.com/acme/api", CWD: "/home/alice/api", RepoRoot: "/home/alice/api", Remote: "https://alice:ghp_x@github.com/acme/api.git", Branch: "main"},
		Data: map[string]any{"tool": "edit", "input_summary": "/home/alice/api/a.go, /home/alice/api/b.go",
			"paths": []any{"/home/alice/secret/plan.md"}, "file_paths": []any{"/home/alice/api/c.go"},
			"status": "the user asked to delete prod", "reason": "permission"}}
	Default.ForSync(&e, false)
	out, _ := json.Marshal(e)
	for _, leak := range []string{"/home/alice", "ghp_", "alice:", "fix the login", "delete prod", "title:"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("%q sent: %s", leak, out)
		}
	}
	if e.Data["input_summary"] != "a.go, b.go" || e.Data["reason"] != "permission" || e.Project.Remote != "github.com/acme/api" || e.Project.Branch != "main" {
		t.Errorf("metadata changed: %s", out)
	}
	if !strings.HasPrefix(e.DedupKey, "h:") {
		t.Errorf("dedup key %q", e.DedupKey)
	}
}
