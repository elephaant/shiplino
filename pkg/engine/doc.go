// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package engine groups events into sessions, subagents and tasks, computes status and parent roll-ups, and moves kanban cards. Work is sharded by root session so many agents run in parallel.
package engine
