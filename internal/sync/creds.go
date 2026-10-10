// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// Creds is a signed-in sync session. The tokens are secrets; the rest
// isn't, but lives alongside so one read restores everything.
type Creds struct {
	Endpoint      string    `json:"endpoint"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at,omitzero"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	Account       string    `json:"account,omitempty"` // who signed in, from GET /v1/me
	Role          string    `json:"role,omitempty"`    // their workspace role, from GET /v1/me
}

// FromToken builds credentials from a sign-in or refresh answer.
func FromToken(endpoint string, t *Token, now time.Time) *Creds {
	c := &Creds{Endpoint: endpoint, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken,
		WorkspaceID: t.WorkspaceID, WorkspaceName: t.WorkspaceName}
	if t.ExpiresIn > 0 {
		c.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return c
}

// signedOutFile records why the service signed this device out, so
// status and doctor can say so after the tokens are gone.
const signedOutFile = "sync-signed-out"

// SignedOut returns why the service signed this device out ("" if it
// didn't).
func (v Vault) SignedOut() string {
	b, _ := os.ReadFile(filepath.Join(v.Home, signedOutFile))
	return string(b)
}

// signOut deletes the tokens and remembers why.
func (v Vault) signOut(reason string) error {
	if err := v.Delete(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(v.Home, signedOutFile), []byte(reason), 0o600)
}

// Where credentials are kept.
const (
	InKeychain = "keychain"
	InFile     = "file"
)

const (
	keyringService = "shiplino"
	keyringUser    = "sync"
	credsFile      = "sync-credentials.json"
)

// Vault stores credentials in the OS keychain (macOS Keychain, Windows
// Credential Manager, Secret Service on Linux). Where there is none, as
// on a headless Linux server, it falls back to a 0600 file in the
// Shiplino home, the same trade-off gh and docker make, and says so:
// `shiplino doctor` and `shiplino sync status` show which one is used.
type Vault struct {
	Home string
}

// CredsPath is the fallback file.
func (v Vault) CredsPath() string { return filepath.Join(v.Home, credsFile) }

// Load returns the stored credentials (nil if signed out), where they
// came from, and, for the file, why the keychain wasn't used.
func (v Vault) Load() (c *Creds, where, why string, err error) {
	s, kerr := keyring.Get(keyringService, keyringUser)
	if kerr == nil {
		c, err = decode([]byte(s))
		return c, InKeychain, "", err
	}
	b, ferr := os.ReadFile(v.CredsPath())
	if errors.Is(ferr, os.ErrNotExist) {
		if errors.Is(kerr, keyring.ErrNotFound) {
			return nil, "", "", nil
		}
		// No keychain and no file: signed out, but say why if asked.
		return nil, "", kerr.Error(), nil
	}
	if ferr != nil {
		return nil, InFile, "", ferr
	}
	why = "no OS keychain"
	if !errors.Is(kerr, keyring.ErrNotFound) {
		why = "OS keychain unavailable: " + kerr.Error()
	}
	c, err = decode(b)
	return c, InFile, why, err
}

func decode(b []byte) (*Creds, error) {
	var c Creds
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("stored sync credentials are unreadable (run `shiplino sync login`): %w", err)
	}
	return &c, nil
}

// Save stores credentials, in the keychain when there is one, and
// returns where they went (and why, for the file).
func (v Vault) Save(c *Creds) (where, why string, err error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", "", err
	}
	_ = os.Remove(filepath.Join(v.Home, signedOutFile))
	kerr := keyring.Set(keyringService, keyringUser, string(b))
	if kerr == nil {
		// Don't leave an older copy on disk.
		if err := os.Remove(v.CredsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return InKeychain, "", err
		}
		return InKeychain, "", nil
	}
	if err := os.MkdirAll(v.Home, 0o700); err != nil {
		return "", "", err
	}
	if err := writeFile0600(v.CredsPath(), b); err != nil {
		return "", "", err
	}
	return InFile, "OS keychain unavailable: " + kerr.Error(), nil
}

// Delete removes credentials from both places, and fails if any are
// still readable afterwards.
func (v Vault) Delete() error {
	kerr := keyring.Delete(keyringService, keyringUser)
	if err := os.Remove(v.CredsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if c, _, _, _ := v.Load(); c != nil {
		return fmt.Errorf("can't remove the sync credentials from the OS keychain: %w", kerr)
	}
	return nil
}

// writeFile0600 replaces path atomically with a file only the user can read.
func writeFile0600(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".creds-*")
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
