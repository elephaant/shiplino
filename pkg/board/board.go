// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package board turns sessions into kanban cards: columns from live
// status, pins from user drags, and automatic weekly sprints.
package board

import (
	"errors"
	"sort"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
)

// Column ids, in board order.
const (
	Backlog = "backlog"
	Running = "running"
	Waiting = "waiting"
	Review  = "review"
	Done    = "done"
	Failed  = "failed"
)

// Columns is the default board layout.
var Columns = []ColumnDef{
	{ID: Backlog, Name: "Backlog"},
	{ID: Running, Name: "Running"},
	{ID: Waiting, Name: "Waiting on you"},
	{ID: Review, Name: "Review"},
	{ID: Done, Name: "Done"},
	{ID: Failed, Name: "Failed"},
}

// ColumnDef names a column.
type ColumnDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ValidColumn reports whether id is a known column.
func ValidColumn(id string) bool {
	for _, c := range Columns {
		if c.ID == id {
			return true
		}
	}
	return false
}

// ColumnFor maps a session status to its automatic column.
func ColumnFor(s engine.Status) string {
	switch s {
	case engine.StatusWaiting:
		return Waiting
	case engine.StatusReview:
		return Review
	case engine.StatusDone:
		return Done
	case engine.StatusFailed:
		return Failed
	case engine.StatusIdle:
		return Review // stopped without finishing: worth a look
	}
	return Running
}

// rollUp is a card's live state: a session is waiting if it or any of its
// subagents waits on you, and running if any of them still works.
func rollUp(root *engine.Session, kids []*engine.Session) (engine.Status, string) {
	status, doing := root.Status, root.NowDoing
	if status == engine.StatusWaiting {
		return status, doing
	}
	var running *engine.Session
	for _, k := range kids {
		switch k.Status {
		case engine.StatusWaiting:
			label := k.ActorType
			if label == "" {
				label = "subagent"
			}
			return engine.StatusWaiting, label + ": " + k.NowDoing
		case engine.StatusRunning:
			if running == nil {
				running = k
			}
		}
	}
	if running != nil && status != engine.StatusRunning {
		label := running.ActorType
		if label == "" {
			label = "subagent"
		}
		return engine.StatusRunning, label + ": " + running.NowDoing
	}
	return status, doing
}

// ErrAgentColumn: auto cards can't be put in Running or Waiting by hand;
// those states come from the agent.
var ErrAgentColumn = errors.New("auto cards can't be moved to Running or Waiting; those follow the agent")

// CheckMove validates a manual move.
func CheckMove(origin, column string) error {
	if !ValidColumn(column) {
		return errors.New("unknown column " + column)
	}
	if origin == OriginAuto && (column == Running || column == Waiting) {
		return ErrAgentColumn
	}
	return nil
}

// Card origins.
const (
	OriginAuto   = "auto"
	OriginManual = "manual"
)

