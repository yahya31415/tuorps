# shellcheck shell=bash
# Portable path helpers for the TDD scripts. Bash 3.2 compatible (macOS
# /bin/bash) and free of GNU-only tools such as `realpath -m`.

# normpath PATH: print PATH made absolute (against $PWD) and normalised
# lexically: "//" and "." collapse, ".." removes the previous segment.
# Symlinks are not resolved, so both sides of a comparison must come from
# the same lexical base.
normpath() {
	local p="$1" out="" seg rest
	case "$p" in
	/*) ;;
	*) p="$PWD/$p" ;;
	esac
	rest="$p/"
	while [ -n "$rest" ]; do
		seg="${rest%%/*}"
		rest="${rest#*/}"
		case "$seg" in
		"" | .) ;;
		..) out="${out%/*}" ;;
		*) out="$out/$seg" ;;
		esac
	done
	printf '%s\n' "${out:-/}"
}
