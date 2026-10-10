package hookfile

import (
	"runtime"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
)

func TestShellQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix quoting")
	}
	cases := map[string]string{
		"/home/u/.shiplino/bin/shiplino": `"/home/u/.shiplino/bin/shiplino"`,
		"/home/a b/shiplino":             `'/home/a b/shiplino'`,
		"/home/o'neil/shiplino":          `'/home/o'\''neil/shiplino'`,
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestPowerShellQuote(t *testing.T) {
	if got := PowerShellQuote(`C:\Users\o'neil\shiplino.exe`); got != `'C:\Users\o''neil\shiplino.exe'` {
		t.Errorf("PowerShellQuote = %s", got)
	}
}

func TestIsOursPerShellFields(t *testing.T) {
	for _, h := range []string{
		`{"bash":"\"/home/dev/.shiplino/bin/shiplino\" hook --agent copilot-cli --event sessionStart"}`,
		`{"powershell":"& 'C:\\Users\\dev\\.shiplino\\bin\\shiplino.exe' hook --agent copilot-cli --event sessionStart"}`,
	} {
		o, err := configfile.ParseObject([]byte(h))
		if err != nil {
			t.Fatal(err)
		}
		if !IsOurs(o, "copilot-cli") || IsOurs(o, "cursor") {
			t.Errorf("IsOurs(%s)", h)
		}
	}
}
