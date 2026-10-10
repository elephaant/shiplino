// Package conversation reads a session's conversation back from the
// agent's own transcript files, on demand. Nothing it reads is stored or
// sent anywhere: it is redacted and handed to the local API, once.
package conversation

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	// The agents whose conversations can be read back.
	_ "github.com/elephaant/shiplino/pkg/adapters/claudecode"
	_ "github.com/elephaant/shiplino/pkg/adapters/codex"
	_ "github.com/elephaant/shiplino/pkg/adapters/cursor"
	_ "github.com/elephaant/shiplino/pkg/adapters/geminicli"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Sources of a Result.
const (
	SourceTranscript = "transcript" // the agent's transcript on this machine
	SourceStored     = "stored"     // what Shiplino stored at the capture level
	SourceNone       = "none"       // nothing is shown
)

// Reasons the transcript wasn't used.
const (
	ReasonMinimal     = "capture_minimal"   // capture level minimal: no content is shown
	ReasonUnsupported = "unsupported_agent" // Shiplino can't read this agent's transcripts
	ReasonNoPath      = "no_transcript"     // no transcript is known for the session
	ReasonOutside     = "outside_roots"     // the known path isn't in the agent's transcript folder
	ReasonMissing     = "transcript_missing"
)

// Caps on what one read returns.
const (
	MaxMessages  = 20000
	maxLineBytes = 16 << 20 // longer lines (pasted images, huge tool output) are skipped
	maxTextStd   = 4000     // runes per message at the standard level
	maxTextFull  = 64000    // and at full
)

// Result is a session's conversation, oldest first.
type Result struct {
	Source   string             `json:"source"`
	Reason   string             `json:"reason,omitempty"`
	Note     string             `json:"note,omitempty"`
	Messages []adapters.Message `json:"messages"`
	// Truncated is set when the conversation has more than MaxMessages.
	Truncated bool `json:"truncated,omitempty"`
}

// Reader reads conversations for one capture level.
type Reader struct {
	UserHome string
	Level    redact.Level
	Redactor *redact.Redactor
	// Events returns a session's stored events in order: used to find the
	// transcript when hooks didn't name it, and as the fallback.
	Events func(sessionID string) ([]model.Event, error)
}

// Read returns the conversation of sess (a top-level session).
func (r Reader) Read(sess *engine.Session) (Result, error) {
	if r.Level == redact.Minimal {
		return Result{Source: SourceNone, Reason: ReasonMinimal, Messages: []adapters.Message{},
			Note: `The capture level is "minimal", so Shiplino doesn't show conversations. Set capture_level = "standard" in config.toml to show them, read from the agent's own files on this machine.`}, nil
	}
	var evs []model.Event
	loadEvents := func() ([]model.Event, error) {
		if evs == nil && r.Events != nil {
			var err error
			if evs, err = r.Events(sess.ID); err != nil {
				return nil, err
			}
		}
		return evs, nil
	}
	reason, path := r.locate(sess, loadEvents)
	if reason == "" {
		msgs, truncated, err := r.readTranscript(sess.Agent, path)
		if err == nil {
			return Result{Source: SourceTranscript, Messages: msgs, Truncated: truncated}, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Result{}, err
		}
		reason = ReasonMissing
	}
	evs, err := loadEvents()
	if err != nil {
		return Result{}, err
	}
	res := Result{Source: SourceStored, Reason: reason, Note: notes[reason], Messages: r.stored(evs)}
	if len(res.Messages) > MaxMessages {
		res.Messages, res.Truncated = res.Messages[:MaxMessages], true
	}
	return res, nil
}

var notes = map[string]string{
	ReasonUnsupported: "Shiplino can't read this agent's transcripts. Showing the prompts, replies and tool calls it stored at your capture level.",
	ReasonNoPath:      "No transcript file is known for this session. Showing the prompts, replies and tool calls Shiplino stored at your capture level.",
	ReasonOutside:     "The transcript path recorded for this session isn't inside the agent's transcript folder, so it isn't read. Showing what Shiplino stored at your capture level.",
	ReasonMissing:     "The agent's transcript file is gone (deleted or rotated). Showing the prompts, replies and tool calls Shiplino stored at your capture level.",
}

// locate finds the session's main transcript. It returns "" and the path,
// or a reason it can't be read.
func (r Reader) locate(sess *engine.Session, events func() ([]model.Event, error)) (reason, path string) {
	a, ok := adapters.Get(sess.Agent)
	if !ok {
		return ReasonUnsupported, ""
	}
	if _, ok := a.(adapters.ConversationReader); !ok {
		return ReasonUnsupported, ""
	}
	disc, _ := a.(adapters.TranscriptDiscoverer)
	candidates := []string{sess.TranscriptPath}
	if evs, err := events(); err == nil {
		for _, e := range evs {
			if e.Raw != nil && strings.HasPrefix(e.Raw.Ref, "transcript:") {
				p := strings.TrimPrefix(e.Raw.Ref, "transcript:")
				if i := strings.LastIndexByte(p, '#'); i >= 0 {
					p = p[:i]
				}
				candidates = append(candidates, p)
			}
		}
	}
	reason = ReasonNoPath
	for _, p := range candidates {
		if p == "" {
			continue
		}
		switch r.check(disc, p) {
		case "":
			return "", p
		case ReasonMissing:
			reason = ReasonMissing
		case ReasonOutside:
			if reason == ReasonNoPath {
				reason = ReasonOutside
			}
		}
	}
	return reason, ""
}

