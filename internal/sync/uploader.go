// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
)

const (
	// uploadEvery is the pause between batches (and between checks for
	// new events when there's no backlog).
	uploadEvery = 5 * time.Second
	// maxBackoff caps the wait after repeated failures.
	maxBackoff = 5 * time.Minute
	// credsEvery is how often stored credentials are re-read, to pick up
	// a `shiplino sync login` or logout from another process.
	credsEvery = 30 * time.Second
)

// errLogin means the service no longer accepts our tokens: they're
// deleted and the user has to sign in again.
var errLogin = errors.New("the sync service signed this device out: run `shiplino sync login`")

// errForbidden means the account may not sync to this workspace (e.g. it
// has a read-only role). The uploader checks again only every forbiddenWait.
var errForbidden = errors.New("this account's role in the workspace can't sync (ask a workspace admin)")

// forbiddenWait is how long to pause after a 403, in case the role changes.
const forbiddenWait = time.Hour

// Uploader sends recorded events to the sync service in the background.
// It reads committed rows only, through the store's read-only pool, on
// its own goroutine: it never blocks or slows recording. Offline, it
// keeps its cursor and retries with backoff.
type Uploader struct {
	home    string
	st      *store.Store
	log     *log.Logger
	version string
	vault   Vault
	now     func() time.Time
	every   time.Duration
	client  func(endpoint string) *Client

	mu         gosync.Mutex
	cfgRaw     []byte
	cfg        config.Config
	cfgErr     error
	creds      *Creds
	where, why string
	credsErr   error
	credsAt    time.Time
	failures   int
	notBefore  time.Time
	needsLogin bool
	forbidden  bool
}

// NewUploader returns an uploader for a Shiplino home. Call Run to start it.
func NewUploader(home string, st *store.Store, logger *log.Logger, version string) *Uploader {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Uploader{home: home, st: st, log: logger, version: version, vault: Vault{Home: home}, now: time.Now, every: uploadEvery,
		client: func(endpoint string) *Client { return NewClient(endpoint, version) }}
}

// Run uploads until ctx is cancelled. Nothing is sent unless sync is
// enabled, the device is signed in and at least one project is allowed.
func (u *Uploader) Run(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			t.Reset(u.Step(ctx))
		}
	}
}

// Step does one round of work and returns how long to wait before the
// next. Only Run's goroutine calls it; mu guards what View reads, and
// isn't held during network calls.
func (u *Uploader) Step(ctx context.Context) time.Duration {
	u.mu.Lock()
	u.reload()
	idle := u.cfgErr != nil || !u.cfg.Sync.Enabled || u.creds == nil || u.needsLogin
	wait := u.notBefore.Sub(u.now())
	u.mu.Unlock()
	if idle {
		return u.every
	}
	if wait > 0 {
		return wait
	}
	err := u.uploadOnce(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err == nil {
		u.failures, u.forbidden = 0, false
		return u.every
	}
	if ctx.Err() != nil {
		return u.every
	}
	u.recordError(ctx, err)
	switch {
	case errors.Is(err, errLogin):
		u.log.Printf("sync: %v", err)
		u.needsLogin = true
		if serr := u.vault.signOut(err.Error()); serr != nil {
			u.log.Printf("sync: %v", serr)
		} else {
			u.creds = nil
		}
		return u.every
	case errors.Is(err, errForbidden):
		u.log.Printf("sync: %v; checking again in %s", err, forbiddenWait)
		u.forbidden = true
		u.notBefore = u.now().Add(forbiddenWait)
		return forbiddenWait
	}
	u.failures++
	var se *StatusError
	var after time.Duration
	if errors.As(err, &se) {
		after = se.RetryAfter
	}
	wait = backoff(u.failures, after)
	u.notBefore = u.now().Add(wait)
	u.log.Printf("sync: %v (retrying in %s)", err, wait.Round(time.Second))
	return wait
}

// backoff is how long to wait after the n-th failure in a row: the
// service's Retry-After if it gave one, else exponential with jitter.
func backoff(n int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, time.Hour)
	}
	d := maxBackoff
	if n < 10 {
		d = min(uploadEvery<<(n-1), maxBackoff)
	}
	return d/2 + rand.N(d/2+1)
}

