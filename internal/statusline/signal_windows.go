//go:build windows

package statusline

import (
	"os"
	"os/exec"
	"path/filepath"
)

// relaySignals does nothing on Windows: there are no signals to pass on.
func relaySignals(*os.Process) (stop func()) { return func() {} }

// gitBash finds Git Bash as Claude Code does: CLAUDE_CODE_GIT_BASH_PATH,
// else bin\bash.exe of the Git for Windows whose git.exe is on PATH
// (git.exe sits in <root>\cmd or <root>\mingw64\bin). "" means PowerShell
// runs the command.
func gitBash() string {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		return p
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	dir := filepath.Dir(git)
	for _, root := range []string{filepath.Dir(dir), filepath.Dir(filepath.Dir(dir))} {
		b := filepath.Join(root, "bin", "bash.exe")
		if fi, err := os.Stat(b); err == nil && !fi.IsDir() {
			return b
		}
	}
	return ""
}
