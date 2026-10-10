package conversation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// A fake token, assembled at runtime so secret scanners don't flag it.
var ghToken = strings.Join([]string{"gh", "p_", strings.Repeat("A1b2C3d4E5", 4)}, "")

func write(t *testing.T, path string, lines ...any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

type m = map[string]any

func claudeFixture(t *testing.T, home string) string {
	main := filepath.Join(home, ".claude", "projects", "-home-dev-shop", "s1.jsonl")
	write(t, main,
		m{"type": "user", "sessionId": "s1", "timestamp": "2026-10-10T10:00:00Z", "message": m{"content": "Fix the login redirect, token " + ghToken}},
		m{"type": "user", "isMeta": true, "sessionId": "s1", "timestamp": "2026-10-10T10:00:01Z", "message": m{"content": "meta text"}},
		m{"type": "assistant", "sessionId": "s1", "timestamp": "2026-10-10T10:00:02Z", "message": m{"id": "r1", "content": []m{{"type": "text", "text": "Looking at **auth.ts**."}}}},
		m{"type": "assistant", "sessionId": "s1", "timestamp": "2026-10-10T10:00:03Z", "message": m{"id": "r1", "content": []m{{"type": "tool_use", "id": "t1", "name": "Bash", "input": m{"command": "npm test"}}}}},
		m{"type": "user", "sessionId": "s1", "timestamp": "2026-10-10T10:00:04Z", "message": m{"content": []m{{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}}}},
		m{"type": "assistant", "sessionId": "s1", "timestamp": "2026-10-10T10:00:05Z", "message": m{"id": "r2", "content": []m{{"type": "tool_use", "id": "t2", "name": "TodoWrite", "input": m{"todos": []m{{"content": "Fix redirect", "status": "completed"}, {"content": "Add a test", "status": "pending"}}}}}}},
		m{"type": "user", "isCompactSummary": true, "sessionId": "s1", "timestamp": "2026-10-10T10:00:06Z", "message": m{"content": "summary of earlier work"}},
		m{"type": "assistant", "sessionId": "s1", "timestamp": "2026-10-10T10:00:09Z", "message": m{"id": "r3", "content": []m{{"type": "text", "text": "Done: the redirect works."}}}},
	)
	write(t, filepath.Join(home, ".claude", "projects", "-home-dev-shop", "s1", "subagents", "agent-a1.jsonl"),
		m{"type": "assistant", "sessionId": "s1", "agentId": "a1", "timestamp": "2026-10-10T10:00:07Z", "message": m{"id": "r9", "content": []m{{"type": "text", "text": "Subagent report."}}}},
	)
	return main
}

func reader(home string, level redact.Level, evs []model.Event) Reader {
	return Reader{UserHome: home, Level: level, Redactor: redact.Default, Events: func(string) ([]model.Event, error) { return evs, nil }}
}

var storedEvents = []model.Event{
	{Kind: model.KindTurnStart, ActorID: "claude-code:s1", Data: m{"prompt": "Fix the login redirect (stored)"}},
	{Kind: model.KindToolStart, ActorID: "claude-code:s1/sub:a1", Data: m{"tool": "shell", "tool_raw": "Bash", "input_summary": "npm test"}},
	{Kind: model.KindTurnEnd, ActorID: "claude-code:s1", Data: m{"assistant_summary": "Done (stored)."}},
}

func TestReadClaudeTranscript(t *testing.T) {
	home := t.TempDir()
	main := claudeFixture(t, home)
	res, err := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "claude-code:s1", Agent: "claude-code", TranscriptPath: main})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceTranscript || res.Reason != "" {
		t.Fatalf("source %q reason %q", res.Source, res.Reason)
	}
	var got []string
	for _, x := range res.Messages {
		got = append(got, x.Role+":"+x.Tool+":"+x.Subagent+":"+x.Text)
	}
	want := []string{
		"user:::Fix the login redirect, token «redacted:github_token»",
		"assistant:::Looking at **auth.ts**.",
		"tool:Bash::npm test",
		"tool:TodoWrite::TodoWrite",
		"assistant::a1:Subagent report.",
		"assistant:::Done: the redirect works.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if todos := res.Messages[3].Todos; len(todos) != 2 || todos[1].Text != "Add a test" || todos[1].Status != "pending" {
		t.Fatalf("todos: %+v", todos)
	}
}

