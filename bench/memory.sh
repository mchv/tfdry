#!/usr/bin/env bash
# Measure tfdry peak resident set size inside the pinned benchmark container.
# GNU time reports one maximum-RSS value per process invocation in KiB. We run
# an odd number of samples and publish the median, minimum, and maximum so the
# result is auditable without confusing heap allocations with process RSS.

set -euo pipefail

export LC_ALL=C

OUT="${OUT:-/out}"
RUNS="${MEMORY_RUNS:-11}"
WARMUPS="${MEMORY_WARMUPS:-3}"

fail() {
	printf 'memory.sh: %s\n' "$*" >&2
	exit 1
}

for tool in tfdry jq /usr/bin/time; do
	command -v "$tool" >/dev/null 2>&1 || fail "required tool not found: $tool"
done

case "$RUNS" in
'' | *[!0-9]*) fail "MEMORY_RUNS must be a positive odd integer (got: $RUNS)" ;;
esac
if ((RUNS < 1 || RUNS % 2 == 0)); then
	fail "MEMORY_RUNS must be a positive odd integer (got: $RUNS)"
fi

case "$WARMUPS" in
'' | *[!0-9]*) fail "MEMORY_WARMUPS must be a non-negative integer (got: $WARMUPS)" ;;
esac

mkdir -p -- "$OUT"
tmp_dir=$(mktemp -d -t tfdry-memory-XXXXXX)
cleanup() {
	rm -rf -- "$tmp_dir"
}
trap cleanup EXIT

json_lines="$tmp_dir/results.jsonl"
: >"$json_lines"

sizes=(small medium large)
for size in ${sizes[@]+"${sizes[@]}"}; do
	fixture_dir="/testdata/$size"
	[ -d "$fixture_dir" ] || fail "fixture directory not found: $fixture_dir"

	for ((i = 0; i < WARMUPS; i++)); do
		tfdry "$fixture_dir" >/dev/null
	done

	samples="$tmp_dir/$size.samples"
	: >"$samples"
	for ((i = 0; i < RUNS; i++)); do
		time_report="$tmp_dir/$size-time-$i.txt"
		/usr/bin/time -v -o "$time_report" tfdry "$fixture_dir" >/dev/null
		rss_kib=$(awk -F ': *' '/Maximum resident set size/ {print $2}' "$time_report")
		case "$rss_kib" in
		'' | *[!0-9]*) fail "could not parse peak RSS from $time_report" ;;
		esac
		printf '%s\n' "$rss_kib" >>"$samples"
	done

	sort -n "$samples" -o "$samples"
	median_line=$((RUNS / 2 + 1))
	min_rss_kib=$(head -n 1 "$samples")
	median_rss_kib=$(sed -n "${median_line}p" "$samples")
	max_rss_kib=$(tail -n 1 "$samples")
	terraform_files=$(find "$fixture_dir" -maxdepth 1 -type f -name '*.tf' | wc -l | tr -d ' ')

	jq -n \
		--arg fixture "$size" \
		--argjson terraform_files "$terraform_files" \
		--argjson median_peak_rss_kib "$median_rss_kib" \
		--argjson min_peak_rss_kib "$min_rss_kib" \
		--argjson max_peak_rss_kib "$max_rss_kib" \
		'{
            fixture: $fixture,
            terraform_files: $terraform_files,
            median_peak_rss_kib: $median_peak_rss_kib,
            min_peak_rss_kib: $min_peak_rss_kib,
            max_peak_rss_kib: $max_peak_rss_kib
        }' >>"$json_lines"
done

generated_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
tfdry_version=$(tfdry version)
architecture=$(uname -m)

jq -s \
	--arg generated_at "$generated_at" \
	--arg tfdry_version "$tfdry_version" \
	--arg architecture "$architecture" \
	--argjson runs "$RUNS" \
	--argjson warmups "$WARMUPS" \
	'{
        schema_version: 1,
        generated_at: $generated_at,
        method: "GNU time 1.9 /usr/bin/time -v",
        metric: "maximum resident set size",
        unit: "KiB",
        summary: "median of per-run maxima",
        runs: $runs,
        warmups: $warmups,
        tfdry_version: $tfdry_version,
        platform: {os: "linux", architecture: $architecture},
        results: .
    }' "$json_lines" >"$OUT/memory.json"

# Markdown report templates use literal backticks inside single-quoted printf
# formats; they are not command substitutions.
# shellcheck disable=SC2016
{
	printf '# tfdry peak RSS\n\n'
	printf 'Generated: `%s`  \n' "$generated_at"
	printf 'Binary: `%s`  \n' "$tfdry_version"
	printf 'Platform: `linux/%s`  \n' "$architecture"
	printf 'Method: GNU time 1.9 `/usr/bin/time -v`; %s warm-ups and %s measured runs per fixture.  \n\n' "$WARMUPS" "$RUNS"
	printf '| Fixture | Terraform files | Median peak RSS [KiB] | Min [KiB] | Max [KiB] |\n'
	printf '|:---|---:|---:|---:|---:|\n'
	jq -r '.results[] | "| \(.fixture) | \(.terraform_files) | \(.median_peak_rss_kib) | \(.min_peak_rss_kib) | \(.max_peak_rss_kib) |"' -- "$OUT/memory.json"
	printf '\nEach sample is a fresh `tfdry` process. Peak RSS includes the Go runtime, stacks, parsed source, and process image; it is not equivalent to Go benchmark `B/op`.\n'
} >"$OUT/memory.md"

printf 'memory reports written to %s/memory.{md,json}\n' "$OUT"
