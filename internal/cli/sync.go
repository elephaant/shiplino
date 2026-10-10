package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/internal/store"
	cloudsync "github.com/elephaant/shiplino/internal/sync"
	"github.com/elephaant/shiplino/pkg/projects"
	"github.com/elephaant/shiplino/pkg/redact"
)

const syncUsage = `usage:
  shiplino sync login [--endpoint URL]   sign this device in (opens your browser)
  shiplino sync status [--dry-run]       workspace, last upload, backlog, errors;
                                         --dry-run prints the next batch without sending it
  shiplino sync allow <project>          sync a project (id, name, glob, or . for this folder)
  shiplino sync deny <project>           stop syncing a project
  shiplino sync logout                   sign out and stop syncing
`

// syncCmd: opt-in cloud sync (docs/sync-protocol.md).
func syncCmd(ctx context.Context, e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.errOut, syncUsage)
		return 2
	}
	switch args[0] {
	case "login":
		return syncLogin(ctx, e, args[1:])
	case "logout":
		return syncLogout(e)
	case "status":
		return syncStatus(ctx, e, args[1:])
	case "allow", "deny":
		if len(args) != 2 {
			fmt.Fprint(e.errOut, syncUsage)
			return 2
		}
		return syncAllow(ctx, e, args[1], args[0] == "allow")
	}
	fmt.Fprint(e.errOut, syncUsage)
	return 2
}

