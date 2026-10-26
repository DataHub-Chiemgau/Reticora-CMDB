#!/usr/bin/env bash
# Assembles the archive of one load.yml run (NFR-10, E-58, WP-439) from the
# artifacts of its load jobs, in the form docs/acceptance/artefakte/lasttest/
# <run-id>/ keeps it:
#
#   tests/load/archive-run.sh <artifacts> <archive> <jobs>
#
# <artifacts> holds one directory load-results-<job> per job artifact (as
# actions/download-artifact stores them), <jobs> is the comma-separated list
# of the jobs the run selected. For every job the script copies into
# <archive>/<job>/ the k6 summary (<job>.json, and <job>.txt, the k6 output
# without its progress lines), the run record (run-<job>.txt: commit,
# server_ref, lscpu, memory and configuration), the outcome of the job
# (result-<job>.txt), the seed duration and the seeded volumes, the
# integrity check, the database cost report, the summaries of the slow
# plans and of the CPU profile, and /readyz. Server and PostgreSQL logs and
# the binary profile stay in the artifacts of the run.
#
# <archive>/ergebnis.md lists the run (from the environment of the
# workflow: GITHUB_RUN_ID, GITHUB_RUN_ATTEMPT, GITHUB_SHA, GITHUB_REF_NAME,
# GITHUB_EVENT_NAME, RUN_URL, SERVER_SHA, INPUT_OBJECTS, INPUT_DURATION,
# INPUT_SCENARIOS, INPUT_SERVER_REF), whether every selected job left an
# artifact and is green, and per job its outcome, host, configuration and
# every k6 threshold with the measured value; <archive>/SHA256SUMS holds
# the checksums of the archive. A job without an artifact is listed as
# missing. The script fails only on wrong arguments: a red or missing job
# is a result the archive records.
set -euo pipefail

if [ "$#" -ne 3 ]; then
	echo "usage: $0 <artifacts> <archive> <jobs>" >&2
	exit 2
fi
artifacts=$1 archive=$2 joblist=$3
if [ ! -d "$artifacts" ]; then
	echo "archive-run: $artifacts is not a directory" >&2
	exit 2
fi
if [ -e "$archive" ] && [ -n "$(ls -A "$archive")" ]; then
	echo "archive-run: $archive is not empty" >&2
	exit 2
fi
IFS=',' read -ra jobs <<<"$joblist"
if [ "${#jobs[@]}" -eq 0 ]; then
	echo "archive-run: no job" >&2
	exit 2
