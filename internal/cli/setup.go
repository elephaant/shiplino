package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/agents"
	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/service"
	"github.com/elephaant/shiplino/internal/update"
	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
)

// env is what commands need from the outside world; tests replace it.
type env struct {
	out, errOut io.Writer
	home        string // Shiplino home (~/.shiplino)
	userHome    string // the user's home directory
	self        string // path of the running binary
	version     string
	svcRun      service.Runner                           // nil = real OS commands
	notifySend  func(context.Context, notify.Note) error // nil = the OS notifier
	openURL     func(string) error                       // nil = the default browser
	in          io.Reader                                // nil = os.Stdin
	upd         *update.Updater                          // nil = GitHub, cosign and real binaries
}

// browse opens a URL in the user's browser.
func (e *env) browse(url string) error {
	if e.openURL != nil {
		return e.openURL(url)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func (e *env) send(ctx context.Context, n notify.Note) error {
	if e.notifySend != nil {
		return e.notifySend(ctx, n)
	}
	return notify.Send(ctx, n)
}

func (e *env) serviceConfig() service.Config {
	return service.Config{Bin: e.binPath(), Home: e.home, UserHome: e.userHome, Run: e.svcRun}
}

func (e *env) binPath() string {
	name := "shiplino"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e.home, "bin", name)
}

func (e *env) backupDir(agent string) string { return filepath.Join(e.home, "backups", agent) }

// setup installs the binary, connects detected agents and checks the hook.
// --dry-run prints what it would change and writes nothing; --no-service
// leaves the daemon for the user to run. --statusline also wraps Claude
// Code's status line (opt-in; =minimal shows a short line of Shiplino's
// own when there is none), --no-statusline puts it back.
func setup(ctx context.Context, e *env, args []string) int {
	noService := hasFlag(args, "--no-service")
	sl := statusLineChoice(args)
	if hasFlag(args, "--dry-run") {
		return setupDryRun(ctx, e, noService, sl)
	}
	fmt.Fprintf(e.out, "Shiplino %s setup\n\n", e.version)

	for _, d := range []string{"bin", "spool", "data", "backups", "logs"} {
		if err := os.MkdirAll(filepath.Join(e.home, d), 0o700); err != nil {
			fmt.Fprintf(e.errOut, "cannot create %s: %v\n", filepath.Join(e.home, d), err)
			return 1
		}
	}
	bin := e.binPath()
	if err := installBinary(e.self, bin); err != nil {
		fmt.Fprintf(e.errOut, "cannot install the binary to %s: %v\n", bin, err)
		return 1
	}
	if err := config.WriteDefault(e.home); err != nil {
		fmt.Fprintf(e.errOut, "cannot create %s: %v\n", config.Path(e.home), err)
		return 1
	}
	if _, err := api.LoadToken(e.home); err != nil {
		fmt.Fprintf(e.errOut, "cannot create the API token: %v\n", err)
		return 1
	}

	ok := true
	connected := 0
	var notes []string
	for _, a := range agents.All {
		if a.OptIn && sl == slKeep {
			continue
		}
		found, version, path := a.Detect(ctx, e.userHome)
		if !found {
			fmt.Fprintf(e.out, "  ➖ %-20s not found\n", a.Name)
			continue
		}
		if a.OptIn {
			ok = setupStatusLine(e, a, path, bin, sl) && ok
			continue
		}
		changed, events, err := a.Install(path, bin, version, e.backupDir(a.ID))
		switch {
		case agents.IsUnparseable(err):
			ok = false
			fmt.Fprintf(e.out, "  ⚠️  %-20s %s isn't plain JSON (comments?), left untouched. Add the hooks by hand: see README.\n", a.Name, tilde(path, e.userHome))
		case err != nil:
			ok = false
			fmt.Fprintf(e.out, "  ❌ %-20s %v\n", a.Name, err)
		default:
			connected++
			what := "hooks already up to date"
			if changed {
				what = fmt.Sprintf("hooks added for %d events", events)
				if a.Note != "" {
					notes = append(notes, a.Note)
				}
			}
			name := a.Name
			if version != "" {
				name += " " + version
			}
			fmt.Fprintf(e.out, "  ✅ %-20s %s (%s)\n", name, what, tilde(path, e.userHome))
		}
	}

	if err := selfTest(ctx, e, bin); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ %-20s %v\n", "Hook test", err)
	} else {
		fmt.Fprintf(e.out, "  ✅ %-20s prints nothing, exits 0\n", "Hook test")
	}

	// Record the choice, so doctor knows a stopped daemon is expected.
	if err := config.SetAutostart(e.home, !noService); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ %-20s can't record the service choice: %v\n", "Config", err)
	}
	daemonURL := ""
	if noService {
		// An earlier setup may have registered it: undo that, so the
		// daemon runs only when the user starts it.
		_ = service.Uninstall(ctx, e.serviceConfig())
		fmt.Fprintf(e.out, "  ➖ %-20s not registered to start at login (--no-service)\n", "Daemon")
	} else if how, err := service.Install(ctx, e.serviceConfig()); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ %-20s %v\n", "Daemon", err)
	} else {
		wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		port, err := service.WaitHealthy(wctx, e.home, healthy)
		cancel()
		if err != nil {
			ok = false
			fmt.Fprintf(e.out, "  ❌ %-20s registered (%s) but not answering: %v\n", "Daemon", how, err)
		} else {
			daemonURL = fmt.Sprintf("http://localhost:%d", port)
			fmt.Fprintf(e.out, "  ✅ %-20s running at %s (%s)\n", "Daemon", daemonURL, how)
			// Fill the board with recent history right away.
			if n, err := runBackfill(ctx, e, 30); err == nil && n > 0 {
				fmt.Fprintf(e.out, "  ✅ %-20s importing %d past session%s from the last 30 days\n", "History", n, plural(n))
			}
		}
	}

	fmt.Fprintln(e.out)
	switch {
	case connected == 0:
		fmt.Fprintln(e.out, "No supported agents were connected. Install one and run `shiplino setup` again.")
	case daemonURL != "":
		fmt.Fprintf(e.out, "Nothing else to do. Open %s, then start any agent. Zero tokens used.\n", daemonURL)
		fmt.Fprintln(e.out, "Sessions already running pick up the hooks after a restart.")
	case noService:
		fmt.Fprintln(e.out, "Run `shiplino daemon` to record (keep it running in a terminal or your own process manager),")
		fmt.Fprintln(e.out, "then open http://localhost:4777. Until it runs, hook events wait in the spool and are recorded when it starts.")
		fmt.Fprintln(e.out, "To start it at login instead, run `shiplino setup` again without --no-service.")
	default:
		fmt.Fprintln(e.out, "Start the daemon with `shiplino daemon`, then open http://localhost:4777.")
	}
	for _, n := range notes {
		fmt.Fprintln(e.out, "Note: "+n+".")
	}
	if !ok {
		return 1
	}
	return 0
}

