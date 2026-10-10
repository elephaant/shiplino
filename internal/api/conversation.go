package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/elephaant/shiplino/internal/conversation"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// maxConversationEvents caps the stored events read for one conversation
// (fallback and transcript discovery).
const maxConversationEvents = 50000

// conversationReader reads conversations at the daemon's capture level.
func (s *Server) conversationReader(ctx context.Context) conversation.Reader {
	level := s.Level
	if level == "" {
		level = redact.Standard
	}
	return conversation.Reader{UserHome: s.UserHome, Level: level, Redactor: s.Redactor,
		Events: func(id string) ([]model.Event, error) { return s.allEvents(ctx, id) }}
}

func (s *Server) allEvents(ctx context.Context, id string) ([]model.Event, error) {
	var out []model.Event
	after := ""
	for len(out) < maxConversationEvents {
		evs, err := s.st.Events(ctx, id, after, 1000)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
		if len(evs) < 1000 {
			break
		}
		after = evs[len(evs)-1].ID
	}
	return out, nil
}

// rootSession loads the top-level session of id (a subagent's id names
// its session's conversation).
func (s *Server) rootSession(w http.ResponseWriter, r *http.Request) *engine.Session {
	sess, err := s.st.Session(r.Context(), rootOf(r.PathValue("id")))
	if err != nil {
		s.internal(w, err)
		return nil
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
	}
	return sess
}

// conversation: GET /api/v1/sessions/{id}/conversation?offset=0&limit=200
// returns the session's messages, read on demand from the agent's own
// transcript (redacted, never stored). format=md returns the whole
// conversation as a Markdown download instead.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format != "" && format != "json" && format != "md" {
		writeError(w, http.StatusBadRequest, "format must be json or md")
		return
	}
	offset, limit := 0, 200
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be 0 or more")
			return
		}
		offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be 1-1000")
			return
		}
		limit = n
	}
	sess := s.rootSession(w, r)
	if sess == nil {
		return
	}
	cr := s.conversationReader(r.Context())
	res, err := cr.Read(sess)
	if err != nil {
		s.internal(w, err)
		return
	}
	if format == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportName(sess.ID)+`.md"`)
		_, _ = w.Write([]byte(conversation.Markdown(sess, res)))
		return
	}
	total := len(res.Messages)
	out := map[string]any{
		"session_id": sess.ID, "source": res.Source, "capture_level": cr.Level,
		"total": total, "offset": offset, "truncated": res.Truncated,
	}
	if res.Reason != "" {
		out["reason"], out["note"] = res.Reason, res.Note
	}
	page := res.Messages[min(offset, total):min(offset+limit, total)]
	out["messages"] = page
	if offset+limit < total {
		out["next_offset"] = offset + limit
	}
	writeJSON(w, http.StatusOK, out)
}

// handoff: GET /api/v1/sessions/{id}/handoff returns {"text": …}, a prompt
// to continue the session elsewhere, built from recorded data only.
func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	sess := s.rootSession(w, r)
	if sess == nil {
		return
	}
	cr := s.conversationReader(r.Context())
	res, err := cr.Read(sess)
	if err != nil {
		s.internal(w, err)
		return
	}
	evs, err := s.allEvents(r.Context(), sess.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	edits, err := s.st.FileEdits(r.Context(), sess.ID, "")
	if err != nil {
		s.internal(w, err)
		return
	}
	var files []conversation.HandoffFile
	for _, f := range summarizeFiles(edits) {
		files = append(files, conversation.HandoffFile{Path: f.Path, Added: f.LinesAdded, Removed: f.LinesRemoved})
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": conversation.Handoff(sess, files, evs, res), "source": res.Source})
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportName is a safe file name for a session's export.
func exportName(id string) string {
	name := unsafeName.ReplaceAllString(id, "-")
	if len(name) > 80 {
		name = name[:80]
	}
	return "shiplino-" + name
}
