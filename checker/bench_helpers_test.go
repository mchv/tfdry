// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"context"
	"os"
	"path/filepath"
)

// ── Shared bench helpers ────────────────────────────────────────────────────
//
// This file hosts helpers shared across every *_bench_test.go in the
// checker package (E101 CIDR, E201 region, E202 account_id, E203 ARN).
// They used to live in region_bench_test.go, but that created an
// implicit cross-file dependency where moving/deleting region_bench_test.go
// would silently break the ARN and account_id benchmarks. Kept
// bench-specific (not test-specific) since the wider test suite uses
// its own harness in checks_test.go.

// parseFile is a thin test-helper wrapper over ParseDir that avoids
// touching the filesystem beyond a temp directory — writes the source
// to a temp file and parses it back. Used by the benchmarks to build
// synthetic fixtures without leaking parser plumbing into every bench.
func parseFile(source, name string) ([]ParsedFile, error) {
	dir, err := os.MkdirTemp("", "tfdry-bench-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		return nil, err
	}
	parsed, _, err := ParseDir(context.Background(), dir)
	return parsed, err
}

// itoa is a tiny int→string helper — avoids the strconv import in a bench
// file (keeps the individual bench files self-contained).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// splitLines returns the newline-delimited lines of s, without a trailing
// empty entry for a terminal newline. Local helper to avoid the `strings`
// import overhead in per-check bench files.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
