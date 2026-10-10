// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"bufio"
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
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"

	_ "github.com/elephaant/shiplino/pkg/adapters/claudecode" // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/codex"      // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/copilotcli" // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/cursor"     // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/geminicli"  // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/windsurf"   // registers the adapter
)

const (
	// maxBatch caps the events committed in one transaction.
	maxBatch = 500
	// defaultReapAfter is how long a fully processed spool file must be
	// idle before it is deleted (raw payloads shouldn't linger on disk).
	defaultReapAfter = 10 * time.Minute
	// rescanEvery is the safety-net rescan when no file events arrive.
	rescanEvery = 2 * time.Second
	// idleCheckEvery is how often quiet running sessions are marked idle.
	idleCheckEvery = time.Minute
	// pollFallback is the poll interval when file notifications fail.
	pollFallback = 500 * time.Millisecond
	// transcriptRecent bounds which stored sessions' transcripts are
	// re-tailed after a restart.
	transcriptRecent = 24 * time.Hour
	// discoverEvery is how often adapters' transcript folders are scanned.
	discoverEvery = 30 * time.Second
)

// maxRead caps the bytes read from one file per pass, and so the longest
// line that is parsed rather than skipped. A variable so tests can shrink it.
var maxRead int64 = 4 << 20

// Stats counts what the daemon could not turn into events.
type Stats struct {
	Lines   int64 `json:"lines"`   // lines read (spool and transcripts)
	Events  int64 `json:"events"`  // new events stored
	Unknown int64 `json:"unknown"` // unknown agents or native events
	Bad     int64 `json:"bad"`     // lines or payloads that failed to decode
}

// lineParser turns one line into events. blob, if set, is a spool blob to
// delete once the events are committed.
type lineParser func(line []byte, ref string) (events []model.Event, blob string)

// Daemon turns spool lines into stored events and session state.
type Daemon struct {
	home      string
	spoolRoot string
	st        *store.Store
	log       *log.Logger
	user      string
	reapAfter time.Duration

	mu          sync.Mutex // guards eng, offsets, stats, onChange during a pass
	eng         *engine.Engine
	offsets     map[string]int64
	stats       Stats
	transcripts map[string]string // transcript path → agent name
	// tstate is each transcript file's parser state (model, turn, …),
	// rebuilt by a warmup pass after a restart.
	tstate       map[string]map[string]string
	lastDiscover time.Time
	lastIdle     time.Time
	watchErr     string
	projects     *resolver
	redactor     *redact.Redactor
	level        redact.Level
	git          *gitwatch.Watcher

	// OnChange, if set, is called after each commit with the sessions that changed.
	OnChange func([]*engine.Session)
}

