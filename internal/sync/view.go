package sync

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/store"
)

// View is the sync state shown by `shiplino sync status`, doctor and the
// settings page. It never contains tokens.
type View struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint"`
	SignedIn      bool   `json:"signed_in"`
	Account       string `json:"account,omitempty"`
	Role          string `json:"role,omitempty"` // in the workspace, as of sign-in
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	// CredentialStore is "keychain" or "file"; CredentialNote says why
	// the file is used.
	CredentialStore string    `json:"credential_store,omitempty"`
	CredentialNote  string    `json:"credential_note,omitempty"`
	SendTitles      bool      `json:"send_titles"`
	Ignored         []string  `json:"ignored,omitempty"` // [sync] settings that no longer apply
	Projects        []string  `json:"projects"`
	Exclude         []string  `json:"exclude"`
	LastUpload      time.Time `json:"last_upload,omitzero"`
	Uploaded        int64     `json:"uploaded"`
	Rejected        int64     `json:"rejected"` // dropped by the service as invalid
	Backlog         int64     `json:"backlog"`  // events stored since the last upload, not yet checked
	LastError       string    `json:"last_error,omitempty"`
	LastErrorAt     time.Time `json:"last_error_at,omitzero"`
	NextRetry       time.Time `json:"next_retry,omitzero"`
	// NeedsLogin: the service signed this device out (the tokens were
	// deleted). Forbidden: the account's role can't sync.
	NeedsLogin bool `json:"needs_login,omitempty"`
	Forbidden  bool `json:"forbidden,omitempty"`
}

// Describe builds a View from the config, stored credentials and progress.
func Describe(ctx context.Context, home string, cfg config.Config, c *Creds, where, why string, st *store.Store) View {
	v := View{Enabled: cfg.Sync.Enabled, Endpoint: cfg.SyncEndpoint(), SendTitles: cfg.Sync.SendTitles, Ignored: cfg.SyncIgnored(),
		Projects: nonNil(cfg.Sync.Projects), Exclude: nonNil(cfg.Sync.Exclude), CredentialStore: where, CredentialNote: why}
	if c == nil {
		if reason := (Vault{Home: home}).SignedOut(); reason != "" {
			v.NeedsLogin, v.LastError = true, reason
		}
		return v
	}
	v.SignedIn, v.Account, v.Role, v.WorkspaceID, v.WorkspaceName, v.Endpoint = true, c.Account, c.Role, c.WorkspaceID, c.WorkspaceName, c.Endpoint
	if st == nil {
		return v
	}
	s, err := st.SyncState(ctx, c.WorkspaceID)
	if err != nil {
		v.LastError = err.Error()
		return v
	}
	v.LastUpload, v.Uploaded, v.Rejected, v.LastError, v.LastErrorAt = s.LastUpload, s.Uploaded, s.Rejected, s.LastError, s.LastErrorAt
	v.Forbidden = s.LastError == errForbidden.Error()
	cursor := s.Cursor
	if ScopeFor(cfg).widens(s.Scope) {
		cursor = 0
	}
	if len(v.Projects) > 0 {
		v.Backlog, _ = st.SyncPending(ctx, cursor)
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// NextBatch is the batch the uploader would send next, built exactly as
// it would be, for `shiplino sync status --dry-run`. Nothing is sent and
// no progress is saved.
func NextBatch(ctx context.Context, home string, cfg config.Config, c *Creds, st *store.Store) (Batch, error) {
	state, err := st.SyncState(ctx, c.WorkspaceID)
	if err != nil {
		return Batch{}, err
	}
	items, _, err := nextItems(ctx, st, ScopeFor(cfg), &state)
	if err != nil {
		return Batch{}, err
	}
	b := Batch{DeviceID: DeviceID(home), DeviceName: deviceName(), Events: []json.RawMessage{}}
	for _, it := range items {
		b.Events = append(b.Events, it.body)
	}
	return b, nil
}

// Role returns the workspace role from a GET /v1/me answer ("" if none).
func Role(me map[string]any) string {
	ws, _ := me["workspace"].(map[string]any)
	r, _ := ws["role"].(string)
	return r
}

// AccountName picks a readable identity from a GET /v1/me answer.
func AccountName(me map[string]any) string {
	pick := func(m map[string]any) string {
		for _, k := range []string{"email", "name", "login", "id"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
	if s := pick(me); s != "" {
		return s
	}
	if u, ok := me["user"].(map[string]any); ok {
		return pick(u)
	}
	return ""
}