func syncLogin(ctx context.Context, e *env, args []string) int {
	endpoint, rest := flagValue(args, "--endpoint")
	if len(rest) > 0 {
		fmt.Fprint(e.errOut, syncUsage)
		return 2
	}
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if endpoint == "" {
		endpoint = cfg.SyncEndpoint()
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if err := config.CheckEndpoint(endpoint); err != nil {
		fmt.Fprintln(e.errOut, "--endpoint:", err)
		return 2
	}
	c := cloudsync.NewClient(endpoint, e.version)
	tok, err := cloudsync.Login(ctx, c, func(d cloudsync.DeviceCode) {
		fmt.Fprintf(e.out, "To sign in, open %s and enter the code:\n\n    %s\n\n", d.VerificationURI, d.UserCode)
		link := d.VerificationURIComplete
		if link == "" {
			link = d.VerificationURI
		}
		if u, err := url.Parse(link); err == nil && config.CheckEndpoint(u.Scheme+"://"+u.Host) == nil && e.browse(link) == nil {
			fmt.Fprintln(e.out, "Opened your browser.")
		}
		fmt.Fprintln(e.out, "Waiting for you to approve… (Ctrl-C to cancel)")
	})
	if err != nil {
		fmt.Fprintln(e.errOut, "Sign-in failed:", err)
		return 1
	}
	creds := cloudsync.FromToken(endpoint, tok, time.Now())
	if me, err := c.Me(ctx, tok.AccessToken); err == nil {
		creds.Account, creds.Role = cloudsync.AccountName(me), cloudsync.Role(me)
	}
	where, why, err := cloudsync.Vault{Home: e.home}.Save(creds)
	if err != nil {
		fmt.Fprintln(e.errOut, "Signed in, but couldn't save the credentials:", err)
		return 1
	}
	err = config.UpdateSync(e.home, func(s *config.Sync) {
		s.Enabled = true
		s.Endpoint = ""
		if endpoint != config.DefaultSyncEndpoint {
			s.Endpoint = endpoint
		}
	})
	if err != nil {
		fmt.Fprintln(e.errOut, "Signed in, but couldn't turn sync on:", err)
		return 1
	}
	who := ""
	if creds.Account != "" {
		who = " as " + creds.Account
	}
	role := ""
	if creds.Role != "" {
		role = " (role: " + creds.Role + ")"
	}
	fmt.Fprintf(e.out, "\n✅ Signed in%s to workspace %s%s.\n", who, workspaceName(creds), role)
	printCredStore(e, where, why)
	cfg, _ = config.Load(e.home)
	if len(cfg.Sync.Projects) == 0 {
		fmt.Fprintln(e.out, "Nothing is sent until you allow a project: `shiplino sync allow <project>` (or `shiplino sync allow .` inside one).")
	}
	fmt.Fprintln(e.out, "See exactly what would be sent with `shiplino sync status --dry-run`.")
	return 0
}

func workspaceName(c *cloudsync.Creds) string {
	if c.WorkspaceName != "" {
		return c.WorkspaceName
	}
	return c.WorkspaceID
}

func printCredStore(e *env, where, why string) {
	if where == cloudsync.InFile {
		fmt.Fprintf(e.out, "⚠️  Credentials are in %s (readable only by you): %s.\n", tilde(cloudsync.Vault{Home: e.home}.CredsPath(), e.userHome), why)
	} else {
		fmt.Fprintln(e.out, "Credentials are in the OS keychain.")
	}
}

func syncLogout(e *env) int {
	if err := (cloudsync.Vault{Home: e.home}).Delete(); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if err := config.UpdateSync(e.home, func(s *config.Sync) { s.Enabled = false }); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	fmt.Fprintln(e.out, "Signed out. Sync is off; nothing more is sent. Data already uploaded stays in the workspace.")
	return 0
}

func syncStatus(ctx context.Context, e *env, args []string) int {
	dry := hasFlag(args, "--dry-run")
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	creds, where, why, err := cloudsync.Vault{Home: e.home}.Load()
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db"))
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	defer st.Close()

	if dry {
		if creds == nil {
			fmt.Fprintln(e.errOut, "Not signed in: nothing would be sent. Sign in with `shiplino sync login`.")
			return 1
		}
		b, err := cloudsync.NextBatch(ctx, e.home, cfg, creds, st)
		if err != nil {
			fmt.Fprintln(e.errOut, err)
			return 1
		}
		out, _ := json.MarshalIndent(b, "", "  ")
		fmt.Fprintln(e.out, string(out))
		level, _ := cfg.SyncLevel()
		fmt.Fprintf(e.errOut, "Dry run: the next batch has %d event%s (capture level %s; %s). Nothing was sent.\n", len(b.Events), plural(len(b.Events)), level, pathsNote(cfg))
		return 0
	}

	v := cloudsync.Describe(ctx, e.home, cfg, creds, where, why, st)
	switch {
	case v.Enabled && v.SignedIn:
		fmt.Fprintln(e.out, "● Sync is on")
	case v.SignedIn:
		fmt.Fprintln(e.out, "○ Sync is off (signed in; turn it on with `shiplino sync login`)")
	case v.NeedsLogin:
		fmt.Fprintf(e.out, "○ Signed out by the sync service, so nothing is sent: %s\n", v.LastError)
	default:
		fmt.Fprintln(e.out, "○ Sync is off: nothing leaves this machine (`shiplino sync login` to start)")
	}
	if v.SignedIn {
		who := ""
		if v.Account != "" {
			who = v.Account + " · "
		}
		role := ""
		if v.Role != "" {
			role = " (" + v.Role + ")"
		}
		fmt.Fprintf(e.out, "  %-14s %sworkspace %s%s\n", "Signed in", who, workspaceName(creds), role)
		fmt.Fprintf(e.out, "  %-14s %s\n", "Endpoint", v.Endpoint)
		store := "OS keychain"
		if v.CredentialStore == cloudsync.InFile {
			store = fmt.Sprintf("%s (0600 file: %s)", tilde(cloudsync.Vault{Home: e.home}.CredsPath(), e.userHome), v.CredentialNote)
		}
		fmt.Fprintf(e.out, "  %-14s %s\n", "Credentials", store)
	}
	level := v.CaptureLevel
	if v.LevelCapped {
		level += fmt.Sprintf(" (lowered from %s to match the local capture_level)", cfg.Sync.CaptureLevel)
	}
	fmt.Fprintf(e.out, "  %-14s %s (%s)\n", "Capture level", level, pathsNote(cfg))
	if len(v.Projects) == 0 {
		fmt.Fprintf(e.out, "  %-14s none allowed, so nothing is sent (`shiplino sync allow <project>`)\n", "Projects")
	} else {
		fmt.Fprintf(e.out, "  %-14s %s\n", "Projects", strings.Join(v.Projects, ", "))
	}
	if len(v.Exclude) > 0 {
		fmt.Fprintf(e.out, "  %-14s %s\n", "Excluded", strings.Join(v.Exclude, ", "))
	}
	if v.SignedIn {
		fmt.Fprintf(e.out, "  %-14s %s · %d events uploaded\n", "Last upload", ago(v.LastUpload), v.Uploaded)
		if v.Rejected > 0 {
			fmt.Fprintf(e.out, "  %-14s %d events the service refused as invalid (dropped, not retried)\n", "Rejected", v.Rejected)
		}
		if v.Forbidden {
			fmt.Fprintln(e.out, "  ⚠️  Uploads are paused: this account's role can't sync to the workspace. Ask an admin, then `shiplino sync login` again.")
		}
		fmt.Fprintf(e.out, "  %-14s %d events to check\n", "Backlog", v.Backlog)
		if v.LastError != "" {
			fmt.Fprintf(e.out, "  %-14s %s (%s)\n", "Last error", v.LastError, ago(v.LastErrorAt))
		}
		if v.Enabled {
			if c, err := e.client(); err != nil || c.get(ctx, "/api/v1/status", &statusResp{}) != nil {
				fmt.Fprintln(e.out, "  ⚠️  The daemon isn't running, so nothing uploads (`shiplino doctor`).")
			}
		}
	}
	return 0
}

// syncAllow adds a project to the allow list, or (deny) removes it,
// excluding it when a glob would still match.
func syncAllow(ctx context.Context, e *env, arg string, allow bool) int {
	id, known := resolveProject(ctx, e, arg)
	if id == "" {
		fmt.Fprintf(e.errOut, "%q isn't a project\n", arg)
		return 2
	}
	var after config.Sync
	err := config.UpdateSync(e.home, func(s *config.Sync) {
		if allow {
			if !slices.Contains(s.Projects, id) {
				s.Projects = append(s.Projects, id)
			}
			s.Exclude = slices.DeleteFunc(s.Exclude, func(p string) bool { return p == id })
		} else {
			s.Projects = slices.DeleteFunc(s.Projects, func(p string) bool { return p == id })
			if cloudsync.ScopeFor(config.Config{Sync: *s}).Allowed(id) && !slices.Contains(s.Exclude, id) {
				s.Exclude = append(s.Exclude, id)
			}
		}
		after = *s
	})
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	switch {
	case allow:
		fmt.Fprintf(e.out, "Allowed %s.", id)
		if !known && !strings.ContainsAny(id, "*?") {
			fmt.Fprint(e.out, " (No sessions recorded there yet.)")
		}
		if after.Enabled {
			fmt.Fprintln(e.out, " Its history uploads within a few seconds.")
		} else {
			fmt.Fprintln(e.out, " It syncs once you sign in with `shiplino sync login`.")
		}
	default:
		fmt.Fprintf(e.out, "%s won't be synced any more. Data already uploaded stays in the workspace.\n", id)
	}
	return 0
}

// resolveProject turns user input into a project id: "." is the current
// folder's project, a recorded project's name becomes its id, and ids
// and globs are taken as they are.
func resolveProject(ctx context.Context, e *env, arg string) (id string, known bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", false
	}
	if arg == "." {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		arg = projects.Detect(wd, gitwatch.Exec{}, e.userHome).ID
	}
	st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db"))
	if err != nil {
		return arg, false
	}
	defer st.Close()
	list, _ := st.Projects(ctx)
	var byName []string
	for _, p := range list {
		if p.ID == arg {
			return arg, true
		}
		if p.Name == arg {
			byName = append(byName, p.ID)
		}
	}
	if len(byName) == 1 {
		return byName[0], true
	}
	return arg, false
}

