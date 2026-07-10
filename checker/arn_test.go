// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E203: AWS ARN validation ────────────────────────────────────────────────

// TestE203_ValidCommercialARN_NoViolation verifies a well-formed ARN in the
// commercial (aws) partition passes cleanly.
func TestE203_ValidCommercialARN_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  role       = "admin"
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for valid managed-policy ARN, got: %v", codes(vs))
	}
}

// TestE203_ValidGovCloudARN_NoViolation verifies the aws-us-gov partition
// is recognised.
func TestE203_ValidGovCloudARN_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  role       = "admin"
  policy_arn = "arn:aws-us-gov:iam::123456789012:role/foo"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for aws-us-gov ARN, got: %v", codes(vs))
	}
}

// TestE203_ValidChinaARN_NoViolation verifies the aws-cn partition.
func TestE203_ValidChinaARN_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  role       = "admin"
  policy_arn = "arn:aws-cn:s3:::my-bucket"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for aws-cn ARN, got: %v", codes(vs))
	}
}

// TestE203_ValidEmptyRegionAndAccount_NoViolation verifies ARNs with
// empty region + empty account fields (typical for S3, IAM global).
func TestE203_ValidEmptyRegionAndAccount_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  role       = "admin"
  policy_arn = "arn:aws:s3:::my-bucket/path/to/object"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for S3 object ARN, got: %v", codes(vs))
	}
}

// TestE203_ValidResourceWithColons_NoViolation verifies ARNs whose
// resource segment contains colons (Lambda functions, log groups)
// don't cause parsing to fail after the 5th colon.
func TestE203_ValidResourceWithColons_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_lambda_permission" "x" {
  function_arn = "arn:aws:lambda:us-east-1:111111111111:function:my-func:version:5"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for lambda ARN with resource-colons, got: %v", codes(vs))
	}
}

// TestE203_InvalidPrefix_Violation catches an ARN missing the `arn:`
// prefix.
func TestE203_InvalidPrefix_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "not-an-arn"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for non-arn value, got: %v", codes(vs))
	}
}

// TestE203_InvalidPartition_Violation catches a typoed partition.
func TestE203_InvalidPartition_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws-eu:iam::aws:policy/AdministratorAccess"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for invalid partition 'aws-eu', got: %v", codes(vs))
	}
}

// TestE203_UppercaseService_Violation catches services with uppercase
// letters (AWS service identifiers are lowercase).
func TestE203_UppercaseService_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws:S3:::my-bucket"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for uppercase service 'S3', got: %v", codes(vs))
	}
}

// TestE203_InvalidRegion_Violation catches an ARN with a bad region
// field (that isn't empty — global services legitimately use empty).
func TestE203_InvalidRegion_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_lambda_permission" "x" {
  function_arn = "arn:aws:lambda:atlantis-central-1:111111111111:function:my-func"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for invalid region in ARN, got: %v", codes(vs))
	}
}

// TestE203_InvalidAccountID_Violation catches an ARN with a bad account
// field (not 12 digits, not empty, not "aws").
func TestE203_InvalidAccountID_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  role_arn = "arn:aws:iam::12345:role/foo"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for invalid account ID in ARN, got: %v", codes(vs))
	}
}

// TestE203_EmptyResource_Violation catches an ARN with no resource
// segment — the resource is mandatory for a well-formed ARN.
func TestE203_EmptyResource_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws:s3::111111111111:"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for empty resource, got: %v", codes(vs))
	}
}

// TestE203_TooFewColons_Violation catches ARNs that lack all 6 fields.
func TestE203_TooFewColons_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws:s3"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for truncated ARN, got: %v", codes(vs))
	}
}

// ── List-shape triggers (attributes ending in _arns) ────────────────────────

// TestE203_ListShape_Checked verifies that list-typed ARN attributes
// (`policy_arns` and similar) have each element validated.
func TestE203_ListShape_Checked(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role" "x" {
  managed_policy_arns = [
    "arn:aws:iam::aws:policy/AdministratorAccess",
    "arn:foobar:iam::aws:policy/BadPolicy",
  ]
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 on invalid element in _arns list, got: %v", codes(vs))
	}
}

// TestE203_ListShape_AllValid_NoViolation verifies list attributes with
// all-valid elements produce no violations.
func TestE203_ListShape_AllValid_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role" "x" {
  managed_policy_arns = [
    "arn:aws:iam::aws:policy/AdministratorAccess",
    "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess",
  ]
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for all-valid list, got: %v", codes(vs))
	}
}

