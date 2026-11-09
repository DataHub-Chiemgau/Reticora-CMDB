#!/usr/bin/env bash
# Evaluates the load-test evidence of three load.yml runs by the rule of
# E-58 (supplement of 2026-10-26, WP-439):
#
#   tests/load/evaluate-runs.sh [--partial | --gate G1|G2] <archive-1> <archive-2> <archive-3>
#
# Every argument is the archive of one run, the directory <run-id>/ that
# tests/load/archive-run.sh builds and docs/acceptance/artefakte/lasttest/
# keeps: ergebnis.md, and per job <job>/<job>.json (k6 --summary-export)
# and <job>/result-<job>.txt; a write job of writes.js (import, template,
# computed; WP-823) also <job>/<job>-ingest.json, the summary of the ingest
# beside it. A k6 threshold is held when the median of its measured values
# over the three runs is within it. The measured value of a run is the
# stat the threshold names as the summary export reports it: p(95) the
# p95 of the run, max, count, value; rate the rate of a counter and the
# value of a rate metric. A job fails when it is red in at least two of the
# three runs. A job without an artifact counts as red in its run and a
# value that is not there as missed: the median is then the worse of the
# other two values, and there is none when two are missing.
#
# The thresholds of the ingest beside a write job (<job>-ingest.json:
# NFR-03 with ingest_visible_delay and ingest_records_missing, the failed
# requests) are evaluated with the job by the same rule, each marked
# "(paralleler Ingest)". A write job none of whose runs has that summary
# misses its budgets ("ohne Zusammenfassung des parallelen Ingest").
#
# The evidence of NFR-10 (WP-206, WP-612: "Drei vollständige Läufe",
# F-417) needs three runs of the same code, each complete: the three runs
# name the same commit under "Server, Seed und Migrationen" in ergebnis.md,
# and every run has each job of the gate, listed under "Jobs" and with its
# directory. --gate selects these jobs (WP-327):
#
#   G2 (default; the evidence of gate G2 and load-evidence.yml): the five
#      jobs of G1 as regression and the four jobs of phase 2: api, ingest,
#      ingest-spool, ingest-rediscover, nfr01-volume, phase2 (WP-822),
#      import, template and computed (WP-823);
#   G1 the five jobs of the G1 evidence (WP-439, WP-612): api, ingest,
#      ingest-spool, ingest-rediscover and nfr01-volume. It keeps the
#      archives of runs before WP-822 evaluable (E-58: the evaluation is
#      reproducible); its last line names the job set G1.
#
# Otherwise the runs are not evaluable: the last line of the report is
# "Bewertung nach E-58 (Median über drei Läufe): nicht bewertbar: ...",
# each reason also goes to stderr, and the script exits 1. The medians are
# still reported. Jobs the runs have beyond those of the gate (api-large-*)
# are evaluated as well.
#
# --partial is a separate mode for a diagnosis, never the evidence: it
# evaluates the jobs any of the three runs lists or has (a job missing in a
# run counts as red there, as above), needs the same server commit as
# well, and its last line starts with "Teilbewertung nach E-58 (Median
# über drei Läufe, kein G1-Nachweis)"; it is no G2 evidence either.
#
# The report goes to stdout as Markdown: the three runs, the
# preconditions, per job a table with every threshold, its value per run,
# the median and whether it is held, the status of the job per run, the
# verdict of the job, and last the line "Bewertung nach E-58 (Median über
# drei Läufe): ..." (--gate G1: "Bewertung nach E-58 (Median über drei
# Läufe, Jobsatz G1): ..."; --partial: "Teilbewertung ..."). The script
# exits 0 when the runs are evaluable, every threshold of every job is held
# in the median and no job is red in two runs, 1 when not, 2 on wrong
# arguments.
set -euo pipefail

usage() {
	echo "usage: $0 [--partial | --gate G1|G2] <archive-1> <archive-2> <archive-3>" >&2
	exit 2
}
partial=false gate=
while [ "$#" -gt 0 ]; do
	case "$1" in
	--partial)
		partial=true
		shift
		;;
	--gate)
		if [ "$#" -lt 2 ] || [ -n "$gate" ]; then
			usage
		fi
		gate=$2
		shift 2
		;;
	*) break ;;
	esac
done
case "$gate" in
'' | G1 | G2) ;;
*)
	echo "evaluate-runs: unknown gate '$gate' (G1 or G2)" >&2
	exit 2
	;;
esac
if $partial && [ -n "$gate" ]; then
	echo "evaluate-runs: --partial evaluates the jobs of the runs, --gate those of a gate: not both" >&2
	exit 2
fi
gate=${gate:-G2}
if [ "$#" -ne 3 ]; then
	usage
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

# The jobs of load.yml (scenarios=all) a complete run of the gate has:
# G1 the five jobs of the G1 evidence, G2 these as regression and the four
# jobs of phase 2 (WP-822, WP-823; WP-327).
g1_jobs=(api ingest ingest-spool ingest-rediscover nfr01-volume)
if [ "$gate" = G1 ]; then
	all_jobs=("${g1_jobs[@]}")
