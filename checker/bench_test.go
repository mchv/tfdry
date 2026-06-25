package checker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// sink prevents the compiler from eliminating benchmark results (dead code elimination).
var sink any

// ── Fixture generators ────────────────────────────────────────────────────────

// tfDir creates n .tf files each with m locals and m output references.
func tfDir(b testing.TB, n, m int) string {
	b.Helper()
	dir := b.TempDir()
	for i := range n {
		var buf []byte
		buf = append(buf, "locals {\n"...)
		for j := range m {
			buf = fmt.Appendf(buf, "  local_%d_%d = \"value-%d-%d\"\n", i, j, i, j)
		}
		buf = append(buf, "}\n"...)
		for j := range m {
			buf = fmt.Appendf(buf, "output \"out_%d_%d\" { value = local.local_%d_%d }\n", i, j, i, j)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.tf", i)), buf, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// tfDirUnformatted creates n unformatted .tf files (triggers E008).
func tfDirUnformatted(b testing.TB, n int) string {
	b.Helper()
	dir := b.TempDir()
	for i := range n {
		content := fmt.Sprintf("locals {\na=\"value-%d\"\n}\n", i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.tf", i)), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// tfDirWithModule creates a caller dir with a local module.
func tfDirWithModule(b testing.TB, n int) string {
	b.Helper()
	dir := b.TempDir()
	modDir := filepath.Join(dir, "modules", "m")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "variables.tf"), []byte(`
variable "name" { type = string }
variable "count" { type = number }
`), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := range n {
		content := fmt.Sprintf(`
module "m%d" {
  source = "./modules/m"
  name   = "value-%d"
  count  = %d
}
`, i, i, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.tf", i)), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// ── ParseDir: parameterised by file count ─────────────────────────────────────
// Use: benchstat -col /files results.txt

func BenchmarkParseDir(b *testing.B) {
	for _, files := range []int{0, 1, 5, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDir(b, files, 10)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				f, v, _ := checker.ParseDir(context.Background(), dir)
				sink = f
				sink = v
			}
		})
	}
}

// ── BuildLocalsMap: parameterised by local count ──────────────────────────────
// Use: benchstat -col /locals results.txt

func BenchmarkBuildLocalsMap(b *testing.B) {
	for _, locals := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("locals=%d", locals), func(b *testing.B) {
			dir := tfDir(b, 5, locals/5)
			files, _, _ := checker.ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m, v := checker.BuildLocalsMap(files)
				sink = m
				sink = v
			}
		})
	}
}

// ── Run (all checks): parameterised by file count ─────────────────────────────
// Isolates CPU-only cost (files pre-parsed). Use: benchstat -col /files results.txt

func BenchmarkRun(b *testing.B) {
	for _, files := range []int{0, 1, 5, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDir(b, files, 10)
			parsed, _, _ := checker.ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ReportMetric(float64(files), "files/op")
			b.ResetTimer()
			for range b.N {
				v, _ := checker.Run(context.Background(), parsed, nil, dir)
				sink = v
			}
		})
	}
}

// ── Full pipeline (ParseDir + Run): end-to-end cost ───────────────────────────

func BenchmarkPipeline(b *testing.B) {
	for _, files := range []int{0, 5, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDir(b, files, 10)
			b.ReportAllocs()
			b.ReportMetric(float64(files), "files/op")
			b.ResetTimer()
			for range b.N {
				parsed, _, _ := checker.ParseDir(context.Background(), dir)
				v, _ := checker.Run(context.Background(), parsed, nil, dir)
				sink = v
			}
		})
	}
}

// ── CheckFormat: parameterised by file count ──────────────────────────────────

func BenchmarkCheckFormat(b *testing.B) {
	for _, files := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDirUnformatted(b, files)
			parsed, _, _ := checker.ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				v, _ := checker.CheckFormat(context.Background(), parsed)
				sink = v
			}
		})
	}
}

// ── FixFormat: measures atomic write throughput ───────────────────────────────

func BenchmarkFixFormat(b *testing.B) {
	for _, files := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			// Re-create unformatted files each iteration so FixFormat always has work.
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				dir := tfDirUnformatted(b, files)
				parsed, _, _ := checker.ParseDir(context.Background(), dir)
				b.StartTimer()
				fixed, v, _ := checker.FixFormat(context.Background(), parsed, dir)
				sink = fixed
				sink = v
			}
		})
	}
}

// ── Module input checking: E006/E007 ─────────────────────────────────────────

func BenchmarkRunModuleChecks(b *testing.B) {
	for _, files := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDirWithModule(b, files)
			parsed, _, _ := checker.ParseDir(context.Background(), dir)
			cs := checker.CheckSet{"E006": {}, "E007": {}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				v, _ := checker.Run(context.Background(), parsed, cs, dir)
				sink = v
			}
		})
	}
}
