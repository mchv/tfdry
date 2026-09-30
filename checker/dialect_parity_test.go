// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mchv/tfdry/checker"
)

type dialectParityMode string

const (
	dialectParityParse  dialectParityMode = "parse"
	dialectParityFormat dialectParityMode = "format"
	dialectParityRun    dialectParityMode = "run"
)

type dialectParityCase struct {
	mode      dialectParityMode
	wantCount int
	files     map[string]string
}

var dialectParityCases = map[string]dialectParityCase{
	"E001": {
		mode:      dialectParityParse,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `resource "aws_s3_bucket" "example" { broken syntax !!!`,
		},
	},
	"E002": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"a.tf": `locals { name = "first" }`,
			"b.tf": `locals { name = "second" }`,
		},
	},
	"E003": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `output "value" { value = local.missing }`,
		},
	},
	"E004": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `
locals { tags = { environment = "production" } }
output "value" { value = "prefix-${local.tags}" }
`,
		},
	},
	"E005": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `
resource "aws_instance" "example" {
  count    = 1
  for_each = toset(["example"])
}
`,
		},
	},
	"E006": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `
locals { names = ["one", "two"] }
module "child" {
  source = "./modules/child"
  name   = local.names
}
`,
			"modules/child/variables.tf": `variable "name" { type = string }`,
		},
	},
	"E007": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `
module "child" {
  source = "./modules/child"
  typo   = "value"
}
`,
			"modules/child/variables.tf": `variable "name" { type = string }`,
		},
	},
	"E008": {
		mode:      dialectParityFormat,
		wantCount: 1,
		files: map[string]string{
			"main.tf": "locals{value=\"unformatted\"}\n",
		},
	},
	"E009": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `output "value" { value = vars.name }`,
		},
	},
	"W001": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `locals { unused = "value" }`,
		},
	},
	"W009": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `output "value" { value = mynewthing.environment }`,
		},
	},
	"E101": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `resource "aws_vpc" "example" { cidr_block = "10.0.0.0/33" }`,
		},
	},
	"E201": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `provider "aws" { region = "us-east-11" }`,
		},
	},
	"E202": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `resource "aws_something" "example" { account_id = "12345678901" }`,
		},
	},
	"E203": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `resource "aws_iam_role_policy_attachment" "example" { policy_arn = "not-an-arn" }`,
		},
	},
	"E204": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `resource "aws_s3_bucket" "example" { bucket = "ab" }`,
		},
	},
	"E210": {
		mode:      dialectParityRun,
		wantCount: 1,
		files: map[string]string{
			"main.tf": `
resource "aws_quicksight_data_source" "example" {
  permissions {
    actions   = ["quicksight:DescribeDataSource"]
    principal = "arn:aws:iam::123456789012:user/example"
  }
}
`,
		},
	},
}

func TestRegisteredChecks_TerraformOpenTofuParity(t *testing.T) {
	t.Parallel()

	checks := checker.AllChecks()
	registered := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if _, duplicate := registered[check.Code]; duplicate {
			t.Fatalf("duplicate registered check code %s", check.Code)
		}
		registered[check.Code] = struct{}{}
		parityCase, ok := dialectParityCases[check.Code]
		if !ok {
			t.Fatalf("registered check %s has no Terraform/OpenTofu parity case", check.Code)
		}
		if parityCase.wantCount <= 0 {
			t.Fatalf("registered check %s must declare a positive expected parity count", check.Code)
		}
	}
	for code := range dialectParityCases {
		if _, ok := registered[code]; !ok {
			t.Fatalf("Terraform/OpenTofu parity case %s has no registered check", code)
		}
	}

	for _, check := range checks {
		check := check
		t.Run(check.Code, func(t *testing.T) {
			t.Parallel()
			parityCase := dialectParityCases[check.Code]
			terraform := runDialectParityCase(t, check, parityCase, ".tf")
			opentofu := runDialectParityCase(t, check, parityCase, ".tofu")
			normalizeDialectViolationPaths(terraform, ".tf", parityCase.files)
			normalizeDialectViolationPaths(opentofu, ".tofu", parityCase.files)
			if !slices.Equal(terraform, opentofu) {
				t.Fatalf("Terraform/OpenTofu violations differ\nTerraform: %+v\nOpenTofu:  %+v", terraform, opentofu)
			}
		})
	}
}

