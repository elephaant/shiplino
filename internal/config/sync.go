package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DefaultSyncEndpoint is the hosted sync service. Override it with
// [sync] endpoint (or `shiplino sync login --endpoint`).
const DefaultSyncEndpoint = "https://api.shiplino.com"

// Sync is the opt-in cloud sync (docs/sync-protocol.md). Nothing is sent
// unless Enabled is true, the user is signed in and a project is allowed.
type Sync struct {
	Enabled bool `toml:"enabled"`
	// Endpoint is the sync service base URL ("" = DefaultSyncEndpoint).
	Endpoint string `toml:"endpoint,omitempty"`
	// CaptureLevel is no longer used: sync sends metadata only, whatever
	// it says. Kept so older config files still load.
	CaptureLevel string `toml:"capture_level,omitempty"`
	// Projects allows project ids or globs ("github.com/acme/*", "*").
	// Empty means nothing syncs.
	Projects []string `toml:"projects"`
	// Exclude removes projects (ids or globs) the allow list matched.
	Exclude []string `toml:"exclude"`
	// SendTitles also sends session titles (redacted, 120 characters).
	// Off by default: a title can say what the work is about.
	SendTitles bool `toml:"send_titles"`
	// SendUser is no longer used: the OS user name is never sent. Kept so
	// older config files still load.
	SendUser bool `toml:"send_user,omitempty"`
}

// SyncIgnored lists [sync] settings that no longer have any effect, for
// `sync status` and doctor.
func (c Config) SyncIgnored() []string {
	var out []string
	if l := c.Sync.CaptureLevel; l != "" && l != "minimal" {
		out = append(out, fmt.Sprintf("capture_level = %q is ignored: sync sends metadata only", l))
	}
	if c.Sync.SendUser {
		out = append(out, "send_user is ignored: your OS user name is never sent")
	}
	return out
}

// SyncEndpoint returns the sync base URL without a trailing slash.
func (c Config) SyncEndpoint() string {
	if c.Sync.Endpoint == "" {
		return DefaultSyncEndpoint
	}
	return strings.TrimRight(c.Sync.Endpoint, "/")
}

func validateSync(s Sync) error {
	if s.Endpoint != "" {
		if err := CheckEndpoint(s.Endpoint); err != nil {
			return fmt.Errorf("sync.endpoint: %w", err)
		}
	}
	return nil
}

// CheckEndpoint accepts https URLs, and plain http only for loopback
// addresses (a local test server), so tokens never cross a network in
// the clear.
func CheckEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q isn't a base URL like https://sync.example.com", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("%q must use https (plain http only works for localhost)", raw)
}

const syncHeader = `[sync]
# Cloud sync, off until you run ` + "`shiplino sync login`" + `. These lines are
# rewritten by ` + "`shiplino sync login|logout|allow|deny`" + `.
# Nothing is sent until a project is allowed: projects lists project ids or
# globs ("github.com/acme/*", "*" for everything); exclude removes matches.
# Only metadata is sent: never prompts, replies, commands, tool output,
# diffs or file contents, and file paths are relative to the project.
# send_titles also sends session titles (off by default).
# Sync settings apply within seconds, without a daemon restart.
# Exactly what is sent: docs/sync-protocol.md, or ` + "`shiplino sync status --dry-run`" + `.
`

// UpdateSync changes the [sync] section of config.toml and leaves the
// rest of the file, comments included, as it was. The result must load
// cleanly, or nothing is written.
func UpdateSync(home string, change func(*Sync)) error {
	old, err := os.ReadFile(Path(home))
	if errors.Is(err, os.ErrNotExist) {
		old, err = []byte(defaultFile), nil
	}
	if err != nil {
		return err
	}
	c, err := Load(home)
	if err != nil {
		return err
	}
	change(&c.Sync)
	if c.Sync.Projects == nil {
		c.Sync.Projects = []string{}
	}
	if c.Sync.Exclude == nil {
		c.Sync.Exclude = []string{}
	}
	body, err := toml.Marshal(c.Sync)
	if err != nil {
		return err
	}
	section := syncHeader + string(body)

	lines := strings.SplitAfter(string(old), "\n")
	start, end := -1, len(lines)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if start < 0 && t == "[sync]" {
			start = i
		} else if start >= 0 && strings.HasPrefix(t, "[") {
			end = i
			break
		}
	}
	var out string
	if start < 0 {
		out = strings.TrimRight(string(old), "\n") + "\n\n" + section
	} else {
		rest := strings.Join(lines[end:], "")
		if rest != "" {
			section += "\n"
		}
		out = strings.Join(lines[:start], "") + section + rest
	}

	var check Config
	dec := toml.NewDecoder(bytes.NewReader([]byte(out)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&check); err != nil {
		return fmt.Errorf("%s: can't update [sync]: %w", Path(home), err)
	}
	if err := validateSync(check.Sync); err != nil {
		return err
	}
	return writeAtomic(Path(home), []byte(out))
}

// writeAtomic replaces a file without ever leaving it half written.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
