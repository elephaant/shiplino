package redact

import (
	"fmt"
	"strings"

	"github.com/elephaant/shiplino/pkg/model"
)

// Level is how much content is kept.
type Level string

const (
	// Minimal keeps event kinds, timing, tool names, file paths, exit
	// codes and tokens/cost: no prompts, commands, messages or outputs.
	Minimal Level = "minimal"
	// Standard (default) adds truncated prompts, commands, short tool
	// summaries and the agent's final message, all redacted.
	Standard Level = "standard"
	// Full keeps everything Shiplino captures, still redacted.
	Full Level = "full"
)

// ParseLevel validates a capture level ("" means Standard).
func ParseLevel(s string) (Level, error) {
	switch Level(s) {
	case "":
		return Standard, nil
	case Minimal, Standard, Full:
		return Level(s), nil
	}
	return "", fmt.Errorf("unknown capture level %q (use minimal, standard or full)", s)
}

// MaxPatchBytes caps the diff kept per file edit ("patch" on file.edit,
// a full-level field: code is content). Longer patches are cut at a line
// boundary and marked patch_truncated. Below full, and for secret files
// (see SecretFile), the patch is dropped and patch_omitted says why.
const MaxPatchBytes = 64 << 10

const (
	maxPrompt  = 2000
	maxSummary = 500
	maxError   = 500
)

// pathTools are tools whose input summary is a file path (allowed at
// every level).
var pathTools = map[string]bool{model.ToolRead: true, model.ToolEdit: true, model.ToolWrite: true}

// Event applies the capture level and redacts every remaining string.
// It runs in the daemon before anything is stored.
func (r *Redactor) Event(e *model.Event, level Level) {
	d := e.Data
	if d == nil {
		return
	}
	minimal := level == Minimal
	// A shell command's program ("go", "npm") is metadata, kept at every
	// level: derived from the redacted command before the command goes,
	// and checked when an agent or SDK sent it.
	if e.Kind == model.KindShellExec {
		if p, ok := d["program"]; ok {
			if s, _ := p.(string); !ValidProgram(s) {
				delete(d, "program")
			}
		} else if p := Program(r.Command(str(d, "command"))); p != "" {
			d["program"] = p
		}
	}
	cut := func(key string, max int) {
		if s, ok := d[key].(string); ok && level != Full && len([]rune(s)) > max {
			d[key] = string([]rune(s)[:max]) + "…"
		}
	}

	if minimal {
		for _, k := range []string{"prompt", "assistant_summary", "command", "title", "notification_type"} {
			delete(d, k)
		}
		if _, ok := d["title"]; !ok {
			delete(d, "title_source")
		}
		if e.Kind == model.KindToolStart && !pathTools[str(d, "tool")] {
			delete(d, "input_summary")
		}
		// A waiting card still needs to say what it waits for; any other
		// message (e.g. a commit subject) is content and goes.
		if _, ok := d["message"]; ok {
			if e.Kind == model.KindWaitingStart {
				d["message"] = waitingText(str(d, "reason"))
			} else {
				delete(d, "message")
			}
		}
		if e.Kind == model.KindToolEnd {
			delete(d, "error") // tool errors carry output text
		}
	}
	if _, ok := d["patch"]; ok {
		switch {
		case minimal:
			delete(d, "patch")
			delete(d, "patch_source")
		case level != Full:
			delete(d, "patch")
			d["patch_omitted"] = "capture_level"
		case SecretFile(str(d, "path")):
			delete(d, "patch")
			d["patch_omitted"] = "secret_file"
		default:
			if p, cut := capPatch(str(d, "patch")); cut {
				d["patch"], d["patch_truncated"] = p, true
			}
		}
	}
	cut("prompt", maxPrompt)
	cut("assistant_summary", maxSummary)
	cut("error", maxError)

	for k, v := range d {
		switch s := v.(type) {
		case string:
			if k == "command" || (k == "input_summary" && str(d, "tool") == model.ToolShell) {
				d[k] = r.Command(s)
			} else {
				d[k] = r.Text(s)
			}
		case map[string]any, []any:
			d[k] = r.value(s)
		}
	}
}

// value redacts every string inside nested maps and lists (data sent by
// custom agents can have any shape).
func (r *Redactor) value(v any) any {
	switch s := v.(type) {
	case string:
		return r.Text(s)
	case map[string]any:
		for k, x := range s {
			s[k] = r.value(x)
		}
	case []any:
		for i, x := range s {
			s[i] = r.value(x)
		}
	}
	return v
}

// capPatch cuts a patch to MaxPatchBytes at a line boundary.
func capPatch(p string) (string, bool) {
	if len(p) <= MaxPatchBytes {
		return p, false
	}
	p = p[:MaxPatchBytes]
	if i := strings.LastIndexByte(p, '\n'); i >= 0 {
		return p[:i+1], true
	}
	return "", true
}

func waitingText(reason string) string {
	switch reason {
	case "permission":
		return "Waiting for your approval"
	case "question":
		return "Waiting for your answer"
	}
	return "Waiting for you"
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
