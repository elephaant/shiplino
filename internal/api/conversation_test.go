package api

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

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/redact"
)

// conversationFixture adds a Claude Code transcript under a temp home and
// a session per transcript path to test.
func conversationFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := setup(t)
	home := t.TempDir()
	f.s.UserHome = home
	dir := filepath.Join(home, ".claude", "projects", "-home-dev-shop")
	os.MkdirAll(dir, 0o700)
	var lines []string
	add := func(v any) { b, _ := json.Marshal(v); lines = append(lines, string(b)) }
	add(map[string]any{"type": "user", "sessionId": "t1", "timestamp": "2026-10-10T10:00:00Z", "message": map[string]any{"content": "Add a health check"}})
	for i := range 3 {
		add(map[string]any{"type": "assistant", "sessionId": "t1", "timestamp": "2026-10-10T10:00:0" + string(rune('1'+i)) + "Z",
			"message": map[string]any{"id": "r", "content": []map[string]any{{"type": "text", "text": "Step " + string(rune('A'+i))}}}})
	}
	main := filepath.Join(dir, "t1.jsonl")
	os.WriteFile(main, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	outside := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(outside, []byte(`{"type":"user","sessionId":"t1","message":{"content":"not to be read"}}`+"\n"), 0o600)

	ctx := context.Background()
	now := time.Now().UTC()
	tx, _ := f.s.st.Begin(ctx)
	for id, path := range map[string]string{
		"claude-code:t1":        main,
		"claude-code:traversal": filepath.Join(dir, "..", "..", "..", "x.jsonl"),
		"claude-code:outside":   outside,
		"claude-code:gone":      filepath.Join(dir, "gone.jsonl"),
	} {
		tx.PutSession(ctx, &engine.Session{ID: id, RootID: id, Agent: "claude-code", Status: engine.StatusDone, StartedAt: now, LastEventAt: now, Title: "Health", TranscriptPath: path})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return f, main
}

type convResp struct {
	Source       string `json:"source"`
	Reason       string `json:"reason"`
	Note         string `json:"note"`
	CaptureLevel string `json:"capture_level"`
	Total        int    `json:"total"`
	NextOffset   *int   `json:"next_offset"`
	Messages     []struct {
		Role, Text string
	} `json:"messages"`
}

func (f *fixture) conv(t *testing.T, id, query string) (int, convResp, string) {
	t.Helper()
	resp, body := f.get(t, "/api/v1/sessions/"+url.PathEscape(id)+"/conversation"+query, bearer)
	var c convResp
	_ = json.Unmarshal(body, &c)
	return resp.StatusCode, c, string(body)
}

func TestConversation(t *testing.T) {
	f, _ := conversationFixture(t)

	if resp, _ := f.get(t, "/api/v1/sessions/claude-code:t1/conversation", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if code, _, _ := f.conv(t, "claude-code:nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown session: %d", code)
	}
	if code, _, _ := f.conv(t, "claude-code:t1", "?limit=0"); code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", code)
	}

	code, c, body := f.conv(t, "claude-code:t1", "?limit=3")
	if code != 200 || c.Source != "transcript" || c.Total != 4 || len(c.Messages) != 3 || c.NextOffset == nil || *c.NextOffset != 3 ||
		c.Messages[0].Role != "user" || c.Messages[0].Text != "Add a health check" || c.CaptureLevel != "standard" {
		t.Fatalf("page 1: %d %s", code, body)
	}
	_, c, body = f.conv(t, "claude-code:t1", "?offset=3&limit=3")
	if len(c.Messages) != 1 || c.Messages[0].Text != "Step C" || c.NextOffset != nil {
		t.Fatalf("page 2: %s", body)
	}
	if _, c, _ = f.conv(t, "claude-code:t1", "?offset=99"); len(c.Messages) != 0 || c.Total != 4 {
		t.Fatalf("past the end: %+v", c)
	}

	// Stored fallback: no transcript known; the stored prompt is shown.
	_, c, body = f.conv(t, "claude-code:s1/sub:a1", "")
	if c.Source != "stored" || c.Reason != "no_transcript" || c.Note == "" || len(c.Messages) != 1 || c.Messages[0].Text != "fix the login redirect" {
		t.Fatalf("stored fallback (via a subagent id): %s", body)
	}

	for id, reason := range map[string]string{"claude-code:traversal": "outside_roots", "claude-code:outside": "outside_roots", "claude-code:gone": "transcript_missing"} {
		_, c, body = f.conv(t, id, "")
		if c.Source != "stored" || c.Reason != reason || strings.Contains(body, "not to be read") {
			t.Errorf("%s: want stored/%s, got %s", id, reason, body)
		}
	}

	f.s.Level = redact.Minimal
	_, c, body = f.conv(t, "claude-code:t1", "")
	if c.Source != "none" || c.Reason != "capture_minimal" || len(c.Messages) != 0 || c.CaptureLevel != "minimal" || strings.Contains(body, "health check") {
		t.Fatalf("minimal: %s", body)
	}
}

func TestConversationMarkdownAndHandoff(t *testing.T) {
	f, _ := conversationFixture(t)
	resp, body := f.get(t, "/api/v1/sessions/claude-code:t1/conversation?format=md", bearer)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown") ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="shiplino-claude-code-t1.md"` ||
		!strings.Contains(string(body), "# Health") || !strings.Contains(string(body), "Step C") {
		t.Fatalf("md: %d %v\n%s", resp.StatusCode, resp.Header, body)
	}
	if resp, _ := f.get(t, "/api/v1/sessions/claude-code:t1/conversation?format=html", bearer); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad format: %d", resp.StatusCode)
	}

	resp, body = f.get(t, "/api/v1/sessions/claude-code:t1/handoff", bearer)
	var h struct{ Text, Source string }
	_ = json.Unmarshal(body, &h)
	if resp.StatusCode != 200 || h.Source != "transcript" || !strings.Contains(h.Text, "> Add a health check") || !strings.Contains(h.Text, "> Step C") {
		t.Fatalf("handoff: %d %s", resp.StatusCode, body)
	}
}
