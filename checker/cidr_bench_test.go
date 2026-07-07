// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ── E101 benchmarks ──────────────────────────────────────────────────────────
//
// The corpus at bench/attr-corpus/values/cidr.txt is the hermetic input for
// these benchmarks (see PR #35 for how it's built). Loading is fatal-on-error
// so a corpus regression surfaces immediately instead of silently producing
// empty benchmarks that report artificially fast timings.

// corpusCIDRPath is resolved relative to the checker package directory (the
// working directory when `go test ./checker` runs). Kept as a var so a rare
// out-of-tree consumer could point at their own copy without patching.
var corpusCIDRPath = filepath.Join("..", "bench", "attr-corpus", "values", "cidr.txt")

// loadCorpusCIDRs reads the committed corpus values file and returns the
// individual CIDR strings, one per line. Empty lines are dropped.
func loadCorpusCIDRs(tb testing.TB) []string {
	tb.Helper()
	f, err := os.Open(corpusCIDRPath)
	if err != nil {
		tb.Fatalf("open corpus: %v — has bench/attr-corpus/values/cidr.txt been generated?", err)
	}
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if err := s.Err(); err != nil {
		tb.Fatalf("scan corpus: %v", err)
	}
	if len(out) == 0 {
		tb.Fatalf("corpus at %s is empty", corpusCIDRPath)
	}
	return out
}

// synthCIDRDir returns a directory containing a single .tf file that exercises
// every scalar trigger with a rotating pick from the corpus, plus a single
// list-typed attribute whose elements are the full corpus. This shape reflects
// the mixed scalar/list workload real modules produce.
func synthCIDRDir(tb testing.TB, values []string) string {
	tb.Helper()
	dir := tb.TempDir()
	var buf []byte
	// Scalar attributes — rotate through the corpus so every scalar trigger
	// sees a real-world value rather than the same repeated literal.
	buf = append(buf, "resource \"aws_vpc\" \"scalar_bench\" {\n"...)
	scalarAttrs := []string{
		"cidr_block", "destination_cidr_block", "destination_ipv6_cidr_block",
		"source_cidr_block", "ipv6_cidr_block", "source_ipv6_cidr_block",
		"cluster_service_cidr", "primary_vpc_cidr", "secondary_vpc_cidr",
		"tgw_destination_cidr", "vpc_cidr",
	}
	for i, attr := range scalarAttrs {
		buf = fmt.Appendf(buf, "  %s = %q\n", attr, values[i%len(values)])
	}
	buf = append(buf, "}\n\n"...)

	// List-typed attribute — every corpus value in one list so the list
	// walker sees a realistic-length payload.
	buf = append(buf, "resource \"aws_security_group\" \"list_bench\" {\n"...)
	buf = append(buf, "  cidr_blocks = [\n"...)
	for _, v := range values {
		buf = fmt.Appendf(buf, "    %q,\n", v)
	}
	buf = append(buf, "  ]\n}\n"...)

	if err := os.WriteFile(filepath.Join(dir, "main.tf"), buf, 0o644); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// BenchmarkE101_ValidateOnly measures the raw net/netip.ParsePrefix cost
// against every corpus entry. Isolates the validation cost from the HCL walk
// so a regression in the trigger loop can be distinguished from a regression
// in the parser dependency.
func BenchmarkE101_ValidateOnly(b *testing.B) {
	values := loadCorpusCIDRs(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, v := range values {
			err := validateCIDR(v)
			sink = err
		}
	}
}

// BenchmarkE101_Corpus measures the end-to-end check cost against a synthetic
// .tf file populated with corpus values across all scalar triggers plus one
// list trigger holding every corpus entry. This is the shape a real
// invocation of `tfdry --checks=E101` would face on a module heavy in
// network resources.
func BenchmarkE101_Corpus(b *testing.B) {
	values := loadCorpusCIDRs(b)
	dir := synthCIDRDir(b, values)
	files, _, _ := ParseDir(context.Background(), dir)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var v []Violation
		for _, f := range files {
			v = append(v, checkCIDR(f)...)
		}
		sink = v
	}
}

// BenchmarkE101_DispatchOnly measures the cost of Run() when E101 is enabled
// but no attribute in the input file triggers it. This isolates the dispatch
// overhead — matters because we don't want enabling E101 to noticeably slow
// down modules that don't touch network resources.
func BenchmarkE101_DispatchOnly(b *testing.B) {
	dir := tfDir(b, 5, 10) // 5 files, 10 locals each — no cidr attributes
	files, _, _ := ParseDir(context.Background(), dir)
	cs := CheckSet{"E101": struct{}{}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		v, _ := Run(context.Background(), files, cs, dir)
		sink = v
	}
}