// setupDryRun prints what setup would do, with a diff of every agent
// config it would change. It writes nothing: no hooks, backups, binary,
// config, service or history import.
func setupDryRun(ctx context.Context, e *env, noService bool, sl slChoice) int {
	fmt.Fprintf(e.out, "Shiplino %s setup --dry-run: showing what would change, writing nothing\n\n", e.version)
	bin := e.binPath()
	ok := true
	for _, a := range agents.All {
		if a.OptIn && sl == slKeep {
			continue
		}
		found, version, path := a.Detect(ctx, e.userHome)
		if !found {
			fmt.Fprintf(e.out, "  ➖ %-20s not found\n", a.Name)
			continue
		}
		var changes []agents.Change
		var err error
		switch sl {
		case slRemove:
			if a.OptIn {
				changes, err = a.PreviewUninstall(path)
				ok = printPlan(e, a.Name, path, changes, err, "not installed") && ok
				continue
			}
		case slMinimal:
			if a.OptIn {
				a.Install = func(path, bin, _, backup string) (bool, int, error) {
					return agents.InstallStatusLine(path, bin, backup, true)
				}
			}
		}
		changes, err = a.PreviewInstall(path, bin, version)
		ok = printPlan(e, a.Name, path, changes, err, "hooks already up to date") && ok
	}
	fmt.Fprintln(e.out, "\nSetup would also:")
	fmt.Fprintf(e.out, "  • copy this binary to %s (the hooks above run it)\n", tilde(bin, e.userHome))
	if _, err := os.Stat(config.Path(e.home)); err != nil {
		fmt.Fprintf(e.out, "  • create %s and an API token\n", tilde(config.Path(e.home), e.userHome))
	}
	if noService {
		fmt.Fprintln(e.out, "  • not register the daemon (--no-service): you run `shiplino daemon` yourself")
	} else {
		fmt.Fprintln(e.out, "  • register the daemon to start at login, start it, and import the last 30 days of history")
	}
	fmt.Fprintln(e.out, "\nNothing was written. Run the same command without --dry-run to apply it.")
	if !ok {
		return 1
	}
	return 0
}

