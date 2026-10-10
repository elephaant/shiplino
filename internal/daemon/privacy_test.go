package daemon

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/redact"
)

// allStored returns every stored event body and session body as one string.
func (e *env) allStored() string {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	var b strings.Builder
	for _, q := range []string{`SELECT body FROM events`, `SELECT body FROM sessions`} {
		rows, err := db.Query(q)
		if err != nil {
			e.t.Fatal(err)
		}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			b.WriteString(s)
		}
		rows.Close()
	}
	return b.String()
}

func TestSecretsNeverReachTheDatabase(t *testing.T) {
	e := newEnv(t)
	secret := "gh" + "p_" + strings.Repeat("Q1w2E3r4T5", 4) // assembled so scanners don't flag the test
	e.hook(`{"session_id":"p1","hook_event_name":"UserPromptSubmit","prompt":"deploy with ` + secret + ` please"}`)
	e.hook(`{"session_id":"p1","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"DB_PASSWORD=hunter2 ./migrate"},"tool_response":{},"tool_use_id":"t1"}`)
	e.poll()
	stored := e.allStored()
	for _, leaked := range []string{secret, "hunter2"} {
		if strings.Contains(stored, leaked) {
			t.Errorf("%q stored", leaked)
		}
	}
	if !strings.Contains(stored, "redacted:github_token") || !strings.Contains(stored, "DB_PASSWORD=«redacted:secret_assignment»") {
		t.Errorf("redaction markers missing")
	}
	if s := e.session("claude-code:p1"); strings.Contains(s.Title, secret) || !strings.Contains(s.Title, "redacted") {
		t.Errorf("title: %q", s.Title)
	}
}

func TestMinimalLevelStoresNoContent(t *testing.T) {
	e := newEnv(t)
	e.d.SetPrivacy(redact.Minimal, redact.Default)
	e.hook(`{"session_id":"p2","hook_event_name":"UserPromptSubmit","prompt":"refactor the payments module"}`)
	e.hook(`{"session_id":"p2","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`)
	e.poll()
	stored := e.allStored()
	for _, leaked := range []string{"payments module", "rm -rf build"} {
		if strings.Contains(stored, leaked) {
			t.Errorf("%q stored at minimal level", leaked)
		}
	}
	s := e.session("claude-code:p2")
	if s.Title != "" || s.Status != "waiting" || s.NowDoing != "Waiting for your approval" || s.Turns != 1 {
		t.Errorf("session at minimal: %+v", s)
	}
}
