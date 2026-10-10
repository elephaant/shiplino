//go:build windows

package wrap

import "os"

// helperMode: the signal tests are Unix only.
func helperMode(string) { os.Exit(99) }
