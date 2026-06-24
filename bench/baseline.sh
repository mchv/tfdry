#!/bin/bash
# bench/baseline.sh — A/B compare current branch against a baseline ref.
#
# Builds two binaries (current HEAD + a baseline ref) and runs hyperfine -L
# to compare them on the same machine state in one invocation.
#
# Usage:
#   bench/baseline.sh                   # baseline = git merge-base HEAD origin/main
#   bench/baseline.sh v0.1.0            # baseline = a specific tag/commit
#   BASELINE=abc123 bench/baseline.sh   # via env var
#
# Output: bench/results/baseline-{small,medium,large}.{md,json}
#
# Required: git, go, hyperfine
# Optional: gum (charm.sh) for prettier output — falls back to plain echo

set -euo pipefail

# ── Style helpers (gum if available, plain echo otherwise) ────────────────────

if command -v gum >/dev/null 2>&1; then GUM=1; else GUM=0; fi

# Palette (lipgloss 256-colour codes used by gum)
C_ACCENT=39   # cyan — headers, in-progress steps
C_OK=78       # green — completed steps
C_INFO=244    # gray — supplementary info
C_WARN=226    # yellow — cached/skipped
C_ERR=196    # red — errors
C_TITLE=212   # pink — section dividers

banner() {
    if [ "$GUM" = "1" ]; then
        gum style --foreground "$C_ACCENT" --border rounded --padding "0 2" --margin "1 0 0 0" "$@"
    else
        printf '\n  %s\n\n' "$*"
    fi
}

kv() {
    # Key/value line: "  key   value" with the key dimmed.
    local key="$1" value="$2"
    if [ "$GUM" = "1" ]; then
        printf '  %s  %s\n' \
            "$(gum style --foreground "$C_INFO" --width 10 "$key")" \
            "$value"
    else
        printf '  %-10s %s\n' "$key" "$value"
    fi
}

step_start() {
    if [ "$GUM" = "1" ]; then
        gum style --foreground "$C_ACCENT" "▸ $*"
    else
        echo "==> $*"
    fi
}

step_done() {
    if [ "$GUM" = "1" ]; then
        gum style --foreground "$C_OK" "✓ $*"
    else
        echo "  ✓ $*"
    fi
}

step_cached() {
    if [ "$GUM" = "1" ]; then
        gum style --foreground "$C_WARN" "↻ $*"
    else
        echo "  ↻ $*"
    fi
}

section() {
    if [ "$GUM" = "1" ]; then
        echo
        gum style --foreground "$C_TITLE" --border rounded --align center --width 20 --padding "0 1" "$@"
    else
        printf '\n=== %s ===\n' "$*"
    fi
}

die() {
    if [ "$GUM" = "1" ]; then
        gum style --foreground "$C_ERR" --bold "error: $*" >&2
    else
        echo "error: $*" >&2
    fi
    exit 1
}

# Run a command with a gum spinner (or plain output if gum is missing).
spin() {
    local title="$1"; shift
    if [ "$GUM" = "1" ]; then
        gum spin --spinner dot --title "$title" --show-error -- "$@"
    else
        echo "==> $title..."
        "$@"
    fi
}

# ── Preflight ────────────────────────────────────────────────────────────────

command -v git       >/dev/null 2>&1 || die "git not installed"
command -v go        >/dev/null 2>&1 || die "go not installed"
command -v hyperfine >/dev/null 2>&1 || die "hyperfine not installed (brew install hyperfine)"

git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "not inside a git repository"

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# ── Resolve baseline ref ─────────────────────────────────────────────────────

