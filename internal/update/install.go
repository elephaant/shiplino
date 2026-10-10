package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Updater installs a release over the binary the hooks point at.
type Updater struct {
	Source
	Target  string // ~/.shiplino/bin/shiplino(.exe): what hooks and the service run
	Current string // the running version
	GOOS    string // "" = runtime.GOOS
	GOARCH  string // "" = runtime.GOARCH
	// RequireSignature refuses to install when cosign isn't available.
	RequireSignature bool
	Verify           SignatureCheck                                        // nil = Cosign
	Smoke            func(ctx context.Context, bin string) (string, error) // nil = run `bin version`
	Log              func(format string, args ...any)                      // progress; nil = quiet
}

// Result says what an install or rollback did.
type Result struct {
	From, To  string
	Signature string // "verified", or why it wasn't checked
}

// ErrNotNewer means the release isn't newer than the running version.
var ErrNotNewer = errors.New("not newer than the installed version")

func (u *Updater) goos() string {
	if u.GOOS != "" {
		return u.GOOS
	}
	return runtime.GOOS
}

func (u *Updater) goarch() string {
	if u.GOARCH != "" {
		return u.GOARCH
	}
	return runtime.GOARCH
}

func (u *Updater) logf(format string, args ...any) {
	if u.Log != nil {
		u.Log(format, args...)
	}
}

// PreviousPath is where the replaced binary is kept for --rollback.
func PreviousPath(target string) string {
	ext := filepath.Ext(target)
	return strings.TrimSuffix(target, ext) + ".previous" + ext
}

// binaryName is the file name inside release archives.
func (u *Updater) binaryName() string {
	if u.goos() == "windows" {
		return "shiplino.exe"
	}
	return "shiplino"
}

// Install downloads, verifies and installs rel. It never installs a
// version that isn't newer than the running one (see Rollback).
func (u *Updater) Install(ctx context.Context, rel Release) (Result, error) {
	res := Result{From: u.Current, To: rel.Version.String()}
	if cur, ok := ParseVersion(u.Current); ok && !IsDev(u.Current) && Compare(rel.Version, cur) <= 0 {
		return res, fmt.Errorf("%s: %w %s", rel.Tag, ErrNotNewer, u.Current)
	}
	if _, err := os.Stat(u.Target); err != nil {
		return res, fmt.Errorf("%s: %w (run `shiplino setup` first)", u.Target, err)
	}
	dir := filepath.Dir(u.Target)
	unlock, err := lockDir(dir)
	if err != nil {
		return res, fmt.Errorf("can't update %s: %w", dir, err)
	}
	defer unlock()
	// Work next to the target so the final rename stays on one filesystem.
	work, err := os.MkdirTemp(dir, ".shiplino-update-")
	if err != nil {
		return res, fmt.Errorf("can't write to %s: %w", dir, err)
	}
	defer os.RemoveAll(work)

	archive := ArchiveName(rel.Version, u.goos(), u.goarch())
	asset := func(name string) (string, error) {
		if url, ok := rel.Assets[name]; ok {
			return url, nil
		}
		return "", fmt.Errorf("release %s has no %s", rel.Tag, name)
	}
	sumsURL, err := asset("checksums.txt")
	if err != nil {
		return res, err
	}
	archURL, err := asset(archive)
	if err != nil {
		return res, fmt.Errorf("%w (no build for %s/%s?)", err, u.goos(), u.goarch())
	}
	sumsPath := filepath.Join(work, "checksums.txt")
	if err := u.download(ctx, sumsURL, sumsPath, maxSmallBytes); err != nil {
		return res, err
	}

	// Signature first: a forged checksums.txt must not be trusted.
	verify := u.Verify
	if verify == nil {
		verify = Cosign
	}
	bundlePath := filepath.Join(work, "checksums.txt.sigstore.json")
	bundleURL, bundleErr := asset("checksums.txt.sigstore.json")
	if bundleErr == nil {
		bundleErr = u.download(ctx, bundleURL, bundlePath, maxSmallBytes)
	}
	switch err := verify(ctx, sumsPath, bundlePath, identityPrefix+rel.Tag); {
	case errors.Is(err, ErrNoCosign):
		if u.RequireSignature {
			return res, errors.New("[update] require_signature is on but cosign isn't installed; install cosign or turn it off")
		}
		res.Signature = "not checked (install cosign to also verify the Sigstore signature)"
	case bundleErr != nil:
		return res, fmt.Errorf("signature bundle unavailable for %s: %w; not installing", rel.Tag, bundleErr)
	case err != nil:
		return res, fmt.Errorf("signature check FAILED for %s checksums.txt; not installing: %w", rel.Tag, err)
	default:
		res.Signature = "verified (Sigstore, " + identityPrefix + rel.Tag + ")"
	}
	u.logf("Signature: %s", res.Signature)

	sums, err := os.ReadFile(sumsPath)
	if err != nil {
		return res, err
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return res, err
	}
	u.logf("Downloading %s…", archive)
	archPath := filepath.Join(work, archive)
	if err := u.download(ctx, archURL, archPath, maxArchiveBytes); err != nil {
		return res, err
	}
	got, err := fileSHA256(archPath)
	if err != nil {
		return res, err
	}
	if got != want {
		return res, fmt.Errorf("checksum mismatch for %s (expected %s, got %s); not installing", archive, want, got)
	}
	u.logf("Checksum verified.")

	newBin := filepath.Join(work, u.binaryName())
	if err := extractBinary(archPath, u.binaryName(), newBin); err != nil {
		return res, err
	}
	if err := os.Chmod(newBin, 0o755); err != nil {
		return res, err
	}
	// The new binary must run here and say it's the version we asked for.
	if err := u.checkVersion(ctx, newBin, rel.Version); err != nil {
		return res, err
	}
	if err := swap(newBin, u.Target, PreviousPath(u.Target), u.goos() == "windows"); err != nil {
		return res, fmt.Errorf("replacing %s: %w", u.Target, err)
	}
	return res, nil
}

