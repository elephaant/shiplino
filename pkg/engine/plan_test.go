package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// stored round-trips data through JSON, as the store and sync do.
func stored(d map[string]any) map[string]any {
	b, _ := json.Marshal(d)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func TestPlan(t *testing.T) {
	e := New(nil, nil)
	item := func(id, text, status string) model.PlanItem {
		return model.PlanItem{ID: id, Text: text, Status: status}
	}
	full := func(sec int, items ...model.PlanItem) model.Event {
		return ev(sec, model.KindSessionUpdate, stored(model.PlanData(items, false)))
	}
	merge := func(sec int, items ...model.PlanItem) model.Event {
		x := ev(sec, model.KindSessionUpdate, stored(model.PlanData(items, true)))
		e.AnnotatePlan(&x) // as the daemon does before storing
		return x
	}
	steps := []struct {
		name        string
		ev          func() model.Event
		total, done int
		items       int
	}{
		{"full list", func() model.Event {
			return full(10, item("", "Add the form", "completed"), item("", "Wire the API", "in_progress"), item("", "Old idea", "cancelled"))
		}, 2, 1, 3},
		{"replaced", func() model.Event {
			return full(11, item("1", "Add the form", "completed"), item("2", "Wire the API", "pending"))
		}, 2, 1, 2},
		{"merge adds", func() model.Event { return merge(12, item("3", "Write tests", "")) }, 3, 1, 3},
		{"merge completes", func() model.Event { return merge(13, item("2", "", "completed")) }, 3, 2, 3},
		{"merge deletes", func() model.Event { return merge(14, item("3", "", "deleted")) }, 2, 2, 2},
		{"late full list ignored", func() model.Event { return full(5, item("", "Stale", "pending")) }, 2, 2, 2},
	}
	for _, st := range steps {
		e.Apply(st.ev())
		s := e.Get(sid)
		if s.PlanTotal != st.total || s.PlanDone != st.done || len(s.PlanItems) != st.items {
			t.Fatalf("%s: %d/%d with %d items, want %d/%d with %d: %+v", st.name, s.PlanDone, s.PlanTotal, len(s.PlanItems), st.done, st.total, st.items, s.PlanItems)
		}
	}
	s := e.Get(sid)
	if s.PlanItems[1].Text != "Wire the API" || s.PlanItems[1].Status != "completed" || !s.PlanAt.Equal(t0.Add(14*time.Second)) {
		t.Errorf("merged item: %+v at %v", s.PlanItems, s.PlanAt)
	}
	// Other session updates don't touch the plan.
	e.Apply(ev(15, model.KindSessionUpdate, map[string]any{"title": "Login form"}))
	if s.PlanTotal != 2 || s.Title != "Login form" {
		t.Errorf("title update changed the plan: %+v", s)
	}
}

// A synced copy has the counts but no items: merges still count.
func TestPlanCountsWithoutItems(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(1, model.KindSessionUpdate, map[string]any{"plan_total": 3.0, "plan_done": 1.0}))
	e.Apply(ev(2, model.KindSessionUpdate, map[string]any{"plan_merge": true, "plan_total": 3.0, "plan_done": 2.0}))
	if s := e.Get(sid); s.PlanTotal != 3 || s.PlanDone != 2 || len(s.PlanItems) != 0 {
		t.Errorf("plan: %d/%d %+v", s.PlanDone, s.PlanTotal, s.PlanItems)
	}
}

// At the minimal capture level items have ids and statuses, no text.
func TestPlanMinimal(t *testing.T) {
	e := New(nil, nil)
	x := ev(1, model.KindSessionUpdate, map[string]any{"plan_merge": true, "plan_items": []any{
		map[string]any{"id": "1", "status": "pending"}, map[string]any{"id": "2", "status": "completed"},
	}})
	e.AnnotatePlan(&x)
	if x.Data["plan_total"] != 2 || x.Data["plan_done"] != 1 {
		t.Fatalf("annotated: %v", x.Data)
	}
	e.Apply(x)
	if s := e.Get(sid); s.PlanTotal != 2 || s.PlanDone != 1 || s.PlanItems[0].Text != "" {
		t.Errorf("plan: %+v", s)
	}
}
