package push

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/elephaant/shiplino/internal/notify"
)

// Keys lists every field an alert can carry off this machine. Payload
// builds nothing else, and every message format is rendered from its
// result, so this list is the whole of what leaves: ids, names, states,
// numbers, times and a link to the local board. Never add content here
// (prompts, titles, commands, file names, the agent's messages).
var Keys = []string{
	"event", "at", "session_id", "agent", "agent_name", "project", "branch", "status", "reason",
	"duration_ms", "cost_usd", "url",
	"scope", "limit_usd", "percent", "window", "resets_at",
	"sessions", "failed", "files_changed", "waiting_ms", "agents",
}

var (
	events   = set(notify.EventWaiting, notify.EventDone, notify.EventFailed, notify.EventBudget, notify.EventLimit, notify.EventDigest, notify.EventTest)
	statuses = set("running", "waiting", "idle", "review", "done", "failed")
	reasons  = set("permission", "question", "idle")
	// token is an id, agent, branch, window or scope: no spaces, short.
	token = regexp.MustCompile(`^[A-Za-z0-9_.:/@+#\-]{1,128}$`)
	// name is a project's short name, which may have spaces.
	name = regexp.MustCompile(`^[\p{L}\p{N}_.@+\- ]{1,64}$`)
)

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Payload is the one function that turns an alert into data for a
// remote target. Strings must look like what they claim to be (an enum,
// an id, a short name) or they are left out; board is the local board's
// base URL ("http://localhost:4777").
func Payload(a notify.Alert, board string) map[string]any {
	p := map[string]any{}
	str := func(k, v string, ok func(string) bool) {
		if v != "" && ok(v) {
			p[k] = v
		}
	}
	in := func(m map[string]bool) func(string) bool { return func(s string) bool { return m[s] } }
	num := func(k string, v float64) {
		if v != 0 && !math.IsNaN(v) && !math.IsInf(v, 0) {
			p[k] = math.Round(v*100) / 100
		}
	}
	whole := func(k string, v int64) {
		if v > 0 {
			p[k] = v
		}
	}
	when := func(k string, t time.Time) {
		if !t.IsZero() {
			p[k] = t.UTC().Format(time.RFC3339)
		}
	}

	str("event", a.Event, in(events))
	if p["event"] == nil {
		return nil
	}
	when("at", a.At)
	str("session_id", a.SessionID, token.MatchString)
	str("agent", a.Agent, token.MatchString)
	if p["agent"] != nil {
		p["agent_name"] = notify.AgentName(a.Agent)
	}
	str("project", strings.TrimSpace(a.Project), name.MatchString)
	str("branch", a.Branch, token.MatchString)
	str("status", a.Status, in(statuses))
	str("reason", a.Reason, in(reasons))
	whole("duration_ms", a.DurationMS)
	num("cost_usd", a.CostUSD)
	if id, ok := p["session_id"].(string); ok && board != "" {
		p["url"] = board + "/session/?id=" + url.QueryEscape(id)
	} else if board != "" {
		p["url"] = board + "/"
	}
	str("scope", a.Scope, func(s string) bool {
		rest, project := strings.CutPrefix(s, "project:")
		if project {
			return name.MatchString(rest)
		}
		return s == "today" || s == "month"
	})
	num("limit_usd", a.LimitUSD)
	num("percent", a.Percent)
	str("window", a.Window, token.MatchString)
	when("resets_at", a.ResetsAt)
	whole("sessions", int64(a.Sessions))
	whole("failed", int64(a.Failed))
	whole("files_changed", int64(a.FilesChanged))
	whole("waiting_ms", a.WaitingMS)
	var agents []string
	for _, id := range a.Agents {
		if token.MatchString(id) {
			agents = append(agents, id)
		}
	}
	if len(agents) > 0 {
		p["agents"] = agents
	}
	return p
}

// message is the human text of one or more alerts, for ntfy, Slack and
// Discord. It is rendered from payloads only.
type message struct {
	Title  string
	Lines  []string
	Link   string // the board, or the session when there is one
	Tags   []string
	Urgent bool // someone waits, or something failed
}

