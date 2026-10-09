// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package shim is the hook fast path: read the hook payload from stdin, append one line to the session's spool file, and exit 0. It must never print anything (zero-token contract, docs/how-it-works.md).
package shim