else
	all_jobs=("${g1_jobs[@]}" phase2 import template computed)
fi
# The write jobs of writes.js: the ingest beside them (<job>-ingest.json)
# holds the budgets of NFR-03 (WP-823).
ingest_jobs=(import template computed)

# join ITEM...: the items separated by ", ".
join() {
	local out
	out="$(printf '%s, ' "$@")"
	echo "${out%, }"
}

# The jobs of the three runs: those ergebnis.md lists and those with a
# directory, in the order of the runs; without --partial the jobs of the
# gate first.
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
if ! $partial; then
	archived=("${jobs[@]}")
	jobs=()
	for job in "${all_jobs[@]}" "${archived[@]}"; do
		addjob "$job"
	done
fi
for dir in "${runs[@]}"; do
	for job in "${jobs[@]}"; do
		for f in "$dir/$job/$job.json" "$dir/$job/$job-ingest.json"; do
			if [ -f "$f" ] && ! jq -e '.metrics | type == "object"' "$f" >/dev/null 2>&1; then
				echo "evaluate-runs: $f is not a k6 summary" >&2
				exit 2
			fi
		done
	done
done

# The preconditions (F-417): the same server commit in the three runs and,
# without --partial, each job of the gate in every run (listed under
# "Jobs" and with its directory). blocked collects what is not met.
blocked=() commits=() nocommit=()
for i in 0 1 2; do
	c="$(row "${runs[$i]}/ergebnis.md" "Server, Seed und Migrationen")"
	commits+=("$c")
	case "$c" in
	'' | '–' | "\`–\`" | "\`\`") nocommit+=("${ids[$i]}") ;;
	esac
done
if [ "${#nocommit[@]}" -gt 0 ]; then
	blocked+=("kein Server-Commit (Server, Seed und Migrationen) in Lauf $(join "${nocommit[@]}")")
elif [ "${commits[0]}" != "${commits[1]}" ] || [ "${commits[0]}" != "${commits[2]}" ]; then
	blocked+=("verschiedene Server-Commits (Server, Seed und Migrationen): ${ids[0]} ${commits[0]}, ${ids[1]} ${commits[1]}, ${ids[2]} ${commits[2]}")
fi
if ! $partial; then
	for job in "${all_jobs[@]}"; do
		without=()
		for i in 0 1 2; do
			IFS=',' read -ra listed <<<"$(row "${runs[$i]}/ergebnis.md" Jobs | tr -d ' ')"
			found=false
			for l in "${listed[@]}"; do
				if [ "$l" = "$job" ]; then
					found=true
				fi
			done
			if ! $found || [ ! -d "${runs[$i]}/$job" ]; then
				without+=("${ids[$i]}")
			fi
		done
		if [ "${#without[@]}" -gt 0 ]; then
			blocked+=("Job \`$job\` fehlt in Lauf $(join "${without[@]}")")
		fi
	done
fi
for b in "${blocked[@]}"; do
	echo "evaluate-runs: nicht bewertbar: $b" >&2
done
if $partial; then
	prefix="Teilbewertung nach E-58 (Median über drei Läufe, kein G1-Nachweis)"
elif [ "$gate" = G1 ]; then
	prefix="Bewertung nach E-58 (Median über drei Läufe, Jobsatz G1)"
else
	prefix="Bewertung nach E-58 (Median über drei Läufe)"
fi

# metrics FILE: the metrics of a k6 summary, null without one.
metrics() {
	if [ -f "$1" ]; then
		jq -c .metrics "$1"
	else
		echo null
	fi
}

# summaries: per job and run one object {job, run, status, metrics,
# ingest}: the status of result-<job>.txt ("fehlt" without the job's
# directory), the metrics of its k6 summary and those of the ingest beside
# it (<job>-ingest.json), each null without one.
summaries() {
	local i job dir
	for job in "${jobs[@]}"; do
		for i in 0 1 2; do
			dir="${runs[$i]}/$job"
			if [ ! -d "$dir" ]; then
				jq -cn --arg job "$job" --argjson run "$i" '{$job, $run, status: "fehlt", metrics: null, ingest: null}'
			else
				jq -cn --arg job "$job" --argjson run "$i" --arg status "$(field "$dir/result-$job.txt" status)" \
					--argjson metrics "$(metrics "$dir/$job.json")" --argjson ingest "$(metrics "$dir/$job-ingest.json")" \
					'{$job, $run, $status, $metrics, $ingest}'
			fi
		done
	done
}

