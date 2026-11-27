//go:build !linux && !darwin && !windows

package service

import (
	"context"
	"fmt"
	"runtime"
)

// Install is not available on this platform. The foreground `lantern daemon`
// command still works; it just has no per-user registration to install.
func Install(context.Context) (Status, error) {
	return Status{}, fmt.Errorf("no per-user service mechanism for %s; run \"lantern daemon\" under your init system", runtime.GOOS)
}

// Uninstall is not available on a platform with no per-user registration.
func Uninstall(context.Context) (bool, error) {
	return false, fmt.Errorf("no per-user service mechanism for %s; run \"lantern daemon\" under your init system", runtime.GOOS)
}

// Inspect reports that the platform is unsupported rather than guessing.
func Inspect(context.Context) (Status, error) {
	return Status{
		Platform: runtime.GOOS,
		Detail:   "no per-user service mechanism on " + runtime.GOOS,
	}, nil
}
