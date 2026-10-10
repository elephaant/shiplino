package demo

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// project is one synthetic repository under /home/dev/code.
type project struct {
	name   string
	branch string
	tasks  []task
}

func (p *project) cwd() string    { return "/home/dev/code/" + p.name }
func (p *project) remote() string { return "https://example.com/acme/" + p.name + ".git" }

// task is something an agent is asked to do in a project.
type task struct {
	prompt  string
	pattern string   // what it searches for first
	files   []string // files it edits, relative to the project
	test    string   // how it checks the change
	summary string   // its last message
}

// projects are the demo's repositories. Everything here is made up.
var projects = []*project{
	{name: "storefront", branch: "main", tasks: []task{
		{"Add a quantity stepper to the cart line items", "LineItem", []string{"src/components/cart/line-item.tsx", "src/components/cart/quantity-stepper.tsx"}, "npm test -- cart", "Added a quantity stepper with keyboard support."},
		{"Fix rounding of cart totals when a discount applies", "roundTotal", []string{"src/lib/money.ts", "src/lib/money.test.ts"}, "npm test -- money", "Totals now round once, after discounts."},
		{"Show the estimated delivery date on the product page", "shippingEstimate", []string{"src/app/product/page.tsx", "src/lib/shipping.ts"}, "npm test -- shipping", "The product page shows a delivery window."},
		{"Lazy-load product images below the fold", "ProductImage", []string{"src/components/product-image.tsx"}, "npm run build", "Images below the fold load lazily now."},
		{"Add an empty state to the wishlist", "Wishlist", []string{"src/app/wishlist/page.tsx"}, "npm test -- wishlist", "The wishlist has an empty state with a call to action."},
	}},
	{name: "billing-api", branch: "main", tasks: []task{
		{"Retry webhook deliveries with exponential backoff", "deliver", []string{"internal/webhooks/deliver.go", "internal/webhooks/deliver_test.go"}, "go test ./internal/webhooks/...", "Deliveries retry 5 times with backoff and jitter."},
		{"Add an endpoint that returns an invoice as PDF", "InvoiceHandler", []string{"internal/invoices/pdf.go", "internal/http/routes.go"}, "go test ./internal/invoices/...", "GET /invoices/{id}.pdf renders the invoice."},
		{"Handle plan changes in the middle of a billing cycle", "prorate", []string{"internal/billing/proration.go", "internal/billing/proration_test.go"}, "go test ./internal/billing/...", "Proration credits the unused part of the old plan."},
		{"Add request ids to the access log", "accessLog", []string{"internal/http/middleware.go"}, "go test ./internal/http/...", "Every log line carries the request id."},
	}},
	{name: "mobile-app", branch: "main", tasks: []task{
		{"Add pull-to-refresh to the orders screen", "OrdersScreen", []string{"src/screens/orders.tsx"}, "npm test -- orders", "Pulling down refreshes the order list."},
		{"Stop the keyboard from covering the sign-in form", "SignInForm", []string{"src/screens/sign-in.tsx"}, "npm test -- sign-in", "The form scrolls above the keyboard."},
		{"Cache the product catalog for offline use", "fetchCatalog", []string{"src/data/catalog.ts", "src/data/cache.ts"}, "npm test -- catalog", "The catalog is cached for 24 hours."},
	}},
	{name: "docs-site", branch: "main", tasks: []task{
		{"Write a quickstart for the billing API", "quickstart", []string{"docs/quickstart.md"}, "npm run build", "Added a five-minute quickstart."},
		{"Fix broken links in the API reference", "href", []string{"docs/reference/webhooks.md", "docs/reference/invoices.md"}, "npm run check-links", "Fixed 7 broken links."},
		{"Add a dark mode toggle to the docs header", "Header", []string{"src/components/header.tsx"}, "npm run build", "The header has a dark mode toggle."},
	}},
}

