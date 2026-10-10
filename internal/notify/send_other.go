// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

//go:build !linux && !darwin && !windows

package notify

import "context"

func osAvailable() (bool, string) { return false, "" }

func osSend(context.Context, Note) error { return ErrUnavailable }
