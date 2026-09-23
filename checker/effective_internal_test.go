// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"fmt"
	"slices"
	"strings"
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

func TestBuildEffectiveConfig_LargeLocalsOverridePreservesOriginal(t *testing.T) {
	t.Parallel()
	const locals = 100
	var primary, override strings.Builder
	primary.WriteString("locals {\n")
	override.WriteString("locals {\n")
	for i := range locals {
		fmt.Fprintf(&primary, "  value_%03d = \"base-%d\"\n", i, i)
		fmt.Fprintf(&override, "  value_%03d = \"override-%d\"\n", i, i)
	}
	primary.WriteString("}\n")
	override.WriteString("}\n")
	base := parseEffectiveTestFile(t, "main.tf", primary.String())
	over := parseEffectiveTestFile(t, "override.tf", override.String())
	firstOriginal := base.Body.Blocks[0].Body.Attributes["value_000"]
	lastOriginal := base.Body.Blocks[0].Body.Attributes["value_099"]

	effective := buildEffectiveConfig([]ParsedFile{base, over})
	attrs := effective.files[0].Body.Blocks[0].Body.Attributes
	if got := stringLiteralValue(attrs["value_000"].Expr); got != "override-0" {
		t.Fatalf("first effective local = %q", got)
	}
	if got := stringLiteralValue(attrs["value_099"].Expr); got != "override-99" {
		t.Fatalf("last effective local = %q", got)
	}
	if base.Body.Blocks[0].Body.Attributes["value_000"] != firstOriginal ||
		base.Body.Blocks[0].Body.Attributes["value_099"] != lastOriginal {
		t.Fatal("primary locals AST was mutated")
	}
}

