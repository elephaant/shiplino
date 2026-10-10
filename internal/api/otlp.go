package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/elephaant/shiplino/pkg/otlp"
)

// The OTLP/HTTP receiver: POST /v1/logs, /v1/metrics and /v1/traces on
// the API listener, so an agent's OTEL_EXPORTER_OTLP_ENDPOINT can point
// at the daemon (with the token as an Authorization header). Logs from
// known agents become events. Metrics and traces are accepted, so an
// exporter configured for them doesn't log errors, and only counted.
// See docs/ingest.md.

func (s *Server) otlpLogs(w http.ResponseWriter, r *http.Request) {
	body, ok := s.otlpBody(w, r)
	if !ok {
		return
	}
	ld, err := otlp.DecodeLogs(body, r.Header.Get("Content-Type"))
	if err != nil {
		s.otlpDecodeError(w, err)
		return
	}
	events, st := otlp.Map(ld, otlp.Meta{ReceivedAt: time.Now()})
	if len(events) > 0 {
		if _, _, err := s.Ingest.Ingest(r.Context(), events); err != nil && !errors.Is(err, ErrPaused) {
			s.internal(w, err)
			return
		}
	}
	s.countIngest(func(c *IngestStats) { c.OTLPLogs.Add(st) })
	otlpOK(w, r)
}

// otlpCount accepts a metrics or traces export without decoding it.
func (s *Server) otlpCount(count func(*IngestStats)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.otlpBody(w, r); !ok {
			return
		}
		if _, err := otlp.WantsJSON(r.Header.Get("Content-Type")); err != nil {
			s.otlpDecodeError(w, err)
			return
		}
		s.countIngest(count)
		otlpOK(w, r)
	}
}

func (s *Server) otlpBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if s.Ingest == nil {
		writeError(w, http.StatusNotImplemented, "ingest unavailable")
		return nil, false
	}
	body, code, err := readBody(w, r)
	if err != nil {
		s.rejectIngest(w, code, "otlp: "+err.Error())
		return nil, false
	}
	return body, true
}

func (s *Server) otlpDecodeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, otlp.ErrContentType):
		s.rejectIngest(w, http.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, otlp.ErrTooMany):
		s.rejectIngest(w, http.StatusRequestEntityTooLarge, err.Error())
	default:
		s.rejectIngest(w, http.StatusBadRequest, "otlp: invalid export body")
	}
}

// otlpOK answers with an empty Export*ServiceResponse (full success) in
// the request's encoding.
func otlpOK(w http.ResponseWriter, r *http.Request) {
	if isJSON, _ := otlp.WantsJSON(r.Header.Get("Content-Type")); isJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}
