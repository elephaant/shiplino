package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/notify/push"
)

func TestNotifyTargets(t *testing.T) {
	keyring.MockInit()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
	}))
	defer srv.Close()

	home := t.TempDir()
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out, home: home, userHome: t.TempDir(), version: "test",
		notifySend: func(context.Context, notify.Note) error { return nil }}
	if err := config.WriteDefault(home); err != nil {
		t.Fatal(err)
	}

	// The URL is pasted at the prompt, so it stays out of shell history.
	e.in = strings.NewReader(srv.URL + "/services/T0/B0/tokenpart\n")
	if code := notifyCmd(context.Background(), e, []string{"add", "slack"}); code != 0 {
		t.Fatalf("add slack %d:\n%s", code, &out)
	}
	if code := notifyCmd(context.Background(), e, []string{"add", "ntfy", "--server", srv.URL, "--topic", "phone-1"}); code != 0 {
		t.Fatalf("add ntfy %d:\n%s", code, &out)
	}
	if code := notifyCmd(context.Background(), e, []string{"add", "webhook", "http://example.com/hook"}); code != 1 {
		t.Fatalf("plain http to another host accepted: %d", code)
	}
	b, _ := os.ReadFile(config.Path(home))
	if strings.Contains(string(b), "tokenpart") || strings.Contains(string(b), "phone-1") {
		t.Fatalf("a secret reached config.toml:\n%s", b)
	}
	cfg, err := config.Load(home)
	if err != nil || strings.Join(cfg.Notify.Push.Targets, ",") != "slack,ntfy" {
		t.Fatalf("config: %v %v", cfg.Notify.Push, err)
	}
	if !strings.Contains(string(b), "# Desktop notifications.") || !strings.Contains(string(b), "[budget]") {
		t.Fatalf("other settings lost:\n%s", b)
	}

	out.Reset()
	if code := notifyCmd(context.Background(), e, []string{"list"}); code != 0 || !strings.Contains(out.String(), "nothing sent yet") || strings.Contains(out.String(), "tokenpart") {
		t.Fatalf("list %d:\n%s", code, &out)
	}
	out.Reset()
	if code := notifyCmd(context.Background(), e, []string{"test"}); code != 0 || len(got) != 2 {
		t.Fatalf("test %d, %d delivered:\n%s", code, len(got), &out)
	}
	if got[1]["topic"] != "phone-1" || got[1]["title"] != "Shiplino test alert" {
		t.Fatalf("ntfy: %v", got[1])
	}
	if st := push.Status(home); !st[push.Slack].Last.OK || !st[push.Ntfy].Last.OK {
		t.Fatalf("status: %+v", st)
	}
	out.Reset()
	notifyCmd(context.Background(), e, []string{"list"})
	if !strings.Contains(out.String(), "last delivered") {
		t.Fatalf("list after test:\n%s", &out)
	}

	if code := notifyCmd(context.Background(), e, []string{"remove", "slack"}); code != 0 {
		t.Fatalf("remove %d:\n%s", code, &out)
	}
	if tg, _, _, _ := (push.Vault{Home: home}).Load(push.Slack); tg != nil {
		t.Fatal("slack still stored")
	}
	cfg, _ = config.Load(home)
	if strings.Join(cfg.Notify.Push.Targets, ",") != "ntfy" {
		t.Fatalf("after remove: %v", cfg.Notify.Push.Targets)
	}

	// A failing target is visible, without its URL.
	srv.Close()
	out.Reset()
	if code := notifyCmd(context.Background(), e, []string{"test", "ntfy"}); code != 1 {
		t.Fatalf("test of a dead target: %d", code)
	}
	out.Reset()
	notifyCmd(context.Background(), e, []string{"list"})
	if !strings.Contains(out.String(), "last delivery failed") {
		t.Fatalf("list:\n%s", &out)
	}
}

func TestNotifyAddWebhookSigned(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out, home: home, version: "test"}
	if code := notifyCmd(context.Background(), e, []string{"add", "webhook", "https://hooks.example.com/x", "--sign"}); code != 0 {
		t.Fatalf("add %d:\n%s", code, &out)
	}
	tg, _, _, _ := push.Vault{Home: home}.Load(push.Webhook)
	if tg == nil || len(tg.Secret) != 64 || !strings.Contains(out.String(), tg.Secret) {
		t.Fatalf("secret: %+v\n%s", tg, &out)
	}
	for _, bad := range [][]string{{"add", "slack", "--sign"}, {"add", "ntfy", "https://x"}, {"add", "pager"}, {"remove"}} {
		if code := notifyCmd(context.Background(), e, bad); code != 2 {
			t.Errorf("%v: %d, want usage", bad, code)
		}
	}
}
