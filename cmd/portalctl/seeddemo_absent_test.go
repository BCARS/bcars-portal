//go:build !demoseed

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSeedDemoAbsentFromDefaultBuild is the acceptance test for
// bcars-portal-fmc.11's first half: a default build — the one `make build` and
// the release pipeline produce — must not carry a command that creates
// accounts with published passwords.
func TestSeedDemoAbsentFromDefaultBuild(t *testing.T) {
	_, ok := demoCommands["seed-demo"]
	assert.False(t, ok, "seed-demo must not be dispatchable in a default build")
	assert.Empty(t, demoCommands, "no development-only commands may be registered in a default build")
}

// TestUsageDoesNotAdvertiseSeedDemoAsAvailable guards the help text: it may
// explain that seed-demo needs a tagged build, but must not list it among the
// commands this binary accepts.
func TestUsageDoesNotAdvertiseSeedDemoAsAvailable(t *testing.T) {
	assert.Empty(t, demoCommandUsage)
	assert.Empty(t, demoEnvUsage)

	out := captureStdout(t, func() { usage(os.Stdout) })
	assert.Contains(t, out, "-tags demoseed",
		"help should tell developers how to get seed-demo")
	assert.NotContains(t, strings.SplitN(out, "Development builds only:", 2)[0], "seed-demo",
		"the command list must not offer seed-demo")
}
