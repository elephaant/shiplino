package copilotcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Session log, checked on 2026-10-10 against the Copilot SDK's session
// event schema (github/copilot-sdk nodejs/src/generated/session-events.ts,
// generated from the CLI's schema), its docs (features/usage-and-billing,
// features/agent-loop) and GitHub's Copilot billing docs: Copilot CLI
// appends every non-ephemeral session event to
// ~/.copilot/session-state/<session id>/events.jsonl as
// {type, data, id, parentId, timestamp, agentId?}. The agentStop hook's
// transcriptPath points at it.
//
// Per-call usage (assistant.usage) is ephemeral and never written. What
// is written is session.shutdown, when a session ends or is exited: its
// data.modelMetrics {<model>: {requests {count}, usage {inputTokens,
// outputTokens, cacheReadTokens, cacheWriteTokens, reasoningTokens},
// totalNanoAiu}} are the session's running totals per model. A resumed
// session writes another shutdown later, so each one is counted as the
// difference from the one before (a total that went down means Copilot
// restarted it, as older versions do on resume: it is counted whole).
// So a session's tokens appear when it ends, not live.
//
// Copilot's inputTokens include cache reads and writes; Shiplino stores
// the uncached part as input. reasoningTokens are part of outputTokens.
// Copilot prices calls itself in AI credits (totalNanoAiu, 1e9 per
// credit; one credit is billed at $0.01), which is the reported cost.
// Without it, tokens are priced from Shiplino's table.

type logLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		CopilotVersion string `json:"copilotVersion"`
		Context        *struct {
			CWD    string `json:"cwd"`
			Branch string `json:"branch"`
		} `json:"context"`
		ModelMetrics map[string]*struct {
			Requests struct {
				Count int64 `json:"count"`
			} `json:"requests"`
			Usage struct {
				InputTokens      int64 `json:"inputTokens"`
				OutputTokens     int64 `json:"outputTokens"`
				CacheReadTokens  int64 `json:"cacheReadTokens"`
				CacheWriteTokens int64 `json:"cacheWriteTokens"`
				ReasoningTokens  int64 `json:"reasoningTokens"`
			} `json:"usage"`
			TotalNanoAiu *float64 `json:"totalNanoAiu"`
		} `json:"modelMetrics"`
	} `json:"data"`
}

// totals is one model's running totals, as kept in meta.State.
type totals struct {
	In, Out, CacheRead, CacheWrite, Reasoning, Requests int64
	NanoAiu                                             float64
}

func (t totals) String() string {
	return fmt.Sprintf("%d %d %d %d %d %d %g", t.In, t.Out, t.CacheRead, t.CacheWrite, t.Reasoning, t.Requests, t.NanoAiu)
}

func parseTotals(s string) (t totals) {
	fmt.Sscanf(s, "%d %d %d %d %d %d %g", &t.In, &t.Out, &t.CacheRead, &t.CacheWrite, &t.Reasoning, &t.Requests, &t.NanoAiu)
	return t
}

// less reports whether any total in t went down from p (a restart).
func (t totals) less(p totals) bool {
	return t.In < p.In || t.Out < p.Out || t.CacheRead < p.CacheRead || t.CacheWrite < p.CacheWrite ||
		t.Reasoning < p.Reasoning || t.Requests < p.Requests || t.NanoAiu < p.NanoAiu
}

func (t totals) minus(p totals) totals {
	return totals{t.In - p.In, t.Out - p.Out, t.CacheRead - p.CacheRead, t.CacheWrite - p.CacheWrite,
		t.Reasoning - p.Reasoning, t.Requests - p.Requests, t.NanoAiu - p.NanoAiu}
}

// usdPerNanoAiu converts Copilot's nano AI units to dollars: 1e9 nano
// units make one AI credit, billed at $0.01.
const usdPerNanoAiu = 0.01 / 1e9

