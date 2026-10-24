//go:build windows

package main

import "os"

// shutdownSignals is Ctrl-C. Windows consoles deliver no SIGTERM; service
// stop requests arrive through the service manager, not this process.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
