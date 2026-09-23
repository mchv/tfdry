// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkDefaultCLIInProcess covers default run orchestration and output but
// intentionally excludes process startup and signal setup.
func BenchmarkDefaultCLIInProcess(b *testing.B) {
	workloads := []struct {
		name         string
		files        int
		paired       bool
		override     bool
		locals       int
		replacements int
	}{
		{name: "ordinary-50", files: 50},
		{name: "paired-25", files: 25, paired: true},
		{name: "override-1", override: true},
		{name: "override-1000-full", locals: 1000, replacements: 1000},
	}
	for _, workload := range workloads {
		workload := workload
		b.Run(workload.name, func(b *testing.B) {
			dir := b.TempDir()
			if workload.locals > 0 {
				writeLargeLocalsBenchmark(b, dir, workload.locals, workload.replacements)
			} else if workload.override {
				writeBenchmarkFile(b, dir, "main.tf", "locals { value = \"base\" }\noutput \"value\" { value = local.value }\n")
				writeBenchmarkFile(b, dir, "override.tf", "locals { value = \"override\" }\n")
			} else {
				for i := range workload.files {
					base := fmt.Sprintf("file_%03d", i)
					src := fmt.Sprintf("output \"value_%03d\" { value = \"ok\" }\n", i)
					writeBenchmarkFile(b, dir, base+".tf", src)
					if workload.paired {
						writeBenchmarkFile(b, dir, base+".tofu", src)
					}
				}
			}
			args := []string{dir}
			if code := run(context.Background(), args, io.Discard, io.Discard); code != 0 {
				b.Fatalf("initial run exit = %d", code)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if code := run(context.Background(), args, io.Discard, io.Discard); code != 0 {
					b.Fatalf("run exit = %d", code)
				}
			}
		})
	}
}

func writeLargeLocalsBenchmark(b *testing.B, dir string, locals, replacements int) {
	b.Helper()
	var primary, override, output strings.Builder
	primary.WriteString("locals {\n")
	override.WriteString("locals {\n")
	output.WriteString("output \"values\" {\n  value = [\n")
	for i := range locals {
		fmt.Fprintf(&primary, "  value_%04d = \"base-%d\"\n", i, i)
		fmt.Fprintf(&output, "    local.value_%04d,\n", i)
		if i < replacements {
			fmt.Fprintf(&override, "  value_%04d = \"override-%d\"\n", i, i)
		}
	}
	primary.WriteString("}\n")
	override.WriteString("}\n")
	output.WriteString("  ]\n}\n")
	writeBenchmarkFile(b, dir, "main.tf", primary.String()+output.String())
	writeBenchmarkFile(b, dir, "override.tf", override.String())
}

func writeBenchmarkFile(b *testing.B, dir, name, content string) {
	b.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		b.Fatal(err)
	}
}
