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

	# Sum statement counts for the blocks belonging to EXACTLY this package —
	# never its subpackages. A cover.out line is
	# "<importpath>/<file>.go:<from>,<to> <numstmt> <count>", so the package
	# is everything before the final "/", compared for equality.
	#
	# This used to be a prefix match, which made a floor environment-dependent
	# and produced a CI-only failure that could not be reproduced locally: the
	# entry for .../internal/store/postgres also swallowed its child package
	# .../internal/store/postgres/gen (sqlc-generated, no test files). Whether
	# a testless package contributes zero-coverage statements to the profile
	# varies between environments, so the *denominator* moved: 1068/1272 = 84.0%
	# locally versus 1068/1372 = 77.8% in CI, from an identical test run.
	#
	# Exact matching also removes a real blind spot the prefix form had: a
	# regression in a low-coverage subpackage could hide behind a
	# high-coverage sibling, because both were averaged into one number. Every
	# package that should be gated now needs its own explicit entry, which is
	# the honest form of the check. Generated code (.../postgres/gen) is
	# deliberately ungated, matching how .golangci.yml treats it — it is not
	# hand-written, and it is exercised through its callers.
	read -r total covered <<<"$(awk -v pkg="${pkg}" '
		{
			colon = index($1, ":")
			if (colon == 0) next
			file = substr($1, 1, colon - 1)
			slash = 0
			for (i = length(file); i > 0; i--) {
				if (substr(file, i, 1) == "/") { slash = i; break }
			}
			if (slash == 0) next
			if (substr(file, 1, slash - 1) != pkg) next
			total += $2
			if ($3 > 0) covered += $2
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