func compose(ps []map[string]any) message {
	if len(ps) == 1 {
		p := ps[0]
		m := message{Title: title(p), Link: s(p, "url"), Tags: []string{tag(p)}}
		if where := where(p); where != "" {
			m.Lines = append(m.Lines, where)
		}
		if d := details(p); d != "" {
			m.Lines = append(m.Lines, d)
		}
		m.Urgent = s(p, "event") == notify.EventWaiting || s(p, "event") == notify.EventFailed
		return m
	}
	m := message{Title: fmt.Sprintf("%d Shiplino alerts", len(ps))}
	same := true
	for _, p := range ps {
		same = same && s(p, "event") == s(ps[0], "event")
		e := s(p, "event")
		m.Urgent = m.Urgent || e == notify.EventWaiting || e == notify.EventFailed
		line := "• " + title(p)
		if w := where(p); w != "" {
			line += " (" + w + ")"
		}
		m.Lines = append(m.Lines, line)
	}
	if same {
		switch s(ps[0], "event") {
		case notify.EventWaiting:
			m.Title = fmt.Sprintf("%d agents need you", len(ps))
		case notify.EventFailed:
			m.Title = fmt.Sprintf("%d agents failed", len(ps))
		case notify.EventDone:
			m.Title = fmt.Sprintf("%d agents finished", len(ps))
		}
		m.Tags = []string{tag(ps[0])}
	}
	if u := s(ps[0], "url"); u != "" {
		if i := strings.Index(u, "/session/"); i > 0 {
			u = u[:i] + "/"
		}
		m.Link = u
	}
	return m
}

func title(p map[string]any) string {
	agent := s(p, "agent_name")
	if agent == "" {
		agent = "An agent"
	}
	switch s(p, "event") {
	case notify.EventWaiting:
		return notify.WaitingTitle(s(p, "agent"), s(p, "reason"))
	case notify.EventDone:
		return agent + " finished"
	case notify.EventFailed:
		return agent + " failed"
	case notify.EventBudget:
		scope := scopeLabel(s(p, "scope"))
		if f(p, "percent") >= 100 {
			return "Budget reached: " + scope
		}
		return fmt.Sprintf("%s: %.0f%% of budget used", scope, f(p, "percent"))
	case notify.EventLimit:
		window := map[string]string{"5h": "5-hour", "7d": "weekly", "1d": "daily"}[s(p, "window")]
		if window == "" {
			window = "usage"
		}
		if f(p, "percent") >= 100 {
			return fmt.Sprintf("%s: %s limit reached", agent, window)
		}
		return fmt.Sprintf("%s: %.0f%% of %s limit used", agent, f(p, "percent"), window)
	case notify.EventDigest:
		return "Today with your agents"
	}
	return "Shiplino test alert"
}

func scopeLabel(scope string) string {
	switch {
	case scope == "today":
		return "Today"
	case scope == "month":
		return "This month"
	case strings.HasPrefix(scope, "project:"):
		return strings.TrimPrefix(scope, "project:") + " today"
	}
	return "Budget"
}

// where is "project · branch".
func where(p map[string]any) string {
	var parts []string
	for _, k := range []string{"project", "branch"} {
		if v := s(p, k); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func details(p map[string]any) string {
	var parts []string
	ms := func(k string) time.Duration {
		v, _ := p[k].(int64)
		return time.Duration(v) * time.Millisecond
	}
	switch s(p, "event") {
	case notify.EventDone:
		if d := ms("duration_ms"); d > 0 {
			parts = append(parts, "finished in "+notify.ShortDuration(d))
		}
	case notify.EventBudget:
		return fmt.Sprintf("$%.2f of $%.2f spent at list prices", f(p, "cost_usd"), f(p, "limit_usd"))
	case notify.EventLimit:
		if t := s(p, "resets_at"); t != "" {
			return "resets " + t
		}
		return ""
	case notify.EventDigest:
		parts = append(parts, fmt.Sprintf("%d sessions", int64Of(p, "sessions")), fmt.Sprintf("$%.2f", f(p, "cost_usd")),
			fmt.Sprintf("%d files changed", int64Of(p, "files_changed")))
		if w := ms("waiting_ms"); w > 0 {
			parts = append(parts, notify.ShortDuration(w)+" waiting on you")
		}
		if n := int64Of(p, "failed"); n > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", n))
		}
		return strings.Join(parts, " · ")
	default:
		if d := ms("duration_ms"); d > 0 {
			parts = append(parts, "session "+notify.ShortDuration(d))
		}
	}
	if c := f(p, "cost_usd"); c > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", c))
	}
	return strings.Join(parts, " · ")
}

func tag(p map[string]any) string {
	switch s(p, "event") {
	case notify.EventWaiting:
		return "bell"
	case notify.EventFailed:
		return "x"
	case notify.EventDone:
		return "white_check_mark"
	case notify.EventBudget, notify.EventLimit:
		return "warning"
	}
	return "robot"
}

func s(p map[string]any, k string) string { v, _ := p[k].(string); return v }

func f(p map[string]any, k string) float64 { v, _ := p[k].(float64); return v }

func int64Of(p map[string]any, k string) int64 { v, _ := p[k].(int64); return v }

// clip caps text at n bytes on a rune boundary.
func clip(t string, n int) string {
	if len(t) <= n {
		return t
	}
	t = t[:n-len("…")]
	for !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	return t + "…"
}
