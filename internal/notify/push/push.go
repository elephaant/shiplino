// Package push sends alerts to phone and team targets: a generic
// webhook (JSON, optionally signed), ntfy, Slack and Discord.
//
// Each target is an explicit opt-in (`shiplino notify add`), and what it
// receives is metadata only: Payload is the single place an alert
// becomes outgoing data, and its Keys are the whole list of fields.
// Prompts, session titles, commands, file names and the agent's own
// notification text never leave the machine, at any capture level.
//
// Delivery never blocks the daemon: each target has its own queue and
// goroutine. Requests time out after 10 seconds and are retried with
// backoff on network errors, 429 and 5xx. A target gets at most one
// message every Gap; alerts that arrive in between are merged into one
// message. The last result per target is kept for doctor and `shiplino
// notify list`.
package push

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/internal/notify"
)

// Settings are the [notify.push] part of config.toml.
type Settings struct {
	Targets []string // enabled kinds; each one's details are in the Vault
	Events  []string // events to send; empty means all but "done"
}

// DefaultEvents are sent when Settings.Events is empty. "done" is left
// out: on a phone or in a channel it's mostly noise.
var DefaultEvents = []string{notify.EventWaiting, notify.EventFailed, notify.EventBudget, notify.EventLimit, notify.EventDigest}

// Defaults for delivery; tests shorten them.
const (
	Gap      = 10 * time.Second // at most one message per target this often
	settle   = 2 * time.Second  // a burst arriving within this is one message
	timeout  = 10 * time.Second // per request
	queue    = 100              // alerts waiting per target; more are dropped and counted
	maxBatch = 20               // alerts in one message (the newest)
	maxWait  = time.Minute      // longest Retry-After honored
)

var backoff = []time.Duration{2 * time.Second, 10 * time.Second, 30 * time.Second}

// Pusher delivers alerts to the configured targets.
type Pusher struct {
	home, board string
	load        func() (Settings, error)
	stamp       func() time.Time // when the settings last changed
	log         *log.Logger
	client      *http.Client

	gap, settle time.Duration
	backoff     []time.Duration

	mu      sync.Mutex
	ctx     context.Context
	loaded  *time.Time // the config file time the targets were read at
	events  map[string]bool
	workers map[string]*worker
}

type worker struct {
	t      Target
	in     chan notify.Alert
	cancel context.CancelFunc
}

// New returns a pusher. board is the local board's base URL for links;
// load reads the settings and configPath is watched for changes, so
// `shiplino notify add|remove` apply without a restart.
func New(home, board, configPath string, load func() (Settings, error), logger *log.Logger) *Pusher {
	stamp := func() time.Time {
		fi, err := os.Stat(configPath)
		if err != nil {
			return time.Time{}
		}
		return fi.ModTime()
	}
	return &Pusher{home: home, board: board, load: load, stamp: stamp, log: logger, client: Client(),
		gap: Gap, settle: settle, backoff: backoff, workers: map[string]*worker{}}
}

// Client is the HTTP client for targets. It never follows redirects, so
// a payload can't be sent on to a host (or a plain-http URL) the user
// didn't choose.
func Client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Run loads the targets and keeps them current until ctx is cancelled.
func (p *Pusher) Run(ctx context.Context) {
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		p.reload()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Active reports whether any target is set up, so callers can skip work.
func (p *Pusher) Active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers) > 0
}

func (p *Pusher) reload() {
	stamp := p.stamp()
	p.mu.Lock()
	ctx, same := p.ctx, p.loaded != nil && stamp.Equal(*p.loaded)
	p.mu.Unlock()
	if ctx == nil || same {
		return
	}
	set, err := p.load()
	if err != nil {
		return // a broken config.toml: keep what runs; doctor reports it
	}
	events := set.Events
	if len(events) == 0 {
		events = DefaultEvents
	}
	want := map[string]bool{notify.EventTest: true}
	for _, e := range events {
		want[e] = true
	}
	// Read the keychain before taking the lock: it can be slow.
	vault := Vault{Home: p.home}
	var targets []Target
	for _, kind := range set.Targets {
		t, _, why, err := vault.Load(kind)
		switch {
		case err != nil:
			p.record(kind, Result{At: time.Now(), Error: err.Error()}, 0)
			continue
		case t == nil:
			msg := "enabled in config.toml, but its details aren't stored; run `shiplino notify add " + kind + "`"
			if why != "" {
				msg += " (" + why + ")"
			}
			p.record(kind, Result{At: time.Now(), Error: msg}, 0)
			continue
		}
		if err := t.Validate(); err != nil {
			p.record(kind, Result{At: time.Now(), Error: err.Error()}, 0)
			continue
		}
		targets = append(targets, *t)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.loaded, p.events = &stamp, want
	for kind, w := range p.workers {
		w.cancel()
		delete(p.workers, kind)
	}
	for _, t := range targets {
		wctx, cancel := context.WithCancel(ctx)
		w := &worker{t: t, in: make(chan notify.Alert, queue), cancel: cancel}
		p.workers[t.Kind] = w
		go p.run(wctx, w)
	}
}

// Push queues alerts for every target that wants them. It never blocks:
// a full queue drops the alert and counts it.
func (p *Pusher) Push(alerts ...notify.Alert) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range alerts {
		if !p.events[a.Event] {
			continue
		}
		for kind, w := range p.workers {
			select {
			case w.in <- a:
			default:
				p.record(kind, Result{}, 1)
			}
		}
	}
}