fi
for job in "${jobs[@]}"; do
	if [[ ! $job =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
		echo "archive-run: invalid job name '$job'" >&2
		exit 2
	fi
done
mkdir -p "$archive"

# The files of a job the archive keeps (%s is the job).
keep=(%s.json %s.txt run-%s.txt result-%s.txt seed-%s.txt volumes-%s.txt integrity-%s.txt statements-%s.txt
	slow-plans-%s.txt cpu-%s.txt readyz-%s.json)

# field FILE KEY: the value after "KEY:" in a record file, else "–".
field() {
	local v
	v="$(sed -n "s/^$2:[[:space:]]*//p" "$1" 2>/dev/null | head -n 1)"
	echo "${v:-–}"
}

# thresholds JSON: one table row per k6 threshold: metric, threshold,
# measured value (rounded to 0.1; time metrics in ms) and the result. In
# the summary export a threshold is true when it was crossed; the rate of a
# rate metric is its "value".
thresholds() {
	jq -r '
		def unit($m): if ($m | test("duration|delay|flush")) then " ms"
			elif ($m | test("records_per_s$")) then " Records/s" else "" end;
		def num: if type == "number" then ((. * 10 | round) / 10 | tostring) else "–" end;
		.metrics | to_entries | sort_by(.key)[] | select(.value.thresholds) | .key as $m | .value as $v
		| $v.thresholds | to_entries[]
		| ((.key | capture("^(?<stat>[a-z]+(\\([0-9.]+\\))?)") | .stat) // "") as $stat
		| $v[if $stat == "rate" then "value" else $stat end] as $val
		| "| `\($m)` | `\(.key)` | \($val | num)\(if $val == null then "" else unit($m) end) | \(if .value then "**verfehlt**" else "eingehalten" end) |"
	' "$1"
}

# measures JSON: the measures of the scenarios without a threshold
# (ingest_*, bulk_*) and the totals of the run.
measures() {
	jq -r '
		def num: if type == "number" then ((. * 10 | round) / 10 | tostring) else "–" end;
		def stats: if has("value") then "value \(.value | num)"
			elif has("p(95)") then "p(95) \(.["p(95)"] | num), max \(.max | num), count \(.count | num)"
			else "count \(.count | num)" end;
		.metrics as $all
		| ($all | to_entries | sort_by(.key)[] | select((.key | test("^(ingest|bulk)_")) and (.value.thresholds | not))
			| "| `\(.key)` | \(.value | stats) |"),
		"| `checks` | \($all.checks.passes // 0) bestanden, \($all.checks.fails // 0) fehlgeschlagen |",
		"| `http_reqs` | \($all.http_reqs.count // 0) |",
		"| `http_req_failed` | Anteil \($all.http_req_failed.value // 0 | num) |"
	' "$1"
}

missing=() red=()
for job in "${jobs[@]}"; do
	src="$artifacts/load-results-$job"
	if [ ! -d "$src" ]; then
		missing+=("$job")
		continue
	fi
	mkdir -p "$archive/$job"
	for pattern in "${keep[@]}"; do
		# shellcheck disable=SC2059 # the pattern is the format
		f="$(printf "$pattern" "$job")"
		if [ ! -f "$src/$f" ]; then
			continue
		fi
		if [ "$f" = "$job.txt" ]; then
			# The k6 output without its progress lines (one block per
			# second of the run); the full output stays in the artifact.
			grep -vE '^running \(|^[[:alnum:]_]+ +(✓ |✗ )?\[ *[0-9]+% \]' "$src/$f" | cat -s >"$archive/$job/$f" || true
		else
			cp "$src/$f" "$archive/$job/$f"
		fi
	done
	if [ "$(field "$archive/$job/result-$job.txt" status)" != success ]; then
		red+=("$job")
	fi
done

# join ITEM...: the items separated by ", ".
join() {
	local out
	out="$(printf '%s, ' "$@")"
	echo "${out%, }"
}

{
	run="${GITHUB_RUN_ID:-–}"
	echo "# Lastlauf $run (load.yml)"
	echo
	echo "Erzeugt von \`tests/load/archive-run.sh\` im Job \`archive\` des Laufs."
	echo
	echo "| Punkt | Wert |"
	echo "|---|---|"
	echo "| Lauf | ${RUN_URL:-–} (Versuch ${GITHUB_RUN_ATTEMPT:-–}, Ereignis ${GITHUB_EVENT_NAME:-–}, Branch ${GITHUB_REF_NAME:-–}) |"
	echo "| Lauf-Commit (tests/load) | \`${GITHUB_SHA:-–}\` |"
	echo "| Server, Seed und Migrationen | \`${SERVER_SHA:-–}\` |"
	echo "| Eingaben | objects ${INPUT_OBJECTS:-–}, duration ${INPUT_DURATION:-–}, scenarios ${INPUT_SCENARIOS:-–}, server_ref ${INPUT_SERVER_REF:-–} |"
	echo "| Jobs | $(join "${jobs[@]}") |"
	if [ "${#missing[@]}" -eq 0 ]; then
		echo "| Vollständig | ja: jeder Job hat ein Artefakt |"
	else
		echo "| Vollständig | **nein**, ohne Artefakt: $(join "${missing[@]}") |"
	fi
	if [ "${#missing[@]}" -eq 0 ] && [ "${#red[@]}" -eq 0 ]; then
		echo "| Ergebnis | alle Jobs grün |"
	else
		echo "| Ergebnis | **nicht grün**: $(join "${red[@]}" "${missing[@]}") |"
	fi
	for job in "${jobs[@]}"; do
		echo
		echo "## Job \`$job\`"
		echo
		dir="$archive/$job"
		if [ ! -d "$dir" ]; then
			echo "Kein Artefakt \`load-results-$job\`."
			continue
		fi
		result="$dir/result-$job.txt" record="$dir/run-$job.txt"
		echo "| Punkt | Wert |"
		echo "|---|---|"
		echo "| Status | $(field "$result" status) (Seed $(field "$result" seed), Volumina $(field "$result" volumes), k6 $(field "$result" k6), Integritätsprüfung $(field "$result" integrity)) |"
		echo "| CPU | $(field "$record" "Model name"), $(field "$record" "CPU(s)") CPUs |"
		echo "| RAM | $(field "$record" MemTotal) |"
		echo "| Konfiguration | Skript $(field "$record" script) (Szenario $(field "$record" "k6 scenario")), objects $(field "$record" objects), duration $(field "$record" duration), Seed-Optionen $(field "$record" "seed options"), Spuren $(field "$record" "ingest lanes"), Rate-Limit $(field "$record" "rate limit rpm")/min und $(field "$record" "rate limit org rpm")/min je Org, k6 $(field "$record" k6) |"
		if [ -f "$dir/seed-$job.txt" ]; then
			echo "| Seed-Dauer | $(field "$dir/seed-$job.txt" "seed seconds") s |"
		fi
		if [ -f "$dir/volumes-$job.txt" ]; then
			echo "| Datenbestand | $(tr '\n' ';' <"$dir/volumes-$job.txt" | sed 's/|/ /g; s/;$//; s/;/, /g') |"
		fi
		if [ -f "$dir/integrity-$job.txt" ]; then
			echo "| Integritätsprüfung | $(awk -F'|' '{s += $2; n++} END {print s + 0 " Verletzungen in " n + 0 " Prüfungen"}' "$dir/integrity-$job.txt") |"
		fi
		echo
		if [ ! -f "$dir/$job.json" ]; then
			echo "Keine k6-Zusammenfassung \`$job.json\`."
			continue
		fi
		echo "| Metrik | Schwelle | gemessen | Ergebnis |"
		echo "|---|---|---:|---|"
		thresholds "$dir/$job.json"
		echo
		echo "| Messgröße | Wert |"
		echo "|---|---|"
		measures "$dir/$job.json"
	done
} >"$archive/ergebnis.md"

(
	cd "$archive"
	sums="$(find . -type f ! -name SHA256SUMS | sed 's#^\./##' | LC_ALL=C sort | xargs sha256sum)"
	printf '%s\n' "$sums" >SHA256SUMS
)
echo "archive-run: $archive ($(join "${jobs[@]}"); missing: ${#missing[@]}, not green: ${#red[@]})"
