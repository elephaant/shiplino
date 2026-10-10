package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Events we register: observe-only hooks (see the package doc).
var Events = []string{
	"sessionStart", "sessionEnd", "beforeSubmitPrompt", "stop",
	"postToolUse", "postToolUseFailure", "afterFileEdit", "afterMCPExecution",
	"afterAgentResponse", "subagentStop", "preCompact",
}

// Detection describes what was found on this machine.
type Detection struct {
	Installed bool
	Version   string
	HooksPath string
}

// Detect looks for Cursor (IDE or cursor-agent CLI) under home.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{HooksPath: filepath.Join(home, ".cursor", "hooks.json")}
	if fi, err := os.Stat(filepath.Join(home, ".cursor")); err == nil && fi.IsDir() {
		d.Installed = true
	}
	for _, name := range []string{"cursor-agent", "cursor"} {
		bin, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		cancel()
		if m := regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out)); err == nil && m != "" {
			d.Version = m
			break
		}
	}
	return d
}

// Install registers `"<bin>" hook --agent cursor` for Events in the
// user-level hooks.json. It never sets failClosed and only uses
// command hooks.
func Install(hooksPath, binPath, backupDir string) (hookfile.Result, error) {
	if !filepath.IsAbs(binPath) {
		return hookfile.Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	cmd := fmt.Sprintf("%s hook --agent %s", hookfile.ShellQuote(binPath), Name)
	return hookfile.InstallFlat(hooksPath, backupDir, Name, Events, func(string) *configfile.Object {
		return &configfile.Object{Members: []configfile.Member{
			{Key: "command", Value: cmd},
			{Key: "timeout", Value: json.Number("5")},
		}}
	}, func(root *configfile.Object) {
		if _, ok := root.Get("version"); !ok {
			// Cursor requires a version; keep it first, like its docs.
			root.Members = append([]configfile.Member{{Key: "version", Value: json.Number("1")}}, root.Members...)
		}
	})
}

// Uninstall removes only Shiplino's handlers.
func Uninstall(hooksPath, backupDir string) (hookfile.Result, error) {
	return hookfile.Uninstall(hooksPath, backupDir, Name)
}

// Installed reports whether hooks.json has our handler, and its command.
func Installed(hooksPath string) (bool, string, error) {
	return hookfile.Installed(hooksPath, Name)
}