// syncChecks are doctor's findings about sync.
func syncChecks(e *env) []check {
	cfg, err := config.Load(e.home)
	if err != nil {
		return nil
	}
	if !cfg.Sync.Enabled {
		return []check{{ok: true, name: "Sync", detail: "off (nothing leaves this machine)"}}
	}
	vault := cloudsync.Vault{Home: e.home}
	creds, where, why, err := vault.Load()
	switch {
	case err != nil:
		return []check{{name: "Sync", detail: err.Error(), fixHint: "shiplino sync login"}}
	case creds == nil && vault.SignedOut() != "":
		return []check{{name: "Sync", detail: "signed out by the sync service; nothing is sent", fixHint: "shiplino sync login"}}
	case creds == nil:
		return []check{{name: "Sync", detail: "on, but not signed in", fixHint: "shiplino sync login"}}
	}
	var out []check
	detail := "on, workspace " + workspaceName(creds)
	if len(cfg.Sync.Projects) == 0 {
		out = append(out, check{ok: true, warn: true, name: "Sync", detail: detail + ", but no project is allowed, so nothing is sent", fixHint: "shiplino sync allow <project>"})
	} else {
		out = append(out, check{ok: true, name: "Sync", detail: fmt.Sprintf("%s, %d project rule%s", detail, len(cfg.Sync.Projects), plural(len(cfg.Sync.Projects)))})
	}
	if where == cloudsync.InFile {
		out = append(out, check{ok: true, warn: true, name: "Sync creds", detail: fmt.Sprintf("in %s, readable only by you (%s)", tilde(cloudsync.Vault{Home: e.home}.CredsPath(), e.userHome), why),
			fixHint: "install a Secret Service keyring (e.g. gnome-keyring) and run shiplino sync login again"})
	} else {
		out = append(out, check{ok: true, name: "Sync creds", detail: "in the OS keychain"})
	}
	if st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db")); err == nil {
		v := cloudsync.Describe(context.Background(), e.home, cfg, creds, where, why, st)
		st.Close()
		switch {
		case v.Forbidden:
			out = append(out, check{name: "Sync upload", detail: v.LastError, fixHint: "ask a workspace admin for a role that can sync"})
		case v.LastError != "":
			out = append(out, check{ok: true, warn: true, name: "Sync upload", detail: v.LastError + " (" + ago(v.LastErrorAt) + ")", fixHint: "shiplino sync status"})
		case !v.LastUpload.IsZero():
			out = append(out, check{ok: true, name: "Sync upload", detail: "last upload " + ago(v.LastUpload)})
		}
		if v.Rejected > 0 {
			out = append(out, check{ok: true, warn: true, name: "Sync upload", detail: fmt.Sprintf("%d events refused by the service as invalid (dropped)", v.Rejected)})
		}
	}
	return out
}

// pathsNote says how paths and the user name are sent at the sync level.
func pathsNote(cfg config.Config) string {
	level, _ := cfg.SyncLevel()
	if level == redact.Minimal {
		return "paths relative to the project, no user name"
	}
	if cfg.Sync.SendUser {
		return "full paths and your user name"
	}
	return "full paths, no user name"
}
