package adapters

import (
	"testing"
	"time"
)

func TestLimitWindow(t *testing.T) {
	for minutes, want := range map[int64]string{300: "5h", 10080: "7d", 43200: "30d", 1440: "1d", 90: "90m", 60: "1h", 0: "", -5: ""} {
		if got := LimitWindow(minutes); got != want {
			t.Errorf("LimitWindow(%d) = %q, want %q", minutes, got, want)
		}
	}
}

func TestLimitData(t *testing.T) {
	d := LimitData(300, "", -1, time.Time{}, "")
	if len(d) != 3 || d["limit_window"] != "5h" || d["window_minutes"] != int64(300) || d["limit_source"] != "reported" {
		t.Fatalf("unknown percent and reset: %v", d)
	}
	d = LimitData(10080, "opus", 12.5, time.Unix(1791900000, 0), "max")
	if d["used_percent"] != 12.5 || d["resets_at"] != "2026-10-13T14:00:00Z" || d["limit_id"] != "opus" || d["plan_type"] != "max" {
		t.Fatalf("full: %v", d)
	}
}
