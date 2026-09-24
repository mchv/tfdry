// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sink prevents the compiler from eliminating benchmark results (dead code elimination).
var (
	sink                any
	effectiveConfigSink effectiveConfig
)

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
				f, v, _ := ParseDir(context.Background(), dir)
				sink = f
				sink = v
			}
		})
	}
}

func BenchmarkParseDirViews(b *testing.B) {
	for _, basenames := range []int{1, 10, 50} {
		for _, paired := range []bool{false, true} {
			name := fmt.Sprintf("basenames=%d/paired=%t", basenames, paired)
			b.Run(name, func(b *testing.B) {
				dir := b.TempDir()
				for i := range basenames {
					base := fmt.Sprintf("file_%03d", i)
					if err := os.WriteFile(filepath.Join(dir, base+".tf"), []byte("locals { value = \"terraform\" }\n"), 0o644); err != nil {
						b.Fatal(err)
					}
					if paired {
						if err := os.WriteFile(filepath.Join(dir, base+".tofu"), []byte("locals { value = \"opentofu\" }\n"), 0o644); err != nil {
							b.Fatal(err)
						}
					}
				}
				semanticFiles, semanticViolations, err := ParseDir(context.Background(), dir)
				if err != nil || len(semanticViolations) != 0 {
					b.Fatalf("ParseDir: err=%v violations=%v", err, semanticViolations)
				}
				physicalFiles, physicalViolations, err := ParseDirForFormat(context.Background(), dir)
				if err != nil || len(physicalViolations) != 0 {
					b.Fatalf("ParseDirForFormat: err=%v violations=%v", err, physicalViolations)
				}
				views, err := ParseDirViews(context.Background(), dir)
				if err != nil || len(views.PhysicalViolations) != 0 || len(views.SemanticViolations) != 0 {
					b.Fatalf("ParseDirViews: err=%v physical=%v semantic=%v", err, views.PhysicalViolations, views.SemanticViolations)
				}
				if len(semanticFiles) != len(views.SemanticFiles) || len(physicalFiles) != len(views.PhysicalFiles) {
					b.Fatalf("view size mismatch: two-pass=%d/%d one-pass=%d/%d", len(semanticFiles), len(physicalFiles), len(views.SemanticFiles), len(views.PhysicalFiles))
				}
				b.Run("two-pass", func(b *testing.B) {
					b.ReportAllocs()
					b.ReportMetric(float64(len(physicalFiles)), "physical/op")
					b.ReportMetric(float64(len(semanticFiles)), "semantic/op")
					for range b.N {
						semanticFiles, semanticViolations, _ := ParseDir(context.Background(), dir)
						physicalFiles, physicalViolations, _ := ParseDirForFormat(context.Background(), dir)
						sink = semanticFiles
						sink = semanticViolations
						sink = physicalFiles
						sink = physicalViolations
					}
				})
				b.Run("one-pass", func(b *testing.B) {
					b.ReportAllocs()
					b.ReportMetric(float64(len(views.PhysicalFiles)), "physical/op")
					b.ReportMetric(float64(len(views.SemanticFiles)), "semantic/op")
					for range b.N {
						views, _ := ParseDirViews(context.Background(), dir)
						sink = views
					}
				})
			})
		}
	}
}

// ── buildLocalsMap: parameterised by local count ──────────────────────────────
// Use: benchstat -col /locals results.txt

func BenchmarkBuildLocalsMap(b *testing.B) {
	for _, locals := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("locals=%d", locals), func(b *testing.B) {
			dir := tfDir(b, 5, locals/5)
			files, _, _ := ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m, v := buildLocalsMap(files)
				sink = m
				sink = v
			}
		})
	}
}

// ── effective override projection ────────────────────────────────────────────

