// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package otlp

import (
	"encoding/binary"
	"fmt"
	"math"
)

// --- OTLP/protobuf ---
//
// A protobuf wire-format reader for the messages below. Lengths are
// checked against the remaining input before slicing, so a corrupt length
// can neither panic nor allocate; wrong wire types and truncated input
// are errors; unknown fields are skipped.
//
//	ExportLogsServiceRequest { repeated ResourceLogs resource_logs = 1; }
//	ResourceLogs  { Resource resource = 1; repeated ScopeLogs scope_logs = 2; }
//	Resource      { repeated KeyValue attributes = 1; }
//	ScopeLogs     { InstrumentationScope scope = 1; repeated LogRecord log_records = 2; }
//	InstrumentationScope { string name = 1; }
//	LogRecord     { fixed64 time_unix_nano = 1; AnyValue body = 5;
//	                repeated KeyValue attributes = 6; fixed64 observed_time_unix_nano = 11;
//	                string event_name = 12; }
//	KeyValue      { string key = 1; AnyValue value = 2; }
//	AnyValue      { oneof: string string_value = 1; bool bool_value = 2; int64 int_value = 3;
//	                double double_value = 4; ArrayValue array_value = 5;
//	                KeyValueList kvlist_value = 6; bytes bytes_value = 7; }
//	ArrayValue    { repeated AnyValue values = 1; }
//	KeyValueList  { repeated KeyValue values = 1; }

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

type wire struct{ b []byte }

func malformed(what string) error { return fmt.Errorf("%w: %s", ErrMalformed, what) }

func (w *wire) varint() (uint64, error) {
	var x uint64
	for i := 0; i < 10; i++ {
		if len(w.b) == 0 {
			return 0, malformed("truncated varint")
		}
		c := w.b[0]
		w.b = w.b[1:]
		if i == 9 && c > 1 {
			return 0, malformed("varint overflow")
		}
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, nil
		}
	}
	return 0, malformed("varint overflow")
}

func (w *wire) tag() (num, typ int, err error) {
	k, err := w.varint()
	if err != nil {
		return 0, 0, err
	}
	if k>>3 == 0 || k>>3 > math.MaxInt32 {
		return 0, 0, malformed("bad field number")
	}
	return int(k >> 3), int(k & 7), nil
}

func (w *wire) bytes() ([]byte, error) {
	n, err := w.varint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(w.b)) {
		return nil, malformed("length past the end")
	}
	out := w.b[:n]
	w.b = w.b[n:]
	return out, nil
}

func (w *wire) fixed64() (uint64, error) {
	if len(w.b) < 8 {
		return 0, malformed("truncated fixed64")
	}
	v := binary.LittleEndian.Uint64(w.b)
	w.b = w.b[8:]
	return v, nil
}

func (w *wire) skip(typ int) error {
	var err error
	switch typ {
	case wireVarint:
		_, err = w.varint()
	case wireFixed64:
		_, err = w.fixed64()
	case wireBytes:
		_, err = w.bytes()
	case wireFixed32:
		if len(w.b) < 4 {
			return malformed("truncated fixed32")
		}
		w.b = w.b[4:]
	default:
		return malformed("unsupported wire type")
	}
	return err
}

// fields calls f for every field of the message in b. f reads the value
// of the fields it knows (returning true) and the rest are skipped.
func fields(b []byte, f func(w *wire, num, typ int) (bool, error)) error {
	w := &wire{b}
	for len(w.b) > 0 {
		num, typ, err := w.tag()
		if err != nil {
			return err
		}
		ok, err := f(w, num, typ)
		if err != nil {
			return err
		}
		if !ok {
			if err := w.skip(typ); err != nil {
				return err
			}
		}
	}
	return nil
}

// sub reads a length-delimited field, checking its wire type.
func sub(w *wire, typ int) ([]byte, error) {
	if typ != wireBytes {
		return nil, malformed("wrong wire type")
	}
	return w.bytes()
}

func decodeLogsProto(body []byte) (Logs, error) {
	var (
		out Logs
		b   budget
	)
	err := fields(body, func(w *wire, num, typ int) (bool, error) {
		if num != 1 {
			return false, nil
		}
		m, err := sub(w, typ)
		if err != nil {
			return true, err
		}
		rl, err := resourceLogs(m, &b)
		out.Resources = append(out.Resources, rl)
		return true, err
	})
	if err != nil {
		return Logs{}, err
	}
	return out, nil
}

