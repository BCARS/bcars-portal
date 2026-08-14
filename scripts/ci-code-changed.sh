#!/bin/sh
set -eu

# Decide whether the expensive code-oriented CI jobs need to run for a change.
#
# Two entry points:
#
#   ci-code-changed.sh                       read paths on stdin, one per line
#   ci-code-changed.sh --range <base> <head> derive the paths from git first
#
# The --range form exists because deriving them wrongly is what
# bcars-portal-8cm was filed for. CI compared the base tip against the head tip
# directly, so every commit that landed on main after a branch point counted as
# part of that branch: PR #75 changed only README.md and docs/, and ran
# build-test and sqlc-diff anyway because main had moved. The comparison has to
# start at the merge base — what the branch introduced — not at wherever the
# base branch happens to be now.
#
# Everything fails safe. Unknown paths, empty input, missing history, an
# unusable ref, or a comparison that cannot be made all print `true` and run
# the jobs. A skipped gate is invisible; a redundant one only costs minutes.

# classify_paths reads paths on stdin and prints true when any of them is code.
classify_paths() {
	seen=0
	while IFS= read -r path || [ -n "$path" ]; do
		[ -z "$path" ] && continue
		seen=1
		case "$path" in
			docs/* | *.md)
				;;
			*)
				printf '%s\n' true
				return
				;;
		esac
	done

	if [ "$seen" -eq 0 ]; then
		printf '%s\n' true
	else
		printf '%s\n' false
	fi
}

# changed_paths prints the paths a head ref introduces relative to a base ref,
# measured from the point the two last had in common.
#
# `git diff A...B` is that comparison: it diffs merge-base(A,B) against B, so a
# base branch that has advanced independently contributes nothing. The
# two-endpoint `git diff A B` used before answers a different question — how the
# two tips differ — which includes commits the branch never touched.
#
# Returns non-zero when the comparison cannot be made, which the caller treats
# as "run the jobs".
changed_paths() {
	base=$1
	head=$2

	[ -n "$base" ] || return 1
	[ -n "$head" ] || return 1

	# An all-zero SHA is git's "nothing was here": a newly created branch or
	# tag on a push event. There is no range to measure.
	case "$base" in
		0000000000000000000000000000000000000000) return 1 ;;
	esac

	# Both endpoints must resolve. A shallow clone that lacks the base, a
	# deleted branch, or a typo lands here.
	git rev-parse --verify --quiet "${base}^{commit}" >/dev/null 2>&1 || return 1
	git rev-parse --verify --quiet "${head}^{commit}" >/dev/null 2>&1 || return 1

	# Unrelated histories (a force-push that rewrote the branch, a grafted
	# shallow clone) have no merge base. Nothing meaningful can be diffed.
	git merge-base "$base" "$head" >/dev/null 2>&1 || return 1

	git diff --name-only --diff-filter=ACDMRTUXB "${base}...${head}"
}

classify_range() {
	base=$1
	head=$2

	if ! paths=$(changed_paths "$base" "$head" 2>/dev/null); then
		# Fail safe: if the range cannot be established, run everything.
		printf '%s\n' true
		return
	fi

	printf '%s\n' "$paths" | classify_paths
}

# --- self-test -------------------------------------------------------------

assert_classification() {
	expected=$1
	paths=$2
	actual=$(printf '%b' "$paths" | classify_paths)
	if [ "$actual" != "$expected" ]; then
		printf 'ci-code-changed: expected %s, got %s for:\n%b' \
			"$expected" "$actual" "$paths" >&2
		exit 1
	fi
}

assert_range() {
	expected=$1
	base=$2
	head=$3
	description=$4
	actual=$(classify_range "$base" "$head")
	if [ "$actual" != "$expected" ]; then
		printf 'ci-code-changed: expected %s, got %s for %s (%s..%s)\n' \
			"$expected" "$actual" "$description" "$base" "$head" >&2
		exit 1
	fi
}

# git_fixture builds a throwaway repository and leaves the shell inside it.
git_fixture() {
	# A git hook or a wrapper may have pointed these at the real repository.
	# The fixture must be its own repository or the assertions below would be
	# measuring this one.
	unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE 2>/dev/null || true

	FIXTURE_DIR=$(mktemp -d)
	cd "$FIXTURE_DIR"
	git -c init.defaultBranch=main init -q .
	git config user.email ci-self-test@invalid
	git config user.name 'CI Self Test'
	git config commit.gpgsign false
}

commit_file() {
	path=$1
	mkdir -p "$(dirname "$path")"
	printf '%s\n' "$2" >"$path"
	git add "$path"
	git commit -q -m "$path"
}

run_git_self_tests() {
	start_dir=$(pwd)
	git_fixture

	commit_file README.md 'start'
	git branch -q docs-only
	git branch -q mixed
	git branch -q code-only

	# The base branch advances with an unrelated code change AFTER the
	# branches are cut. This is the condition that broke PR #75.
	commit_file internal/httpapi/router.go 'base moved on'

	git checkout -q docs-only
	commit_file docs/phase-2-progress.md 'docs edit'
	commit_file README.md 'readme edit'

	# The reproduction has to actually reproduce: the old two-endpoint
	# comparison must see the base branch's code commit. Without this check a
	# later change could make the scenario vacuous while the assertion below
	# still passed.
	if ! git diff --name-only main docs-only | grep -q 'internal/httpapi/router.go'; then
		printf 'ci-code-changed: SELF-TEST BROKEN — the advanced-base scenario no longer reproduces\n' >&2
		exit 1
	fi
	if [ "$(git diff --name-only main docs-only | classify_paths)" != "true" ]; then
		printf 'ci-code-changed: SELF-TEST BROKEN — the old comparison no longer misclassifies\n' >&2
		exit 1
	fi

	assert_range false main docs-only 'docs-only branch with an advanced base'

	git checkout -q mixed
	commit_file docs/phase-2-progress.md 'docs edit'
	commit_file Makefile 'code edit'
	assert_range true main mixed 'branch touching both docs and code'

	git checkout -q code-only
	commit_file cmd/portal/main.go 'code edit'
	assert_range true main code-only 'branch touching only code'

	# A push: the range is the previous tip to the new one. Same code path,
	# because for a fast-forward the merge base IS the previous tip.
	git checkout -q main
	before=$(git rev-parse HEAD)
	commit_file docs/deployment.md 'docs edit on main'
	assert_range false "$before" HEAD 'fast-forward push of a docs commit'
	commit_file internal/db/schema.sql 'code edit on main'
	assert_range true "$before" HEAD 'fast-forward push including code'

	# Fail-safe cases. Each of these must run the jobs rather than skip them.
	assert_range true 0000000000000000000000000000000000000000 HEAD 'branch creation'
	assert_range true deadbeefdeadbeefdeadbeefdeadbeefdeadbeef HEAD 'unknown base sha'
	assert_range true HEAD deadbeefdeadbeefdeadbeefdeadbeefdeadbeef 'unknown head sha'
	assert_range true '' HEAD 'empty base'
	assert_range true HEAD '' 'empty head'
	assert_range true HEAD HEAD 'empty range'

	# Unrelated histories: no merge base to measure from.
	git checkout -q --orphan orphan
	git rm -rqf .
	commit_file docs/orphan.md 'orphan'
	assert_range true main orphan 'unrelated histories'

	cd "$start_dir"
	rm -rf "$FIXTURE_DIR"
}

if [ "${1:-}" = "--self-test" ]; then
	assert_classification false 'README.md\n'
	assert_classification false 'contrib/review-guide.md\n'
	assert_classification false 'docs/openapi.json\n'
	assert_classification false 'docs/phase-3-plan.md\nAGENTS.md\n'
	assert_classification true 'internal/httpapi/router.go\n'
	assert_classification true '.github/workflows/ci.yml\n'
	assert_classification true 'docs/phase-3-plan.md\nMakefile\n'
	assert_classification true ''

	run_git_self_tests

	printf '%s\n' 'ci-code-changed: self-test ok'
	exit 0
fi

if [ "${1:-}" = "--range" ]; then
	if [ "$#" -ne 3 ]; then
		printf 'usage: %s --range <base> <head>\n' "$0" >&2
		exit 2
	fi
	classify_range "$2" "$3"
	exit 0
fi

classify_paths
