// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"

	_ "github.com/elephaant/shiplino/pkg/adapters/claudecode" // registers the adapter
)

const (
	// maxBatch caps the events committed in one transaction.
	maxBatch = 500
	// maxRead caps the bytes read from one spool file per pass.
	maxRead = 4 << 20
	// defaultReapAfter is how long a fully processed spool file must be
	// idle before it is deleted (raw payloads shouldn't linger on disk).
	defaultReapAfter = 10 * time.Minute
	// rescanEvery is the safety-net rescan when no file events arrive.
	rescanEvery = 2 * time.Second
)

// Stats counts what the daemon could not turn into events.
type Stats struct {
	Lines   int64 // spool lines read
	Events  int64 // new events stored
	Unknown int64 // unknown agents or native events (kept on disk as raw until reaped)
	Bad     int64 // lines or payloads that failed to decode
}

// Daemon turns spool lines into stored events and session state.
type Daemon struct {
	home      string
	spoolRoot string
	st        *store.Store
	log       *log.Logger
	user      string
	reapAfter time.Duration

	mu      sync.Mutex // guards eng, offsets, stats, onChange during a pass
	eng     *engine.Engine
	offsets map[string]int64
	stats   Stats

	// OnChange, if set, is called after each commit with the sessions that changed.
	OnChange func([]*engine.Session)
}

// New loads stored state and returns a daemon for the Shiplino home.
func New(ctx context.Context, home string, st *store.Store, logger *log.Logger) (*Daemon, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	d := &Daemon{home: home, spoolRoot: spool.Dir(home), st: st, log: logger, reapAfter: defaultReapAfter}
	if u, err := user.Current(); err == nil {
		d.user = u.Username
	}
	if err := d.reload(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// reload resets in-memory state from the database.
func (d *Daemon) reload(ctx context.Context) error {
	sessions, err := d.st.Sessions(ctx, 0)
	if err != nil {
		return err
	}
	offsets, err := d.st.Cursors(ctx)
	if err != nil {
		return err
	}
	d.eng = engine.New(sessions)
	d.offsets = offsets
	return nil
}

// Stats returns a snapshot of the counters.
func (d *Daemon) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stats
}

// Run processes the spool until ctx is cancelled, reacting to file
// changes and rescanning every few seconds as a safety net.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.spoolRoot, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	d.watchDirs(w)

	tick := time.NewTicker(rescanEvery)
	defer tick.Stop()
	var debounce <-chan time.Time
	poll := func() {
		if err := d.Poll(ctx); err != nil && ctx.Err() == nil {
			d.log.Printf("poll: %v", err)
		}
	}
	poll()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					_ = w.Add(ev.Name)
				}
			}
			if debounce == nil {
				debounce = time.After(20 * time.Millisecond)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			d.log.Printf("watch: %v", err)
		case <-debounce:
			debounce = nil
			poll()
		case <-tick.C:
			d.watchDirs(w)
			poll()
		}
	}
}

func (d *Daemon) watchDirs(w *fsnotify.Watcher) {
	_ = w.Add(d.spoolRoot)
	entries, _ := os.ReadDir(d.spoolRoot)
	for _, e := range entries {
		if e.IsDir() && e.Name() != "blobs" {
			_ = w.Add(filepath.Join(d.spoolRoot, e.Name()))
		}
	}
}

// Poll processes every new complete line in the spool once.
func (d *Daemon) Poll(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	files, err := filepath.Glob(filepath.Join(d.spoolRoot, "*", "*.jsonl"))
	if err != nil {
		return err
	}
	var errs []error
	// Finish reaps interrupted by a crash between rename and delete.
	leftovers, _ := filepath.Glob(filepath.Join(d.spoolRoot, "*", "*.jsonl.done"))
	for _, done := range leftovers {
		rel, _ := filepath.Rel(d.spoolRoot, done[:len(done)-len(".done")])
		if err := d.finishReap(ctx, done, "spool/"+filepath.ToSlash(rel)); err != nil {
			errs = append(errs, err)
		}
	}
	for _, path := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(d.spoolRoot, path)
		src := "spool/" + filepath.ToSlash(rel)
		if err := d.processFile(ctx, path, src); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src, err))
			continue
		}
		if err := d.maybeReap(ctx, path, src); err != nil {
			errs = append(errs, fmt.Errorf("reap %s: %w", src, err))
		}
	}
	return errors.Join(errs...)
}

