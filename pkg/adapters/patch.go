package adapters

import (
	"fmt"
	"strings"
)

// Patches on file.edit events ("patch" in data) are unified-diff hunks
// without file headers: "@@ … @@" lines, then " ", "-" and "+" lines.
// They are content: the daemon keeps them only at the full capture level,
// never for secret files, capped and redacted (pkg/redact).
// "patch_source" says where a patch came from: "agent" (the agent's own
// diff) or "computed" (built by Shiplino from the edit's old and new text).

// Hunk is one unified-diff hunk with optional line numbers (0 = unknown).
type Hunk struct {
	OldStart, OldLines, NewStart, NewLines int
	Lines                                  []string // each starts with ' ', '-' or '+'
}

// FormatHunks renders hunks as patch text.
func FormatHunks(hunks []Hunk) string {
	var b strings.Builder
	for _, h := range hunks {
		if h.OldStart > 0 || h.NewStart > 0 {
			fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		} else {
			b.WriteString("@@ @@\n")
		}
		for _, l := range h.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// SnippetHunk is the hunk for replacing old with new, where both are
// snippets of a file with unknown position: lines they share at the start
// and end become context (at most 3 each side), the rest is removed/added.
func SnippetHunk(old, new string) Hunk {
	a, b := splitLines(old), splitLines(new)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var h Hunk
	for _, l := range a[max(0, pre-3):pre] {
		h.Lines = append(h.Lines, " "+l)
	}
	for _, l := range a[pre : len(a)-suf] {
		h.Lines = append(h.Lines, "-"+l)
	}
	for _, l := range b[pre : len(b)-suf] {
		h.Lines = append(h.Lines, "+"+l)
	}
	for _, l := range a[len(a)-suf : min(len(a), len(a)-suf+3)] {
		h.Lines = append(h.Lines, " "+l)
	}
	return h
}

// SnippetPatch is the patch for a set of old → new replacements.
func SnippetPatch(pairs ...[2]string) string {
	var hunks []Hunk
	for _, p := range pairs {
		if p[0] != p[1] {
			hunks = append(hunks, SnippetHunk(p[0], p[1]))
		}
	}
	return FormatHunks(hunks)
}

// NewFilePatch is the patch that creates a file with content.
func NewFilePatch(content string) string {
	lines := splitLines(content)
	if len(lines) == 0 {
		return ""
	}
	h := Hunk{NewStart: 1, NewLines: len(lines)}
	for _, l := range lines {
		h.Lines = append(h.Lines, "+"+l)
	}
	return FormatHunks([]Hunk{h})
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
