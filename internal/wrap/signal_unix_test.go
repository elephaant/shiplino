// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unix

package wrap

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func helperMode(mode string) {
	switch mode {
	case "wrapper": // run `shiplino wrap` around this binary in trap mode
		os.Setenv("WRAP_TEST_MODE", os.Getenv("WRAP_TEST_CHILD"))
		exe, _ := os.Executable()
		os.Exit(Run([]string{"--", exe}, os.Stdin, os.Stdout, os.Stderr))
	case "trap": // report the first SIGTERM or SIGINT and exit 7 or 8
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
		fmt.Println("ready")
		switch <-c {
		case syscall.SIGTERM:
			fmt.Println("got SIGTERM")
			os.Exit(7)
		default:
			fmt.Println("got SIGINT")
			os.Exit(8)
		}
	case "sleep": // die from the default action of whatever signal comes
		fmt.Println("ready")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(99)
}

// startWrapper runs the test binary as `shiplino wrap -- <self in child
// mode>` and waits until the child is ready for signals.
func startWrapper(t *testing.T, child string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	setHome(t)
	cmd := exec.Command(self(t))
	cmd.Env = append(os.Environ(), "WRAP_TEST_MODE=wrapper", "WRAP_TEST_CHILD="+child)
	// Its own session: no controlling terminal, as in a script.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	r := bufio.NewReader(stdout)
	ready := make(chan string, 1)
	go func() { l, _ := r.ReadString('\n'); ready <- l }()
	select {
	case l := <-ready:
		if strings.TrimSpace(l) != "ready" {
			t.Fatalf("child said %q", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child never got ready")
	}
	return cmd, r
}

func exitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(10 * time.Second):
		t.Fatal("wrapper did not exit")
	}
	return -1
}

func TestSignalsReachTheCommand(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code int
		said string
	}{
		{syscall.SIGTERM, 7, "got SIGTERM"},
		{syscall.SIGINT, 8, "got SIGINT"},
	} {
		t.Run(c.sig.String(), func(t *testing.T) {
			cmd, out := startWrapper(t, "trap")
			if err := cmd.Process.Signal(c.sig); err != nil {
				t.Fatal(err)
			}
			line, _ := out.ReadString('\n')
			if code := exitCode(t, cmd); code != c.code || strings.TrimSpace(line) != c.said {
				t.Fatalf("exit %d, said %q", code, line)
			}
		})
	}
}

func TestKilledCommandExitsLikeAShell(t *testing.T) {
	cmd, _ := startWrapper(t, "sleep")
	cmd.Process.Signal(syscall.SIGTERM)
	if code := exitCode(t, cmd); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("exit %d", code)
	}
}