// reload re-reads config.toml when it changed, and, while sync is on, the
// credentials when the config changed or every credsEvery.
func (u *Uploader) reload() {
	raw, err := os.ReadFile(config.Path(u.home))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		u.cfgErr = err
		return
	}
	changed := u.cfgRaw == nil || !bytes.Equal(raw, u.cfgRaw)
	if changed {
		u.cfgRaw = append([]byte{}, raw...)
		u.cfg, u.cfgErr = config.Load(u.home)
	}
	if !u.cfg.Sync.Enabled {
		// Sync is off: don't even touch the keychain.
		u.creds, u.where, u.why, u.credsErr, u.credsAt = nil, "", "", nil, time.Time{}
		return
	}
	if changed || u.now().Sub(u.credsAt) >= credsEvery {
		old := u.creds
		u.creds, u.where, u.why, u.credsErr = u.vault.Load()
		u.credsAt = u.now()
		if u.creds == nil || old == nil || u.creds.AccessToken != old.AccessToken {
			u.needsLogin, u.forbidden, u.failures, u.notBefore = false, false, 0, time.Time{}
		}
	}
}

func (u *Uploader) recordError(ctx context.Context, err error) {
	st, serr := u.st.SyncState(ctx, u.creds.WorkspaceID)
	if serr != nil {
		return
	}
	st.LastError, st.LastErrorAt = err.Error(), u.now()
	_ = u.st.PutSyncState(ctx, st)
}

// uploadOnce sends the next batch, if any.
func (u *Uploader) uploadOnce(ctx context.Context) error {
	scope := ScopeFor(u.cfg)
	st, err := u.st.SyncState(ctx, u.creds.WorkspaceID)
	if err != nil {
		return err
	}
	before := st
	items, last, err := nextItems(ctx, u.st, scope, &st)
	if err != nil {
		return err
	}
	if err := u.send(ctx, &st, items); err != nil {
		if st != before {
			_ = u.st.PutSyncState(ctx, st)
		}
		return err
	}
	if last > st.Cursor {
		st.Cursor = last
	}
	st.LastError, st.LastErrorAt = "", time.Time{}
	if st == before {
		return nil
	}
	return u.st.PutSyncState(ctx, st)
}

// nextItems reads the rows after the cursor and prepares the next batch.
// When the allow list grew, it starts over from the first event: the
// service ignores events it already has.
func nextItems(ctx context.Context, s *store.Store, scope Scope, st *store.SyncState) ([]item, int64, error) {
	if scope.widens(st.Scope) {
		st.Cursor = 0
	}
	st.Scope = scope.key()
	if len(scope.allow) == 0 {
		return nil, st.Cursor, nil // nothing allowed: don't move the cursor
	}
	rows, err := s.SyncRows(ctx, st.Cursor, MaxBatchEvents)
	if err != nil {
		return nil, 0, fmt.Errorf("read events: %w", err)
	}
	items, last := scope.Prepare(rows)
	return items, last, nil
}

// send uploads items in order, moving the cursor after each accepted
// request. A 413 splits the batch in half; a single event the service
// still refuses is skipped (and logged) so it can't block the rest.
func (u *Uploader) send(ctx context.Context, st *store.SyncState, items []item) error {
	if len(items) == 0 {
		return nil
	}
	res, err := u.post(ctx, items)
	if IsStatus(err, 413) {
		if len(items) == 1 {
			u.log.Printf("sync: the service refused one event as too large; skipped it")
			st.Cursor = items[0].rowid
			return u.st.PutSyncState(ctx, *st)
		}
		mid := len(items) / 2
		if err := u.send(ctx, st, items[:mid]); err != nil {
			return err
		}
		return u.send(ctx, st, items[mid:])
	}
	if err != nil {
		return err
	}
	st.Cursor = items[len(items)-1].rowid
	st.Uploaded += int64(res.Accepted) // duplicates were already counted
	if res.Rejected > 0 {
		// Dropped by the service's schema check: resending can't help,
		// so they're counted and passed, never retried.
		st.Rejected += int64(res.Rejected)
		u.log.Printf("sync: the service rejected %d event(s) as invalid", res.Rejected)
	}
	st.LastUpload = u.now()
	// Save now: a crash before the next save would only resend this
	// batch, and the service ignores duplicates anyway.
	return u.st.PutSyncState(ctx, *st)
}

