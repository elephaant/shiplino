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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	_ "github.com/elephaant/shiplino/pkg/adapters/cline"      // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/codex"      // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/copilotcli" // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/cursor"     // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/geminicli"  // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/opencode"   // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/windsurf"   // registers the adapter
	_ "github.com/elephaant/shiplino/pkg/adapters/wrap"       // registers the adapter
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

// counters back Stats. They are atomic: lines are parsed concurrently.
type counters struct{ lines, events, unknown, bad atomic.Int64 }

func (c *counters) snapshot() Stats {
	return Stats{Lines: c.lines.Load(), Events: c.events.Load(), Unknown: c.unknown.Load(), Bad: c.bad.Load()}
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

	mu          sync.Mutex // guards eng, offsets, onChange during a pass
	eng         *engine.Engine
	offsets     map[string]int64
	stats       counters
	transcripts map[string]string // transcript path → agent name
	// tstate is each transcript file's parser state (model, turn, …),
	// rebuilt by a warmup pass after a restart.
	tstate map[string]map[string]string
	// docs are the whole-file transcripts being read (see documents.go).
	docs         map[string]*docState
	lastDiscover time.Time
	lastIdle     time.Time
	watchErr     string
	projects     *resolver
	redactor     *redact.Redactor
	level        redact.Level
	git          *gitwatch.Watcher

	// OnChange, if set, is called after each commit with the sessions that changed.
	OnChange func([]*engine.Session)
	// OnCommit, if set, is called after each commit with the number of
	// events it carried and how long the transaction took (benchmarks).
	OnCommit func(events int, took time.Duration)
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
	d.docs = map[string]*docState{}
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
	// Imported counts, per agent, transcripts skipped because they copy
	// another agent's session (whose own record is used instead).
	Imported map[string]int `json:"imported,omitempty"`
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
	h := Health{Stats: d.stats.snapshot(), Transcripts: len(d.transcripts), Paused: spool.Paused(d.home, time.Now()), WatchError: d.watchErr}
	for path, agent := range d.transcripts {
		if d.tstate["transcript:"+path][adapters.StateImported] == "1" {
			if h.Imported == nil {
				h.Imported = map[string]int{}
			}
			h.Imported[agent]++
		}
	}
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
func (d *Daemon) Stats() Stats { return d.stats.snapshot() }

// Run processes the spool until ctx is cancelled, reacting to file
// changes and rescanning every few seconds as a safety net.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.spoolRoot, 0o700); err != nil {
		return err
	}
	// Without file notifications (e.g. the system's inotify limit is used
	// up by other programs) the daemon still works, by polling often.
	every := rescanEvery
	changed := make(chan struct{}, 1) // a file changed since the last pass
	newDirs := make(chan string, 64)  // created paths that may be folders to watch
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
		go d.drain(w, changed, newDirs)
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
		case path := <-newDirs:
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				_ = w.Add(path)
			}
		case <-changed:
			if debounce == nil {
				debounce = time.After(20 * time.Millisecond)
			}
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