func runDialectParityCase(t *testing.T, check checker.CheckInfo, parityCase dialectParityCase, extension string) []checker.Violation {
	t.Helper()
	files := make(map[string]string, len(parityCase.files))
	for name, source := range parityCase.files {
		if filepath.Ext(name) != ".tf" {
			t.Fatalf("parity fixture %s must use a terminal .tf extension", name)
		}
		name = strings.TrimSuffix(name, ".tf") + extension
		files[name] = source
	}
	dir := writeTFDir(t, files)
	ctx := context.Background()

	var violations []checker.Violation
	switch parityCase.mode {
	case dialectParityParse:
		_, parseViolations, err := checker.ParseDir(ctx, dir)
		if err != nil {
			t.Fatalf("ParseDir: %v", err)
		}
		violations = parseViolations
	case dialectParityFormat:
		parsed, parseViolations, err := checker.ParseDirForFormat(ctx, dir)
		if err != nil {
			t.Fatalf("ParseDirForFormat: %v", err)
		}
		if len(parseViolations) != 0 {
			t.Fatalf("format fixture has parse violations: %+v", parseViolations)
		}
		violations, err = checker.CheckFormat(ctx, parsed)
		if err != nil {
			t.Fatalf("CheckFormat: %v", err)
		}
	case dialectParityRun:
		parsed, parseViolations, err := checker.ParseDir(ctx, dir)
		if err != nil {
			t.Fatalf("ParseDir: %v", err)
		}
		if len(parseViolations) != 0 {
			t.Fatalf("run fixture has parse violations: %+v", parseViolations)
		}
		violations, err = checker.Run(ctx, parsed, checker.CheckSet{check.Code: {}}, dir)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	default:
		t.Fatalf("unknown parity mode %q", parityCase.mode)
	}

	if len(violations) != parityCase.wantCount {
		t.Fatalf("fixture for %s produced %d violations, want %d: %+v", check.Code, len(violations), parityCase.wantCount, violations)
	}
	for _, violation := range violations {
		if violation.Code != check.Code {
			t.Fatalf("fixture for %s produced unexpected violation %+v", check.Code, violation)
		}
		if violation.Severity != check.Severity {
			t.Fatalf("fixture for %s produced severity %q, want registry severity %q", check.Code, violation.Severity, check.Severity)
		}
	}
	return violations
}

func normalizeDialectViolationPaths(violations []checker.Violation, extension string, fixtureFiles map[string]string) {
	pathReplacements := make(map[string]string, len(fixtureFiles))
	for name := range fixtureFiles {
		dialectName := strings.TrimSuffix(name, ".tf") + extension
		pathReplacements[dialectName] = name
	}
	for i := range violations {
		if normalised, ok := pathReplacements[violations[i].File]; ok {
			violations[i].File = normalised
		}
		for dialectName, terraformName := range pathReplacements {
			violations[i].Message = strings.ReplaceAll(
				violations[i].Message,
				" at "+dialectName+":",
				" at "+terraformName+":",
			)
		}
	}
}

func TestNormalizeDialectViolationPaths_OnlyKnownFixtureNames(t *testing.T) {
	t.Parallel()
	violations := []checker.Violation{{
		File:    "a.tofu",
		Message: "first defined at a.tofu:1; ba.tofu: and unrelated suffix .tofu: remain",
	}}
	normalizeDialectViolationPaths(violations, ".tofu", map[string]string{"a.tf": ""})
	if violations[0].File != "a.tf" {
		t.Fatalf("File = %q, want a.tf", violations[0].File)
	}
	want := "first defined at a.tf:1; ba.tofu: and unrelated suffix .tofu: remain"
	if violations[0].Message != want {
		t.Fatalf("Message = %q, want %q", violations[0].Message, want)
	}
}