// check validates a main transcript path taken from stored data: it must
// be a clean absolute .jsonl path matching one of the agent's transcript
// root patterns, both as written and with symlinks resolved.
func (r Reader) check(disc adapters.TranscriptDiscoverer, p string) string {
	if disc == nil || r.UserHome == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p || filepath.Ext(p) != ".jsonl" {
		return ReasonOutside
	}
	now := time.Now()
	roots := disc.TranscriptRoots(r.UserHome, now, time.Time{})
	if len(roots) == 0 {
		return ReasonMissing // the agent's transcript folder doesn't exist
	}
	if !matchAny(roots, p) {
		return ReasonOutside
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ReasonMissing
	}
	if !matchAny(roots, real) {
		home, err := filepath.EvalSymlinks(r.UserHome)
		if err != nil || !matchAny(disc.TranscriptRoots(home, now, time.Time{}), real) {
			return ReasonOutside
		}
	}
	if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() {
		return ReasonMissing
	}
	return ""
}

func matchAny(patterns []string, p string) bool {
	for _, pat := range patterns {
		if ok, _ := filepath.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// within reports whether sub (a subagent file next to main) resolves to a
// regular .jsonl file inside main's folder.
func within(main, sub string) bool {
	dir, err := filepath.EvalSymlinks(filepath.Dir(main))
	if err != nil || filepath.Ext(sub) != ".jsonl" {
		return false
	}
	real, err := filepath.EvalSymlinks(sub)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	fi, err := os.Stat(real)
	return err == nil && fi.Mode().IsRegular()
}

func (r Reader) readTranscript(agent, main string) ([]adapters.Message, bool, error) {
	a, _ := adapters.Get(agent)
	cr := a.(adapters.ConversationReader)
	var all []adapters.Message
	timed := true
	truncated := false
	for i, f := range cr.ConversationFiles(main) {
		if i > 0 && (f == main || !within(main, f)) {
			continue
		}
		msgs, cut, err := r.readFile(cr, f)
		if err != nil {
			if i == 0 {
				return nil, false, err
			}
			continue // a subagent file that went away
		}
		truncated = truncated || cut
		if i > 0 {
			sub := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			for j := range msgs {
				if msgs[j].Subagent == "" {
					msgs[j].Subagent = sub
				}
			}
		}
		for _, m := range msgs {
			timed = timed && !m.TS.IsZero()
		}
		all = append(all, msgs...)
	}
	if timed {
		sort.SliceStable(all, func(i, j int) bool { return all[i].TS.Before(all[j].TS) })
	}
	if len(all) > MaxMessages {
		all, truncated = all[:MaxMessages], true
	}
	return all, truncated, nil
}

func (r Reader) readFile(cr adapters.ConversationReader, path string) ([]adapters.Message, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	state := map[string]string{}
	keyed := map[string]int{}
	var out []adapters.Message
	for {
		line, err := readLine(br)
		if len(line) > 0 {
			for _, m := range cr.ConversationLine(line, state) {
				m = r.clean(m)
				if m.Key != "" {
					if i, ok := keyed[m.Key]; ok {
						out[i] = m // rewritten whole: the last copy wins
						continue
					}
					keyed[m.Key] = len(out)
				}
				if n := len(out); n > 0 && same(out[n-1], m) {
					continue // the same message written twice in a row
				}
				out = append(out, m)
			}
			if len(out) > MaxMessages {
				return out, true, nil
			}
		}
		if err == io.EOF {
			return out, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
}

// readLine reads one line, skipping (returning empty) lines over
// maxLineBytes without holding them in memory.
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf)+len(chunk) <= maxLineBytes {
			buf = append(buf, chunk...)
		} else {
			buf = buf[:0]
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil && err != io.EOF {
				return nil, err
			}
			return []byte{}, err
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func same(a, b adapters.Message) bool {
	return a.Role == b.Role && a.Text == b.Text && a.Tool == b.Tool && a.Subagent == b.Subagent && a.Role != adapters.RoleTool
}

// clean caps and redacts one message.
func (r Reader) clean(m adapters.Message) adapters.Message {
	red := r.Redactor
	if red == nil {
		red = redact.Default
	}
	limit := maxTextStd
	if r.Level == redact.Full {
		limit = maxTextFull
	}
	if rs := []rune(m.Text); len(rs) > limit {
		m.Text = string(rs[:limit]) + "…"
	}
	if m.Role == adapters.RoleTool {
		m.Text = red.Command(m.Text)
	} else {
		m.Text = red.Text(m.Text)
	}
	for i := range m.Todos {
		m.Todos[i].Text = red.Text(m.Todos[i].Text)
	}
	return m
}

// stored rebuilds the conversation from stored events: prompts, the
// agent's final message of each turn, and tool calls, as captured (and
// already redacted) at the capture level.
func (r Reader) stored(evs []model.Event) []adapters.Message {
	out := []adapters.Message{}
	for _, e := range evs {
		sub := ""
		if _, s, ok := strings.Cut(e.ActorID, "/sub:"); ok {
			sub = s
		}
		str := func(k string) string { s, _ := e.Data[k].(string); return s }
		var m adapters.Message
		switch e.Kind {
		case model.KindTurnStart:
			m = adapters.Message{Role: adapters.RoleUser, Text: str("prompt")}
		case model.KindTurnEnd:
			m = adapters.Message{Role: adapters.RoleAssistant, Text: str("assistant_summary")}
		case model.KindToolStart:
			m = adapters.Message{Role: adapters.RoleTool, Tool: firstOf(str("tool_raw"), str("tool")), Text: str("input_summary")}
		default:
			continue
		}
		if m.Text == "" && m.Role != adapters.RoleTool {
			continue
		}
		m.TS, m.Subagent = e.TS, sub
		out = append(out, r.clean(m))
	}
	return out
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
