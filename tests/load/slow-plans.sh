#!/usr/bin/env bash
# Summarizes the auto_explain entries of a PostgreSQL log (NFR-02, WP-438,
# F-117): per statement (white space collapsed, the first 160 characters of
# its text, or of a long column list its start and what follows FROM) the
# executions over auto_explain.log_min_duration, their total and longest
# duration, by total; then the plan of the longest execution of each of
# them, with its actual rows and buffers.
#
#   docker logs postgres 2>&1 | tests/load/slow-plans.sh > slow-plans.txt
#   tests/load/slow-plans.sh postgres.log > slow-plans.txt
#
# The log is PostgreSQL's stderr format (log_line_prefix '%m [%p] ' of the
# image); a plan continues on the lines that start with a tab.
set -euo pipefail

awk '
function flush(    key, from) {
	if (dur == "") return
	q = text
	gsub(/[ \t]+/, " ", q)
	sub(/^ /, "", q)
	key = substr(q, 1, 160)
	# A long column list (the CI reads share theirs): its start and what
	# follows FROM tell the statements apart.
	from = index(q, " FROM ")
	if (from > 100) key = substr(q, 1, 50) " ..." substr(q, from, 160)
	if (!(key in n)) keys[++nkeys] = key
	n[key]++
	total[key] += dur
	if (dur > longest[key]) { longest[key] = dur; plan[key] = entry }
	dur = ""
}
/LOG:  duration: [0-9.]+ ms  plan:/ {
	flush()
	match($0, /duration: [0-9.]+ ms/)
	dur = substr($0, RSTART + 10, RLENGTH - 13) + 0
	entry = $0; text = ""; inquery = 0
	next
}
dur != "" && /^\t/ {
	entry = entry "\n" $0
	line = $0
	sub(/^\t+/, "", line)
	if (line ~ /^Query Text:/) { sub(/^Query Text: */, "", line); text = line; inquery = 1; next }
	if (line ~ /^Query Parameters:/) { inquery = 0; next }
	# The plan starts with its first node: the query text ends there.
	if (inquery && line ~ /^(Limit|Nested Loop|Hash|Merge|Seq Scan|Index|Bitmap|Sort|Aggregate|GroupAggregate|HashAggregate|Result|Unique|Append|WindowAgg|Subquery Scan|CTE Scan|Gather|Materialize|Incremental Sort|Function Scan|Values Scan|LockRows|ProjectSet|Insert|Update|Delete|Memoize) /) inquery = 0
	if (inquery && length(text) < 2000) text = text " " line
	next
}
{ flush() }
END {
	flush()
	if (nkeys == 0) { print "no statement over auto_explain.log_min_duration"; exit }
	# Order by total duration (insertion sort; a few dozen statements).
	for (i = 1; i <= nkeys; i++) order[i] = keys[i]
	for (i = 2; i <= nkeys; i++) {
		k = order[i]; j = i - 1
		while (j > 0 && total[order[j]] < total[k]) { order[j + 1] = order[j]; j-- }
		order[j + 1] = k
	}
	printf "%6s  %12s  %10s  %s\n", "calls", "total ms", "max ms", "statement"
	for (i = 1; i <= nkeys; i++) printf "%6d  %12.0f  %10.0f  %s\n", n[order[i]], total[order[i]], longest[order[i]], order[i]
	for (i = 1; i <= nkeys; i++) {
		print ""
		printf "== longest execution of: %s\n", order[i]
		print plan[order[i]]
	}
}
' "$@"
