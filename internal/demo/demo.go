// Package demo runs `shiplino demo`: a separate daemon with synthetic
// sessions, for a first look, screenshots and browser tests.
//
// The demo lives in a temporary folder that is deleted when it stops. It
// points the Shiplino home and the user home there, so it never reads or
// writes ~/.shiplino or the agents' real folders, and it registers no
// hooks or services. The data goes through the real pipeline: hook
// payloads in the spool and transcripts in each agent's own format, read
// by the adapters. Only commits and PR merges, which come from git and
// GitHub, are stored directly in the shape those collectors use.
package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/internal/daemon"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/model"
)

// Config is what the CLI passes to Run.
type Config struct {
	Version string
	Out     io.Writer
	// Open, if set, opens the board in a browser.
	Open func(url string) error
	// Ready, if set, is called once the board is seeded and serving.
	Ready func(url, home string)
	// Pace is the time per step of the live sessions (default 2.5s).
	Pace time.Duration
}

// Run starts the demo and blocks until ctx is cancelled, then deletes
// everything it wrote.
func Run(ctx context.Context, cfg Config) (err error) {
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Pace == 0 {
		cfg.Pace = 2500 * time.Millisecond
	}
	root, err := os.MkdirTemp("", "shiplino-demo-")
	if err != nil {
		return err
	}
	defer func() {
		if rerr := os.RemoveAll(root); rerr != nil {
			err = errors.Join(err, fmt.Errorf("delete the demo folder %s: %w", root, rerr))
		}
	}()
	home, userHome := filepath.Join(root, "shiplino"), filepath.Join(root, "home")
	for _, dir := range []string{home, userHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	// Everything that looks for the user's home (transcript discovery,
	// agent detection on the settings page) finds the demo's instead.
	defer setEnv(map[string]string{"SHIPLINO_HOME": home, "HOME": userHome, "USERPROFILE": userHome})()

	w := &writer{spool: spool.Dir(home), home: userHome}
	sc := newScript(w)
	live, events, err := sc.seed(time.Now())
	if err != nil {
		return fmt.Errorf("seed the demo: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		failure error
	)
	fail := func(err error) {
		mu.Lock()
		failure = errors.Join(failure, err)
		mu.Unlock()
		cancel()
	}
	ready := func(d *daemon.Daemon, board string) {
		// The live sessions start their first task now, so they show as
		// running from the start.
		for i, s := range live {
			s.live, s.pace, s.r = ctx, cfg.Pace, rand.New(rand.NewPCG(uint64(i), 117))
			s.prompt(time.Now(), s.project.tasks[0].prompt)
			s.prompted = true
			if s.err != nil {
				fail(fmt.Errorf("start the live sessions: %w", s.err))
				return
			}
		}
		// Read the seeded spool and transcripts now, so the board is
		// complete when it opens, then add what git and GitHub would.
		if err := d.Poll(ctx); err != nil {
			fail(fmt.Errorf("read the demo data: %w", err))
			return
		}
		if _, _, err := d.Ingest(ctx, events); err != nil {
			fail(fmt.Errorf("store the demo commits: %w", err))
			return
		}
		if err := backlog(ctx, board, home); err != nil {
			fail(fmt.Errorf("add the demo backlog: %w", err))
			return
		}
		ingest := func(evs []model.Event) error { _, _, err := d.Ingest(ctx, evs); return err }
		for _, s := range live {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := replay(s, ingest); err != nil && ctx.Err() == nil {
					fmt.Fprintf(cfg.Out, "demo: live replay stopped: %v\n", err)
				}
			}()
		}
		fmt.Fprintf(cfg.Out, "Shiplino demo: %s\n\n", board)
		fmt.Fprintln(cfg.Out, "  Synthetic sessions in four made-up projects, with agents working live.")
		fmt.Fprintln(cfg.Out, "  Your real data, agents and hooks aren't touched.")
		fmt.Fprintln(cfg.Out, "  Press Ctrl-C to stop; the demo data is deleted.")
		if cfg.Open != nil {
			if err := cfg.Open(board); err != nil {
				fmt.Fprintf(cfg.Out, "\nOpen %s in your browser.\n", board)
			}
		}
		if cfg.Ready != nil {
			cfg.Ready(board, home)
		}
	}
	err = daemon.Run(ctx, cfg.Version, daemon.Options{Demo: true, Git: fakeGit{}, User: "dev", Ready: ready})
	cancel()
	wg.Wait()
	if err := errors.Join(err, failure); err != nil {
		return err
	}
	fmt.Fprintln(cfg.Out, "Demo stopped; its data was deleted.")
	return nil
}

// replay keeps a live session working until its context ends: a task,
// a short stay in Review, the commit that moves it to Done, a pause, the
// next task.
func replay(s *session, ingest func([]model.Event) error) error {
	for i := 0; ; i++ {
		gits := s.work(s.project.tasks[i%len(s.project.tasks)], committed)
		for _, g := range gits {
			s.after(time.Until(g.at))
			if s.err != nil {
				break
			}
			if err := ingest([]model.Event{g.event()}); err != nil {
				return err
			}
		}
		s.after(s.gap(4))
		if s.err != nil {
			if errors.Is(s.err, context.Canceled) {
				return nil
			}
			return s.err
		}
	}
}

// backlog adds a few cards nobody has started, through the API like the
// web app does.
func backlog(ctx context.Context, board, home string) error {
	token, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return err
	}
	cards := []struct{ project, title string }{
		{"storefront", "Gift cards at checkout"},
		{"storefront", "Remember the last used shipping address"},
		{"billing-api", "Export invoices as CSV"},
		{"mobile-app", "Push notification when an order ships"},
	}
	for _, c := range cards {
		body, _ := json.Marshal(map[string]string{"title": c.title})
		u := board + "/api/v1/projects/" + url.PathEscape("example.com/acme/"+c.project) + "/cards"
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("POST %s: %s", u, resp.Status)
		}
	}
	return nil
}

// setEnv sets environment variables and returns a function that restores
// them.
func setEnv(vars map[string]string) func() {
	type old struct {
		v  string
		ok bool
	}
	saved := map[string]old{}
	for k, v := range vars {
		o, ok := os.LookupEnv(k)
		saved[k] = old{o, ok}
		os.Setenv(k, v)
	}
	return func() {
		for k, o := range saved {
			if o.ok {
				os.Setenv(k, o.v)
			} else {
				os.Unsetenv(k)
			}
		}
	}
}
