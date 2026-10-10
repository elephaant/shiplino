package redact

import (
	"path"
	"regexp"
	"strings"
)

// programRe is what a program token may look like: a plain lower-case
// name, short enough that it can't carry content.
var programRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

// wrappers run the next word as the program.
var wrappers = map[string]bool{
	"sudo": true, "env": true, "time": true, "nohup": true, "exec": true, "command": true, "builtin": true, "nice": true,
}

// setup commands only prepare the shell; the next command is the one
// that matters ("cd api && go test ./...").
var setup = map[string]bool{"cd": true, "pushd": true, "popd": true, "export": true, "set": true, "source": true, ".": true}

// Program is the program a shell command runs, as a metadata token: "go"
// for `cd api && GOFLAGS=-v go test ./...`, "npm" for `/usr/bin/npm ci`.
// It's the first word of the first command that isn't a directory change
// or shell setup, without variable assignments, wrappers (sudo, env, …),
// directory or ".exe", in lower case. It returns "" when that word isn't
// a plain name of at most 32 characters of [a-z0-9._-], so no argument,
// path or secret can pass as a program. Pass the redacted command.
func Program(cmd string) string {
	cmd = strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "(", " ", ")", " ").Replace(cmd)
	for _, seg := range strings.Split(cmd, "\n") {
		words := strings.Fields(seg)
		for len(words) > 0 {
			w := strings.Trim(words[0], `"'`)
			if strings.Contains(w, "=") || strings.HasPrefix(w, "-") || wrappers[w] {
				words = words[1:] // FOO=1, a wrapper or its option
				continue
			}
			break
		}
		if len(words) == 0 {
			continue
		}
		w := strings.Trim(words[0], `"'`)
		if setup[w] {
			continue
		}
		w = strings.ToLower(path.Base(strings.ReplaceAll(w, `\`, "/")))
		w = strings.TrimSuffix(w, ".exe")
		if programRe.MatchString(w) {
			return w
		}
		return ""
	}
	return ""
}

// ValidProgram reports whether s is a well-formed program token.
func ValidProgram(s string) bool { return programRe.MatchString(s) }