// slChoice is what setup does with the opt-in status line wrapper.
type slChoice int

const (
	slKeep    slChoice = iota // leave it as it is
	slInstall                 // --statusline
	slMinimal                 // --statusline=minimal
	slRemove                  // --no-statusline
)

func statusLineChoice(args []string) slChoice {
	switch {
	case hasFlag(args, "--no-statusline"):
		return slRemove
	case hasFlag(args, "--statusline=minimal"):
		return slMinimal
	case hasFlag(args, "--statusline"):
		return slInstall
	}
	return slKeep
}

// setupStatusLine installs or removes the status line wrapper and says
// what it did. It reports whether that worked.
func setupStatusLine(e *env, a agents.Hooks, path, bin string, sl slChoice) bool {
	var changed bool
	var err error
	if sl == slRemove {
		changed, err = a.Uninstall(path, e.backupDir(a.ID))
	} else {
		changed, _, err = agents.InstallStatusLine(path, bin, e.backupDir(a.ID), sl == slMinimal)
	}
	switch {
	case agents.IsUnparseable(err):
		fmt.Fprintf(e.out, "  ⚠️  %-20s %s isn't plain JSON (comments?), left untouched\n", a.Name, tilde(path, e.userHome))
		return false
	case err != nil:
		fmt.Fprintf(e.out, "  ❌ %-20s %v\n", a.Name, err)
		return false
	case sl == slRemove && changed:
		fmt.Fprintf(e.out, "  ✅ %-20s removed, your own status line command is back (%s)\n", a.Name, tilde(path, e.userHome))
	case sl == slRemove:
		fmt.Fprintf(e.out, "  ➖ %-20s not installed\n", a.Name)
	case changed:
		fmt.Fprintf(e.out, "  ✅ %-20s records plan usage; what it shows is unchanged (%s)\n", a.Name, tilde(path, e.userHome))
		if st, _ := claudecode.ReadStatusLine(path, e.userHome); st.Original == "" && !st.Minimal {
			fmt.Fprintf(e.out, "     %-20s you had no status line, so it's empty, and Claude Code hides its footer hints while\n", "")
			fmt.Fprintf(e.out, "     %-20s one is set (--statusline=minimal shows plan usage there; --no-statusline removes it)\n", "")
		}
	default:
		fmt.Fprintf(e.out, "  ✅ %-20s already up to date (%s)\n", a.Name, tilde(path, e.userHome))
	}
	return true
}

// printPlan prints one agent's dry-run result: a unified diff per file,
// or why there is none. It reports whether the change could be made.
func printPlan(e *env, name, path string, changes []agents.Change, err error, none string) bool {
	switch {
	case agents.IsUnparseable(err):
		fmt.Fprintf(e.out, "  ⚠️  %-20s %s isn't plain JSON (comments?): it would be left untouched\n", name, tilde(path, e.userHome))
		return false
	case err != nil && len(changes) == 0:
		fmt.Fprintf(e.out, "  ❌ %-20s %v\n", name, err)
		return false
	case len(changes) == 0:
		fmt.Fprintf(e.out, "  ✅ %-20s no changes: %s (%s)\n", name, none, tilde(path, e.userHome))
		return true
	}
	fmt.Fprintf(e.out, "  ✏️  %-20s would change %d file%s:\n\n", name, len(changes), plural(len(changes)))
	for _, c := range changes {
		fmt.Fprintln(e.out, c.Diff(tilde(c.Path, e.userHome)))
	}
	if err != nil {
		fmt.Fprintf(e.out, "  ⚠️  %-20s %v\n", name, err)
		return false
	}
	return true
}

