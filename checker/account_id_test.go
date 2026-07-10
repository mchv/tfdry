// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E202: AWS account ID validation ─────────────────────────────────────────

// TestE202_ValidAccountID_NoViolation verifies a 12-digit account ID
// passes cleanly.
func TestE202_ValidAccountID_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_ebs_snapshot_ids_data_source" "x" {
  account_id = "123456789012"
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("expected no E202 for 12-digit ID, got: %v", codes(vs))
	}
}

// TestE202_TooShort_Violation catches an account ID under 12 digits.
func TestE202_TooShort_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = "12345678901"
}
`,
	})
	if !hasCode(vs, "E202") {
		t.Fatalf("expected E202 for 11-digit ID, got: %v", codes(vs))
	}
}

// TestE202_TooLong_Violation catches an account ID over 12 digits.
func TestE202_TooLong_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = "1234567890123"
}
`,
	})
	if !hasCode(vs, "E202") {
		t.Fatalf("expected E202 for 13-digit ID, got: %v", codes(vs))
	}
}

// TestE202_NonDigit_Violation catches an account ID with a non-digit
// character (letters, hyphens, spaces).
func TestE202_NonDigit_Violation(t *testing.T) {
	cases := []string{
		"12345678901a", // letter at end
		"a23456789012", // letter at start
		"1234-5678-90", // hyphens (11 chars + 2 hyphens = 13 chars, non-digit)
		"123 45678901", // space
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{
				"main.tf": `
resource "aws_something" "x" {
  account_id = "` + tc + `"
}
`,
			})
			if !hasCode(vs, "E202") {
				t.Fatalf("expected E202 for %q, got: %v", tc, codes(vs))
			}
		})
	}
}

// TestE202_LeadingZeroesAllowed verifies leading zeroes are accepted —
// AWS account IDs are string-typed identifiers, not integers, so
// "000123456789" is a valid syntactic ID (whether such an account
// exists is an AWS-side concern, not tfdry's).
func TestE202_LeadingZeroesAllowed(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = "000123456789"
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("leading-zero ID should pass E202, got: %v", codes(vs))
	}
}

// TestE202_EmptyString_Skipped verifies an empty attribute value is
// silently skipped (mirrors E201/E101 policy).
func TestE202_EmptyString_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = ""
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("empty account_id should not fire E202, got: %v", codes(vs))
	}
}

// TestE202_Interpolation_Skipped verifies an interpolated value is
// silently skipped — statically-unresolvable content can't be validated
// as a 12-digit ID.
func TestE202_Interpolation_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = data.aws_caller_identity.current.account_id
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("interpolated account_id should not fire E202, got: %v", codes(vs))
	}
}

// TestE202_TemplatedAccountID_Skipped verifies templated values (with
// interpolation parts) are silently skipped. Account IDs are compact
// digit sequences with no natural boundaries — placeholder-composed
// validation would be arbitrary.
func TestE202_TemplatedAccountID_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = "1234${var.mid}9012"
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("templated account_id should not fire E202, got: %v", codes(vs))
	}
}

// TestE202_AttributeNotInTriggerList_Skipped verifies attributes not on
// the trigger list are silently ignored, even if their values happen to
// look like account IDs.
func TestE202_AttributeNotInTriggerList_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  tags = {
    cost_centre = "12345678901" # 11 digits — not a real ID, but not a trigger either
  }
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("non-trigger attribute should not fire E202, got: %v", codes(vs))
	}
}

// TestE202_DisabledCheck_NoViolation verifies --checks= without E202
// suppresses the check.
func TestE202_DisabledCheck_NoViolation(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_something" "x" {
  account_id = "not-a-real-id"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E101": {}} // deliberately not E202
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E202") {
		t.Fatalf("expected no E202 when disabled, got: %v", codes(vs))
	}
}

// TestE202_VariableDefault_Skipped verifies E202 does not fire inside
// variable defaults (Tier-3 exclusion, mirrors E101/E201).
func TestE202_VariableDefault_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "account_id" {
  default = "not-a-real-id"
}
`,
	})
	if hasCode(vs, "E202") {
		t.Fatalf("variable default should not fire E202, got: %v", codes(vs))
	}
}
