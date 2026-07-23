#!/bin/sh
# Generates a synthetic terraform directory of N files using only the null provider.
# By default output is properly formatted (clean tfdry baseline). With --dirty
# the same content is emitted with leading whitespace stripped, so the file
# remains valid HCL but `hclwrite.Format` (and `terraform fmt`) have real work
# to do. Used to benchmark write-mode formatting performance.
#
# Usage: gen-testdata.sh [--dirty] <output-dir> <num-files>
#   --dirty       emit unformatted (still-valid) HCL — leading indentation removed
#   <output-dir>  destination directory (created if missing)
#   <num-files>   non-negative integer; number of file_<i>.tf files to emit.
#                 n=0 still produces providers.tf + locals.tf (a minimal valid
#                 configuration with no resource files), useful for testing
#                 the empty-resource case.
set -eu

usage() {
	echo "usage: gen-testdata.sh [--dirty] <output-dir> <num-files>" >&2
	exit 2
}

dirty=0
if [ "${1:-}" = "--dirty" ]; then
	dirty=1
	shift
fi

[ "$#" -eq 2 ] || usage
[ -n "$1" ] || usage

dir="$1"
case "$dir" in
-*) dir="./$dir" ;;
esac
n="$2"

# n must be a non-negative integer.
case "$n" in
'' | *[!0-9]*)
	echo "gen-testdata.sh: <num-files> must be a non-negative integer (got: $n)" >&2
	exit 2
	;;
esac

mkdir -p "$dir"

# Clean up any stale file_*.tf from a previous run. Without this, rerunning
# with a smaller n leaves the higher-indexed files behind (e.g. n=100 then
# n=5 would still yield 100 files), polluting subsequent benchmarks.
# `rm -f` with an unmatched glob is a silent no-op in /bin/sh.
rm -f "$dir"/file_*.tf

# emit reads stdin and writes to $1, optionally stripping leading whitespace
# when --dirty was passed. The output is still valid HCL — just non-canonical.
emit() {
	if [ "$dirty" = "1" ]; then
		sed -e 's/^[[:space:]]*//' >"$1"
	else
		cat >"$1"
	fi
}

emit "$dir/providers.tf" <<'EOF'
terraform {
  required_version = ">= 1.0"
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "3.3.0"
    }
  }
}
EOF

emit "$dir/locals.tf" <<'EOF'
locals {
  env  = "prod"
  team = "platform"
}

output "env" {
  value = local.env
}

output "team" {
  value = local.team
}
EOF

i=0
while [ "$i" -lt "$n" ]; do
	emit "$dir/file_$i.tf" <<EOF
locals {
  name_$i = "resource-$i-\${local.env}"
  port_$i = $((1000 + i))
}

resource "null_resource" "r_$i" {
  triggers = {
    name = local.name_$i
    env  = local.env
    port = tostring(local.port_$i)
  }
}

output "id_$i" {
  value = null_resource.r_$i.id
}
EOF
	i=$((i + 1))
done
