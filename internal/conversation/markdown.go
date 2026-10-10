package conversation

import (
	"fmt"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// Markdown renders a conversation as a Markdown document.
func Markdown(sess *engine.Session, res Result) string {
	var b strings.Builder
	title := sess.Title
	if title == "" {
		title = sess.ID
	}
	fmt.Fprintf(&b, "# %s\n\n", noHTML(oneLine(title)))
	fmt.Fprintf(&b, "- Session: %s\n", codeSpan(sess.ID))
	agent := sess.Agent
	if sess.AgentVersion != "" {
		agent += " " + sess.AgentVersion
	}
	if sess.Model != "" {
		agent += " · " + sess.Model
	}
	fmt.Fprintf(&b, "- Agent: %s\n", agent)
	if sess.ProjectID != "" || sess.Branch != "" {
		fmt.Fprintf(&b, "- Project: %s\n", strings.Trim(sess.ProjectID+" · "+sess.Branch, " ·"))
	}
	if !sess.StartedAt.IsZero() {
		fmt.Fprintf(&b, "- Started: %s\n", sess.StartedAt.UTC().Format(time.RFC3339))
	}
	switch res.Source {
	case SourceTranscript:
		b.WriteString("- Source: the agent's transcript on this machine (secrets redacted)\n")
	case SourceStored:
		b.WriteString("- Source: what Shiplino stored at its capture level (secrets redacted)\n")
	}
	if res.Note != "" {
		fmt.Fprintf(&b, "\n> %s\n", oneLine(res.Note))
	}
	if res.Truncated {
		fmt.Fprintf(&b, "\n> Only the first %d messages are included.\n", MaxMessages)
	}
	for _, m := range res.Messages {
		b.WriteString("\n")
		who := map[string]string{adapters.RoleUser: "User", adapters.RoleAssistant: "Assistant", adapters.RoleTool: "Tool"}[m.Role]
		if m.Subagent != "" {
			who += " (subagent " + m.Subagent + ")"
		}
		when := ""
		if !m.TS.IsZero() {
			when = " · " + m.TS.UTC().Format("2006-01-02 15:04:05Z")
		}
		if m.Role == adapters.RoleTool {
			fmt.Fprintf(&b, "**%s** %s%s", who, codeSpan(m.Tool), when)
			if m.Text != "" {
				fmt.Fprintf(&b, ": %s", codeSpan(oneLine(m.Text)))
			}
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "### %s%s\n\n%s\n", who, when, noHTML(strings.TrimSpace(m.Text)))
	}
	return b.String()
}

// HandoffFile is one changed file for a handoff prompt.
type HandoffFile struct {
	Path           string
	Added, Removed int
}

// maxHandoffFiles and friends keep a handoff prompt short.
const (
	maxHandoffFiles    = 30
	maxHandoffCommands = 5
	maxHandoffText     = 800
)

// Handoff writes a short prompt to continue a session in another agent or
// a new session, from recorded data only (no model call): the goal, the
// project, the files touched, the last state and reply, open todo items
// and failing commands.
func Handoff(sess *engine.Session, files []HandoffFile, evs []model.Event, res Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Continue work from an earlier %s session (id %s).\n", sess.Agent, sess.ID)

	var goal, last string
	var todos []adapters.Todo
	for _, m := range res.Messages {
		switch {
		case m.Role == adapters.RoleUser && goal == "" && m.Subagent == "":
			goal = m.Text
		case m.Role == adapters.RoleAssistant && m.Subagent == "":
			last = m.Text
		case m.Todos != nil && m.Subagent == "":
			todos = m.Todos
		}
	}
	if sess.Title != "" {
		fmt.Fprintf(&b, "\nTask: %s\n", oneLine(sess.Title))
	}
	if goal != "" {
		fmt.Fprintf(&b, "\nGoal (the first prompt):\n%s\n", quote(cut(goal, maxHandoffText)))
	}
	where := []string{}
	if sess.ProjectID != "" {
		where = append(where, "project "+sess.ProjectID)
	}
	if sess.CWD != "" {
		where = append(where, "folder "+sess.CWD)
	}
	if sess.Branch != "" {
		where = append(where, "branch "+sess.Branch)
	}
	if len(where) > 0 {
		fmt.Fprintf(&b, "\nWhere: %s\n", strings.Join(where, ", "))
	}
	status := string(sess.Status)
	if sess.NowDoing != "" {
		status += " (last: " + oneLine(sess.NowDoing) + ")"
	}
	fmt.Fprintf(&b, "Status: %s\n", status)

	if len(files) > 0 {
		fmt.Fprintf(&b, "\nFiles changed (%d, +%d −%d):\n", len(files), sess.LinesAdded, sess.LinesRemoved)
		for i, f := range files {
			if i == maxHandoffFiles {
				fmt.Fprintf(&b, "- … and %d more\n", len(files)-i)
				break
			}
			fmt.Fprintf(&b, "- %s +%d −%d\n", rel(sess.CWD, f.Path), f.Added, f.Removed)
		}
	}

	var open []adapters.Todo
	for _, t := range todos {
		if t.Status != "completed" && t.Status != "cancelled" {
			open = append(open, t)
		}
	}
	if len(open) > 0 {
		b.WriteString("\nOpen todo items:\n")
		for _, t := range open {
			fmt.Fprintf(&b, "- [ ] %s", oneLine(t.Text))
			if t.Status != "" && t.Status != "pending" {
				fmt.Fprintf(&b, " (%s)", t.Status)
			}
			b.WriteString("\n")
		}
	}

	if failing := failingCommands(evs); len(failing) > 0 {
		b.WriteString("\nCommands that failed:\n")
		for _, c := range failing {
			b.WriteString("- " + c + "\n")
		}
	}

	if last != "" {
		fmt.Fprintf(&b, "\nThe agent's last message:\n%s\n", quote(cut(last, maxHandoffText)))
	}
	b.WriteString("\nCheck the current state of the files before you continue.\n")
	return b.String()
}

// failingCommands lists the last few shell commands that exited non-zero,
// each once, oldest first.
func failingCommands(evs []model.Event) []string {
	var out []string
	seen := map[string]bool{}
	for i := len(evs) - 1; i >= 0 && len(out) < maxHandoffCommands; i-- {
		e := evs[i]
		if e.Kind != model.KindShellExec {
			continue
		}
		code, ok := e.Data["exit_code"].(float64)
		if !ok {
			if n, isInt := e.Data["exit_code"].(int); isInt {
				code, ok = float64(n), true
			}
		}
		cmd, _ := e.Data["command"].(string)
		if !ok || code == 0 || cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append([]string{fmt.Sprintf("%s (exit %d)", codeSpan(oneLine(cut(cmd, 200))), int(code))}, out...)
	}
	return out
}

func rel(cwd, p string) string {
	if cwd != "" {
		if r, ok := strings.CutPrefix(p, strings.TrimSuffix(cwd, "/")+"/"); ok {
			return r
		}
	}
	return p
}

func cut(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n]) + "…"
	}
	return strings.TrimSpace(s)
}

// noHTML escapes "<" outside code fences and code spans, so HTML written
// in a transcript (or a prompt) shows as text in any Markdown viewer
// instead of being rendered.
func noHTML(s string) string {
	lines := strings.Split(s, "\n")
	fenced := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.Contains(l, "<") {
			continue
		}
		var b strings.Builder
		inCode := false
		for _, c := range l {
			switch {
			case c == '`':
				inCode = !inCode
			case c == '<' && !inCode:
				b.WriteString("&lt;")
				continue
			}
			b.WriteRune(c)
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func quote(s string) string { return "> " + strings.ReplaceAll(s, "\n", "\n> ") }

// codeSpan wraps s in enough backticks that none inside can end it.
func codeSpan(s string) string {
	run, longest := 0, 0
	for _, c := range s {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}
