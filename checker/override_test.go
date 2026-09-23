// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/mchv/tfdry/checker"
)

func TestOverrideLocalsReplaceWithoutDuplicate(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { value = { base = true } }
output "x" { value = "prefix-${local.value}" }`,
		"override.tf": `locals { value = "override" }`,
	})
	if hasCode(vs, "E002") || hasCode(vs, "E004") {
		t.Fatalf("valid local override produced diagnostics: %v", codes(vs))
	}
}

func TestOverrideLocalsApplyLexicographically(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { value = "base" }
output "x" { value = "prefix-${local.value}" }`,
		"a_override.tf": `locals { value = "a" }`,
		"z_override.tf": `locals { value = { final = true } }`,
	})
	if hasCode(vs, "E002") {
		t.Fatalf("ordered local overrides produced E002: %v", codes(vs))
	}
	for _, v := range vs {
		if v.Code == "E004" {
			if v.File != "main.tf" {
				t.Fatalf("E004 consumer file = %q, want main.tf", v.File)
			}
			return
		}
	}
	t.Fatalf("later z_override.tf value did not win: %v", codes(vs))
}

func TestOverrideLocalsApplySourceOrderWithinFile(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { value = { base = true } }
output "x" { value = "prefix-${local.value}" }`,
		"override.tf": `locals { value = { first = true } }
locals { value = "second" }`,
	})
	if hasCode(vs, "E002") || hasCode(vs, "E004") {
		t.Fatalf("later local override in same file did not win: %v", codes(vs))
	}
}

func TestOverrideLocalsOpenTofuPeerTakesPrecedence(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `locals { value = { base = true } }
output "x" { value = "prefix-${local.value}" }`,
		"settings_override.tf":   `locals { value = { terraform = true } }`,
		"settings_override.tofu": `locals { value = "opentofu" }`,
	})
	if hasCode(vs, "E002") || hasCode(vs, "E004") {
		t.Fatalf("authoritative .tofu override did not win: %v", codes(vs))
	}
}

func TestOverrideOnlyLocalDoesNotCreateBaseDefinition(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf":     `output "x" { value = local.missing }`,
		"override.tf": `locals { missing = "invalid override-only value" }`,
	})
	if !hasCode(vs, "E003") {
		t.Fatalf("override-only local incorrectly became effective: %v", codes(vs))
	}
}

func TestPrimaryDuplicateLocalStillViolates(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"a.tf": `locals { value = "a" }`,
		"b.tf": `locals { value = "b" }`,
	})
	if !hasCode(vs, "E002") {
		t.Fatalf("primary duplicate local lost E002: %v", codes(vs))
	}
}

func TestNonOverrideSuffixRemainsPrimary(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf":           `locals { value = "base" }`,
		"override_extra.tf": `locals { value = "ordinary duplicate" }`,
	})
	if !hasCode(vs, "E002") {
		t.Fatalf("ordinary file was misclassified as override: %v", codes(vs))
	}
}

func TestOverrideWinningLocalRetainsSourceFile(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu":     `locals { value = "base" }`,
		"override.tofu": `locals { value = "override" }`,
	})
	for _, v := range vs {
		if v.Code == "W001" {
			if v.File != "override.tofu" {
				t.Fatalf("W001 file = %q, want override.tofu", v.File)
			}
			return
		}
	}
	t.Fatalf("expected W001 for effective override local, got %v", codes(vs))
}

func TestOverrideReplacedExpressionIsNotChecked(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_s3_bucket" "example" {
  bucket = vars.bad
}`,
		"override.tf": `resource "aws_s3_bucket" "example" {
  bucket = "valid-name"
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("replaced base expression was still checked: %v", codes(vs))
	}
}

func TestOverrideWinningExpressionRetainsSourceFile(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_s3_bucket" "example" {
  bucket = "valid-name"
}`,
		"override.tf": `resource "aws_s3_bucket" "example" {
  bucket = vars.bad
}`,
	})
	for _, v := range vs {
		if v.Code == "E009" {
			if v.File != "override.tf" {
				t.Fatalf("E009 file = %q, want override.tf", v.File)
			}
			return
		}
	}
	t.Fatalf("winning invalid override expression was not checked: %v", codes(vs))
}

