package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// topRows caps the projects, models and failures listed in the text form.
const topRows = 5

// Hint ends the text report.
const Hint = "For a live board, history and alerts, run `shiplino setup`."

// WriteText prints the report as a compact terminal summary. It prints
// names, counts, tokens and costs only, never conversation content.
func WriteText(w io.Writer, r *Report) {
	days := int(r.Until.Sub(r.Since).Round(time.Hour).Hours() / 24)
	period := fmt.Sprintf("last %d day%s", days, plural(days))
	if days < 1 {
		period = "last " + duration(r.Until.Sub(r.Since).Milliseconds())
	}
	fmt.Fprintf(w, "Shiplino report: %s (since %s), from %d transcript%s\n", period, r.Since.Local().Format("Jan 2 15:04"), r.Transcripts, plural(r.Transcripts))
	if r.Unreadable > 0 || r.BadLines > 0 {
		fmt.Fprintf(w, "Skipped %d unreadable file%s and %d line%s that failed to parse.\n", r.Unreadable, plural(r.Unreadable), r.BadLines, plural(int(r.BadLines)))
	}
	if r.Totals.Sessions == 0 {
		fmt.Fprintln(w, "\nNo agent sessions found in this period.")
		fmt.Fprintln(w, "\n"+Hint)
		return
	}

	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Agent\tSessions\tSubagents\tActive\tInput\tOutput\tCache read\tCache write\tCost")
	row := func(name string, u Usage) {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", name, u.Sessions, u.Subagents, duration(u.ActiveMS),
			tokens(u.InputTokens), tokens(u.OutputTokens), tokens(u.CacheReadTokens), tokens(u.CacheWriteTokens), money(u.CostUSD))
	}
	for _, a := range r.Agents {
		row(a.Agent, a.Usage)
	}
	if len(r.Agents) > 1 {
		row("Total", r.Totals)
	}
	tw.Flush()

	t := r.Totals
	fmt.Fprintf(w, "\nCost is the API-equivalent price of the tokens: %s reported by the agents, %s computed by Shiplino from token counts.\n",
		money(t.ReportedCostUSD), money(t.ComputedCostUSD))
	if t.Unpriced > 0 {
		fmt.Fprintf(w, "%d session%s used models without a known price (not in the cost).\n", t.Unpriced, plural(t.Unpriced))
	}
	if t.NoUsage > 0 {
		fmt.Fprintf(w, "%d session%s from agents that record no token usage.\n", t.NoUsage, plural(t.NoUsage))
	}

	if len(r.Projects) > 0 {
		fmt.Fprintln(w, "\nTop projects")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, p := range r.Projects[:min(len(r.Projects), topRows)] {
			fmt.Fprintf(tw, "  %s\t%d session%s\t%s\t%s\n", p.Name, p.Sessions, plural(p.Sessions), duration(p.ActiveMS), money(p.CostUSD))
		}
		tw.Flush()
	}
	if len(r.Models) > 0 {
		fmt.Fprintln(w, "\nTop models")
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range r.Models[:min(len(r.Models), topRows)] {
			fmt.Fprintf(tw, "  %s\t%s in\t%s out\t%s cache read\t%s\n", m.Model, tokens(m.InputTokens), tokens(m.OutputTokens), tokens(m.CacheReadTokens), money(m.CostUSD))
		}
		tw.Flush()
	}
	if len(r.Limits) > 0 {
		fmt.Fprintln(w, "\nPlan limits (as last reported)")
		for _, l := range r.Limits {
			fmt.Fprintf(w, "  %s\n", limitLine(l, r.Until))
		}
	}

	f := r.Failures
	if f.FailureCounts != (FailureCounts{}) {
		fmt.Fprintf(w, "\nFailures: %d failed tool call%s, %d failed command%s, %d denial%s, %d retry loop%s, %d session%s ended badly\n",
			f.ToolFailures, plural(f.ToolFailures), f.ShellFailures, plural(f.ShellFailures), f.Denials, plural(f.Denials),
			f.RetryLoops, plural(f.RetryLoops), f.EndedBadly, plural(f.EndedBadly))
		var top []string
		for _, t := range f.Tools[:min(len(f.Tools), 3)] {
			top = append(top, fmt.Sprintf("%s (%s) %d", t.Tool, t.Agent, t.Failures+t.Denials))
		}
		for _, s := range f.Shell[:min(len(f.Shell), 3)] {
			top = append(top, fmt.Sprintf("%s exit %d ×%d", s.Program, s.ExitCode, s.Failures))
		}
		if len(top) > 0 {
			fmt.Fprintf(w, "  Most often: %s\n", strings.Join(top, ", "))
		}
	}
	fmt.Fprintln(w, "\n"+Hint)
}

func limitLine(l Limit, now time.Time) string {
	name := l.Agent + " " + l.Window
	if l.LimitID != "" {
		name += " (" + l.LimitID + ")"
	}
	var parts []string
	switch {
	case l.LimitReached:
		parts = append(parts, "limit reached")
	case l.UsedPercent != nil:
		parts = append(parts, fmt.Sprintf("%.0f%% used", *l.UsedPercent))
	default:
		parts = append(parts, "usage not reported")
	}
	if !l.ResetsAt.IsZero() {
		if d := l.ResetsAt.Sub(now); d > 0 {
			parts = append(parts, "resets in "+duration(d.Milliseconds()))
		} else {
			parts = append(parts, "window has reset since")
		}
	}
	if l.PlanType != "" {
		parts = append(parts, l.PlanType+" plan")
	}
	return name + ": " + strings.Join(parts, ", ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func money(usd float64) string {
	if usd > 0 && usd < 0.01 {
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", usd)
}

// tokens prints a token count compactly: 950, 12.3k, 4.5M, 1.2B.
func tokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%.1fB", float64(n)/1e9)
}

// duration prints milliseconds as "3d 2h", "2h 05m", "12m" or "40s".
func duration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
