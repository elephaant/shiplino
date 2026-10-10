package agents

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines surround each change.
const diffContext = 3

// maxDiffCells caps the line-matching table. Past it, the changed middle
// of the file is shown as all removed and all added: still a correct
// diff, just not the shortest one.
const maxDiffCells = 4 << 20

// Diff returns c as a unified diff (like `diff -u`), labeled with name.
// A side that doesn't exist is labeled /dev/null.
func (c Change) Diff(name string) string {
	from, to := name, name
	if c.Created {
		from = "/dev/null"
	}
	if c.Removed {
		to = "/dev/null"
	}
	return unified(from, to, splitLines(string(c.Before)), splitLines(string(c.After)))
}

// splitLines keeps each line's "\n", so a missing final newline shows.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type op struct {
	kind byte // ' ', '-' or '+'
	line string
}

// lineOps matches a and b line by line: the common prefix and suffix,
// then a longest common subsequence of the middle.
func lineOps(a, b []string) []op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]op, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		ops = append(ops, op{' ', l})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if (len(ma)+1)*(len(mb)+1) > maxDiffCells {
		for _, l := range ma {
			ops = append(ops, op{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, op{'+', l})
		}
	} else {
		ops = append(ops, lcs(ma, mb)...)
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, op{' ', l})
	}
	return ops
}

func lcs(a, b []string) []op {
	w := len(b) + 1
	// t[i*w+j] is the LCS length of a[i:] and b[j:].
	t := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			} else {
				t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i, j = i+1, j+1
		case t[(i+1)*w+j] >= t[i*w+j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

// unified renders the hunks of a → b; "" when they're equal.
func unified(from, to string, a, b []string) string {
	ops := lineOps(a, b)
	var sb strings.Builder
	for start := 0; start < len(ops); {
		// Find the next change, then extend the hunk while the next change
		// is close enough that their context would touch.
		first := start
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		last := first
		for k := first; k < len(ops); k++ {
			if ops[k].kind != ' ' {
				if k-last > 2*diffContext {
					break
				}
				last = k
			}
		}
		lo, hi := max(first-diffContext, start), min(last+diffContext+1, len(ops))
		if sb.Len() == 0 {
			fmt.Fprintf(&sb, "--- %s\n+++ %s\n", from, to)
		}
		writeHunk(&sb, ops, lo, hi)
		start = hi
	}
	return sb.String()
}

func writeHunk(sb *strings.Builder, ops []op, lo, hi int) {
	// Line numbers of ops[lo] on each side.
	la, lb := 1, 1
	for _, o := range ops[:lo] {
		if o.kind != '+' {
			la++
		}
		if o.kind != '-' {
			lb++
		}
	}
	var na, nb int
	for _, o := range ops[lo:hi] {
		if o.kind != '+' {
			na++
		}
		if o.kind != '-' {
			nb++
		}
	}
	// diff -u numbers an empty range by the line before it.
	if na == 0 {
		la--
	}
	if nb == 0 {
		lb--
	}
	fmt.Fprintf(sb, "@@ -%s +%s @@\n", hunkRange(la, na), hunkRange(lb, nb))
	for _, o := range ops[lo:hi] {
		sb.WriteByte(o.kind)
		sb.WriteString(o.line)
		if !strings.HasSuffix(o.line, "\n") {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

func hunkRange(start, n int) string {
	if n == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}
