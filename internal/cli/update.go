package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/service"
	"github.com/elephaant/shiplino/internal/update"
)

const updateUsage = `Usage:
  shiplino update                check for a newer release and install it
  shiplino update --check        only check, and say what's available
  shiplino update --rollback     go back to the version before the last update
Flags:
  --channel stable|prerelease    override [update] channel for this run
  --force                        replace a development build with a release
`

// updater builds the updater for this machine; tests swap the parts that
// reach the network, cosign or a real binary.
func (e *env) updater(cfg config.Config) *update.Updater {
	u := update.Updater{}
	if e.upd != nil {
		u = *e.upd
	}
	u.Target, u.Current, u.RequireSignature = e.binPath(), e.version, cfg.Update.RequireSignature
	u.UserAgent = "shiplino/" + e.version
	u.Log = func(format string, args ...any) { fmt.Fprintf(e.out, "  "+format+"\n", args...) }
	return &u
}

// updateCmd checks for, installs or rolls back releases (docs/updating.md).
func updateCmd(ctx context.Context, e *env, args []string) int {
	channel, args := flagValue(args, "--channel")
	for _, a := range args {
		if a != "--check" && a != "--rollback" && a != "--force" {
			fmt.Fprintf(e.errOut, "unknown argument %q\n\n%s", a, updateUsage)
			return 2
		}
	}
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if channel == "" {
		channel = cfg.Update.Channel
	}
	u := e.updater(cfg)
	if hasFlag(args, "--rollback") {
		return rollback(ctx, e, u)
	}
	checkOnly := hasFlag(args, "--check")
	if update.IsDev(e.version) && !checkOnly && !hasFlag(args, "--force") {
		fmt.Fprintf(e.errOut, "This is a development build (%s), so it isn't updated from releases.\nInstall a release with the installer, or run `shiplino update --force` to replace it with the newest one.\n", e.version)
		return 1
	}
	if !checkOnly && refuseManaged(e) {
		return 1
	}
	pre, err := u.PrereleasesWanted(channel, e.version)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 2
	}
	label := "stable"
	if pre {
		label = "prerelease"
	}
	st, rel, err := update.Check(ctx, e.home, &u.Source, channel, e.version)
	if err != nil {
		fmt.Fprintf(e.errOut, "Can't check for updates: %v\n", err)
		return 1
	}
	if rel.Tag == "" {
		fmt.Fprintf(e.out, "No %s release found.\n", label)
		return 0
	}
	newer := st.Newer(e.version)
	if newer == "" && !update.IsDev(e.version) {
		fmt.Fprintf(e.out, "Shiplino %s is up to date (newest %s release: %s).\n", e.version, label, rel.Version)
		return 0
	}
	if checkOnly {
		fmt.Fprintf(e.out, "Shiplino %s is available (you have %s): %s\nRun `shiplino update` to install it.\n", rel.Version, e.version, rel.URL)
		return 0
	}

	fmt.Fprintf(e.out, "Updating Shiplino %s → %s (%s)\n", e.version, rel.Version, label)
	res, err := u.Install(ctx, rel)
	if err != nil {
		fmt.Fprintf(e.errOut, "❌ Update failed, nothing was changed: %v\n", err)
		return 1
	}
	return afterSwap(ctx, e, u, res, "Updated to "+res.To)
}

// rollback puts the binary from before the last update back.
func rollback(ctx context.Context, e *env, u *update.Updater) int {
	if refuseManaged(e) {
		return 1
	}
	res, err := u.Rollback(ctx)
	if err != nil {
		fmt.Fprintf(e.errOut, "❌ %v\n", err)
		return 1
	}
	// Don't let auto_install put the version back tomorrow.
	st := update.LoadState(e.home)
	st.SkipAuto = res.From
	_ = update.SaveState(e.home, st)
	return afterSwap(ctx, e, u, res, "Rolled back to "+res.To)
}

