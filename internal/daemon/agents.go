package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/elephaant/shiplino/internal/agents"
	"github.com/elephaant/shiplino/internal/api"
)

// AgentPlan is what GET /api/v1/agents/{key}/preview returns: the diff
// connecting or removing an agent would make. Nothing is written.
type AgentPlan struct {
	Key     string     `json:"key"`
	Name    string     `json:"name"`
	Action  string     `json:"action"` // connect | remove
	Changes []FileDiff `json:"changes"`
	// Problem is why the change can't be made in full (e.g. a config file
	// with comments); Changes still lists what would be written.
	Problem string `json:"problem,omitempty"`
	Note    string `json:"note,omitempty"` // shown after connecting
}

// FileDiff is one file's unified diff, labeled with its ~ path.
type FileDiff struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

// bin is the installed binary the hooks run (what setup points them at).
func (a *admin) bin() string {
	bin := filepath.Join(a.home, "bin", "shiplino")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

// agent finds the agent and its hook config on this machine.
func (a *admin) agent(ctx context.Context, key string, connect bool) (h agents.Hooks, path, version, userHome string, err error) {
	h, ok := agents.Find(key)
	if !ok {
		return h, "", "", "", fmt.Errorf("%w %q", api.ErrUnknownAgent, key)
	}
	userHome, _ = os.UserHomeDir()
	found, version, path := h.Detect(ctx, userHome)
	if !connect {
		return h, path, version, userHome, nil
	}
	if !found {
		return h, "", "", "", fmt.Errorf("%s isn't installed on this machine", h.Name)
	}
	if _, err := os.Stat(a.bin()); err != nil {
		return h, "", "", "", fmt.Errorf("%s is missing: run `shiplino setup` first", tilde(a.bin(), userHome))
	}
	return h, path, version, userHome, nil
}

func (a *admin) AgentPreview(ctx context.Context, key string, connect bool) (any, error) {
	h, path, version, userHome, err := a.agent(ctx, key, connect)
	if err != nil {
		return nil, err
	}
	plan := AgentPlan{Key: key, Name: h.Name, Action: "remove", Changes: []FileDiff{}}
	var changes []agents.Change
	if connect {
		plan.Action, plan.Note = "connect", h.Note
		changes, err = h.PreviewInstall(path, a.bin(), version)
	} else {
		changes, err = h.PreviewUninstall(path)
	}
	switch {
	case agents.IsUnparseable(err):
		plan.Problem = tilde(path, userHome) + " isn't plain JSON (comments?), so Shiplino won't edit it. Add the hooks by hand: see the README."
	case err != nil:
		plan.Problem = err.Error()
	}
	for _, c := range changes {
		p := tilde(c.Path, userHome)
		plan.Changes = append(plan.Changes, FileDiff{Path: p, Diff: c.Diff(p)})
	}
	return plan, nil
}

func (a *admin) AgentChange(ctx context.Context, key string, connect bool) error {
	a.agentMu.Lock()
	defer a.agentMu.Unlock()
	h, path, version, userHome, err := a.agent(ctx, key, connect)
	if err != nil {
		return err
	}
	backups := filepath.Join(a.home, "backups", h.ID)
	if connect {
		_, _, err = h.Install(path, a.bin(), version, backups)
	} else {
		_, err = h.Uninstall(path, backups)
	}
	if agents.IsUnparseable(err) {
		return fmt.Errorf("%s isn't plain JSON (comments?), so Shiplino left it untouched", tilde(path, userHome))
	}
	return err
}