// uninstall removes our hooks; with --purge it also deletes all data.
// --dry-run prints what it would remove and changes nothing.
func uninstall(ctx context.Context, e *env, args []string) int {
	purge := hasFlag(args, "--purge")
	if hasFlag(args, "--dry-run") {
		return uninstallDryRun(ctx, e, purge)
	}
	ok := true
	if err := service.Uninstall(ctx, e.serviceConfig()); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ Daemon       %v\n", err)
	} else {
		fmt.Fprintln(e.out, "  ✅ Daemon       stopped and removed from login items")
	}
	for _, a := range agents.All {
		_, _, path := a.Detect(ctx, e.userHome)
		changed, err := a.Uninstall(path, e.backupDir(a.ID))
		switch {
		case err != nil:
			ok = false
			fmt.Fprintf(e.out, "  ❌ %-12s %v\n", a.Name, err)
		case changed && a.OptIn:
			fmt.Fprintf(e.out, "  ✅ %-12s removed, your own status line is back (%s)\n", a.Name, tilde(path, e.userHome))
		case changed:
			fmt.Fprintf(e.out, "  ✅ %-12s hooks removed (%s)\n", a.Name, tilde(path, e.userHome))
		case a.OptIn:
			// Off by default: nothing worth a line.
		default:
			fmt.Fprintf(e.out, "  ➖ %-12s no Shiplino hooks found\n", a.Name)
		}
	}
	if purge {
		if err := os.RemoveAll(e.home); err != nil {
			ok = false
			fmt.Fprintf(e.out, "  ❌ could not delete %s: %v\n", e.home, err)
		} else {
			fmt.Fprintf(e.out, "  ✅ deleted %s\n", e.home)
		}
	} else {
		fmt.Fprintf(e.out, "\nYour data is kept in %s (use --purge to delete it).\n", e.home)
	}
	if !ok {
		return 1
	}
	return 0
}

// uninstallDryRun prints what uninstall would remove, writing nothing.
func uninstallDryRun(ctx context.Context, e *env, purge bool) int {
	fmt.Fprintf(e.out, "Shiplino %s uninstall --dry-run: showing what would change, writing nothing\n\n", e.version)
	ok := true
	for _, a := range agents.All {
		_, _, path := a.Detect(ctx, e.userHome)
		changes, err := a.PreviewUninstall(path)
		if err == nil && len(changes) == 0 {
			if !a.OptIn {
				fmt.Fprintf(e.out, "  ➖ %-20s no Shiplino hooks found\n", a.Name)
			}
			continue
		}
		ok = printPlan(e, a.Name, path, changes, err, "") && ok
	}
	fmt.Fprintln(e.out, "\nUninstall would also:")
	fmt.Fprintln(e.out, "  • stop the daemon and remove it from login items")
	if purge {
		fmt.Fprintf(e.out, "  • delete %s: every recorded session, backups and settings\n", e.home)
	} else {
		fmt.Fprintf(e.out, "  • keep your data in %s (--purge deletes it)\n", e.home)
	}
	fmt.Fprintln(e.out, "\nNothing was written. Run the same command without --dry-run to apply it.")
	if !ok {
		return 1
	}
	return 0
}

// installBinary copies the running binary to dst (atomically), unless it
// already runs from there. Hooks always point at dst, so updates replace
// it in place and agent configs never need editing again.
func installBinary(self, dst string) error {
	if s, err := filepath.EvalSymlinks(self); err == nil {
		self = s
	}
	if d, err := filepath.EvalSymlinks(dst); err == nil && d == self {
		return nil
	}
	src, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, src) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".shiplino-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// selfTest runs the installed hook exactly as an agent would and checks
// the zero-token contract: no output, exit 0, one spool line written.
func selfTest(ctx context.Context, e *env, bin string) error {
	return update.HookTest(ctx, e.home, bin)
}

// healthy reports whether a Shiplino daemon answers on the port.
func healthy(port int) bool {
	c := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func tilde(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
