//go:build unix

package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that mean "stop now": Ctrl-C, and the
// SIGTERM that systemd and Docker send. Registering only os.Interrupt left
// SIGTERM with its default action, which kills the process without running the
// daemon's shutdown path.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
