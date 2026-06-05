#!/bin/bash
# End-to-end benchmark: tfdry vs terraform fmt/validate.
# Runs inside the bench container. Outputs markdown + JSON reports to /out.
set -euo pipefail

OUT=${OUT:-/out}
mkdir -p "$OUT"

echo "=== environment ==="
echo "tfdry:     $(tfdry version)"
echo "terraform: $(terraform version | head -n1)"
echo "hyperfine: $(hyperfine --version)"
echo

# -N (no shell) eliminates shell startup overhead for sub-5ms commands.

# ── 1. Format check: tfdry fmt -check vs terraform fmt -check ─────────────────
# Closest apples-to-apples comparison: both check formatting only, no rewrite.
# Both exit 3 if any file needs formatting.
echo "=== format check (small) ==="
hyperfine -N \
    --warmup 3 \
    --runs 30 \
    --export-markdown "$OUT/fmt-small.md" \
    --export-json "$OUT/fmt-small.json" \
    --command-name 'tfdry fmt -check' \
        'tfdry fmt -check /testdata/small' \
    --command-name 'terraform fmt -check' \
        'terraform fmt -check -recursive /testdata/small'

echo
echo "=== format check (large) ==="
hyperfine -N \
    --warmup 3 \
    --runs 30 \
    --export-markdown "$OUT/fmt-large.md" \
    --export-json "$OUT/fmt-large.json" \
    --command-name 'tfdry fmt -check' \
        'tfdry fmt -check /testdata/large' \
    --command-name 'terraform fmt -check' \
        'terraform fmt -check -recursive /testdata/large'

# ── 2. Full check: tfdry vs terraform validate ────────────────────────────────
# terraform validate requires `terraform init` (already done at build time).
# tfdry runs all checks (E001-E008, W001).
# Note: terraform validate must run from inside the dir, so we use bash -c.
echo
echo "=== full validation (small) ==="
hyperfine -N \
    --warmup 3 \
    --runs 30 \
    --export-markdown "$OUT/validate-small.md" \
    --export-json "$OUT/validate-small.json" \
    --command-name 'tfdry' \
        'tfdry /testdata/small' \
    --command-name 'terraform validate' \
        'bash -c "cd /testdata/small && terraform validate"'

echo
echo "=== full validation (large) ==="
hyperfine -N \
    --warmup 3 \
    --runs 20 \
    --export-markdown "$OUT/validate-large.md" \
    --export-json "$OUT/validate-large.json" \
    --command-name 'tfdry' \
        'tfdry /testdata/large' \
    --command-name 'terraform validate' \
        'bash -c "cd /testdata/large && terraform validate"'

# ── 3. Scaling: format check across sizes, parameterised ─────────────────────
echo
echo "=== scaling: format check (small/medium/large) ==="
hyperfine -N \
    --warmup 3 \
    --runs 20 \
    --parameter-list size small,medium,large \
    --export-markdown "$OUT/scaling-fmt.md" \
    --export-json "$OUT/scaling-fmt.json" \
    'tfdry fmt -check /testdata/{size}' \
    'terraform fmt -check -recursive /testdata/{size}'

# ── 4. JSON output overhead ───────────────────────────────────────────────────
echo
echo "=== json output overhead ==="
hyperfine -N \
    --warmup 3 \
    --runs 30 \
    --export-markdown "$OUT/json-overhead.md" \
    --export-json "$OUT/json-overhead.json" \
    --command-name 'tfdry (human)' \
        'tfdry /testdata/large' \
    --command-name 'tfdry --json' \
        'tfdry --json /testdata/large'

echo
echo "=== reports written to $OUT ==="
ls -lh "$OUT"
