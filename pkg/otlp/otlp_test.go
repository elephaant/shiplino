package otlp

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

const sid = "claude-code:0f0e0d0c-1111-4222-8333-444455556666"

var received = time.Date(2026, 10, 10, 10, 1, 0, 0, time.UTC)

// fixtures returns the synthetic Claude Code export as OTLP/JSON and as
// OTLP/protobuf. The .pb file was encoded from the .json one by the
// OpenTelemetry Collector's pdata library (v1.67.0), so the decoder is
// checked against an independent encoder.
func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for ct, f := range map[string]string{"application/json": "claude-code-logs.json", "application/x-protobuf": "claude-code-logs.pb"} {
		b, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		out[ct] = b
	}
	return out
}

func TestJSONAndProtobufDecodeTheSame(t *testing.T) {
	fx := fixtures(t)
	js, err := DecodeLogs(fx["application/json"], "application/json")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := DecodeLogs(fx["application/x-protobuf"], "application/x-protobuf")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(js, pb) {
		t.Fatalf("JSON and protobuf differ:\n%+v\n%+v", js, pb)
	}
	r := pb.Resources[0]
	if r.Attributes["service.name"] != "claude-code" || r.Scopes[0].Name != "com.anthropic.claude_code.events" || len(r.Scopes[0].Records) != 6 {
		t.Fatalf("resource: %+v", r)
	}
	rec := r.Scopes[0].Records[1]
	if rec.TimeUnixNano != 1791626402000000000 || rec.Body != "claude_code.api_request" || rec.Attributes["cost_usd"] != 0.125 || rec.Attributes["input_tokens"] != int64(1200) {
		t.Fatalf("record: %+v", rec)
	}
}

func TestClaudeCodeLogs(t *testing.T) {
	for ct, body := range fixtures(t) {
		t.Run(ct, func(t *testing.T) {
			ld, err := DecodeLogs(body, ct)
			if err != nil {
				t.Fatal(err)
			}
			events, st := Map(ld, Meta{ReceivedAt: received, User: "dev"})
			if want := (Stats{Records: 8, Events: 5, Ignored: 2, Unknown: 2}); st != want {
				t.Fatalf("stats = %+v, want %+v", st, want)
			}
			for _, e := range events {
				if err := e.Validate(); err != nil {
					t.Errorf("%s: %v", e.Kind, err)
				}
				if e.SessionID != sid || e.Collector != model.CollectorOTLP || e.User != "dev" || e.DedupKey == "" {
					t.Errorf("%s: %+v", e.Kind, e)
				}
			}

			turn := events[0]
			if turn.Kind != model.KindTurnStart || turn.TurnID != "a1b2c3d4-0000-4000-8000-000000000001" || turn.Data["prompt_chars"] != 42 {
				t.Errorf("turn: %+v", turn)
			}
			if _, ok := turn.Data["prompt"]; ok {
				t.Errorf("redacted placeholder stored as the prompt: %v", turn.Data)
			}
			if !turn.TS.Equal(time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)) {
				t.Errorf("turn ts = %s", turn.TS)
			}

			req := events[1]
			want := map[string]any{
				"model": "claude-opus-5", "input_tokens": int64(1200), "output_tokens": int64(300), "cache_read_tokens": int64(50000),
				"cache_write_tokens": int64(800), "cost_usd": 0.125, "cost_source": "reported", "duration_ms": int64(2100),
				"request_id": "req_test0001", "query_source": "repl_main_thread",
			}
			for k, v := range want {
				if req.Data[k] != v {
					t.Errorf("usage %s = %#v, want %#v", k, req.Data[k], v)
				}
			}
			if req.Kind != model.KindUsage || req.DedupKey != sid+":otlp:request:req_test0001" || req.Agent.Version != "2.1.300" {
				t.Errorf("usage: %+v", req)
			}
			// Numbers sent as strings or doubles still count.
			if bg := events[2]; bg.Data["cost_usd"] != 0.0025 || bg.Data["input_tokens"] != int64(900) || bg.Data["output_tokens"] != int64(40) {
				t.Errorf("background usage: %v", bg.Data)
			}

			start, end := events[3], events[4]
			if start.Kind != model.KindToolStart || start.DedupKey != sid+":toolu_test01:start" || start.Data["tool"] != "shell" || start.Data["tool_raw"] != "Bash" {
				t.Errorf("tool.start: %+v", start)
			}
			if end.Kind != model.KindToolEnd || end.DedupKey != sid+":toolu_test01:end" || end.Data["ok"] != true || end.Data["duration_ms"] != int64(1500) {
				t.Errorf("tool.end: %+v", end)
			}
			if d := end.TS.Sub(start.TS); d != 1500*time.Millisecond {
				t.Errorf("tool start is %s before its end, want the reported duration", d)
			}
		})
	}
}

