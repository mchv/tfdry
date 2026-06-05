#!/bin/sh
# Generates a synthetic terraform directory of N files using only the null provider.
# Output is properly formatted and has no unused locals (clean tfdry baseline).
#
# Usage: gen-testdata.sh <output-dir> <num-files>
#   <output-dir>  destination directory (created if missing)
#   <num-files>   non-negative integer; number of file_<i>.tf files to emit.
#                 n=0 still produces providers.tf + locals.tf (a minimal valid
#                 configuration with no resource files), useful for testing
#                 the empty-resource case.
set -eu

usage() {
    echo "usage: gen-testdata.sh <output-dir> <num-files>" >&2
    exit 2
}

[ "$#" -eq 2 ] || usage
[ -n "$1" ]    || usage

dir="$1"
n="$2"

# n must be a non-negative integer.
case "$n" in
    ''|*[!0-9]*) echo "gen-testdata.sh: <num-files> must be a non-negative integer (got: $n)" >&2; exit 2 ;;
esac

mkdir -p "$dir"

# Clean up any stale file_*.tf from a previous run. Without this, rerunning
# with a smaller n leaves the higher-indexed files behind (e.g. n=100 then
# n=5 would still yield 100 files), polluting subsequent benchmarks.
# `rm -f` with an unmatched glob is a silent no-op in /bin/sh.
rm -f "$dir"/file_*.tf

cat > "$dir/providers.tf" <<'EOF'
terraform {
  required_version = ">= 1.0"
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}
EOF

cat > "$dir/locals.tf" <<'EOF'
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
  cat > "$dir/file_$i.tf" <<EOF
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
