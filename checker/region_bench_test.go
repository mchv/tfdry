// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"os"
	"path/filepath"
	"testing"
)

// ── E201 benchmarks — zero-alloc hot path verification ─────────────────────

// BenchmarkE201_ValidateOnly measures the pure validator (map lookup +
// return). Expected: zero allocs/op, sub-100ns.
func BenchmarkE201_ValidateOnly(b *testing.B) {
	// Rotate through a mix of valid and invalid regions to exercise both
	// hot paths (found + not-found).
	inputs := []string{
		"us-east-1",     // valid, first shard
		"eu-west-1",     // valid, middle shard
		"cn-north-1",    // valid, china partition
		"atlantis-cx-1", // invalid, right shape
		"us-east-11",    // invalid, typo
		"",              // empty (map miss, cheap)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validateRegion(inputs[i%len(inputs)])
	}
}

// BenchmarkE201_Corpus measures the full checker on the region corpus.
// The corpus is thin (3 entries) so this bench is small — enough to
// establish the zero-alloc contract on the walker path, not to compare
// against E101's larger corpus bench.
func BenchmarkE201_Corpus(b *testing.B) {
	entries, err := os.ReadFile(filepath.Join("..", "bench", "attr-corpus", "values", "region.txt"))
	if err != nil {
		b.Skipf("corpus not available: %v", err)
	}

	// Build a synthetic .tf file with one nested region attribute per corpus
	// entry. Each entry goes into its own resource block so HCL doesn't
	// collapse duplicate attribute keys.
	tf := "resource \"aws_s3_bucket_replication_configuration\" \"x\" {\n"
	for _, r := range splitLines(string(entries)) {
		if r == "" {
			continue
		}
		tf += "  destination { region = \"" + r + "\" }\n"
	}
	tf += "}\n"

	files, err := parseFile(tf, "main.tf")
	if err != nil {
		b.Fatalf("parse: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range files {
			_ = checkRegion(f)
		}
	}
}

// BenchmarkE201_NoTriggers exercises the walker on a file whose
// attributes are all non-triggers. This measures the "trigger map miss"
// cost that every attribute in a real Terraform module pays. Expected:
// zero allocs, sub-linear per-attribute cost.
func BenchmarkE201_NoTriggers(b *testing.B) {
	sizes := []int{10, 50, 200}
	for _, n := range sizes {
		b.Run("attrs="+itoa(n), func(b *testing.B) {
			tf := "resource \"aws_s3_bucket\" \"x\" {\n"
			for i := 0; i < n; i++ {
				tf += "  key" + itoa(i) + " = \"value\"\n"
			}
			tf += "}\n"

			files, err := parseFile(tf, "main.tf")
			if err != nil {
				b.Fatalf("parse: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, f := range files {
					_ = checkRegion(f)
				}
			}
		})
	}
}
