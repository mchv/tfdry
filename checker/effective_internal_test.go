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
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
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
		name      string
		base      string
		override  string
		wantFile  string
		wantValue bool
	}{
		{name: "parenthesized true retained", base: "(true)", override: "false", wantFile: "main.tofu", wantValue: true},
		{name: "unary true retained", base: "!false", override: "false", wantFile: "main.tofu", wantValue: true},
		{name: "string true retained", base: `"true"`, override: "false", wantFile: "main.tofu", wantValue: true},
		{name: "override true wins", base: "false", override: "!false", wantFile: "override.tofu", wantValue: true},
		{name: "string false overridden", base: `"false"`, override: "true", wantFile: "override.tofu", wantValue: true},
		{name: "invalid string ignored", base: `"yes"`, override: "false", wantFile: "override.tofu", wantValue: false},
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
			enforced := state.Body.Attributes["enforced"]
			if got := enforced.NameRange.Filename; got != tc.wantFile {
				t.Fatalf("enforced file = %q, want %q", got, tc.wantFile)
			}
			value, diags := enforced.Expr.Value(nil)
			if diags.HasErrors() || !value.IsKnown() || value.IsNull() {
				t.Fatalf("evaluate enforced: %s", diags.Error())
			}
			value, err := convert.Convert(value, cty.Bool)
			if err != nil {
				t.Fatalf("convert enforced to bool: %v", err)
			}
			if got := value.True(); got != tc.wantValue {
				t.Fatalf("enforced value = %v, want %v", got, tc.wantValue)
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

func TestBuildEffectiveConfig_ClonesOnlyTouchedFileBodies(t *testing.T) {
	t.Parallel()
	untouched := parseEffectiveTestFile(t, "a.tf", `resource "example" "untouched" { value = "base" }`)
	touched := parseEffectiveTestFile(t, "b.tf", `resource "example" "touched" { value = "base" }`)
	override := parseEffectiveTestFile(t, "override.tf", `resource "example" "touched" { value = "override" }`)

	effective := buildEffectiveConfig([]ParsedFile{untouched, touched, override})
	var effectiveUntouched, effectiveTouched *ParsedFile
	for i := range effective.files {
		switch effective.files[i].Name {
		case "a.tf":
			effectiveUntouched = &effective.files[i]
		case "b.tf":
			effectiveTouched = &effective.files[i]
		}
	}
	if effectiveUntouched == nil || effectiveTouched == nil {
		t.Fatalf("effective files missing: %+v", effective.files)
	}
	if effectiveUntouched.Body != untouched.Body {
		t.Fatal("untouched primary file body was cloned")
	}
	if effectiveTouched.Body == touched.Body {
		t.Fatal("touched primary file body was not cloned")
	}
	winning := effectiveTouched.Body.Blocks[0].Body.Attributes["value"]
	if got := stringLiteralValue(winning.Expr); got != "override" {
		t.Fatalf("effective touched value = %q, want override", got)
	}
	original := touched.Body.Blocks[0].Body.Attributes["value"]
	if got := stringLiteralValue(original.Expr); got != "base" {
		t.Fatalf("original touched file mutated: %q", got)
	}
}

func TestBuildEffectiveConfig_TerraformOverridesCloneOnlyAffectedBodies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		primaryA   string
		primaryB   string
		override   string
		wantAClone bool
		wantBClone bool
	}{
		{
			name:     "required providers touches containing block only",
			primaryA: `terraform { required_version = ">= 1.0" }`,
			primaryB: `terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}`,
			override: `terraform {
  required_providers {
    random = { source = "hashicorp/random" }
  }
}`,
			wantBClone: true,
		},
		{
			name:     "encryption touches containing block only",
			primaryA: `terraform { required_version = ">= 1.0" }`,
			primaryB: `terraform {
  encryption {
    state { enforced = false }
  }
}`,
			override: `terraform {
  encryption {
    state { enforced = true }
  }
}`,
			wantBClone: true,
		},
		{
			name:     "provider meta override touches nothing",
			primaryA: `terraform { required_version = ">= 1.0" }`,
			primaryB: `terraform {
  provider_meta "aws" { value = "base" }
}`,
			override: `terraform {
  provider_meta "aws" { value = "ignored" }
}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := parseEffectiveTestFile(t, "a.tf", tc.primaryA)
			b := parseEffectiveTestFile(t, "b.tf", tc.primaryB)
			override := parseEffectiveTestFile(t, "override.tf", tc.override)
			effective := buildEffectiveConfig([]ParsedFile{a, b, override})
			var effectiveA, effectiveB *ParsedFile
			for i := range effective.files {
				switch effective.files[i].Name {
				case "a.tf":
					effectiveA = &effective.files[i]
				case "b.tf":
					effectiveB = &effective.files[i]
				}
			}
			if effectiveA == nil || effectiveB == nil {
				t.Fatalf("effective files missing: %+v", effective.files)
			}
			if got := effectiveA.Body != a.Body; got != tc.wantAClone {
				t.Fatalf("a.tf cloned = %v, want %v", got, tc.wantAClone)
			}
			if got := effectiveB.Body != b.Body; got != tc.wantBClone {
				t.Fatalf("b.tf cloned = %v, want %v", got, tc.wantBClone)
			}
		})
	}
}

func TestBuildEffectiveConfig_OverrideOnlyTerraformFiltersAndCompounds(t *testing.T) {
	t.Parallel()
	primary := parseEffectiveTestFile(t, "main.tf", `locals { value = "base" }`)
	first := parseEffectiveTestFile(t, "a_override.tf", `terraform {
  required_version = ">= 1.0"
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
  backend "local" { path = "state.tfstate" }
  provider_meta "aws" { value = vars.bad }
  encryption {
    key_provider "pbkdf2" "main" { passphrase = "secret" }
  }
}`)
	later := parseEffectiveTestFile(t, "z_override.tf", `terraform {
  required_version = ">= 2.0"
  required_providers {
    random = { source = "hashicorp/random" }
  }
  cloud {}
  encryption {
    state { enforced = true }
  }
}`)
	firstProviderMeta := findChildBlock(t, first.Body.Blocks[0].Body, "provider_meta", "aws")
	firstBackend := findChildBlock(t, first.Body.Blocks[0].Body, "backend", "local")

	effective := buildEffectiveConfig([]ParsedFile{primary, first, later})
	if len(effective.files) != 2 {
		t.Fatalf("effective files = %d, want primary plus synthetic override", len(effective.files))
	}
	if effective.files[0].Body != primary.Body {
		t.Fatal("unrelated primary body was cloned")
	}
	var synthetic *ParsedFile
	for i := range effective.files {
		if effective.files[i].Name == "a_override.tf" {
			synthetic = &effective.files[i]
		}
	}
	if synthetic == nil {
		t.Fatalf("synthetic override file missing: %+v", effective.files)
	}
	terraformBlock := findChildBlock(t, synthetic.Body, "terraform")
	if got := stringLiteralValue(terraformBlock.Body.Attributes["required_version"].Expr); got != ">= 2.0" {
		t.Fatalf("required_version = %q, want later override", got)
	}
	if got := terraformBlock.Body.Attributes["required_version"].NameRange.Filename; got != "z_override.tf" {
		t.Fatalf("required_version file = %q", got)
	}
	for _, block := range terraformBlock.Body.Blocks {
		if block.Type == "provider_meta" || block.Type == "backend" {
			t.Fatalf("forbidden/replaced block remained effective: %s", block.Type)
		}
	}
	required := findChildBlock(t, terraformBlock.Body, "required_providers")
	for _, name := range []string{"aws", "random"} {
		if _, ok := required.Body.Attributes[name]; !ok {
			t.Fatalf("required provider %q missing", name)
		}
	}
	if got := required.Body.Attributes["random"].NameRange.Filename; got != "z_override.tf" {
		t.Fatalf("random provider file = %q", got)
	}
	cloud := findChildBlock(t, terraformBlock.Body, "cloud")
	if cloud.TypeRange.Filename != "z_override.tf" {
		t.Fatalf("cloud file = %q", cloud.TypeRange.Filename)
	}
	encryption := findChildBlock(t, terraformBlock.Body, "encryption")
	_ = findChildBlock(t, encryption.Body, "key_provider", "pbkdf2", "main")
	state := findChildBlock(t, encryption.Body, "state")
	if got := state.Body.Attributes["enforced"].NameRange.Filename; got != "z_override.tf" {
		t.Fatalf("state enforced file = %q", got)
	}
	if findChildBlock(t, first.Body.Blocks[0].Body, "provider_meta", "aws") != firstProviderMeta ||
		findChildBlock(t, first.Body.Blocks[0].Body, "backend", "local") != firstBackend {
		t.Fatal("original override AST was mutated")
	}
}

func TestBuildEffectiveConfig_ManagedLifecycleAndActionConfigProvenance(t *testing.T) {
	t.Parallel()
	base := parseEffectiveTestFile(t, "main.tf", `resource "example" "x" {
  lifecycle {
    create_before_destroy = false
    ignore_changes        = [tags]
    replace_triggered_by  = [aws_instance.base]
  }
}
action "example" "run" {
  config {
    retained = "base"
    replaced = "base"
  }
}`)
	override := parseEffectiveTestFile(t, "override.tf", `resource "example" "x" {
  lifecycle {
    create_before_destroy = true
    ignore_changes        = []
    replace_triggered_by  = [aws_instance.override]
  }
}
action "example" "run" {
  config {
    replaced = "override"
  }
}`)
	baseLifecycle := findChildBlock(t, base.Body.Blocks[0].Body, "lifecycle")
	baseActionConfig := findChildBlock(t, base.Body.Blocks[1].Body, "config")

	effective := buildEffectiveConfig([]ParsedFile{base, override})
	resource := findChildBlock(t, effective.files[0].Body, "resource", "example", "x")
	lifecycle := findChildBlock(t, resource.Body, "lifecycle")
	if got := lifecycle.Body.Attributes["create_before_destroy"].NameRange.Filename; got != "override.tf" {
		t.Fatalf("create_before_destroy file = %q", got)
	}
	if got := lifecycle.Body.Attributes["ignore_changes"].NameRange.Filename; got != "main.tf" {
		t.Fatalf("empty ignore_changes incorrectly cleared primary; file=%q", got)
	}
	if got := lifecycle.Body.Attributes["replace_triggered_by"].NameRange.Filename; got != "main.tf" {
		t.Fatalf("replace_triggered_by incorrectly overridden; file=%q", got)
	}
	action := findChildBlock(t, effective.files[0].Body, "action", "example", "run")
	config := findChildBlock(t, action.Body, "config")
	if got := config.Body.Attributes["retained"].NameRange.Filename; got != "main.tf" {
		t.Fatalf("retained action config file = %q", got)
	}
	if got := config.Body.Attributes["replaced"].NameRange.Filename; got != "override.tf" {
		t.Fatalf("replaced action config file = %q", got)
	}
	if findChildBlock(t, base.Body.Blocks[0].Body, "lifecycle") != baseLifecycle ||
		findChildBlock(t, base.Body.Blocks[1].Body, "config") != baseActionConfig {
		t.Fatal("primary lifecycle/action AST was mutated")
	}
}

func TestBuildEffectiveConfig_DataEphemeralLifecycleRetainsPrimaryNode(t *testing.T) {
	t.Parallel()
	for _, blockType := range []string{"data", "ephemeral"} {
		blockType := blockType
		t.Run(blockType, func(t *testing.T) {
			t.Parallel()
			base := parseEffectiveTestFile(t, "main.tf", blockType+` "example" "x" {
  lifecycle {
    precondition {
      condition     = true
      error_message = "base"
    }
  }
}`)
			override := parseEffectiveTestFile(t, "override.tf", blockType+` "example" "x" {
  lifecycle {
    precondition {
      condition     = true
      error_message = "override"
    }
  }
}`)
			baseLifecycle := findChildBlock(t, base.Body.Blocks[0].Body, "lifecycle")
			effective := buildEffectiveConfig([]ParsedFile{base, override})
			block := findChildBlock(t, effective.files[0].Body, blockType, "example", "x")
			if got := findChildBlock(t, block.Body, "lifecycle"); got != baseLifecycle {
				t.Fatalf("%s lifecycle pointer changed", blockType)
			}
			if got := findChildBlock(t, override.Body.Blocks[0].Body, "lifecycle").Body.Blocks[0].Body.Attributes["error_message"].NameRange.Filename; got != "override.tf" {
				t.Fatalf("override AST mutated; file=%q", got)
			}
		})
	}
}

func TestBuildEffectiveConfig_ManagedLifecycleIgnoreChangesMerge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		base     string
		override string
		wantFile string
	}{
		{name: "all remains sticky over list", base: "all", override: "[tags]", wantFile: "main.tf"},
		{name: "list replaced by all", base: "[tags]", override: "all", wantFile: "override.tf"},
		{name: "empty list does not clear", base: "[tags]", override: "[]", wantFile: "main.tf"},
		{name: "nonempty list replaces list", base: "[tags]", override: "[name]", wantFile: "override.tf"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := parseEffectiveTestFile(t, "main.tf", `resource "example" "x" {
  lifecycle { ignore_changes = `+tc.base+` }
}`)
			override := parseEffectiveTestFile(t, "override.tf", `resource "example" "x" {
  lifecycle { ignore_changes = `+tc.override+` }
}`)
			effective := buildEffectiveConfig([]ParsedFile{base, override})
			resource := findChildBlock(t, effective.files[0].Body, "resource", "example", "x")
			lifecycle := findChildBlock(t, resource.Body, "lifecycle")
			if got := lifecycle.Body.Attributes["ignore_changes"].NameRange.Filename; got != tc.wantFile {
				t.Fatalf("ignore_changes file = %q, want %q", got, tc.wantFile)
			}
		})
	}
}
