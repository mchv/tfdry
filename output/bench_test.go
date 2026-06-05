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
