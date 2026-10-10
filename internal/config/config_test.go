package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/redact"
)

func TestDefaultsAndRoundTrip(t *testing.T) {
	home := t.TempDir()
	c, err := Load(home)
	if err != nil || c.Level() != redact.Standard {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err = Load(home)
	if err != nil || c.Level() != redact.Standard {
		t.Fatalf("default file: %+v %v", c, err)
	}
	os.WriteFile(Path(home), []byte("capture_level = \"minimal\"\n[redaction]\nextra_patterns = [\"ACME-[0-9]+\"]\n"), 0o600)
	c, err = Load(home)
	if err != nil || c.Level() != redact.Minimal || c.Redactor().Text("ACME-42") != "«redacted:custom»" {
		t.Fatalf("custom: %+v %v", c, err)
	}
	if WriteDefault(home); !strings.Contains(readFile(t, Path(home)), "minimal") {
		t.Fatal("WriteDefault overwrote an existing file")
	}
}

func TestRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"unknown key":  "capture_levle = \"minimal\"\n",
		"bad level":    "capture_level = \"everything\"\n",
		"bad pattern":  "[redaction]\nextra_patterns = [\"([\"]\n",
		"invalid toml": "capture_level = \n",
		"push target":  "[notify.push]\ntargets = [\"pager\"]\n",
		"push event":   "[notify.push]\nevents = [\"prompt\"]\n",
		"channel":      "[update]\nchannel = \"nightly\"\n",
	} {
		home := t.TempDir()
		os.WriteFile(Path(home), []byte(content), 0o600)
		if _, err := Load(home); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotifySettings(t *testing.T) {
	home := t.TempDir()
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if s, on := c.NotifySettings(); !on || !s.Waiting || !s.Finished || !s.Failed || s.MinTurn != 30*time.Second {
		t.Fatalf("defaults: %+v %v", s, on)
	}
	os.WriteFile(Path(home), []byte("[notify]\nfinished = false\nmin_turn = \"2m\"\n"), 0o600)
	c, _ = Load(home)
	if s, on := c.NotifySettings(); !on || s.Finished || !s.Waiting || s.MinTurn != 2*time.Minute {
		t.Fatalf("custom: %+v %v", s, on)
	}
	os.WriteFile(Path(home), []byte("[notify]\nenabled = false\n"), 0o600)
	c, _ = Load(home)
	if _, on := c.NotifySettings(); on {
		t.Fatal("enabled = false ignored")
	}
	os.WriteFile(Path(home), []byte("[notify]\nmin_turn = \"soon\"\n"), 0o600)
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "min_turn") {
		t.Fatalf("bad min_turn: %v", err)
	}
}

func TestLimitSettings(t *testing.T) {
	home := t.TempDir()
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.LimitPercent() != 80 || len(c.Limits.Plans) != 0 {
		t.Fatalf("defaults: %v %v", c.LimitPercent(), c.Limits.Plans)
	}
	cases := []struct {
		file    string
		percent float64
		err     string
	}{
		{"[notify]\nlimit_percent = 90\n[limits.plans]\nclaude-code = \"plan\"\ncodex = \"api\"\n", 90, ""},
		{"[notify]\nenabled = false\n", 0, ""},
		{"[notify]\nlimit_percent = 0\n", 0, ""},
		{"[notify]\nlimit_percent = 120\n", 0, "limit_percent"},
		{"[limits.plans]\ncodex = \"max\"\n", 0, "limits.plans.codex"},
	}
	for _, tc := range cases {
		os.WriteFile(Path(home), []byte(tc.file), 0o600)
		c, err := Load(home)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("%q: err %v", tc.file, err)
			}
			continue
		}
		if err != nil || c.LimitPercent() != tc.percent {
			t.Fatalf("%q: %v %v", tc.file, c.LimitPercent(), err)
		}
	}
}

