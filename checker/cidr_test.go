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

// ── Interpolation-aware validation (2026-07-08) ──────────────────────────────

// TestE101_InterpolatedValidScope_NoViolation: an interpolated CIDR with
// a valid Terraform scope root and enough literal shape produces no
// violation. Composed form validates cleanly.
func TestE101_InterpolatedValidScope_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.subnet}.0/24"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101, got: %v", codes(vs))
	}
	assertNoScopeRootDiag(t, vs, "var.subnet in CIDR interpolation")
}

// TestE101_InterpolatedInvalidLiteralOctet: an interpolated CIDR where the
// literal parts contain an invalid octet (256) must be flagged E101 even
// though other octets are interpolated.
func TestE101_InterpolatedInvalidLiteralOctet_ViolationE101(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.subnet}.256/24"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101 (invalid literal octet 256), got: %v", codes(vs))
	}
}

// TestE101_InterpolatedInsufficientShape_Skipped: when literal parts don't
// canonically fix the octet count, composed-form check is deliberately
// skipped to avoid false positives on legitimate prefix-style interpolations.
func TestE101_InterpolatedInsufficientShape_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  # var.vpc_prefix could reasonably be "10.0" → resolves to 10.0.0.0/16.
  # Only 2 literal dots — insufficient shape, must not flag as E101.
  cidr_block = "${var.vpc_prefix}.0.0/16"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101 (insufficient shape), got: %v", codes(vs))
	}
}

// TestE009_InterpolatedInvalidScopeRoot_ViolationE009: an interpolation
// with a typoed scope root ("vars" instead of "var") produces an E009
// violation with a corrective hint.
func TestE009_InterpolatedInvalidScopeRoot_ViolationE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${vars.subnet}.0/24"
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 (invalid scope root 'vars'), got: %v", codes(vs))
	}
	// The message must include the offending root and the corrective hint.
	var found *checker.Violation
	for i := range vs {
		if vs[i].Code == "E009" {
			found = &vs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected exactly one E009, none found")
	}
	if !contains(found.Message, `"vars"`) {
		t.Errorf("message %q missing offending root 'vars'", found.Message)
	}
	if !contains(found.Message, `"var"`) {
		t.Errorf("message %q missing corrective hint 'var'", found.Message)
	}
}

// TestE009_InterpolatedResourceType_NoViolation: an interpolation whose
// root is a valid resource-type identifier (contains an underscore) must
// not trigger E009 — provider resource references are legitimate.
func TestE009_InterpolatedResourceType_NoViolation(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_subnet" "s" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.0.${aws_vpc.main.instance_tenancy}.0/24"
}
`,
	})
	assertNoScopeRootDiag(t, vs, "aws_vpc resource-type reference")
}

// TestE009_Disabled_NoScopeRootDiagnostic: with E009 disabled but E101
// enabled, format errors surface but scope-root diagnostics do not.
func TestE009_Disabled_NoScopeRootDiagnostic(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${vars.subnet}.0/24"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	// Only enable E101; E009 disabled.
	enabled := checker.CheckSet{"E101": {}}
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	assertNoScopeRootDiag(t, vs, "E009 disabled via --checks=E101")
}

// TestE101_InterpolatedListElement_MultipleDiagnostics: a list attribute
// with a mix of literal and interpolated elements produces independent
// diagnostics per element.
func TestE101_InterpolatedListElement_MultipleDiagnostics(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group_rule" "x" {
  cidr_blocks = [
    "10.0.0.0/16",
    "10.0.${vars.subnet}.0/24",
    "10.0.${var.subnet}.256/24",
  ]
}
`,
	})
	// Second element: bad scope root → E009. Third element: bad literal
	// octet → E101. First element: clean, no diagnostic.
	if !hasCode(vs, "E009") {
		t.Errorf("expected E009 for 'vars' scope-root typo, got: %v", codes(vs))
	}
	if !hasCode(vs, "E101") {
		t.Errorf("expected E101 for literal octet 256, got: %v", codes(vs))
	}
}

