package sync

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Scope decides what leaves the machine: which projects, and how much of
// each event.
type Scope struct {
	Projects, Exclude []string
	Level             redact.Level
	Redactor          *redact.Redactor
	SendUser          bool // send the OS user name (never at minimal)
	allow, deny       []*regexp.Regexp
}

// ScopeFor builds the scope from the user's config.
func ScopeFor(c config.Config) Scope {
	level, _ := c.SyncLevel()
	s := Scope{Projects: c.Sync.Projects, Exclude: c.Sync.Exclude, Level: level, Redactor: c.Redactor(), SendUser: c.Sync.SendUser}
	for _, p := range s.Projects {
		s.allow = append(s.allow, glob(p))
	}
	for _, p := range s.Exclude {
		s.deny = append(s.deny, glob(p))
	}
	return s
}

// glob matches a whole project id; * matches anything (slashes too) and
// ? one character.
func glob(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Allowed reports whether a project's events may be sent. Events with no
// known project only go when the allow list has "*".
func (s Scope) Allowed(projectID string) bool {
	match := func(list []*regexp.Regexp) bool {
		return slices.ContainsFunc(list, func(re *regexp.Regexp) bool { return re.MatchString(projectID) })
	}
	return match(s.allow) && !match(s.deny)
}

// key identifies the allow and exclude lists, to notice when they change.
func (s Scope) key() string {
	b, _ := json.Marshal([2][]string{sorted(s.Projects), sorted(s.Exclude)})
	return string(b)
}

// widens reports whether this scope allows something the scope with the
// old key didn't, so already-skipped history must be read again.
func (s Scope) widens(oldKey string) bool {
	if oldKey == "" {
		return false
	}
	var old [2][]string
	if json.Unmarshal([]byte(oldKey), &old) != nil {
		return true
	}
	for _, p := range s.Projects {
		if !slices.Contains(old[0], p) {
			return true
		}
	}
	for _, p := range old[1] {
		if !slices.Contains(s.Exclude, p) {
			return true
		}
	}
	return false
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}

// item is one event ready to send, with the rowid it came from.
type item struct {
	rowid int64
	body  json.RawMessage
}

// envelopeBytes is room left in a batch for device_id, device_name and
// JSON punctuation.
const envelopeBytes = 4 << 10

// batchBytes is MaxBatchBytes; tests shrink it.
var batchBytes = MaxBatchBytes

// Prepare turns stored rows into events to send: rows of projects that
// aren't allowed are skipped, and the rest are stripped to the sync
// capture level and redacted again (with the user's extra rules too).
// It stops before batchBytes. last is the rowid of the last row
// consumed (sent or skipped).
func (s Scope) Prepare(rows []store.SyncRow) (items []item, last int64) {
	size := envelopeBytes
	for _, r := range rows {
		if !s.Allowed(r.ProjectID) {
			last = r.RowID
			continue
		}
		var e model.Event
		if json.Unmarshal(r.Body, &e) != nil {
			last = r.RowID // unreadable rows are skipped, as at ingest
			continue
		}
		body, err := json.Marshal(s.Outgoing(e))
		if err != nil {
			last = r.RowID
			continue
		}
		if len(items) > 0 && size+len(body)+1 > batchBytes {
			break
		}
		size += len(body) + 1
		items = append(items, item{rowid: r.RowID, body: body})
		last = r.RowID
	}
	return items, last
}

// Outgoing is an event exactly as it is sent.
func (s Scope) Outgoing(e model.Event) model.Event {
	if e.Data != nil {
		e.Data = deepCopy(e.Data)
		s.Redactor.Event(&e, s.Level)
	}
	e.Raw = nil // a pointer into local files: meaningless elsewhere
	if !s.SendUser || s.Level == redact.Minimal {
		e.User = ""
	}
	if s.Level == redact.Minimal {
		localPaths(&e)
	}
	return e
}

// pathKeys are data fields holding a file or folder path.
var pathKeys = []string{"path", "file_path", "cwd", "transcript_path"}

// pathTools are tools whose input_summary is a file path.
var pathTools = map[string]bool{model.ToolRead: true, model.ToolEdit: true, model.ToolWrite: true}

// localPaths keeps local paths from leaving the machine at minimal: paths
// become relative to the project root, paths outside it become "…/" plus
// their base name, and the project's absolute folders are dropped (its
// id, remote, branch and head stay).
func localPaths(e *model.Event) {
	root := ""
	if p := e.Project; p != nil {
		root = p.RepoRoot
		if root == "" {
			for _, prefix := range []string{"local:", "dir:"} {
				if rest, ok := strings.CutPrefix(p.ID, prefix); ok {
					root = rest
				}
			}
		}
		if root == "" {
			root = p.CWD
		}
		cp := *p
		cp.CWD, cp.RepoRoot = "", ""
		e.Project = &cp
	}
	d := e.Data
	if d == nil {
		return
	}
	for _, k := range pathKeys {
		if s, ok := d[k].(string); ok {
			d[k] = relPath(root, s)
		}
	}
	if s, ok := d["input_summary"].(string); ok && pathTools[str(d, "tool")] {
		d["input_summary"] = relPath(root, s)
	}
	if list, ok := d["files"].([]any); ok {
		out := make([]any, len(list))
		for i, v := range list {
			if s, ok := v.(string); ok {
				v = relPath(root, s)
			}
			out[i] = v
		}
		d["files"] = out
	}
}

// relPath makes an absolute path relative to root ("." for root itself);
// one outside root becomes "…/<base name>". Relative paths are kept.
func relPath(root, p string) string {
	if !isAbs(p) {
		return p
	}
	sp := filepath.ToSlash(p)
	if r := strings.TrimRight(filepath.ToSlash(root), "/"); r != "" && isAbs(root) {
		if sp == r {
			return "."
		}
		if rest, ok := strings.CutPrefix(sp, r+"/"); ok {
			return rest
		}
	}
	return "…/" + path.Base(strings.ReplaceAll(sp, `\`, "/"))
}

// isAbs accepts Unix and Windows absolute paths on every OS, since events
// may describe either.
func isAbs(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			v = deepCopy(sub)
		}
		out[k] = v
	}
	return out
}
