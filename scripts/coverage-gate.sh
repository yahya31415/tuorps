#!/usr/bin/env bash
# coverage-gate.sh — enforce statement coverage on a Go cover profile.
#
#   coverage-gate.sh <profile> [core_min=90] [overall_min=80]
#
# "core" is every file under an internal/<domain>/core/ directory. Generated
# sqlc code (any sqlcgen/ directory) is excluded. Blocks that appear more than
# once (merged profiles) count once and are covered if hit anywhere.
set -euo pipefail

profile="${1:?usage: coverage-gate.sh <profile> [core_min] [overall_min]}"
core_min="${2:-90}"
overall_min="${3:-80}"
[[ -r "$profile" ]] || {
	echo "coverage-gate: cannot read $profile" >&2
	exit 1
}

awk -v core_min="$core_min" -v overall_min="$overall_min" '
	/^mode:/ { next }
	NF == 3 {
		block = $1
		file = block; sub(/:[^:]*$/, "", file)
		if (file ~ /\/sqlcgen\//) next
		stmts[block] = $2
		if ($3 > 0) hit[block] = 1
		isCore[block] = (file ~ /\/core\//)
	}
	END {
		for (b in stmts) {
			total += stmts[b]; if (b in hit) covered += stmts[b]
			if (isCore[b]) { ctotal += stmts[b]; if (b in hit) ccovered += stmts[b] }
		}
		fail = 0
		if (ctotal > 0) {
			pct = 100 * ccovered / ctotal
			printf "core: %.1f%% (min %s%%)\n", pct, core_min
			if (ccovered * 100 < core_min * ctotal) { print "coverage-gate: core coverage below threshold"; fail = 1 }
		} else {
			print "core: n/a (no core packages)"
		}
		pct = total > 0 ? 100 * covered / total : 100
		printf "overall: %.1f%% (min %s%%)\n", pct, overall_min
		if (covered * 100 < overall_min * total) { print "coverage-gate: overall coverage below threshold"; fail = 1 }
		exit fail
	}
' "$profile"
