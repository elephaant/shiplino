package claudecode

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
	"github.com/elephaant/shiplino/pkg/model"
)

var userHome = absHome()

func absHome() string {
	if runtime.GOOS == "windows" {
		return `C:\Users\dev`
	}
	return "/home/dev"
}

// commandIn reads statusLine.command from the file ("" if none).
func commandIn(t *testing.T, path string) (string, *configfile.Object) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := configfile.ParseObject(b)
	if err != nil {
		t.Fatal(err)
	}
	sl, cmd, err := statusLineOf(root)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, sl
}

// Commands people really use, with the characters that break naive
// quoting: && pipes, quotes, $, backslashes, newlines and Unicode.
var originals = []string{
	"~/.claude/statusline.sh",
	`jq -r '"[\(.model.display_name)] \(.context_window.used_percentage // 0)% context"'`,
	"input=$(cat); echo \"$input\" | jq -r .model.id && git branch --show-current 2>/dev/null",
	"powershell -NoProfile -File C:/Users/dev/.claude/statusline.ps1",
	"printf 'a\\tb\\n' # ✓ ünïcode\nsecond line",
	"npx -y ccusage statusline",
}

func TestStatusLineWrapAndRestoreExactly(t *testing.T) {
	for _, orig := range originals {
		before := `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": ` + quoteJSON(orig) + `,
    "padding": 2,
    "refreshInterval": 5
  },
  "permissions": { "allow": ["Bash(git status)"] }
}
`
		path, backups := setup(t, before)
		res, err := InstallStatusLine(path, bin, userHome, backups, false)
		if err != nil || !res.Changed || res.Backup == "" {
			t.Fatalf("install %q: %+v %v", orig, res, err)
		}
		cmd, sl := commandIn(t, path)
		st, ok := ParseStatusLineCommand(cmd, userHome)
		if !ok || st.Original != orig || st.Bin != bin {
			t.Errorf("wrapped %q as %q: %+v", orig, cmd, st)
		}
		if p, _ := sl.Get("padding"); p == nil {
			t.Errorf("padding dropped")
		}
		// Installing again changes nothing.
		if res, err := InstallStatusLine(path, bin, userHome, backups, false); err != nil || res.Changed {
			t.Errorf("reinstall: %+v %v", res, err)
		}
		if res, err := UninstallStatusLine(path, userHome, backups); err != nil || !res.Changed {
			t.Fatalf("uninstall: %+v %v", res, err)
		}
		after, _ := os.ReadFile(path)
		if !hookfile.SameJSON([]byte(before), after) {
			t.Errorf("not restored:\n%s\nwant\n%s", after, before)
		}
		if cmd, _ := commandIn(t, path); cmd != orig {
			t.Errorf("restored %q, want %q", cmd, orig)
		}
	}
}

func quoteJSON(s string) string {
	var b strings.Builder
	if err := marshalString(&b, s); err != nil {
		panic(err)
	}
	return b.String()
}

func marshalString(b *strings.Builder, s string) error {
	o := &configfile.Object{Members: []configfile.Member{{Key: "k", Value: s}}}
	raw, err := o.MarshalJSON()
	if err != nil {
		return err
	}
	b.WriteString(strings.TrimSuffix(strings.TrimPrefix(string(raw), `{"k":`), "}"))
	return nil
}

