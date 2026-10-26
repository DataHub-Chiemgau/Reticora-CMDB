#!/usr/bin/env bash
# Evaluates the load-test evidence of three load.yml runs by the rule of
# E-58 (supplement of 2026-10-26, WP-439):
#
#   tests/load/evaluate-runs.sh <archive-1> <archive-2> <archive-3>
#
# Every argument is the archive of one run, the directory <run-id>/ that
# tests/load/archive-run.sh builds and docs/acceptance/artefakte/lasttest/
# keeps: ergebnis.md, and per job <job>/<job>.json (k6 --summary-export)
# and <job>/result-<job>.txt. A k6 threshold is held when the median of
# its measured values over the three runs is within it. The measured value
# of a run is the stat the threshold names as the summary export reports
# it: p(95) the p95 of the run, max, count, value; rate the rate of a
# counter and the value of a rate metric. A job fails when it is red in at
# least two of the three runs. A job without an artifact counts as red in
# its run and a value that is not there as missed: the median is then the
# worse of the other two values, and there is none when two are missing.
#
# The report goes to stdout as Markdown: the three runs, per job a table
# with every threshold, its value per run, the median and whether it is
# held, the status of the job per run, the verdict of the job, and last
# the line "Bewertung nach E-58 (Median über drei Läufe): ...". The script
# exits 0 when every threshold of every job is held in the median and no
# job is red in two runs, 1 when not, 2 on wrong arguments.
set -euo pipefail

if [ "$#" -ne 3 ]; then
	echo "usage: $0 <archive-1> <archive-2> <archive-3>" >&2
	exit 2
fi
runs=() ids=()
for dir in "$@"; do
	if [ ! -d "$dir" ]; then
		echo "evaluate-runs: $dir is not a directory" >&2
		exit 2
	fi
	if [ ! -f "$dir/ergebnis.md" ]; then
		echo "evaluate-runs: $dir is not a run archive (no ergebnis.md)" >&2
		exit 2
	fi
	id="$(basename "$(cd "$dir" && pwd)")"
	for seen in "${ids[@]}"; do
		if [ "$seen" = "$id" ]; then
			echo "evaluate-runs: run $id is given twice" >&2
			exit 2
		fi
	done
	runs+=("$dir") ids+=("$id")
done

# field FILE KEY: the value after "KEY:" in a record file, else "–".
field() {
	local v
	v="$(sed -n "s/^$2:[[:space:]]*//p" "$1" 2>/dev/null | head -n 1)"
	echo "${v:-–}"
}

# row FILE NAME: the value of the row "| NAME | ... |" of ergebnis.md, else
# "–".
row() {
	local v
	v="$(awk -v p="| $2 | " 'index($0, p) == 1 { v = substr($0, length(p) + 1); sub(/ \|$/, "", v); print v; exit }' "$1")"
	echo "${v:-–}"
}

