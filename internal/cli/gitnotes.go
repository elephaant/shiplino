package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
)

const gitUsage = "usage: shiplino git notes [--dry-run] [<folder>]"

// notesRef is where Shiplino's notes live; it's never pushed.
const notesRef = "refs/notes/shiplino"

// gitCmd: `shiplino git notes` attaches each recorded commit's line
// authorship to the commit as a git note. Writing to the repository is
// opt-in ([git] notes = true); --dry-run only prints the notes.
func gitCmd(ctx context.Context, e *env, args []string) int {
	if len(args) == 0 || args[0] != "notes" {
		fmt.Fprintln(e.errOut, gitUsage)
		return 2
	}
	dry, dir := false, "."
	for _, a := range args[1:] {
		switch {
		case a == "--dry-run":
			dry = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(e.errOut, gitUsage)
			return 2
		default:
			dir = a
		}
	}
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, "shiplino git notes:", err)
		return 1
	}
	if !cfg.Git.Notes && !dry {
		fmt.Fprintf(e.errOut, "Writing git notes is off. Set notes = true under [git] in %s\n"+
			"to let Shiplino write them (to %s, never pushed), or preview them with --dry-run.\n", config.Path(e.home), notesRef)
		return 1
	}
	out, err := runGit(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintf(e.errOut, "shiplino git notes: %s isn't in a git repository\n", dir)
		return 1
	}
	st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db"))
	if err != nil {
		fmt.Fprintln(e.errOut, "shiplino git notes:", err)
		return 1
	}
	defer st.Close()
	n, err := writeNotes(ctx, st, filepath.FromSlash(out), dry, e.out)
	if err != nil {
		fmt.Fprintln(e.errOut, "shiplino git notes:", err)
		return 1
	}
	switch {
	case n == 0:
		fmt.Fprintln(e.out, "No recorded commits with line authorship in this repository.")
	case dry:
		fmt.Fprintf(e.out, "%d notes (dry run: nothing written).\n", n)
	default:
		fmt.Fprintf(e.out, "Wrote %d notes to %s (local only). Show them with: git log --notes=shiplino\n", n, notesRef)
	}
	return 0
}

// writeNotes writes (or, dry, prints) a note for each recorded commit in
// the repository at top that still exists, and returns how many.
func writeNotes(ctx context.Context, st *store.Store, top string, dry bool, out io.Writer) (int, error) {
	events, err := st.CommitEvents(ctx)
	if err != nil {
		return 0, err
	}
	var order []string
	bySHA := map[string][]model.Event{}
	for _, ev := range events {
		sha, _ := ev.Data["sha"].(string)
		if sha == "" || ev.Project == nil || !sameDir(cmp.Or(ev.Project.RepoRoot, ev.Project.CWD), top) {
			continue
		}
		if bySHA[sha] == nil {
			order = append(order, sha)
		}
		bySHA[sha] = append(bySHA[sha], ev)
	}
	n := 0
	for _, sha := range order {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if _, err := runGit(top, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
			continue // rewritten or gone
		}
		note := noteText(bySHA[sha])
		if dry {
			fmt.Fprintf(out, "%s\n%s\n", sha[:min(12, len(sha))], indent(note))
		} else if _, err := runGit(top, strings.NewReader(note), "notes", "--ref="+notesRef, "add", "-f", "-F", "-", sha); err != nil {
			return n, fmt.Errorf("note on %s: %w", sha[:min(12, len(sha))], err)
		}
		n++
	}
	return n, nil
}

// noteText is a commit's note: counts only (no prompts, code or paths).
func noteText(evs []model.Event) string {
	d := evs[0].Data
	var agents []string
	for _, ev := range evs {
		if !slices.Contains(agents, ev.Agent.Name) {
			agents = append(agents, ev.Agent.Name)
		}
	}
	slices.Sort(agents)
	files, _ := d["agent_files"].([]any)
	return fmt.Sprintf("Shiplino line authorship (%s)\nLines added: %d\nAgent lines: %d\nOther lines: %d\nUnknown lines: %d\nAgent-edited files: %d\nAgents: %s\n",
		d["authorship"], int(num(d["lines_added"])), int(num(d["agent_lines_added"])), int(num(d["human_lines_added"])),
		int(num(d["unknown_lines_added"])), len(files), strings.Join(agents, ", "))
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

// sameDir reports whether a and b are the same folder, symlinks resolved.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return canon(a) == canon(b)
}

// runGit runs git in dir and returns its trimmed output.
func runGit(dir string, stdin io.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
