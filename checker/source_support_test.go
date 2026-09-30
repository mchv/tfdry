// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

func TestSourceSupport_CanonicalAndCopied(t *testing.T) {
	t.Parallel()
	first := checker.SourceSupport()
	if first.Syntax != "native_hcl" {
		t.Fatalf("Syntax = %q, want native_hcl", first.Syntax)
	}
	if want := []string{"terraform", "opentofu"}; !slices.Equal(first.Dialects, want) {
		t.Fatalf("Dialects = %v, want %v", first.Dialects, want)
	}
	if want := []string{".tf", ".tofu"}; !slices.Equal(first.FileExtensions, want) {
		t.Fatalf("FileExtensions = %v, want %v", first.FileExtensions, want)
	}

	first.Dialects[0] = "mutated"
	first.FileExtensions[0] = ".mutated"
	second := checker.SourceSupport()
	if want := []string{"terraform", "opentofu"}; !slices.Equal(second.Dialects, want) {
		t.Fatalf("mutating one result changed canonical Dialects: %v", second.Dialects)
	}
	if want := []string{".tf", ".tofu"}; !slices.Equal(second.FileExtensions, want) {
		t.Fatalf("mutating one result changed canonical FileExtensions: %v", second.FileExtensions)
	}
}

func TestSourceSupport_AdvertisedExtensionsSelectedByLoaders(t *testing.T) {
	t.Parallel()
	support := checker.SourceSupport()
	files := map[string]string{
		"ignored.tf.json":   `{"locals":{"ignored":true}}`,
		"ignored.tofu.json": `{"locals":{"ignored":true}}`,
	}
	want := make([]string, len(support.FileExtensions))
	for i, extension := range support.FileExtensions {
		name := fmt.Sprintf("advertised-%02d%s", i, extension)
		files[name] = fmt.Sprintf("locals { value_%d = \"selected\" }\n", i)
		want[i] = name
	}
	dir := writeTFDir(t, files)
	ctx := context.Background()

	semantic, semanticViolations, err := checker.ParseDir(ctx, dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	if len(semanticViolations) != 0 {
		t.Fatalf("advertised semantic sources produced parse violations: %+v", semanticViolations)
	}
	semanticNames := make([]string, len(semantic))
	for i, file := range semantic {
		semanticNames[i] = file.Name
	}
	if !slices.Equal(semanticNames, want) {
		t.Fatalf("semantic loader selected %v, want advertised extensions %v", semanticNames, want)
	}

	physical, physicalViolations, err := checker.ParseDirForFormat(ctx, dir)
	if err != nil {
		t.Fatalf("ParseDirForFormat: %v", err)
	}
	if len(physicalViolations) != 0 {
		t.Fatalf("advertised physical sources produced parse violations: %+v", physicalViolations)
	}
	physicalNames := make([]string, len(physical))
	for i, file := range physical {
		physicalNames[i] = file.Name
	}
	if !slices.Equal(physicalNames, want) {
		t.Fatalf("physical loader selected %v, want advertised extensions %v", physicalNames, want)
	}
}
