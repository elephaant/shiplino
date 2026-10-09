// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

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
}

// TranscriptParser is implemented by adapters that read the agent's own
// session files for data hooks don't carry (tokens, model, version).
type TranscriptParser interface {
	// ParseTranscriptLine converts one transcript line into zero or more
	// events. Lines that carry nothing of interest return (nil, nil).
	ParseTranscriptLine(line []byte, meta TranscriptMeta) ([]model.Event, error)
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
