//go:build windows

package wrap

import "os"

// relayed: Ctrl-C and Ctrl-Break reach every process on the console,
// the command included. wrap only catches them so it keeps running until
// the command exits and reports its exit code.
var relayed = []os.Signal{os.Interrupt}

// relay does nothing on Windows: the console already delivered the event.
func relay(*os.Process, os.Signal) {}

// exitStatus is the command's exit code.
func exitStatus(ps *os.ProcessState) (int, string) { return ps.ExitCode(), "" }
