// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeAdmin struct {
	paused   bool
	until    time.Time
	notified int
	fail     bool
}

func (a *fakeAdmin) Settings(context.Context) any {
	return map[string]any{"paused": a.paused, "until": a.until}
}
func (a *fakeAdmin) Pause(u time.Time) error { a.paused, a.until = true, u; return nil }
func (a *fakeAdmin) Resume() error           { a.paused = false; return nil }
func (a *fakeAdmin) Backfill(since time.Time) int {
	a.until = since
	return 3
}
func (a *fakeAdmin) TestNotification(context.Context) error {
	if a.fail {
		return errors.New("no notification service found")
	}
	a.notified++
	return nil
}

func (f *fixture) post(t *testing.T, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+path, strings.NewReader(body))
	bearer(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSettingsEndpoints(t *testing.T) {
	f := setup(t)
	if resp, _ := f.get(t, "/api/v1/settings", bearer); resp.StatusCode != 501 {
		t.Fatalf("without admin: %d", resp.StatusCode)
	}
	a := &fakeAdmin{}
	f.s.Admin = a
	if resp, _ := f.get(t, "/api/v1/settings", nil); resp.StatusCode != 401 {
		t.Fatalf("settings without auth: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/api/v1/settings", bearer); resp.StatusCode != 200 {
		t.Fatalf("settings: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/pause", `{"minutes":30}`); resp.StatusCode != 200 || !a.paused || time.Until(a.until) < 29*time.Minute {
		t.Fatalf("pause 30m: %d %+v", resp.StatusCode, a)
	}
	if resp, _ := f.post(t, "/api/v1/pause", ""); resp.StatusCode != 200 || !a.until.IsZero() {
		t.Fatalf("pause until resumed: %d %+v", resp.StatusCode, a)
	}
	if resp, _ := f.post(t, "/api/v1/pause", `{"minutes":-1}`); resp.StatusCode != 400 {
		t.Fatalf("bad minutes: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/resume", ""); resp.StatusCode != 200 || a.paused {
		t.Fatalf("resume: %d", resp.StatusCode)
	}
	if resp, body := f.post(t, "/api/v1/backfill", `{"days":7}`); resp.StatusCode != 200 || !strings.Contains(body, `"transcripts":3`) || time.Since(a.until) < 6*24*time.Hour {
		t.Fatalf("backfill: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.post(t, "/api/v1/backfill", `{"days":0}`); resp.StatusCode != 400 {
		t.Fatalf("bad days: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/notify/test", ""); resp.StatusCode != 204 || a.notified != 1 {
		t.Fatalf("notify test: %d", resp.StatusCode)
	}
	a.fail = true
	if resp, body := f.post(t, "/api/v1/notify/test", ""); resp.StatusCode != 502 || !strings.Contains(body, "no notification service") {
		t.Fatalf("notify failure: %d %s", resp.StatusCode, body)
	}
}
