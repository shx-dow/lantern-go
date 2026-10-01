// Package version reports which build of Lantern is running.
//
// Values are injected at link time, for example:
//
//	go build -ldflags "-X github.com/shx-dow/lantern-go/internal/version.Version=0.1.0 \
//	                   -X github.com/shx-dow/lantern-go/internal/version.Commit=$(git rev-parse --short HEAD) \
//	                   -X github.com/shx-dow/lantern-go/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// When a binary is built without those flags — `go install` from a module, or
// a plain `go build` — the commit and date are recovered from the embedded
// build info, so a version is never blank when it could have been known.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These are overridden with -ldflags -X at release time.
var (
	// Version is the release version, or "dev" for an untagged build.
	Version = "dev"
	// Commit is the short git revision.
	Commit = ""
	// Date is the build timestamp in RFC 3339.
	Date = ""
)

// Info describes a build.
type Info struct {
	Version string
	Commit  string
	Date    string
	Go      string
	OS      string
	Arch    string
}

// Get returns the build information, filling Commit and Date from the
// embedded build info when the linker did not supply them.
func Get() Info {
	info := Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	if info.Commit != "" && info.Date != "" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = shorten(s.Value)
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		}
	}
	return info
}

// shorten trims a full revision to the 12 characters git uses by default.
func shorten(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// String renders one line for `lantern version`.
func (i Info) String() string {
	s := i.Version
	if i.Commit != "" {
		s += " (" + i.Commit
		if i.Date != "" {
			s += ", " + i.Date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s %s/%s", s, i.Go, i.OS, i.Arch)
}