func TestStatusLineKeepsCharactersReadable(t *testing.T) {
	path, backups := setup(t, `{"statusLine":{"type":"command","command":"a && b > c"}}`)
	if _, err := InstallStatusLine(path, bin, userHome, backups, false); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallStatusLine(path, userHome, backups); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"a && b > c"`) {
		t.Errorf("escaped: %s", b)
	}
}

func TestStatusLineWithoutOne(t *testing.T) {
	for name, content := range map[string]string{"no file": "", "empty object": "{}\n", "user keys": userSettings} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, content)
			res, err := InstallStatusLine(path, bin, userHome, backups, false)
			if err != nil || !res.Changed {
				t.Fatalf("install: %+v %v", res, err)
			}
			cmd, _ := commandIn(t, path)
			st, ok := ParseStatusLineCommand(cmd, userHome)
			if !ok || st.Original != "" || st.Minimal {
				t.Errorf("command %q: %+v", cmd, st)
			}
			if got, err := ReadStatusLine(path, userHome); err != nil || !got.Installed || got.Bin != bin {
				t.Errorf("read: %+v %v", got, err)
			}
			// Asking for the minimal line later keeps it ours and marks it.
			if _, err := InstallStatusLine(path, bin, userHome, backups, true); err != nil {
				t.Fatal(err)
			}
			if got, _ := ReadStatusLine(path, userHome); !got.Minimal {
				t.Errorf("not minimal: %+v", got)
			}
			if res, err := UninstallStatusLine(path, userHome, backups); err != nil || !res.Changed {
				t.Fatalf("uninstall: %+v %v", res, err)
			}
			after, err := os.ReadFile(path)
			if content == "" {
				if string(after) != "{}\n" {
					t.Errorf("after = %q", after)
				}
				return
			}
			if err != nil || !hookfile.SameJSON([]byte(content), after) {
				t.Errorf("not restored: %s", after)
			}
		})
	}
}

func TestStatusLineLeavesOtherFilesAlone(t *testing.T) {
	cases := map[string]struct {
		content string
		want    error
	}{
		"comments":      {"{\n  // mine\n  \"statusLine\": {\"type\": \"command\", \"command\": \"x\"}\n}\n", ErrUnparseable},
		"trailing data": {`{"statusLine": {"type": "command", "command": "x"}} {}`, ErrUnparseable},
		"other type":    {`{"statusLine": {"type": "static", "text": "hi"}}`, ErrStatusLineNotCommand},
		"not object":    {`{"statusLine": "x"}`, ErrStatusLineNotCommand},
	}
	for name, c := range cases {
		path, backups := setup(t, c.content)
		_, err := InstallStatusLine(path, bin, userHome, backups, false)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: install err = %v", name, err)
		}
		_, err = UninstallStatusLine(path, userHome, backups)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: uninstall err = %v", name, err)
		}
		if b, _ := os.ReadFile(path); string(b) != c.content {
			t.Errorf("%s: file changed", name)
		}
		if _, err := os.Stat(backups); !os.IsNotExist(err) {
			t.Errorf("%s: backup written for an untouched file", name)
		}
	}
}

func TestStatusLineUninstallLeavesTheUsersOwn(t *testing.T) {
	content := `{"statusLine": {"type": "command", "command": "other-tool statusline"}}`
	path, backups := setup(t, content)
	if res, err := UninstallStatusLine(path, userHome, backups); err != nil || res.Changed {
		t.Errorf("%+v %v", res, err)
	}
	if st, err := ReadStatusLine(path, userHome); err != nil || st.Installed || st.Other != "other-tool statusline" {
		t.Errorf("read: %+v %v", st, err)
	}
	// Removing the hooks leaves the wrapper, and the other way round.
	if _, err := InstallStatusLine(path, bin, userHome, backups, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, bin, "2.1.300", backups); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(path, backups); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadStatusLine(path, userHome); !st.Installed || st.Original != "other-tool statusline" {
		t.Errorf("hooks uninstall touched the status line: %+v", st)
	}
}

func TestStatusLinePointsAtNewBinary(t *testing.T) {
	path, backups := setup(t, `{"statusLine": {"type": "command", "command": "mine.sh"}}`)
	old := absPath("/opt/old/shiplino")
	if _, err := InstallStatusLine(path, old, userHome, backups, false); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallStatusLine(path, bin, userHome, backups, false); err != nil {
		t.Fatal(err)
	}
	st, _ := ReadStatusLine(path, userHome)
	if st.Bin != bin || st.Original != "mine.sh" {
		t.Errorf("%+v", st)
	}
}

func TestStatusLineCommandShapes(t *testing.T) {
	cases := []struct{ bin, home, want string }{
		{"/home/dev/.shiplino/bin/shiplino", "/home/dev", "/home/dev/.shiplino/bin/shiplino statusline"},
		{"/home/my user/.shiplino/bin/shiplino", "/home/my user", "~/.shiplino/bin/shiplino statusline"},
		{"/opt/it's here/shiplino", "/home/dev", `'/opt/it'\''s here/shiplino' statusline`},
	}
	if runtime.GOOS == "windows" {
		cases = []struct{ bin, home, want string }{
			{`C:\Users\dev\.shiplino\bin\shiplino.exe`, `C:\Users\dev`, "C:/Users/dev/.shiplino/bin/shiplino.exe statusline"},
			{`C:\Users\Dev User\.shiplino\bin\shiplino.exe`, `C:\Users\Dev User`, "~/.shiplino/bin/shiplino.exe statusline"},
			{`D:\Tools Dir\shiplino.exe`, `C:\Users\dev`, `"D:/Tools Dir/shiplino.exe" statusline`},
		}
	}
	for _, c := range cases {
		got := StatusLineCommand(c.bin, c.home, "", false)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.bin, got, c.want)
		}
		if runtime.GOOS != "windows" && strings.Contains(c.bin, "'") {
			continue // the regexp reads simple quoting only; still ours by Bin below
		}
		st, ok := ParseStatusLineCommand(got+" --wrap "+"eA", c.home)
		if !ok || st.Bin != c.bin || st.Original != "x" {
			t.Errorf("%s: parsed %+v %v", got, st, ok)
		}
	}
	for _, notOurs := range []string{"ccusage statusline", "npx ccusage statusline", "/usr/bin/shiplino-ish statusline", "shiplino statusline --wrap !!", "shiplino hook --agent claude-code"} {
		if _, ok := ParseStatusLineCommand(notOurs, userHome); ok {
			t.Errorf("%q taken as ours", notOurs)
		}
	}
}

