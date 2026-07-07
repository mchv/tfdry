// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E101: valid CIDRs — no violation ─────────────────────────────────────────

func TestE101_ValidIPv4Scalar_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.0.0/16"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101, got: %v", codes(vs))
	}
}

func TestE101_ValidIPv6Scalar_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  ipv6_cidr_block = "2001:db8::/32"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101, got: %v", codes(vs))
	}
}

func TestE101_ValidIPv4List_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group_rule" "x" {
  cidr_blocks = ["10.0.0.0/16", "172.16.0.0/12"]
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101, got: %v", codes(vs))
	}
}

func TestE101_ValidIPv6List_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group_rule" "x" {
  ipv6_cidr_blocks = ["2001:db8::/32", "fc00::/7"]
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101, got: %v", codes(vs))
	}
}

// ── E101: invalid CIDRs — violation expected ─────────────────────────────────

func TestE101_InvalidScalar_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "not a cidr"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101, got: %v", codes(vs))
	}
}

func TestE101_InvalidPrefixLength_Violation(t *testing.T) {
	// /33 is invalid for IPv4 — the prefix length must be 0..32.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.0.0/33"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101, got: %v", codes(vs))
	}
}

func TestE101_InvalidIPv4Octet_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.0.256/24"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101, got: %v", codes(vs))
	}
}

func TestE101_InvalidIPv6_Violation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  ipv6_cidr_block = "notipv6::/32"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101, got: %v", codes(vs))
	}
}

func TestE101_ListWithOneBad_OneViolationOnBadElement(t *testing.T) {
	// A single bad element should produce exactly one E101, not drop the
	// whole list. The violation must point at the offending element's line,
	// not the attribute-declaration line — a maintainer scanning tfdry
	// output wants to jump directly to the broken value.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group_rule" "x" {
  cidr_blocks = [
    "10.0.0.0/16",
    "not a cidr",
    "172.16.0.0/12",
  ]
}
`,
	})
	var e101 []checker.Violation
	for _, v := range vs {
		if v.Code == "E101" {
			e101 = append(e101, v)
		}
	}
	if len(e101) != 1 {
		t.Fatalf("expected exactly 1 E101, got %d: %v", len(e101), codes(vs))
	}
	// The bad element sits on line 5 of the source (after the leading \n).
	// Any other line means the check is attributing violations to the
	// wrong location — a bug that would degrade user experience.
	if got := e101[0].Line; got != 5 {
		t.Fatalf("expected E101 on line 5 (the bad element), got line %d", got)
	}
}

// ── E101: things the check must skip (no false positives) ────────────────────

func TestE101_Interpolation_Skipped(t *testing.T) {
	// Interpolated / traversal expressions cannot be validated statically —
	// the CIDR only exists at plan time. The check must skip.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = var.vpc_cidr
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("interpolation should be skipped, got: %v", codes(vs))
	}
}

func TestE101_InterpolatedTemplate_Skipped(t *testing.T) {
	// A template with an interpolation part (${...}) is still a
	// hclsyntax.TemplateExpr but with >1 parts. Must not be treated as
	// a literal CIDR to validate.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "${var.vpc_prefix}.0.0/16"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("template with interpolation should be skipped, got: %v", codes(vs))
	}
}

func TestE101_EmptyString_Skipped(t *testing.T) {
	// An empty string literal is clearly not intended as a CIDR to validate.
	// Producing an E101 here would flag every unset-by-convention placeholder
	// (e.g. cluster_service_cidr = "" from the EKS test fixture in the corpus).
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = ""
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("empty string should be skipped, got: %v", codes(vs))
	}
}

func TestE101_NonStringValue_Skipped(t *testing.T) {
	// A non-string value on a CIDR attribute is a different class of error
	// (provider-schema type mismatch) that tfdry does not attempt to catch
	// offline. E101 must skip rather than emit noise.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = 42
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("non-string value should be skipped, got: %v", codes(vs))
	}
}

func TestE101_AttributeNotInTriggerList_Skipped(t *testing.T) {
	// Attributes named things like `subnet` or `default` are not on the
	// trigger list — Tier 3 exclusions from the design phase — so even if
	// their value looks CIDR-shaped, we don't validate.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  some_other_attr = "not.a.valid/cidr"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("non-trigger attribute should be skipped, got: %v", codes(vs))
	}
}

func TestE101_VariableDefault_Skipped(t *testing.T) {
	// `default` inside a `variable` block is a Tier 3 exclusion — the type
	// of the variable is not knowable without a variable-type walker.
	// A malformed value in a variable's default would otherwise be flagged
	// even though callers may override it.
	vs := run(t, map[string]string{
		"main.tf": `