func TestReadFindsTranscriptFromEvents(t *testing.T) {
	home := t.TempDir()
	main := claudeFixture(t, home)
	evs := []model.Event{{Kind: model.KindUsage, Raw: &model.RawRef{Ref: "transcript:" + main + "#120"}}}
	res, err := reader(home, redact.Standard, evs).Read(&engine.Session{ID: "claude-code:s1", Agent: "claude-code"})
	if err != nil || res.Source != SourceTranscript || len(res.Messages) != 6 {
		t.Fatalf("%v %q %d", err, res.Source, len(res.Messages))
	}
}

func TestReadFallbacks(t *testing.T) {
	home := t.TempDir()
	main := claudeFixture(t, home)
	outside := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	write(t, outside, m{"type": "user", "sessionId": "s1", "message": m{"content": "not to be read"}})
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		name, agent, path, reason string
	}{
		{"no path", "claude-code", "", ReasonNoPath},
		{"outside roots", "claude-code", outside, ReasonOutside},
		{"traversal", "claude-code", filepath.Join(home, ".claude", "projects", "x") + sep + ".." + sep + ".." + sep + ".." + sep + "etc.jsonl", ReasonOutside},
		{"relative", "claude-code", filepath.Join("..", "s1.jsonl"), ReasonOutside},
		{"not jsonl", "claude-code", strings.TrimSuffix(main, ".jsonl") + ".txt", ReasonOutside},
		{"missing", "claude-code", filepath.Join(home, ".claude", "projects", "-home-dev-shop", "gone.jsonl"), ReasonMissing},
		{"another agent's folder", "codex", main, ReasonMissing}, // no ~/.codex/sessions at all
		{"unsupported agent", "windsurf", main, ReasonUnsupported},
	} {
		res, err := reader(home, redact.Standard, storedEvents).Read(&engine.Session{ID: "claude-code:s1", Agent: tc.agent, TranscriptPath: tc.path})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Source != SourceStored || res.Reason != tc.reason || res.Note == "" {
			t.Errorf("%s: source %q reason %q, want stored %q", tc.name, res.Source, res.Reason, tc.reason)
			continue
		}
		if len(res.Messages) != 3 || res.Messages[0].Text != "Fix the login redirect (stored)" || res.Messages[1].Subagent != "a1" || res.Messages[1].Tool != "Bash" {
			t.Errorf("%s: stored messages %+v", tc.name, res.Messages)
		}
	}
}

func TestReadSymlinkOutOfRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := t.TempDir()
	claudeFixture(t, home)
	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	write(t, outside, m{"type": "user", "sessionId": "s1", "message": m{"content": "not to be read"}})
	link := filepath.Join(home, ".claude", "projects", "-home-dev-shop", "link.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	res, _ := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "claude-code:s1", Agent: "claude-code", TranscriptPath: link})
	if res.Source != SourceStored || res.Reason != ReasonOutside {
		t.Fatalf("symlink out of roots: %q %q", res.Source, res.Reason)
	}
	// A subagent file linking out of the session's folder is skipped.
	main := filepath.Join(home, ".claude", "projects", "-home-dev-shop", "s1.jsonl")
	if err := os.Symlink(outside, filepath.Join(home, ".claude", "projects", "-home-dev-shop", "s1", "subagents", "agent-x.jsonl")); err != nil {
		t.Fatal(err)
	}
	res, _ = reader(home, redact.Standard, nil).Read(&engine.Session{ID: "claude-code:s1", Agent: "claude-code", TranscriptPath: main})
	for _, x := range res.Messages {
		if strings.Contains(x.Text, "not to be read") {
			t.Fatal("read a subagent file outside the session folder")
		}
	}
}

func TestReadMinimal(t *testing.T) {
	home := t.TempDir()
	main := claudeFixture(t, home)
	res, err := reader(home, redact.Minimal, storedEvents).Read(&engine.Session{ID: "claude-code:s1", Agent: "claude-code", TranscriptPath: main})
	if err != nil || res.Source != SourceNone || res.Reason != ReasonMinimal || len(res.Messages) != 0 || !strings.Contains(res.Note, "capture_level") {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestReadCaps(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, ".claude", "projects", "p", "s2.jsonl")
	long := strings.Repeat("x", maxTextStd+50)
	huge := strings.Repeat("y", maxLineBytes+10)
	write(t, main,
		m{"type": "user", "sessionId": "s2", "timestamp": "2026-10-10T10:00:00Z", "message": m{"content": long}},
		m{"type": "user", "sessionId": "s2", "timestamp": "2026-10-10T10:00:01Z", "message": m{"content": huge}},
		m{"type": "assistant", "sessionId": "s2", "timestamp": "2026-10-10T10:00:02Z", "message": m{"content": []m{{"type": "text", "text": "after"}}}},
	)
	res, err := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "claude-code:s2", Agent: "claude-code", TranscriptPath: main})
	if err != nil || len(res.Messages) != 2 || len([]rune(res.Messages[0].Text)) != maxTextStd+1 || res.Messages[1].Text != "after" {
		t.Fatalf("%v %d", err, len(res.Messages))
	}
	res, _ = reader(home, redact.Full, nil).Read(&engine.Session{ID: "claude-code:s2", Agent: "claude-code", TranscriptPath: main})
	if res.Messages[0].Text != long {
		t.Fatal("full level cut a message under its cap")
	}
}

