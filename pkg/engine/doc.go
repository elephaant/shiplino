// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package engine groups events into sessions, subagents and tasks, computes status and parent roll-ups, and moves kanban cards. An Engine is fed from one goroutine; folding an event is cheap next to parsing and storing it.
package engine
