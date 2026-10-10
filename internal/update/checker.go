package update

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRestart is returned by the daemon after it installed an update, so
// the caller starts the new binary in its place.
var ErrRestart = errors.New("restarting into the updated binary")

// Checker is the daemon's opt-in update check ([update] check or
// auto_install). It asks GitHub once a day, records the answer for
// doctor, status and Settings, and installs the release itself only with
// auto_install.
type Checker struct {
	Home, Current, Channel string
	Source                 *Source
	Every                  time.Duration // 0 = 24h
	FirstDelay             time.Duration // 0 = 1 minute after start
	// Updater installs when set (auto_install); nil only notifies.
	Updater *Updater
	// HookTest checks an installed binary as a hook (HookTest); a failure
	// rolls the install back.
	HookTest func(ctx context.Context, bin string) error
	// Installed is called after a successful install; the daemon then
	// restarts into the new binary.
	Installed func(Result)
	Logf      func(format string, args ...any)
}

// Run checks until ctx ends or an update was installed.
func (c *Checker) Run(ctx context.Context) {
	every := c.Every
	if every <= 0 {
		every = 24 * time.Hour
	}
	first := c.FirstDelay
	if first <= 0 {
		first = time.Minute
	}
	// Restarts don't reset the clock: check when the last one is a day old.
	wait := first
	if last := LoadState(c.Home).CheckedAt; !last.IsZero() {
		if d := time.Until(last.Add(every)); d > wait {
			wait = d
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		if c.once(ctx) {
			return
		}
	}
}

// once checks, and installs with auto_install. It reports whether an
// update was installed.
func (c *Checker) once(ctx context.Context) bool {
	st, rel, err := Check(ctx, c.Home, c.Source, c.Channel, c.Current)
	if err != nil {
		c.logf("update check failed: %v", err)
		return false
	}
	newer := st.Newer(c.Current)
	if newer == "" {
		return false
	}
	if c.Updater == nil || newer == st.SkipAuto {
		c.logf("update available: %s (you have %s); run `shiplino update`", newer, c.Current)
		return false
	}
	c.logf("installing update %s (auto_install)", newer)
	res, err := c.Updater.Install(ctx, rel)
	if err != nil {
		st.InstallError = err.Error()
		_ = SaveState(c.Home, st)
		c.logf("auto-update to %s failed: %v", newer, err)
		return false
	}
	// Never break the agent: the new binary must pass the hook test
	// before the daemon restarts into it, or the old one goes back.
	if c.HookTest != nil {
		if err := c.HookTest(ctx, c.Updater.Target); err != nil {
			st.InstallError = fmt.Sprintf("%s failed the hook test (%v), so %s was put back", newer, err, c.Current)
			if _, rerr := c.Updater.Rollback(ctx); rerr != nil {
				st.InstallError += "; putting it back failed too: " + rerr.Error()
			}
			st.SkipAuto = newer // don't try this version again by itself
			_ = SaveState(c.Home, st)
			c.logf("auto-update: %s", st.InstallError)
			return false
		}
	}
	c.logf("updated %s → %s (signature %s)", res.From, res.To, res.Signature)
	if c.Installed != nil {
		c.Installed(res)
	}
	return true
}

func (c *Checker) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}
