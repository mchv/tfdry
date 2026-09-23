// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"slices"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

func TestIsOverrideFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"override.tf", true},
		{"override.tofu", true},
		{"dev_override.tf", true},
		{"dev_override.tofu", true},
		{"override_extra.tf", false},
		{"dev_override.tf.json", false},
		{"main.tf", false},
		{"main.tofu", false},
		{"override", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isOverrideFilename(tc.name); got != tc.want {
				t.Fatalf("isOverrideFilename(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestBuildEffectiveConfig_NoOverrideReturnsOriginalBodies(t *testing.T) {
	t.Parallel()
	file := parseEffectiveTestFile(t, "main.tf", `locals { value = "base" }`)
	effective := buildEffectiveConfig([]ParsedFile{file})
	if len(effective.files) != 1 {
		t.Fatalf("effective files = %d, want 1", len(effective.files))
	}
	if effective.files[0].Body != file.Body {
		t.Fatal("no-override fast path cloned the original body")
	}
}

func TestBuildEffectiveConfig_DoesNotMutateOriginalAndRetainsRanges(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tf", `resource "example" "x" { value = "base" }`)
	override := parseEffectiveTestFile(t, "override.tf", `resource "example" "x" { value = "override" }`)
	baseAttr := base.Body.Blocks[0].Body.Attributes["value"]

	effective := buildEffectiveConfig([]ParsedFile{base, override})
	if len(effective.files) != 1 {
		t.Fatalf("effective files = %d, want 1", len(effective.files))
	}
	winning := effective.files[0].Body.Blocks[0].Body.Attributes["value"]
	if got := stringLiteralValue(winning.Expr); got != "override" {
		t.Fatalf("effective value = %q, want override", got)
	}
	if winning.NameRange.Filename != "override.tf" {
		t.Fatalf("winning filename = %q, want override.tf", winning.NameRange.Filename)
	}
	if got := stringLiteralValue(baseAttr.Expr); got != "base" {
		t.Fatalf("original AST was mutated: value = %q", got)
	}
	if base.Body.Blocks[0].Body.Attributes["value"] != baseAttr {
		t.Fatal("original attribute pointer changed")
	}
}

func parseEffectiveTestFile(t *testing.T, name, src string) ParsedFile {
	t.Helper()
	file, diags := hclsyntax.ParseConfig([]byte(src), name, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		t.Fatalf("parse %s: %s", name, diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("body type = %T, want *hclsyntax.Body", file.Body)
	}
	return ParsedFile{Name: name, Body: body, Src: []byte(src)}
}

func TestBuildEffectiveConfig_PreservesOverrideNestedBlockOrder(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tf", `resource "example" "x" {
  base {}
}`)
	override := parseEffectiveTestFile(t, "override.tf", `resource "example" "x" {
  foo {}
  bar {}
  foo {}
}`)
	effective := buildEffectiveConfig([]ParsedFile{base, override})
	blocks := effective.files[0].Body.Blocks[0].Body.Blocks
	got := make([]string, len(blocks))
	for i, block := range blocks {
		got[i] = effectiveNestedBlockType(block)
	}
	want := []string{"base", "foo", "bar", "foo"}
	if !slices.Equal(got, want) {
		t.Fatalf("nested block order = %v, want %v", got, want)
	}
}

func TestBuildEffectiveConfig_NoOverrideAllocations(t *testing.T) {
	file := parseEffectiveTestFile(t, "main.tf", `locals { value = "base" }`)
	allocs := testing.AllocsPerRun(100, func() {
		_ = buildEffectiveConfig([]ParsedFile{file})
	})
	if allocs != 0 {
		t.Fatalf("no-override allocations = %.1f, want 0", allocs)
	}
}
