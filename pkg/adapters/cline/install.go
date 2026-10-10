package cline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Events we register: observe-only hooks (see the package doc).
var Events = []string{
	"TaskStart", "TaskResume", "UserPromptSubmit", "PostToolUse",
	"TaskComplete", "TaskCancel", "TaskError", "SessionShutdown",
}

// Note is shown after a fresh install.
const Note = "Cline: hooks apply to new tasks. In VS Code, keep Settings → Features → Hooks on (the default)."

// marker is the line that makes a hook script ours.
const marker = "Shiplino hook: records this Cline event for the Shiplino board."

// Detection describes what was found on this machine.
type Detection struct {
	Installed bool
	Version   string
	HooksDir  string
}

// HooksDir is the global hooks directory both the extension and the CLI
// read when Documents is ~/Documents.
func HooksDir(home string) string {
	return filepath.Join(home, "Documents", "Cline", "Hooks")
}

// extensionDirs are where VS Code and its forks keep extensions.
var extensionDirs = []string{".vscode", ".vscode-insiders", ".vscode-oss", ".vscode-server", ".cursor", ".windsurf"}

var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

// extensionVersion returns whether the Cline extension is installed in a
// VS Code-family editor, and its newest version.
func extensionVersion(home string) (bool, string) {
	var versions []string
	for _, d := range extensionDirs {
		dirs, _ := filepath.Glob(filepath.Join(home, d, "extensions", "saoudrizwan.claude-dev-*"))
		for _, p := range dirs {
			versions = append(versions, semver.FindString(filepath.Base(p)))
		}
	}
	if len(versions) == 0 {
		return false, ""
	}
	sort.Slice(versions, func(i, j int) bool { return lessVersion(versions[i], versions[j]) })
	return true, versions[len(versions)-1]
}

func lessVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if len(pa[i]) != len(pb[i]) {
			return len(pa[i]) < len(pb[i])
		}
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// Detect looks for Cline: the VS Code extension, the cline CLI on PATH,
// or Cline's own folders (~/.cline, ~/Documents/Cline).
func Detect(ctx context.Context, home string) Detection {
	d := Detection{HooksDir: HooksDir(home)}
	d.Installed, d.Version = extensionVersion(home)
	for _, p := range []string{filepath.Join(home, ".cline"), filepath.Dir(d.HooksDir)} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			d.Installed = true
		}
	}
	if bin, err := exec.LookPath("cline"); err == nil {
		d.Installed = true
		if d.Version == "" {
			ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			out, err := exec.CommandContext(ctx, bin, "--version").Output()
			cancel()
			if err == nil {
				d.Version = semver.FindString(string(out))
			}
		}
	}
	return d
}

// NoteExtension is shown after installing into a moved Documents folder.
const NoteExtension = "Cline (VS Code): your Documents folder isn't ~/Documents, so the extension's hooks were added there too."

