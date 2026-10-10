// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

func TestTimelineEndpoint(t *testing.T) {
	f := setup(t)
	resp, body := f.get(t, "/api/v1/timeline", bearer)
	var tl Timeline
	if resp.StatusCode != 200 || json.Unmarshal(body, &tl) != nil {
		t.Fatalf("timeline: %d %s", resp.StatusCode, body)
	}
	// The running root (turn started at setup time) and its finished
	// subagent, which has a zero-length life and so no segment.
	if len(tl.Rows) != 1 || tl.Rows[0].ID != "claude-code:s1" || tl.Rows[0].Segments[0].State != engine.SegRunning {
		t.Fatalf("rows: %s", body)
	}
	for _, q := range []string{"from=yesterday", "from=2026-10-10T00:00:00Z&to=2026-10-09T00:00:00Z", "from=2026-01-01T00:00:00Z&to=2026-10-01T00:00:00Z"} {
		if resp, _ := f.get(t, "/api/v1/timeline?"+q, bearer); resp.StatusCode != 400 {
			t.Errorf("%s: %d", q, resp.StatusCode)
		}
	}
	if resp, _ := f.get(t, "/api/v1/timeline", nil); resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
}

func TestBuildTimelineOrdersSubagentsUnderTheirSession(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	sessions := []*engine.Session{
		{ID: "b", RootID: "b", Status: engine.StatusDone, StartedAt: at(10), LastEventAt: at(20), EndedAt: at(20)},
		{ID: "a/sub:2", RootID: "a", ParentID: "a", Depth: 1, Status: engine.StatusDone, StartedAt: at(4), LastEventAt: at(6), EndedAt: at(6)},
		{ID: "a", RootID: "a", Status: engine.StatusDone, StartedAt: at(0), LastEventAt: at(30), EndedAt: at(30), BestCostUSD: 1.5},
		{ID: "a/sub:1", RootID: "a", ParentID: "a", Depth: 1, Status: engine.StatusDone, StartedAt: at(2), LastEventAt: at(3), EndedAt: at(3)},
		{ID: "old", RootID: "old", Status: engine.StatusDone, StartedAt: at(-100), LastEventAt: at(-90), EndedAt: at(-90)},
	}
	rows := buildTimeline(sessions, nil, at(0), at(60), at(60))
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if got, _ := json.Marshal(ids); string(got) != `["a","a/sub:1","a/sub:2","b"]` {
		t.Fatalf("order: %s", got)
	}
	if rows[0].CostUSD != 1.5 {
		t.Errorf("cost: %v", rows[0].CostUSD)
	}
}

func TestSessionFilesEndpoint(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, _ := f.s.st.Begin(ctx)
	edit := func(key, actor, path string, data map[string]any) {
		data["path"] = path
		if _, err := tx.InsertEvent(ctx, model.Event{ID: model.NewULID(now), V: 1, TS: now, Kind: model.KindFileEdit, Agent: model.Agent{Name: "claude-code"},
			Collector: model.CollectorHook, SessionID: "claude-code:s1", ActorID: actor, DedupKey: key, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	edit("e1", "claude-code:s1", "/home/dev/app/a.go", map[string]any{"op": "modify", "lines_added": 2, "lines_removed": 1, "patch": "@@ @@\n-x\n+y\n+z\n"})
	edit("e2", "claude-code:s1/sub:a1", "/home/dev/app/a.go", map[string]any{"op": "modify", "lines_added": 1, "lines_removed": 0})
	edit("e3", "claude-code:s1", "/home/dev/app/.env", map[string]any{"op": "create", "lines_added": 1, "lines_removed": 0, "patch_omitted": "secret_file"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var list struct{ Files []FileSummary }
	resp, body := f.get(t, "/api/v1/sessions/claude-code:s1/files", bearer)
	if resp.StatusCode != 200 || json.Unmarshal(body, &list) != nil || len(list.Files) != 2 {
		t.Fatalf("files: %d %s", resp.StatusCode, body)
	}
	env, a := list.Files[0], list.Files[1]
	if a.Path != "/home/dev/app/a.go" || a.Edits != 2 || a.LinesAdded != 3 || a.Patches != 1 || len(a.Actors) != 2 {
		t.Errorf("a.go: %+v", a)
	}
	if env.Op != "create" || env.Patches != 0 || env.Omitted != "secret_file" {
		t.Errorf(".env: %+v", env)
	}

	// A subagent sees only its own edits.
	_, body = f.get(t, "/api/v1/sessions/"+url.PathEscape("claude-code:s1/sub:a1")+"/files", bearer)
	if json.Unmarshal(body, &list) != nil || len(list.Files) != 1 || list.Files[0].Edits != 1 {
		t.Errorf("subagent files: %s", body)
	}

	var one struct{ Edits []model.Event }
	_, body = f.get(t, "/api/v1/sessions/claude-code:s1/files?path="+url.QueryEscape("/home/dev/app/a.go"), bearer)
	if json.Unmarshal(body, &one) != nil || len(one.Edits) != 2 || one.Edits[0].Data["patch"] != "@@ @@\n-x\n+y\n+z\n" {
		t.Errorf("edits: %s", body)
	}
}
