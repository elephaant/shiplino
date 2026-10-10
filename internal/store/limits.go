package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// LatestLimits returns the newest limit event per agent, limit and window
// length since t.
func (s *Store) LatestLimits(ctx context.Context, since time.Time) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT body FROM (
			SELECT body, ROW_NUMBER() OVER (
				PARTITION BY agent, json_extract(body, '$.data.limit_id'), json_extract(body, '$.data.window_minutes')
				ORDER BY ts DESC, rowid DESC) AS n
			FROM events WHERE kind = 'limit' AND ts >= ?
		) WHERE n = 1`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e model.Event
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UsageRow is the token usage of one model response.
type UsageRow struct {
	Agent   string
	TS      time.Time
	Tokens  int64 // input, output, cache reads and cache writes
	CostUSD float64
}

// UsageSince returns per-response token usage since t in time order. Agent
// cost reports and telemetry stand-ins are left out: they don't add tokens.
func (s *Store) UsageSince(ctx context.Context, since time.Time) ([]UsageRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent, ts,
			COALESCE(json_extract(body, '$.data.input_tokens'), 0) + COALESCE(json_extract(body, '$.data.output_tokens'), 0)
			+ COALESCE(json_extract(body, '$.data.cache_read_tokens'), 0) + COALESCE(json_extract(body, '$.data.cache_write_tokens'), 0),
			COALESCE(json_extract(body, '$.data.cost_usd'), 0)
		FROM events
		WHERE kind = 'usage' AND ts >= ? AND collector != 'otlp' AND json_extract(body, '$.data.report') IS NULL
		ORDER BY ts`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		var ts int64
		if err := rows.Scan(&r.Agent, &ts, &r.Tokens, &r.CostUSD); err != nil {
			return nil, err
		}
		r.TS = time.UnixMilli(ts)
		out = append(out, r)
	}
	return out, rows.Err()
}