// processFile consumes complete lines from the stored offset to EOF.
func (d *Daemon) processFile(ctx context.Context, path, src string) error {
	for {
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		off := d.offsets[src]
		if fi.Size() < off {
			off = 0 // truncated or replaced: start over; dedup keeps it idempotent
		}
		if fi.Size() == off {
			return nil
		}
		chunk, err := readAt(path, off, min(fi.Size()-off, maxRead))
		if err != nil {
			return err
		}
		end := bytes.LastIndexByte(chunk, '\n')
		if end < 0 {
			if len(chunk) == maxRead {
				return fmt.Errorf("line at offset %d exceeds %d bytes", off, maxRead)
			}
			return nil // partial line: wait for the rest
		}
		if err := d.processLines(ctx, chunk[:end+1], off, src); err != nil {
			return err
		}
		if int64(len(chunk)) < maxRead {
			return nil
		}
	}
}

func readAt(path string, off, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:got], nil
}

// processLines handles complete lines starting at file offset base,
// committing in batches together with the advanced cursor.
func (d *Daemon) processLines(ctx context.Context, data []byte, base int64, src string) error {
	var (
		pending []model.Event
		blobs   []string
		pos     int64
	)
	flush := func(upTo int64) error {
		if err := d.commit(ctx, pending, store.Cursor{Source: src, Offset: base + upTo}); err != nil {
			return err
		}
		for _, b := range blobs {
			_ = os.Remove(filepath.Join(d.spoolRoot, filepath.FromSlash(b)))
		}
		pending, blobs = pending[:0], blobs[:0]
		return nil
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		line := data[:i]
		ref := src + "#" + strconv.FormatInt(base+pos, 10)
		evs, blob := d.parseLine(line, ref)
		pending = append(pending, evs...)
		if blob != "" {
			blobs = append(blobs, blob)
		}
		pos += int64(i + 1)
		data = data[i+1:]
		if len(pending) >= maxBatch {
			if err := flush(pos); err != nil {
				return err
			}
		}
	}
	return flush(pos)
}

// parseLine turns one spool line into events. Failures are counted, never
// fatal: one bad line must not stop the pipeline.
func (d *Daemon) parseLine(line []byte, ref string) (events []model.Event, blob string) {
	d.stats.Lines++
	var env spool.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		d.stats.Bad++
		return nil, ""
	}
	payload := []byte(env.P)
	if env.B != "" {
		blob = env.B
		b, err := os.ReadFile(filepath.Join(d.spoolRoot, filepath.FromSlash(env.B)))
		if err != nil {
			d.stats.Bad++
			return nil, ""
		}
		payload = b
	}
	if len(payload) == 0 {
		d.stats.Bad++ // non-JSON hook input (env.S) or empty stdin
		return nil, blob
	}
	a, ok := adapters.Get(env.Agent)
	if !ok {
		d.stats.Unknown++
		return nil, blob
	}
	evs, err := a.ParseHook(payload, adapters.HookMeta{
		EnvelopeID: env.ID, Event: env.Event, ReceivedAt: time.Unix(0, env.TS), User: d.user, Ref: ref,
	})
	switch {
	case errors.Is(err, adapters.ErrUnknownEvent):
		d.stats.Unknown++
	case err != nil:
		d.stats.Bad++
		d.log.Printf("%s: %v", ref, err)
	}
	return evs, blob
}

// commit stores events, applies the new ones and saves the cursor, all in
// one transaction. On failure the in-memory state is rebuilt from disk.
func (d *Daemon) commit(ctx context.Context, events []model.Event, cur store.Cursor) (err error) {
	tx, err := d.st.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			if rerr := d.reload(ctx); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
	}()

	changed := map[string]*engine.Session{}
	var stored int64
	for _, e := range events {
		isNew, err := tx.InsertEvent(ctx, e)
		if err != nil {
			return err
		}
		if !isNew {
			continue
		}
		stored++
		for _, s := range d.eng.Apply(e) {
			changed[s.ID] = s
		}
	}
	list := make([]*engine.Session, 0, len(changed))
	for _, s := range changed {
		if err := tx.PutSession(ctx, s); err != nil {
			return err
		}
		list = append(list, s)
	}
	if err := tx.PutCursor(ctx, cur); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.offsets[cur.Source] = cur.Offset
	d.stats.Events += stored
	if d.OnChange != nil && len(list) > 0 {
		d.OnChange(list)
	}
	return nil
}

// maybeReap deletes a fully processed spool file that has been idle for
// reapAfter. It renames first, so a hook appending at the same moment
// creates a fresh file instead of losing its line, then drains whatever
// landed before the rename.
func (d *Daemon) maybeReap(ctx context.Context, path, src string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != d.offsets[src] || time.Since(fi.ModTime()) < d.reapAfter {
		return nil
	}
	done := path + ".done"
	if err := os.Rename(path, done); err != nil {
		return err
	}
	return d.finishReap(ctx, done, src)
}

func (d *Daemon) finishReap(ctx context.Context, done, src string) error {
	if err := d.processFile(ctx, done, src); err != nil {
		return err
	}
	tx, err := d.st.Begin(ctx)
	if err != nil {
		return err
	}
	if err := tx.DeleteCursor(ctx, src); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(d.offsets, src)
	return os.Remove(done)
}
