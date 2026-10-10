package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Where targets are kept.
const (
	InKeychain = "keychain"
	InFile     = "file"
)

const keyringService = "shiplino"

// Vault keeps targets in the OS keychain (macOS Keychain, Windows
// Credential Manager, Secret Service on Linux), like the sync
// credentials. Where there is none, as on a headless Linux server, it
// falls back to a 0600 file in the Shiplino home and says so: `shiplino
// notify list` and doctor show which one is used.
type Vault struct {
	Home string
}

func user(kind string) string { return "notify-" + kind }

func (v Vault) path(kind string) string { return filepath.Join(v.Home, "notify-"+kind+".json") }

// Load returns the stored target (nil if there is none), where it came
// from, and, for the file, why the keychain wasn't used.
func (v Vault) Load(kind string) (t *Target, where, why string, err error) {
	s, kerr := keyring.Get(keyringService, user(kind))
	if kerr == nil {
		t, err = decode(kind, []byte(s))
		return t, InKeychain, "", err
	}
	b, ferr := os.ReadFile(v.path(kind))
	if errors.Is(ferr, os.ErrNotExist) {
		if errors.Is(kerr, keyring.ErrNotFound) {
			return nil, "", "", nil
		}
		return nil, "", kerr.Error(), nil
	}
	if ferr != nil {
		return nil, InFile, "", ferr
	}
	why = "no OS keychain"
	if !errors.Is(kerr, keyring.ErrNotFound) {
		why = "OS keychain unavailable: " + kerr.Error()
	}
	t, err = decode(kind, b)
	return t, InFile, why, err
}

func decode(kind string, b []byte) (*Target, error) {
	var t Target
	if err := json.Unmarshal(b, &t); err != nil || t.Kind != kind {
		return nil, fmt.Errorf("the stored %s target is unreadable (run `shiplino notify add %s` again)", kind, kind)
	}
	return &t, nil
}

// Save stores a target, in the keychain when there is one.
func (v Vault) Save(t Target) (where, why string, err error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", "", err
	}
	kerr := keyring.Set(keyringService, user(t.Kind), string(b))
	if kerr == nil {
		if err := os.Remove(v.path(t.Kind)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return InKeychain, "", err
		}
		return InKeychain, "", nil
	}
	if err := os.MkdirAll(v.Home, 0o700); err != nil {
		return "", "", err
	}
	if err := writeFile0600(v.path(t.Kind), b); err != nil {
		return "", "", err
	}
	return InFile, "OS keychain unavailable: " + kerr.Error(), nil
}

// Delete removes a target from both places, and fails if it is still
// readable afterwards.
func (v Vault) Delete(kind string) error {
	kerr := keyring.Delete(keyringService, user(kind))
	if err := os.Remove(v.path(kind)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if t, _, _, _ := v.Load(kind); t != nil {
		return fmt.Errorf("can't remove the %s target from the OS keychain: %w", kind, kerr)
	}
	return nil
}

// writeFile0600 replaces path atomically with a file only the user can read.
func writeFile0600(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".notify-*")
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
