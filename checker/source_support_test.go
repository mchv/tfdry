// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
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