func TestReadCodex(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "sessions", "2026", "10", "10", "rollout-2026-10-10T10-00-00-c1.jsonl")
	ev := func(ts, typ string, p m) m { return m{"timestamp": ts, "type": typ, "payload": p} }
	write(t, path,
		ev("2026-10-10T10:00:00Z", "session_meta", m{"id": "c1", "cwd": "/home/dev/shop"}),
		ev("2026-10-10T10:00:01Z", "response_item", m{"type": "message", "role": "user", "content": []m{{"type": "input_text", "text": "<environment_context>injected</environment_context>"}}}),
		ev("2026-10-10T10:00:01Z", "event_msg", m{"type": "item_completed", "item": m{"type": "UserMessage", "id": "u1", "content": []m{{"type": "text", "text": "Add a health check"}}}}),
		ev("2026-10-10T10:00:02Z", "event_msg", m{"type": "item_completed", "item": m{"type": "CommandExecution", "id": "x1", "command": []string{"bash", "-lc", "go test ./..."}, "exit_code": 1}}),
		ev("2026-10-10T10:00:03Z", "event_msg", m{"type": "item_completed", "item": m{"type": "FileChange", "id": "x2", "changes": m{"main.go": m{"type": "update"}}}}),
		ev("2026-10-10T10:00:04Z", "response_item", m{"type": "function_call", "name": "update_plan", "arguments": `{"plan":[{"step":"Write handler","status":"completed"},{"step":"Add test","status":"in_progress"}]}`}),
		ev("2026-10-10T10:00:05Z", "response_item", m{"type": "message", "role": "assistant", "content": []m{{"type": "output_text", "text": "Added /healthz."}}}),
		ev("2026-10-10T10:00:05Z", "event_msg", m{"type": "item_completed", "item": m{"type": "AgentMessage", "id": "a1", "content": []m{{"type": "text", "text": "Added /healthz."}}}}),
	)
	res, err := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "codex:c1", Agent: "codex", TranscriptPath: path})
	if err != nil || res.Source != SourceTranscript {
		t.Fatalf("%v %+v", err, res)
	}
	var got []string
	for _, x := range res.Messages {
		got = append(got, x.Role+":"+x.Tool+":"+x.Text)
	}
	want := "user::Add a health check\ntool:exec:go test ./...\ntool:apply_patch:main.go\ntool:update_plan:update_plan\nassistant::Added /healthz."
	if strings.Join(got, "\n") != want {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
	if todos := res.Messages[3].Todos; len(todos) != 2 || todos[1].Status != "in_progress" {
		t.Fatalf("plan: %+v", todos)
	}
}