// run sends a target's alerts, merging bursts and keeping Gap between
// messages.
func (p *Pusher) run(ctx context.Context, w *worker) {
	var last time.Time
	for {
		var batch []notify.Alert
		select {
		case a := <-w.in:
			batch = append(batch, a)
		case <-ctx.Done():
			return
		}
		wait := p.settle
		if g := time.Until(last.Add(p.gap)); g > wait {
			wait = g
		}
		timer := time.NewTimer(wait)
	collect:
		for {
			select {
			case a := <-w.in:
				batch = append(batch, a)
			case <-timer.C:
				break collect
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		if len(batch) > maxBatch {
			batch = batch[len(batch)-maxBatch:]
		}
		res := p.deliver(ctx, w.t, batch)
		last = time.Now()
		if ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		p.record(w.t.Kind, res, 0)
		p.mu.Unlock()
		if !res.OK && p.log != nil {
			p.log.Printf("notify: %s delivery failed after %d attempts: %s", w.t.Kind, res.Attempts, res.Error)
		}
	}
}

// deliver sends one message, retrying what can succeed later.
func (p *Pusher) deliver(ctx context.Context, t Target, alerts []notify.Alert) Result {
	id := deliveryID()
	var res Result
	for attempt := 0; ; attempt++ {
		var retry time.Duration
		res, retry = send(ctx, p.client, t, alerts, p.board, id)
		res.Attempts = attempt + 1
		if res.OK || retry < 0 || attempt >= len(p.backoff) {
			return res
		}
		wait := p.backoff[attempt]
		if retry > wait {
			wait = min(retry, maxWait)
		}
		select {
		case <-ctx.Done():
			return res
		case <-time.After(wait):
		}
	}
}

// Send delivers alerts to a target once, for `shiplino notify test`.
func Send(ctx context.Context, t Target, alerts []notify.Alert, board string) Result {
	res, _ := send(ctx, Client(), t, alerts, board, deliveryID())
	res.Attempts = 1
	return res
}

// send makes one attempt. retry is -1 when trying again can't help, and
// otherwise the delay the server asked for (0 if none).
func send(ctx context.Context, c *http.Client, t Target, alerts []notify.Alert, board, id string) (Result, time.Duration) {
	now := time.Now()
	res := Result{At: now}
	var ps []map[string]any
	for _, a := range alerts {
		if pl := Payload(a, board); pl != nil {
			ps = append(ps, pl)
		}
	}
	if len(ps) == 0 {
		res.Error = "nothing to send"
		return res, -1
	}
	req, err := t.request(ps, now, id)
	if err != nil {
		res.Error = err.Error()
		return res, -1
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		res.Error = clean(err)
		return res, 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	res.Code = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.OK = true
		return res, -1
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.Error = fmt.Sprintf("HTTP %d: redirects aren't followed; use the final URL", resp.StatusCode)
		return res, -1
	}
	res.Error = "HTTP " + strconv.Itoa(resp.StatusCode)
	if msg := strings.Join(strings.Fields(string(b)), " "); msg != "" && !strings.ContainsAny(msg, "<") {
		res.Error += ": " + clip(msg, 120)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			return res, time.Duration(s) * time.Second
		}
		return res, 0
	}
	return res, -1
}

// clean drops the URL from a transport error: it can hold a token.
func clean(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

func deliveryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Result is the outcome of one delivery.
type Result struct {
	At       time.Time `json:"at"`
	OK       bool      `json:"ok"`
	Code     int       `json:"code,omitempty"`
	Error    string    `json:"error,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
}

// State is what is known about a target's deliveries.
type State struct {
	Last    Result    `json:"last"`
	LastOK  time.Time `json:"last_ok,omitzero"`
	Sent    int64     `json:"sent"`
	Failed  int64     `json:"failed"`
	Dropped int64     `json:"dropped"` // queue full
}

const statusFile = "notify-status.json"

// Status reads the delivery state of every target.
func Status(home string) map[string]State {
	out := map[string]State{}
	b, err := os.ReadFile(filepath.Join(home, statusFile))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

var statusMu sync.Mutex

// Record saves a delivery result (or dropped alerts) for a target.
func Record(home, kind string, res Result, dropped int64) {
	statusMu.Lock()
	defer statusMu.Unlock()
	all := Status(home)
	s := all[kind]
	s.Dropped += dropped
	if !res.At.IsZero() {
		s.Last = res
		switch {
		case res.OK:
			s.Sent++
			s.LastOK = res.At
		case res.Attempts > 0:
			s.Failed++
		}
	}
	all[kind] = s
	b, err := json.Marshal(all)
	if err == nil {
		_ = writeFile0600(filepath.Join(home, statusFile), b)
	}
}

// Forget removes a target's delivery state.
func Forget(home, kind string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	all := Status(home)
	if _, ok := all[kind]; !ok {
		return
	}
	delete(all, kind)
	if b, err := json.Marshal(all); err == nil {
		_ = writeFile0600(filepath.Join(home, statusFile), b)
	}
}

func (p *Pusher) record(kind string, res Result, dropped int64) {
	Record(p.home, kind, res, dropped)
}

// Valid reports whether kind is a target kind.
func Valid(kind string) bool { return slices.Contains(Kinds, kind) }
