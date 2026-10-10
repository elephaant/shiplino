// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/board"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// now is replaceable in tests.
var now = time.Now

func calendarFor(p *store.ProjectSummary) board.Calendar {
	return board.Calendar{Origin: p.FirstSeen}
}

// boardData loads the sessions and overrides that can make cards in
// sprint n (0: every sprint).
func (s *Server) boardData(r *http.Request, p *store.ProjectSummary, n int) ([]*engine.Session, map[string]board.Override, error) {
	var since time.Time
	if n > 1 {
		since = calendarFor(p).Get(n).Starts
	}
	sessions, err := s.st.BoardSessions(r.Context(), p.ID, since)
	if err != nil {
		return nil, nil, err
	}
	overrides, err := s.st.Overrides(r.Context(), p.ID)
	return sessions, overrides, err
}

func (s *Server) projectFromPath(w http.ResponseWriter, r *http.Request) *store.ProjectSummary {
	p, err := s.st.Project(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internal(w, err)
		return nil
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "project not found")
	}
	return p
}

// sprintParam parses ?sprint= (current | all | N).
func sprintParam(r *http.Request, cal board.Calendar) (int, error) {
	switch v := r.URL.Query().Get("sprint"); v {
	case "", "current":
		return cal.Number(now()), nil
	case "all":
		return 0, nil
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, errors.New("sprint must be current, all or a number")
		}
		return n, nil
	}
}

