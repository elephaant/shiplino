// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package configfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripKeepsOrderAndNumbers(t *testing.T) {
	in := `{"zeta":1,"alpha":{"b":2.50,"a":[true,null,"x"]},"big":12345678901234567890}`
	o, err := ParseObject([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Format(o)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Index(s, `"zeta"`) > strings.Index(s, `"alpha"`) || strings.Index(s, `"b"`) > strings.Index(s, `"a"`) {
		t.Fatalf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, "2.50") || !strings.Contains(s, "12345678901234567890") {
		t.Fatalf("numbers changed:\n%s", s)
	}
	o2, err := ParseObject(out)
	if err != nil {
		t.Fatal(err)
	}
	out2, _ := Format(o2)
	if string(out2) != s {
		t.Fatal("format is not stable")
	}
}

func TestRejectsWhatWeCannotRewriteExactly(t *testing.T) {
	for _, in := range []string{
		`{"a":1} // comment`,
		`{"a":1,"a":2}`,
		`[1,2]`,
		`{"a":1}{"b":2}`,
		`{"a":}`,
		``,
	} {
		if _, err := ParseObject([]byte(in)); err == nil {
			t.Errorf("ParseObject(%q) accepted", in)
		}
	}
}

func TestSetGetDelete(t *testing.T) {
	o := &Object{}
	o.Set("a", "1")
	o.Set("b", "2")
	o.Set("a", "3")
	if v, _ := o.Get("a"); v != "3" || len(o.Members) != 2 || o.Members[0].Key != "a" {
		t.Fatalf("%+v", o.Members)
	}
	o.Delete("a")
	if _, ok := o.Get("a"); ok || len(o.Members) != 1 {
		t.Fatalf("%+v", o.Members)
	}
}

func TestBackupAndWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if b, err := Backup(path, filepath.Join(dir, "backups")); err != nil || b != "" {
		t.Fatalf("backup of missing file: %q %v", b, err)
	}
	os.WriteFile(path, []byte(`{"old":true}`), 0o640)
	b, err := Backup(path, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(b); string(got) != `{"old":true}` {
		t.Fatalf("backup content %q", got)
	}
	if err := WriteAtomic(path, []byte(`{"new":true}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != `{"new":true}` {
		t.Fatalf("content %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings.json.tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}
