package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/notify/push"
)

// Push is [notify.push]: alerts sent to your phone or a team channel.
// Each target is an outbound connection, so none is on until you add it
// with `shiplino notify add`. Its URL, topic and tokens are kept in the
// OS keychain; this section only says which targets are on.
type Push struct {
	Targets []string `toml:"targets"`
	// Events to send ("waiting", "failed", "done", "budget", "limit",
	// "digest"); empty means push.DefaultEvents.
	Events []string `toml:"events"`
}

var pushEvents = []string{notify.EventWaiting, notify.EventDone, notify.EventFailed, notify.EventBudget, notify.EventLimit, notify.EventDigest}

func validatePush(p Push) error {
	for _, t := range p.Targets {
		if !push.Valid(t) {
			return fmt.Errorf("notify.push.targets: unknown target %q (want %s)", t, strings.Join(push.Kinds, ", "))
		}
	}
	for _, e := range p.Events {
		if !slices.Contains(pushEvents, e) {
			return fmt.Errorf("notify.push.events: unknown event %q (want %s)", e, strings.Join(pushEvents, ", "))
		}
	}
	return nil
}

// PushSettings returns the push targets and events.
func (c Config) PushSettings() push.Settings {
	return push.Settings{Targets: c.Notify.Push.Targets, Events: c.Notify.Push.Events}
}

const pushHeader = `[notify.push]
# Alerts on your phone or in a team channel: a webhook, ntfy, Slack or
# Discord. Off until you run ` + "`shiplino notify add`" + `; these lines are
# rewritten by ` + "`shiplino notify add|remove`" + `, and changes apply within
# seconds. URLs, topics and tokens are kept in the OS keychain, not here.
# Alerts carry metadata only (agent, project, branch, status, why it's
# waiting, duration, cost and a link to this board), never prompts,
# titles, commands, file names or the agent's messages.
# events: any of "waiting", "failed", "done", "budget", "limit", "digest"
# (empty = all but "done").
`

// UpdatePush changes the [notify.push] section of config.toml and leaves
// the rest of the file as it was.
func UpdatePush(home string, change func(*Push)) error {
	c, err := Load(home)
	if err != nil {
		return err
	}
	change(&c.Notify.Push)
	p := c.Notify.Push
	if p.Targets == nil {
		p.Targets = []string{}
	}
	if p.Events == nil {
		p.Events = []string{}
	}
	body, err := toml.Marshal(p)
	if err != nil {
		return err
	}
	return rewriteSection(home, "notify.push", pushHeader+string(body))
}