// ── Template / interpolation cases (uses PR #47 template subsystem) ─────────

// TestE203_InterpolatedPartition_NoViolation verifies a template with
// the partition field fully replaced by an interpolation (a common
// pattern via data.aws_partition.current.partition) passes.
func TestE203_InterpolatedPartition_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AdministratorAccess"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for interpolated partition, got: %v", codes(vs))
	}
}

// TestE203_InterpolatedAccount_NoViolation verifies a template with
// the account field fully replaced by an interpolation passes.
func TestE203_InterpolatedAccount_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws:iam::${var.account_id}:role/foo"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for interpolated account, got: %v", codes(vs))
	}
}

// TestE203_InterpolatedResourceOnly_NoViolation verifies templates where
// only the resource segment has interpolations (very common pattern).
func TestE203_InterpolatedResourceOnly_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws:s3:::${var.bucket_id}/${var.prefix}"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 for interpolated resource, got: %v", codes(vs))
	}
}

// TestE203_TemplatedInvalidPartition_Violation catches a bad partition
// even when other fields are interpolated.
func TestE203_TemplatedInvalidPartition_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws-eu:iam::${var.account}:role/foo"
}
`,
	})
	if !hasCode(vs, "E203") {
		t.Fatalf("expected E203 for invalid partition in template, got: %v", codes(vs))
	}
}

// TestE203_TemplatedMidField_Skipped verifies templates where an
// interpolation is mixed with literal content INSIDE a pre-resource
// field are silently skipped. Cannot statically validate the composed
// value of the field.
func TestE203_TemplatedMidField_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_iam_role_policy_attachment" "x" {
  policy_arn = "arn:aws-${var.suffix}:iam::aws:policy/foo"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("mid-field interpolation should be skipped, got: %v", codes(vs))
	}
}

// ── Attribute triggering (suffix pattern) ───────────────────────────────────

// TestE203_ScalarSuffixTrigger_Checked verifies scalar attributes ending
// in `_arn` are picked up.
func TestE203_ScalarSuffixTrigger_Checked(t *testing.T) {
	cases := []string{"role_arn", "target_arn", "source_arn", "topic_arn"}
	for _, attr := range cases {
		attr := attr
		t.Run(attr, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{
				"main.tf": `
resource "aws_something" "x" {
  ` + attr + ` = "not-an-arn"
}
`,
			})
			if !hasCode(vs, "E203") {
				t.Fatalf("expected E203 on %s, got: %v", attr, codes(vs))
			}
		})
	}
}

// TestE203_BareArnAttribute_Skipped verifies an attribute named exactly
// "arn" (with no underscore prefix) is NOT triggered. Bare `arn` is
// almost always an output-only attribute, so linting the (unlikely)
// input case would be a source of noise.
func TestE203_BareArnAttribute_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  arn = "not-an-arn"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("bare 'arn' attribute should not fire E203, got: %v", codes(vs))
	}
}

// TestE203_NonARNAttribute_NotChecked verifies attributes without an
// _arn / _arns suffix are silently ignored, even when they hold
// ARN-looking values (defensive against false positives).
func TestE203_NonARNAttribute_NotChecked(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  description = "this ARN is not a trigger: arn:foobar:s3:::bucket"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("non-trigger attribute should not fire E203, got: %v", codes(vs))
	}
}

// ── Standard exclusions ──────────────────────────────────────────────────────

// TestE203_EmptyString_Skipped verifies empty attribute values are
// silently skipped.
func TestE203_EmptyString_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  role_arn = ""
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("empty _arn should not fire E203, got: %v", codes(vs))
	}
}

// TestE203_Interpolation_Skipped verifies bare traversals (not templates)
// are silently skipped — statically unresolvable.
func TestE203_Interpolation_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  role_arn = data.aws_iam_role.admin.arn
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("bare traversal should not fire E203, got: %v", codes(vs))
	}
}

// TestE203_DisabledCheck_NoViolation verifies --checks= without E203
// suppresses the check.
func TestE203_DisabledCheck_NoViolation(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  role_arn = "not-an-arn"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E101": {}} // deliberately not E203
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E203") {
		t.Fatalf("expected no E203 when disabled, got: %v", codes(vs))
	}
}

// TestE203_VariableDefault_Skipped verifies E203 does not fire inside
// variable defaults (Tier-3 exclusion).
func TestE203_VariableDefault_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "role_arn" {
  default = "not-an-arn"
}
`,
	})
	if hasCode(vs, "E203") {
		t.Fatalf("variable default should not fire E203, got: %v", codes(vs))
	}
}