# The jobs of the three runs: those ergebnis.md lists and those with a
# directory, in the order of the runs.
jobs=()
addjob() {
	local j
	if [[ ! $1 =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
		return
	fi
	for j in "${jobs[@]}"; do
		if [ "$j" = "$1" ]; then
			return
		fi
	done
	jobs+=("$1")
}
for dir in "${runs[@]}"; do
	IFS=',' read -ra listed <<<"$(row "$dir/ergebnis.md" Jobs | tr -d ' ')"
	for job in "${listed[@]}"; do
		addjob "$job"
	done
	for d in "$dir"/*/; do
		if [ -d "$d" ]; then
			addjob "$(basename "$d")"
		fi
	done
done
if [ "${#jobs[@]}" -eq 0 ]; then
	echo "evaluate-runs: no job in the archives" >&2
	exit 2
fi
for dir in "${runs[@]}"; do
	for job in "${jobs[@]}"; do
		f="$dir/$job/$job.json"
		if [ -f "$f" ] && ! jq -e '.metrics | type == "object"' "$f" >/dev/null 2>&1; then
			echo "evaluate-runs: $f is not a k6 summary" >&2
			exit 2
		fi
	done
done

# summaries: per job and run one object {job, run, status, metrics}: the
# status of result-<job>.txt ("fehlt" without the job's directory) and the
# metrics of its k6 summary (null without one).
summaries() {
	local i job dir
	for job in "${jobs[@]}"; do
		for i in 0 1 2; do
			dir="${runs[$i]}/$job"
			if [ ! -d "$dir" ]; then
				jq -cn --arg job "$job" --argjson run "$i" '{$job, $run, status: "fehlt", metrics: null}'
			elif [ -f "$dir/$job.json" ]; then
				jq -c --arg job "$job" --argjson run "$i" --arg status "$(field "$dir/result-$job.txt" status)" \
					'{$job, $run, $status, metrics: .metrics}' "$dir/$job.json"
			else
				jq -cn --arg job "$job" --argjson run "$i" --arg status "$(field "$dir/result-$job.txt" status)" \
					'{$job, $run, $status, metrics: null}'
			fi
		done
	done
}

# evaluate: the tables, verdicts and the overall line from the summaries.
# A threshold "p(95)<500" is the stat, the operator and the bound; a
# missing value takes the worst place (above an upper bound, below a lower
# one), so the median of three is the worse of two present values.
evaluate() {
	jq -rn --argjson jobs "$(printf '%s\n' "${jobs[@]}" | jq -R . | jq -sc .)" \
		--argjson ids "$(printf '%s\n' "${ids[@]}" | jq -R . | jq -sc .)" '
		def unit($m): if ($m | test("duration|delay|flush")) then " ms"
			elif ($m | test("records_per_s$")) then " Records/s" else "" end;
		def num: if . > -1 and . < 1 then (. * 10000 | round) / 10000 else (. * 10 | round) / 10 end | tostring;
		def cell($m): if . == null or isinfinite then "–" else "\(num)\(unit($m))" end;
		def parse: first(capture("^\\s*(?<stat>[a-z]+(\\([0-9.]+\\))?)\\s*(?<op>===|==|!=|<=|>=|<|>)\\s*(?<bound>[-+0-9.eE]+)\\s*$")
			| .bound = (.bound | tonumber?)) // null;
		def value($stat): if type != "object" then null
			elif $stat == "rate" and (has("rate") | not) then .value
			else .[$stat] end | if type == "number" then . else null end;
		def holds($p): . != null and $p != null and (
			if $p.op == "<" then . < $p.bound elif $p.op == "<=" then . <= $p.bound
			elif $p.op == ">" then . > $p.bound elif $p.op == ">=" then . >= $p.bound
			elif $p.op == "!=" then . != $p.bound else . == $p.bound end);
		def worst($p): if $p.op == ">" or $p.op == ">=" then (infinite | -.)
			elif $p.op == "!=" then $p.bound else infinite end;
		def status: if . == "success" then "grün" elif . == "fehlt" then "**fehlt**" else "**rot**" end;

		[inputs] as $all
		| [$jobs[] as $job
			| [range(0; 3) as $i | first($all[] | select(.job == $job and .run == $i))] as $per
			| ([$per[] | select(.status != "success")] | length) as $red
			| [[$per[].metrics // {} | to_entries[] | select(.value | type == "object" and has("thresholds"))
				| .key as $m | .value.thresholds | keys[] | {m: $m, t: .}] | unique[]
				| . as $thr | ($thr.t | parse) as $p
				| [$per[] | .metrics[$thr.m] | value($p.stat // "")] as $vals
				| ($vals | map(. // worst($p)) | sort | .[1]) as $med
				| ($med | holds($p)) as $held
				| {m: $thr.m, t: $thr.t, held: $held,
					row: "| `\($thr.m)` | `\($thr.t)` | \([range(0; 3) as $i
						| if $per[$i].status == "fehlt" then "fehlt" else ($vals[$i] | cell($thr.m)) end] | join(" | ")) | \($med | cell($thr.m)) | \(if $held then "ja" else "**nein**" end) |"}
			] as $rows
			| {job: $job, red: $red, rows: $rows,
				status: "| Status des Jobs | – | \([$per[].status | status] | join(" | ")) | rot in \($red) von 3 | \(if $red < 2 then "ja" else "**nein**" end) |",
				missed: ([$rows[] | select(.held | not) | "`\($job)` `\(.m)` `\(.t)`"]
					+ (if $red >= 2 then ["`\($job)` rot in \($red) von 3 Läufen"] else [] end))}
		] as $res
		| ($res[]
			| "", "## Job `\(.job)`", "",
			"| Metrik | Schwelle | \($ids | join(" | ")) | Median | gehalten |",
			"|---|---|---:|---:|---:|---:|---|",
			(.rows[] | .row), .status, "",
			"Urteil: \(if (.missed | length) == 0 then "gehalten" else "**verfehlt**" end) (Schwellen im Median: \([.rows[] | select(.held)] | length) von \(.rows | length) gehalten; rot in \(.red) von 3 Läufen)."),
		"",
		"Bewertung nach E-58 (Median über drei Läufe): \([$res[].missed[]] | if length == 0 then "alle Budgets gehalten" else "verfehlt: \(join("; "))" end)"
	'
}

report="$(
	echo "# Bewertung über drei Läufe (E-58)"
	echo
	echo "Erzeugt von \`tests/load/evaluate-runs.sh\` aus den Archiven der Läufe ${ids[0]}, ${ids[1]} und ${ids[2]}."
	echo "Regel (E-58, Nachtrag vom 2026-10-26): Eine k6-Schwelle gilt als gehalten, wenn der Median ihrer"
	echo "Messwerte über die drei Läufe sie einhält (bei \`p(95)\` der p95 je Lauf); ein Job gilt als"
	echo "gescheitert, wenn er in mindestens zwei der drei Läufe rot ist. Ein Job ohne Artefakt zählt in"
	echo "seinem Lauf als rot, ein fehlender Messwert als verfehlt."
	echo
	echo "| Lauf-ID | Lauf | Server, Seed und Migrationen | Eingaben | Vollständig | Ergebnis |"
	echo "|---|---|---|---|---|---|"
	for i in 0 1 2; do
		f="${runs[$i]}/ergebnis.md"
		echo "| ${ids[$i]} | $(row "$f" Lauf) | $(row "$f" "Server, Seed und Migrationen") | $(row "$f" Eingaben) | $(row "$f" Vollständig) | $(row "$f" Ergebnis) |"
	done
	summaries | evaluate
)"
printf '%s\n' "$report"
if [ "${report##*$'\n'}" = "Bewertung nach E-58 (Median über drei Läufe): alle Budgets gehalten" ]; then
	exit 0
fi
exit 1
