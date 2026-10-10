package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/pkg/engine"
)

// Line authorship: which of a commit's added lines came from the linked
// sessions' file edits. A committed line counts as the agent's when an
// edit's diff (kept at capture level full) added the same line to the
// same file; each edited line matches at most one committed line. Below
// full there are no diffs, so only the files the agent touched are known.
const (
	maxAuthorFileLines = 5000 // files adding more lines aren't diffed
	maxAuthorEdits     = 200  // edits read per file and session
	editSkew           = 2 * time.Second
)

// Authorship evidence for a commit or a file.
const (
	authObserved  = "observed"  // every agent-edited line was matched against diffs
	authPartial   = "partial"   // some agent-edited files had no diff (or a cut one)
	authUnknown   = "unknown"   // no diffs at all: capture level below full
	authUntouched = "untouched" // a file no linked session edited
)

// fileAuthorship is one committed file's split.
type fileAuthorship struct {
	Path     string // project-relative
	Added    int
	Agent    int
	Evidence string
}

// authorship is a commit's split of added lines.
type authorship struct {
	Agent, Human, Unknown int
	AgentFiles            []string // project-relative
	Files                 []fileAuthorship
	Evidence              string
}

// data is the authorship as git.commit fields (all metadata: counts,
// an enum and project-relative paths).
func (a authorship) data(d map[string]any) {
	d["agent_lines_added"], d["human_lines_added"], d["unknown_lines_added"] = a.Agent, a.Human, a.Unknown
	d["authorship"] = a.Evidence
	files := make([]any, len(a.AgentFiles))
	for i, f := range a.AgentFiles {
		files[i] = f
	}
	d["agent_files"] = files
	per := make([]any, 0, len(a.Files))
	for _, f := range a.Files {
		per = append(per, map[string]any{"path": f.Path, "lines_added": f.Added, "agent_lines": f.Agent, "authorship": f.Evidence})
	}
	d["authorship_files"] = per
}

// fileEvidence is what the linked sessions' edits say about one file.
type fileEvidence struct {
	touched bool
	full    bool     // every edit kept its whole diff
	added   []string // lines the edits' diffs added
}

// commitAuthorship splits a commit's added lines between the agent's
// edits, other sources and unknown. Called with d.mu held.
func (d *Daemon) commitAuthorship(ctx context.Context, c gitwatch.Commit, attrs []engine.Attribution) authorship {
	// Only files the linked sessions (or their subagents) edited are looked
	// up, so a commit of thousands of files costs a few queries.
	roots := map[string]bool{}
	for _, a := range attrs {
		roots[a.SessionID] = true
	}
	edited := map[string]bool{}
	for _, s := range d.eng.Sessions() {
		if roots[s.RootID] {
			for _, f := range s.Files {
				edited[filepath.Clean(f)] = true
			}
		}
	}
	ev := map[string]*fileEvidence{}
	for _, f := range c.Files {
		if !edited[filepath.Clean(f)] {
			continue
		}
		fe := &fileEvidence{full: true}
		ev[f] = fe
		for _, a := range attrs {
			edits, err := d.st.FileEditEvents(ctx, a.SessionID, "", f, maxAuthorEdits)
			if err != nil {
				d.log.Printf("authorship %s: %v", c.SHA[:min(7, len(c.SHA))], err)
				fe.full = false
				continue
			}
			for _, e := range edits {
				if e.TS.After(c.When.Add(editSkew)) {
					continue // edited after the commit
				}
				fe.touched = true
				p, ok := e.Data["patch"].(string)
				if !ok || e.Data["patch_truncated"] == true {
					fe.full = false
				}
				fe.added = append(fe.added, patchAdded(p)...)
			}
		}
	}
	var diff []string
	for _, f := range c.Files {
		if fe := ev[f]; fe != nil && fe.touched && len(fe.added) > 0 && c.Added[f] > 0 && c.Added[f] <= maxAuthorFileLines {
			diff = append(diff, f)
		}
	}
	committed, err := d.git.Git.AddedLines(c.Worktree, c.SHA, diff)
	if err != nil {
		d.log.Printf("authorship %s: read diff: %v", c.SHA[:min(7, len(c.SHA))], err)
		committed = nil
	}
	return splitAuthorship(c, ev, committed, d.redactor.Text)
}

// splitAuthorship does the counting for commitAuthorship. committed holds
// the commit's added lines for the files that were diffed; redact turns a
// line into its stored form (diffs are stored redacted).
func splitAuthorship(c gitwatch.Commit, ev map[string]*fileEvidence, committed map[string][]string, redact func(string) string) authorship {
	var a authorship
	observed, unknown := 0, 0
	for _, f := range c.Files {
		added := max(c.Added[f], 0)
		rel, err := filepath.Rel(c.Worktree, f)
		if err != nil {
			continue
		}
		fa := fileAuthorship{Path: filepath.ToSlash(rel), Added: added, Evidence: authUntouched}
		fe := ev[f]
		lines, diffed := committed[f]
		switch {
		case fe == nil || !fe.touched:
			a.Human += added
		case added == 0: // only deletions, or a binary file: nothing to split
			fa.Evidence = authObserved
		case !diffed && added > 0:
			fa.Evidence = authUnknown
			a.Unknown += added
			unknown++
		default:
			fa.Agent = matchLines(lines, fe.added, redact)
			fa.Evidence = authObserved
			rest := added - fa.Agent
			if fe.full {
				a.Human += rest
				observed++
			} else {
				fa.Evidence = authPartial
				a.Unknown += rest
				observed++
				unknown++
			}
			a.Agent += fa.Agent
		}
		if fe != nil && fe.touched && len(a.AgentFiles) < maxCommitFiles {
			a.AgentFiles = append(a.AgentFiles, fa.Path)
		}
		if len(a.Files) < maxCommitFiles {
			a.Files = append(a.Files, fa)
		}
	}
	switch {
	case unknown == 0:
		a.Evidence = authObserved
	case observed > 0 || a.Agent > 0:
		a.Evidence = authPartial
	default:
		a.Evidence = authUnknown
	}
	return a
}

// matchLines counts the committed lines that an edit added, each edited
// line used once. Lines are compared without surrounding whitespace (a
// formatter may re-indent); a committed line that doesn't match as is is
// tried again redacted, the form edits are stored in.
func matchLines(committed, edited []string, redact func(string) string) int {
	pool := make(map[string]int, len(edited))
	for _, l := range edited {
		pool[strings.TrimSpace(l)]++
	}
	n := 0
	take := func(k string) bool {
		if pool[k] > 0 {
			pool[k]--
			n++
			return true
		}
		return false
	}
	for _, l := range committed {
		k := strings.TrimSpace(l)
		if !take(k) && redact != nil {
			if r := strings.TrimSpace(redact(l)); r != k {
				take(r)
			}
		}
	}
	return n
}

// patchAdded returns the lines a stored patch adds ("+" lines of its hunks).
func patchAdded(p string) []string {
	var out []string
	for _, l := range strings.Split(p, "\n") {
		if strings.HasPrefix(l, "+") { // stored patches have no file headers
			out = append(out, l[1:])
		}
	}
	return out
}