func (s *Server) getBoard(w http.ResponseWriter, r *http.Request) {
	p := s.projectFromPath(w, r)
	if p == nil {
		return
	}
	cal := calendarFor(p)
	n, err := sprintParam(r, cal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessions, overrides, err := s.boardData(r, p, n)
	if err != nil {
		s.internal(w, err)
		return
	}
	cards := board.Build(sessions, overrides, cal, now())
	s.withPRState(r, cards)
	out := map[string]any{"project": p, "columns": board.Layout(board.FilterSprint(cards, n)), "current_sprint": cal.Number(now())}
	if n > 0 {
		out["sprint"] = cal.Get(n)
	}
	writeJSON(w, http.StatusOK, out)
}

// withPRState adds the GitHub integration's pull request state (when it's
// on) to the cards' PR links.
func (s *Server) withPRState(r *http.Request, cards []board.Card) {
	prs, err := s.st.PRs(r.Context())
	if err != nil || len(prs) == 0 {
		return
	}
	for i := range cards {
		for j, l := range cards[i].Links {
			if pr, ok := prs[l.URL]; l.Kind == "pr" && ok {
				cards[i].Links[j].State, cards[i].Links[j].Checks, cards[i].Links[j].Review, cards[i].Links[j].Title = pr.State, pr.Checks, pr.Review, pr.Title
			}
		}
	}
}

type cardPatch struct {
	Column   *string  `json:"column"`
	Position *float64 `json:"position"`
	Pinned   *bool    `json:"pinned"`
	Title    *string  `json:"title"`
	Notes    *string  `json:"notes"`
}

func (s *Server) createCard(w http.ResponseWriter, r *http.Request) {
	p := s.projectFromPath(w, r)
	if p == nil {
		return
	}
	var body struct {
		Title string `json:"title"`
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	o := board.Override{
		CardID: "card:" + model.NewULID(now()), ProjectID: p.ID, Origin: board.OriginManual,
		Title: strings.TrimSpace(body.Title), Notes: body.Notes, Column: board.Backlog, Position: float64(now().UnixMilli()), CreatedAt: now(),
	}
	if err := s.st.PutOverride(r.Context(), o); err != nil {
		s.internal(w, err)
		return
	}
	s.hub.Signal("board:"+p.ID, Message{T: "board.changed", Project: p.ID})
	writeJSON(w, http.StatusCreated, o)
}

func (s *Server) patchCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch cardPatch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	o, err := s.st.Override(r.Context(), id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if o == nil { // first change to an auto card: it must be a known session
		sess, err := s.st.Session(r.Context(), id)
		if err != nil {
			s.internal(w, err)
			return
		}
		if sess == nil || sess.ParentID != "" {
			writeError(w, http.StatusNotFound, "card not found")
			return
		}
		o = &board.Override{CardID: id, ProjectID: sess.ProjectID, Origin: board.OriginAuto}
	}
	if patch.Column != nil {
		if err := board.CheckMove(o.Origin, *patch.Column); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		o.Column = *patch.Column
	}
	if patch.Position != nil {
		o.Position = *patch.Position
	}
	if patch.Pinned != nil && !*patch.Pinned {
		if o.Origin == board.OriginManual {
			writeError(w, http.StatusUnprocessableEntity, "manual cards are always placed by hand")
			return
		}
		o.Column, o.Position = "", 0 // back to following the agent
	}
	if patch.Title != nil {
		if o.Origin != board.OriginManual {
			writeError(w, http.StatusUnprocessableEntity, "auto card titles come from the agent")
			return
		}
		o.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Notes != nil {
		o.Notes = *patch.Notes
	}
	if err := s.st.PutOverride(r.Context(), *o); err != nil {
		s.internal(w, err)
		return
	}
	s.hub.Signal("board:"+o.ProjectID, Message{T: "board.changed", Project: o.ProjectID})
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) deleteCard(w http.ResponseWriter, r *http.Request) {
	o, err := s.st.Override(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internal(w, err)
		return
	}
	if o == nil || o.Origin != board.OriginManual {
		writeError(w, http.StatusNotFound, "manual card not found (agent cards can't be deleted)")
		return
	}
	if err := s.st.DeleteOverride(r.Context(), o.CardID); err != nil {
		s.internal(w, err)
		return
	}
	s.hub.Signal("board:"+o.ProjectID, Message{T: "board.changed", Project: o.ProjectID})
	w.WriteHeader(http.StatusNoContent)
}

// SprintReport summarizes one sprint of one project.
type SprintReport struct {
	Sprint       board.Sprint       `json:"sprint"`
	Done         int                `json:"done"`
	Failed       int                `json:"failed"`
	Open         int                `json:"open"`
	RolledOver   int                `json:"rolled_over"`
	AgentHours   float64            `json:"agent_hours"`
	WaitingHours float64            `json:"waiting_hours"`
	CostUSD      float64            `json:"cost_usd"`
	CostByAgent  map[string]float64 `json:"cost_by_agent"`
	CostByModel  map[string]float64 `json:"cost_by_model"`
	PullRequests int                `json:"pull_requests"`
	Commits      int                `json:"commits"`
	LinesAdded   int                `json:"lines_added"`
	LinesRemoved int                `json:"lines_removed"`
	TopFiles     []FileCount        `json:"top_files"`
}

// FileCount is a file and how many sessions in the sprint edited it.
type FileCount struct {
	Path     string `json:"path"`
	Sessions int    `json:"sessions"`
}

func (s *Server) sprintReport(w http.ResponseWriter, r *http.Request) {
	p := s.projectFromPath(w, r)
	if p == nil {
		return
	}
	cal := calendarFor(p)
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "sprint must be a number")
		return
	}
	sessions, overrides, err := s.boardData(r, p, n)
	if err != nil {
		s.internal(w, err)
		return
	}
	byID := map[string]*engine.Session{}
	for _, se := range sessions {
		byID[se.ID] = se
	}
	rep := SprintReport{Sprint: cal.Get(n), CostByAgent: map[string]float64{}, CostByModel: map[string]float64{}, TopFiles: []FileCount{}}
	files := map[string]int{}
	for _, c := range board.Build(sessions, overrides, cal, now()) {
		if c.Sprint != n || c.Origin != board.OriginAuto {
			continue
		}
		switch c.Column {
		case board.Done:
			rep.Done++
		case board.Failed:
			rep.Failed++
		default:
			rep.Open++
		}
		if c.RolledOverFrom > 0 {
			rep.RolledOver++
		}
		rep.AgentHours += float64(c.DurationMS) / 3.6e6
		rep.WaitingHours += float64(c.WaitingMS) / 3.6e6
		rep.CostUSD += c.CostUSD
		rep.CostByAgent[c.Agent] += c.CostUSD
		model := c.Model
		if model == "" {
			model = "unknown"
		}
		rep.CostByModel[model] += c.CostUSD
		rep.LinesAdded += c.LinesAdded
		rep.LinesRemoved += c.LinesRemoved
		for _, l := range c.Links {
			switch l.Kind {
			case "pr":
				rep.PullRequests++
			case "commit":
				rep.Commits++
			}
		}
		if se := byID[c.ID]; se != nil {
			for _, f := range se.Files {
				files[f]++
			}
		}
	}
	for f, k := range files {
		rep.TopFiles = append(rep.TopFiles, FileCount{Path: f, Sessions: k})
	}
	sort.Slice(rep.TopFiles, func(i, j int) bool {
		if rep.TopFiles[i].Sessions != rep.TopFiles[j].Sessions {
			return rep.TopFiles[i].Sessions > rep.TopFiles[j].Sessions
		}
		return rep.TopFiles[i].Path < rep.TopFiles[j].Path
	})
	if len(rep.TopFiles) > 10 {
		rep.TopFiles = rep.TopFiles[:10]
	}
	writeJSON(w, http.StatusOK, rep)
}

// NeedsYou is one agent waiting for the user.
type NeedsYou struct {
	SessionID string    `json:"session_id"`
	RootID    string    `json:"root_id"`
	ProjectID string    `json:"project_id"`
	Agent     string    `json:"agent"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Since     time.Time `json:"since"`
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	projects, err := s.st.Projects(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	all, err := s.st.Sessions(r.Context(), 0)
	if err != nil {
		s.internal(w, err)
		return
	}
	byProject := map[string][]*engine.Session{}
	titles := map[string]string{}
	var needs []NeedsYou
	for _, se := range all {
		byProject[se.ProjectID] = append(byProject[se.ProjectID], se)
		if se.ParentID == "" {
			titles[se.ID] = se.Title
		}
	}
	for _, se := range all {
		if se.Status == engine.StatusWaiting {
			needs = append(needs, NeedsYou{SessionID: se.ID, RootID: se.RootID, ProjectID: se.ProjectID, Agent: se.Agent,
				Title: titles[se.RootID], Message: se.NowDoing, Since: se.WaitingSince})
		}
	}
	sort.Slice(needs, func(i, j int) bool { return needs[i].Since.Before(needs[j].Since) })
	type row struct {
		store.ProjectSummary
		Sprint     board.Sprint   `json:"sprint"`
		Columns    map[string]int `json:"columns"`
		Subagents  int            `json:"running_subagents"`
		SprintCost float64        `json:"sprint_cost_usd"`
	}
	rows := []row{}
	for i := range projects {
		p := &projects[i]
		cal := calendarFor(p)
		cur := cal.Number(now())
		rw := row{ProjectSummary: *p, Sprint: cal.Get(cur), Columns: map[string]int{}}
		overrides, err := s.st.Overrides(r.Context(), p.ID)
		if err != nil {
			s.internal(w, err)
			return
		}
		for _, c := range board.Build(byProject[p.ID], overrides, cal, now()) {
			if c.Sprint == cur || c.Column == board.Backlog {
				rw.Columns[c.Column]++
				rw.SprintCost += c.CostUSD
			}
			for _, sub := range c.Subagents {
				if sub.Status == engine.StatusRunning {
					rw.Subagents++
				}
			}
		}
		rows = append(rows, rw)
	}
	if needs == nil {
		needs = []NeedsYou{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": rows, "needs_you": needs})
}