func TestBuildEffectiveConfig_EncryptionTargetRetainsMethodAndEnforcedOR(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tofu", `terraform {
  encryption {
    state {
      enforced = true
      method   = vars.bad
    }
  }
}`)
	override := parseEffectiveTestFile(t, "override.tofu", `terraform {
  encryption {
    state {
      enforced = false
    }
  }
}`)
	effective := buildEffectiveConfig([]ParsedFile{base, override})
	terraformBlock := effective.files[0].Body.Blocks[0]
	encryption := terraformBlock.Body.Blocks[0]
	state := encryption.Body.Blocks[0]
	if state.Body.Attributes["method"].NameRange.Filename != "main.tofu" {
		t.Fatalf("retained method file = %q", state.Body.Attributes["method"].NameRange.Filename)
	}
	if state.Body.Attributes["enforced"].NameRange.Filename != "main.tofu" {
		t.Fatalf("enforced OR did not retain true base value; file=%q", state.Body.Attributes["enforced"].NameRange.Filename)
	}
}

func TestBuildEffectiveConfig_EncryptionKeyProviderKeepsBaseMetadataAlias(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tofu", `terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      encrypted_metadata_alias = "base"
      passphrase               = "base"
    }
  }
}`)
	override := parseEffectiveTestFile(t, "override.tofu", `terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      encrypted_metadata_alias = "override"
      passphrase               = "override"
    }
  }
}`)
	encryption := effectiveEncryptionBlock(t, base, override)
	provider := findChildBlock(t, encryption.Body, "key_provider", "pbkdf2", "main")
	if got := provider.Body.Attributes["encrypted_metadata_alias"].NameRange.Filename; got != "main.tofu" {
		t.Fatalf("metadata alias file = %q, want main.tofu", got)
	}
	if got := provider.Body.Attributes["passphrase"].NameRange.Filename; got != "override.tofu" {
		t.Fatalf("passphrase file = %q, want override.tofu", got)
	}
}

func TestBuildEffectiveConfig_EncryptionEnforcedTruthTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		base     string
		override string
		wantFile string
	}{
		{name: "parenthesized true retained", base: "(true)", override: "false", wantFile: "main.tofu"},
		{name: "unary true retained", base: "!false", override: "false", wantFile: "main.tofu"},
		{name: "override true wins", base: "false", override: "!false", wantFile: "override.tofu"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := parseEffectiveTestFile(t, "main.tofu", `terraform {
  encryption {
    state {
      enforced = `+tc.base+`
    }
  }
}`)
			override := parseEffectiveTestFile(t, "override.tofu", `terraform {
  encryption {
    state {
      enforced = `+tc.override+`
    }
  }
}`)
			encryption := effectiveEncryptionBlock(t, base, override)
			state := findChildBlock(t, encryption.Body, "state")
			if got := state.Body.Attributes["enforced"].NameRange.Filename; got != tc.wantFile {
				t.Fatalf("enforced file = %q, want %q", got, tc.wantFile)
			}
		})
	}
}

func TestBuildEffectiveConfig_EncryptionTargetAndRemoteFieldMerges(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tofu", `terraform {
  encryption {
    state {
      method = vars.base_method
      fallback { method = vars.base_fallback }
    }
    remote_state_data_sources {
      default { method = vars.default_method }
      remote_state_data_source "archive" { method = vars.archive_method }
    }
  }
}`)
	override := parseEffectiveTestFile(t, "override.tofu", `terraform {
  encryption {
    state {
      enforced = true
    }
    remote_state_data_sources {
      default {
        fallback {
          method = vars.default_fallback
        }
      }
      remote_state_data_source "archive" {
        fallback {
          method = vars.archive_fallback
        }
      }
      remote_state_data_source "new" {
        method = vars.new_method
      }
    }
  }
}`)
	encryption := effectiveEncryptionBlock(t, base, override)
	state := findChildBlock(t, encryption.Body, "state")
	if got := state.Body.Attributes["method"].NameRange.Filename; got != "main.tofu" {
		t.Fatalf("state method file = %q", got)
	}
	if got := findChildBlock(t, state.Body, "fallback").Body.Attributes["method"].NameRange.Filename; got != "main.tofu" {
		t.Fatalf("state fallback file = %q", got)
	}
	remote := findChildBlock(t, encryption.Body, "remote_state_data_sources")
	defaultTarget := findChildBlock(t, remote.Body, "default")
	if got := defaultTarget.Body.Attributes["method"].NameRange.Filename; got != "main.tofu" {
		t.Fatalf("default method file = %q", got)
	}
	if got := findChildBlock(t, defaultTarget.Body, "fallback").Body.Attributes["method"].NameRange.Filename; got != "override.tofu" {
		t.Fatalf("default fallback file = %q", got)
	}
	archive := findChildBlock(t, remote.Body, "remote_state_data_source", "archive")
	if got := archive.Body.Attributes["method"].NameRange.Filename; got != "main.tofu" {
		t.Fatalf("archive method file = %q", got)
	}
	if got := findChildBlock(t, archive.Body, "fallback").Body.Attributes["method"].NameRange.Filename; got != "override.tofu" {
		t.Fatalf("archive fallback file = %q", got)
	}
	newTarget := findChildBlock(t, remote.Body, "remote_state_data_source", "new")
	if got := newTarget.Body.Attributes["method"].NameRange.Filename; got != "override.tofu" {
		t.Fatalf("new target file = %q", got)
	}
}

func effectiveEncryptionBlock(t *testing.T, files ...ParsedFile) *hclsyntax.Block {
	t.Helper()
	effective := buildEffectiveConfig(files)
	for _, file := range effective.files {
		for _, block := range file.Body.Blocks {
			if block.Type != "terraform" {
				continue
			}
			for _, nested := range block.Body.Blocks {
				if nested.Type == "encryption" {
					return nested
				}
			}
		}
	}
	t.Fatal("effective encryption block not found")
	return nil
}

func findChildBlock(t *testing.T, body *hclsyntax.Body, blockType string, labels ...string) *hclsyntax.Block {
	t.Helper()
	for _, block := range body.Blocks {
		if block.Type == blockType && slices.Equal(block.Labels, labels) {
			return block
		}
	}
	t.Fatalf("block %s %v not found", blockType, labels)
	return nil
}
