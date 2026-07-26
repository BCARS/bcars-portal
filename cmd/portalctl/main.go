// Package main is the BCARS portal admin CLI.
//
// Phase 1 status: only --help and --version work. WS4.7 adds bootstrap-admin;
// WS8.2 adds backup/restore.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/bcars/bcars-portal/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage(os.Stdout)
	case "--version":
		fmt.Println(version.Get().Short())
	case "version":
		fmt.Print(version.Get().Long())
	case "bootstrap-admin":
		fmt.Fprintln(os.Stderr, "portalctl bootstrap-admin: not yet implemented (WS4.7).")
		os.Exit(0)
	case "backup", "restore":
		fmt.Fprintln(os.Stderr, "portalctl "+os.Args[1]+": not yet implemented (WS8.2).")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "portalctl: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	_ = flag.CommandLine
}

func usage(w *os.File) {
	fmt.Fprintln(w, `portalctl — BCARS members portal admin CLI.

Phase 1 is under construction. See docs/phase-1-plan.md.

Commands:
  bootstrap-admin --email <addr>   Create the first administrator (WS4.7).
  backup --to <dir>                Encrypted database backup (WS8.2).
  restore --from <path> --into <dir>   Restore an encrypted backup (WS8.2).
  version                          Print detailed build info.
  --version                        Print short version identifier.
  help                             Show this help.`)
}