func TestOverrideNestedBlocksReplaceSameType(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "example" "x" {
  setting {
    value = vars.bad
  }
}`,
		"override.tf": `resource "example" "x" {
  setting {
    value = "effective"
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("replaced nested block expression was still checked: %v", codes(vs))
	}
}

func TestOverrideLiteralBlockReplacesDynamicPeer(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "example" "x" {
  dynamic "setting" {
    for_each = vars.bad
    content {
      value = setting.value
    }
  }
}`,
		"override.tf": `resource "example" "x" {
  setting {
    value = "effective"
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("literal override did not replace dynamic peer: %v", codes(vs))
	}
}

func TestOverrideCanAssembleCountForEachConflict(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `module "child" {
  source = "./child"
  count  = 1
}`,
		"override.tf": `module "child" {
  for_each = toset(["x"])
}`,
	})
	for _, v := range vs {
		if v.Code == "E005" {
			if v.File != "override.tf" {
				t.Fatalf("E005 file = %q, want override.tf", v.File)
			}
			return
		}
	}
	t.Fatalf("merged count/for_each conflict not detected: %v", codes(vs))
}

func TestOverrideModuleInputUsesWinningValue(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "child" {
  source = "./child"
  value  = 42
}`,
			"override.tf": `module "child" {
  value = "not a number"
}`,
		},
		"child",
		map[string]string{
			"variables.tf": `variable "value" { type = number }`,
		},
	)
	vs := runDir(t, dir)
	for _, v := range vs {
		if v.Code == "E006" {
			if v.File != "override.tf" {
				t.Fatalf("E006 file = %q, want override.tf", v.File)
			}
			return
		}
	}
	t.Fatalf("winning module input was not type-checked: %v", codes(vs))
}

func TestOverrideChildVariableSchemaUsesWinningType(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "child" {
  source = "./child"
  value  = "string caller"
}`,
		},
		"child",
		map[string]string{
			"variables.tf": `variable "value" { type = string }`,
			"override.tf":  `variable "value" { type = number }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("child variable override type was ignored: %v", codes(vs))
	}
}

func TestOverrideChildVariableRetainsOmittedType(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "child" {
  source = "./child"
  value  = "not an object"
}`,
		},
		"child",
		map[string]string{
			"variables.tf":  `variable "value" { type = object({ name = string }) }`,
			"z_override.tf": `variable "value" { default = { name = "default" } }`,
		},
	)
	vs := runDir(t, dir)
	if !hasCode(vs, "E006") {
		t.Fatalf("omitted override type did not retain primary schema: %v", codes(vs))
	}
}

func TestOverrideWinningAWSAttributeOnlyIsValidated(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_vpc" "example" {
  cidr_block = "999.0.0.0/16"
}`,
		"override.tf": `resource "aws_vpc" "example" {
  cidr_block = "10.0.0.0/16"
}`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("replaced base CIDR was still validated: %v", codes(vs))
	}
}

func TestOverrideLifecycleMergesArgumentsInsteadOfReplacingBlock(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_instance" "example" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [vars.bad]
    }
  }
}`,
		"override.tf": `resource "aws_instance" "example" {
  lifecycle {
    prevent_destroy = true
  }
}`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("base lifecycle fields were incorrectly discarded: %v", codes(vs))
	}
}

func TestOverrideRequiredProvidersRetainsOtherProviders(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [mystery.bad]
    }
  }
}`,
		"override.tf": `terraform {
  required_providers {
    random = {
      source = "hashicorp/random"
    }
  }
}`,
	})
	if !hasCode(vs, "W009") {
		t.Fatalf("base required provider entry was incorrectly discarded: %v", codes(vs))
	}
}

func TestOverrideCloudReplacesBackendSelection(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `terraform {
  backend "local" {
    path = vars.bad
  }
}`,
		"override.tf": `terraform {
  cloud {
    organi` + `zation = "example"
    workspaces { name = "workspace" }
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("replaced backend expression was still checked: %v", codes(vs))
	}
}

func TestOverrideProviderAliasMatchesSameConfiguration(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `provider "aws" {
  alias  = "west"
  region = "not-a-region"
}`,
		"override.tf": `provider "aws" {
  alias  = "west"
  region = "eu-west-1"
}`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("aliased provider override did not replace region: %v", codes(vs))
	}
}

