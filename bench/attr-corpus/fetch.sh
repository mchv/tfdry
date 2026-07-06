#!/bin/sh
# bench/attr-corpus/fetch.sh — fetch pinned Terraform corpora from GitHub.
#
# Reads repos.txt line by line and downloads each `<owner>/<repo> <tag>` as a
# tar.gz snapshot from GitHub into `bench/attr-corpus/files/<owner>-<repo>/`.
# The `files/` directory is gitignored — this script is idempotent.
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

fetched=0
cached=0
failed=0

# Read repos.txt with an explicit fd so counter updates survive the loop
# (a while-read pipeline would put the body in a subshell).
while IFS= read -r line <&3 || [ -n "$line" ]; do
    # Strip trailing CR (Windows CRLF checkouts), comments, and surrounding
    # whitespace. Without the tr, a stray \r ends up in the tag and produces
    # an invalid tarball URL that fails with a 404.
    entry=$(echo "$line" | tr -d '\r' | sed -e 's/#.*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [ -z "$entry" ] && continue

    repo=$(echo "$entry" | awk '{print $1}')
    tag=$(echo  "$entry" | awk '{print $2}')

    if [ -z "$repo" ] || [ -z "$tag" ]; then
        echo "fetch.sh: malformed entry: $line" >&2
        failed=$((failed + 1))
        continue
    fi

    dest_name=$(echo "$repo" | tr '/' '-')
    dest="$FILES_DIR/$dest_name"

    if [ -d "$dest" ] && [ "$(ls -A "$dest" 2>/dev/null)" ]; then
        printf '  ↻ %-55s %s (cached)\n' "$repo" "$tag"
        cached=$((cached + 1))
        continue
    fi

    url="https://github.com/${repo}/archive/refs/tags/${tag}.tar.gz"

    # Download to a temp file first so curl's exit code is checked before tar
    # runs. `curl | tar` would let tar succeed on empty stdin and hide the 404.
    # Plain `mktemp` (no template) for cross-platform portability: BSD mktemp
    # treats the argument to `-t` as a prefix, not a template, and does not
    # accept suffixes after the XXXXXX placeholders.
    tmp=$(mktemp)
    if ! curl -fsSL "$url" -o "$tmp"; then
        printf '  ✗ %-55s %s (HTTP error)\n' "$repo" "$tag" >&2
        rm -f "$tmp"
        failed=$((failed + 1))
        continue
    fi

    mkdir -p "$dest"
    if ! tar -xzf "$tmp" -C "$dest" --strip-components=1; then
        printf '  ✗ %-55s %s (tar extract failed)\n' "$repo" "$tag" >&2
        rm -f "$tmp"
        rm -rf "$dest"
        failed=$((failed + 1))
        continue
    fi
    rm -f "$tmp"

    # Post-check: an empty dest means the tag pointed at an empty tree (highly
    # unusual for a Terraform module) or the strip-components landed wrong.
    # Either way, the corpus for this repo is not usable.
    if [ ! "$(ls -A "$dest" 2>/dev/null)" ]; then
        printf '  ✗ %-55s %s (empty extraction)\n' "$repo" "$tag" >&2
        rm -rf "$dest"
        failed=$((failed + 1))
        continue
    fi

    printf '  ✓ %-55s %s\n' "$repo" "$tag"
    fetched=$((fetched + 1))
done 3< "$REPOS_FILE"

echo
echo "fetched: $fetched   cached: $cached   failed: $failed"

[ "$failed" -eq 0 ]
