package adapters

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// ErrUnknownEvent is returned for native events an adapter doesn't know.
// Callers keep the raw payload and count it; it is never a hard failure.
var ErrUnknownEvent = errors.New("unknown native event")

// HookMeta is what the shim recorded alongside a raw hook payload.
type HookMeta struct {
	EnvelopeID string    // ULID of the spool line; stable across re-reads
	Event      string    // native hook event name, if known
	ReceivedAt time.Time // when the hook ran
	MachineID  string
	User       string
	Ref        string // pointer to the raw payload, e.g. "spool/claude-code/<s>.jsonl#1234"
}

// Adapter converts one agent's native data into universal events.
//
// Detection, hook installation and transcript tailing join this interface
// as they are built; see docs/adding-an-adapter.md.
type Adapter interface {
	// Name is the agent name used in events and spool paths, e.g. "claude-code".
	Name() string
	// ParseHook converts one hook payload into zero or more events.
	// Known events that carry nothing worth recording return (nil, nil).
	ParseHook(payload []byte, meta HookMeta) ([]model.Event, error)
}

// TranscriptMeta describes where a transcript line came from.
type TranscriptMeta struct {
	ReceivedAt time.Time // fallback timestamp when the line has none
	User       string
	Ref        string // e.g. "transcript:/path/to/file.jsonl#1234"
	// Path is the transcript file, for formats whose ids live in the path.
	Path string
	// ModTime is the file's modification time when it was read, a
	// timestamp fallback for formats with few or no timestamps.
	ModTime time.Time
	// State is per-file memory the daemon keeps for the parser (e.g. the
	// current model or turn), for formats that state things once.
	State map[string]string
	// Warmup is set while the daemon replays a file's earlier lines after
	// a restart to rebuild State; the parser must return no events.
	Warmup bool
}

// StateImported is the State key a parser sets ("1") on a transcript that
// is a copy of another agent's session (e.g. history imported into a
// desktop app). The parser emits nothing for it, since the original
// agent's own record is used; the daemon counts these files for doctor.
const StateImported = "imported"

// TranscriptDiscoverer is implemented by adapters whose transcripts can
// be found by location (sessions without hooks, e.g. desktop apps).
type TranscriptDiscoverer interface {
	// TranscriptRoots returns glob patterns that cover every transcript
	// modified since `since` (callers still filter by modification time).
	TranscriptRoots(userHome string, now, since time.Time) []string
}

// TranscriptParser is implemented by adapters that read the agent's own
// session files for data hooks don't carry (tokens, model, version).
type TranscriptParser interface {
	// ParseTranscriptLine converts one transcript line into zero or more
	// events. Lines that carry nothing of interest return (nil, nil).
	ParseTranscriptLine(line []byte, meta TranscriptMeta) ([]model.Event, error)
}

// DocumentParser is implemented by adapters whose transcripts are whole
// JSON documents that the agent rewrites in place (Cline's task files),
// instead of lines it appends. TranscriptRoots finds them by their .json
// extension.
type DocumentParser interface {
	// ParseTranscriptDocument converts a whole document into events. The
	// daemon calls it again whenever the file changes, so every event
	// needs a dedup key that stays the same across rewrites, and a value
	// must only be emitted once it's final. recheck asks for another call
	// later even if the file doesn't change, for values that become final
	// with time (meta.ModTime and meta.ReceivedAt tell how long the file
	// has been quiet).
	ParseTranscriptDocument(doc []byte, meta TranscriptMeta) (events []model.Event, recheck bool, err error)
}

var registry = struct {
	sync.RWMutex
	m map[string]Adapter
}{m: map[string]Adapter{}}

// Register makes an adapter available by name. It panics on duplicates,
// which can only happen through a programming error at init time.
func Register(a Adapter) {
	registry.Lock()
	defer registry.Unlock()
	if _, dup := registry.m[a.Name()]; dup {
		panic("adapters: duplicate adapter " + a.Name())
	}
	registry.m[a.Name()] = a
}

// Get returns the adapter registered under name.
func Get(name string) (Adapter, bool) {
	registry.RLock()
	defer registry.RUnlock()
	a, ok := registry.m[name]
	return a, ok
}

// Names lists registered adapters, sorted.
func Names() []string {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]string, 0, len(registry.m))
	for n := range registry.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
