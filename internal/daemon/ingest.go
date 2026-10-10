// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"context"
	"time"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/model"
)

// Ingest stores validated events from the HTTP ingest endpoint or the
// OTLP receiver (api.Ingester). They take the same path as spool events:
// redaction, project annotation, dedup, then the engine. It returns
// api.ErrPaused, storing nothing, while recording is paused.
func (d *Daemon) Ingest(ctx context.Context, events []model.Event) (accepted, duplicates int, err error) {
	if spool.Paused(d.home, time.Now()) {
		return 0, 0, api.ErrPaused
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range events {
		if events[i].User == "" {
			events[i].User = d.user
		}
		d.redactor.Event(&events[i], d.level) // before anything touches disk
	}
	n, err := d.commitCount(ctx, events, nil)
	if err != nil {
		return 0, 0, err
	}
	return int(n), len(events) - int(n), nil
}
