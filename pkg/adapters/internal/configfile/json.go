// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package configfile edits agents' config files safely: order-preserving
// JSON, backups and atomic writes.
package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Object is a JSON object that keeps its keys in file order, so editing a
// user's config doesn't reshuffle it.
type Object struct {
	Members []Member
}

// Member is one key/value pair. Values are *Object, []any, string,
// json.Number, bool or nil.
type Member struct {
	Key   string
	Value any
}

// Get returns the value for key.
func (o *Object) Get(key string) (any, bool) {
	for _, m := range o.Members {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Set replaces the value for key, or appends it.
func (o *Object) Set(key string, v any) {
	for i := range o.Members {
		if o.Members[i].Key == key {
			o.Members[i].Value = v
			return
		}
	}
	o.Members = append(o.Members, Member{Key: key, Value: v})
}

// Delete removes key.
func (o *Object) Delete(key string) {
	out := o.Members[:0]
	for _, m := range o.Members {
		if m.Key != key {
			out = append(out, m)
		}
	}
	o.Members = out
}

// ParseObject decodes a JSON document whose top level is an object.
// Duplicate keys, trailing data and comments are rejected: a file we
// can't read exactly must not be rewritten.
func ParseObject(b []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON document")
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("top level is not a JSON object")
	}
	return o, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &Object{}
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is %v", kt)
				}
				if seen[key] {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Members = append(o.Members, Member{Key: key, Value: v})
			}
			if _, err := dec.Token(); err != nil { // '}'
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil // string, json.Number, bool, nil
	}
}

// MarshalJSON writes the object with its keys in order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o.Members {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(m.Key)
		buf.Write(k)
		buf.WriteByte(':')
		v, err := json.Marshal(m.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Format renders the object as indented JSON with a trailing newline.
func Format(o *Object) ([]byte, error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
