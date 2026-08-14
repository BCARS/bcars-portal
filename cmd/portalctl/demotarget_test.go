package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The demo portal targets are convenience, and convenience targets are exactly
// where safety properties erode: the next person who needs one more thing to
// work adds a flag, and nothing objects. These tests read the Makefile and
// object.
//
// They live in this package, untagged, alongside the test that keeps seed-demo
// out of default builds, because they guard the same boundary from the other
// side: that one asserts the shipped binary cannot seed, these assert the
// build that can seed never lands where the shipped one is expected.

// makefileTarget returns the recipe lines of a Makefile target, which is every
// line after `name:` up to the next line that starts in column zero.
func makefileTarget(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile("../../Makefile")
	require.NoError(t, err, "Makefile must be readable from the package directory")

	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, name+":") {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "Makefile has no %s target", name)

	var body []string
	for _, line := range lines[start+1:] {
		if line != "" && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") &&
			!strings.HasPrefix(line, "#") {
			break
		}
		body = append(body, line)
	}
	return strings.Join(body, "\n")
}

// TestRunDemoDoesNotWeakenSecurityFlags holds the demo target to the bar the
// ordinary run target already meets.
//
// --allow-insecure-cookies is expected and necessary: signing in over plaintext
// http://localhost cannot work without it, and `run` passes it too. Everything
// else must stay on. --allow-empty-pepper in particular would defeat the reason
// this target exists, since supplying one pepper to both the seeding step and
// the server is the whole point.
func TestRunDemoDoesNotWeakenSecurityFlags(t *testing.T) {
	body := makefileTarget(t, "run-demo")

	assert.NotContains(t, body, "--allow-empty-pepper",
		"run-demo must supply a real pepper; seeding and serving have to agree on one "+
			"or the seeded passwords never verify (bcars-portal-fmc.14)")
	assert.Contains(t, body, "PORTAL_PASSWORD_PEPPER=",
		"run-demo must pass the pepper explicitly rather than relying on the caller's environment")
	assert.NotContains(t, body, "--trusted-proxy-header",
		"a development target has no business trusting a forwarded client address")
}

// TestSeedingAndServingUseOnePepper is the property the target exists for. Two
// separately written pepper values would look correct and produce a portal
// whose published demo passwords are all rejected.
func TestSeedingAndServingUseOnePepper(t *testing.T) {
	raw, err := os.ReadFile("../../Makefile")
	require.NoError(t, err)

	// Every use must be the variable, never an inline literal.
	uses := regexp.MustCompile(`PORTAL_PASSWORD_PEPPER=(\S+)`).FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, uses, "the Makefile should set the pepper for the demo targets")

	for _, u := range uses {
		assert.Equal(t, "$(DEMO_PEPPER)", u[1],
			"the pepper must come from the single DEMO_PEPPER variable, not be written out again")
	}
	assert.GreaterOrEqual(t, len(uses), 2,
		"both the seeding step and the server need it, which is precisely why it is a variable")
}

// TestDemoBuildDoesNotOverwriteTheShippedBinary keeps a binary containing
// seed-demo, and the passwords published beside it, from being written where
// every other target expects the shipped tool.
func TestDemoBuildDoesNotOverwriteTheShippedBinary(t *testing.T) {
	body := makefileTarget(t, "build-demo")

	require.Contains(t, body, "-tags demoseed", "build-demo is the tagged build")
	assert.Contains(t, body, "portalctl-demo",
		"the demoseed build must go to its own name")
	assert.NotRegexp(t, regexp.MustCompile(`-o \S*bin/portalctl(\s|$)`), body,
		"a demoseed binary must never be written to bin/portalctl, where make smoke "+
			"and any manual use would pick it up as the shipped tool")
}

// TestDemoResetCannotBeAimedAtRealData guards an rm -rf whose path comes from
// an overridable variable.
func TestDemoResetCannotBeAimedAtRealData(t *testing.T) {
	body := makefileTarget(t, "demo-reset")

	assert.Contains(t, body, "$(DEMO_DIR)",
		"demo-reset must delete only the demo directory")
	for _, danger := range []string{"$(RUN_DB)", "$(CURDIR)/data", "bcars.db"} {
		assert.NotContains(t, body, danger,
			"demo-reset must not be able to remove %s", danger)
	}
}
