package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/elephaant/shiplino/internal/spool"
)

// HookTest runs bin as a hook exactly as an agent would and checks the
// zero-token contract: no output, exit 0, one spool line written (then
// removed). Setup, `shiplino update` and auto_install all run it.
func HookTest(ctx context.Context, home, bin string) error {
	const agent = "shiplino-selftest"
	cmd := exec.CommandContext(ctx, bin, "hook", "--agent", agent)
	cmd.Env = append(os.Environ(), "SHIPLINO_HOME="+home)
	cmd.Stdin = strings.NewReader(`{"session_id":"selftest","hook_event_name":"SessionStart"}`)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	dir := filepath.Join(spool.Dir(home), agent)
	defer os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("hook failed: %v", err)
	}
	if out.Len() > 0 {
		return fmt.Errorf("hook printed output (would cost tokens): %q", out.String())
	}
	if _, err := os.Stat(spool.SessionFile(spool.Dir(home), agent, "selftest")); err != nil {
		return errors.New("hook didn't write to the spool")
	}
	return nil
}