// afterSwap checks the new binary as a hook, restarts the daemon and says
// what happened. A binary that breaks the hook contract is rolled back.
func afterSwap(ctx context.Context, e *env, u *update.Updater, res update.Result, done string) int {
	bin := e.binPath()
	if err := selfTest(ctx, e, bin); err != nil {
		fmt.Fprintf(e.errOut, "❌ The new binary failed the hook test (%v); restoring %s\n", err, res.From)
		if _, rerr := u.Rollback(ctx); rerr != nil {
			fmt.Fprintf(e.errOut, "❌ Restoring failed too: %v. Reinstall with the installer.\n", rerr)
		}
		return 1
	}
	fmt.Fprintf(e.out, "  ✅ %-12s prints nothing, exits 0\n", "Hook test")
	ok := restartDaemon(ctx, e, res.To)
	fmt.Fprintf(e.out, "%s. The version before it is kept: `shiplino update --rollback` switches back.\n", done)
	if self, err := filepath.EvalSymlinks(e.self); err == nil {
		if b, err := filepath.EvalSymlinks(bin); err == nil && self != b {
			fmt.Fprintf(e.out, "Note: you ran %s; hooks and the daemon use %s, which was updated.\n", e.self, bin)
		}
	}
	if !ok {
		return 1
	}
	return 0
}

// restartDaemon restarts the background service and waits until it
// reports the new version.
func restartDaemon(ctx context.Context, e *env, want string) bool {
	how, err := service.Restart(ctx, e.serviceConfig())
	if errors.Is(err, service.ErrNotInstalled) {
		if c, cerr := e.client(); cerr == nil {
			var st statusResp
			if c.get(ctx, "/api/v1/status", &st) == nil && st.Version != want {
				fmt.Fprintf(e.out, "  ⚠️  %-12s still running %s: restart `shiplino daemon` to use %s\n", "Daemon", st.Version, want)
			}
		}
		return true
	}
	if err != nil {
		fmt.Fprintf(e.out, "  ❌ %-12s restart failed: %v (run `shiplino doctor --fix`)\n", "Daemon", err)
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if c, err := e.client(); err == nil {
			var st statusResp
			if c.get(wctx, "/api/v1/status", &st) == nil && st.Version == want {
				fmt.Fprintf(e.out, "  ✅ %-12s restarted (%s), running %s\n", "Daemon", how, want)
				return true
			}
		}
		select {
		case <-wctx.Done():
			fmt.Fprintf(e.out, "  ⚠️  %-12s restarted (%s) but doesn't report %s yet; see `shiplino doctor`\n", "Daemon", how, want)
			return true
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// refuseManaged stops updates of binaries a package manager owns.
func refuseManaged(e *env) bool {
	mgr, cmd := update.Managed(e.self)
	if mgr == "" {
		return false
	}
	fmt.Fprintf(e.errOut, "Shiplino was installed with %s (%s), so update it there: %s\nThen run `shiplino setup` so the hooks use the new version.\n", mgr, e.self, cmd)
	return true
}

// updateChecks reports update state for doctor. It never makes network
// calls: it reads the last check.
func updateChecks(e *env, cfg config.Config) []check {
	st := update.LoadState(e.home)
	hint := "set [update] check = true in " + tilde(config.Path(e.home), e.userHome) + " for a daily check"
	c := check{ok: true, name: "Updates"}
	switch newer := st.Newer(e.version); {
	case update.IsDev(e.version):
		c.detail = "development build; not updated from releases"
	case newer != "":
		c.warn, c.detail, c.fixHint = true, fmt.Sprintf("%s is available (you have %s)", newer, e.version), "shiplino update"
		if st.InstallError != "" {
			c.detail += "; installing it automatically failed: " + st.InstallError
			if st.SkipAuto == newer {
				c.detail += " (it won't be retried automatically)"
			}
		}
	case st.Error != "" && cfg.UpdateChecks():
		c.warn, c.detail, c.fixHint = true, "last check failed: "+st.Error, "shiplino update --check"
	case !cfg.UpdateChecks():
		c.detail = "automatic checks off (they ask api.github.com)"
		if !st.CheckedAt.IsZero() {
			c.detail += "; last checked " + ago(st.CheckedAt)
		}
		c.detail += "; " + hint + ", or run `shiplino update --check`"
	default:
		c.detail = "up to date, checked " + ago(st.CheckedAt)
		if st.CheckedAt.IsZero() {
			c.detail = "checks on; the first runs a minute after the daemon starts"
		}
		if cfg.Update.AutoInstall {
			c.detail += "; new releases install automatically"
		}
	}
	if mgr, cmd := update.Managed(e.self); mgr != "" {
		c.detail += fmt.Sprintf(" (installed with %s: %s)", mgr, cmd)
	}
	return []check{c}
}

// updateHint is the one line `shiplino status` adds when the last check
// found a newer release.
func updateHint(e *env) string {
	if v := update.LoadState(e.home).Newer(e.version); v != "" {
		return fmt.Sprintf("↑ Shiplino %s is available: run `shiplino update`", v)
	}
	return ""
}