// fakeGit answers project detection for the demo folders: each is the
// root of a repo with an example.com remote. Other folders aren't repos.
type fakeGit struct{}

func (fakeGit) find(dir string) *project {
	dir = filepath.ToSlash(filepath.Clean(dir))
	for _, p := range projects {
		if dir == p.cwd() || strings.HasPrefix(dir, p.cwd()+"/") {
			return p
		}
	}
	return nil
}

func (g fakeGit) CommonDir(dir string) string {
	if p := g.find(dir); p != nil {
		return filepath.FromSlash(p.cwd() + "/.git")
	}
	return ""
}

func (g fakeGit) Remote(dir string) string {
	if p := g.find(dir); p != nil {
		return p.remote()
	}
	return ""
}

func (g fakeGit) Branch(dir string) string {
	if p := g.find(dir); p != nil {
		return p.branch
	}
	return ""
}

// outcome is how a turn ends.
type outcome int

const (
	committed outcome = iota // edits, then a commit: Done
	merged                   // edits, a PR, a commit, then the PR is merged: Done
	prOpen                   // edits and a PR still open: Review
	review                   // edits not committed yet: Review
	answered                 // no edits (a question): Done
	failed                   // the agent stopped with an error: Failed
)

// git is a commit or a PR merge, recorded after the turn that caused it.
// Shiplino learns these from git and GitHub; the demo has neither, so they
// are stored the way the git watcher and the GitHub poller store them.
type git struct {
	at      time.Time
	s       *session
	sha     string
	message string
	files   []string
	added   int
	removed int
	pr      int // merged PR number; 0 for a commit
	// attribution is how sure the link to the session is: "exact" (the
	// default) or "likely" (git only saw files the session edited).
	attribution string
}

func (g git) event() model.Event {
	sid := model.SessionID(g.s.agent, g.s.id)
	e := model.Event{ID: model.NewULID(g.at), V: model.SchemaVersion, TS: g.at.UTC(), ReceivedAt: g.at.UTC(),
		Agent: model.Agent{Name: g.s.agent}, Collector: model.CollectorGit, SessionID: sid, ActorID: sid}
	if g.pr != 0 {
		e.Kind = model.KindGitPR
		e.Data = map[string]any{"url": prURL(g.s.project, g.pr), "number": g.pr, "action": "merged", "source": "github"}
		e.DedupKey = fmt.Sprintf("%s:pr:%d:merged", sid, g.pr)
		return e
	}
	root := filepath.FromSlash(g.s.project.cwd())
	e.Kind = model.KindGitCommit
	e.Project = &model.Project{CWD: root, RepoRoot: root}
	e.Data = map[string]any{"sha": g.sha, "message": g.message, "author": "dev", "files": g.files, "files_changed": len(g.files),
		"lines_added": g.added, "lines_removed": g.removed, "attribution": cmp.Or(g.attribution, "exact")}
	// Line authorship: most lines are the agent's, a few the user's
	// (picked from the sha, so the random sequence stays as it was).
	human := g.added * int(g.sha[len(g.sha)-1]%4) / 20
	files := make([]any, len(g.files))
	for i, f := range g.files {
		files[i] = f
	}
	e.Data["agent_lines_added"], e.Data["human_lines_added"], e.Data["unknown_lines_added"] = g.added-human, human, 0
	e.Data["authorship"], e.Data["agent_files"] = "observed", files
	e.DedupKey = sid + ":git:" + g.sha
	return e
}

func prURL(p *project, n int) string {
	return fmt.Sprintf("https://example.com/acme/%s/pull/%d", p.name, n)
}

// gap is n steps of the session's pace, with some jitter.
func (s *session) gap(n float64) time.Duration {
	return time.Duration(n * (0.6 + 0.8*s.r.Float64()) * float64(s.pace))
}

// tokens is a token count around base, scaled up because each step stands
// for several model calls.
func (s *session) tokens(base int64) int64 { return 5 * (base/2 + s.r.Int64N(base)) }

