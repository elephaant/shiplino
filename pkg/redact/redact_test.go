// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/model"
)

// Fake secrets are assembled at runtime so secret scanners don't flag
// this file; none of them are real credentials.
func fake(parts ...string) string { return strings.Join(parts, "") }

var (
	ghToken   = fake("gh", "p_", strings.Repeat("A1b2C3d4E5", 4))
	ghPAT     = fake("github", "_pat_", strings.Repeat("11AbCdEfGh", 4))
	antKey    = fake("sk-", "ant-", "api03-", strings.Repeat("Zx9Yw8Vu7T", 4))
	oaiKey    = fake("sk-", "proj-", strings.Repeat("Qw3Er4Ty5U", 4))
	awsKey    = fake("AK", "IA", "IOSFODNN7EXAMPLE")
	slackTok  = fake("xo", "xb-", "1234567890-abcdefghijkl")
	stripeKey = fake("sk", "_live_", strings.Repeat("51HcD8eF", 3))
	googleKey = fake("AI", "za", "SyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q")
	jwt       = fake("ey", "JhbGciOiJIUzI1NiJ9", ".", "ey", "JzdWIiOiIxMjM0NTY3ODkwIn0", ".", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U")
	privKey   = fake("-----BEGIN ", "RSA PRIVATE KEY-----\nMIIEow\nabc\n-----END ", "RSA PRIVATE KEY-----")
)

func TestKnownFormats(t *testing.T) {
	cases := []struct{ in, kind string }{
		{"push with " + ghToken, "github_token"},
		{"token " + ghPAT + " ok", "github_token"},
		{"export ANTHROPIC=" + antKey, "anthropic_key"},
		{"key " + oaiKey, "openai_key"},
		{"aws " + awsKey, "aws_access_key"},
		{"slack " + slackTok, "slack_token"},
		{"stripe " + stripeKey, "stripe_key"},
		{"google " + googleKey, "google_api_key"},
		{"auth " + jwt, "jwt"},
		{privKey, "private_key"},
	}
	for _, c := range cases {
		out := Default.Text(c.in)
		if !strings.Contains(out, Marker(c.kind)) {
			t.Errorf("%s not redacted as %s: %q", c.kind, c.kind, out)
		}
		for _, secret := range []string{ghToken, ghPAT, antKey, oaiKey, awsKey, slackTok, stripeKey, googleKey, jwt, "MIIEow"} {
			if strings.Contains(c.in, secret) && strings.Contains(out, secret) {
				t.Errorf("secret survived in %q", out)
			}
		}
	}
}

func TestContextKeepsTheReadablePart(t *testing.T) {
	cases := map[string]string{
		`curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123"`: `curl -H "Authorization: Bearer «redacted:bearer_token»"`,
		"DATABASE_PASSWORD=hunter2 npm start":                            "DATABASE_PASSWORD=«redacted:secret_assignment» npm start",
		`mysql --password="s3cr3t pass" -u root`:                         `mysql --password=«redacted:secret_flag» -u root`,
		"postgres://app:pa55word@db.internal:5432/app":                   "postgres://app:«redacted:url_password»@db.internal:5432/app",
		`{"api_key": "abc123", "name": "x"}`:                             `{"api_key": "«redacted:secret_json»", "name": "x"}`,
	}
	for in, want := range cases {
		if got := Default.Text(in); got != want {
			t.Errorf("\n in:  %s\n got: %s\nwant: %s", in, got, want)
		}
	}
}

func TestNoFalsePositivesOnOrdinaryText(t *testing.T) {
	for _, s := range []string{
		"git checkout 3f2c1a9b8e7d6c5b4a39281706f5e4d3c2b1a090", // a SHA
		"npm test -- --runInBand src/auth/session.test.ts",
		"Fix the login redirect loop. Users bounce back to /login.",
		"go test ./internal/daemon/... -run TestEndToEnd -count=1",
		"the token budget is 64000 tokens",
		"password reset flow should email the user",
	} {
		if got := Default.Command(s); got != s {
			t.Errorf("changed ordinary text:\n in:  %s\n out: %s", s, got)
		}
	}
}

func TestHighEntropyInCommands(t *testing.T) {
	cmd := "deploy --key Xy7Qw9Lp2Zr8Mn4Kv6Ts1Bd3Fh5Jg0"
	if got := Default.Command(cmd); !strings.Contains(got, Marker("high_entropy")) {
		t.Fatalf("high-entropy token kept: %s", got)
	}
	// Text (prompts etc.) doesn't do entropy masking.
	if got := Default.Text(cmd); got != cmd {
		t.Fatalf("Text masked entropy: %s", got)
	}
}

func TestCustomPatterns(t *testing.T) {
	r, err := New([]string{`ACME-[0-9]{6}`})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Text("ticket ACME-123456 done"); got != "ticket «redacted:custom» done" {
		t.Fatalf("%q", got)
	}
	if _, err := New([]string{`([`}); err == nil {
		t.Fatal("invalid pattern accepted")
	}
}

func TestSecretFile(t *testing.T) {
	for _, p := range []string{"/app/.env", "/app/.env.local", `C:\keys\server.pem`, "/home/u/.ssh/id_rsa", "/home/u/.aws/credentials", "/home/u/.npmrc"} {
		if !SecretFile(p) {
			t.Errorf("%s should be secret", p)
		}
	}
	for _, p := range []string{"/app/src/env.ts", "/app/README.md", "/app/environment.go"} {
		if SecretFile(p) {
			t.Errorf("%s isn't secret", p)
		}
	}
}

func ev(kind model.Kind, data map[string]any) *model.Event {
	return &model.Event{Kind: kind, Data: data}
}

func TestEventStandard(t *testing.T) {
	long := strings.Repeat("x", 5000)
	e := ev(model.KindTurnStart, map[string]any{"prompt": "use " + ghToken + " " + long, "prompt_chars": 5000})
	Default.Event(e, Standard)
	p := e.Data["prompt"].(string)
	if strings.Contains(p, ghToken) || !strings.Contains(p, Marker("github_token")) || len([]rune(p)) > maxPrompt+1 {
		t.Fatalf("prompt: %d chars, %.60q", len([]rune(p)), p)
	}
	sh := ev(model.KindShellExec, map[string]any{"command": "API_TOKEN=abc123 ./deploy.sh", "exit_code": 0})
	Default.Event(sh, Standard)
	if sh.Data["command"] != "API_TOKEN=«redacted:secret_assignment» ./deploy.sh" || sh.Data["exit_code"] != 0 {
		t.Fatalf("command: %v", sh.Data)
	}
	nested := ev(model.KindUsage, map[string]any{"model_usage": map[string]any{"m": map[string]any{"note": "key " + antKey}}})
	Default.Event(nested, Standard)
	if strings.Contains(nested.Data["model_usage"].(map[string]any)["m"].(map[string]any)["note"].(string), antKey) {
		t.Fatal("nested string not redacted")
	}
	list := ev(model.KindNote, map[string]any{"args": []any{"ok", map[string]any{"k": "key " + antKey}, []any{antKey}}})
	Default.Event(list, Standard)
	if b, _ := json.Marshal(list.Data); strings.Contains(string(b), antKey) || !strings.Contains(string(b), `"ok"`) {
		t.Fatalf("strings in lists not redacted: %s", b)
	}
}

func TestEventMinimal(t *testing.T) {
	cases := []*model.Event{
		ev(model.KindTurnStart, map[string]any{"prompt": "secret plans", "prompt_chars": 12, "title": "Plans", "title_source": "agent"}),
		ev(model.KindShellExec, map[string]any{"command": "cat notes.txt", "exit_code": 1, "duration_ms": 5}),
		ev(model.KindToolStart, map[string]any{"tool": "shell", "input_summary": "cat notes.txt"}),
		ev(model.KindToolStart, map[string]any{"tool": "edit", "input_summary": "/app/src/a.go"}),
		ev(model.KindWaitingStart, map[string]any{"reason": "permission", "message": "Approve: rm -rf build"}),
		ev(model.KindToolEnd, map[string]any{"ok": false, "error": "Exit code 1\nsecret output"}),
		ev(model.KindTurnEnd, map[string]any{"status": "ok", "assistant_summary": "Here is the plan…"}),
	}
	for _, e := range cases {
		Default.Event(e, Minimal)
	}
	if _, ok := cases[0].Data["prompt"]; ok || cases[0].Data["prompt_chars"] != 12 || cases[0].Data["title"] != nil || cases[0].Data["title_source"] != nil {
		t.Errorf("turn.start: %v", cases[0].Data)
	}
	if _, ok := cases[1].Data["command"]; ok || cases[1].Data["exit_code"] != 1 {
		t.Errorf("shell.exec: %v", cases[1].Data)
	}
	if _, ok := cases[2].Data["input_summary"]; ok {
		t.Errorf("shell summary kept: %v", cases[2].Data)
	}
	if cases[3].Data["input_summary"] != "/app/src/a.go" {
		t.Errorf("file path dropped: %v", cases[3].Data)
	}
	if cases[4].Data["message"] != "Waiting for your approval" {
		t.Errorf("waiting: %v", cases[4].Data)
	}
	if _, ok := cases[5].Data["error"]; ok {
		t.Errorf("tool error kept: %v", cases[5].Data)
	}
	if _, ok := cases[6].Data["assistant_summary"]; ok || cases[6].Data["status"] != "ok" {
		t.Errorf("turn.end: %v", cases[6].Data)
	}
}

func TestEventFullKeepsLengthButRedacts(t *testing.T) {
	long := strings.Repeat("y", 5000) + " " + ghToken
	e := ev(model.KindTurnStart, map[string]any{"prompt": long})
	Default.Event(e, Full)
	p := e.Data["prompt"].(string)
	if len(p) < 5000 || strings.Contains(p, ghToken) {
		t.Fatalf("full: %d chars, token kept=%v", len(p), strings.Contains(p, ghToken))
	}
}

func TestParseLevel(t *testing.T) {
	if l, err := ParseLevel(""); err != nil || l != Standard {
		t.Fatal(l, err)
	}
	if _, err := ParseLevel("everything"); err == nil {
		t.Fatal("bad level accepted")
	}
}

func TestMinimalMessageOnlyOnWaiting(t *testing.T) {
	commit := model.Event{Kind: model.KindGitCommit, Data: map[string]any{"message": "fix: rotate api key", "sha": "abc123"}}
	Default.Event(&commit, Minimal)
	if _, ok := commit.Data["message"]; ok || commit.Data["sha"] != "abc123" {
		t.Fatalf("commit at minimal: %v", commit.Data)
	}
	wait := model.Event{Kind: model.KindWaitingStart, Data: map[string]any{"message": "Approve: rm -rf build", "reason": "permission"}}
	Default.Event(&wait, Minimal)
	if wait.Data["message"] != "Waiting for your approval" {
		t.Fatalf("waiting at minimal: %v", wait.Data)
	}
}
