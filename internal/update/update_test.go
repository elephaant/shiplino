package update

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
)

func TestMain(m *testing.M) {
	// The test binary doubles as a fake shiplino for RunVersion.
	if v := os.Getenv("SHIPLINO_TEST_FAKE_VERSION"); v != "" && len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("shiplino", v)
		os.Exit(0)
	}
	// ...and as a hook: good writes the spool line, chatty prints.
	if mode := os.Getenv("SHIPLINO_TEST_FAKE_HOOK"); mode != "" && len(os.Args) > 1 && os.Args[1] == "hook" {
		if mode == "chatty" {
			fmt.Println("hello model")
			os.Exit(0)
		}
		f := spool.SessionFile(spool.Dir(os.Getenv("SHIPLINO_HOME")), "shiplino-selftest", "selftest")
		os.MkdirAll(filepath.Dir(f), 0o700)
		os.WriteFile(f, []byte("{}\n"), 0o600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCompare(t *testing.T) {
	// Each version is lower than the next (semver.org precedence example).
	order := []string{"0.1.0-alpha.2", "0.1.0-alpha.10", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "v1.0.1", "1.1.0", "2.0.0"}
	for i := 0; i+1 < len(order); i++ {
		a, ok1 := ParseVersion(order[i])
		b, ok2 := ParseVersion(order[i+1])
		if !ok1 || !ok2 || Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
			t.Errorf("%s < %s failed", order[i], order[i+1])
		}
	}
	for _, bad := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-a..b", "latest", "v1.x.0"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if v, _ := ParseVersion("v1.2.3+build.5"); v.String() != "1.2.3" {
		t.Errorf("build metadata kept: %s", v)
	}
	for v, dev := range map[string]bool{"0.0.0-dev": true, "0.1.0-SNAPSHOT-abc123": true, "garbage": true, "0.1.0-alpha.2": false, "1.0.0": false} {
		if IsDev(v) != dev {
			t.Errorf("IsDev(%q) = %v", v, !dev)
		}
	}
}

func TestLatestChannels(t *testing.T) {
	s := newServer(t,
		fakeRelease{tag: "v0.1.0-alpha.2", prerelease: true},
		fakeRelease{tag: "v0.3.0-alpha.1", prerelease: true},
		fakeRelease{tag: "v0.2.0"},
		fakeRelease{tag: "v9.9.9", draft: true},
		fakeRelease{tag: "nightly"},
	)
	src := s.source()
	for _, tc := range []struct{ channel, current, want string }{
		{"", "0.1.0-alpha.2", "0.3.0-alpha.1"}, // a prerelease build follows prereleases
		{"", "0.2.0", "0.2.0"},                 // a stable build sees stable only
		{"stable", "0.1.0-alpha.2", "0.2.0"},
		{"prerelease", "0.2.0", "0.3.0-alpha.1"},
	} {
		rel, err := src.Latest(context.Background(), tc.channel, tc.current)
		if err != nil || rel.Version.String() != tc.want {
			t.Errorf("channel %q from %s: got %s %v, want %s", tc.channel, tc.current, rel.Version, err, tc.want)
		}
	}
	if _, err := src.Latest(context.Background(), "nightly", "0.2.0"); err == nil {
		t.Error("unknown channel accepted")
	}
	empty := newServer(t, fakeRelease{tag: "v0.1.0-alpha.1", prerelease: true}).source()
	if _, err := empty.Latest(context.Background(), "stable", "0.2.0"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("no stable release: %v", err)
	}
}

func TestHTTPSOnly(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "[]") }))
	defer plain.Close()
	src := Source{API: plain.URL}
	if _, err := src.Latest(context.Background(), "", "0.1.0"); err == nil || !strings.Contains(err.Error(), "HTTPS only") {
		t.Fatalf("plain http API: %v", err)
	}
	// An HTTPS server redirecting to plain HTTP is refused too.
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/releases", http.StatusFound)
	}))
	defer redirect.Close()
	src = Source{API: redirect.URL, HTTP: redirect.Client()}
	if _, err := src.Latest(context.Background(), "", "0.1.0"); err == nil || !strings.Contains(err.Error(), "HTTPS only") {
		t.Fatalf("redirect to http: %v", err)
	}
}