// after advances the session's clock by d: virtual time for seeded
// history, a real wait for live sessions.
func (s *session) after(d time.Duration) time.Time {
	if s.live == nil {
		s.now = s.now.Add(d)
		return s.now
	}
	select {
	case <-s.live.Done():
		if s.err == nil {
			s.err = s.live.Err()
		}
	case <-time.After(d):
	}
	s.now = time.Now()
	return s.now
}

// work runs one turn of a task (plan, look around, edit, test, finish)
// and returns the commit and PR merge that follow it.
func (s *session) work(tk task, o outcome) []git {
	if s.prompted {
		s.prompted = false // the turn was started already
	} else {
		s.prompt(s.after(0), tk.prompt)
	}
	s.usage(s.after(s.gap(1)), "I'll look at how this works today, then make the change.", s.tokens(6000), s.tokens(400), s.tokens(24000))
	steps := []todo{{"Find the code involved", "in_progress"}, {"Make the change", "pending"}, {"Run the checks", "pending"}}
	s.todos(s.after(s.gap(0.5)), steps)
	s.search(s.after(s.gap(1)), tk.pattern)
	for _, f := range tk.files {
		s.read(s.after(s.gap(0.5)), f)
	}
	if (s.agent == claude || s.agent == codex) && s.r.IntN(3) == 0 {
		s.now = s.subagent(s.after(s.gap(1)), "Find callers of "+tk.pattern, tk.files)
	}
	if o == answered {
		s.usage(s.after(s.gap(2)), tk.summary, s.tokens(8000), s.tokens(900), s.tokens(30000))
		s.stop(s.after(s.gap(0.5)), tk.summary)
		return nil
	}
	steps[0].status, steps[1].status = "completed", "in_progress"
	s.todos(s.after(s.gap(0.5)), steps)
	added, removed := 0, 0
	for _, f := range tk.files {
		a, r := 4+s.r.IntN(50), s.r.IntN(14)
		s.edit(s.after(s.gap(2)), f, a, r)
		added, removed = added+a, removed+r
	}
	s.usage(s.after(s.gap(1)), "Made the change; running the checks.", s.tokens(12000), s.tokens(2500), s.tokens(40000))
	steps[1].status, steps[2].status = "completed", "in_progress"
	s.todos(s.after(s.gap(0.3)), steps)
	if o == failed {
		s.fail(s.after(s.gap(2)), "API Error: 529 Overloaded")
		return nil
	}
	if s.r.IntN(3) == 0 { // a failing check, then a fix
		s.shell(s.after(s.gap(1)), 6*time.Second, tk.test, 1)
		s.edit(s.after(s.gap(1.5)), tk.files[0], 3, 1)
		added, removed = added+3, removed+1
	}
	if s.agent != cursor && s.r.IntN(2) == 0 { // asks before running the checks
		s.wait(s.after(s.gap(0.5)), tk.test)
		s.after(s.gap(5))
	}
	s.shell(s.after(s.gap(0.2)), 5*time.Second, tk.test, 0)
	steps[2].status = "completed"
	s.todos(s.after(s.gap(0.3)), steps)
	s.usage(s.after(s.gap(1)), tk.summary, s.tokens(9000), s.tokens(700), s.tokens(52000))
	if s.agent == codex {
		s.limit5h = min(s.limit5h+3+5*s.r.Float64(), 97)
		s.codexLimits(s.now, s.limit5h, 30+s.limit5h/4)
	}
	pr := 0
	if (o == merged || o == prOpen) && s.agent == claude {
		pr = s.prs.next()
		s.pr(s.after(s.gap(1)), pr)
	}
	s.stop(s.after(s.gap(0.5)), tk.summary)
	if o == review || o == prOpen {
		return nil
	}
	commit := git{at: s.now.Add(s.gap(6)), s: s, sha: fmt.Sprintf("%07x", s.r.Uint32()&0xfffffff), message: tk.prompt,
		files: tk.files, added: added, removed: removed}
	if s.r.IntN(4) == 0 {
		commit.attribution = "likely"
	}
	out := []git{commit}
	if pr != 0 {
		out = append(out, git{at: commit.at.Add(time.Duration(1+s.r.IntN(4)) * time.Hour), s: s, pr: pr})
	}
	return out
}

