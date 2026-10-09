// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package board

import (
	"fmt"
	"time"
)

// Calendar computes a project's automatic sprints: fixed-length windows
// starting on a weekday, numbered from the project's first window.
// Nothing is stored; membership is derived, so sprints need no jobs.
type Calendar struct {
	Origin   time.Time // the project's first activity
	Length   int       // days per sprint; 0 = 7
	StartDow int       // ISO weekday the sprint starts on, 1 = Monday … 7 = Sunday; 0 = Monday
	Loc      *time.Location
}

// Sprint is one window.
type Sprint struct {
	Number int       `json:"number"`
	Name   string    `json:"name"`
	Starts time.Time `json:"starts"`
	Ends   time.Time `json:"ends"` // exclusive
}

func (c Calendar) length() int {
	if c.Length <= 0 {
		return 7
	}
	return c.Length
}

func (c Calendar) loc() *time.Location {
	if c.Loc == nil {
		return time.Local
	}
	return c.Loc
}

func (c Calendar) weekday() time.Weekday {
	if c.StartDow <= 0 || c.StartDow > 7 {
		return time.Monday
	}
	return time.Weekday(c.StartDow % 7)
}

// day is a calendar day number in the calendar's time zone, immune to
// daylight-saving shifts (computed on UTC noon of that civil date).
func (c Calendar) day(t time.Time) int {
	l := t.In(c.loc())
	return int(time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.UTC).Unix() / 86400)
}

// firstDay is the day number sprint 1 starts on: the start weekday on or
// before the origin.
func (c Calendar) firstDay() int {
	o := c.Origin.In(c.loc())
	back := (int(o.Weekday()) - int(c.weekday()) + 7) % 7
	return c.day(o) - back
}

// Number returns the sprint containing t (1-based; earlier times are 1).
func (c Calendar) Number(t time.Time) int {
	d := c.day(t) - c.firstDay()
	if d < 0 {
		return 1
	}
	return d/c.length() + 1
}

// Get returns sprint n.
func (c Calendar) Get(n int) Sprint {
	if n < 1 {
		n = 1
	}
	startDay := c.firstDay() + (n-1)*c.length()
	civil := func(day int) time.Time {
		u := time.Unix(int64(day)*86400, 0).UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, c.loc())
	}
	start, end := civil(startDay), civil(startDay+c.length())
	return Sprint{Number: n, Name: fmt.Sprintf("Sprint %d · %s – %s", n, start.Format("Jan 2"), end.AddDate(0, 0, -1).Format("Jan 2")), Starts: start, Ends: end}
}

// Assign decides a card's sprint: the sprint it started in if it closed
// there; otherwise it rolled over to the sprint it closed in, or to the
// current sprint while still open.
func (c Calendar) Assign(started, ended time.Time, closed bool, now time.Time) (sprint, rolledFrom int) {
	startN := c.Number(started)
	endN := c.Number(now)
	if closed {
		endN = c.Number(ended)
	}
	if endN > startN {
		return endN, startN
	}
	return startN, 0
}
