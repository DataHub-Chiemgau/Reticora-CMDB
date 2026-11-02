#!/usr/bin/env bash
# Summarizes a CPU profile of the server (pprof, RETICORA_PPROF_ADDR) of a
# load run (NFR-02, WP-438, F-117): the functions with the most CPU time,
# then per request handler of api.js its share of the profile and where its
# time goes (database calls and row decoding, JSON serialization, ...),
# cumulative, from the handler down.
#
#   tests/load/cpu-profile-report.sh cpu-api.pprof > cpu-api.txt
#
# Needs go (go tool pprof); the profile carries the function names.
set -euo pipefail

if [ $# -ne 1 ] || [ ! -s "$1" ]; then
	echo "usage: $0 <cpu profile>" >&2
	exit 2
fi
profile=$1

# The handlers of the scenarios of tests/load/api.js.
handlers=(
	'internal/ci\.\(\*Handler\)\.List$'
	'internal/search\.\(\*Handler\)\.Search$'
	'internal/ci\.\(\*Handler\)\.Get$'
	'internal/topology\.\(\*Handler\)\.GetTopology$'
	'internal/topology\.\(\*Handler\)\.GetCIImpact$'
	'internal/relationship\.\(\*Handler\)\.'
	'internal/rack\.\(\*Handler\)\.'
)

echo "== Functions by CPU time (flat)"
go tool pprof -top -nodecount=30 "$profile" 2>/dev/null
echo
echo "== Functions by CPU time including callees (cumulative)"
go tool pprof -top -cum -nodecount=40 "$profile" 2>/dev/null | tail -n +7
for h in "${handlers[@]}"; do
	echo
	echo "== Handler $h, from the handler down (cumulative)"
	go tool pprof -top -cum -nodecount=25 -focus="$h" -show_from="$h" "$profile" 2>/dev/null | tail -n +7 ||
		echo "(no samples)"
done