// counter hands out PR numbers.
type counter struct{ n int }

func (c *counter) next() int { c.n++; return c.n }

// models are the models each agent uses in the demo.
var models = map[string]string{claude: "claude-sonnet-5-5", codex: "gpt-5.6-terra", cursor: "claude-opus-5-5", gemini: "gemini-3.8-flash"}

// script seeds sessions from one seeded random source, so a demo looks
// the same every time (screenshots, tests).
type script struct {
	w        *writer
	r        *rand.Rand
	prs      counter
	sessions []*session
	git      []git
}

func newScript(w *writer) *script {
	return &script{w: w, r: rand.New(rand.NewPCG(117, 2026)), prs: counter{n: 140}}
}

// session starts a session at `at`; pace is the typical time per step.
func (sc *script) session(agent string, p *project, model string, at time.Time, pace time.Duration) *session {
	id := fmt.Sprintf("demo-%s-%04d", strings.SplitN(agent, "-", 2)[0], len(sc.sessions)+1)
	s := newSession(sc.w, agent, id, p, model)
	s.now, s.pace, s.r, s.prs, s.limit5h = at, pace, sc.r, &sc.prs, 20
	sc.sessions = append(sc.sessions, s)
	s.start(at)
	return s
}

func (sc *script) work(s *session, tk task, o outcome) { sc.git = append(sc.git, s.work(tk, o)...) }

func (sc *script) pick(p *project) task { return p.tasks[sc.r.IntN(len(p.tasks))] }

// history seeds a week of finished work, up to yesterday evening.
func (sc *script) history(now time.Time) {
	agents := []string{claude, claude, claude, codex, codex, cursor, gemini}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for d := 7; d >= 1; d-- {
		start := today.AddDate(0, 0, -d).Add(9 * time.Hour)
		for i := range 2 + sc.r.IntN(3) {
			at := start.Add(time.Duration(i*150+sc.r.IntN(90)) * time.Minute)
			agent := agents[sc.r.IntN(len(agents))]
			p := projects[sc.r.IntN(len(projects))]
			o := committed
			switch x := sc.r.IntN(20); {
			case x < 4:
				o = merged
			case x < 6:
				o = answered
			case x < 8 && (agent == claude || agent == cursor): // the agents that report errors
				o = failed
			}
			s := sc.session(agent, p, models[agent], at, 40*time.Second)
			sc.work(s, sc.pick(p), o)
			if o != failed && sc.r.IntN(2) == 0 { // a follow-up in the same session
				s.now = s.now.Add(time.Duration(5+sc.r.IntN(20)) * time.Minute)
				sc.work(s, sc.pick(p), committed)
			}
			s.end(s.after(time.Minute))
		}
	}
	// Two days ago Claude Code hit its 5-hour limit mid-task.
	s := sc.session(claude, projects[1], "claude-opus-5-5", today.AddDate(0, 0, -2).Add(16*time.Hour), 30*time.Second)
	sc.work(s, projects[1].tasks[2], answered)
	s.limitReached(s.after(time.Minute), s.now.Add(90*time.Minute))
	s.fail(s.after(time.Second), "You've hit your session limit")
	s.end(s.after(time.Minute))
}