// post sends one request, refreshing the access token when it expired
// or the service answers 401.
func (u *Uploader) post(ctx context.Context, items []item) (Result, error) {
	if !u.creds.ExpiresAt.IsZero() && u.now().After(u.creds.ExpiresAt.Add(-time.Minute)) {
		if err := u.refresh(ctx); err != nil {
			return Result{}, err
		}
	}
	b := Batch{DeviceID: DeviceID(u.home), DeviceName: deviceName(), Events: make([]json.RawMessage, len(items))}
	for i, it := range items {
		b.Events[i] = it.body
	}
	c := u.client(u.creds.Endpoint)
	res, err := c.Upload(ctx, u.creds.AccessToken, b)
	if !IsStatus(err, 401) {
		return res, classify(err)
	}
	if err := u.refresh(ctx); err != nil {
		return Result{}, err
	}
	res, err = c.Upload(ctx, u.creds.AccessToken, b)
	return res, classify(err)
}

// classify turns upload answers that need special handling into errors
// Step recognizes.
func classify(err error) error {
	switch {
	case IsStatus(err, 401):
		return errLogin
	case IsStatus(err, 403):
		return errForbidden
	case IsStatus(err, 400):
		// A malformed request is a client bug: back off like any failure
		// rather than retry in a tight loop.
		return fmt.Errorf("the service refused the request as malformed (please report this): %w", err)
	}
	return err
}

func (u *Uploader) refresh(ctx context.Context) error {
	if u.creds.RefreshToken == "" {
		return errLogin
	}
	t, err := u.client(u.creds.Endpoint).Refresh(ctx, u.creds.RefreshToken)
	if IsStatus(err, 400, 401, 403) { // e.g. 400 invalid_grant (RFC 6749)
		return errLogin
	}
	if err != nil {
		return fmt.Errorf("refresh the sign-in: %w", err)
	}
	c := FromToken(u.creds.Endpoint, t, u.now())
	c.Account, c.Role = u.creds.Account, u.creds.Role
	if c.RefreshToken == "" {
		c.RefreshToken = u.creds.RefreshToken
	}
	where, why, err := u.vault.Save(c)
	if err != nil {
		return fmt.Errorf("save the refreshed sign-in: %w", err)
	}
	u.mu.Lock()
	u.creds, u.where, u.why = c, where, why
	u.mu.Unlock()
	return nil
}

// View describes the uploader's state for the settings page.
func (u *Uploader) View(ctx context.Context) View {
	u.mu.Lock()
	if u.cfgRaw == nil {
		u.reload() // before the first Step
	}
	cfg, c, where, why, cfgErr, credsErr := u.cfg, u.creds, u.where, u.why, u.cfgErr, u.credsErr
	needsLogin, forbidden, notBefore := u.needsLogin, u.forbidden, u.notBefore
	u.mu.Unlock()
	v := Describe(ctx, u.home, cfg, c, where, why, u.st)
	if cfgErr != nil {
		v.LastError = cfgErr.Error()
	} else if credsErr != nil {
		v.LastError = credsErr.Error()
	}
	v.NeedsLogin = v.NeedsLogin || needsLogin
	v.Forbidden = v.Forbidden || forbidden
	if notBefore.After(u.now()) {
		v.NextRetry = notBefore
	}
	return v
}

// DeviceID is this machine's stable sync id, created on first use.
func DeviceID(home string) string {
	p := filepath.Join(home, "device_id")
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
		return strings.TrimSpace(string(b))
	}
	id := model.NewULID(time.Now())
	_ = os.MkdirAll(home, 0o700)
	_ = os.WriteFile(p, []byte(id+"\n"), 0o600)
	return id
}

func deviceName() string {
	n, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return n
}
