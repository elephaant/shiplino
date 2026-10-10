package adapters

import (
	"fmt"
	"time"
)

// Limit events (kind "limit") carry the state of a plan usage window as the
// agent reports it. Their data fields are metadata only:
//
//	limit_window    "5h", "7d", "30d": the window's length (see LimitWindow)
//	window_minutes  the same length in minutes
//	limit_id        the agent's name for the limit, when it has several
//	used_percent    0-100, absent when the agent doesn't say
//	limit_reached   true when the agent refused a request at this limit
//	resets_at       when the window resets (RFC 3339, UTC)
//	plan_type       the plan the agent names, e.g. "plus"
//	limit_source    "reported"

// LimitWindow names a window by its length: "5h", "7d", "30d", or minutes
// ("90m") when it isn't a whole number of hours.
func LimitWindow(minutes int64) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes%1440 == 0:
		return fmt.Sprintf("%dd", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// LimitData builds the data of a limit event. used < 0 means unknown.
func LimitData(minutes int64, limitID string, used float64, resets time.Time, plan string) map[string]any {
	d := map[string]any{"limit_window": LimitWindow(minutes), "window_minutes": minutes, "limit_source": "reported"}
	if limitID != "" {
		d["limit_id"] = limitID
	}
	if used >= 0 {
		d["used_percent"] = used
	}
	if !resets.IsZero() {
		d["resets_at"] = resets.UTC().Format(time.RFC3339)
	}
	if plan != "" {
		d["plan_type"] = plan
	}
	return d
}