func BenchmarkBuildEffectiveConfig(b *testing.B) {
	for _, overrides := range []int{0, 1, 5} {
		b.Run(fmt.Sprintf("overrides=%d", overrides), func(b *testing.B) {
			dir := b.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`locals { value = "base" }
resource "example" "x" { value = "base" }
`), 0o644); err != nil {
				b.Fatal(err)
			}
			for i := range overrides {
				name := fmt.Sprintf("%02d_override.tf", i)
				src := fmt.Sprintf("locals { value = \"override-%d\" }\nresource \"example\" \"x\" { value = \"override-%d\" }\n", i, i)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			files, violations, err := ParseDir(context.Background(), dir)
			if err != nil || len(violations) != 0 {
				b.Fatalf("ParseDir: err=%v violations=%v", err, violations)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				effectiveConfigSink = buildEffectiveConfig(files)
			}
		})
	}
}

func BenchmarkBuildEffectiveConfigLocals(b *testing.B) {
	for _, locals := range []int{10, 100, 1000} {
		replacementCases := []int{1, max(1, locals/10), locals}
		seen := make(map[int]struct{}, len(replacementCases))
		for _, replacements := range replacementCases {
			if _, duplicate := seen[replacements]; duplicate {
				continue
			}
			seen[replacements] = struct{}{}
			name := fmt.Sprintf("locals=%d/replaced=%d", locals, replacements)
			b.Run(name, func(b *testing.B) {
				dir := b.TempDir()
				var primary, override strings.Builder
				primary.WriteString("locals {\n")
				override.WriteString("locals {\n")
				for i := range locals {
					fmt.Fprintf(&primary, "  value_%04d = \"base-%d\"\n", i, i)
					if i < replacements {
						fmt.Fprintf(&override, "  value_%04d = \"override-%d\"\n", i, i)
					}
				}
				primary.WriteString("}\n")
				override.WriteString("}\n")
				if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(primary.String()), 0o644); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "override.tf"), []byte(override.String()), 0o644); err != nil {
					b.Fatal(err)
				}
				files, violations, err := ParseDir(context.Background(), dir)
				if err != nil || len(violations) != 0 {
					b.Fatalf("ParseDir: err=%v violations=%v", err, violations)
				}
				b.ReportAllocs()
				b.ResetTimer()
				b.ReportMetric(float64(locals), "locals/op")
				b.ReportMetric(float64(replacements), "replaced/op")
				for range b.N {
					effectiveConfigSink = buildEffectiveConfig(files)
				}
			})
		}
	}
}

func BenchmarkBuildEffectiveConfigSparse(b *testing.B) {
	for _, files := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := b.TempDir()
			for i := range files {
				name := fmt.Sprintf("file_%04d.tf", i)
				src := fmt.Sprintf("resource \"example\" \"file_%04d\" { value = \"base\" }\n", i)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
					b.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "override.tf"), []byte(`resource "example" "file_0000" { value = "override" }
`), 0o644); err != nil {
				b.Fatal(err)
			}
			parsed, violations, err := ParseDir(context.Background(), dir)
			if err != nil || len(violations) != 0 {
				b.Fatalf("ParseDir: err=%v violations=%v", err, violations)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.ReportMetric(float64(files), "files/op")
			for range b.N {
				effectiveConfigSink = buildEffectiveConfig(parsed)
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
			parsed, _, _ := ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ReportMetric(float64(files), "files/op")
			b.ResetTimer()
			for range b.N {
				v, _ := Run(context.Background(), parsed, nil, dir)
				sink = v
			}
		})
	}
}

// ── Checker pipeline (ParseDir + Run; excludes CLI/output) ────────────────────

func BenchmarkCheckerPipeline(b *testing.B) {
	for _, files := range []int{0, 5, 10, 50} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := tfDir(b, files, 10)
			b.ReportAllocs()
			b.ReportMetric(float64(files), "files/op")
			b.ResetTimer()
			for range b.N {
				parsed, _, _ := ParseDir(context.Background(), dir)
				v, _ := Run(context.Background(), parsed, nil, dir)
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
			parsed, _, _ := ParseDir(context.Background(), dir)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				v, _ := CheckFormat(context.Background(), parsed)
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
				parsed, _, _ := ParseDir(context.Background(), dir)
				b.StartTimer()
				fixed, v, _ := FixFormat(context.Background(), parsed, dir)
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
			parsed, _, _ := ParseDir(context.Background(), dir)
			cs := CheckSet{"E006": {}, "E007": {}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				v, _ := Run(context.Background(), parsed, cs, dir)
				sink = v
			}
		})
	}
}
