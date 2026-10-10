// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package otlp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"strconv"
	"strings"
)

// A minimal OTLP logs decoder (stdlib only). It reads the parts of an
// ExportLogsServiceRequest Shiplino maps: resource attributes, scope
// names and log records (time_unix_nano, observed_time_unix_nano,
// event_name, body, attributes). Everything else is skipped. Field
// numbers follow opentelemetry/proto/logs/v1/logs.proto and
// common/v1/common.proto.

// Logs is a decoded logs export.
type Logs struct {
	Resources []ResourceLogs
}

// ResourceLogs are the records of one resource (one agent process).
type ResourceLogs struct {
	Attributes map[string]any
	Scopes     []ScopeLogs
}

// ScopeLogs are the records of one instrumentation scope.
type ScopeLogs struct {
	Name    string
	Records []LogRecord
}

// LogRecord is one log record. Attribute and body values are string,
// bool, int64, float64, []byte, []any or map[string]any.
type LogRecord struct {
	TimeUnixNano         uint64
	ObservedTimeUnixNano uint64
	EventName            string
	Body                 any
	Attributes           map[string]any
}

var (
	// ErrContentType is returned for a body that is neither OTLP protobuf nor JSON.
	ErrContentType = errors.New("otlp: content type must be application/x-protobuf or application/json")
	// ErrMalformed is returned for a body that doesn't decode.
	ErrMalformed = errors.New("otlp: malformed export")
	// ErrTooMany is returned when an export holds more items than MaxItems.
	ErrTooMany = errors.New("otlp: too many records or attributes in one export")
)

// MaxItems caps the records, attributes and values decoded from one
// export, so a small body of empty messages can't allocate without bound.
const MaxItems = 200_000

// maxDepth caps nested array and map values.
const maxDepth = 16

// WantsJSON reports whether contentType selects OTLP/JSON (else protobuf),
// or returns ErrContentType.
func WantsJSON(contentType string) (bool, error) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false, ErrContentType
	}
	switch mt {
	case "application/x-protobuf", "application/protobuf":
		return false, nil
	case "application/json":
		return true, nil
	}
	return false, ErrContentType
}

// DecodeLogs decodes an OTLP/HTTP logs export body.
func DecodeLogs(body []byte, contentType string) (Logs, error) {
	isJSON, err := WantsJSON(contentType)
	if err != nil {
		return Logs{}, err
	}
	if isJSON {
		return decodeLogsJSON(body)
	}
	return decodeLogsProto(body)
}

// budget counts decoded items against MaxItems.
type budget int

func (b *budget) take() error {
	if *b++; *b > MaxItems {
		return ErrTooMany
	}
	return nil
}

// --- OTLP/JSON ---
//
// OTLP/JSON is the protobuf JSON mapping with lowerCamelCase keys. 64-bit
// integers (timestamps, intValue) come as strings or numbers, doubles may
// be "NaN" or "Infinity", bytes are base64, and trace and span ids are
// hex (unused here). Unknown keys are ignored.

type jsonLogs struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []jsonKV `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			LogRecords []struct {
				TimeUnixNano         jsonInt  `json:"timeUnixNano"`
				ObservedTimeUnixNano jsonInt  `json:"observedTimeUnixNano"`
				EventName            string   `json:"eventName"`
				Body                 *jsonAny `json:"body"`
				Attributes           []jsonKV `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type jsonKV struct {
	Key   string  `json:"key"`
	Value jsonAny `json:"value"`
}

type jsonAny struct {
	StringValue *string    `json:"stringValue"`
	BoolValue   *bool      `json:"boolValue"`
	IntValue    *jsonInt   `json:"intValue"`
	DoubleValue *jsonFloat `json:"doubleValue"`
	ArrayValue  *struct {
		Values []jsonAny `json:"values"`
	} `json:"arrayValue"`
	KvlistValue *struct {
		Values []jsonKV `json:"values"`
	} `json:"kvlistValue"`
	BytesValue []byte `json:"bytesValue"`
}

// jsonInt is a 64-bit integer sent as a JSON string or number.
type jsonInt int64

func (n *jsonInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "null" || s == "" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = jsonInt(v)
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64) // nanosecond timestamps fit uint64
	if err != nil {
		return fmt.Errorf("%w: integer %s", ErrMalformed, b)
	}
	*n = jsonInt(v)
	return nil
}

// jsonFloat is a double sent as a number or as "NaN", "Infinity", "-Infinity".
type jsonFloat float64

func (f *jsonFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	switch s {
	case "NaN":
		*f = jsonFloat(math.NaN())
	case "Infinity":
		*f = jsonFloat(math.Inf(1))
	case "-Infinity":
		*f = jsonFloat(math.Inf(-1))
	default:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("%w: double %s", ErrMalformed, b)
		}
		*f = jsonFloat(v)
	}
	return nil
}

func decodeLogsJSON(body []byte) (Logs, error) {
	var in jsonLogs
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&in); err != nil {
		return Logs{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	var (
		out Logs
		b   budget
	)
	for _, rl := range in.ResourceLogs {
		r := ResourceLogs{}
		var err error
		if r.Attributes, err = jsonAttrs(rl.Resource.Attributes, &b, 0); err != nil {
			return Logs{}, err
		}
		for _, sl := range rl.ScopeLogs {
			s := ScopeLogs{Name: sl.Scope.Name}
			for _, lr := range sl.LogRecords {
				if err := b.take(); err != nil {
					return Logs{}, err
				}
				rec := LogRecord{TimeUnixNano: uint64(lr.TimeUnixNano), ObservedTimeUnixNano: uint64(lr.ObservedTimeUnixNano), EventName: lr.EventName}
				if lr.Body != nil {
					if rec.Body, err = lr.Body.value(&b, 0); err != nil {
						return Logs{}, err
					}
				}
				if rec.Attributes, err = jsonAttrs(lr.Attributes, &b, 0); err != nil {
					return Logs{}, err
				}
				s.Records = append(s.Records, rec)
			}
			r.Scopes = append(r.Scopes, s)
		}
		out.Resources = append(out.Resources, r)
	}
	return out, nil
}

func jsonAttrs(kvs []jsonKV, b *budget, depth int) (map[string]any, error) {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		v, err := kv.Value.value(b, depth)
		if err != nil {
			return nil, err
		}
		m[kv.Key] = v
	}
	return m, nil
}

func (a *jsonAny) value(b *budget, depth int) (any, error) {
	if err := b.take(); err != nil {
		return nil, err
	}
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: values nested too deep", ErrMalformed)
	}
	switch {
	case a.StringValue != nil:
		return *a.StringValue, nil
	case a.BoolValue != nil:
		return *a.BoolValue, nil
	case a.IntValue != nil:
		return int64(*a.IntValue), nil
	case a.DoubleValue != nil:
		return float64(*a.DoubleValue), nil
	case a.ArrayValue != nil:
		out := make([]any, 0, len(a.ArrayValue.Values))
		for i := range a.ArrayValue.Values {
			v, err := a.ArrayValue.Values[i].value(b, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case a.KvlistValue != nil:
		return jsonAttrs(a.KvlistValue.Values, b, depth+1)
	case a.BytesValue != nil:
		return a.BytesValue, nil
	}
	return nil, nil
}
