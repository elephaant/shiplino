package gitwatch

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxDiffBytes caps how much of a commit's diff AddedLines reads.
const MaxDiffBytes = 4 << 20

// ErrDiffTooBig means a commit's diff was over MaxDiffBytes.
var ErrDiffTooBig = errors.New("diff too big")

// maxLine caps one added line; longer lines are cut (minified files).
const maxLine = 4096

// AddedLines returns the lines a commit added to the given files
// (absolute paths in root), by file. It reads the commit's own diff
// (`git show`, no context, no external diff tools) and stops at
// MaxDiffBytes. Read-only, like every other call.
func (g Exec) AddedLines(root, sha string, files []string) (map[string][]string, error) {
	if len(files) == 0 {
		return map[string][]string{}, nil
	}
	args := []string{"--no-optional-locks", "-C", root, "-c", "core.quotePath=false", "show", "--format=", "--no-color",
		"--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/", sha, "--"}
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		args = append(args, ":(literal)"+filepath.ToSlash(rel))
	}
	t := g.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lim := &io.LimitedReader{R: out, N: MaxDiffBytes + 1}
	added := parseAdded(lim, root)
	if lim.N <= 0 {
		cancel() // stop git; its exit error doesn't matter now
		cmd.Wait()
		return nil, ErrDiffTooBig
	}
	io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return added, nil
}

// parseAdded reads a unified diff and returns the added lines per file,
// keyed by absolute path under root.
func parseAdded(r io.Reader, root string) map[string][]string {
	out := map[string][]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxDiffBytes+1)
	file, inHunk := "", false
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file, inHunk = "", false
		case !inHunk && strings.HasPrefix(l, "+++ "):
			file = ""
			if p, ok := strings.CutPrefix(l[4:], "b/"); ok {
				file = filepath.Join(root, filepath.FromSlash(strings.TrimRight(p, "\t")))
			}
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case inHunk && file != "" && strings.HasPrefix(l, "+"):
			l = l[1:]
			if len(l) > maxLine {
				l = l[:maxLine]
			}
			out[file] = append(out[file], l)
		}
	}
	return out
}
