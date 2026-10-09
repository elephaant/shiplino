// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package hookfile

import (
	"runtime"
	"testing"
)

func TestShellQuote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix quoting")
	}
	cases := map[string]string{
		"/home/u/.shiplino/bin/shiplino": `"/home/u/.shiplino/bin/shiplino"`,
		"/home/a b/shiplino":             `'/home/a b/shiplino'`,
		"/home/o'neil/shiplino":          `'/home/o'\''neil/shiplino'`,
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