resolve_baseline() {
    if [ $# -ge 1 ] && [ -n "$1" ]; then
        echo "$1"; return
    fi
    if [ -n "${BASELINE:-}" ]; then
        echo "$BASELINE"; return
    fi
    # If origin/main exists, prefer its merge-base with HEAD. Wrap the
    # merge-base call in a conditional so `set -e` doesn't abort the script
    # when HEAD shares no common ancestry with origin/main (G17 — happens on
    # forks that started independently or when origin/main hasn't been
    # fetched). On failure, fall through to the local `main` branch below
    # rather than dying outright.
    if git rev-parse --verify --quiet origin/main >/dev/null; then
        if base=$(git merge-base HEAD origin/main 2>/dev/null); then
            echo "$base"; return
        fi
    fi
    if git rev-parse --verify --quiet main >/dev/null; then
        echo "main"; return
    fi
    die "cannot determine baseline ref. Pass one explicitly: bench/baseline.sh <ref>"
}

if [ -n "${EXPERIMENT:-}" ]; then
    BASELINE_REF=""
else
    BASELINE_REF=$(resolve_baseline "${1:-}")
fi

# ── Mode: ref-vs-ref (default) or env-vs-env (EXPERIMENT set) ────────────────
# When EXPERIMENT is set, both binaries are built from current source — one
# with default env, one with GOEXPERIMENT=$EXPERIMENT. The git ref machinery
# is skipped entirely. Useful for measuring Go toolchain experiments
# (e.g. EXPERIMENT=jsonv2) without juggling refs.

if [ -n "${EXPERIMENT:-}" ]; then
    HEAD_SHORT=$(git rev-parse --short HEAD)
    banner "tfdry · GOEXPERIMENT=$EXPERIMENT benchmark"
    kv "HEAD"       "$HEAD_SHORT"
    kv "Experiment" "$EXPERIMENT"
    [ -n "${ARGS:-}" ] && kv "Args" "$ARGS"
    [ -n "${LABEL:-}" ] && kv "Label" "$LABEL"
    echo
else
    BASELINE_SHA=$(git rev-parse --verify "$BASELINE_REF" 2>/dev/null) \
        || die "cannot resolve ref '$BASELINE_REF'"
    HEAD_SHA=$(git rev-parse HEAD)

    [ "$BASELINE_SHA" = "$HEAD_SHA" ] && \
        die "baseline ($BASELINE_REF) is the same commit as HEAD — nothing to compare"

    BASELINE_SHORT=$(git rev-parse --short "$BASELINE_SHA")
    HEAD_SHORT=$(git rev-parse --short HEAD)
    # Per-invocation worktree: each run gets its own dir under /tmp.
    # This eliminates concurrency races and stale-worktree issues, at the cost of
    # always rebuilding the baseline binary (~1-2s).
    WORKTREE_DIR="${WORKTREE_DIR:-$(mktemp -d -t tfdry-baseline-XXXXXX)}"

    banner "tfdry · baseline benchmark"
    kv "Baseline" "$BASELINE_REF ($BASELINE_SHORT)"
    kv "HEAD"     "$HEAD_SHORT"
    kv "Worktree" "$WORKTREE_DIR"
    echo
fi

# ── Build both binaries ──────────────────────────────────────────────────────

BUILD_FLAGS=(-trimpath -ldflags="-s -w")
CURRENT_BIN="$REPO_ROOT/.bench-current"
BASELINE_BIN="$REPO_ROOT/.bench-baseline"

if [ -n "${EXPERIMENT:-}" ]; then
    # Both builds from current source; one with the experiment, one without.
    spin "Building default" go build "${BUILD_FLAGS[@]}" -o "$BASELINE_BIN" .
    step_done "default built ($(wc -c < "$BASELINE_BIN" | tr -d ' ') bytes)"

    spin "Building GOEXPERIMENT=$EXPERIMENT" \
        env "GOEXPERIMENT=$EXPERIMENT" go build "${BUILD_FLAGS[@]}" -o "$CURRENT_BIN" .
    step_done "$EXPERIMENT built ($(wc -c < "$CURRENT_BIN" | tr -d ' ') bytes)"
else
    spin "Building current" go build "${BUILD_FLAGS[@]}" -o "$CURRENT_BIN" .
    step_done "current built"

    BASELINE_BIN="$WORKTREE_DIR/.bench-baseline"

    # Register cleanup BEFORE any operation that could fail (go.mod check,
    # baseline build, etc.) so the worktree is always reaped on exit. The
    # function defensively handles vars that may not yet be set.
    cleanup() {
        [ -n "${TESTDATA_TMP:-}" ] && rm -rf "$TESTDATA_TMP"
        if [ -n "${WORKTREE_DIR:-}" ] && [ -d "$WORKTREE_DIR" ]; then
            git worktree remove --force "$WORKTREE_DIR" 2>/dev/null || rm -rf "$WORKTREE_DIR"
            git worktree prune
        fi
    }

    # mktemp -d already created the dir; populate it as a worktree.
    rmdir "$WORKTREE_DIR" 2>/dev/null || true
    spin "Creating worktree for $BASELINE_SHORT" \
        git worktree add --detach --force "$WORKTREE_DIR" "$BASELINE_SHA"
    trap cleanup EXIT

    # Guard against baselines that predate the Go module (or aren't this project).
    if [ ! -f "$WORKTREE_DIR/go.mod" ]; then
        die "baseline $BASELINE_REF ($BASELINE_SHORT) has no go.mod — cannot build a Go binary from it"
    fi

    # `gum spin` doesn't accept a working directory, so build via a wrapper.
    # Pass WORKTREE_DIR as a positional arg so single quotes in the path can't break out.
    spin "Building baseline" bash -c 'cd "$1" && go build -trimpath -ldflags="-s -w" -o .bench-baseline .' _ "$WORKTREE_DIR"
    step_done "baseline built"
fi

# ── Stage testdata in a temp dir ─────────────────────────────────────────────

TESTDATA_TMP=$(mktemp -d -t tfdry-bench-XXXXXX)

# In experiment mode there is no worktree; install a simpler trap.
if [ -n "${EXPERIMENT:-}" ]; then
    trap 'rm -rf "$TESTDATA_TMP"' EXIT
fi
# In ref mode, the cleanup() trap registered above already handles TESTDATA_TMP.

# DIRTY=1 generates unformatted (still-valid) HCL into <size>-src/ and uses
# hyperfine --prepare to reset the working <size>/ dir from -src/ before each
# run. This is required when ARGS=fmt (or any write-mode command) so each
# iteration sees fresh dirty input — without it, the first run would format
# the files in place and every subsequent run would be a no-op.
#
# In DIRTY mode the committed bench/testdata/small fixtures are NOT used; we
# use gen-testdata --dirty for every size so file shape and dirtiness are
# uniform. Absolute numbers won't be directly comparable to non-DIRTY runs,
# but baseline-vs-current deltas are exactly what the mode is for.
if [ "${DIRTY:-0}" = "1" ]; then
    "$REPO_ROOT/bench/gen-testdata.sh" --dirty "$TESTDATA_TMP/small-src"  5   >/dev/null
    "$REPO_ROOT/bench/gen-testdata.sh" --dirty "$TESTDATA_TMP/medium-src" 20  >/dev/null
    "$REPO_ROOT/bench/gen-testdata.sh" --dirty "$TESTDATA_TMP/large-src"  100 >/dev/null
else
    cp -R "$REPO_ROOT/bench/testdata/small" "$TESTDATA_TMP/small"
    "$REPO_ROOT/bench/gen-testdata.sh" "$TESTDATA_TMP/medium" 20 >/dev/null
    "$REPO_ROOT/bench/gen-testdata.sh" "$TESTDATA_TMP/large"  100 >/dev/null
fi

mkdir -p "$TESTDATA_TMP/bin"
if [ -n "${EXPERIMENT:-}" ]; then
    VARIANT_A="default"
    VARIANT_B="$EXPERIMENT"
    REPORT_PREFIX="experiment-${LABEL:-$EXPERIMENT}"
else
    VARIANT_A="baseline"
    VARIANT_B="current"
    REPORT_PREFIX="baseline"
fi
[ "${DIRTY:-0}" = "1" ] && REPORT_PREFIX="${REPORT_PREFIX}-dirty"
cp "$BASELINE_BIN" "$TESTDATA_TMP/bin/$VARIANT_A"
cp "$CURRENT_BIN"  "$TESTDATA_TMP/bin/$VARIANT_B"

step_done "testdata staged (small / medium / large${DIRTY:+ — dirty})"

# ── Run hyperfine: one A/B comparison per size ───────────────────────────────

mkdir -p "$REPO_ROOT/bench/results"

for size in small medium large; do
    section "$size"
    if [ "${DIRTY:-0}" = "1" ]; then
        # Refresh the working dir from -src/ before each measured run so the
        # dirty-data invariant holds. cp -R cost is outside the timed window.
        # Wrap in `bash -c` because hyperfine -N also disables shell parsing
        # for --prepare, which would otherwise treat `&&` as a literal arg.
        hyperfine -N \
            --warmup 3 \
            --runs 30 \
            --prepare "bash -c \"rm -rf '$TESTDATA_TMP/$size' && cp -R '$TESTDATA_TMP/$size-src' '$TESTDATA_TMP/$size'\"" \
            -L variant "$VARIANT_A,$VARIANT_B" \
            --command-name '{variant}' \
            --export-markdown "$REPO_ROOT/bench/results/$REPORT_PREFIX-$size.md" \
            --export-json     "$REPO_ROOT/bench/results/$REPORT_PREFIX-$size.json" \
            "$TESTDATA_TMP/bin/{variant} ${ARGS:-} $TESTDATA_TMP/$size"
    else
        hyperfine -N \
            --warmup 3 \
            --runs 30 \
            -L variant "$VARIANT_A,$VARIANT_B" \
            --command-name '{variant}' \
            --export-markdown "$REPO_ROOT/bench/results/$REPORT_PREFIX-$size.md" \
            --export-json     "$REPO_ROOT/bench/results/$REPORT_PREFIX-$size.json" \
            "$TESTDATA_TMP/bin/{variant} ${ARGS:-} $TESTDATA_TMP/$size"
    fi
done

# ── Final summary ────────────────────────────────────────────────────────────

echo
banner "done"
kv "Reports"  "bench/results/$REPORT_PREFIX-*.{md,json}"
echo
