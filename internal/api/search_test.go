// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/store"
)

func TestSearchEndpoint(t *testing.T) {
	f := setup(t)
	if resp, _ := f.get(t, "/api/v1/search?q=login", nil); resp.StatusCode != 401 {
		t.Fatalf("search without auth: %d", resp.StatusCode)
	}
	resp, body := f.get(t, "/api/v1/search?q=redir", bearer)
	var out struct{ Results []store.Hit }
	if resp.StatusCode != 200 || json.Unmarshal(body, &out) != nil || len(out.Results) != 1 || out.Results[0].Title != "Fix login" {
		t.Fatalf("search: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/api/v1/search?q=", bearer); !strings.Contains(string(body), `"results":[]`) {
		t.Fatalf("empty query: %s", body)
	}
	if resp, _ := f.get(t, "/api/v1/search?q=x&limit=999", bearer); resp.StatusCode != 400 {
		t.Fatalf("bad limit: %d", resp.StatusCode)
	}
}

func TestExportEndpoint(t *testing.T) {
	f := setup(t)
	resp, body := f.get(t, "/api/v1/export?format=csv", bearer)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), ".csv") {
		t.Fatalf("csv: %d %v", resp.StatusCode, resp.Header)
	}
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][0] != "id" || len(rows[1]) != len(exportColumns) {
		t.Fatalf("csv rows: %v %v", rows, err)
	}
	resp, body = f.get(t, "/api/v1/export", bearer)
	var list []map[string]any
	if resp.StatusCode != 200 || json.Unmarshal(body, &list) != nil || len(list) != 2 {
		t.Fatalf("json: %d %s", resp.StatusCode, body)
	}
	if _, body := f.get(t, "/api/v1/export?since=1h", bearer); !strings.Contains(string(body), "claude-code:s1") {
		t.Fatalf("since=1h dropped recent sessions: %s", body)
	}
	for _, bad := range []string{"format=xml", "since=soon"} {
		if resp, _ := f.get(t, "/api/v1/export?"+bad, bearer); resp.StatusCode != 400 {
			t.Fatalf("%s: %d", bad, resp.StatusCode)
		}
	}
}

func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{
		`=HYPERLINK("x")`: `'=HYPERLINK("x")`, "@cmd": "'@cmd", "-1.5": "-1.5", "+fix": "'+fix", "plain": "plain", "": "",
	} {
		if got := safeCell(in); got != want {
			t.Errorf("safeCell(%q) = %q, want %q", in, got, want)
		}
	}
}