func TestUnmatchedNamedOverrideBlockIsIgnored(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `locals { value = "base" }`,
		"override.tf": `resource "aws_s3_bucket" "missing" {
  bucket = vars.bad
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("unmatched named override became effective: %v", codes(vs))
	}
}

func TestOverrideWinningAWSAttributeRetainsSourceFile(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `provider "aws" {
  region = "eu-west-1"
}`,
		"override.tf": `provider "aws" {
  region = "not-a-region"
}`,
	})
	for _, v := range vs {
		if v.Code == "E201" {
			if v.File != "override.tf" {
				t.Fatalf("E201 file = %q, want override.tf", v.File)
			}
			return
		}
	}
	t.Fatalf("winning invalid region was not checked: %v", codes(vs))
}

func TestOverrideFormattingUsesPhysicalFiles(t *testing.T) {
	t.Parallel()
	dir := writeTFDir(t, map[string]string{
		"main.tf":                   "locals { value = \"base\" }\n",
		"override.tf":               "locals {value=\"override\"}\n",
		"settings_override.tf":      "locals {value=\"terraform\"}\n",
		"settings_override.tofu":    "locals {value=\"opentofu\"}\n",
		"not_configuration.txt":     "ignored",
		"not_configuration.tf.json": `{}`,
	})
	files, parseViolations, err := checker.ParseDirForFormat(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(parseViolations) != 0 {
		t.Fatalf("parse violations: %+v", parseViolations)
	}
	got := make([]string, len(files))
	for i, file := range files {
		got[i] = file.Name
	}
	sort.Strings(got)
	want := []string{"main.tf", "override.tf", "settings_override.tf", "settings_override.tofu"}
	if !slices.Equal(got, want) {
		t.Fatalf("format files = %v, want %v", got, want)
	}
	violations, err := checker.CheckFormat(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"override.tf", "settings_override.tf", "settings_override.tofu"} {
		found := false
		for _, violation := range violations {
			if violation.Code == "E008" && violation.File == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dirty physical override %s missing E008: %+v", name, violations)
		}
	}
}

func TestOverrideRunE008UsesPhysicalSource(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf":     "locals {\n  value = \"base\"\n}\n",
		"override.tf": "locals {value=\"override\"}\n",
	})
	for _, v := range vs {
		if v.Code == "E008" && v.File == "override.tf" {
			return
		}
	}
	t.Fatalf("physical override source missing E008: %+v", vs)
}

func TestOverrideAcrossMultiplePrimaryTerraformBlocks(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"a.tf": `terraform { required_version = ">= 1.0" }`,
		"b.tf": `terraform {
  backend "local" {
    path = vars.bad
  }
}`,
		"override.tf": `terraform {
  cloud {
    organi` + `zation = "example"
    workspaces { name = "workspace" }
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("override missed settings from a later primary terraform block: %v", codes(vs))
	}
}

func TestForbiddenLifecycleConditionOverrideDoesNotHideBase(t *testing.T) {
	t.Parallel()
	for _, conditionType := range []string{"precondition", "postcondition"} {
		conditionType := conditionType
		t.Run(conditionType, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{
				"main.tf": `resource "example" "x" {
  lifecycle {
    ` + conditionType + ` {
      condition     = true
      error_message = vars.bad
    }
  }
}`,
				"override.tf": `resource "example" "x" {
  lifecycle {
    ` + conditionType + ` {
      condition     = true
      error_message = "invalid override construct"
    }
  }
}`,
			})
			if !hasCode(vs, "E009") {
				t.Fatalf("forbidden lifecycle override hid base %s: %v", conditionType, codes(vs))
			}
		})
	}
}

