#!/usr/bin/env bash
# tdd.sh — red/green bookkeeping for the TDD guard (see CLAUDE.md §1).
#
#   tdd.sh red <pkg> <run>   # run the targeted test; record .tdd/red only if it fails for a valid reason
#   tdd.sh green             # rerun the recorded test and its whole package (-race); clear .tdd/red on success
#
# Operates on the Go module in the current directory.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/path.sh
. "$here/lib/path.sh"

readonly RED_FILE=".tdd/red"
readonly RED_MARKER='recorded-by: make red'

die() {
	printf 'tdd: %s\n' "$*" >&2
	exit 1
}

# pkg_dir ./internal/x/... -> internal/x (absolute)
pkg_dir() {
	normpath "${1%/...}"
}

# error_locations <output>: one file path per build error. A package with no
# non-test files (a brand-new package under test) is reported as <dir>/_.
error_locations() {
	{
		grep -oE '^[^[:space:]:]+\.go:[0-9]+(:[0-9]+)?:' <<<"$1" | sed -E 's/:[0-9]+(:[0-9]+)?:$//'
		sed -nE 's/^.*: no non-test Go files in (.+)$/\1\/_/p' <<<"$1"
	} | sort -u
}

# compile_errors_outside <pkg> <output>: print error locations not under the package dir.
compile_errors_outside() {
	local pkg="$1" out="$2" dir recursive=0 file abs
	dir="$(pkg_dir "$pkg")"
	[[ "$pkg" == */... ]] && recursive=1
	while IFS= read -r file; do
		abs="$(normpath "$file")"
		if ((recursive)); then
			[[ "$abs" == "$dir"/* ]] && continue
		else
			[[ "${abs%/*}" == "$dir" ]] && continue
		fi
		printf '%s\n' "$file"
	done < <(error_locations "$out")
}

red() {
	local pkg="${1:-}" run="${2:-}" out status kind outside
	[[ -n "$pkg" ]] || die "PKG is required, e.g. make red PKG=./internal/x/core RUN='^TestName\$'"
	[[ -n "$run" ]] || die "RUN is required: name the single test that describes the next behaviour."

	out="$(go test -count=1 "$pkg" -run "$run" 2>&1)"
	status=$?
	printf '%s\n' "$out"

	if ((status == 0)); then
		if grep -q 'no tests to run' <<<"$out"; then
			die "RUN '$run' matched no tests in $pkg; nothing was recorded."
		fi
		die "the test passed, so it is not red; nothing was recorded. Write a test that fails first."
	fi

	kind="test"
	if grep -qE '\[(build|setup) failed\]|^# ' <<<"$out"; then
		kind="build"
		outside="$(compile_errors_outside "$pkg" "$out")"
		if [[ -n "$outside" ]]; then
			die "refusing to record red: unrelated compile error outside $pkg in:
$outside"
		fi
		if [[ -z "$(error_locations "$out")" ]]; then
			die "refusing to record red: build failed without a locatable compile error."
		fi
	fi

	mkdir -p "$(dirname "$RED_FILE")"
	{
		printf '%s\n' "$RED_MARKER"
		printf 'pkg: %s\n' "$pkg"
		printf 'run: %s\n' "$run"
		printf 'kind: %s\n' "$kind"
		printf 'recorded-at: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf 'head: %s\n' "$(git rev-parse --short HEAD 2>/dev/null || echo none)"
		printf -- '--- failing output ---\n%s\n' "$out"
	} >"$RED_FILE"
	printf '\nRED recorded (%s failure) for %s -run %s. Production edits are allowed for 30 minutes.\n' "$kind" "$pkg" "$run"
}

green() {
	[[ -f "$RED_FILE" ]] || die "no red recorded; run 'make red PKG=… RUN=…' with a failing test first."
	local pkg run out
	pkg="$(sed -n 's/^pkg: //p' "$RED_FILE" | head -n 1)"
	run="$(sed -n 's/^run: //p' "$RED_FILE" | head -n 1)"
	[[ -n "$pkg" && -n "$run" ]] || die "$RED_FILE is malformed; rerun 'make red'."

	if ! out="$(go test -count=1 "$pkg" -run "$run" 2>&1)"; then
		printf '%s\n' "$out"
		die "still red: $pkg -run $run fails."
	fi
	if grep -q 'no tests to run' <<<"$out"; then
		printf '%s\n' "$out"
		die "RUN '$run' no longer matches any test in $pkg."
	fi
	if ! out="$(go test -count=1 -race "$pkg" 2>&1)"; then
		printf '%s\n' "$out"
		die "target test passes but package $pkg has failures."
	fi
	printf '%s\n' "$out"
	rm -f -- "$RED_FILE"
	printf '\nGREEN: %s passes (with -race). Refactor, then commit test: and feat:.\n' "$pkg"
}

case "${1:-}" in
red) red "${2:-}" "${3:-}" ;;
green) green ;;
*) die "usage: tdd.sh red <pkg> <run> | tdd.sh green" ;;
esac
