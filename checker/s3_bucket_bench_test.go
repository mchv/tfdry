// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import "testing"

// ── E204 benchmarks — zero-alloc single-pass validator verification ────────

// BenchmarkE204_ValidateOnly measures the pure validator (length filter,
// boundary check, single character-set pass, IP-shape check on remaining
// candidates). Rotates through a mix of valid and invalid inputs so that
// the microbench captures each rejection path amortised across the
// steady state. Expected: sub-100 ns/op, zero allocs.
func BenchmarkE204_ValidateOnly(b *testing.B) {
	inputs := []string{
		"my-app.logs.bucket-2026", // valid, hyphen+dot mix
		"amzn-s3-demo-bucket",     // valid grammar (reserved-prefix rule is v2)
		"logs-2026-01",            // valid, digits inline
		"1abc2",                   // valid, digit-bounded
		"ab",                      // too short
		"MyBucket",                // uppercase reject
		"my_bucket",               // underscore reject
		"192.168.5.4",             // IP-shape reject
		"example..com",            // consecutive-dot reject
		".mybucket",               // starts-with-dot reject
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validateS3BucketName(inputs[i%len(inputs)])
	}
}

// BenchmarkE204_ValidateOnly_HappyPath measures the validator on
// valid-only inputs — the steady-state case in a real Terraform module
// where the developer's names are correctly formed. Amortises across a
// variety of valid shapes so the branch predictor doesn't overfit.
// Isolates the cost of "confirm valid" from the reject paths.
func BenchmarkE204_ValidateOnly_HappyPath(b *testing.B) {
	valid := []string{
		"my-app-bucket",
		"my.app.bucket",
		"logs-2026-01",
		"1abc2",
		"a1b2c3-d4e5f6-g7h8i9j0",
		"prod.app.frontend.logs",
		"stage-app-frontend-logs",
		"amzn-s3-demo-bucket",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validateS3BucketName(valid[i%len(valid)])
	}
}

// BenchmarkE204_WalkerWithTriggers measures the full walker path on a
// synthetic file containing several aws_s3_* resources, each with a
// valid bucket attribute. This captures the "trigger match + attr
// walk + validator" cost as it applies to a realistic module.
func BenchmarkE204_WalkerWithTriggers(b *testing.B) {
	// A file with 10 aws_s3_* resources, each declaring a bucket name.
	// Names deliberately vary in shape to exercise different validator
	// paths.
	names := []string{
		"my-app-bucket",
		"my.app.bucket",
		"logs-2026-01",
		"1abc2",
		"a1b2c3-d4e5f6",
		"prod.frontend.logs",
		"stage-frontend-logs",
		"assets-cdn-2026",
		"customer-data-us-east-1",
		"backups.example-corp",
	}
	tf := ""
	for i, n := range names {
		tf += "resource \"aws_s3_bucket\" \"r" + itoa(i) + "\" {\n"
		tf += "  bucket = \"" + n + "\"\n"
		tf += "}\n"
	}

	files, err := parseFile(tf, "main.tf")
	if err != nil {
		b.Fatalf("parse: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range files {
			_ = checkS3BucketName(f)
		}
	}
}

// BenchmarkE204_NoTriggers exercises the walker on files with no trigger
// attributes — i.e. resources that aren't aws_s3_*, or aws_s3_* resources
// with only non-bucket attributes. Measures the "aws_s3_ prefix miss"
// cost per top-level block, which every non-S3 module pays.
func BenchmarkE204_NoTriggers(b *testing.B) {
	sizes := []int{10, 50, 200}
	for _, n := range sizes {
		b.Run("resources="+itoa(n), func(b *testing.B) {
			tf := ""
			// Non-aws_s3_* resources — E204 should skip them at the
			// prefix-check step without descending into attributes.
			for i := 0; i < n; i++ {
				tf += "resource \"aws_instance\" \"r" + itoa(i) + "\" {\n"
				tf += "  ami           = \"ami-abc\"\n"
				tf += "  instance_type = \"t3.micro\"\n"
				tf += "}\n"
			}

			files, err := parseFile(tf, "main.tf")
			if err != nil {
				b.Fatalf("parse: %v", err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, f := range files {
					_ = checkS3BucketName(f)
				}
			}
		})
	}
}