// drain reads the watcher's events and errors until it is closed, and
// only signals Run. Run must not read them itself: on Windows,
// Watcher.Add waits for the watcher's goroutine, which waits for its
// events to be read, so Run calling Add would hang the daemon (#99).
func (d *Daemon) drain(w *fsnotify.Watcher, changed chan<- struct{}, newDirs chan<- string) {
	events, errs := w.Events, w.Errors
	for events != nil || errs != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Has(fsnotify.Create) {
				select {
				case newDirs <- ev.Name:
				default: // full: the next rescan adds new folders anyway
				}
			}
			select {
			case changed <- struct{}{}:
			default: // a pass is already due
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			d.log.Printf("watch: %v", err)
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

// Poll processes every new complete line in the spool and the tailed
// transcripts once.
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
	spoolSrcs := make([]source, len(files))
	for i, path := range files {
		rel, _ := filepath.Rel(d.spoolRoot, path)
		spoolSrcs[i] = source{path: path, src: "spool/" + filepath.ToSlash(rel), parse: d.parseLine}
	}
	failed, err := d.ingest(ctx, spoolSrcs)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, s := range spoolSrcs {
		if err := failed[s.src]; err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.src, err))
			continue
		}
		if err := d.maybeReap(ctx, s.path, s.src); err != nil {
			errs = append(errs, fmt.Errorf("reap %s: %w", s.src, err))
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
	var tsrcs []source
	mainFile := map[string]string{} // source → its transcript, for main files
	docs := map[string]string{}
	for path, agent := range d.transcripts {
		if isDocument(path, agent) {
			docs[path] = agent
			continue
		}
		files := []string{path}
		subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
		files = append(files, subs...)
		for _, f := range files {
			src := "transcript:" + f
			var mod time.Time
			if fi, err := os.Stat(f); err == nil {
				mod = fi.ModTime()
			}
			state, ok := d.tstate[src]
			if !ok {
				state = map[string]string{}
				d.tstate[src] = state
				d.warmup(f, d.offsets[src], agent, state, mod)
			}
			if f == path {
				mainFile[src] = path
			}
			tsrcs = append(tsrcs, source{path: f, src: src, parse: d.transcriptParser(agent, f, mod, state)})
		}
	}
	failed, err = d.ingest(ctx, tsrcs)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, s := range tsrcs {
		err := failed[s.src]
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			if path, ok := mainFile[s.src]; ok {
				delete(d.transcripts, path)
			}
		default:
			errs = append(errs, fmt.Errorf("transcript %s: %w", s.path, err))
		}
	}
	if err := d.readDocuments(ctx, docs); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// addTranscript starts tailing an agent's transcript. The path may come