// New loads stored state and returns a daemon for the Shiplino home.
func New(ctx context.Context, home string, st *store.Store, logger *log.Logger) (*Daemon, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	d := &Daemon{home: home, spoolRoot: spool.Dir(home), st: st, log: logger, reapAfter: defaultReapAfter, projects: newResolver(),
		redactor: redact.Default, level: redact.Standard, git: gitwatch.NewWatcher()}
	if u, err := user.Current(); err == nil {
		d.user = u.Username
	}
	if err := d.rebuildIfStale(ctx); err != nil {
		return nil, fmt.Errorf("rebuild sessions: %w", err)
	}
	if err := d.reload(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// rebuildIfStale replays every stored event through the current engine
// when the sessions were built by an older one, so improvements (new
// fields, fixes) apply to past sessions too. Runs once per engine change.
func (d *Daemon) rebuildIfStale(ctx context.Context) error {
	rev, err := d.st.Meta(ctx, "engine_rev")
	if err != nil || rev == strconv.Itoa(engine.Rev) {
		return err
	}
	start := time.Now()
	eng := engine.New(nil, nil)
	n := 0
	if err := d.st.EachEvent(ctx, func(e model.Event) error {
		eng.Apply(e)
		n++
		return nil
	}); err != nil {
		return err
	}
	tx, err := d.st.Begin(ctx)
	if err != nil {
		return err
	}
	eng.MarkIdle(time.Now()) // idleness isn't an event: reapply it
	sessions := eng.Sessions()
	for _, s := range sessions {
		if err := tx.PutSession(ctx, s); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.SetMeta(ctx, "engine_rev", strconv.Itoa(engine.Rev)); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if n > 0 {
		d.log.Printf("rebuilt %d sessions from %d events for engine rev %d in %s", len(sessions), n, engine.Rev, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// SetPrivacy sets the capture level and redactor applied to every event
// before it is stored.
func (d *Daemon) SetPrivacy(level redact.Level, r *redact.Redactor) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.level, d.redactor = level, r
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
	totals, err := d.st.ProcessTotals(ctx)
	if err != nil {
		return err
	}
	d.eng = engine.New(sessions, totals)
	d.offsets = offsets
	d.transcripts = map[string]string{}
	d.tstate = map[string]map[string]string{}
	d.lastDiscover = time.Time{}
	recent := time.Now().Add(-transcriptRecent)
	for _, s := range sessions {
		if s.TranscriptPath != "" && s.LastEventAt.After(recent) {
			d.addTranscript(s.TranscriptPath, s.Agent)
		}
		// Keep linking commits for sessions active before a restart.
		if s.ParentID == "" && s.CWD != "" && s.LastEventAt.After(recent) && d.git != nil {
			d.git.Watch(s.CWD)
		}
	}
	return nil
}

// Health is what the daemon reports about itself.
type Health struct {
	Stats
	SpoolBacklogBytes int64 `json:"spool_backlog_bytes"` // unprocessed spool data
	Transcripts       int   `json:"transcripts"`         // transcript files being tailed
	Paused            bool  `json:"paused"`
	// WatchError is set when file notifications are unavailable and the
	// daemon polls instead (slower to react, a little more CPU).
	WatchError string `json:"watch_error,omitempty"`
}

// noCWD reports whether the session doesn't know its folder yet.
func (d *Daemon) noCWD(sessionID string) bool {
	s := d.eng.Get(sessionID)
	return s == nil || s.CWD == ""
}

// Health returns counters plus the current spool backlog.
func (d *Daemon) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	h := Health{Stats: d.stats, Transcripts: len(d.transcripts), Paused: spool.Paused(d.home, time.Now()), WatchError: d.watchErr}
	files, _ := filepath.Glob(filepath.Join(d.spoolRoot, "*", "*.jsonl"))
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(d.spoolRoot, f)
		if n := fi.Size() - d.offsets["spool/"+filepath.ToSlash(rel)]; n > 0 {
			h.SpoolBacklogBytes += n
		}
	}
	return h
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
	// Without file notifications (e.g. the system's inotify limit is used
	// up by other programs) the daemon still works, by polling often.
	every := rescanEvery
	var events <-chan fsnotify.Event
	var werrs <-chan error
	w, err := fsnotify.NewWatcher()
	if err != nil {
		d.log.Printf("file notifications unavailable (%v); polling every %s", err, pollFallback)
		every = pollFallback
		d.mu.Lock()
		d.watchErr = err.Error()
		d.mu.Unlock()
	} else {
		defer w.Close()
		d.watchDirs(w)
		events, werrs = w.Events, w.Errors
	}

	tick := time.NewTicker(every)
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
		case ev, ok := <-events:
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
		case err, ok := <-werrs:
			if !ok {
				return nil
			}
			d.log.Printf("watch: %v", err)
		case <-debounce:
			debounce = nil
			poll()
		case <-tick.C:
			if w != nil {
				d.watchDirs(w)
			}
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
		if err := d.processFile(ctx, path, src, d.parseLine); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src, err))
			continue
		}
		if err := d.maybeReap(ctx, path, src); err != nil {
			errs = append(errs, fmt.Errorf("reap %s: %w", src, err))
		}
	}
	if err := d.checkCommits(ctx); err != nil {
		errs = append(errs, fmt.Errorf("git: %w", err))
	}
	if time.Since(d.lastIdle) >= idleCheckEvery {
		if err := d.checkIdle(ctx, time.Now()); err != nil {
			errs = append(errs, fmt.Errorf("idle: %w", err))
		}
	}
	if time.Since(d.lastDiscover) >= discoverEvery {
		d.discover()
	}
	// Transcripts are read-only: tailed from the saved offset, never deleted.
	for path, agent := range d.transcripts {
		files := []string{path}
		subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
		files = append(files, subs...)
		for _, f := range files {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			src := "transcript:" + f
			state, ok := d.tstate[src]
			if !ok {
				state = map[string]string{}
				d.tstate[src] = state
				d.warmup(f, d.offsets[src], agent, state)
			}
			err := d.processFile(ctx, f, src, d.transcriptParser(agent, state))
			if errors.Is(err, os.ErrNotExist) && f == path {
				delete(d.transcripts, path)
				break
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("transcript %s: %w", f, err))
			}
		}
	}
	return errors.Join(errs...)
}

