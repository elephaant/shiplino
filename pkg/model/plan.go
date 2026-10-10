package model

import "strings"

// An agent's own todo list (its plan) travels in session.update data:
//
//	plan_items  [{id, text, status}]: the items. text is content: it's
//	            stored at the standard and full capture levels only, and
//	            never synced
//	plan_merge  true when the items update the list by id (add, change,
//	            or remove with status "deleted") instead of replacing it
//	plan_total  items that aren't cancelled, after this update
//	plan_done   completed items, after this update
//
// Adapters set plan_total and plan_done on full lists. A merge can only be
// counted against the list so far, so the daemon adds them before storing
// (engine.AnnotatePlan). The counts are metadata and sync; the items don't.

// Plan item statuses, normalized from each agent's own.
const (
	PlanPending    = "pending"
	PlanInProgress = "in_progress"
	PlanCompleted  = "completed"
	PlanCancelled  = "cancelled"
	PlanBlocked    = "blocked"
	PlanDeleted    = "deleted" // merges only: removes the item
)

// maxPlanText caps an item's text.
const maxPlanText = 200

// PlanItem is one entry of an agent's todo list.
type PlanItem struct {
	ID     string `json:"id,omitempty"`
	Text   string `json:"text,omitempty"`
	Status string `json:"status,omitempty"`
}

// PlanStatus normalizes an agent's todo status ("" stays "").
func PlanStatus(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "complete", "done":
		return PlanCompleted
	case "canceled":
		return PlanCancelled
	case "in-progress", "inprogress", "active":
		return PlanInProgress
	case "todo", "not_started":
		return PlanPending
	}
	return s
}

// PlanData is the session.update data for a plan update.
func PlanData(items []PlanItem, merge bool) map[string]any {
	list := make([]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{}
		if it.ID != "" {
			m["id"] = it.ID
		}
		if t := strings.Join(strings.Fields(it.Text), " "); t != "" {
			if r := []rune(t); len(r) > maxPlanText {
				t = string(r[:maxPlanText-1]) + "…"
			}
			m["text"] = t
		}
		if s := PlanStatus(it.Status); s != "" {
			m["status"] = s
		}
		list = append(list, m)
	}
	d := map[string]any{"plan_items": list}
	if merge {
		d["plan_merge"] = true
	} else {
		d["plan_total"], d["plan_done"] = PlanCounts(items)
	}
	return d
}

// PlanCounts counts the items that aren't cancelled (or deleted) and
// the completed ones.
func PlanCounts(items []PlanItem) (total, done int) {
	for _, it := range items {
		switch PlanStatus(it.Status) {
		case PlanCancelled, PlanDeleted:
			continue
		case PlanCompleted:
			done++
		}
		total++
	}
	return total, done
}

// PlanItems reads plan_items from event data (as decoded from JSON or
// built by PlanData). ok is false when the data has none.
func PlanItems(data map[string]any) (items []PlanItem, ok bool) {
	list, ok := data["plan_items"].([]any)
	if !ok {
		return nil, false
	}
	for _, x := range list {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		id, _ := m["id"].(string)
		text, _ := m["text"].(string)
		status, _ := m["status"].(string)
		items = append(items, PlanItem{ID: id, Text: text, Status: status})
	}
	return items, true
}

// MergePlan applies a merge update to list and returns the new list:
// items are matched by id, empty fields keep their value, "deleted"
// removes the item and new ids are appended (pending unless given).
// Items without an id are appended. list itself isn't modified.
func MergePlan(list, update []PlanItem) []PlanItem {
	out := append([]PlanItem(nil), list...)
	for _, u := range update {
		i := -1
		for j := range out {
			if u.ID != "" && out[j].ID == u.ID {
				i = j
				break
			}
		}
		status := PlanStatus(u.Status)
		switch {
		case status == PlanDeleted:
			if i >= 0 {
				out = append(out[:i], out[i+1:]...)
			}
		case i >= 0:
			if u.Text != "" {
				out[i].Text = u.Text
			}
			if status != "" {
				out[i].Status = status
			}
		default:
			if status == "" {
				status = PlanPending
			}
			out = append(out, PlanItem{ID: u.ID, Text: u.Text, Status: status})
		}
	}
	return out
}