// TestE101_ComposedMessage_ShowsPlaceholder: format violations on
// interpolated values must include a placeholder-substituted display form
// (<P>) in the message so users can see the shape the check operated on.
func TestE101_ComposedMessage_ShowsPlaceholder(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.subnet}.256/24"
}
`,
	})
	var e101 *checker.Violation
	for i := range vs {
		if vs[i].Code == "E101" {
			e101 = &vs[i]
			break
		}
	}
	if e101 == nil {
		t.Fatalf("expected E101, got: %v", codes(vs))
	}
	if !contains(e101.Message, "<P>") {
		t.Errorf("message %q should show <P> placeholder for interp", e101.Message)
	}
	if !contains(e101.Message, "256") {
		t.Errorf("message %q should include the offending literal octet '256'", e101.Message)
	}
}

// contains is a local, allocation-free helper for substring assertions.
// Kept package-private to cidr_test.go rather than promoted to
// checks_test.go because it's only used in this file.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestE009_EnabledAlone_FiresWithoutE101 verifies that a user running with
// only E009 enabled (--checks=E009) still gets scope-root diagnostics on
// interpolated CIDR attributes. Existing coverage (TestE009_Disabled_*)
// tested the E101-alone direction; this test closes the bidirectional
// gate matrix by exercising the E009-alone direction.
func TestE009_EnabledAlone_FiresWithoutE101(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${vars.subnet}.0/24"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E009": {}}
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 with only E009 enabled, got: %v", codes(vs))
	}
}

// TestE101_Disabled_NoFormatDiagnostic verifies the flipside: with only
// E009 enabled and a CIDR value that would normally trigger E101 (invalid
// literal octet 256), the E101 diagnostic must be suppressed even though
// checkCIDR runs (because E009 is enabled).
func TestE101_Disabled_NoFormatDiagnostic(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.subnet}.256/24"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E009": {}}
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E101") {
		t.Fatalf("E101 must be suppressed when E101 is disabled (only E009 enabled), got: %v", codes(vs))
	}
}

// TestE101_Disabled_NoFormatDiagnostic_PureLiteral covers the same
// suppression for the pure-literal fast path — E101-disabled must not
// emit format diagnostics on hardcoded invalid CIDRs either.
func TestE101_Disabled_NoFormatDiagnostic_PureLiteral(t *testing.T) {
	dir := writeTFDir(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "not-a-cidr"
}
`,
	})
	parsed, parseViolations, _ := checker.ParseDir(context.Background(), dir)
	enabled := checker.CheckSet{"E009": {}}
	vs := slices.Concat(parseViolations, mustRun(context.Background(), parsed, enabled, dir))
	if hasCode(vs, "E101") {
		t.Fatalf("E101 pure-literal branch must be suppressed when E101 is disabled, got: %v", codes(vs))
	}
}

// TestE101_InterpolatedPartialOctet_NoFalsePositive verifies that an
// interpolation mid-octet does not produce a false E101 when the
// placeholder-substituted composed form happens to be invalid. The
// user's intent is opaque — '${var.suffix}' in "10.0.0.26${var.suffix}"
// could reasonably resolve to anything, so we cannot statically decide.
//
// Why existing tests missed this: previous coverage placed interpolations
// only at segment boundaries (between dots, or immediately before a
// slash), where placeholder substitution is a clean single-segment
// replacement. Mid-octet interpolations weren't exercised.
func TestE101_InterpolatedPartialOctet_NoFalsePositive(t *testing.T) {
	// Composed with placeholder "0" would be "10.0.0.260/24" — invalid,
	// 260 > 255. But we cannot know the user's intent; must not flag.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.0.26${var.suffix}/24"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101 (interp mid-octet, opaque intent), got: %v", codes(vs))
	}
}

// TestE101_InterpolatedTrailingDigits_NoFalsePositive: interpolation
// followed by literal digits before the slash — same category as the
// partial-octet case (interp not adjacent to segment separator).
func TestE101_InterpolatedTrailingDigits_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.0.${var.prefix}5/24"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101 (interp adjacent to digit '5'), got: %v", codes(vs))
	}
}

// TestE101_InterpolatedSegmentBoundary_StillChecked confirms the boundary
// case still fires: interp between dots, with an invalid literal octet
// elsewhere in the value, must produce E101.
func TestE101_InterpolatedSegmentBoundary_StillChecked(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.x}.256/24"
}
`,
	})
	if !hasCode(vs, "E101") {
		t.Fatalf("expected E101 (256 in literal, interp at segment boundary), got: %v", codes(vs))
	}
}

// TestE101_AdjacentInterpolations_Skipped covers two interpolations back
// to back with no literal separator between them — cannot determine
// where one segment ends and the next begins.
func TestE101_AdjacentInterpolations_Skipped(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_vpc" "x" {
  cidr_block = "10.0.${var.a}${var.b}.0/24"
}
`,
	})
	if hasCode(vs, "E101") {
		t.Fatalf("expected no E101 (adjacent interps), got: %v", codes(vs))
	}
}
