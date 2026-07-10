// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"os"
	"path/filepath"
	"testing"
)

// ── E202 benchmarks — zero-alloc branchless byte-loop verification ─────────

// BenchmarkE202_ValidateOnly measures the pure validator (length check +
// branchless digit loop). Expected: single-digit ns/op, zero allocs.
func BenchmarkE202_ValidateOnly(b *testing.B) {
	inputs := []string{
		"123456789012", // valid
		"000000000000", // valid, all-zeroes
		"12345678901",  // 11 digits (fails length)
		"12345678901a", // 12 chars, letter (fails digit)
		"1234-5678-90", // 12 chars, non-digit mix
		"",             // empty (fails length)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validateAccountID(inputs[i%len(inputs)])
	}
}

// BenchmarkE202_Corpus measures the full checker on the account_id corpus.
// Small corpus (4 entries) — sufficient to establish the zero-alloc contract
// on the walker path. Each entry goes into a distinct resource block so HCL
// doesn't collapse duplicate attribute keys.
func BenchmarkE202_Corpus(b *testing.B) {
	entries, err := os.ReadFile(filepath.Join("..", "bench", "attr-corpus", "values", "account_id.txt"))
	if err != nil {
		b.Skipf("corpus not available: %v", err)
	}

	var tf string
	i := 0
	for _, r := range splitLines(string(entries)) {
		if r == "" {
			continue
		}
		tf += "resource \"aws_accounts\" \"r" + itoa(i) + "\" {\n"
		tf += "  account_id = \"" + r + "\"\n"
		tf += "}\n"
		i++
	}

	files, err := parseFile(tf, "main.tf")
	if err != nil {
		b.Fatalf("parse: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range files {
			_ = checkAccountID(f)
		}
	}
}

// BenchmarkE202_NoTriggers exercises the walker on files with no trigger
// attributes. Measures the trigger-map-miss cost per attribute.
func BenchmarkE202_NoTriggers(b *testing.B) {
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
					_ = checkAccountID(f)
				}
			}
		})
	}
}