// Override is what the user changed about a card (stored per card).
type Override struct {
	CardID    string    `json:"card_id"`
	ProjectID string    `json:"project_id"`
	Origin    string    `json:"origin"`
	Title     string    `json:"title,omitempty"` // manual cards
	Notes     string    `json:"notes,omitempty"`
	Column    string    `json:"column,omitempty"` // set = pinned
	Position  float64   `json:"position,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Subagent is a nested row on a card.
type Subagent struct {
	ID       string        `json:"id"`
	Type     string        `json:"type,omitempty"`
	Status   engine.Status `json:"status"`
	NowDoing string        `json:"now_doing,omitempty"`
	CostUSD  float64       `json:"cost_usd"`
}

// Card is what the board shows.
type Card struct {
	ID             string        `json:"id"`
	Origin         string        `json:"origin"`
	Title          string        `json:"title"`
	TitleSource    string        `json:"title_source,omitempty"`
	Column         string        `json:"column"`
	Pinned         bool          `json:"pinned"`
	Position       float64       `json:"position,omitempty"`
	ProjectID      string        `json:"project_id"`
	Agent          string        `json:"agent,omitempty"`
	Model          string        `json:"model,omitempty"`
	Status         engine.Status `json:"status,omitempty"`
	Branch         string        `json:"branch,omitempty"`
	NowDoing       string        `json:"now_doing,omitempty"`
	StartedAt      time.Time     `json:"started_at"`
	LastEventAt    time.Time     `json:"last_event_at"`
	DurationMS     int64         `json:"duration_ms"`
	WaitingMS      int64         `json:"waiting_ms,omitempty"`
	CostUSD        float64       `json:"cost_usd"`
	CostSource     string        `json:"cost_source,omitempty"`
	Usage          string        `json:"usage,omitempty"` // engine.Session.Usage: "none" means no cost data
	Files          int           `json:"files"`
	LinesAdded     int           `json:"lines_added"`
	LinesRemoved   int           `json:"lines_removed"`
	Links          []engine.Link `json:"links,omitempty"`
	Subagents      []Subagent    `json:"subagents,omitempty"`
	ToolCalls      int           `json:"tool_calls"` // including subagents'
	ActiveMS       int64         `json:"active_ms"`
	Sprint         int           `json:"sprint"`
	RolledOverFrom int           `json:"rolled_over_from,omitempty"`
	Notes          string        `json:"notes,omitempty"`
}

// Build makes cards from a project's sessions (roots and subagents) and
// the user's overrides. now decides sprint membership of open cards.
func Build(sessions []*engine.Session, overrides map[string]Override, cal Calendar, now time.Time) []Card {
	children := map[string][]*engine.Session{}
	for _, s := range sessions {
		if s.ParentID != "" {
			children[s.RootID] = append(children[s.RootID], s)
		}
	}
	var cards []Card
	for _, s := range sessions {
		if s.ParentID != "" {
			continue
		}
		end := s.LastEventAt
		if !s.EndedAt.IsZero() {
			end = s.EndedAt
		}
		status, doing := rollUp(s, children[s.ID])
		c := Card{
			ID: s.ID, Origin: OriginAuto, Title: s.Title, TitleSource: s.TitleSource, Column: ColumnFor(status),
			ProjectID: s.ProjectID, Agent: s.Agent, Model: s.Model, Status: status, Branch: s.Branch, NowDoing: doing,
			StartedAt: s.StartedAt, LastEventAt: s.LastEventAt, DurationMS: end.Sub(s.StartedAt).Milliseconds(), WaitingMS: s.WaitingMS,
			CostUSD: s.BestCostUSD, CostSource: s.CostSource, Usage: s.Usage, Files: len(s.Files), LinesAdded: s.LinesAdded, LinesRemoved: s.LinesRemoved,
			Links: s.Links, ToolCalls: s.ToolCalls, ActiveMS: s.ActiveMS,
		}
		if status == engine.StatusIdle && len(s.Files) == 0 {
			c.Column = Done // went quiet without changing anything
		}
		if c.Title == "" {
			c.Title = "Session in " + s.ProjectID
		}
		files := map[string]bool{}
		for _, f := range s.Files {
			files[f] = true
		}
		for _, ch := range children[s.ID] {
			c.Subagents = append(c.Subagents, Subagent{ID: ch.ID, Type: ch.ActorType, Status: ch.Status, NowDoing: ch.NowDoing, CostUSD: ch.CostUSD})
			// Work done by subagents counts on the card.
			for _, f := range ch.Files {
				files[f] = true
			}
			c.LinesAdded += ch.LinesAdded
			c.LinesRemoved += ch.LinesRemoved
			c.ToolCalls += ch.ToolCalls
		}
		c.Files = len(files)
		sort.Slice(c.Subagents, func(i, j int) bool { return c.Subagents[i].ID < c.Subagents[j].ID })
		closed := c.Column == Done || c.Column == Failed
		if o, ok := overrides[s.ID]; ok {
			if o.Column != "" {
				c.Column, c.Pinned, c.Position = o.Column, true, o.Position
				closed = o.Column == Done || o.Column == Failed
			}
			c.Notes = o.Notes
		}
		c.Sprint, c.RolledOverFrom = cal.Assign(s.StartedAt, end, closed, now)
		cards = append(cards, c)
	}
	for _, o := range overrides {
		if o.Origin != OriginManual {
			continue
		}
		col := o.Column
		if col == "" {
			col = Backlog
		}
		c := Card{ID: o.CardID, Origin: OriginManual, Title: o.Title, Column: col, Pinned: true, Position: o.Position,
			ProjectID: o.ProjectID, StartedAt: o.CreatedAt, LastEventAt: o.CreatedAt, Notes: o.Notes}
		closed := col == Done || col == Failed
		c.Sprint, c.RolledOverFrom = cal.Assign(o.CreatedAt, o.CreatedAt, closed, now)
		if col == Backlog {
			c.Sprint, c.RolledOverFrom = 0, 0 // backlog isn't in a sprint
		}
		cards = append(cards, c)
	}
	return cards
}

// Column is one column of a rendered board.
type Column struct {
	ColumnDef
	Cards []Card `json:"cards"`
}

// Layout groups cards into columns: pinned cards by position, the rest by
// most recent activity.
func Layout(cards []Card) []Column {
	cols := make([]Column, len(Columns))
	idx := map[string]int{}
	for i, d := range Columns {
		cols[i] = Column{ColumnDef: d, Cards: []Card{}}
		idx[d.ID] = i
	}
	for _, c := range cards {
		cols[idx[c.Column]].Cards = append(cols[idx[c.Column]].Cards, c)
	}
	for i := range cols {
		cc := cols[i].Cards
		sort.SliceStable(cc, func(a, b int) bool {
			if cc[a].Pinned != cc[b].Pinned {
				return cc[a].Pinned // pinned cards first, in the user's order
			}
			if cc[a].Pinned {
				return cc[a].Position < cc[b].Position
			}
			return cc[a].LastEventAt.After(cc[b].LastEventAt)
		})
	}
	return cols
}

// FilterSprint keeps cards in sprint n (0 = backlog and every sprint).
func FilterSprint(cards []Card, n int) []Card {
	if n == 0 {
		return cards
	}
	var out []Card
	for _, c := range cards {
		if c.Sprint == n || c.Column == Backlog {
			out = append(out, c)
		}
	}
	return out
}