func TestForbiddenDependsOnOverrideDoesNotHideBase(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "example" "x" {
  depends_on = [vars.bad]
}`,
		"override.tf": `resource "example" "x" {
  depends_on = [aws_instance.example]
}`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("forbidden depends_on override hid base expression: %v", codes(vs))
	}
}

func TestOverrideWinningDiagnosticsUseOverrideSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code string
		base string
		over string
	}{
		{
			name: "W009",
			code: "W009",
			base: `resource "example" "x" { value = "base" }`,
			over: `resource "example" "x" { value = mystery.value }`,
		},
		{
			name: "E101",
			code: "E101",
			base: `resource "aws_vpc" "x" { cidr_block = "10.0.0.0/16" }`,
			over: `resource "aws_vpc" "x" { cidr_block = "999.0.0.0/16" }`,
		},
		{
			name: "E202",
			code: "E202",
			base: `resource "aws_iam_role" "x" { account_id = "123456789012" }`,
			over: `resource "aws_iam_role" "x" { account_id = "invalid" }`,
		},
		{
			name: "E203",
			code: "E203",
			base: `resource "aws_iam_role" "x" { role_arn = "arn:aws:iam::123456789012:role/ok" }`,
			over: `resource "aws_iam_role" "x" { role_arn = "not-an-arn" }`,
		},
		{
			name: "E204",
			code: "E204",
			base: `resource "aws_s3_bucket" "x" { bucket = "valid-bucket" }`,
			over: `resource "aws_s3_bucket" "x" { bucket = "Invalid_Bucket" }`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{"main.tf": tc.base, "override.tf": tc.over})
			count := 0
			for _, violation := range vs {
				if violation.Code != tc.code {
					continue
				}
				count++
				if violation.File != "override.tf" {
					t.Fatalf("%s file = %q, want override.tf", tc.code, violation.File)
				}
			}
			if count != 1 {
				t.Fatalf("%s count = %d, want 1; codes=%v", tc.code, count, codes(vs))
			}
		})
	}
}

func TestOverrideWinningUnknownModuleInputUsesOverrideSource(t *testing.T) {
	t.Parallel()
	dir := writeModuleFiles(
		t,
		map[string]string{
			"main.tf": `module "child" {
  source = "./child"
  known  = "ok"
}`,
			"override.tf": `module "child" {
  unknown = "bad"
}`,
		},
		"child",
		map[string]string{
			"variables.tf": `variable "known" { type = string }`,
		},
	)
	vs := runDir(t, dir)
	for _, violation := range vs {
		if violation.Code == "E007" {
			if violation.File != "override.tf" {
				t.Fatalf("E007 file = %q, want override.tf", violation.File)
			}
			return
		}
	}
	t.Fatalf("winning unknown module input missing E007: %v", codes(vs))
}

func TestOverrideWinningBlockTypoUsesOverrideSource(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_quicksight_data_set" "x" {
  permissions {}
}`,
		"override.tf": `resource "aws_quicksight_data_set" "x" {
  permission {}
}`,
	})
	for _, violation := range vs {
		if violation.Code == "E210" {
			if violation.File != "override.tf" {
				t.Fatalf("E210 file = %q, want override.tf", violation.File)
			}
			return
		}
	}
	t.Fatalf("winning block typo missing E210: %v", codes(vs))
}

func TestOverrideLifecycleActionTriggerReplacesPriorBlocks(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `resource "aws_instance" "example" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [vars.bad]
    }
  }
}`,
		"override.tf": `resource "aws_instance" "example" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.example.ok]
    }
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("override action_trigger did not replace prior blocks: %v", codes(vs))
	}
}

func TestOverrideRequiredProvidersReplacesSameProviderEntry(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [mystery.bad]
    }
  }
}`,
		"override.tf": `terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.0"
    }
  }
}`,
	})
	if hasCode(vs, "W009") {
		t.Fatalf("same-provider override entry did not replace prior value: %v", codes(vs))
	}
}

func TestOverrideStateStoreReplacesBackendSelection(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `terraform {
  backend "local" {
    path = vars.bad
  }
}`,
		"override.tofu": `terraform {
  state_store "example" {
    value = "effective"
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("state_store did not replace backend selection: %v", codes(vs))
	}
}

func TestForbiddenTopLevelOverrideBlocksDoNotReplacePrimary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		over string
	}{
		{
			name: "moved",
			base: `moved {
  from = vars.bad
  to   = aws_instance.example
}`,
			over: `moved {
  from = aws_instance.old
  to   = aws_instance.example
}`,
		},
		{
			name: "import",
			base: `import {
  to = aws_instance.example
  id = vars.bad
}`,
			over: `import {
  to = aws_instance.example
  id = "i-123"
}`,
		},
		{
			name: "removed",
			base: `removed {
  from = vars.bad
}`,
			over: `removed {
  from = aws_instance.example
}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{"main.tf": tc.base, "override.tf": tc.over})
			if !hasCode(vs, "E009") {
				t.Fatalf("forbidden %s override hid primary expression: %v", tc.name, codes(vs))
			}
		})
	}
}

