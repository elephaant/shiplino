package adapters

import "testing"

func TestPatches(t *testing.T) {
	tests := []struct {
		name, got, want string
	}{
		{"replace with context", SnippetPatch([2]string{"a\nb\nc\nd\ne\nf\n", "a\nb\nc\nX\ne\nf\n"}),
			"@@ @@\n a\n b\n c\n-d\n+X\n e\n f\n"},
		{"context capped at 3 lines", SnippetPatch([2]string{"1\n2\n3\n4\n5\nold", "1\n2\n3\n4\n5\nnew"}),
			"@@ @@\n 3\n 4\n 5\n-old\n+new\n"},
		{"pure insert", SnippetPatch([2]string{"", "new line"}), "@@ @@\n+new line\n"},
		{"no-op skipped", SnippetPatch([2]string{"same", "same"}), ""},
		{"several edits", SnippetPatch([2]string{"a", "b"}, [2]string{"c", "d"}), "@@ @@\n-a\n+b\n@@ @@\n-c\n+d\n"},
		{"new file", NewFilePatch("x\ny\n"), "@@ -0,0 +1,2 @@\n+x\n+y\n"},
		{"empty new file", NewFilePatch(""), ""},
		{"numbered hunk", FormatHunks([]Hunk{{OldStart: 10, OldLines: 1, NewStart: 10, NewLines: 2, Lines: []string{"-a", "+b", "+c"}}}),
			"@@ -10,1 +10,2 @@\n-a\n+b\n+c\n"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, tt.got, tt.want)
		}
	}
}
