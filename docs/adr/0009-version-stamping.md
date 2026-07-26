# ADR-0009: Version stamping via ldflags + runtime/debug

- Status: Accepted
- Date: 2026-07-26

## Context

Every build of `cmd/portal` and `cmd/portalctl` needs a version identifier
usable at the CLI (`--version`), from logs at startup, and (later) as an API
response. Two candidate sources exist:

1. Values injected at link time via `-ldflags "-X ..."`.
2. Values embedded automatically by the Go toolchain and read through
   `runtime/debug.ReadBuildInfo()` — includes the module path, git commit
   SHA, commit time, and a dirty-tree flag.

## Decision

Merge both, with a strict division of responsibility:

- **Release tag** comes from ldflags. The Makefile injects the nearest git
  tag (`git describe --tags --abbrev=0`) into a package-level
  `var version = "dev"` in `internal/version`. Untagged builds render as
  `"dev"`.
- **Revision SHA, commit time, and dirty-tree flag** come from
  `runtime/debug.ReadBuildInfo()`. The Makefile deliberately does **not**
  pass any of these through ldflags. The Go toolchain already embeds them
  faithfully for any `go build` inside a VCS checkout.
- The composed short form is `<tag-or-dev>+<12-char-sha>[-dirty]`. The
  detailed long form is a multi-line human summary.

No runtime shell-out to `git`. No use of `git describe --dirty` at build
time (it would duplicate the dirty suffix and disagree with runtime/debug's
authoritative view of the source tree).

## Rejected alternatives

- **Ldflags for everything**: forces the Makefile to shell out for SHA/time
  and to guess whether the tree is dirty. Duplicates work runtime/debug
  already does correctly.
- **runtime/debug only, no ldflags**: cannot see a release tag; every build
  reports `dev`.
- **A generated `version.go` file committed to the tree**: pollutes commits
  with build-only metadata and creates spurious diffs.

## Consequences

- `go install github.com/bcars/bcars-portal/cmd/portal@v1.2.3` reports
  `"dev"` for the tag (no ldflags) but the full module version is visible
  via `Info.Module` — acceptable because the portal is deployed via
  `make build`, not `go install`.
- Container/CI builds must invoke `make build` (or pass `-ldflags` directly)
  to get a proper tag stamp. Anything less than that is a "dev" build by
  definition, which is the honest label.
- A future release process may want to also inject a build timestamp or a
  build-server identifier; those additions belong in `internal/version.Info`
  as new fields, not as parallel mechanisms.
