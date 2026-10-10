// Package update finds, verifies and installs Shiplino releases from
// GitHub (`shiplino update`, and the opt-in daily check in the daemon).
//
// Trust follows the installer (scripts/install.sh): the archive must match
// its SHA-256 in the release's checksums.txt, and when `cosign` is
// installed the Sigstore signature of checksums.txt must verify against
// this repository's release workflow for exactly that tag. Only HTTPS is
// used, the version must be newer than the running one, and only the
// binary is read from the archive.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "elephaant/shiplino"

// DefaultAPI is the GitHub REST endpoint of the repository.
const DefaultAPI = "https://api.github.com/repos/" + Repo

// Channels. Auto follows the running version: a prerelease build sees
// prereleases (and newer stable releases), a stable build sees stable
// releases only.
const (
	ChannelAuto       = ""
	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// Size limits for what is downloaded, so a bad server can't fill the disk.
const (
	maxAPIBytes     = 4 << 20
	maxSmallBytes   = 1 << 20
	maxArchiveBytes = 300 << 20
)

// Release is one published release.
type Release struct {
	Tag        string
	Version    Version
	Prerelease bool
	URL        string            // release page
	Assets     map[string]string // file name → download URL
}

// Source reads releases over HTTPS.
type Source struct {
	API       string       // "" = DefaultAPI
	HTTP      *http.Client // nil = a client with timeouts that refuses plain HTTP
	UserAgent string
}

// ErrNoRelease means no release matches the channel.
var ErrNoRelease = errors.New("no release found")

func (s *Source) client() *http.Client {
	c := s.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Minute}
	}
	// Never follow a redirect off HTTPS (GitHub redirects downloads to its
	// asset host; that's fine, a downgrade to http isn't).
	cc := *c
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to %s: HTTPS only", req.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &cc
}

// get fetches an HTTPS URL, at most limit bytes, into w.
func (s *Source) get(ctx context.Context, raw string, limit int64, w io.Writer) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("refusing %q: HTTPS only", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	ua := s.UserAgent
	if ua == "" {
		ua = "shiplino-update"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u.Redacted(), resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("GET %s: %w", u.Redacted(), err)
	}
	if n > limit {
		return fmt.Errorf("GET %s: larger than %d bytes", u.Redacted(), limit)
	}
	return nil
}

// download saves an HTTPS URL to path.
func (s *Source) download(ctx context.Context, raw, path string, limit int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = s.get(ctx, raw, limit, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Latest returns the newest release on the channel. current decides the
// auto channel.
func (s *Source) Latest(ctx context.Context, channel, current string) (Release, error) {
	api := s.API
	if api == "" {
		api = DefaultAPI
	}
	pr, err := s.PrereleasesWanted(channel, current)
	if err != nil {
		return Release{}, err
	}
	var buf bytes.Buffer
	if err := s.get(ctx, api+"/releases?per_page=30", maxAPIBytes, &buf); err != nil {
		return Release{}, fmt.Errorf("listing releases: %w", err)
	}
	var list []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		HTMLURL    string `json:"html_url"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(buf.Bytes(), &list); err != nil {
		return Release{}, fmt.Errorf("listing releases: %w", err)
	}
	var best Release
	found := false
	for _, r := range list {
		v, ok := ParseVersion(r.Tag)
		if r.Draft || !ok || (!pr && (r.Prerelease || v.Prerelease())) {
			continue
		}
		if found && Compare(v, best.Version) <= 0 {
			continue
		}
		best = Release{Tag: r.Tag, Version: v, Prerelease: r.Prerelease || v.Prerelease(), URL: r.HTMLURL, Assets: map[string]string{}}
		for _, a := range r.Assets {
			best.Assets[a.Name] = a.URL
		}
		found = true
	}
	if !found {
		return Release{}, ErrNoRelease
	}
	return best, nil
}

// PrereleasesWanted reports whether a channel includes prereleases.
func (s *Source) PrereleasesWanted(channel, current string) (bool, error) {
	switch channel {
	case ChannelStable:
		return false, nil
	case ChannelPrerelease:
		return true, nil
	case ChannelAuto:
		v, ok := ParseVersion(current)
		return !ok || v.Prerelease() || IsDev(current), nil
	}
	return false, fmt.Errorf("unknown update channel %q (want \"stable\" or \"prerelease\")", channel)
}

// ArchiveName is the release file for an OS and CPU, as GoReleaser names it.
func ArchiveName(v Version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("shiplino_%s_%s_%s%s", v.String(), goos, goarch, ext)
}
