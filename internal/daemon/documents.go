package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Document transcripts (adapters.DocumentParser) are JSON files the agent
// rewrites whole, such as Cline's task files. They can't be tailed from an
// offset, so a changed document is read again in full. Its events carry
// dedup keys that stay the same across rewrites, so reading it again
// never counts anything twice, and no cursor is kept: after a restart
// each document is read once more.

// Limits on document reads; variables so tests can change them.
var (
	// docEvery is the least time between two reads of one document. An
	// agent may rewrite it on every message, and a long task's file is
	// megabytes.
	docEvery = 5 * time.Second
	// docRecheck is how often a document whose parser asked for a recheck
	// is read again while it doesn't change.
	docRecheck = 30 * time.Second
	// maxDoc caps the size of a document that is read.
	maxDoc int64 = 64 << 20
)

// docState is what the daemon remembers about one document.
type docState struct {
	mod     time.Time
	size    int64
	readAt  time.Time
	recheck bool              // the parser has values that may still become final
	state   map[string]string // the parser's memory (adapters.TranscriptMeta.State)
	tooBig  bool              // logged once
}

// isDocument reports whether path is a document transcript of agent.
func isDocument(path, agent string) bool {
	a, ok := adapters.Get(agent)
	if !ok || filepath.Ext(path) != ".json" {
		return false
	}
	_, ok = a.(adapters.DocumentParser)
	return ok
}

// readDocuments reads the documents that changed or are due a recheck,
// in parallel, and commits their events. Called with d.mu held.
func (d *Daemon) readDocuments(ctx context.Context, docs map[string]string) error {
	type job struct {
		path, agent string
		st          *docState
		mod         time.Time
		size        int64
		events      []model.Event
		recheck     bool
		err         error
	}
	now := time.Now()
	var jobs []*job
	for path, agent := range docs {
		fi, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				delete(d.transcripts, path)
				delete(d.docs, path)
			}
			continue
		}
		st := d.docs[path]
		if st == nil {
			st = &docState{state: map[string]string{}}
			d.docs[path] = st
		}
		changed := !fi.ModTime().Equal(st.mod) || fi.Size() != st.size
		if now.Sub(st.readAt) < docEvery || (!changed && !(st.recheck && now.Sub(st.readAt) >= docRecheck)) {
			continue
		}
		jobs = append(jobs, &job{path: path, agent: agent, st: st, mod: fi.ModTime(), size: fi.Size()})
	}
	if len(jobs) == 0 {
		return nil
	}

	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	done := make(chan struct{})
	for _, j := range jobs {
		go func() {
			sem <- struct{}{}
			j.events, j.recheck, j.err = d.parseDocument(j.path, j.agent, j.st, j.mod, j.size)
			<-sem
			done <- struct{}{}
		}()
	}
	for range jobs {
		<-done
	}

	var pending []model.Event
	for _, j := range jobs {
		if j.err != nil {
			d.stats.bad.Add(1)
			d.log.Printf("transcript %s: %v", j.path, j.err)
		}
		pending = append(pending, j.events...)
	}
	for len(pending) > 0 {
		n := min(len(pending), maxBatch)
		if err := d.commit(ctx, pending[:n], nil); err != nil {
			return err // read again next pass: d.docs was reset by reload
		}
		pending = pending[n:]
	}
	for _, j := range jobs {
		j.st.mod, j.st.size, j.st.readAt, j.st.recheck = j.mod, j.size, now, j.recheck
	}
	return nil
}

// parseDocument reads one document and returns its (redacted) events. It
// only touches the document's own state, so documents parse concurrently.
func (d *Daemon) parseDocument(path, agent string, st *docState, mod time.Time, size int64) ([]model.Event, bool, error) {
	if size > maxDoc {
		if !st.tooBig {
			st.tooBig = true
			return nil, false, fmt.Errorf("%d bytes, over the %d byte limit for whole-file transcripts; not read", size, maxDoc)
		}
		return nil, false, nil
	}
	st.tooBig = false
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	a, _ := adapters.Get(agent)
	dp, ok := a.(adapters.DocumentParser)
	if !ok {
		return nil, false, nil
	}
	evs, recheck, err := dp.ParseTranscriptDocument(b, adapters.TranscriptMeta{
		ReceivedAt: time.Now(), User: d.user, Ref: "transcript:" + path, Path: path, ModTime: mod, State: st.state,
	})
	for i := range evs {
		d.redactor.Event(&evs[i], d.level) // before anything touches disk
	}
	return evs, recheck, err
}
