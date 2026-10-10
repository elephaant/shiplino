package daemon

import (
	"os"
	"testing"
)

// TestMain points HOME at an empty temp dir so no test reads the real
// agent folders (transcript discovery scans the user's home).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "shiplino-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