func resourceLogs(m []byte, b *budget) (ResourceLogs, error) {
	r := ResourceLogs{Attributes: map[string]any{}}
	if err := b.take(); err != nil {
		return r, err
	}
	err := fields(m, func(w *wire, num, typ int) (bool, error) {
		switch num {
		case 1: // Resource
			res, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			return true, fields(res, func(w *wire, num, typ int) (bool, error) {
				if num != 1 {
					return false, nil
				}
				return true, keyValue(w, typ, r.Attributes, b, 0)
			})
		case 2: // ScopeLogs
			sl, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			s, err := scopeLogs(sl, b)
			r.Scopes = append(r.Scopes, s)
			return true, err
		}
		return false, nil
	})
	return r, err
}

func scopeLogs(m []byte, b *budget) (ScopeLogs, error) {
	var s ScopeLogs
	if err := b.take(); err != nil {
		return s, err
	}
	err := fields(m, func(w *wire, num, typ int) (bool, error) {
		switch num {
		case 1: // InstrumentationScope
			sc, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			return true, fields(sc, func(w *wire, num, typ int) (bool, error) {
				if num != 1 {
					return false, nil
				}
				name, err := sub(w, typ)
				s.Name = string(name)
				return true, err
			})
		case 2: // LogRecord
			lr, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			if err := b.take(); err != nil {
				return true, err
			}
			rec, err := logRecord(lr, b)
			s.Records = append(s.Records, rec)
			return true, err
		}
		return false, nil
	})
	return s, err
}

func logRecord(m []byte, b *budget) (LogRecord, error) {
	r := LogRecord{Attributes: map[string]any{}}
	err := fields(m, func(w *wire, num, typ int) (bool, error) {
		var err error
		switch num {
		case 1, 11: // time_unix_nano, observed_time_unix_nano
			if typ != wireFixed64 {
				return true, malformed("wrong wire type")
			}
			v, err := w.fixed64()
			if num == 1 {
				r.TimeUnixNano = v
			} else {
				r.ObservedTimeUnixNano = v
			}
			return true, err
		case 5: // body
			var v []byte
			if v, err = sub(w, typ); err == nil {
				r.Body, err = anyValue(v, b, 0)
			}
			return true, err
		case 6: // attributes
			return true, keyValue(w, typ, r.Attributes, b, 0)
		case 12: // event_name
			var v []byte
			v, err = sub(w, typ)
			r.EventName = string(v)
			return true, err
		}
		return false, nil
	})
	return r, err
}

// keyValue reads one KeyValue field into m.
func keyValue(w *wire, typ int, m map[string]any, b *budget, depth int) error {
	kv, err := sub(w, typ)
	if err != nil {
		return err
	}
	if err := b.take(); err != nil {
		return err
	}
	var (
		key string
		val any
	)
	err = fields(kv, func(w *wire, num, typ int) (bool, error) {
		switch num {
		case 1:
			k, err := sub(w, typ)
			key = string(k)
			return true, err
		case 2:
			v, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			val, err = anyValue(v, b, depth)
			return true, err
		}
		return false, nil
	})
	m[key] = val
	return err
}

func anyValue(m []byte, b *budget, depth int) (any, error) {
	if err := b.take(); err != nil {
		return nil, err
	}
	if depth > maxDepth {
		return nil, malformed("values nested too deep")
	}
	var out any
	err := fields(m, func(w *wire, num, typ int) (bool, error) {
		switch num {
		case 1, 7: // string_value, bytes_value
			v, err := sub(w, typ)
			if num == 1 {
				out = string(v)
			} else {
				out = append([]byte(nil), v...)
			}
			return true, err
		case 2, 3: // bool_value, int_value
			if typ != wireVarint {
				return true, malformed("wrong wire type")
			}
			v, err := w.varint()
			if num == 2 {
				out = v != 0
			} else {
				out = int64(v)
			}
			return true, err
		case 4: // double_value
			if typ != wireFixed64 {
				return true, malformed("wrong wire type")
			}
			v, err := w.fixed64()
			out = math.Float64frombits(v)
			return true, err
		case 5: // array_value
			arr, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			list := []any{}
			err = fields(arr, func(w *wire, num, typ int) (bool, error) {
				if num != 1 {
					return false, nil
				}
				v, err := sub(w, typ)
				if err != nil {
					return true, err
				}
				x, err := anyValue(v, b, depth+1)
				list = append(list, x)
				return true, err
			})
			out = list
			return true, err
		case 6: // kvlist_value
			kvl, err := sub(w, typ)
			if err != nil {
				return true, err
			}
			kv := map[string]any{}
			err = fields(kvl, func(w *wire, num, typ int) (bool, error) {
				if num != 1 {
					return false, nil
				}
				return true, keyValue(w, typ, kv, b, depth+1)
			})
			out = kv
			return true, err
		}
		return false, nil
	})
	return out, err
}