// current seeds today's state besides the live sessions: something
// waiting, things to review, something failed, something shipped.
func (sc *script) current(now time.Time) {
	// Waiting on a permission prompt for a few minutes.
	s := sc.session(claude, projects[1], models[claude], now.Add(-6*time.Minute), 8*time.Second)
	tk := projects[1].tasks[3]
	s.prompt(s.after(0), tk.prompt)
	s.usage(s.after(s.gap(1)), "I'll add a middleware that sets the request id.", s.tokens(5000), s.tokens(400), s.tokens(20000))
	s.todos(s.after(s.gap(1)), []todo{{"Add the request id middleware", "completed"}, {"Log the id", "in_progress"}, {"Run the tests", "pending"}})
	s.search(s.after(s.gap(1)), tk.pattern)
	s.edit(s.after(s.gap(2)), tk.files[0], 18, 3)
	s.wait(now.Add(-3*time.Minute), tk.test)

	// Asking a question, and done but waiting for the next prompt.
	s = sc.session(claude, projects[3], models[claude], now.Add(-12*time.Minute), 8*time.Second)
	tk = projects[3].tasks[0]
	s.prompt(s.after(0), tk.prompt)
	s.search(s.after(s.gap(1)), tk.pattern)
	s.read(s.after(s.gap(1)), tk.files[0])
	s.usage(s.after(s.gap(1)), "Should the quickstart use the sandbox or live keys?", s.tokens(4000), s.tokens(300), s.tokens(18000))
	s.notify(now.Add(-8*time.Minute), "elicitation_dialog", "Claude has a question for you")
	s = sc.session(claude, projects[2], models[claude], now.Add(-25*time.Minute), 10*time.Second)
	tk = projects[2].tasks[1]
	s.prompt(s.after(0), "Why does the sign-in form jump when the keyboard opens?")
	s.read(s.after(s.gap(1)), tk.files[0])
	s.usage(s.after(s.gap(2)), "The form isn't inside a keyboard-avoiding view, so it doesn't move up.", s.tokens(5000), s.tokens(600), s.tokens(20000))
	s.stop(s.after(s.gap(0.5)), "The form isn't inside a keyboard-avoiding view.")
	s.notify(s.after(time.Minute), "idle_prompt", "Claude is waiting for your input")

	// Finished, not committed yet: Review.
	s = sc.session(cursor, projects[2], models[cursor], now.Add(-50*time.Minute), 30*time.Second)
	sc.work(s, projects[2].tasks[0], review)
	s = sc.session(claude, projects[0], "claude-opus-5-5", now.Add(-95*time.Minute), 30*time.Second)
	sc.work(s, projects[0].tasks[2], prOpen)

	// Stopped with an error: Failed.
	s = sc.session(claude, projects[2], models[claude], now.Add(-70*time.Minute), 25*time.Second)
	sc.work(s, projects[2].tasks[2], failed)

	// Shipped this morning, with a merged PR.
	s = sc.session(claude, projects[3], models[claude], now.Add(-5*time.Hour), 30*time.Second)
	sc.work(s, projects[3].tasks[1], merged)
	s.end(s.after(time.Minute))
}

// live starts the sessions that keep working while the demo runs, each
// with a turn finished earlier today.
func (sc *script) live(now time.Time) []*session {
	var out []*session
	for _, a := range []struct {
		agent string
		p     *project
		ago   time.Duration
	}{{claude, projects[0], 3 * time.Hour}, {codex, projects[1], 2 * time.Hour}, {gemini, projects[3], 4 * time.Hour}} {
		s := sc.session(a.agent, a.p, models[a.agent], now.Add(-a.ago), 30*time.Second)
		sc.work(s, a.p.tasks[len(a.p.tasks)-1], committed)
		out = append(out, s)
	}
	return out
}

// seed writes everything up to now. It returns the live sessions and the
// git events to store once the daemon has read the sessions.
func (sc *script) seed(now time.Time) (live []*session, events []model.Event, err error) {
	sc.history(now)
	sc.current(now)
	live = sc.live(now)
	for _, g := range sc.git {
		if g.at.Before(now) {
			events = append(events, g.event())
		}
	}
	for _, s := range sc.sessions {
		if s.err != nil {
			return nil, nil, s.err
		}
	}
	return live, events, nil
}
