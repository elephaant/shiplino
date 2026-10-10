package aider

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Name is the agent name Aider sessions are recorded under.
const Name = "aider"

// HistoryFile is the chat history file name Aider uses by default.
const HistoryFile = ".aider.chat.history.md"

// Line is one line of the chat history file. Offset is its byte offset
// in the file (stable, so it makes dedup keys) and Len its length in the
// file including the newline, so consecutive lines can be told apart
// from lines with other content between them.
type Line struct {
	Offset int64  `json:"o"`
	Len    int    `json:"n"`
	Text   string `json:"t"`
}

// ItemKind is what a recognized history line says.
type ItemKind string

const (
	ItemPrompt  ItemKind = "prompt"  // Text: the prompt (several lines joined)
	ItemVersion ItemKind = "version" // Text: Aider's version
	ItemModel   ItemKind = "model"   // Text: the main model
	ItemTokens  ItemKind = "tokens"  // Sent, Received, CacheWrite, CacheHit
	ItemCost    ItemKind = "cost"    // MessageCost, SessionCost (USD)
	ItemEdit    ItemKind = "edit"    // Text: path relative to the repo root
	ItemCommit  ItemKind = "commit"  // SHA, Text: commit subject
)

// Item is one fact read from the history.
type Item struct {
	Kind   ItemKind
	Offset int64
	Text   string
	SHA    string

	// Token counts as Aider prints them: rounded above 1,000 ("2.1k").
	Sent, Received, CacheWrite, CacheHit int64
	MessageCost, SessionCost             float64
}

var (
	versionRe = regexp.MustCompile(`^> Aider v(\S+)`)
	modelRe   = regexp.MustCompile(`^> (?:Main model|Model): (\S+)`)
	tokensRe  = regexp.MustCompile(`^(?:> )?Tokens: ([\d.]+k?) sent(?:, ([\d.]+k?) cache write)?(?:, ([\d.]+k?) cache hit)?, ([\d.]+k?) received\.`)
	costRe    = regexp.MustCompile(`(?:^|[ >])Cost: \$([\d.]+) message, \$([\d.]+) session\.`)
	editRe    = regexp.MustCompile(`^> Applied edit to (.+)$`)
	commitRe  = regexp.MustCompile(`^> Commit ([0-9a-f]{7,40})(?: (.*))?$`)
)

// clean drops the line ending and Markdown hard break Aider adds.
func clean(text string) string {
	return strings.TrimRight(text, " \r\n")
}

func isPrompt(t string) bool { return t == "####" || strings.HasPrefix(t, "#### ") }

// Relevant reports whether a history line carries anything Shiplino
// records. Everything else (the model's answers, other tool output) is
// never copied out of the file.
func Relevant(text string) bool {
	t := clean(text)
	if isPrompt(t) {
		return true
	}
	for _, re := range []*regexp.Regexp{versionRe, modelRe, tokensRe, costRe, editRe, commitRe} {
		if re.MatchString(t) {
			return true
		}
	}
	return false
}

// Minimize removes content kept only above the minimal capture level:
// prompt text and commit messages. Paths, tokens and cost stay.
func Minimize(text string) string {
	t := clean(text)
	if isPrompt(t) {
		return "####"
	}
	if m := commitRe.FindStringSubmatch(t); m != nil {
		return "> Commit " + m[1]
	}
	return t
}

// Parse turns relevant history lines (in file order) into items.
// Consecutive "####" lines are one multi-line prompt.
func Parse(lines []Line) []Item {
	var out []Item
	var prompt *Item
	var promptEnd int64
	for _, l := range lines {
		t := clean(l.Text)
		if isPrompt(t) {
			text := strings.TrimSpace(strings.TrimPrefix(t, "####"))
			if text == "<blank>" {
				text = ""
			}
			if prompt != nil && l.Offset == promptEnd {
				if text != "" {
					prompt.Text = strings.TrimPrefix(prompt.Text+"\n"+text, "\n")
				}
			} else {
				out = append(out, Item{Kind: ItemPrompt, Offset: l.Offset, Text: text})
				prompt = &out[len(out)-1]
			}
			promptEnd = l.Offset + int64(l.Len)
			continue
		}
		prompt = nil
		if m := versionRe.FindStringSubmatch(t); m != nil {
			out = append(out, Item{Kind: ItemVersion, Offset: l.Offset, Text: m[1]})
		}
		if m := modelRe.FindStringSubmatch(t); m != nil {
			out = append(out, Item{Kind: ItemModel, Offset: l.Offset, Text: m[1]})
		}
		if m := tokensRe.FindStringSubmatch(t); m != nil {
			out = append(out, Item{Kind: ItemTokens, Offset: l.Offset,
				Sent: tokens(m[1]), CacheWrite: tokens(m[2]), CacheHit: tokens(m[3]), Received: tokens(m[4])})
		}
		if m := costRe.FindStringSubmatch(t); m != nil {
			msg, _ := strconv.ParseFloat(m[1], 64)
			total, _ := strconv.ParseFloat(m[2], 64)
			out = append(out, Item{Kind: ItemCost, Offset: l.Offset, MessageCost: msg, SessionCost: total})
		}
		if m := editRe.FindStringSubmatch(t); m != nil {
			out = append(out, Item{Kind: ItemEdit, Offset: l.Offset, Text: m[1]})
		}
		if m := commitRe.FindStringSubmatch(t); m != nil {
			out = append(out, Item{Kind: ItemCommit, Offset: l.Offset, SHA: m[1], Text: m[2]})
		}
	}
	return out
}

// tokens reads Aider's token format: "512", "2.1k" or "12k".
func tokens(s string) int64 {
	mult := 1.0
	if strings.HasSuffix(s, "k") {
		s, mult = strings.TrimSuffix(s, "k"), 1000
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f*mult + 0.5)
}

// HistoryPath returns where an Aider run started in cwd with args writes
// its chat history, and the root its edit paths are relative to (the git
// root, or cwd without git). It mirrors Aider's own defaults; a path set
// only in .aider.conf.yml or .env isn't seen.
func HistoryPath(args []string, getenv func(string) string, cwd string) (path, root string) {
	root = cwd
	noGit := false
	for i, a := range args {
		switch {
		case a == "--chat-history-file" && i+1 < len(args):
			path = args[i+1]
		case strings.HasPrefix(a, "--chat-history-file="):
			path = strings.TrimPrefix(a, "--chat-history-file=")
		case a == "--no-git":
			noGit = true
		}
	}
	if !noGit {
		if r := gitRoot(cwd); r != "" {
			root = r
		}
	}
	if path == "" {
		path = getenv("AIDER_CHAT_HISTORY_FILE")
	}
	if path == "" {
		return filepath.Join(root, HistoryFile), root
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return path, root
}

// gitRoot finds the enclosing repository by looking for .git (a directory,
// or a file in worktrees and submodules), as Aider's git lookup does.
func gitRoot(dir string) string {
	for d := filepath.Clean(dir); ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}
