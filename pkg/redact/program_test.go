package redact

import (
	"testing"

	"github.com/elephaant/shiplino/pkg/model"
)

func TestProgram(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"go test ./...", "go"},
		{"cd /home/dev/api && GOFLAGS=-v go test ./pkg/...", "go"},
		{"/usr/local/bin/npm ci", "npm"},
		{"sudo -E env FOO=1 make build", "make"},
		{"export CI=1; pnpm -r test | tee out.log", "pnpm"},
		{`C:\Tools\Node\NPX.EXE vitest`, "npx"},
		{"(cd web && npm run build)", "npm"},
		{"./scripts/check.sh --fast", "check.sh"},
		{`"python3" -m pytest`, "python3"},
		{"cd /tmp", ""},
		{"", ""},
		{"«redacted:github_token» push", ""},
		{"g++ -o a a.cc", ""},
		{"some-very-long-program-name-that-is-too-long-for-a-token", ""},
		{"Über-tool run", ""},
	}
	for _, c := range cases {
		if got := Program(c.cmd); got != c.want {
			t.Errorf("Program(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestEventDerivesProgram(t *testing.T) {
	for _, level := range []Level{Minimal, Standard, Full} {
		e := model.Event{Kind: model.KindShellExec, Data: map[string]any{"command": "cd api && go test ./...", "exit_code": 1}}
		Default.Event(&e, level)
		if e.Data["program"] != "go" {
			t.Errorf("%s: program = %v", level, e.Data["program"])
		}
	}
	// A secret as the first word never becomes a program.
	e := model.Event{Kind: model.KindShellExec, Data: map[string]any{"command": ghToken + " x"}}
	Default.Event(&e, Standard)
	if _, ok := e.Data["program"]; ok {
		t.Errorf("program from a secret: %v", e.Data["program"])
	}
	// A program sent by an SDK is checked, not trusted.
	e = model.Event{Kind: model.KindShellExec, Data: map[string]any{"program": "rm -rf /home/dev", "command": "make"}}
	Default.Event(&e, Standard)
	if _, ok := e.Data["program"]; ok {
		t.Errorf("invalid program kept: %v", e.Data["program"])
	}
}
