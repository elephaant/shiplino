package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/report"
)

// `shiplino report` works on a machine where Shiplino was never set up,
// and writes nothing: no Shiplino home, database, config or spool.
func TestReportNeedsNoSetupAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SHIPLINO_HOME", "")
	ts := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	var lines []string
	for i, u := range []string{`{"input_tokens":10,"output_tokens":5}`, `{"input_tokens":20,"output_tokens":7,"cache_read_input_tokens":1000}`} {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","sessionId":"s1","cwd":"/home/dev/app","timestamp":%q,"message":{"id":"m%d","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"a private reply"}],"usage":%s}}`, ts, i, u))
	}
	path := filepath.Join(home, ".claude", "projects", "-home-dev-app", "s1.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	before := tree(t, home)

	var out, errOut bytes.Buffer
	if code := Run([]string{"report", "--json", "--since", "2d"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var r report.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if r.Schema != report.SchemaVersion || r.Totals.Sessions != 1 || r.Totals.InputTokens != 30 || r.Totals.OutputTokens != 12 || r.Totals.CostUSD <= 0 {
		t.Fatalf("report: %+v", r.Totals)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr (not a terminal) = %q", errOut.String())
	}

	out.Reset()
	if code := Run([]string{"report", "--agent", "claude-code", "--project", "app"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if s := out.String(); !strings.Contains(s, "claude-code") || !strings.Contains(s, "shiplino setup") || strings.Contains(s, "private reply") {
		t.Fatalf("text:\n%s", s)
	}

	if _, err := os.Stat(filepath.Join(home, ".shiplino")); !os.IsNotExist(err) {
		t.Fatalf("~/.shiplino exists: %v", err)
	}
	after := tree(t, home)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("files changed under HOME:\nbefore %v\nafter  %v", before, after)
	}
}

func TestReportFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, args := range [][]string{
		{"report", "--since", "7"},
		{"report", "--since", "0d"},
		{"report", "--agent", "nope"},
		{"report", "extra"},
	} {
		var out, errOut bytes.Buffer
		if code := Run(args, &out, &errOut, "test"); code != 2 || errOut.Len() == 0 {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut.String())
		}
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v", in, got, err)
		}
	}
}

// tree lists every path under root with its size and modification time.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprint(p, fi.Size(), fi.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