// Rollback puts the previous binary back. The replaced one becomes the
// previous binary, so a second rollback undoes the first.
func (u *Updater) Rollback(ctx context.Context) (Result, error) {
	prev := PreviousPath(u.Target)
	if _, err := os.Stat(prev); err != nil {
		return Result{}, fmt.Errorf("no previous version to roll back to (%s is missing)", prev)
	}
	ver, err := u.smoke(ctx, prev)
	if err != nil {
		return Result{}, fmt.Errorf("the previous binary doesn't run: %w", err)
	}
	res := Result{From: u.Current, To: ver}
	unlock, err := lockDir(filepath.Dir(u.Target))
	if err != nil {
		return res, err
	}
	defer unlock()
	work, err := os.MkdirTemp(filepath.Dir(u.Target), ".shiplino-update-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	staged := filepath.Join(work, u.binaryName())
	if err := copyFile(prev, staged); err != nil {
		return res, err
	}
	if err := swap(staged, u.Target, prev, u.goos() == "windows"); err != nil {
		return res, fmt.Errorf("replacing %s: %w", u.Target, err)
	}
	return res, nil
}

func (u *Updater) checkVersion(ctx context.Context, bin string, want Version) error {
	out, err := u.smoke(ctx, bin)
	if err != nil {
		return fmt.Errorf("the downloaded binary doesn't run on this machine: %w", err)
	}
	if got, ok := ParseVersion(out); !ok || Compare(got, want) != 0 {
		return fmt.Errorf("the downloaded binary says it's %q, not %s; not installing", out, want)
	}
	return nil
}

// smoke runs `bin version` and returns the version it prints.
func (u *Updater) smoke(ctx context.Context, bin string) (string, error) {
	if u.Smoke != nil {
		return u.Smoke(ctx, bin)
	}
	return RunVersion(ctx, bin)
}

// RunVersion runs `bin version` and returns the version it prints.
func RunVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != "shiplino" {
		return "", fmt.Errorf("unexpected output %q", strings.TrimSpace(string(out)))
	}
	return f[1], nil
}