// from a hook payload, so only files under the user's home directory are
// accepted: .jsonl files for line parsers, .json for document parsers.
func (d *Daemon) addTranscript(path, agent string) {
	if _, ok := d.transcripts[path]; ok || !filepath.IsAbs(path) {
		return
	}
	a, ok := adapters.Get(agent)
	if !ok {
		return
	}
	_, lines := a.(adapters.TranscriptParser)
	if !(lines && filepath.Ext(path) == ".jsonl") && !isDocument(path, agent) {
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
func (d *Daemon) warmup(path string, off int64, agent string, state map[string]string, mod time.Time) {
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
	meta := adapters.TranscriptMeta{ReceivedAt: time.Now(), User: d.user, Path: path, ModTime: mod, State: state, Warmup: true}
	for sc.Scan() {
		_, _ = tp.ParseTranscriptLine(sc.Bytes(), meta)
	}
}

func (d *Daemon) transcriptParser(agent, path string, mod time.Time, state map[string]string) lineParser {
	return func(line []byte, ref string) ([]model.Event, string) {
		d.stats.lines.Add(1)
		a, _ := adapters.Get(agent)
		tp, ok := a.(adapters.TranscriptParser)
		if !ok {
			return nil, ""
		}
		evs, err := tp.ParseTranscriptLine(line, adapters.TranscriptMeta{ReceivedAt: time.Now(), User: d.user, Ref: ref, Path: path, ModTime: mod, State: state})
		if err != nil {
			d.stats.bad.Add(1)
		}
		return evs, ""
	}
}

// source is one file read line by line from a saved offset.
type source struct {
	path, src string
	parse     lineParser
}

// parsedLine is one complete line turned into (redacted) events, with the
// file offset just past it.
type parsedLine struct {
	events []model.Event
	blob   string
	end    int64
}

// chunk is what one read of a source produced.
type chunk struct {
	lines []parsedLine
	more  bool // the read was capped: more complete lines may follow
	err   error
}

// ingest reads every source to its end. Sources are read, parsed and
// redacted in parallel (each source by one goroutine at a time, so per
// session order holds), then committed in source order by the calling
// goroutine, the single writer: up to maxBatch events per transaction,
// together with the cursors they cover. It returns the sources that
// failed to read, and an error if a commit failed (the pass stops; the
// cursors weren't advanced, so the next pass retries).
func (d *Daemon) ingest(ctx context.Context, sources []source) (map[string]error, error) {
	failed := map[string]error{}
	for len(sources) > 0 {
		offs := make([]int64, len(sources))
		for i, s := range sources {
			offs[i] = d.offsets[s.src]
		}
		// At most GOMAXPROCS chunks are in flight, bounding memory.
		out := make([]chan chunk, len(sources))
		for i := range out {
			out[i] = make(chan chunk, 1)
		}
		sem := make(chan struct{}, runtime.GOMAXPROCS(0))
		go func() {
			for i, s := range sources {
				sem <- struct{}{}
				go func() { out[i] <- d.readChunk(s, offs[i]) }()
			}
		}()

		var (
			pending []model.Event
			blobs   []string
			cursors = map[string]int64{}
			next    []source
			err     error
		)
		flush := func() error {
			if len(cursors) == 0 {
				return nil
			}
			list := make([]store.Cursor, 0, len(cursors))
			for src, off := range cursors {
				list = append(list, store.Cursor{Source: src, Offset: off})
			}
			if err := d.commit(ctx, pending, list); err != nil {
				return err
			}
			for _, b := range blobs {
				_ = os.Remove(filepath.Join(d.spoolRoot, filepath.FromSlash(b)))
			}
			pending, blobs = pending[:0], blobs[:0]
			clear(cursors)
			return nil
		}
		for i, s := range sources {
			c := <-out[i]
			<-sem
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				continue // keep draining the readers
			}
			if c.err != nil {
				failed[s.src] = c.err
				continue
			}
			for _, l := range c.lines {
				pending = append(pending, l.events...)
				if l.blob != "" {
					blobs = append(blobs, l.blob)
				}
				cursors[s.src] = l.end
				if len(pending) >= maxBatch {
					if err = flush(); err != nil {
						break
					}
				}
			}
			if c.more {
				next = append(next, s)
			}
		}
		if err == nil {
			err = flush()
		}
		if err != nil {
			return failed, err
		}
		sources = next
	}
	return failed, nil
}

// readChunk reads the complete lines after off (at most maxRead bytes),
// then parses and redacts them. It only touches atomic counters and the
// source's own parser state, so different sources can be read
// concurrently.
func (d *Daemon) readChunk(s source, off int64) chunk {
	fi, err := os.Stat(s.path)
	if err != nil {
		return chunk{err: err}
	}
	if fi.Size() < off {
		off = 0 // truncated or replaced: start over; dedup keeps it idempotent
	}
	if fi.Size() == off {
		return chunk{}
	}
	data, err := readAt(s.path, off, min(fi.Size()-off, maxRead))
	if err != nil {
		return chunk{err: err}
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		if int64(len(data)) < maxRead {
			return chunk{} // partial line: wait for the rest
		}
		// One line longer than maxRead: skip it rather than stall.
		next, err := lineEnd(s.path, off)
		if errors.Is(err, io.EOF) {
			return chunk{} // the giant line isn't complete yet
		}
		if err != nil {
			return chunk{err: err}
		}
		d.stats.lines.Add(1)
		d.stats.bad.Add(1)
		d.log.Printf("%s#%d: skipped line longer than %d bytes", s.src, off, maxRead)
		return chunk{lines: []parsedLine{{end: next}}, more: true}
	}
	c := chunk{more: int64(len(data)) == maxRead}
	data = data[:end+1]
	pos := off
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		evs, blob := s.parse(data[:i], s.src+"#"+strconv.FormatInt(pos, 10))
		for j := range evs {
			d.redactor.Event(&evs[j], d.level) // before anything touches disk
		}
		pos += int64(i + 1)
		data = data[i+1:]
		c.lines = append(c.lines, parsedLine{events: evs, blob: blob, end: pos})
	}
	return c
}

