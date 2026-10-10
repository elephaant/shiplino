package opencodeplugin

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func node(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	return bin
}

func TestSource(t *testing.T) {
	if n := bytes.Count(Source, []byte(`"`+BinPlaceholder+`"`)); n != 1 {
		t.Fatalf("placeholder literal appears %d times, want 1", n)
	}
	if !bytes.Contains(Source, []byte(Marker)) {
		t.Fatal("marker missing")
	}
}

// The plugin parses as an ES module (OpenCode runs it on Bun; node checks
// the syntax).
func TestSyntax(t *testing.T) {
	bin := node(t)
	f := filepath.Join(t.TempDir(), "shiplino.mjs")
	if err := os.WriteFile(f, Source, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("node --check: %v\n%s", err, out)
	}
}

// shiplino.test.mjs runs the event handler against a fake shiplino binary.
func TestNode(t *testing.T) {
	bin := node(t)
	cmd := exec.Command(bin, "--test", "shiplino.test.mjs")
	cmd.Env = append(os.Environ(), "UPDATE=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
