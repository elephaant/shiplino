package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
)

// search: GET /api/v1/search?q=&project=&limit=
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "limit must be 1-200")
			return
		}
		limit = n
	}
	hits, err := s.st.Search(r.Context(), q.Get("q"), q.Get("project"), limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	if hits == nil {
		hits = []store.Hit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": hits})
}

// exportColumns are the CSV columns, in order.
var exportColumns = []string{
	"id", "parent_id", "agent", "agent_version", "project_id", "branch", "title", "model", "status",
	"started_at", "ended_at", "last_event_at", "turns", "tool_calls", "tool_errors", "files_changed",
	"lines_added", "lines_removed", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"cost_usd", "cost_source", "waiting_ms",
}

// export: GET /api/v1/export?format=csv|json&project=&since=7d
// Sessions (and subagents) as a file download.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "format must be csv or json")
		return
	}
	var since time.Time
	if v := q.Get("since"); v != "" {
		d, err := parseAge(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must look like 30m, 12h or 7d")
			return
		}
		since = time.Now().Add(-d)
	}
	all, err := s.st.SessionsIn(r.Context(), q.Get("project"), 1_000_000)
	if err != nil {
		s.internal(w, err)
		return
	}
	list := make([]*engine.Session, 0, len(all))
	for _, x := range all {
		if x.LastEventAt.After(since) {
			list = append(list, x)
		}
	}
	name := "shiplino-sessions-" + time.Now().Format("2006-01-02") + "." + format
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if format == "json" {
		writeJSON(w, http.StatusOK, list)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write(exportColumns)
	for _, x := range list {
		_ = cw.Write(exportRow(x))
	}
	cw.Flush()
}

func exportRow(x *engine.Session) []string {
	ts := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	i := strconv.Itoa
	i64 := func(n int64) string { return strconv.FormatInt(n, 10) }
	row := []string{
		x.ID, x.ParentID, x.Agent, x.AgentVersion, x.ProjectID, x.Branch, x.Title, x.Model, string(x.Status),
		ts(x.StartedAt), ts(x.EndedAt), ts(x.LastEventAt), i(x.Turns), i(x.ToolCalls), i(x.ToolErrors), i(x.FilesChanged()),
		i(x.LinesAdded), i(x.LinesRemoved), i64(x.InputTokens), i64(x.OutputTokens), i64(x.CacheReadTokens), i64(x.CacheWriteTokens),
		strconv.FormatFloat(x.BestCostUSD, 'f', 6, 64), x.CostSource, i64(x.WaitingMS),
	}
	for k, v := range row {
		row[k] = safeCell(v)
	}
	return row
}

// safeCell stops spreadsheet apps from running text as a formula:
// titles come from prompts, which anyone can write.
func safeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "'" + v
		}
	}
	return v
}

// parseAge reads durations like 30m, 12h, 7d.
func parseAge(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 0 {
			return 0, strconv.ErrSyntax
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}
