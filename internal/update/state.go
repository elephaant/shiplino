package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is the result of the last update check, kept in
// ~/.shiplino/update.json for doctor, `shiplino status` and Settings.
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Channel   string    `json:"channel,omitempty"`
	Latest    string    `json:"latest,omitempty"` // newest release on the channel
	URL       string    `json:"url,omitempty"`    // its release page
	Error     string    `json:"error,omitempty"`  // why the last check failed
	// InstallError says why the daemon's own install (auto_install) of
	// Latest failed.
	InstallError string `json:"install_error,omitempty"`
	// SkipAuto is a version the user rolled back from: auto_install
	// doesn't install it again (it's still shown as available).
	SkipAuto string `json:"skip_auto,omitempty"`
}

// StatePath is where the last check is stored.
func StatePath(home string) string { return filepath.Join(home, "update.json") }

// LoadState reads the last check; a missing file is a zero State.
func LoadState(home string) State {
	var s State
	if b, err := os.ReadFile(StatePath(home)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveState writes the state atomically.
func SaveState(home string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".update-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), StatePath(home))
}

// Newer returns the newer release the state knows about, compared with
// the running version ("" when up to date, unknown, or a dev build).
func (s State) Newer(current string) string {
	latest, ok := ParseVersion(s.Latest)
	cur, cok := ParseVersion(current)
	if !ok || !cok || IsDev(current) || Compare(latest, cur) <= 0 {
		return ""
	}
	return latest.String()
}

// Check asks for the newest release and records the answer in home.
func Check(ctx context.Context, home string, src *Source, channel, current string) (State, Release, error) {
	prev := LoadState(home)
	st := State{CheckedAt: time.Now().UTC(), Channel: channel, SkipAuto: prev.SkipAuto}
	rel, err := src.Latest(ctx, channel, current)
	switch {
	case errors.Is(err, ErrNoRelease):
		err = nil
	case err != nil:
		// Keep what the last good check found.
		st.Error, st.Latest, st.URL = err.Error(), prev.Latest, prev.URL
	default:
		st.Latest, st.URL = rel.Version.String(), rel.URL
	}
	if serr := SaveState(home, st); err == nil {
		err = serr
	}
	return st, rel, err
}
