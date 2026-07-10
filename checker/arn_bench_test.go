// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"os"
	"path/filepath"
	"testing"
)

// ── E203 benchmarks — SIMD-adjacent IndexByte parsing verification ─────────

// BenchmarkE203_ValidateOnly measures the pure ARN validator: prefix
// check + 4× IndexByte + per-field grammar checks. The IndexByte calls
// are SIMD-accelerated in Go's stdlib on amd64/arm64.
//
// Expected: two-digit ns/op on realistic ARNs, zero allocs.
func BenchmarkE203_ValidateOnly(b *testing.B) {
	inputs := []string{
		"arn:aws:iam::aws:policy/AdministratorAccess",
		"arn:aws:s3:::my-bucket/path/to/object",
		"arn:aws:lambda:us-east-1:111111111111:function:my-func:version:5",
		"arn:aws-us-gov:iam::123456789012:role/foo",
		"arn:aws-cn:s3:::bucket",
		"not-an-arn",             // fast-reject at HasPrefix
		"arn:foobar:s3:::bucket", // partition failure
		"arn:aws:S3:::bucket",    // service failure
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = validateARN(inputs[i%len(inputs)])
	}
}

// BenchmarkE203_Corpus measures the walker on the harvested ARN corpus
// (64 real ARNs from open-source Terraform modules). Zero-alloc target.
func BenchmarkE203_Corpus(b *testing.B) {
	entries, err := os.ReadFile(filepath.Join("..", "bench", "attr-corpus", "values", "arn.txt"))
	if err != nil {
		b.Skipf("corpus not available: %v", err)
	}

	var tf string
	i := 0
	for _, r := range splitLines(string(entries)) {
		if r == "" {
			continue
		}
		tf += "resource \"aws_iam_role_policy_attachment\" \"r" + itoa(i) + "\" {\n"
		tf += "  policy_arn = \"" + r + "\"\n"
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
			_ = checkARN(f)
		}
	}
}

// BenchmarkE203_NoTriggers measures walker cost on files with no
// _arn / _arns attributes — every attribute pays a suffix-check price.
// Establishes the per-attribute overhead of the HasSuffix scan.
func BenchmarkE203_NoTriggers(b *testing.B) {
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
					_ = checkARN(f)
				}
			}
		})
	}
}

// BenchmarkE203_TemplatesCorpus measures the composed-form path over
// the harvested ARN templates (real interpolation patterns from the
// wild — `${data.aws_partition.current.partition}`, `${local.partition}`).
// This exercises Compose + validateARNFields(permissive=true).
func BenchmarkE203_TemplatesCorpus(b *testing.B) {
	entries, err := os.ReadFile(filepath.Join("..", "bench", "attr-corpus", "values", "arn_templates.txt"))
	if err != nil {
		b.Skipf("template corpus not available: %v", err)
	}

	var tf string
	i := 0
	for _, r := range splitLines(string(entries)) {
		if r == "" {
			continue
		}
		// The corpus entries are already quoted strings; use them
		// as attribute values verbatim.
		tf += "resource \"aws_iam_role_policy_attachment\" \"r" + itoa(i) + "\" {\n"
		tf += "  policy_arn = " + r + "\n"
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
			_ = checkARN(f)
		}
	}
}
