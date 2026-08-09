//go:build linux || darwin

package cdp

import (
	"log"
	"os"
	"testing"

	playwright "github.com/mxschmitt/playwright-go"
)

// The driver must never be handed a terminal file descriptor. os/exec passes an
// *os.File straight through to the child, so anything but a plain io.Writer
// lets the node driver reach the user's terminal — see driverio.go for what
// that costs.
func TestDriverStderr_NeverATerminal(t *testing.T) {
	w := driverStderr()
	if w == nil {
		t.Fatal("driverStderr() = nil; playwright-go would default it to os.Stderr and hand the driver the user's terminal")
	}
	if f, ok := w.(*os.File); ok {
		t.Fatalf("driverStderr() = *os.File(%q); os/exec passes the descriptor through unchanged, so the driver can reach the terminal", f.Name())
	}
}

// Setting RunOptions.Stderr without RunOptions.Logger makes playwright-go
// redirect Go's process-wide standard logger to the same writer, which would
// silently discard log output for every package in the binary. Exercise the
// real dependency rather than trusting the read: this fails if driverLogger
// goes away or playwright-go moves the branch.
func TestDriverRunOptions_LeaveTheStandardLoggerAlone(t *testing.T) {
	before := log.Writer()
	t.Cleanup(func() { log.SetOutput(before) })

	if _, err := playwright.NewDriver(&playwright.RunOptions{
		DriverDirectory: t.TempDir(),
		Stderr:          driverStderr(),
		Logger:          driverLogger(),
	}); err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	if after := log.Writer(); after != before {
		t.Errorf("playwright-go repointed the standard logger to %T; RunOptions.Logger must stay non-nil to avoid its log.SetOutput branch", after)
	}
}
