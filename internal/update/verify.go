package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Release signing identity: GoReleaser signs checksums.txt keylessly from
// the release workflow (.goreleaser.yaml, .github/workflows/release.yml).
const (
	identityPrefix = "https://github.com/" + Repo + "/.github/workflows/release.yml@refs/tags/"
	oidcIssuer     = "https://token.actions.githubusercontent.com"
)

// ErrNoCosign means cosign isn't installed, so the signature can't be
// checked (the checksum still is).
var ErrNoCosign = errors.New("cosign isn't installed")

// SignatureCheck verifies a Sigstore bundle for checksums.txt against a
// certificate identity. It returns ErrNoCosign when it can't check.
type SignatureCheck func(ctx context.Context, checksums, bundle, identity string) error

// Cosign runs `cosign verify-blob` from PATH, like the installer does,
// but pinned to the exact tag being installed.
func Cosign(ctx context.Context, checksums, bundle, identity string) error {
	bin, err := exec.LookPath("cosign")
	if err != nil {
		return ErrNoCosign
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "verify-blob",
		"--bundle", bundle,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", oidcIssuer,
		checksums).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign verify-blob: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// checksumFor finds name's SHA-256 in a checksums.txt ("<hex>  <name>"
// lines). A missing or ambiguous entry is an error.
func checksumFor(sums []byte, name string) (string, error) {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		h := strings.ToLower(f[0])
		if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("checksums.txt has a malformed entry for %s", name)
		}
		if want != "" && want != h {
			return "", fmt.Errorf("checksums.txt lists %s twice with different sums", name)
		}
		want = h
	}
	if want == "" {
		return "", fmt.Errorf("%s isn't listed in checksums.txt", name)
	}
	return want, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary copies the one file called name at the top of the archive
// to dst. Nothing else is written, so entry names like "../x" or absolute
// paths can't escape; links and directories are skipped.
func extractBinary(archive, name, dst string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	err = copyEntry(archive, name, out)
	if err == nil {
		err = out.Sync() // durable before it's renamed into place
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}

func copyEntry(archive, name string, out io.Writer) error {
	limit := func(r io.Reader) error {
		n, err := io.Copy(out, io.LimitReader(r, maxArchiveBytes+1))
		if err == nil && n > maxArchiveBytes {
			err = errors.New("binary in the archive is too large")
		}
		return err
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return fmt.Errorf("reading %s: %w", archive, err)
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.Name != name || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return limit(rc)
		}
		return fmt.Errorf("the archive doesn't contain %s", name)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", archive, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("the archive doesn't contain %s", name)
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", archive, err)
		}
		if h.Name == name && h.Typeflag == tar.TypeReg {
			return limit(tr)
		}
	}
}
