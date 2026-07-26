// Package main is the BCARS portal HTTP server entry point.
//
// Phase 1 status: only --help and --version work. Later workstreams wire in
// the HTTP router (WS2), migrations (WS3.1), authentication (WS4), and so on.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/bcars/bcars-portal/internal/version"
)

func main() {
	fs := flag.NewFlagSet("portal", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `bcars-portal — BCARS members portal server.

Phase 1 is under construction. See docs/phase-1-plan.md.

Usage:
  portal [flags]

Flags:
`)
		fs.PrintDefaults()
	}

	showVersion := fs.Bool("version", false, "print version and exit")
	showVersionLong := fs.Bool("version-long", false, "print detailed build info and exit")
	migrate := fs.Bool("migrate", false, "apply pending migrations at startup (WS3.1+)")
	_ = migrate

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if *showVersionLong {
		fmt.Print(version.Get().Long())
		return
	}
	if *showVersion {
		fmt.Println(version.Get().Short())
		return
	}

	fmt.Fprintln(os.Stderr, "portal: HTTP server not yet implemented (Phase 1 WS2+).")
	fmt.Fprintln(os.Stderr, "Version:", version.Get().Short())
	fmt.Fprintln(os.Stderr, "Run 'portal --help' for available flags.")
	os.Exit(0)
}
