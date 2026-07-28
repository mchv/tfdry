// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import "testing"

// ── E204 benchmarks — semantic dispatch and zero-allocation validators ─────

func BenchmarkE204_ValidateOnly(b *testing.B) {
	inputs := []string{
		"my-app.logs.bucket-2026",
		"amzn-s3-demo-bucket",
		"logs-2026-01",
		"1abc2",
		"ab",
		"MyBucket",
		"my_bucket",
		"192.168.5.4",
		"example..com",
		".mybucket",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validateS3BucketName(inputs[i%len(inputs)])
	}
}

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

func BenchmarkE204_ValidateDirectory(b *testing.B) {
	inputs := []string{
		"example--usw2-az1--x-s3",
		"example--usw2-xxx-lz1--x-s3",
		"plain-directory-bucket",
		"example----x-s3",
		"example.logs--usw2-az1--x-s3",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = validateS3DirectoryBucketName(inputs[i%len(inputs)])
	}
}

// BenchmarkE204_WalkerWithTriggers exercises both validated context kinds.
func BenchmarkE204_WalkerWithTriggers(b *testing.B) {
	tf := ""
	for i := 0; i < 5; i++ {
		tf += "resource \"aws_s3_bucket\" \"general" + itoa(i) + "\" {\n"
		tf += "  bucket = \"general-" + itoa(i) + "-bucket\"\n"
		tf += "}\n"
		tf += "resource \"aws_s3_directory_bucket\" \"directory" + itoa(i) + "\" {\n"
		tf += "  bucket = \"directory-" + itoa(i) + "--usw2-az1--x-s3\"\n"
		tf += "}\n"
	}

	files, err := parseFile(tf, "main.tf")
	if err != nil {
		b.Fatalf("parse: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, file := range files {
			_ = checkS3BucketName(file)
		}
	}
}

// BenchmarkE204_ClassifiedSkips measures known contexts whose values can be
// access-point ARNs, grandfathered names, or multiple bucket subtypes.
func BenchmarkE204_ClassifiedSkips(b *testing.B) {
	const accessPointARN = "arn:aws:s3:eu-west-1:123456789012:accesspoint/build-artefacts"
	tf := ""
	for i := 0; i < 5; i++ {
		tf += "resource \"aws_s3_object\" \"object" + itoa(i) + "\" {\n"
		tf += "  bucket = \"" + accessPointARN + "\"\n"
		tf += "  key = \"artifact\"\n"
		tf += "}\n"
		tf += "resource \"aws_s3_bucket_policy\" \"policy" + itoa(i) + "\" {\n"
		tf += "  bucket = \"Legacy_Bucket\"\n"
		tf += "  policy = \"{}\"\n"
		tf += "}\n"
	}

	files, err := parseFile(tf, "main.tf")
	if err != nil {
		b.Fatalf("parse: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, file := range files {
			_ = checkS3BucketName(file)
		}
	}
}

// BenchmarkE204_NoTriggers measures fail-closed misses for unrelated provider
// resources. Every top-level resource pays this dispatch cost.
func BenchmarkE204_NoTriggers(b *testing.B) {
	sizes := []int{10, 50, 200}
	for _, n := range sizes {
		b.Run("resources="+itoa(n), func(b *testing.B) {
			tf := ""
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
				for _, file := range files {
					_ = checkS3BucketName(file)
				}
			}
		})
	}
}