variable "cidr_x" {
  default = "not a cidr"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("variable default should not trigger E101, got: %v", codes(vs))
	}
}

// ── E101: coverage of the full trigger table ─────────────────────────────────

func TestE101_AllScalarTriggersFlagInvalid(t *testing.T) {
	// Every attribute in the scalar-shape trigger list must produce E101
	// on an invalid value. Regressions here would silently disable
	// coverage for a specific attribute — hard to notice without a
	// completeness assertion.
	scalarTriggers := []string{
		"cidr_block",
		"destination_cidr_block",
		"destination_ipv6_cidr_block",
		"source_cidr_block",
		"ipv6_cidr_block",
		"source_ipv6_cidr_block",
		"cluster_service_cidr",
		"primary_vpc_cidr",
		"secondary_vpc_cidr",
		"tgw_destination_cidr",
		"vpc_cidr",
	}
	for _, attr := range scalarTriggers {
		t.Run(attr, func(t *testing.T) {
			vs := run(t, map[string]string{
				"main.tf": `
resource "aws_vpc" "x" {
  ` + attr + ` = "definitely not a cidr"
}
`,
			})
			if !hasCode(vs, "E101") {
				t.Fatalf("attribute %q with invalid CIDR must trigger E101, got: %v", attr, codes(vs))
			}
		})
	}
}

func TestE101_AllListTriggersFlagInvalid(t *testing.T) {
	listTriggers := []string{
		"cidr_blocks",
		"ipv6_cidr_blocks",
		"source_ipv6_cidr_blocks",
		"admin_cidr_blocks",
		"allowed_cidr_blocks",
		"egress_cidr_blocks",
		"ingress_cidr_blocks",
		"secondary_cidr_blocks",
		"transit_gateway_cidr_blocks",
	}
	for _, attr := range listTriggers {
		t.Run(attr, func(t *testing.T) {
			vs := run(t, map[string]string{
				"main.tf": `
resource "aws_security_group_rule" "x" {
  ` + attr + ` = ["definitely not a cidr"]
}
`,
			})
			if !hasCode(vs, "E101") {
				t.Fatalf("attribute %q with invalid CIDR in list must trigger E101, got: %v", attr, codes(vs))
			}
		})
	}
}

// ── E101: interaction with the check-set filter ──────────────────────────────

func TestE101_DisabledCheck_NoViolation(t *testing.T) {
	// The check-set filter must properly gate E101. If a user runs
	// tfdry --checks=E001, running an E101-triggering file must not
	// leak the E101 violation into the results.
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "not a cidr"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, checker.CheckSet{"E001": struct{}{}}, dir))
	if hasCode(vs, "E101") {
		t.Fatalf("E101 must not fire when disabled, got: %v", codes(vs))
	}
}

// ── E101: nested-block coverage ──────────────────────────────────────────────

func TestE101_NestedBlock_Checked(t *testing.T) {
	// aws_security_group ingress {} blocks are a common source of CIDR
	// literals. The walker must descend into nested blocks, not just
	// top-level attributes.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  ingress {
    cidr_blocks = ["not a cidr"]
  }
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101 for nested-block cidr_blocks, got: %v", codes(vs))
	}
}
