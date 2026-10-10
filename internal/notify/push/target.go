package push

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Target kinds.
const (
	Webhook = "webhook" // generic JSON POST, optionally signed
	Ntfy    = "ntfy"    // ntfy.sh or a self-hosted ntfy server
	Slack   = "slack"   // Slack incoming webhook
	Discord = "discord" // Discord channel webhook
)

// Kinds are the supported target kinds.
var Kinds = []string{Webhook, Ntfy, Slack, Discord}

// Target is where alerts go. All of it is kept in the OS keychain: a
// webhook URL is itself a credential, and so is an ntfy topic on a
// public server (anyone who knows it can read it).
type Target struct {
	Kind   string `json:"kind"`
	URL    string `json:"url,omitempty"`    // webhook, Slack, Discord
	Secret string `json:"secret,omitempty"` // webhook: HMAC-SHA256 signing key
	Server string `json:"server,omitempty"` // ntfy, e.g. "https://ntfy.sh"
	Topic  string `json:"topic,omitempty"`  // ntfy
	Token  string `json:"token,omitempty"`  // ntfy access token
}

// Validate checks a target before it is saved or used.
func (t Target) Validate() error {
	switch t.Kind {
	case Webhook, Slack, Discord:
		return CheckURL(t.URL)
	case Ntfy:
		if err := CheckURL(t.Server); err != nil {
			return err
		}
		if !topic.MatchString(t.Topic) {
			return errors.New("an ntfy topic is 1-64 letters, digits, - or _")
		}
		return nil
	}
	return fmt.Errorf("unknown target %q (want one of %s)", t.Kind, strings.Join(Kinds, ", "))
}

// topic is what ntfy accepts as a topic name.
var topic = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)

// CheckURL accepts https URLs, and plain http only for this machine (a
// local relay or test server), so alerts never cross a network in the
// clear.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("that isn't a URL like https://example.com/hook")
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
	return errors.New("the URL must use https (plain http only works for localhost)")
}

// Where says where a target delivers without revealing its secret: the
// host, plus the ntfy topic's first characters.
func (t Target) Where() string {
	raw := t.URL
	if t.Kind == Ntfy {
		raw = t.Server
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "?"
	}
	w := u.Host
	if t.Kind == Ntfy {
		w += "/" + t.Topic[:min(3, len(t.Topic))] + "…"
	}
	return w
}

// request builds the HTTP request for a batch of payloads.
func (t Target) request(ps []map[string]any, now time.Time, delivery string) (*http.Request, error) {
	var body any
	endpoint := t.URL
	switch t.Kind {
	case Webhook:
		body = map[string]any{"version": 1, "source": "shiplino", "delivery": delivery,
			"sent_at": now.UTC().Format(time.RFC3339), "alerts": ps}
	case Ntfy:
		m := compose(ps)
		n := map[string]any{"topic": t.Topic, "title": m.Title, "message": strings.Join(m.Lines, "\n"), "tags": m.Tags}
		if n["message"] == "" {
			n["message"] = m.Title
		}
		if m.Urgent {
			n["priority"] = 4
		}
		if m.Link != "" {
			n["click"] = m.Link
		}
		body, endpoint = n, strings.TrimRight(t.Server, "/")+"/"
	case Slack:
		m := compose(ps)
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
		text := "*" + esc.Replace(m.Title) + "*"
		for _, l := range m.Lines {
			text += "\n" + esc.Replace(l)
		}
		if m.Link != "" {
			text += "\n<" + m.Link + "|Open in Shiplino>"
		}
		body = map[string]any{"text": text}
	case Discord:
		m := compose(ps)
		esc := strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`, ">", `\>`, "#", `\#`)
		text := "**" + esc.Replace(m.Title) + "**"
		for _, l := range m.Lines {
			text += "\n" + esc.Replace(l)
		}
		if m.Link != "" {
			text += "\n<" + m.Link + ">"
		}
		// No pings, whatever a name contains.
		body = map[string]any{"content": clip(text, 2000), "username": "Shiplino", "allowed_mentions": map[string]any{"parse": []string{}}}
	default:
		return nil, fmt.Errorf("unknown target %q", t.Kind)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("bad target URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "shiplino")
	switch t.Kind {
	case Webhook:
		req.Header.Set("X-Shiplino-Delivery", delivery)
		if t.Secret != "" {
			ts := strconv.FormatInt(now.Unix(), 10)
			req.Header.Set("X-Shiplino-Timestamp", ts)
			req.Header.Set("X-Shiplino-Signature", Sign(t.Secret, ts, b))
		}
	case Ntfy:
		if t.Token != "" {
			req.Header.Set("Authorization", "Bearer "+t.Token)
		}
	}
	return req, nil
}

// Sign is the webhook signature: "sha256=" and the hex HMAC-SHA256 of
// the timestamp, a dot and the body, keyed with the shared secret.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a webhook signature in constant time, and that the
// timestamp is within tolerance of now (to refuse replays). Receivers
// written in Go can use it as is.
func Verify(secret, timestamp, signature string, body []byte, now time.Time, tolerance time.Duration) bool {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(Sign(secret, timestamp, body)))
}