func TestForbiddenCheckRuleOverridesDoNotHidePrimary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		over string
	}{
		{
			name: "top-level check",
			base: `check "example" {
  assert {
    condition     = true
    error_message = vars.bad
  }
}`,
			over: `check "example" {
  assert {
    condition     = true
    error_message = "invalid override construct"
  }
}`,
		},
		{
			name: "variable validation",
			base: `variable "value" {
  type = string
  validation {
    condition     = true
    error_message = vars.bad
  }
}`,
			over: `variable "value" {
  validation {
    condition     = true
    error_message = "invalid override construct"
  }
}`,
		},
		{
			name: "output precondition",
			base: `output "value" {
  value = "base"
  precondition {
    condition     = true
    error_message = vars.bad
  }
}`,
			over: `output "value" {
  precondition {
    condition     = true
    error_message = "invalid override construct"
  }
}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{"main.tf": tc.base, "override.tf": tc.over})
			if !hasCode(vs, "E009") {
				t.Fatalf("forbidden %s override hid primary expression: %v", tc.name, codes(vs))
			}
		})
	}
}

func TestForbiddenEphemeralDependsOnOverrideDoesNotHidePrimary(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `ephemeral "example" "x" {
  depends_on = [vars.bad]
}`,
		"override.tofu": `ephemeral "example" "x" {
  depends_on = [ephemeral.example.other]
}`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("forbidden ephemeral depends_on override hid primary expression: %v", codes(vs))
	}
}

func TestForbiddenProviderMetaOverrideDoesNotHidePrimary(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tf": `terraform {
  provider_meta "aws" {
    value = vars.bad
  }
}`,
		"override.tf": `terraform {
  provider_meta "aws" {
    value = "invalid override construct"
  }
}`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("forbidden provider_meta override hid primary expression: %v", codes(vs))
	}
}

func TestInertOverridePreservesAllPrimaryTerraformBlocks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
	}{
		{
			name: "provider_meta across files",
			files: map[string]string{
				"a.tf": `terraform {
  provider_meta "aws" { value = "ok" }
}`,
				"b.tf": `terraform {
  provider_meta "google" { value = vars.bad }
}`,
				"override.tf": `terraform {}`,
			},
		},
		{
			name: "repeated required_version expressions",
			files: map[string]string{
				"a.tf":        `terraform { required_version = vars.bad }`,
				"b.tf":        `terraform { required_version = ">= 1.0" }`,
				"override.tf": `terraform {}`,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vs := run(t, tc.files)
			if !hasCode(vs, "E009") {
				t.Fatalf("inert override hid primary terraform expression: %v", codes(vs))
			}
		})
	}
}

func TestOverrideEncryptionRetainsOmittedDefinitions(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `locals {
  passphrase = "correct-horse-battery-staple"
}
terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      passphrase = local.passphrase
    }
    method "aes_gcm" "main" {
      keys = key_provider.pbkdf2.main
    }
    state {
      method = method.aes_gcm.main
    }
  }
}`,
		"override.tofu": `terraform {
  encryption {
    state {
      enforced = true
    }
  }
}`,
	})
	if hasCode(vs, "W001") || hasCode(vs, "W009") {
		t.Fatalf("encryption override discarded retained definitions: %v", codes(vs))
	}
}

func TestOverrideEncryptionRetainsBaseDiagnostics(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      passphrase = vars.bad
    }
    method "aes_gcm" "main" {
      keys = key_provider.pbkdf2.main
    }
    state {
      method = method.aes_gcm.main
    }
  }
}`,
		"override.tofu": `terraform {
  encryption {
    state {
      enforced = true
    }
  }
}`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("retained key provider diagnostic disappeared: %v", codes(vs))
	}
}

func TestOverrideEncryptionMergesNamedKeyProvider(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      passphrase = vars.bad
    }
  }
}`,
		"override.tofu": `terraform {
  encryption {
    key_provider "pbkdf2" "main" {
      passphrase = "replacement"
    }
  }
}`,
	})
	if hasCode(vs, "E009") {
		t.Fatalf("named key provider override did not replace supplied field: %v", codes(vs))
	}
}

func TestOverrideEncryptionRetainsRemoteTargets(t *testing.T) {
	t.Parallel()
	vs := run(t, map[string]string{
		"main.tofu": `terraform {
  encryption {
    remote_state_data_sources {
      default {
        method = vars.bad
      }
      remote_state_data_source "archive" {
        method = vars.other
      }
    }
  }
}`,
		"override.tofu": `terraform {
  encryption {
    remote_state_data_sources {
      remote_state_data_source "new" {
        method = method.aes_gcm.main
      }
    }
  }
}`,
	})
	count := 0
	for _, violation := range vs {
		if violation.Code == "E009" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("retained remote target E009 count = %d, want 2; codes=%v", count, codes(vs))
	}
}
