//go:build unix

package statusline

import (
	"os"
	"os/signal"
	"syscall"
)

// relaySignals passes stop signals on to the command, so when Claude
// Code cancels a status line run (a newer update arrived) the command
// stops as it would without the wrapper. stop ends the relay.
func relaySignals(p *os.Process) (stop func()) {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = p.Signal(s)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(sigs)
		close(done)
	}
}

// gitBash is Windows only.
func gitBash() string { return "" }
