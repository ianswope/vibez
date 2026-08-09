//go:build linux || darwin

package cdp

import (
	"io"
	"log/slog"
)

// driverStderr is what the long-lived Playwright driver process gets for
// stderr. It must never be the terminal.
//
// playwright.Run starts `node cli.js run-driver` through playwright-go's
// newPipeTransport, which pipes the child's stdin and stdout but passes
// RunOptions.Stderr through untouched — and that defaults to os.Stderr, a live
// TTY file descriptor. Node initialises a TTY stream for it and snapshots the
// terminal's termios, by which point BubbleTea has already switched the
// terminal to raw mode, then re-applies that snapshot from its exit handler.
// The driver lives for the whole session and is stopped during shutdown, so
// node's restore lands *after* BubbleTea has put the terminal back into cooked
// mode and silently undoes it, leaving the shell with no echo, no line editing
// and no ctrl+c until the user closes the window.
//
// Returning a plain io.Writer rather than an *os.File also makes os/exec give
// the child a pipe, so driver diagnostics can never scribble over the TUI.
func driverStderr() io.Writer { return io.Discard }

// driverLogger keeps playwright-go away from Go's standard logger.
//
// transformRunOptions calls log.SetOutput(RunOptions.Stderr) whenever Stderr is
// set and Logger is not, so supplying driverStderr() on its own would point the
// whole process's standard log output at io.Discard as a side effect. Passing a
// non-nil logger takes that branch out of play. The only global left is
// playwright-go's own package logger, which vibez already keeps quiet: it is
// written only by PlaywrightDriver.log, and RunOptions.Verbose is false for
// every call site here.
func driverLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