# evaluate: the tables, verdicts and the overall line from the summaries.
# A threshold "p(95)<500" is the stat, the operator and the bound; a
# missing value takes the worst place (above an upper bound, below a lower
# one), so the median of three is the worse of two present values. The
# thresholds of the ingest beside a write job follow those of the job.
evaluate() {
	jq -rn --argjson jobs "$(printf '%s\n' "${jobs[@]}" | jq -R . | jq -sc .)" \
		--argjson ingestJobs "$(printf '%s\n' "${ingest_jobs[@]}" | jq -R . | jq -sc .)" \
		--argjson ids "$(printf '%s\n' "${ids[@]}" | jq -R . | jq -sc .)" \
		--argjson blocked "$(printf '%s\n' "${blocked[@]}" | jq -R 'select(. != "")' | jq -sc .)" --arg prefix "$prefix" '
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
		def source($s): if $s == "ingest" then .ingest else .metrics end;
		def mark($s): if $s == "ingest" then " (paralleler Ingest)" else "" end;

		[inputs] as $all
		| [$jobs[] as $job
			| [range(0; 3) as $i | first($all[] | select(.job == $job and .run == $i))] as $per
			| ([$per[] | select(.status != "success")] | length) as $red
			| [("metrics", "ingest") as $s
				| [$per[] | source($s) // {} | to_entries[] | select(.value | type == "object" and has("thresholds"))
				| .key as $m | .value.thresholds | keys[] | {s: $s, m: $m, t: .}] | unique[]
				| . as $thr | ($thr.t | parse) as $p
				| [$per[] | source($thr.s) | if type == "object" then .[$thr.m] else null end | value($p.stat // "")] as $vals
				| ($vals | map(. // worst($p)) | sort | .[1]) as $med
				| ($med | holds($p)) as $held
				| {m: $thr.m, t: $thr.t, s: $thr.s, held: $held,
					row: "| `\($thr.m)`\(mark($thr.s)) | `\($thr.t)` | \([range(0; 3) as $i
						| if $per[$i].status == "fehlt" then "fehlt" else ($vals[$i] | cell($thr.m)) end] | join(" | ")) | \($med | cell($thr.m)) | \(if $held then "ja" else "**nein**" end) |"}
			] as $rows
			| (($ingestJobs | index($job)) != null and ([$per[] | select(.ingest != null)] | length) == 0) as $noIngest
			| {job: $job, red: $red, rows: $rows, noIngest: $noIngest,
				status: "| Status des Jobs | – | \([$per[].status | status] | join(" | ")) | rot in \($red) von 3 | \(if $red < 2 then "ja" else "**nein**" end) |",
				missed: ([$rows[] | select(.held | not) | "`\($job)`\(if .s == "ingest" then " paralleler Ingest" else "" end) `\(.m)` `\(.t)`"]
					+ (if $noIngest then ["`\($job)` ohne Zusammenfassung des parallelen Ingest (`\($job)-ingest.json`)"] else [] end)
					+ (if $red >= 2 then ["`\($job)` rot in \($red) von 3 Läufen"] else [] end))}
		] as $res
		| ($res[]
			| "", "## Job `\(.job)`", "",
			"| Metrik | Schwelle | \($ids | join(" | ")) | Median | gehalten |",
			"|---|---|---:|---:|---:|---:|---|",
			(.rows[] | .row), .status,
			(if .noIngest then "| Paralleler Ingest | – | fehlt | fehlt | fehlt | – | **nein** |" else empty end), "",
			"Urteil: \(if (.missed | length) == 0 then "gehalten" else "**verfehlt**" end) (Schwellen im Median: \([.rows[] | select(.held)] | length) von \(.rows | length) gehalten\(if .noIngest then ", keine Zusammenfassung `\(.job)-ingest.json` des parallelen Ingest" else "" end); rot in \(.red) von 3 Läufen)."),
		"",
		"\($prefix): \(if ($blocked | length) > 0 then "nicht bewertbar: \($blocked | join("; "))"
			else [$res[].missed[]] | if length == 0 then "alle Budgets gehalten" else "verfehlt: \(join("; "))" end end)"
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
	echo "In den Schreibjobs $(printf "\`%s\`, " "${ingest_jobs[@]}" | sed 's/, $//') gelten zusätzlich die Schwellen des"
	echo "parallelen Ingest (\`<job>-ingest.json\`, WP-823) nach derselben Regel."
	echo
	if $partial; then
		echo "Teilbewertung (\`--partial\`), kein G1-Nachweis: bewertet sind die Jobs, die einer der drei Läufe"
		echo "nennt oder hat. Voraussetzung: derselbe Server-Commit in allen drei Läufen."
	else
		echo "Jobsatz $gate (\`--gate $gate\`). Voraussetzungen (WP-206, NFR-10): derselbe Server-Commit in allen"
		echo "drei Läufen und in jedem Lauf jeder der ${#all_jobs[@]} Jobs $(printf "\`%s\`, " "${all_jobs[@]}" | sed 's/, $//') (in der Zeile Jobs genannt und mit Verzeichnis)."
	fi
	if [ "${#blocked[@]}" -eq 0 ]; then
		echo "Voraussetzungen erfüllt."
	else
		echo "Voraussetzungen **nicht erfüllt**: $(printf '%s; ' "${blocked[@]}" | sed 's/; $//')."
	fi
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
if [ "${report##*$'\n'}" = "$prefix: alle Budgets gehalten" ]; then
	exit 0
fi
exit 1
