package otlp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"testing"
)

// Protobuf builders for malformed-input tests.
func tag(num, typ int) []byte { return binary.AppendUvarint(nil, uint64(num<<3|typ)) }

func msg(num int, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := append(tag(num, wireBytes), binary.AppendUvarint(nil, uint64(len(b)))...)
	return append(out, b...)
}

func str(num int, s string) []byte { return msg(num, []byte(s)) }

// record wraps LogRecord fields in a request: resource_logs → scope_logs → log_records.
func record(fields ...[]byte) []byte { return msg(1, msg(2, msg(2, fields...))) }

func TestMalformedProtobuf(t *testing.T) {
	huge := append(tag(1, wireBytes), binary.AppendUvarint(nil, 1<<62)...)
	cases := map[string][]byte{
		"truncated tag":             {0x80},
		"huge length":               huge,
		"length past the end":       append(tag(1, wireBytes), 5, 1, 2),
		"varint overflow":           append(tag(1, wireVarint), bytes.Repeat([]byte{0xff}, 11)...),
		"field number zero":         {0x02, 0x00},
		"group wire type":           append(tag(1, 3), 0),
		"wire type 7":               append(tag(1, 7), 0),
		"resource_logs as varint":   append(tag(1, wireVarint), 1),
		"time as varint":            record(append(tag(1, wireVarint), 1)),
		"truncated fixed64":         record(append(tag(1, wireFixed64), 1, 2, 3)),
		"attribute value as varint": record(msg(6, str(1, "k"), append(tag(2, wireVarint), 1))),
		"int_value as bytes":        record(msg(6, str(1, "k"), msg(2, str(3, "x")))),
		"double as fixed32":         record(msg(6, str(1, "k"), msg(2, append(tag(4, wireFixed32), 1, 2, 3, 4)))),
		"truncated fixed32 skip":    record(append(tag(99, wireFixed32), 1)),
	}
	for name, b := range cases {
		_, err := DecodeLogs(b, "application/x-protobuf")
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestUnknownProtobufFieldsAreSkipped(t *testing.T) {
	b := record(
		append(tag(2, wireVarint), 9),                    // severity_number
		append(tag(8, wireFixed32), 1, 0, 0, 0),          // flags
		msg(9, bytes.Repeat([]byte{0xab}, 16)),           // trace_id
		append(tag(50, wireFixed64), make([]byte, 8)...), // a future field
		str(12, "claude_code.user_prompt"),
	)
	ld, err := DecodeLogs(b, "application/x-protobuf")
	if err != nil {
		t.Fatal(err)
	}
	if r := ld.Resources[0].Scopes[0].Records[0]; r.EventName != "claude_code.user_prompt" {
		t.Fatalf("record: %+v", r)
	}
}

func TestProtobufLimits(t *testing.T) {
	// Many empty records in a small body: refused before allocating them all.
	empty := msg(2) // an empty LogRecord, 2 bytes
	scope := msg(2, bytes.Repeat(empty, MaxItems+1))
	if _, err := DecodeLogs(msg(1, scope), "application/x-protobuf"); !errors.Is(err, ErrTooMany) {
		t.Errorf("too many records: %v", err)
	}
	// Deeply nested array values.
	v := str(1, "leaf")
	for range maxDepth + 2 {
		v = msg(5, msg(1, v)) // AnyValue{array_value: ArrayValue{values: [v]}}
	}
	if _, err := DecodeLogs(record(msg(5, v)), "application/x-protobuf"); !errors.Is(err, ErrMalformed) {
		t.Errorf("deep nesting: %v", err)
	}
}

// Every prefix and many random corruptions of a valid export decode
// without panicking: they either succeed or return an error.
func TestCorruptedProtobufNeverPanics(t *testing.T) {
	valid := fixtures(t)["application/x-protobuf"]
	for n := range len(valid) {
		_, _ = DecodeLogs(valid[:n], "application/x-protobuf")
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		b := append([]byte(nil), valid...)
		for range 1 + r.IntN(4) {
			b[r.IntN(len(b))] = byte(r.Uint32())
		}
		if ld, err := DecodeLogs(b, "application/x-protobuf"); err == nil {
			Map(ld, Meta{ReceivedAt: received})
		}
	}
}

func FuzzDecodeLogsProto(f *testing.F) {
	f.Add(fixtures(f)["application/x-protobuf"])
	f.Add(record(str(12, "claude_code.api_request"), msg(6, str(1, "session.id"), msg(2, str(1, "s")))))
	f.Fuzz(func(t *testing.T, b []byte) {
		if ld, err := DecodeLogs(b, "application/x-protobuf"); err == nil {
			Map(ld, Meta{ReceivedAt: received})
		}
	})
}

func FuzzDecodeLogsJSON(f *testing.F) {
	f.Add(fixtures(f)["application/json"])
	f.Fuzz(func(t *testing.T, b []byte) {
		if ld, err := DecodeLogs(b, "application/json"); err == nil {
			Map(ld, Meta{ReceivedAt: received})
		}
	})
}