func TestInstallAndRollback(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} { // windows = the rename-aside path
		t.Run(goos, func(t *testing.T) {
			s := newServer(t, release(t, "v0.2.0", "fake shiplino 0.2.0"), release(t, "v0.1.0", "fake shiplino 0.1.0"))
			u := installed(t, s, goos, "0.1.0", "fake shiplino 0.1.0")
			rel, err := u.Latest(context.Background(), "", u.Current)
			if err != nil {
				t.Fatal(err)
			}
			res, err := u.Install(context.Background(), rel)
			if err != nil {
				t.Fatal(err)
			}
			prev := PreviousPath(u.Target)
			if read(t, u.Target) != "fake shiplino 0.2.0" || read(t, prev) != "fake shiplino 0.1.0" {
				t.Fatalf("after install: target %q, previous %q", read(t, u.Target), read(t, prev))
			}
			if res.From != "0.1.0" || res.To != "0.2.0" || !strings.HasPrefix(res.Signature, "verified") {
				t.Fatalf("result %+v", res)
			}
			if goos == "windows" && !strings.HasSuffix(prev, "shiplino.previous.exe") {
				t.Fatalf("previous path %s", prev)
			}
			if runtime.GOOS != "windows" {
				if fi, _ := os.Stat(u.Target); fi.Mode().Perm() != 0o755 {
					t.Fatalf("mode %v", fi.Mode())
				}
			}

			// Rollback swaps them; a second rollback swaps back.
			u.Current = "0.2.0"
			res, err = u.Rollback(context.Background())
			if err != nil || res.To != "0.1.0" || read(t, u.Target) != "fake shiplino 0.1.0" || read(t, prev) != "fake shiplino 0.2.0" {
				t.Fatalf("rollback: %+v %v", res, err)
			}
			if _, err := u.Rollback(context.Background()); err != nil || read(t, u.Target) != "fake shiplino 0.2.0" {
				t.Fatalf("second rollback: %v", err)
			}
			assertClean(t, filepath.Dir(u.Target))
		})
	}
}

// assertClean checks that no temporary files are left next to the binary.
func assertClean(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestInstallRefuses(t *testing.T) {
	good := func(t *testing.T) fakeRelease { return release(t, "v0.2.0", "fake shiplino 0.2.0") }
	archive := ArchiveName(Version{Minor: 2}, "linux", "amd64")
	for _, tc := range []struct {
		name    string
		rel     func(t *testing.T) fakeRelease
		current string
		tweak   func(u *Updater)
		want    string
	}{
		{name: "older (downgrade)", current: "0.3.0", rel: good, want: "not newer"},
		{name: "same version", current: "0.2.0", rel: good, want: "not newer"},
		{name: "checksum mismatch", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			r.sums = strings.Repeat("ab", 32) + "  " + archive + "\n"
			return r
		}, want: "checksum mismatch"},
		{name: "not in checksums", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			r.sums = strings.Repeat("ab", 32) + "  other.tar.gz\n"
			return r
		}, want: "isn't listed"},
		{name: "malformed checksum", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			r.sums = "zz  " + archive + "\n"
			return r
		}, want: "malformed"},
		{name: "signature fails", rel: good, tweak: func(u *Updater) {
			u.Verify = func(context.Context, string, string, string) error { return errors.New("bad sig") }
		}, want: "signature check FAILED"},
		{name: "bundle missing", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			delete(r.files, "checksums.txt.sigstore.json")
			return r
		}, want: "signature bundle unavailable"},
		{name: "cosign required", rel: good, tweak: func(u *Updater) {
			u.RequireSignature = true
			u.Verify = func(context.Context, string, string, string) error { return ErrNoCosign }
		}, want: "require_signature"},
		{name: "wrong version inside", rel: func(t *testing.T) fakeRelease { return release(t, "v0.2.0", "fake shiplino 0.1.5") }, want: "says it's"},
		{name: "doesn't run", rel: func(t *testing.T) fakeRelease { return release(t, "v0.2.0", "MZ garbage") }, want: "doesn't run"},
		{name: "path traversal only", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			r.files[archive] = tarGz(t, []entry{{name: "../shiplino", body: "fake shiplino 0.2.0"}, {name: "/shiplino", body: "x"}, {name: "bin/shiplino", body: "x"}})
			return r
		}, want: "doesn't contain shiplino"},
		{name: "symlink entry", rel: func(t *testing.T) fakeRelease {
			r := good(t)
			r.files[archive] = tarGz(t, []entry{{name: "shiplino", typ: tar.TypeSymlink}})
			return r
		}, want: "doesn't contain shiplino"},
		{name: "no build for this platform", rel: good, tweak: func(u *Updater) { u.GOARCH = "riscv64" }, want: "no build for linux/riscv64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, tc.rel(t))
			cur := tc.current
			if cur == "" {
				cur = "0.1.0"
			}
			u := installed(t, s, "linux", cur, "fake shiplino "+cur)
			if tc.tweak != nil {
				tc.tweak(u)
			}
			rel, err := u.Latest(context.Background(), "", "0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			_, err = u.Install(context.Background(), rel)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if read(t, u.Target) != "fake shiplino "+cur {
				t.Fatal("target changed after a refused install")
			}
			if _, err := os.Stat(PreviousPath(u.Target)); err == nil {
				t.Fatal("previous binary written after a refused install")
			}
			assertClean(t, filepath.Dir(u.Target))
		})
	}
}

