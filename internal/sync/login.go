// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package sync

import (
	"context"
	"errors"
	"time"
)

// pollUnit is one second of the device flow's interval; tests shrink it.
var pollUnit = time.Second

// Login runs the device flow (RFC 8628): it gets a code, calls show so
// the user can approve it in a browser, then polls until the user
// approves, denies, or the code expires.
func Login(ctx context.Context, c *Client, show func(DeviceCode)) (*Token, error) {
	d, err := c.StartDevice(ctx)
	if err != nil {
		return nil, err
	}
	show(d)
	interval := time.Duration(max(d.Interval, 1)) * pollUnit
	if d.Interval == 0 {
		interval = 5 * pollUnit // RFC 8628 §3.2 default
	}
	expires := time.Duration(d.ExpiresIn) * pollUnit
	if d.ExpiresIn <= 0 {
		expires = 15 * time.Minute / time.Second * pollUnit
	}
	deadline := time.Now().Add(expires)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		t, err := c.PollDevice(ctx, d.DeviceCode)
		switch {
		case err == nil:
			return t, nil
		case errors.Is(err, ErrSlowDown):
			interval += 5 * pollUnit
		case errors.Is(err, ErrPending):
		case errors.Is(err, ErrExpired), errors.Is(err, ErrDenied), IsStatus(err, 400, 401, 403, 404):
			return nil, err
		default:
			// A network blip or a 5xx: keep polling until the code expires.
		}
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
	}
}
