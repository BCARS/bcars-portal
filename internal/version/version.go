// Package version exposes build and VCS metadata for the BCARS portal.
//
// It merges two sources:
//
//  1. A package-level `version` string that release builds can inject via
//     `-ldflags "-X github.com/bcars/bcars-portal/internal/version.version=v1.2.3"`.
//     Defaults to "dev" for local builds.
//
//  2. `runtime/debug.ReadBuildInfo()` which, for any binary built by
//     `go build` inside a git checkout, carries the commit SHA, commit
//     time, and dirty-tree flag as build settings.
//
// The resulting Info value is safe to log at startup, return from an API,
// or print from a CLI flag.
package version

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// version is overridden at link time. Do not read it directly; call Get().
var version = "dev"

// Info is an immutable snapshot of everything we know about this build.
// Field names are stable — they show up in API responses and log lines.
type Info struct {
	// Version is the release tag (e.g. "v1.2.3") when built with -ldflags,
	// otherwise "dev".
	Version string `json:"version"`

	// Revision is the git commit SHA the binary was built from, or "" when
	// the binary was built outside a VCS checkout (e.g. `go install` from
	// a module cache).
	Revision string `json:"revision,omitempty"`

	// RevisionShort is Revision truncated to 12 characters. Empty when
	// Revision is empty.
	RevisionShort string `json:"revision_short,omitempty"`

	// Time is the commit time in RFC3339 form, or "" when unavailable.
	Time string `json:"time,omitempty"`

	// Modified is true when the source tree had uncommitted changes at
	// build time. Also true when VCS metadata is unavailable and we cannot
	// prove the tree was clean — see the note in Get().
	Modified bool `json:"modified"`

	// GoVersion is the Go toolchain that produced the binary.
	GoVersion string `json:"go_version"`

	// Module is the main module path, e.g. "github.com/bcars/bcars-portal".
	Module string `json:"module,omitempty"`
}

var (
	cacheOnce sync.Once
	cached    Info
)

// Get returns the build info for the current binary. It is safe to call
// concurrently and returns the same value every time within one process.
func Get() Info {
	cacheOnce.Do(func() {
		cached = compute()
	})
	return cached
}

func compute() Info {
	info := Info{
		Version:   strings.TrimSpace(version),
		GoVersion: runtime.Version(),
	}
	if info.Version == "" {
		info.Version = "dev"
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		// No BuildInfo means we cannot prove the tree was clean. Treat as
		// modified so a curious operator investigates.
		info.Modified = true
		return info
	}
	info.Module = bi.Main.Path

	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
			if len(s.Value) >= 12 {
				info.RevisionShort = s.Value[:12]
			} else {
				info.RevisionShort = s.Value
			}
		case "vcs.time":
			info.Time = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// Short returns a one-line identifier suitable for log lines, HTTP response
// headers, or CLI --version output. Examples:
//
//	dev+abc123def456
//	dev+abc123def456-dirty
//	v1.2.3+abc123def456
//	dev                     (no VCS info available)
func (i Info) Short() string {
	var b strings.Builder
	b.WriteString(i.Version)
	if i.RevisionShort != "" {
		b.WriteByte('+')
		b.WriteString(i.RevisionShort)
	}
	if i.Modified {
		b.WriteString("-dirty")
	}
	return b.String()
}

// Long returns a multi-line human description. Trailing newline included so
// it drops cleanly into `fmt.Print`.
func (i Info) Long() string {
	var b strings.Builder
	fmt.Fprintf(&b, "bcars-portal %s\n", i.Short())
	if i.Module != "" {
		fmt.Fprintf(&b, "  module:   %s\n", i.Module)
	}
	if i.Revision != "" {
		fmt.Fprintf(&b, "  revision: %s\n", i.Revision)
	}
	if i.Time != "" {
		fmt.Fprintf(&b, "  built:    %s\n", i.Time)
	}
	fmt.Fprintf(&b, "  go:       %s\n", i.GoVersion)
	if i.Modified {
		b.WriteString("  tree:     modified (uncommitted changes at build time)\n")
	}
	return b.String()
}

// JSON returns the info as a compact JSON byte slice for API responses.
// It never returns an error; the shape is fixed at compile time.
func (i Info) JSON() []byte {
	b, err := json.Marshal(i)
	if err != nil {
		// Impossible for this fixed struct; if it somehow happens we still
		// return a valid JSON error object so callers can serve it.
		return []byte(`{"version":"unknown","error":"marshal failed"}`)
	}
	return b
}