// lineEnd returns the offset just past the line starting at off, or
// io.EOF if that line isn't complete yet.
func lineEnd(path string, off int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	pos := off
	for {
		n, err := f.ReadAt(buf, pos)
		if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
		pos += int64(n)
		if err != nil {
			return 0, err
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

// parseLine turns one spool line into events. Failures are counted, never
// fatal: one bad line must not stop the pipeline.
func (d *Daemon) parseLine(line []byte, ref string) (events []model.Event, blob string) {
	d.stats.lines.Add(1)
	var env spool.Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		d.stats.bad.Add(1)
		return nil, ""
	}
	payload := []byte(env.P)
	if env.B != "" {
		blob = env.B
		b, err := os.ReadFile(filepath.Join(d.spoolRoot, filepath.FromSlash(env.B)))
		if err != nil {
			d.stats.bad.Add(1)
			return nil, ""
		}
		payload = b
	}
	if len(payload) == 0 {
		d.stats.bad.Add(1) // non-JSON hook input (env.S) or empty stdin
		return nil, blob
	}
	a, ok := adapters.Get(env.Agent)
	if !ok {
		d.stats.unknown.Add(1)
		return nil, blob
	}
	evs, err := a.ParseHook(payload, adapters.HookMeta{
		EnvelopeID: env.ID, Event: env.Event, ReceivedAt: time.Unix(0, env.TS), User: d.user, Ref: ref,
	})
	switch {
	case errors.Is(err, adapters.ErrUnknownEvent):
		d.stats.unknown.Add(1)
	case err != nil:
		d.stats.bad.Add(1)
		d.log.Printf("%s: %v", ref, err)
	}
	return evs, blob
}

// commit stores events (already redacted), applies the new ones and
// saves the cursors, all in one transaction. On failure the in-memory
// state is rebuilt from disk.
func (d *Daemon) commit(ctx context.Context, events []model.Event, cursors []store.Cursor) error {
	_, err := d.commitCount(ctx, events, cursors)
	return err
}

// commitCount is commit, also returning how many events were new (not
// duplicates and not redundant with another source).
func (d *Daemon) commitCount(ctx context.Context, events []model.Event, cursors []store.Cursor) (stored int64, err error) {
	start := time.Now()
	tx, err := d.st.Begin(ctx)
	if err != nil {
		return 0, err
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
	for _, e := range events {
		if d.eng.Redundant(e) {
			continue
		}
		d.projects.annotate(&e)
		// Watch on a session or turn start, or on the first event with a
		// folder for agents whose starts carry none (Windsurf).
		if e.Project != nil && e.Project.CWD != "" && e.Project.RepoRoot != "" && d.git != nil && (e.Kind == model.KindSessionStart || e.Kind == model.KindTurnStart || d.noCWD(e.SessionID)) {
			d.git.Watch(e.Project.CWD)
		}
		d.eng.AnnotatePlan(&e) // plan merges are stored with their counts
		isNew, err := tx.InsertEvent(ctx, e)
		if err != nil {
			return 0, err
		}
		if !isNew {
			continue
		}
		stored++
		// Only the agent itself (its hooks) says which files to tail, not
		// events posted over the network API.
		if p, ok := e.Data["transcript_path"].(string); ok && p != "" && e.Collector != model.CollectorHTTP && e.Collector != model.CollectorOTLP {
			d.addTranscript(p, e.Agent.Name)
		}
		for _, s := range d.eng.Apply(e) {
			changed[s.ID] = s
		}
	}
	list := make([]*engine.Session, 0, len(changed))
	for _, s := range changed {
		if err := tx.PutSession(ctx, s); err != nil {
			return 0, err
		}
		if p, ok := d.projects.byID[s.ProjectID]; ok && s.ParentID == "" {
			if err := tx.PutProject(ctx, p, s.LastEventAt); err != nil {
				return 0, err
			}
		}
		list = append(list, s)
	}
	for _, c := range cursors {
		if err := tx.PutCursor(ctx, c); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, c := range cursors {
		d.offsets[c.Source] = c.Offset
	}
	d.stats.events.Add(stored)
	if d.OnCommit != nil {
		d.OnCommit(len(events), time.Since(start))
	}
	if d.OnChange != nil && len(list) > 0 {
		d.OnChange(list)
	}
	return stored, nil
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
	failed, err := d.ingest(ctx, []source{{path: done, src: src, parse: d.parseLine}})
	if err == nil {
		err = failed[src]
	}
	if err != nil {
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
