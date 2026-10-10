//go:build unix

package wrap

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// relayed are the signals wrap catches (so it outlives the command) and
// passes on.
var relayed = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT,
	syscall.SIGWINCH, syscall.SIGUSR1, syscall.SIGUSR2}

// relay passes a signal to the command. The terminal sends Ctrl-C,
// Ctrl-\, hangups and window resizes to the whole foreground process
// group, which includes the command: relaying those again would deliver
// them twice (Aider, for one, exits on a second quick Ctrl-C). They are
// relayed only when wrap isn't in the terminal's foreground group, e.g.
// when someone runs `kill -INT` on wrap in a script.
func relay(p *os.Process, sig os.Signal) {
	switch sig {
	case syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGWINCH:
		if foreground() {
			return
		}
	}
	_ = p.Signal(sig)
}

// foreground reports whether wrap's process group is the foreground group
// of the terminal on stdin, stdout or stderr.
func foreground() bool {
	for _, fd := range []int{0, 1, 2} {
		if pg, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP); err == nil {
			return pg == unix.Getpgrp()
		}
	}
	return false
}

// exitStatus is the command's exit code; a command killed by a signal
// exits 128+n, as shells report it.
func exitStatus(ps *os.ProcessState) (int, string) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), unix.SignalName(ws.Signal())
	}
	return ps.ExitCode(), ""
}
