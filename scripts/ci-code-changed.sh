#!/bin/sh
set -eu

# Read changed repository paths, one per line, and print whether expensive
# code-oriented CI jobs should run. Unknown or empty input fails safe by
# running the jobs.
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

if [ "${1:-}" = "--self-test" ]; then
	assert_classification false 'README.md\n'
	assert_classification false 'contrib/review-guide.md\n'
	assert_classification false 'docs/openapi.json\n'
	assert_classification false 'docs/phase-3-plan.md\nAGENTS.md\n'
	assert_classification true 'internal/httpapi/router.go\n'
	assert_classification true '.github/workflows/ci.yml\n'
	assert_classification true 'docs/phase-3-plan.md\nMakefile\n'
	assert_classification true ''
	printf '%s\n' 'ci-code-changed: self-test ok'
	exit 0
fi

classify_paths