// addTranscript starts tailing an agent's transcript. The path comes from
// a hook payload, so only .jsonl files under the user's home directory are
// accepted.
func (d *Daemon) addTranscript(path, agent string) {
	if _, ok := d.transcripts[path]; ok || !filepath.IsAbs(path) || filepath.Ext(path) != ".jsonl" {
		return
	}
	a, ok := adapters.Get(agent)
	if !ok {
		return
	}
	if _, ok := a.(adapters.TranscriptParser); !ok {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	d.transcripts[path] = agent
}

// checkIdle marks running sessions that have gone quiet as idle (the
// agent was likely closed without telling us) and stores them. Called
// with d.mu held.
func (d *Daemon) checkIdle(ctx context.Context, now time.Time) error {
	d.lastIdle = now
	changed := d.eng.MarkIdle(now)
	if len(changed) == 0 {
		return nil
	}
	tx, err := d.st.Begin(ctx)
	if err != nil {
		return err
	}
	for _, s := range changed {
		if err := tx.PutSession(ctx, s); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if d.OnChange != nil {
		d.OnChange(changed)
	}
	return nil
}

// discover registers transcripts that adapters can find on disk, for
// sessions that run without hooks (e.g. desktop apps).
func (d *Daemon) discover() {
	d.lastDiscover = time.Now()
	d.discoverSince(time.Now().Add(-transcriptRecent))
}

// discoverSince registers transcripts modified since `since` and returns
// how many were new.
func (d *Daemon) discoverSince(recent time.Time) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	before := len(d.transcripts)
	for _, name := range adapters.Names() {
		a, _ := adapters.Get(name)
		td, ok := a.(adapters.TranscriptDiscoverer)
		if !ok {
			continue
		}
		for _, g := range td.TranscriptRoots(home, time.Now(), recent) {
			matches, _ := filepath.Glob(g)
			for _, m := range matches {
				if fi, err := os.Stat(m); err == nil && fi.ModTime().After(recent) {
					d.addTranscript(m, name)
				}
			}
		}
	}
	return len(d.transcripts) - before
}

// Backfill imports agents' transcripts modified since `since`: history
// from before Shiplino was set up. They are read on the next passes.
func (d *Daemon) Backfill(since time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.discoverSince(since)
}

// warmup replays a transcript up to off with Warmup set, so a parser that
// keeps state across lines (current model, turn) resumes correctly. It
// produces no events.
func (d *Daemon) warmup(path string, off int64, agent string, state map[string]string) {
	if off <= 0 {
		return
	}
	a, _ := adapters.Get(agent)
	tp, ok := a.(adapters.TranscriptParser)
	if !ok {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, off))
	sc.Buffer(make([]byte, 64<<10), int(maxRead))
	meta := adapters.TranscriptMeta{ReceivedAt: time.Now(), User: d.user, State: state, Warmup: true}
	for sc.Scan() {
		_, _ = tp.ParseTranscriptLine(sc.Bytes(), meta)
	}
}

