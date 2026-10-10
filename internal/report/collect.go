package report

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
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

// maxLine is the longest transcript line that is parsed; longer ones are
// skipped and counted as bad, as the daemon does.
const maxLine = 4 << 20

// Options select what to read.
type Options struct {
	UserHome string    // where the agents keep their transcripts
	Since    time.Time // sessions started at or after this
	Now      time.Time
	Agent    string // only this agent ("" for all)
	Project  string // only this project, by name or id ("" for all)
	// Redactor is applied to every event at the minimal capture level
	// before anything else sees it: the report needs metadata only, so
	// prompts, commands and messages are dropped right away. nil means
	// the default rules.
	Redactor *redact.Redactor
	// Git answers project detection; nil uses the git binary (read-only).
	Git projects.Git
	// Progress, if set, is called after each transcript file is read.
	Progress func(done, total int)
}

// transcript is one file to read and the adapter that reads it.
type transcript struct {
	path  string
	agent string
	sub   bool // a subagent's file: read after the main sessions
	size  int64
}

// discover lists the transcripts that adapters can find under the user's
// home, modified since `since`, with Claude Code style subagent files
// next to their sessions. Main session files come first (subagents need
// their parent), each group largest first so the slowest files start early.
func discover(userHome string, now, since time.Time, agent string) []transcript {
	seen := map[string]bool{}
	var out []transcript
	add := func(path, name string) {
		if seen[path] || filepath.Ext(path) != ".jsonl" {
			return
		}
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(since) {
			return
		}
		seen[path] = true
		out = append(out, transcript{path: path, agent: name, sub: filepath.Base(filepath.Dir(path)) == "subagents", size: fi.Size()})
	}
	for _, name := range adapters.Names() {
		if agent != "" && name != agent {
			continue
		}
		a, _ := adapters.Get(name)
		td, ok := a.(adapters.TranscriptDiscoverer)
		if _, parses := a.(adapters.TranscriptParser); !ok || !parses {
			continue
		}
		for _, g := range td.TranscriptRoots(userHome, now, since) {
			matches, _ := filepath.Glob(g)
			for _, m := range matches {
				add(m, name)
				subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(m, ".jsonl"), "subagents", "*.jsonl"))
				for _, s := range subs {
					add(s, name)
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b transcript) int {
		if a.sub != b.sub {
			if a.sub {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(b.size, a.size), cmp.Compare(a.path, b.path))
	})
	return out
}

// parsed is what reading one transcript produced.
type parsed struct {
	events []model.Event
	lines  int64
	bad    int64
	err    error
}

// read parses one transcript with its adapter and strips every event to
// metadata. As in the daemon, an unfinished last line isn't parsed and
// over-long lines are skipped.
func read(t transcript, r *redact.Redactor) parsed {
	var p parsed
	a, _ := adapters.Get(t.agent)
	tp, _ := a.(adapters.TranscriptParser)
	f, err := os.Open(t.path)
	if err != nil {
		p.err = err
		return p
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		p.err = err
		return p
	}
	meta := adapters.TranscriptMeta{ReceivedAt: fi.ModTime(), Path: t.path, ModTime: fi.ModTime(), State: map[string]string{}}
	br := bufio.NewReaderSize(f, 64<<10)
	var off int64
	var long bool // inside a line longer than maxLine
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxLine {
			long, line = true, line[:0]
		} else if !long {
			line = append(line, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.err = err
			}
			return p // an unfinished last line isn't parsed
		}
		start := off
		off += int64(len(chunk)) // offsets only label events (Ref)
		p.lines++
		if long {
			p.bad++
			long, line = false, line[:0]
			continue
		}
		meta.Ref = "transcript:" + t.path + "#" + strconv.FormatInt(start, 10)
		evs, err := tp.ParseTranscriptLine(bytes.TrimRight(line, "\r\n"), meta)
		if err != nil {
			p.bad++
		}
		for i := range evs {
			r.Event(&evs[i], redact.Minimal) // before anything else sees it
		}
		p.events = append(p.events, evs...)
		line = line[:0]
	}
}

// fold is the state built from the events, like the daemon's store.
type fold struct {
	eng      *engine.Engine
	seen     map[string]bool // dedup keys, as the store's unique index
	projects map[string]projects.Project
	byCWD    map[string]projects.Project
	limits   map[string]model.Event // latest per agent, limit and window
	git      projects.Git
	userHome string
	since    time.Time
}

func (f *fold) apply(e model.Event) {
	if e.DedupKey == "" || f.eng.Redundant(e) || f.seen[e.DedupKey] {
		return
	}
	f.seen[e.DedupKey] = true
	if e.Project != nil && e.Project.CWD != "" {
		p, ok := f.byCWD[e.Project.CWD]
		if !ok {
			p = projects.Detect(e.Project.CWD, f.git, f.userHome)
			f.byCWD[e.Project.CWD] = p
			f.projects[p.ID] = p
		}
		e.Project.ID = p.ID
	}
	if e.Kind == model.KindLimit && !e.TS.Before(f.since) {
		key := e.Agent.Name + "\x00" + str(e.Data, "limit_id") + "\x00" + str(e.Data, "limit_window")
		if old, ok := f.limits[key]; !ok || !e.TS.Before(old.TS) {
			f.limits[key] = e
		}
	}
	f.eng.Apply(e)
}

// Collect reads the transcripts and builds the report. Files are read and
// parsed in parallel; events are folded in file order by one goroutine.
func Collect(ctx context.Context, opt Options) (*Report, error) {
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	if opt.Redactor == nil {
		opt.Redactor = redact.Default
	}
	if opt.Git == nil {
		opt.Git = gitwatch.Exec{}
	}
	files := discover(opt.UserHome, opt.Now, opt.Since, opt.Agent)
	f := &fold{eng: engine.New(nil, nil), seen: map[string]bool{}, projects: map[string]projects.Project{},
		byCWD: map[string]projects.Project{}, limits: map[string]model.Event{}, git: opt.Git, userHome: opt.UserHome, since: opt.Since}

	// At most GOMAXPROCS parsed files are held at once, bounding memory.
	out := make([]chan parsed, len(files))
	for i := range out {
		out[i] = make(chan parsed, 1)
	}
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var stop atomic.Bool
	go func() {
		for i, t := range files {
			sem <- struct{}{}
			if stop.Load() {
				out[i] <- parsed{}
				continue
			}
			go func() { out[i] <- read(t, opt.Redactor) }()
		}
	}()
	r := &Report{Since: opt.Since, Until: opt.Now, Agent: opt.Agent, Project: opt.Project}
	var err error
	for i := range files {
		p := <-out[i]
		<-sem
		if err == nil {
			err = ctx.Err()
			if err != nil {
				stop.Store(true)
			}
		}
		if err != nil {
			continue // keep draining the readers
		}
		r.Transcripts++
		r.Lines += p.lines
		r.BadLines += p.bad
		if p.err != nil {
			r.Unreadable++
		}
		for _, e := range p.events {
			f.apply(e)
		}
		if opt.Progress != nil {
			opt.Progress(i+1, len(files))
		}
	}
	if err != nil {
		return nil, err
	}
	f.eng.MarkIdle(opt.Now)
	build(r, f, opt)
	return r, nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
