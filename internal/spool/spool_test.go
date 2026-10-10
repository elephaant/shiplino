// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package spool

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"claude-code":            "claude-code",
		"3f2c-11aa_bb.v2":        "3f2c-11aa_bb.v2",
		"../../etc/passwd":       "_._.._etc_passwd",
		".hidden":                "_hidden",
		"a/b\\c":                 "a_b_c",
		strings.Repeat("x", 300): strings.Repeat("x", 128),
		"sess ion\x00":           "sess_ion_",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SafeName(""); !strings.HasPrefix(got, "_unknown-") {
		t.Errorf("SafeName(\"\") = %q", got)
	}
}

func TestAppendLineCreatesPrivateFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := AppendLine(root, "claude-code", "s1", []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if err := AppendLine(root, "claude-code", "s1", []byte("{\"a\":2}\n")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(SessionFile(root, "claude-code", "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{\"a\":1}\n{\"a\":2}\n" {
		t.Fatalf("content = %q", b)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(SessionFile(root, "claude-code", "s1"))
		if fi.Mode().Perm() != filePerm {
			t.Errorf("file perm = %v, want %v", fi.Mode().Perm(), os.FileMode(filePerm))
		}
		di, _ := os.Stat(filepath.Join(root, "claude-code"))
		if di.Mode().Perm() != dirPerm {
			t.Errorf("dir perm = %v, want %v", di.Mode().Perm(), os.FileMode(dirPerm))
		}
	}
}

func TestAppendLineStaysInsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := AppendLine(root, "../x", "../../escape", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, SessionFile(root, "../x", "../../escape"))
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("session file escaped root: %s", rel)
	}
}

// Many writers appending to the same session file must never interleave.
func TestConcurrentAppendsDoNotInterleave(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	const writers, perWriter = 32, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			pad := strings.Repeat(string(rune('a'+w%26)), 3000)
			for i := 0; i < perWriter; i++ {
				line := []byte(fmt.Sprintf("{\"w\":%d,\"i\":%d,\"pad\":%q}\n", w, i, pad))
				if err := AppendLine(root, "claude-code", "shared", line); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	f, err := os.Open(SessionFile(root, "claude-code", "shared"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, MaxLine*2), MaxLine*2)
	n := 0
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("{\"w\":")) || !bytes.HasSuffix(line, []byte("\"}")) {
			t.Fatalf("corrupt line %d: %.80q…", n, line)
		}
		n++
	}
	if n != writers*perWriter {
		t.Fatalf("lines = %d, want %d", n, writers*perWriter)
	}
}

func TestWriteBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	rel, err := WriteBlob(root, "01JD3K", []byte(`{"big":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if rel != "blobs/01JD3K.json" {
		t.Fatalf("rel = %q", rel)
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || string(b) != `{"big":true}` {
		t.Fatalf("blob = %q, %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "blobs"))
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestHome(t *testing.T) {
	t.Setenv("SHIPLINO_HOME", "/tmp/custom-shiplino")
	if got := Home(); got != "/tmp/custom-shiplino" {
		t.Fatalf("Home() = %q", got)
	}
}

func TestPaused(t *testing.T) {
	home := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	if Paused(home, now) {
		t.Fatal("paused without a marker")
	}
	os.WriteFile(filepath.Join(home, "paused"), nil, 0o600)
	if !Paused(home, now) {
		t.Fatal("empty marker should pause indefinitely")
	}
	os.WriteFile(filepath.Join(home, "paused"), []byte("1800000060\n"), 0o600)
	if !Paused(home, now) || Paused(home, now.Add(2*time.Minute)) {
		t.Fatal("timed pause wrong")
	}
}
