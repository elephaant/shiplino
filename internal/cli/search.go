// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/store"
)

// flagValue returns the value after name in args, and args without both.
func flagValue(args []string, name string) (string, []string) {
	var rest []string
	val := ""
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			val = args[i+1]
			i++
			continue
		}
		if v, ok := strings.CutPrefix(args[i], name+"="); ok {
			val = v
			continue
		}
		rest = append(rest, args[i])
	}
	return val, rest
}

// search finds prompts, commands, file paths, titles and commit messages.
func search(ctx context.Context, e *env, args []string) int {
	project, args := flagValue(args, "--project")
	q := strings.Join(args, " ")
	if strings.TrimSpace(q) == "" {
		fmt.Fprintln(e.errOut, "usage: shiplino search <words…> [--project <id>]")
		return 2
	}
	c, err := e.client()
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	var r struct{ Results []store.Hit }
	if err := c.get(ctx, "/api/v1/search?"+url.Values{"q": {q}, "project": {project}, "limit": {"30"}}.Encode(), &r); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if len(r.Results) == 0 {
		fmt.Fprintln(e.out, "No matches.")
		return 0
	}
	label := map[string]string{"turn.start": "prompt", "shell.exec": "command", "file.edit": "file", "git.commit": "commit", "git.pr": "pr"}
	for _, h := range r.Results {
		kind := label[h.Kind]
		if kind == "" {
			kind = "title"
		}
		fmt.Fprintf(e.out, "%-8s %-9s %s\n", kind, ago(h.TS), clip(strings.Join(strings.Fields(h.Snippet), " "), 100))
		fmt.Fprintf(e.out, "         %s  %s\n", clip(h.Title, 60), h.SessionID)
	}
	return 0
}

// export writes sessions as CSV or JSON to stdout or --out.
func export(ctx context.Context, e *env, args []string) int {
	format, args := flagValue(args, "--format")
	project, args := flagValue(args, "--project")
	since, args := flagValue(args, "--since")
	out, args := flagValue(args, "--out")
	if len(args) > 0 {
		fmt.Fprintln(e.errOut, "usage: shiplino export [--format csv|json] [--project <id>] [--since 7d] [--out file]")
		return 2
	}
	if format == "" {
		format = "csv"
		if strings.HasSuffix(out, ".json") {
			format = "json"
		}
	}
	c, err := e.client()
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	path := "/api/v1/export?" + url.Values{"format": {format}, "project": {project}, "since": {since}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	hc := c.http
	hc.Timeout = 2 * time.Minute
	resp, err := hc.Do(req)
	if err != nil {
		fmt.Fprintln(e.errOut, errDaemonDown)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Fprintf(e.errOut, "export failed: %s %s\n", resp.Status, strings.TrimSpace(string(b)))
		return 1
	}
	w := e.out
	if out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintln(e.errOut, err)
			return 1
		}
		defer f.Close()
		w = f
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if out != "" {
		fmt.Fprintf(e.errOut, "Wrote %s\n", out)
	}
	return 0
}