// ParseTranscriptLine implements adapters.TranscriptParser. meta.State
// keeps the session's folder, version and each model's last totals.
func (Adapter) ParseTranscriptLine(raw []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var l logLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("copilot-cli session log: %w", err)
	}
	st := meta.State
	if st == nil {
		st = map[string]string{}
	}
	switch l.Type {
	case "session.start", "session.resume":
		if c := l.Data.Context; c != nil && c.CWD != "" {
			st["cwd"], st["branch"] = c.CWD, c.Branch
		}
		if l.Data.CopilotVersion != "" {
			st["version"] = l.Data.CopilotVersion
		}
		return nil, nil
	case "session.shutdown":
	default:
		return nil, nil
	}
	session := filepath.Base(filepath.Dir(meta.Path))
	if session == "" || session == "." || len(l.Data.ModelMetrics) == 0 {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil {
		ts = meta.ModTime
	}
	ts = ts.UTC()
	sid := model.SessionID(Name, session)
	models := make([]string, 0, len(l.Data.ModelMetrics))
	for m := range l.Data.ModelMetrics {
		models = append(models, m)
	}
	sort.Strings(models)
	var out []model.Event
	for _, m := range models {
		x := l.Data.ModelMetrics[m]
		if x == nil {
			continue
		}
		cur := totals{x.Usage.InputTokens, x.Usage.OutputTokens, x.Usage.CacheReadTokens, x.Usage.CacheWriteTokens,
			x.Usage.ReasoningTokens, x.Requests.Count, 0}
		if x.TotalNanoAiu != nil {
			cur.NanoAiu = *x.TotalNanoAiu
		}
		prev := parseTotals(st["totals:"+m])
		st["totals:"+m] = cur.String()
		d := cur
		if !cur.less(prev) {
			d = cur.minus(prev)
		}
		if meta.Warmup || d == (totals{}) {
			continue
		}
		u := pricing.Usage{Input: max(d.In-d.CacheRead-d.CacheWrite, 0), Output: d.Out, CacheRead: d.CacheRead, CacheWrite5m: d.CacheWrite, At: ts}
		data := map[string]any{
			"model": m, "input_tokens": u.Input, "output_tokens": u.Output,
			"cache_read_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite5m,
		}
		if d.Reasoning > 0 {
			data["reasoning_tokens"] = d.Reasoning
		}
		switch cost, ok := pricing.Default().Cost(priceID(m), u); {
		case x.TotalNanoAiu != nil && d.NanoAiu > 0:
			data["cost_usd"], data["cost_source"] = d.NanoAiu*usdPerNanoAiu, "reported"
		case ok:
			data["cost_usd"], data["cost_source"] = cost, "computed"
		default:
			data["cost_source"] = "unpriced"
		}
		e := model.Event{
			ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts, ReceivedAt: meta.ReceivedAt.UTC(),
			Kind: model.KindUsage, Agent: model.Agent{Name: Name, Version: st["version"], Surface: "cli"},
			Collector: model.CollectorTranscript, User: meta.User, SessionID: sid, ActorID: sid, Data: data,
			DedupKey: sid + ":usage:" + l.ID + ":" + m,
		}
		if st["cwd"] != "" {
			e.Project = &model.Project{CWD: st["cwd"], Branch: st["branch"]}
		}
		if meta.Ref != "" {
			e.Raw = &model.RawRef{Ref: meta.Ref}
		}
		out = append(out, e)
	}
	return out, nil
}

// priceID maps Copilot's model ids to the price table's: "claude-opus-4.7"
// is claude-opus-4-7, and a "-1m" or "-1m-internal" suffix names the same
// model with a longer context.
func priceID(m string) string {
	m = strings.TrimSuffix(strings.TrimSuffix(m, "-1m-internal"), "-1m")
	if strings.HasPrefix(m, "claude-") {
		m = strings.ReplaceAll(m, ".", "-")
	}
	return m
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: every
// session's log.
func (Adapter) TranscriptRoots(userHome string, _, _ time.Time) []string {
	base := filepath.Join(Dir(userHome), "session-state")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	return []string{filepath.Join(base, "*", "events.jsonl")}
}
