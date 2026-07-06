#!/bin/sh
# bench/attr-corpus/extract.sh — walk the fetched corpus with hclsyntax and
# refresh the per-family attribute-value files under `values/`.
#
# Wraps the Go extractor at `cmd/extract/main.go` which is `//go:build ignore`
# (excluded from normal `go build ./...`). Run via `go run` so no separate
# build/install step is needed.
#
# Requires: go
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FILES_DIR="$SCRIPT_DIR/files"
VALUES_DIR="$SCRIPT_DIR/values"
EXTRACTOR="$SCRIPT_DIR/cmd/extract/main.go"

command -v go >/dev/null 2>&1 || { echo "extract.sh: go not installed" >&2; exit 2; }

if [ ! -d "$FILES_DIR" ] || [ -z "$(ls -A "$FILES_DIR" 2>/dev/null)" ]; then
    echo "extract.sh: $FILES_DIR is empty. Run \`make bench-corpus-fetch\` first." >&2
    exit 2
fi

mkdir -p "$VALUES_DIR"

# Run from the repo root so `go run` resolves the tfdry go.mod (needed for the
# hclsyntax import).
cd "$(git rev-parse --show-toplevel)"
go run "$EXTRACTOR" "$FILES_DIR" "$VALUES_DIR"
