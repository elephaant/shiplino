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
			map[string]any{"exit_code": 1, "tool": "shell"}},
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