// DetectExtension finds the extension's own global hooks directory when
// it differs from HooksDir: Documents moved by xdg-user-dirs on Linux or
// into OneDrive on Windows. Only checked for the real user's home, so a
// test's temporary home never reaches the real Documents folder.
func DetectExtension(ctx context.Context, home string) Detection {
	found, version := extensionVersion(home)
	d := Detection{Version: version, HooksDir: HooksDir(home)}
	if real, err := os.UserHomeDir(); err != nil || filepath.Clean(real) != filepath.Clean(home) {
		return d
	}
	if docs := documentsDir(ctx, home); docs != "" {
		if dir := filepath.Join(docs, "Cline", "Hooks"); !samePath(dir, d.HooksDir) {
			d.HooksDir, d.Installed = dir, found
		}
	}
	return d
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// Result reports what Install or Uninstall did.
type Result struct {
	Dir     string
	Changed bool
	Events  []string // events our hook is registered for (Install)
	// Taken are events whose hook file the user already has: Cline runs
	// one file per event and directory, so ours isn't added.
	Taken []string
}

// ErrTaken means some events already have the user's own hook file.
var ErrTaken = errors.New("hook files already exist for")

// Err reports events Install couldn't take, if any.
func (r Result) Err() error {
	if len(r.Taken) == 0 {
		return nil
	}
	return fmt.Errorf("%w %s in %s (yours, kept; Cline runs one per event): those events aren't recorded", ErrTaken, strings.Join(r.Taken, ", "), r.Dir)
}

// powerShell selects the script flavor; a variable so tests cover both.
var powerShell = filepath.Separator == '\\'

// fileName is the hook file for event: <Event> (an executable shell
// script) on macOS and Linux, <Event>.ps1 on Windows. Never both: the CLI
// would run both, and each host ignores the other's on its platform.
func fileName(event string) string {
	if powerShell {
		return event + ".ps1"
	}
	return event
}

// Script is the hook file content that runs the shim with Cline's payload
// on stdin. It discards any output and always exits 0, so a missing
// binary can't make Cline report a failed hook.
func Script(binPath string) []byte {
	if powerShell {
		// Windows PowerShell 5.1 decodes stdin and encodes pipes to native
		// programs in legacy code pages: UTF-8 both ways keeps non-ASCII
		// prompts intact. The BOM makes it read this file as UTF-8 too.
		return []byte("\xef\xbb\xbf# " + marker + "\r\n" +
			"# Managed by `shiplino setup`; `shiplino uninstall` removes it.\r\n" +
			"try {\r\n" +
			"  try { [Console]::InputEncoding = [Text.UTF8Encoding]::new($false) } catch {}\r\n" +
			"  $OutputEncoding = [Text.UTF8Encoding]::new($false)\r\n" +
			"  [Console]::In.ReadToEnd() | & " + hookfile.PowerShellQuote(binPath) + " hook --agent " + Name + " *> $null\r\n" +
			"} catch {}\r\n" +
			"exit 0\r\n")
	}
	return []byte("#!/bin/sh\n" +
		"# " + marker + "\n" +
		"# Managed by `shiplino setup`; `shiplino uninstall` removes it.\n" +
		hookfile.ShellQuote(binPath) + " hook --agent " + Name + " >/dev/null 2>&1\n" +
		"exit 0\n")
}

// ourCommand matches the command line in our scripts (the marker says
// they are ours; the binary may have any name).
var ourCommand = regexp.MustCompile(`(?m)^.*["' ]hook --agent ` + Name + `\b.*$`)

// ours returns the command line of a hook file Shiplino wrote.
func ours(b []byte) (string, bool) {
	if !bytes.Contains(b, []byte(marker)) {
		return "", false
	}
	cmd := ourCommand.Find(b)
	return strings.TrimSpace(string(cmd)), cmd != nil
}

// Install writes one hook script per event into dir, running
// `"<bin>" hook --agent cline`. Existing files of ours are updated in
// place; the user's own files are never touched (see Result.Taken).
func Install(dir, binPath string) (Result, error) {
	res := Result{Dir: dir}
	if !filepath.IsAbs(binPath) {
		return res, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	want := Script(binPath)
	for _, ev := range Events {
		path := filepath.Join(dir, fileName(ev))
		b, err := os.ReadFile(path)
		switch {
		case err == nil && bytes.Equal(b, want):
			res.Events = append(res.Events, ev)
			continue
		case err == nil:
			if _, ok := ours(b); !ok {
				res.Taken = append(res.Taken, ev)
				continue
			}
		case !errors.Is(err, os.ErrNotExist):
			return res, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return res, err
		}
		if err := configfile.WriteAtomic(path, want); err != nil {
			return res, err
		}
		// Executable: the extension runs only files with the x bit.
		if err := os.Chmod(path, 0o755); err != nil {
			return res, err
		}
		res.Events = append(res.Events, ev)
		res.Changed = true
	}
	return res, nil
}

// Uninstall removes only the hook files Shiplino wrote, for every event
// it ever registered and both script flavors.
func Uninstall(dir string) (Result, error) {
	res := Result{Dir: dir}
	for _, ev := range Events {
		for _, name := range []string{ev, ev + ".ps1"} {
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return res, err
			}
			if _, ok := ours(b); !ok {
				continue
			}
			if err := os.Remove(path); err != nil {
				return res, err
			}
			res.Changed = true
		}
	}
	return res, nil
}

// Installed reports whether dir has our hook scripts, and their command.
// An error lists events missing ours because the user's own file is
// there.
func Installed(dir string) (bool, string, error) {
	var cmd string
	var taken []string
	for _, ev := range Events {
		b, err := os.ReadFile(filepath.Join(dir, fileName(ev)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, "", err
		}
		if c, ok := ours(b); ok {
			cmd = c
		} else {
			taken = append(taken, ev)
		}
	}
	return cmd != "", cmd, Result{Dir: dir, Taken: taken}.Err()
}
