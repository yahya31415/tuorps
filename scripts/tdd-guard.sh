#!/usr/bin/env bash
# tdd-guard.sh — Claude Code PreToolUse hook enforcing red-first TDD (see CLAUDE.md §1).
#
#   tdd-guard.sh edit   # matcher Edit|Write|MultiEdit: block production .go edits without a fresh red
#   tdd-guard.sh bash   # matcher Bash: block hand-made changes to .tdd/ state
#
# Reads the hook payload (JSON) on stdin. Exit 0 allows the tool call; exit 2
# blocks it and the stderr message is shown to the agent. Fails closed.
set -euo pipefail

# Claude Code treats any exit code other than 2 as a non-blocking error, so
# an unexpected failure must still block (fail closed).
# shellcheck disable=SC2329 # invoked by the EXIT trap below
on_exit() {
	local rc=$?
	if [ "$rc" -ne 0 ] && [ "$rc" -ne 2 ]; then
		printf 'tdd-guard: internal error (exit %s); blocking to be safe.\n' "$rc" >&2
		exit 2
	fi
}
trap on_exit EXIT

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/path.sh
. "$here/lib/path.sh"

readonly RED_MAX_AGE_MIN=30
readonly RED_MARKER='recorded-by: make red'
JQ="${TDD_GUARD_JQ:-jq}"

block() {
	printf 'tdd-guard: %s\n' "$*" >&2
	exit 2
}

command -v "$JQ" >/dev/null 2>&1 || block "jq is required to parse hook input; install jq."

mode="${1:-}"
input="$(cat)"
printf '%s' "$input" | "$JQ" -e 'type == "object"' >/dev/null 2>&1 ||
	block "could not parse hook input as a JSON object."

field() { printf '%s' "$input" | "$JQ" -r "$1 // empty"; }

project="$(field .cwd)"
project="${CLAUDE_PROJECT_DIR:-${project:-$PWD}}"
project="$(normpath "$project")"

guard_edit() {
	local path rel
	path="$(field .tool_input.file_path)"
	[[ -z "$path" ]] && exit 0
	[[ "$path" == /* ]] || path="$project/$path"
	path="$(normpath "$path")"

	case "$path" in
	"$project"/*) rel="${path#"$project"/}" ;;
	*) exit 0 ;; # outside the project: not ours to guard
	esac

	case "$rel" in
	.tdd | .tdd/*) block "$rel is managed by 'make red' / 'make green' and cannot be edited directly." ;;
	esac

	case "$rel" in
	*.go) ;;
	*) exit 0 ;; # docs, configs, scripts, migrations SQL …
	esac

	case "/$rel" in
	*_test.go | */testdata/* | */docs/* | */migrations/* | */sqlcgen/* | *_gen.go) exit 0 ;;
	esac

	if [[ -f "$path" ]] && head -n 5 -- "$path" | grep -Eq '^// Code generated .* DO NOT EDIT\.$'; then
		exit 0
	fi

	local red="$project/.tdd/red"
	if [[ -f "$red" ]] && grep -qxF "$RED_MARKER" "$red" &&
		[[ -n "$(find "$red" -mmin "-$RED_MAX_AGE_MIN" 2>/dev/null)" ]]; then
		exit 0
	fi

	block "refusing to edit production file $rel: no failing test recorded in the last ${RED_MAX_AGE_MIN} minutes.
Follow CLAUDE.md §1: write the failing test first, then run
    make red PKG=./path/to/pkg RUN='^TestName\$'
and retry this edit. After the test passes, run 'make green'."
}

guard_bash() {
	local cmd
	cmd="$(field .tool_input.command)"
	[[ "$cmd" == *.tdd* ]] || exit 0
	# Read-only inspection of the guard state is fine; anything else must go through make.
	if [[ "$cmd" =~ ^[[:space:]]*(cat|ls|head|tail|stat)[[:space:]] ]] && [[ ! "$cmd" =~ [\;\&\|\<\>\`\$] ]]; then
		exit 0
	fi
	block "the .tdd/ state may only be changed by 'make red' and 'make green'."
}

case "$mode" in
edit) guard_edit ;;
bash) guard_bash ;;
*) block "unknown mode '${mode}' (expected 'edit' or 'bash')." ;;
esac
