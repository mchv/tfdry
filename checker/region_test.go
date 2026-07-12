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

// TestE201_ExtremelyLongInput_Rejected verifies inputs far outside the
// valid AWS region length range are rejected efficiently by the length
// filter before hitting the map lookup. Regression guard for the
// length-filter fast-reject claim in the region.go docstring.
func TestE201_ExtremelyLongInput_Rejected(t *testing.T) {
	// Longest valid region is 14 chars ("ap-northeast-3", "cn-northwest-1").
	// This input is 40 chars — should never match.
	longInput := "us-east-1-with-a-really-long-suffix-abcd"
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "` + longInput + `"
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("expected E201 for extremely long input, got: %v", codes(vs))
	}
}

// TestE201_ExtremelyShortInput_Rejected verifies inputs below the min
// valid region length are rejected efficiently. Shortest valid region
// is 9 chars (us-east-1, us-west-1, etc.).
func TestE201_ExtremelyShortInput_Rejected(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "us"
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("expected E201 for 2-char input, got: %v", codes(vs))
	}
}

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

// ── AWS applicability gating (round 4 redesign) ─────────────────────────────

// TestE201_GoogleProviderRegion_NoViolation verifies that a `region`
// attribute inside `provider "google"` does NOT fire E201. `region` is
// generic across cloud providers (GCP, DigitalOcean, Cloudflare Workers,
// etc.), and a default-error finding on non-AWS Terraform configurations
// would violate the "default errors must be highly certain" contract.
// AWS applicability must be established before validating the value.
func TestE201_GoogleProviderRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "google" {
  region = "us-central1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("GCP provider region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_GoogleBetaProviderRegion_NoViolation covers the google-beta
// provider variant, which appears in official GCP Terraform docs.
func TestE201_GoogleBetaProviderRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "google-beta" {
  region = "europe-west1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("google-beta provider region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_GCPResourceRegion_NoViolation covers the case where the AWS
// signal isn't the provider block but the resource type. A google_* or
// digitalocean_* resource with a region attribute must not fire.
func TestE201_GCPResourceRegion_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "google_compute_instance" "x" {
  region = "us-central1"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("GCP resource region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_GenericModuleInput_NoViolation covers module inputs. Without
// knowing the module's schema or origin, `region = "..."` inside a
// `module "..." { ... }` block cannot be assumed to be an AWS region.
// Skip.
func TestE201_GenericModuleInput_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
module "network" {
  source = "./modules/network"
  region = "eu-west-99"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("module input region should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_AWSResourceRegion_StillFires is the positive regression
// guard: an obviously-invalid region inside an aws_* resource must
// still fire E201 after the scoping change.
func TestE201_AWSResourceRegion_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket_replication_configuration" "x" {
  destination {
    region = "atlantis-central-1"
  }
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("invalid region inside aws_* resource should still fire E201, got: %v", codes(vs))
	}
}

// TestE201_AWSDataSourceRegion_StillFires covers data blocks — `data
// "aws_*"` should also carry AWS applicability.
func TestE201_AWSDataSourceRegion_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
data "aws_availability_zones" "x" {
  region = "atlantis-central-1"
}
`,
	})
	if !hasCode(vs, "E201") {
		t.Fatalf("invalid region inside aws_* data should still fire E201, got: %v", codes(vs))
	}
}

// ── Missing regions from AWS documentation (as of 2026) ─────────────────────

// TestE201_ApSoutheast6_NoViolation verifies that ap-southeast-6 is
// recognised as a valid AWS region. Present in official AWS region
// documentation; missing from the round-3 hand-maintained list which
// contained ap-southeast-5 and -7 but skipped -6.
func TestE201_ApSoutheast6_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "ap-southeast-6"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("ap-southeast-6 is a valid AWS region and should not fire E201, got: %v", codes(vs))
	}
}

// TestE201_ApEast2_NoViolation verifies that ap-east-2 (Taipei) is
// recognised as a valid AWS region. Announced by AWS in mid-2025.
func TestE201_ApEast2_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
provider "aws" {
  region = "ap-east-2"
}
`,
	})
	if hasCode(vs, "E201") {
		t.Fatalf("ap-east-2 (Taipei) is a valid AWS region and should not fire E201, got: %v", codes(vs))
	}
}
