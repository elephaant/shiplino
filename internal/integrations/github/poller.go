package github

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// Store is what the poller needs from the database.
type Store interface {
	SessionsActiveSince(ctx context.Context, t time.Time) ([]*engine.Session, error)
	PRs(ctx context.Context) (map[string]PR, error)
	PutPR(ctx context.Context, pr PR) error
}

// Recorder stores events through the daemon's normal pipeline.
type Recorder func(ctx context.Context, events []model.Event) error

const (
	lookback      = 14 * 24 * time.Hour // sessions whose PRs are followed
	noPRRecheck   = 30 * time.Minute    // a branch without a PR is asked again after this
	closedRecheck = 24 * time.Hour      // closed PRs can be reopened
)

// Status is what doctor and the settings page show.
type Status struct {
	Enabled     bool      `json:"enabled"`
	TokenSource string    `json:"token_source,omitempty"`
	LastPoll    time.Time `json:"last_poll,omitzero"`
	Tracked     int       `json:"tracked"`
	Remaining   int       `json:"rate_remaining"`
	Error       string    `json:"error,omitempty"`
}

// Poller follows the pull requests of recent sessions.
type Poller struct {
	st       Store
	record   Recorder
	changed  func([]*engine.Session) // their cards should refresh (PR state changed)
	every    time.Duration
	newToken func(ctx context.Context) (string, string, error)
	now      func() time.Time
	base     string // API base URL; empty means api.github.com (tests set it)

	mu       sync.Mutex
	client   *Client
	status   Status
	noPR     map[string]time.Time // repo#branch → when last found without a PR
	lastSeen map[string]time.Time // url → last poll
}

// NewPoller returns a poller that checks every `every` (at least a minute).
func NewPoller(st Store, record Recorder, changed func([]*engine.Session), every time.Duration) *Poller {
	if every < time.Minute {
		every = time.Minute
	}
	return &Poller{st: st, record: record, changed: changed, every: every, newToken: Token, now: time.Now,
		status: Status{Enabled: true, Remaining: -1}, noPR: map[string]time.Time{}, lastSeen: map[string]time.Time{}}
}

// Status returns the current state.
func (p *Poller) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.status
	if p.client != nil {
		s.Remaining = p.client.Remaining()
	}
	return s
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.every)
	defer t.Stop()
	for {
		p.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll does one pass. Errors are kept in Status, never returned: a GitHub
// problem must not affect recording.
func (p *Poller) Poll(ctx context.Context) {
	err := p.poll(ctx)
	p.mu.Lock()
	p.status.LastPoll = p.now()
	p.status.Error = ""
	if err != nil {
		p.status.Error = err.Error()
	}
	p.mu.Unlock()
}

func (p *Poller) poll(ctx context.Context) error {
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil {
		token, source, err := p.newToken(ctx)
		if err != nil {
			return err
		}
		c = New(token)
		if p.base != "" {
			c.Base = p.base
		}
		p.mu.Lock()
		p.client, p.status.TokenSource = c, source
		p.mu.Unlock()
	}
	now := p.now()
	sessions, err := p.st.SessionsActiveSince(ctx, now.Add(-lookback))
	if err != nil {
		return err
	}
	known, err := p.st.PRs(ctx)
	if err != nil {
		return err
	}

	// Which PRs to follow, and which sessions point at each.
	byURL := map[string][]*engine.Session{}
	branches := map[string][]*engine.Session{} // repo#branch → sessions without a PR
	for _, s := range sessions {
		if s.ParentID != "" {
			continue
		}
		hasPR := false
		for _, l := range s.Links {
			if l.Kind == "pr" && l.URL != "" {
				if _, _, ok := ParseURL(l.URL); ok {
					byURL[l.URL] = append(byURL[l.URL], s)
					hasPR = true
				}
			}
		}
		if repo, ok := RepoOf(s.ProjectID); ok && !hasPR && s.Branch != "" && !defaultBranch(s.Branch) {
			k := repo + "#" + s.Branch
			branches[k] = append(branches[k], s)
		}
	}

	var events []model.Event
	var refresh []*engine.Session
	// Link sessions to the PR of their branch.
	for k, list := range branches {
		if t, ok := p.noPR[k]; ok && now.Sub(t) < noPRRecheck {
			continue
		}
		repo, branch := splitKey(k)
		pr, found, err := c.ForBranch(ctx, repo, branch)
		if err != nil {
			return err
		}
		if !found {
			p.noPR[k] = now
			continue
		}
		for _, s := range list {
			events = append(events, prEvent(s, pr, "linked", now))
		}
		byURL[pr.URL] = append(byURL[pr.URL], list...)
	}
	// Refresh each followed PR.
	for u, list := range byURL {
		prev, had := known[u]
		if had && (prev.State == "merged" || prev.State == "closed") && now.Sub(p.lastSeen[u]) < closedRecheck {
			continue
		}
		repo, number, _ := ParseURL(u)
		pr, err := c.Get(ctx, repo, number)
		if err != nil {
			return err
		}
		p.lastSeen[u] = now
		if had && samePR(prev, pr) {
			continue
		}
		if err := p.st.PutPR(ctx, pr); err != nil {
			return err
		}
		refresh = append(refresh, list...)
		if pr.State == "merged" && (!had || prev.State != "merged") {
			for _, s := range list {
				events = append(events, prEvent(s, pr, "merged", pr.MergedAt))
			}
		}
	}
	p.mu.Lock()
	p.status.Tracked = len(byURL)
	p.mu.Unlock()
	if len(events) > 0 {
		if err := p.record(ctx, events); err != nil {
			return err
		}
	}
	if len(refresh) > 0 && p.changed != nil {
		p.changed(refresh)
	}
	return nil
}

// prEvent links a session to a PR (action "linked") or records its merge.
func prEvent(s *engine.Session, pr PR, action string, at time.Time) model.Event {
	if at.IsZero() {
		at = time.Now()
	}
	key := fmt.Sprintf("%s:pr:%d", s.ID, pr.Number)
	if action == "merged" {
		key += ":merged"
	}
	return model.Event{
		ID: model.NewULID(at), V: model.SchemaVersion, TS: at.UTC(), ReceivedAt: time.Now().UTC(),
		Kind: model.KindGitPR, Agent: model.Agent{Name: s.Agent}, Collector: model.CollectorGit,
		SessionID: s.ID, ActorID: s.ID,
		Data:     map[string]any{"url": pr.URL, "number": pr.Number, "action": action, "source": "github"},
		DedupKey: key,
	}
}

func defaultBranch(b string) bool {
	switch b {
	case "main", "master", "trunk", "develop", "HEAD":
		return true
	}
	return false
}

func splitKey(k string) (repo, branch string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '#' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}

// samePR compares what the board shows.
func samePR(a, b PR) bool {
	return a.State == b.State && a.Checks == b.Checks && a.Review == b.Review && a.Title == b.Title && a.HeadSHA == b.HeadSHA
}