func TestSyncDefaultsAndValidation(t *testing.T) {
	home := t.TempDir()
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sync.Enabled || c.SyncEndpoint() != DefaultSyncEndpoint || len(c.Sync.Projects) != 0 {
		t.Fatalf("defaults: %+v", c.Sync)
	}
	if c.Sync.SendTitles || len(c.SyncIgnored()) != 0 {
		t.Fatalf("default sync: %+v", c.Sync)
	}
	for name, content := range map[string]string{
		"http endpoint": "[sync]\nendpoint = \"http://sync.example.com\"\n",
		"unknown key":   "[sync]\nenabeld = true\n",
	} {
		os.WriteFile(Path(home), []byte(content), 0o600)
		if _, err := Load(home); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	os.WriteFile(Path(home), []byte("[sync]\nendpoint = \"http://127.0.0.1:9999/\"\n"), 0o600)
	if c, err := Load(home); err != nil || c.SyncEndpoint() != "http://127.0.0.1:9999" {
		t.Fatalf("loopback endpoint: %v %q", err, c.SyncEndpoint())
	}
}

func TestUpdatePush(t *testing.T) {
	// An older file without [notify.push]: the section is appended.
	home := t.TempDir()
	old := "[notify]\nenabled = false # mine\n\n# my budget\n[budget]\ndaily_usd = 5\n"
	os.WriteFile(Path(home), []byte(old), 0o600)
	if err := UpdatePush(home, func(p *Push) { p.Targets = append(p.Targets, "ntfy") }); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, Path(home))
	if !strings.HasPrefix(after, old) {
		t.Fatalf("rest changed:\n%s", after)
	}
	c, err := Load(home)
	if err != nil || len(c.PushSettings().Targets) != 1 || c.Budget.DailyUSD != 5 {
		t.Fatalf("after: %+v %v", c, err)
	}
	// Desktop off, a push target on: plan limit alerts still go out.
	if c.LimitPercent() != 80 {
		t.Fatalf("limit percent %v", c.LimitPercent())
	}
	// In the default file, the section sits between [notify] and the
	// comments of the next table, which stay.
	home = t.TempDir()
	WriteDefault(home)
	if err := UpdatePush(home, func(p *Push) { p.Targets = []string{"slack"} }); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, Path(home))
	if strings.Count(after, "[notify.push]") != 1 || !strings.Contains(after, "targets = ['slack']") || !strings.Contains(after, "\n[budget]\n# Spend limits") {
		t.Fatalf("default file:\n%s", after)
	}
	if err := UpdatePush(home, func(p *Push) { p.Targets = []string{"pager"} }); err == nil {
		t.Fatal("unknown target written")
	}
}

func TestUpdateSyncKeepsTheRestOfTheFile(t *testing.T) {
	home := t.TempDir()
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, Path(home))
	if err := UpdateSync(home, func(s *Sync) {
		s.Enabled = true
		s.Projects = append(s.Projects, "example.com/acme/api")
	}); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, Path(home))
	head := before[:strings.Index(before, "[sync]")]
	if !strings.HasPrefix(after, head) || strings.Count(after, "[sync]") != 1 {
		t.Fatalf("rest of the file changed:\n%s", after)
	}
	c, err := Load(home)
	if err != nil || !c.Sync.Enabled || len(c.Sync.Projects) != 1 || c.Sync.CaptureLevel != "" {
		t.Fatalf("after update: %+v %v", c.Sync, err)
	}
	// A section in the middle of a file, followed by another one.
	os.WriteFile(Path(home), []byte("# mine\ncapture_level = \"full\"\n\n[sync]\nenabled = true\n\n[notify]\nenabled = false # keep\n"), 0o600)
	if err := UpdateSync(home, func(s *Sync) { s.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, Path(home))
	if !strings.HasPrefix(after, "# mine\ncapture_level = \"full\"\n") || !strings.HasSuffix(after, "[notify]\nenabled = false # keep\n") {
		t.Fatalf("neighbours changed:\n%s", after)
	}
	if c, err := Load(home); err != nil || c.Sync.Enabled || c.Level() != redact.Full {
		t.Fatalf("reload: %+v %v", c, err)
	}
	// A broken file is never rewritten.
	os.WriteFile(Path(home), []byte("capture_level = \n"), 0o600)
	if err := UpdateSync(home, func(s *Sync) { s.Enabled = true }); err == nil || readFile(t, Path(home)) != "capture_level = \n" {
		t.Fatalf("broken file: %v", err)
	}
}

func TestSetAutostart(t *testing.T) {
	home := t.TempDir()
	if c, _ := Load(home); !c.Autostart() {
		t.Fatal("autostart is the default")
	}
	// No file yet: the default file is written, with autostart off.
	if err := SetAutostart(home, false); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(home); err != nil || c.Autostart() || c.Level() != redact.Standard {
		t.Fatalf("off: %+v %v", c.Service, err)
	}
	after := readFile(t, Path(home))
	if strings.Count(after, "[service]") != 1 || !strings.Contains(after, "[sync]") {
		t.Fatalf("file:\n%s", after)
	}
	// An older file without [service] keeps its content.
	os.WriteFile(Path(home), []byte("# mine\ncapture_level = \"full\"\n"), 0o600)
	if err := SetAutostart(home, false); err != nil {
		t.Fatal(err)
	}
	if err := SetAutostart(home, true); err != nil {
		t.Fatal(err)
	}
	after = readFile(t, Path(home))
	if !strings.HasPrefix(after, "# mine\ncapture_level = \"full\"\n\n[service]\n") || strings.Count(after, "[service]") != 1 {
		t.Fatalf("file:\n%s", after)
	}
	if c, err := Load(home); err != nil || !c.Autostart() || c.Level() != redact.Full {
		t.Fatalf("on: %+v %v", c.Service, err)
	}
}