func (d *Daemon) transcriptParser(agent string, state map[string]string) lineParser {
	return func(line []byte, ref string) ([]model.Event, string) {
		d.stats.Lines++
		a, _ := adapters.Get(agent)
		tp, ok := a.(adapters.TranscriptParser)
		if !ok {
			return nil, ""
		}
		evs, err := tp.ParseTranscriptLine(line, adapters.TranscriptMeta{ReceivedAt: time.Now(), User: d.user, Ref: ref, State: state})
		if err != nil {
			d.stats.Bad++
		}
		return evs, ""
	}
}

// processFile consumes complete lines from the stored offset to EOF.
func (d *Daemon) processFile(ctx context.Context, path, src string, parse lineParser) error {
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
			if int64(len(chunk)) == maxRead {
				// One line longer than maxRead: skip it rather than stall.
				if err := d.skipLine(ctx, path, src, off); errors.Is(err, io.EOF) {
					return nil // the giant line isn't complete yet
				} else if err != nil {
					return err
				}
				continue
			}
			return nil // partial line: wait for the rest
		}
		if err := d.processLines(ctx, chunk[:end+1], off, src, parse); err != nil {
			return err
		}
		if int64(len(chunk)) < maxRead {
			return nil
		}
	}
}

// skipLine advances the cursor past the line starting at off, if it is
// complete; an incomplete giant line is left until more data arrives.
func (d *Daemon) skipLine(ctx context.Context, path, src string, off int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	pos := off
	for {
		n, err := f.ReadAt(buf, pos)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			d.stats.Lines++
			d.stats.Bad++
			d.log.Printf("%s#%d: skipped line longer than %d bytes", src, off, maxRead)
			return d.commit(ctx, nil, store.Cursor{Source: src, Offset: pos + int64(i) + 1})
		}
		pos += int64(n)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF // wait for the rest of the line
			}
			return err
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
func (d *Daemon) processLines(ctx context.Context, data []byte, base int64, src string, parse lineParser) error {
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
		evs, blob := parse(line, ref)
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
		d.redactor.Event(&e, d.level) // before anything touches disk
		if d.eng.Redundant(e) {
			continue
		}
		d.projects.annotate(&e)
		// Watch on a session or turn start, or on the first event with a
		// folder for agents whose starts carry none (Windsurf).
		if e.Project != nil && e.Project.CWD != "" && e.Project.RepoRoot != "" && (e.Kind == model.KindSessionStart || e.Kind == model.KindTurnStart || d.noCWD(e.SessionID)) {
			d.git.Watch(e.Project.CWD)
		}
		isNew, err := tx.InsertEvent(ctx, e)
		if err != nil {
			return err
		}
		if !isNew {
			continue
		}
		stored++
		if p, ok := e.Data["transcript_path"].(string); ok && p != "" {
			d.addTranscript(p, e.Agent.Name)
		}
		for _, s := range d.eng.Apply(e) {
			changed[s.ID] = s
		}
	}
	list := make([]*engine.Session, 0, len(changed))
	for _, s := range changed {
		if err := tx.PutSession(ctx, s); err != nil {
			return err
		}
		if p, ok := d.projects.byID[s.ProjectID]; ok && s.ParentID == "" {
			if err := tx.PutProject(ctx, p, s.LastEventAt); err != nil {
				return err
			}
		}
		list = append(list, s)
	}
	if cur.Source != "" {
		if err := tx.PutCursor(ctx, cur); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cur.Source != "" {
		d.offsets[cur.Source] = cur.Offset
	}
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
	if err := d.processFile(ctx, done, src, d.parseLine); err != nil {
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
