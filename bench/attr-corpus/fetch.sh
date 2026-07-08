#!/bin/sh
# bench/attr-corpus/fetch.sh — fetch pinned Terraform corpora from GitHub.
#
# Reads repos.txt line by line and downloads each `<owner>/<repo> <ref>` as a
# tar.gz snapshot from GitHub into `bench/attr-corpus/files/<owner>-<repo>/`.
# The `files/` directory is gitignored — this script is idempotent.
#
# Ref may be either:
#   - a release tag  (e.g. `v5.13.0`), fetched via /archive/refs/tags/<tag>
#   - a full commit SHA (40 lowercase hex chars), fetched via /archive/<sha>
# Anything not matching the strict full-SHA shape is treated as a tag, so a
# non-existent tag fails-loud rather than silently falling back to a branch
# of the same name. Tags remain the preferred pinning form; SHAs unblock
# repos that don't cut releases (see the aws-cloudformation/iac-model-
# evaluation entry in repos.txt).
#
# Existing corpus directories are left alone. Use `make bench-corpus-clean`
# (or delete `files/` by hand) to force a re-fetch.
#
# Requires: curl, tar
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPOS_FILE="$SCRIPT_DIR/repos.txt"
FILES_DIR="$SCRIPT_DIR/files"

command -v curl >/dev/null 2>&1 || { echo "fetch.sh: curl not installed" >&2; exit 2; }
command -v tar  >/dev/null 2>&1 || { echo "fetch.sh: tar not installed"  >&2; exit 2; }

[ -f "$REPOS_FILE" ] || { echo "fetch.sh: $REPOS_FILE not found" >&2; exit 2; }

mkdir -p "$FILES_DIR"

# Trap-based cleanup for the per-repo temporary tarball. We reassign $tmp
# inside the loop and reset it to empty after successful extraction, so on
# EXIT / INT / TERM the trap removes at most one stale file — whichever
# repo was in flight when the script was interrupted.
tmp=""
cleanup() {
    if [ -n "$tmp" ] && [ -f "$tmp" ]; then
        rm -f "$tmp"
    fi
}
trap cleanup EXIT INT TERM

fetched=0
cached=0
failed=0

# Read repos.txt with an explicit fd so counter updates survive the loop
# (a while-read pipeline would put the body in a subshell).
while IFS= read -r line <&3 || [ -n "$line" ]; do
    # Strip trailing CR (Windows CRLF checkouts), comments, and surrounding
    # whitespace. `printf '%s\n'` over `echo` for the initial pipe so a value
    # that starts with a dash isn't misinterpreted as an echo flag on shells
    # where echo honours -n / -e / -E.
    entry=$(printf '%s\n' "$line" | tr -d '\r' | sed -e 's/#.*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [ -z "$entry" ] && continue

    repo=$(printf '%s\n' "$entry" | awk '{print $1}')
    ref=$(printf  '%s\n' "$entry" | awk '{print $2}')

    if [ -z "$repo" ] || [ -z "$ref" ]; then
        echo "fetch.sh: malformed entry: $line" >&2
        failed=$((failed + 1))
        continue
    fi

    dest_name=$(printf '%s\n' "$repo" | tr '/' '-')
    dest="$FILES_DIR/$dest_name"

    if [ -d "$dest" ] && [ "$(ls -A "$dest" 2>/dev/null)" ]; then
        printf '  ↻ %-55s %s (cached)\n' "$repo" "$ref"
        cached=$((cached + 1))
        continue
    fi

    # A ref that is exactly 40 lowercase hex chars is treated as a commit
    # SHA and fetched via /archive/${sha}.tar.gz (which GitHub resolves to
    # a tree snapshot at that commit). Anything else is treated as a tag
    # and fetched via /archive/refs/tags/${tag}.tar.gz, which surfaces a
    # 404 if the tag doesn't exist. Full-SHA-only avoids collisions with
    # tag names that happen to be short hex strings.
    #
    # `printf '%s\n'` (not `echo`) matches the pattern used elsewhere in
    # this loop for the same reason: a value that starts with a dash
    # would otherwise be misinterpreted as an echo flag on shells that
    # honour -n / -e / -E. Not a realistic risk for repos.txt-derived
    # refs (which are always tags or SHAs), but the local idiom exists
    # for a reason and this new code path should follow it.
    if printf '%s\n' "$ref" | grep -Eq '^[0-9a-f]{40}$'; then
        url="https://github.com/${repo}/archive/${ref}.tar.gz"
    else
        url="https://github.com/${repo}/archive/refs/tags/${ref}.tar.gz"
    fi

    # Download to a temp file first so curl's exit code is checked before tar
    # runs. `curl | tar` would let tar succeed on empty stdin and hide the 404.
    #
    # Portability notes on `mktemp`:
    # - The positional-argument form `mktemp path/prefix.XXXXXXXX` is accepted
    #   by both BSD (macOS) and GNU (Linux) `mktemp`. This is the portable
    #   contract for supplying a template.
    # - `-t prefix` is NOT portable across the two: on BSD `-t` takes a bare
    #   prefix and appends the placeholders itself; on GNU `-t` expects a
    #   template that already contains `XXXXXX`. Using the positional form
    #   sidesteps the divergence entirely.
    # - `${TMPDIR:-/tmp}` respects macOS's per-user `$TMPDIR`
    #   (`/var/folders/.../T/`) while still falling back on `/tmp` on Linux
    #   where `$TMPDIR` is often unset.
    tmp=$(mktemp "${TMPDIR:-/tmp}/tfdry-attr-corpus.XXXXXXXX")
    if ! curl -fsSL "$url" -o "$tmp"; then
        printf '  ✗ %-55s %s (HTTP error)\n' "$repo" "$ref" >&2
        rm -f "$tmp"
        tmp=""
        failed=$((failed + 1))
        continue
    fi

    mkdir -p "$dest"
    if ! tar -xzf "$tmp" -C "$dest" --strip-components=1; then
        printf '  ✗ %-55s %s (tar extract failed)\n' "$repo" "$ref" >&2
        rm -f "$tmp"
        tmp=""
        rm -rf "$dest"
        failed=$((failed + 1))
        continue
    fi
    rm -f "$tmp"
    tmp=""

    # Post-check: an empty dest means the ref pointed at an empty tree (highly
    # unusual for a Terraform module) or the strip-components landed wrong.
    # Either way, the corpus for this repo is not usable.
    if [ ! "$(ls -A "$dest" 2>/dev/null)" ]; then
        printf '  ✗ %-55s %s (empty extraction)\n' "$repo" "$ref" >&2
        rm -rf "$dest"
        failed=$((failed + 1))
        continue
    fi

    printf '  ✓ %-55s %s\n' "$repo" "$ref"
    fetched=$((fetched + 1))
done 3< "$REPOS_FILE"

echo
echo "fetched: $fetched   cached: $cached   failed: $failed"

[ "$failed" -eq 0 ]
