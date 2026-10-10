package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/otlp"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Ingester stores events posted to the API by custom agents and OTLP
// exporters. The daemon implements it; nil disables those endpoints.
type Ingester interface {
	Ingest(ctx context.Context, events []model.Event) (accepted, duplicates int, err error)
}

// ErrPaused is returned by an Ingester while recording is paused.
var ErrPaused = errors.New("recording is paused")

// Limits per request, for /api/v1/ingest and the OTLP receiver.
const (
	MaxIngestEvents = 1000
	MaxIngestBytes  = 4 << 20 // after decompression
)

// IngestStats counts ingest and OTLP traffic since the daemon started.
type IngestStats struct {
	Requests    int64      `json:"requests"` // to /api/v1/ingest
	Accepted    int64      `json:"accepted"`
	Duplicates  int64      `json:"duplicates"`
	Rejected    int64      `json:"rejected"` // invalid events
	OTLPLogs    otlp.Stats `json:"otlp_logs"`
	OTLPMetrics int64      `json:"otlp_metric_requests"` // counted, not decoded
	OTLPTraces  int64      `json:"otlp_trace_requests"`  // counted, not decoded
	LastError   string     `json:"last_error,omitempty"` // last rejected request or event
}

// IngestError reports one rejected event by its position in the request.
type IngestError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

// IngestResult is what POST /api/v1/ingest returns.
type IngestResult struct {
	Accepted   int           `json:"accepted"`
	Duplicates int           `json:"duplicates"`
	Errors     []IngestError `json:"errors"`
	Paused     bool          `json:"paused,omitempty"` // nothing was stored
}

func (s *Server) countIngest(f func(*IngestStats)) {
	s.ingestMu.Lock()
	f(&s.ingestStats)
	s.ingestMu.Unlock()
}

// IngestStats returns a snapshot of the counters.
func (s *Server) IngestStats() IngestStats {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	return s.ingestStats
}

// ingest: POST /api/v1/ingest with a JSON array of universal events, or
// NDJSON (one event per line) with Content-Type: application/x-ndjson.
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if s.Ingest == nil {
		writeError(w, http.StatusNotImplemented, "ingest unavailable")
		return
	}
	s.countIngest(func(st *IngestStats) { st.Requests++ })
	body, code, err := readBody(w, r)
	if err != nil {
		s.rejectIngest(w, code, err.Error())
		return
	}
	raws, err := splitEvents(body, r.Header.Get("Content-Type"))
	if err != nil {
		s.rejectIngest(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(raws) > MaxIngestEvents {
		s.rejectIngest(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d events per request", MaxIngestEvents))
		return
	}
	now := time.Now()
	res := IngestResult{Errors: []IngestError{}}
	events := make([]model.Event, 0, len(raws))
	for i, raw := range raws {
		e, err := ingestEvent(raw, now)
		if err != nil {
			res.Errors = append(res.Errors, IngestError{Index: i, Error: err.Error()})
			continue
		}
		events = append(events, e)
	}
	if len(events) > 0 {
		res.Accepted, res.Duplicates, err = s.Ingest.Ingest(r.Context(), events)
		switch {
		case errors.Is(err, ErrPaused):
			res.Paused = true
		case err != nil:
			s.internal(w, err)
			return
		}
	}
	s.countIngest(func(st *IngestStats) {
		st.Accepted += int64(res.Accepted)
		st.Duplicates += int64(res.Duplicates)
		st.Rejected += int64(len(res.Errors))
		if len(res.Errors) > 0 {
			st.LastError = fmt.Sprintf("event %d: %s", res.Errors[0].Index, res.Errors[0].Error)
		}
	})
	code = http.StatusOK
	if len(events) == 0 && len(res.Errors) > 0 {
		code = http.StatusBadRequest
	}
	writeJSON(w, code, res)
}

func (s *Server) rejectIngest(w http.ResponseWriter, code int, msg string) {
	s.countIngest(func(st *IngestStats) { st.LastError = msg })
	writeError(w, code, msg)
}

// readBody reads a request body of at most MaxIngestBytes, gzip-decoded
// if needed (OTLP exporters often compress). On error it returns the
// status code to answer with.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	var in io.Reader = http.MaxBytesReader(w, r.Body, MaxIngestBytes)
	switch enc := strings.ToLower(r.Header.Get("Content-Encoding")); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(in)
		if err != nil {
			return nil, http.StatusBadRequest, errors.New("invalid gzip body")
		}
		defer zr.Close()
		in = zr
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
	b, err := io.ReadAll(io.LimitReader(in, MaxIngestBytes+1))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig) || len(b) > MaxIngestBytes:
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("body larger than %d bytes", MaxIngestBytes)
	case err != nil:
		return nil, http.StatusBadRequest, errors.New("could not read the body")
	}
	return b, 0, nil
}