// The event name may come only as the bare event.name attribute; the
// resource's service.name then says which agent sent it. JSON quirks:
// int64 as a number or string, NaN doubles, hex trace ids.
func TestBareEventNameAndJSONQuirks(t *testing.T) {
	body := `{"resourceLogs":[{"resource":{"attributes":[
		{"key":"service.name","value":{"stringValue":"claude-code"}},
		{"key":"session.id","value":{"stringValue":"s9"}}]},
	  "scopeLogs":[{"logRecords":[{
		"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",
		"attributes":[
		  {"key":"event.name","value":{"stringValue":"api_request"}},
		  {"key":"cost_usd","value":{"doubleValue":"NaN"}},
		  {"key":"cost_usd_micros","value":{"intValue":2500}},
		  {"key":"input_tokens","value":{"intValue":"7"}},
		  {"key":"event.sequence","value":{"intValue":"7"}},
		  {"key":"tags","value":{"arrayValue":{"values":[{"stringValue":"a"},{"kvlistValue":{"values":[{"key":"k","value":{"boolValue":true}}]}}]}}},
		  {"key":"raw","value":{"bytesValue":"AAE="}}]}]}]}]}`
	ld, err := DecodeLogs([]byte(body), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	a := ld.Resources[0].Scopes[0].Records[0].Attributes
	if !reflect.DeepEqual(a["tags"], []any{"a", map[string]any{"k": true}}) || !reflect.DeepEqual(a["raw"], []byte{0, 1}) {
		t.Errorf("nested values: %#v %#v", a["tags"], a["raw"])
	}
	events, st := Map(ld, Meta{ReceivedAt: received})
	if st.Events != 1 || events[0].SessionID != "claude-code:s9" || !events[0].TS.Equal(received) {
		t.Fatalf("%+v %+v", st, events)
	}
	// A NaN cost can't be stored: the micros figure is used instead.
	if e := events[0]; e.Data["cost_usd"] != 0.0025 || e.Data["input_tokens"] != int64(7) {
		t.Errorf("data: %v", e.Data)
	}
	if events[0].DedupKey != "claude-code:s9:otlp:request:1791626460000000000:7" {
		t.Errorf("fallback dedup key = %q", events[0].DedupKey)
	}
}

func TestContentTypes(t *testing.T) {
	for ct, want := range map[string]error{
		"application/json":                nil,
		"application/json; charset=utf-8": nil,
		"application/x-protobuf":          nil,
		"text/plain":                      ErrContentType,
		"":                                ErrContentType,
	} {
		if _, err := WantsJSON(ct); !errors.Is(err, want) {
			t.Errorf("%q: %v", ct, err)
		}
	}
	if _, err := DecodeLogs([]byte("not json"), "application/json"); !errors.Is(err, ErrMalformed) {
		t.Errorf("garbage JSON: %v", err)
	}
	if _, err := DecodeLogs([]byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"soon"}]}]}]}`), "application/json"); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad timestamp: %v", err)
	}
}
