#!/usr/bin/env bash
#
# check-scan-exceptions.sh — CVE fix SLA of security/scan-exceptions.txt
# (SEC-11, WP-389). Runs in CI before govulncheck, npm audit and Trivy
# (ci.yml, release.yml), which all read the file.
#
# Every line that is neither empty nor a comment must read
#   <ID> exp:<YYYY-MM-DD> since:<YYYY-MM-DD> sev:<critical|high> [# <reason>]
# with since = first report of the finding (not in the future) and
# since <= exp <= since + 7 days (critical) or since + 30 days (high).
# Trivy reads the file as .trivyignore, where a line without exp: suppresses
# its finding forever. exp:, since: and sev: occur exactly once and before
# the comment: Trivy takes the first exp: of the line, the evaluation in
# ci.yml the last one. An expired entry is valid; it suppresses nothing.
#
# Usage: scripts/ci/check-scan-exceptions.sh [file]
#   (default security/scan-exceptions.txt; SCAN_EXCEPTIONS_TODAY=<YYYY-MM-DD>
#   replaces today's UTC date, for tests)
set -euo pipefail

file="${1:-security/scan-exceptions.txt}"
today="${SCAN_EXCEPTIONS_TODAY:-$(date -u +%F)}"

# valid_date <YYYY-MM-DD>: a calendar date in exactly this form.
valid_date() {
    [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] && [ "$(date -u -d "$1" +%F 2>/dev/null)" = "$1" ]
}

# plus_days <YYYY-MM-DD> <n>
plus_days() {
    date -u -d "@$(( $(date -u -d "$1" +%s) + $2 * 86400 ))" +%F
}

if [ ! -r "$file" ]; then
    printf '::error::%s is not readable\n' "$file"
    exit 1
fi
if ! valid_date "$today"; then
    printf '::error::invalid date for today: %s\n' "$today"
    exit 1
fi

lineno=0
entries=0
errors=0
bad() {
    errors=$((errors + 1))
    printf '::error file=%s,line=%d::%s\n' "$file" "$lineno" "$1"
}

while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    read -r -a fields <<<"$line" || true
    [ "${#fields[@]}" -eq 0 ] && continue
    [ "${fields[0]:0:1}" = "#" ] && continue
    entries=$((entries + 1))

    id="${fields[0]}"
    exp="" since="" sev=""
    n_exp=0 n_since=0 n_sev=0
    comment=0 problems=()
    for field in "${fields[@]:1}"; do
        [ "${field:0:1}" = "#" ] && comment=1
        case "$field" in
            exp:*) n_exp=$((n_exp + 1)); exp="${field#exp:}" ;;
            since:*) n_since=$((n_since + 1)); since="${field#since:}" ;;
            sev:*) n_sev=$((n_sev + 1)); sev="${field#sev:}" ;;
            *) [ "$comment" -eq 1 ] || problems+=("unknown field '$field' before the comment") ;;
        esac
        case "$field" in
            exp:*|since:*|sev:*) [ "$comment" -eq 0 ] || problems+=("'$field' inside the comment") ;;
        esac
    done

    [[ "$id" =~ ^(CVE-[0-9]{4}-[0-9]{4,}|GHSA(-[0-9a-z]{4}){3}|GO-[0-9]{4}-[0-9]{4,})$ ]] \
        || problems+=("ID '$id' is not a CVE-, GHSA- or GO- ID")
    if [ "$n_exp" -eq 0 ]; then
        problems+=("exp:<YYYY-MM-DD> is missing; Trivy would ignore the finding forever")
    elif [ "$n_exp" -gt 1 ]; then
        problems+=("exp: occurs $n_exp times; Trivy and ci.yml would read different dates")
    fi
    [ "$n_since" -eq 1 ] || problems+=("needs exactly one since:<YYYY-MM-DD>, the first report of the finding (found $n_since)")
    [ "$n_sev" -eq 1 ] || problems+=("needs exactly one sev:critical or sev:high (found $n_sev)")

    if [ "${#problems[@]}" -eq 0 ]; then
        case "$sev" in
            critical) days=7 ;;
            high) days=30 ;;
            *) problems+=("sev:$sev is neither critical nor high") ;;
        esac
        valid_date "$exp" || problems+=("exp:$exp is not a valid date")
        valid_date "$since" || problems+=("since:$since is not a valid date")
    fi
    if [ "${#problems[@]}" -eq 0 ]; then
        cap="$(plus_days "$since" "$days")"
        if [[ "$since" > "$today" ]]; then
            problems+=("since:$since lies in the future (today $today)")
        elif [[ "$exp" < "$since" ]]; then
            problems+=("exp:$exp lies before since:$since")
        elif [[ "$exp" > "$cap" ]]; then
            problems+=("exp:$exp exceeds the SLA for $sev ($days days from since:$since, at most $cap)")
        fi
    fi

    for p in "${problems[@]}"; do
        bad "$id: $p"
    done
done <"$file"

printf '%s: %d entries, %d problems\n' "$file" "$entries" "$errors"
[ "$errors" -eq 0 ]
