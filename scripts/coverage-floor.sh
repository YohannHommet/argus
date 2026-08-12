#!/usr/bin/env bash
# Enforces per-package coverage floors (SPEC §8.3, PLAN P1-06) against a Go
# coverprofile produced by:
#
#   go test -race -covermode=atomic -coverprofile=cover.out ./...
#
# Floors live in scripts/coverage-floors.txt (one "<package> <min-percent>"
# entry per line). A global percentage target is gameable (one well-tested
# package can hide a completely untested one); per-package floors are not.
#
# Usage: scripts/coverage-floor.sh [cover.out] [floors.txt]
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cover_file="${1:-${script_dir}/../server/cover.out}"
floors_file="${2:-${script_dir}/coverage-floors.txt}"

if [[ ! -f "${cover_file}" ]]; then
	echo "coverage-floor: coverage profile not found: ${cover_file}" >&2
	exit 1
fi

if [[ ! -f "${floors_file}" ]]; then
	echo "coverage-floor: floors file not found: ${floors_file}" >&2
	exit 1
fi

fail=0

while IFS= read -r line || [[ -n "${line}" ]]; do
	# Strip comments and surrounding whitespace; skip blank lines.
	line="${line%%#*}"
	line="$(echo "${line}" | xargs || true)"
	[[ -z "${line}" ]] && continue

	pkg="$(echo "${line}" | awk '{print $1}')"
	floor="$(echo "${line}" | awk '{print $2}')"

	if [[ -z "${pkg}" || -z "${floor}" ]]; then
		echo "coverage-floor: malformed floors line: ${line}" >&2
		exit 1
	fi

	# Sum statement counts for every profiled block whose file belongs to
	# this package (cover.out lines are prefixed with the full module
	# import path, so an exact "<pkg>/" match can't collide with a
	# same-prefix sibling package, e.g. .../store vs .../store/postgres).
	read -r total covered <<<"$(awk -v pkg="${pkg}/" '
		$1 ~ "^" pkg {
			n = $2
			cnt = $3
			total += n
			if (cnt > 0) covered += n
		}
		END { printf "%d %d", total+0, covered+0 }
	' "${cover_file}")"

	if [[ "${total}" -eq 0 ]]; then
		echo "coverage-floor: no coverage data found for package ${pkg} (is the floors entry stale?)" >&2
		fail=1
		continue
	fi

	actual="$(awk -v c="${covered}" -v t="${total}" 'BEGIN { printf "%.1f", (c/t)*100 }')"

	below="$(awk -v a="${actual}" -v f="${floor}" 'BEGIN { print (a+0 < f+0) ? 1 : 0 }')"
	if [[ "${below}" -eq 1 ]]; then
		echo "coverage-floor: FAIL ${pkg}: ${actual}% < floor ${floor}%" >&2
		fail=1
	else
		echo "coverage-floor: ok   ${pkg}: ${actual}% >= floor ${floor}%"
	fi
done <"${floors_file}"

exit "${fail}"