func TestParseStatusLineLimits(t *testing.T) {
	raw := `{"hook_event_name":"StatusLine","session_id":"s-1","version":"2.1.300","model":"claude-opus-5-5",` +
		`"rate_limits":{"seven_day":{"used_percentage":41.2,"resets_at":1738857600},"five_hour":{"used_percentage":23.5,"resets_at":1738425600},` +
		`"seven_day_opus":{"used_percentage":7},"spend_limit":{"used_percentage":3,"resets_at":1},"five_hour_bad":{"used_percentage":1}}}`
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	evs, err := Adapter{}.ParseHook([]byte(raw), adapters.HookMeta{EnvelopeID: "e1", ReceivedAt: now, Event: StatusLineEvent})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("%d events: %+v", len(evs), evs)
	}
	want := []struct {
		window, id string
		used       float64
		resets     string
	}{
		{"5h", "", 23.5, "2025-02-01T16:00:00Z"},
		{"7d", "", 41.2, "2025-02-06T16:00:00Z"},
		{"7d", "opus", 7, ""},
	}
	keys := map[string]bool{}
	for i, e := range evs {
		w := want[i]
		d := e.Data
		if e.Kind != model.KindLimit || e.SessionID != "claude-code:s-1" || e.Agent.Version != "2.1.300" || e.Collector != model.CollectorHook || e.Validate() != nil {
			t.Errorf("event %d: %+v (%v)", i, e, e.Validate())
		}
		if d["limit_window"] != w.window || d["used_percent"] != w.used || d["limit_source"] != "reported" || d["limit_reached"] != nil {
			t.Errorf("event %d data: %v", i, d)
		}
		if id, _ := d["limit_id"].(string); id != w.id {
			t.Errorf("event %d limit_id = %q", i, id)
		}
		if r, _ := d["resets_at"].(string); r != w.resets {
			t.Errorf("event %d resets_at = %q", i, r)
		}
		keys[e.DedupKey] = true
	}
	if len(keys) != 3 {
		t.Errorf("dedup keys collide: %v", keys)
	}
	// Seen again (another session, a refresh): the same keys.
	again, _ := Adapter{}.ParseHook([]byte(strings.Replace(raw, `"s-1"`, `"s-2"`, 1)), adapters.HookMeta{EnvelopeID: "e2", ReceivedAt: now.Add(time.Minute)})
	for _, e := range again {
		if !keys[e.DedupKey] {
			t.Errorf("new key for the same numbers: %s", e.DedupKey)
		}
	}
	// A malformed window doesn't hide the others.
	mixed := `{"hook_event_name":"StatusLine","session_id":"s-1","version":7,"rate_limits":{"five_hour":"x","seven_day":{"used_percentage":2}}}`
	if evs, err := (Adapter{}).ParseHook([]byte(mixed), adapters.HookMeta{ReceivedAt: now}); err != nil || len(evs) != 1 || evs[0].Data["limit_window"] != "7d" {
		t.Errorf("mixed: %v %v", evs, err)
	}
	// Nothing usable: no events, no error.
	for _, in := range []string{
		`{"hook_event_name":"StatusLine","session_id":"s-1"}`,
		`{"hook_event_name":"StatusLine","session_id":"s-1","rate_limits":{"five_hour":{}}}`,
	} {
		if evs, err := (Adapter{}).ParseHook([]byte(in), adapters.HookMeta{ReceivedAt: now}); err != nil || len(evs) != 0 {
			t.Errorf("%s: %v %v", in, evs, err)
		}
	}
}