// splitEvents returns the raw events of a JSON array or NDJSON body.
func splitEvents(body []byte, contentType string) ([]json.RawMessage, error) {
	if mt, _, _ := mime.ParseMediaType(contentType); mt == "application/x-ndjson" {
		var out []json.RawMessage
		for line := range bytes.Lines(body) {
			if line = bytes.TrimSpace(line); len(line) > 0 {
				out = append(out, json.RawMessage(line))
			}
		}
		return out, nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errors.New("body must be a JSON array of events, or NDJSON with Content-Type: application/x-ndjson")
	}
	return out, nil
}

// ingestEvent decodes and completes one posted event. Shiplino assigns
// the event id itself; the sender's dedup_key (or else its id) makes
// retries idempotent. Dedup keys are namespaced by session, so a sender
// can't collide with events from hooks or transcripts.
func ingestEvent(raw json.RawMessage, now time.Time) (model.Event, error) {
	var e model.Event
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, fmt.Errorf("invalid event JSON: %w", err)
	}
	if e.V == 0 {
		e.V = model.SchemaVersion
	}
	if e.TS.IsZero() {
		e.TS = now
	}
	key := e.DedupKey
	if key == "" {
		key = e.ID
	}
	if key == "" {
		key = model.NewULID(now) // no id from the sender: retries can't dedup
	}
	e.ID = model.NewULID(e.TS)
	e.ReceivedAt = now.UTC()
	e.TS = e.TS.UTC()
	e.Collector = model.CollectorHTTP
	e.Raw = nil

	name := e.Agent.Name
	switch {
	case name == "":
		return e, errors.New("missing agent.name")
	case len(name) > 64 || strings.ContainsAny(name, ":/ \t\r\n"):
		return e, errors.New("agent.name must be at most 64 characters, without ':', '/' or spaces")
	case !strings.HasPrefix(e.SessionID, name+":") || len(e.SessionID) == len(name)+1:
		return e, fmt.Errorf("session_id must be namespaced by the agent: %q", name+":<id>")
	case !under(e.ActorID, e.SessionID) || !under(e.ParentActor, e.SessionID):
		return e, errors.New("actor_id and parent_actor must be the session_id or start with session_id + \"/\"")
	}
	e.DedupKey = "http:" + e.SessionID + ":" + key
	if err := e.Validate(); err != nil {
		return e, err
	}
	if e.Kind == model.KindUsage {
		priceUsage(e.Data, e.TS)
	}
	return e, nil
}

// priceUsage prices a usage event that carries tokens but no cost with
// the bundled price table, as the adapters do for transcripts. A cost the
// sender reports is kept as is: the agent's own figure wins.
func priceUsage(d map[string]any, at time.Time) {
	if d == nil || d["cost_usd"] != nil {
		return
	}
	if report, _ := d["report"].(bool); report {
		return
	}
	m, _ := d["model"].(string)
	if m == "" {
		return
	}
	tokens := func(k string) int64 {
		f, _ := d[k].(float64)
		return int64(max(f, 0))
	}
	u := pricing.Usage{
		Input: tokens("input_tokens"), Output: tokens("output_tokens"),
		CacheRead: tokens("cache_read_tokens"), CacheWrite5m: tokens("cache_write_tokens"), At: at,
	}
	if cost, ok := pricing.Default().Cost(m, u); ok {
		d["cost_usd"], d["cost_source"] = cost, "computed"
	} else {
		d["cost_source"] = "unpriced"
	}
}

// under reports whether actor is empty, the session, or one of its subagents.
func under(actor, session string) bool {
	return actor == "" || actor == session || strings.HasPrefix(actor, session+"/")
}
