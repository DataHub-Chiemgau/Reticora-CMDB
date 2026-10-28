#!/usr/bin/env bash
# Lists the failed requests of a k6 run per request name and status (F-388):
#
#   tests/load/failed-requests.sh <console-log>
#
# <console-log> is the k6 console of the run (k6 run --console-output),
# where tests/load/lib.js logs every request k6 counts in http_req_failed
# as "failed request name=<name> status=<status> <method> <path> <body>".
# The output is Markdown for the job summary: one line with the total and
# a table with name, status (0: no answer, e.g. a timeout) and count, the
# most frequent first, then the first logged line per name and status as
# an example. Without a console log it says so. Exit 0, 2 on wrong
# arguments.
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <console-log>" >&2
	exit 2
fi
log=$1
if [ ! -f "$log" ]; then
	echo "Fehlgeschlagene Anfragen: kein k6-Konsolenprotokoll (\`$(basename "$log")\`)."
	exit 0
fi
pairs="$(grep -oE 'failed request name=[^ ]+ status=[0-9]+' "$log" | sed -E 's/^failed request name=([^ ]+) status=([0-9]+)$/\1 \2/' || true)"
if [ -z "$pairs" ]; then
	echo "Fehlgeschlagene Anfragen: keine."
	exit 0
fi
echo "Fehlgeschlagene Anfragen: $(wc -l <<<"$pairs" | tr -d ' ')"
echo
echo "| Anfrage (\`name\`) | Status | Anzahl |"
echo "|---|---:|---:|"
sort <<<"$pairs" | uniq -c | sort -k1,1nr -k2,2 -k3,3n | awk '{ printf "| `%s` | %s | %s |\n", $2, $3, $1 }'
echo
echo "Erstes Beispiel je Anfrage und Status:"
echo
echo '```text'
awk 'match($0, /failed request name=[^ ]+ status=[0-9]+/) {
	key = substr($0, RSTART, RLENGTH)
	if (!(key in seen)) {
		seen[key] = 1
		line = substr($0, RSTART)
		sub(/" source=console$/, "", line)
		print substr(line, 1, 300)
	}
}' "$log"
echo '```'
