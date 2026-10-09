// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Envelope is one spool line: the raw hook payload plus what the shim knows.
// Exactly one of P (inline JSON payload), S (non-JSON payload as text) or
// B (path of a blob file, relative to the spool root) is set.
type Envelope struct {
	ID    string          `json:"id"`
	Agent string          `json:"a"`
	Event string          `json:"e,omitempty"`
	TS    int64           `json:"t"` // Unix nanoseconds when the hook ran
	PID   int             `json:"pid"`
	P     json.RawMessage `json:"p,omitempty"`
	S     string          `json:"s,omitempty"`
	B     string          `json:"b,omitempty"`
}

// MaxLine is the largest line written to a session file. Larger payloads go
// to a blob file so a single write() stays small enough not to interleave
// with other processes appending to the same file.
const MaxLine = 4096

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Home returns the Shiplino data directory: $SHIPLINO_HOME if set,
// otherwise ~/.shiplino. It returns "" if neither can be determined.
func Home() string {
	if h := os.Getenv("SHIPLINO_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".shiplino")
}

// Paused reports whether recording is paused: home/paused exists and,
// if it holds a Unix timestamp, that time hasn't passed yet.
func Paused(home string, now time.Time) bool {
	b, err := os.ReadFile(filepath.Join(home, "paused"))
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return true // paused until resumed
	}
	until, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return true
	}
	return now.Unix() < until
}

// Dir returns the spool directory under a Shiplino home.
func Dir(home string) string { return filepath.Join(home, "spool") }

// SessionFile returns the spool file for one agent session.
func SessionFile(root, agent, session string) string {
	return filepath.Join(root, SafeName(agent), SafeName(session)+".jsonl")
}

// AppendLine appends line (which must end in '\n') to the session's spool
// file with a single O_APPEND write.
func AppendLine(root, agent, session string, line []byte) error {
	dir := filepath.Join(root, SafeName(agent))
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	f, err := os.OpenFile(SessionFile(root, agent, session), os.O_WRONLY|os.O_APPEND|os.O_CREATE, filePerm)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// WriteBlob stores a large payload as root/blobs/<id>.json and returns its
// path relative to root. The blob is complete before this returns, so a
// line referencing it never points at a partial file.
func WriteBlob(root, id string, data []byte) (string, error) {
	dir := filepath.Join(root, "blobs")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", err
	}
	name := SafeName(id) + ".json"
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	if err := tmp.Chmod(filePerm); err != nil && !isUnsupported(err) {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return "blobs/" + name, nil
}

// isUnsupported reports chmod errors on platforms without Unix permissions.
func isUnsupported(err error) bool { return strings.Contains(err.Error(), "not supported") }

// SafeName turns an agent or session id into a safe file name: only
// [A-Za-z0-9._-], no leading dot, at most 128 bytes. Session ids come from
// hook payloads, so this also blocks path traversal.
func SafeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < 128; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		case c == '.' && b.Len() > 0:
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_unknown-" + strconv.Itoa(os.Getpid())
	}
	return b.String()
}
