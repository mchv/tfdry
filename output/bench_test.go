// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package output_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/mchv/tfdry/checker"
	"github.com/mchv/tfdry/output"
)

var sink any

// makeViolations builds n synthetic violations with realistic shapes.
func makeViolations(n int) []checker.Violation {
	vs := make([]checker.Violation, n)
	for i := 0; i < n; i++ {
		vs[i] = checker.Violation{
			Code:     "E001",
			Severity: "error",
			File:     fmt.Sprintf("modules/example/file_%d.tf", i),
			Line:     i + 1,
			Message:  fmt.Sprintf("variable \"unused_%d\" is declared but not referenced anywhere", i),
		}
	}
	return vs
}

// BenchmarkWriteJSON measures Report → JSON with varying violation counts.
// Use: benchstat -col /violations <file>
func BenchmarkWriteJSON(b *testing.B) {
	for _, n := range []int{0, 1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("violations=%d", n), func(b *testing.B) {
			r := output.NewReport("/some/terraform/dir", makeViolations(n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := output.WriteJSON(io.Discard, r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDescribeJSON measures the describe --json output path.
func BenchmarkDescribeJSON(b *testing.B) {
	checks := checker.AllChecks()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := output.WriteChecksJSON(io.Discard, checks); err != nil {
			b.Fatal(err)
		}
	}
	sink = checks
}

// BenchmarkWriteHuman measures Report → human-readable text with varying
// violation counts. Use: benchstat -col /violations <file>
func BenchmarkWriteHuman(b *testing.B) {
	for _, n := range []int{0, 1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("violations=%d", n), func(b *testing.B) {
			r := output.NewReport("/some/terraform/dir", makeViolations(n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				output.WriteHuman(io.Discard, r)
			}
		})
	}
}

// makeFixedFiles builds n synthetic root-relative fixed-file display paths.
func makeFixedFiles(n int) []string {
	files := make([]string, n)
	for i := range files {
		files[i] = fmt.Sprintf("modules/example/file_%d.tf", i)
	}
	return files
}

// BenchmarkNewReportWithFixedFiles measures fixed-path copying, sanitisation,
// and sorting separately from rendering.
func BenchmarkNewReportWithFixedFiles(b *testing.B) {
	for _, n := range []int{0, 10, 1000} {
		b.Run(fmt.Sprintf("fixed_files=%d", n), func(b *testing.B) {
			fixedFiles := makeFixedFiles(n)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				sink = output.NewReportWithFixedFiles("/some/terraform/dir", nil, fixedFiles)
			}
		})
	}
}

// BenchmarkWriteJSONFixedFiles measures rendering reports containing only
// successful rewrites.
func BenchmarkWriteJSONFixedFiles(b *testing.B) {
	for _, n := range []int{0, 10, 1000} {
		b.Run(fmt.Sprintf("fixed_files=%d", n), func(b *testing.B) {
			r := output.NewReportWithFixedFiles("/some/terraform/dir", nil, makeFixedFiles(n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := output.WriteJSON(io.Discard, r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWriteHumanFixedFiles measures fixed-only human rendering while
// retaining the single-buffer write path.
func BenchmarkWriteHumanFixedFiles(b *testing.B) {
	for _, n := range []int{0, 10, 1000} {
		b.Run(fmt.Sprintf("fixed_files=%d", n), func(b *testing.B) {
			r := output.NewReportWithFixedFiles("/some/terraform/dir", nil, makeFixedFiles(n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := output.WriteHuman(io.Discard, r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
