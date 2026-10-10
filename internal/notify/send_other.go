//go:build !linux && !darwin && !windows

package notify

import "context"

func osAvailable() (bool, string) { return false, "" }

func osSend(context.Context, Note) error { return ErrUnavailable }