func TestReadGeminiRewrites(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "tmp", "p1", "chats", "session-2026-10-10T10-00-g1.jsonl")
	write(t, path,
		m{"sessionId": "g1", "projectHash": "h", "startTime": "2026-10-10T10:00:00Z", "kind": "main"},
		m{"id": "m1", "timestamp": "2026-10-10T10:00:01Z", "type": "user", "content": []m{{"text": "Rename the flag"}}},
		m{"id": "m2", "timestamp": "2026-10-10T10:00:02Z", "type": "gemini", "content": "Working", "toolCalls": []m{{"id": "t1", "name": "run_shell_command", "args": m{"command": "make"}, "status": "executing"}}},
		m{"id": "m2", "timestamp": "2026-10-10T10:00:02Z", "type": "gemini", "content": "Working on it.", "toolCalls": []m{{"id": "t1", "name": "run_shell_command", "args": m{"command": "make"}, "status": "success"}, {"id": "t2", "name": "write_todos", "args": m{"todos": []m{{"description": "Update docs", "status": "pending"}}}, "status": "success"}}},
		m{"id": "m3", "timestamp": "2026-10-10T10:00:03Z", "type": "info", "content": "CLI notice"},
	)
	write(t, filepath.Join(home, ".gemini", "tmp", "p1", "chats", "g1", "sub1.jsonl"),
		m{"sessionId": "sub1", "kind": "subagent"},
		m{"id": "s1", "timestamp": "2026-10-10T10:00:02.500Z", "type": "gemini", "content": "Sub result"},
	)
	res, err := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "gemini-cli:g1", Agent: "gemini-cli", TranscriptPath: path})
	if err != nil || res.Source != SourceTranscript {
		t.Fatalf("%v %+v", err, res)
	}
	var got []string
	for _, x := range res.Messages {
		got = append(got, x.Role+":"+x.Subagent+":"+x.Text)
	}
	want := "user::Rename the flag\nassistant::Working on it.\ntool::make\ntool::write_todos\nassistant:sub1:Sub result"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestReadCursorTodosMerge(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".cursor", "projects", "home-dev-shop", "agent-transcripts", "cv1", "cv1.jsonl")
	write(t, path,
		m{"role": "user", "message": m{"content": []m{{"type": "text", "text": "<timestamp>Saturday, Oct 10, 2026, 10:00 AM</timestamp>\n<user_query>\nTidy the CSS\n</user_query>"}}}},
		m{"role": "assistant", "message": m{"content": []m{{"type": "text", "text": "Sure."}, {"type": "tool_use", "name": "TodoWrite", "input": m{"merge": false, "todos": []m{{"id": "1", "content": "Find unused rules", "status": "in_progress"}, {"id": "2", "content": "Remove them", "status": "pending"}}}}}}},
		m{"role": "assistant", "message": m{"content": []m{{"type": "tool_use", "name": "TodoWrite", "input": m{"merge": true, "todos": []m{{"id": "1", "status": "completed"}}}}}}},
	)
	res, err := reader(home, redact.Standard, nil).Read(&engine.Session{ID: "cursor:cv1", Agent: "cursor", TranscriptPath: path})
	if err != nil || res.Source != SourceTranscript || len(res.Messages) != 4 || res.Messages[0].Text != "Tidy the CSS" {
		t.Fatalf("%v %+v", err, res)
	}
	todos := res.Messages[3].Todos
	if len(todos) != 2 || todos[0].Status != "completed" || todos[0].Text != "Find unused rules" || todos[1].Status != "pending" {
		t.Fatalf("merged todos: %+v", todos)
	}
}

func TestMarkdownAndHandoff(t *testing.T) {
	home := t.TempDir()
	main := claudeFixture(t, home)
	sess := &engine.Session{ID: "claude-code:s1", Agent: "claude-code", Title: "Fix login", ProjectID: "shop", Branch: "fix/login",
		CWD: "/home/dev/shop", Status: engine.StatusReview, LinesAdded: 12, LinesRemoved: 3, TranscriptPath: main,
		StartedAt: time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)}
	res, _ := reader(home, redact.Standard, nil).Read(sess)

	md := Markdown(sess, res)
	for _, want := range []string{"# Fix login", "- Project: shop · fix/login", "### User · 2026-10-10 10:00:00Z", "«redacted:github_token»",
		"**Tool** `Bash` · 2026-10-10 10:00:03Z: `npm test`", "### Assistant (subagent a1)", "Done: the redirect works."} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, ghToken) {
		t.Fatal("markdown has the secret")
	}

	evs := []model.Event{
		{Kind: model.KindShellExec, Data: m{"command": "npm test", "exit_code": float64(1)}},
		{Kind: model.KindShellExec, Data: m{"command": "npm run lint", "exit_code": float64(0)}},
		{Kind: model.KindShellExec, Data: m{"command": "npm test", "exit_code": float64(1)}},
	}
	text := Handoff(sess, []HandoffFile{{Path: "/home/dev/shop/src/auth.ts", Added: 12, Removed: 3}}, evs, res)
	for _, want := range []string{"Task: Fix login", "> Fix the login redirect, token «redacted:github_token»", "project shop, folder /home/dev/shop, branch fix/login",
		"Status: review", "- src/auth.ts +12 −3", "- [ ] Add a test", "`npm test` (exit 1)", "> Done: the redirect works."} {
		if !strings.Contains(text, want) {
			t.Errorf("handoff lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Fix redirect") || strings.Contains(text, "npm run lint") || strings.Count(text, "npm test") != 1 {
		t.Errorf("handoff lists done todos or passing/duplicate commands:\n%s", text)
	}
}

func TestCodeSpan(t *testing.T) {
	for in, want := range map[string]string{"a": "`a`", "a`b": "``a`b``", "`x`": "`` `x` ``"} {
		if got := codeSpan(in); got != want {
			t.Errorf("codeSpan(%q) = %q, want %q", in, got, want)
		}
	}
}
