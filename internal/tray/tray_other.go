//go:build !linux && !windows && !darwin

package tray

// Available is false on platforms without a backend.
func Available() bool { return false }

// Run always reports unsupported on platforms without a backend.
func Run(Config) error { return ErrNotSupported }