func TestInstallWithoutCosignStillChecksChecksum(t *testing.T) {
	s := newServer(t, release(t, "v0.2.0", "fake shiplino 0.2.0"))
	u := installed(t, s, "linux", "0.1.0", "fake shiplino 0.1.0")
	u.Verify = func(context.Context, string, string, string) error { return ErrNoCosign }
	rel, _ := u.Latest(context.Background(), "", u.Current)
	res, err := u.Install(context.Background(), rel)
	if err != nil || !strings.Contains(res.Signature, "not checked") {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestOneUpdateAtATime(t *testing.T) {
	s := newServer(t, release(t, "v0.2.0", "fake shiplino 0.2.0"))
	u := installed(t, s, "linux", "0.1.0", "fake shiplino 0.1.0")
	lock := filepath.Join(filepath.Dir(u.Target), ".shiplino-update.lock")
	os.WriteFile(lock, nil, 0o600)
	rel, _ := u.Latest(context.Background(), "", u.Current)
	if _, err := u.Install(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "another update is running") {
		t.Fatalf("concurrent update: %v", err)
	}
	// A lock left by a crash long ago is taken over.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(lock, old, old)
	if _, err := u.Install(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	assertClean(t, filepath.Dir(u.Target))
}

func TestRollbackWithoutPrevious(t *testing.T) {
	u := installed(t, newServer(t), "linux", "0.1.0", "fake shiplino 0.1.0")
	if _, err := u.Rollback(context.Background()); err == nil || !strings.Contains(err.Error(), "no previous version") {
		t.Fatalf("got %v", err)
	}
}

func TestRunVersion(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHIPLINO_TEST_FAKE_VERSION", "0.2.0")
	if v, err := RunVersion(context.Background(), self); err != nil || v != "0.2.0" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestManaged(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/homebrew/bin/shiplino":                                                                                         "Homebrew",
		"/usr/local/Cellar/shiplino/0.2.0/bin/shiplino":                                                                      "Homebrew",
		"/home/linuxbrew/.linuxbrew/bin/shiplino":                                                                            "Homebrew",
		`C:\Users\dev\scoop\apps\shiplino\current\shiplino.exe`:                                                              "Scoop",
		"/nix/store/abc-shiplino/bin/shiplino":                                                                               "Nix",
		"/usr/local/Caskroom/shiplino/0.2.0/shiplino":                                                                        "Homebrew",
		"/usr/lib/node_modules/shiplino/node_modules/@shiplino/linux-x64/bin/shiplino":                                       "npm",
		`C:\Users\dev\AppData\Roaming\npm\node_modules\shiplino\node_modules\@shiplino\win32-x64\bin\shiplino.exe`:           "npm",
		"/home/dev/.local/share/pnpm/global/5/.pnpm/@shiplino+linux-x64@0.2.0/node_modules/@shiplino/linux-x64/bin/shiplino": "pnpm",
		"/home/dev/.bun/install/global/node_modules/@shiplino/linux-x64/bin/shiplino":                                        "Bun",
		"/home/dev/.config/yarn/global/node_modules/@shiplino/linux-x64/bin/shiplino":                                        "Yarn",
		"/home/dev/.shiplino/bin/shiplino":                                                                                   "",
		"/usr/local/bin/shiplino":                                                                                            "",
	} {
		if runtime.GOOS != "windows" && strings.Contains(path, `\`) {
			path = strings.ReplaceAll(path, `\`, "/")
		}
		if got, _ := Managed(path); got != want {
			t.Errorf("Managed(%s) = %q, want %q", path, got, want)
		}
	}
	if _, cmd := Managed("/opt/homebrew/Caskroom/shiplino/0.2.0/shiplino"); cmd != "brew upgrade --cask shiplino" {
		t.Errorf("cask upgrade command = %q", cmd)
	}
}

func TestStateAndChecker(t *testing.T) {
	home := t.TempDir()
	s := newServer(t, release(t, "v0.2.0", "fake shiplino 0.2.0"))
	u := installed(t, s, "linux", "0.1.0", "fake shiplino 0.1.0")
	src := s.source()

	// Notify only: records the newer version, installs nothing.
	c := &Checker{Home: home, Current: "0.1.0", Source: &src}
	if c.once(context.Background()) {
		t.Fatal("installed without auto_install")
	}
	st := LoadState(home)
	if st.Newer("0.1.0") != "0.2.0" || st.URL == "" || st.CheckedAt.IsZero() || st.Newer("0.2.0") != "" || st.Newer("0.0.0-dev") != "" {
		t.Fatalf("state %+v", st)
	}

	// auto_install, but the user rolled back from 0.2.0: skipped.
	installedRes := Result{}
	c.Updater, c.Installed = u, func(r Result) { installedRes = r }
	st.SkipAuto = "0.2.0"
	SaveState(home, st)
	if c.once(context.Background()) || read(t, u.Target) != "fake shiplino 0.1.0" {
		t.Fatal("reinstalled a version the user rolled back from")
	}

	// auto_install installs and asks for a restart.
	st.SkipAuto = ""
	SaveState(home, st)
	if !c.once(context.Background()) || installedRes.To != "0.2.0" || read(t, u.Target) != "fake shiplino 0.2.0" {
		t.Fatalf("auto install: %+v", installedRes)
	}

	// A failed check keeps the last known release and records the error.
	s.Close()
	if _, _, err := Check(context.Background(), home, &src, "", "0.1.0"); err == nil {
		t.Fatal("check against a closed server succeeded")
	}
	if st := LoadState(home); st.Error == "" || st.Latest != "0.2.0" {
		t.Fatalf("after failure: %+v", st)
	}
}

func TestHookTest(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("SHIPLINO_TEST_FAKE_HOOK", "good")
	if err := HookTest(context.Background(), home, self); err != nil {
		t.Fatalf("good hook: %v", err)
	}
	if entries, _ := os.ReadDir(spool.Dir(home)); len(entries) != 0 {
		t.Fatalf("hook test left spool data: %v", entries)
	}
	t.Setenv("SHIPLINO_TEST_FAKE_HOOK", "chatty")
	if err := HookTest(context.Background(), home, self); err == nil || !strings.Contains(err.Error(), "printed output") {
		t.Fatalf("chatty hook: %v", err)
	}
}

func TestAutoInstallRollsBackWhenTheHookBreaks(t *testing.T) {
	home := t.TempDir()
	s := newServer(t, release(t, "v0.2.0", "fake shiplino 0.2.0"))
	u := installed(t, s, "linux", "0.1.0", "fake shiplino 0.1.0")
	src := s.source()
	restarted, tested := false, ""
	c := &Checker{Home: home, Current: "0.1.0", Source: &src, Updater: u,
		HookTest: func(_ context.Context, bin string) error {
			tested = read(t, bin)
			return errors.New("hook printed output")
		},
		Installed: func(Result) { restarted = true },
	}
	if c.once(context.Background()) || restarted {
		t.Fatal("restarted into a binary that failed the hook test")
	}
	if tested != "fake shiplino 0.2.0" {
		t.Fatalf("hook test ran on %q, not the new binary", tested)
	}
	if read(t, u.Target) != "fake shiplino 0.1.0" {
		t.Fatal("old binary not put back")
	}
	st := LoadState(home)
	if st.SkipAuto != "0.2.0" || !strings.Contains(st.InstallError, "failed the hook test") || st.Newer("0.1.0") != "0.2.0" {
		t.Fatalf("state %+v", st)
	}
	// Not retried by itself the next day.
	tested = ""
	if c.once(context.Background()) || tested != "" || read(t, u.Target) != "fake shiplino 0.1.0" {
		t.Fatal("retried a version that broke the hook")
	}
	if st := LoadState(home); !strings.Contains(st.InstallError, "failed the hook test") {
		t.Fatalf("failure forgotten after the next check: %+v", st)
	}
	assertClean(t, filepath.Dir(u.Target))
}

func TestCheckerWaitsForADayBetweenChecks(t *testing.T) {
	home := t.TempDir()
	SaveState(home, State{CheckedAt: time.Now()})
	src := Source{API: "https://127.0.0.1:1/never"}
	c := &Checker{Home: home, Current: "0.1.0", Source: &src, FirstDelay: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Run(ctx)
	if st := LoadState(home); st.Error != "" {
		t.Fatalf("checked again right after a restart: %+v", st)
	}
}
