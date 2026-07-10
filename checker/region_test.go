// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E201: AWS region validation ─────────────────────────────────────────────

// TestE201_ValidStandardRegion_NoViolation verifies a well-known commercial
// region parses cleanly.
func TestE201_ValidStandardRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "us-east-1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("expected no E201 for us-east-1, got: %v", codes(vs))
	}
}

// TestE201_ValidGovCloudRegion_NoViolation verifies AWS GovCloud regions
// are recognised (aws-us-gov partition).
func TestE201_ValidGovCloudRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "us-gov-west-1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("expected no E201 for us-gov-west-1, got: %v", codes(vs))
	}
}

// TestE201_ValidChinaRegion_NoViolation verifies AWS China regions
// are recognised (aws-cn partition).
func TestE201_ValidChinaRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "cn-north-1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("expected no E201 for cn-north-1, got: %v", codes(vs))
	}
}

// TestE201_InvalidRegion_Violation catches an obvious typo.
func TestE201_InvalidRegion_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "us-east-11"
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("expected E201 for us-east-11 (typoed), got: %v", codes(vs))
	}
}

// TestE201_MadeUpRegion_Violation catches a fabricated region that has the
// right shape but isn't a real AWS region.
func TestE201_MadeUpRegion_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "atlantis-central-1"
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("expected E201 for atlantis-central-1, got: %v", codes(vs))
	}
}

// TestE201_EmptyString_Skipped verifies the empty string is silently
// skipped — an empty region attribute is typically the user's intent to
// let AWS pick a default, not a typo.
func TestE201_EmptyString_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = ""
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("empty region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_Interpolation_Skipped verifies an interpolated region is
// silently skipped — statically unresolvable content can't be validated
// as a region literal.
func TestE201_Interpolation_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = var.region
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("interpolated region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_TemplatedRegion_Skipped verifies a templated string (with
// interpolation parts) is silently skipped — regions don't have
// segment boundaries that let placeholder substitution be meaningful.
func TestE201_TemplatedRegion_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "us-${var.axis}-1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("templated region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_AttributeNotInTriggerList_Skipped verifies attributes with
// values that look like regions but aren't in the trigger list are
// silently ignored. Guards against false positives on unrelated string
// attributes that happen to hold region-shaped values (e.g. a tag).
func TestE201_AttributeNotInTriggerList_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "not-a-region-attribute"
  tags = {
    location = "atlantis-central-1"
  }
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("non-trigger attribute should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_DisabledCheck_NoViolation verifies --checks= without E201
// suppresses the check.
func TestE201_DisabledCheck_NoViolation(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "atlantis-central-1"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E101": {}} // deliberately not E201
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E201") {
		t.Fatalf("expected no E201 when disabled, got: %v", codes(vs))
	}
}

// TestE201_VariableDefault_Skipped verifies E201 does not fire inside a
// variable block's default value — variable defaults are Tier-3-excluded
// for grammar checks (mirrors E101's behaviour on cidr_block defaults).
func TestE201_VariableDefault_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "region" {
  default = "atlantis-central-1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("variable default should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_NestedBlock_Checked verifies E201 fires on region attributes
// inside nested resource blocks, not just top-level provider blocks.
func TestE201_NestedBlock_Checked(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_replication_configuration" "x" {
  role = "arn:aws:iam::111111111111:role/x"
  rule {
    id     = "r1"
    status = "Enabled"
    destination {
      bucket        = "arn:aws:s3:::backup"
      storage_class = "STANDARD"
      region        = "atlantis-central-1"
    }
  }
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("expected E201 on nested-block region, got: %v", codes(vs))
	}
}
