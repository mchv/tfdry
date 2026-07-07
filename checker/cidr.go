// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"net/netip"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ── E101: CIDR block literal validation ──────────────────────────────────────
//
// Attribute triggers are enumerated (not regex) — see PR β design in issue #23
// for the tier rationale. Adding an attribute here is a deliberate act; a
// misplaced entry produces user-visible false positives that erode trust in
// the whole check.
//
// Triggers live in a single map with a shape enum, not two shape-specific
// maps. Every attribute name that hclsyntax hands us gets one map lookup;
// the zero value of cidrShape is `cidrShapeNone`, so a non-trigger name
// falls through the switch below without extra work. Halving the lookup
// cost matters because the hot path is "attribute is not a trigger" —
// a realistic Terraform block has 10-50 attributes of which typically
// 0-2 are CIDR-related.

// cidrShape encodes whether an attribute holds a single CIDR string or a
// list of CIDR strings. The zero value cidrShapeNone corresponds to a
// map miss and lets the switch below be a plain lookup with no separate
// ok-check.
type cidrShape uint8

const (
	cidrShapeNone cidrShape = iota
	cidrShapeScalar
	cidrShapeList
)

// cidrTriggers is the enumerated attribute-name → shape table. Tier 1
// (standard AWS provider) and Tier 2 (common module conventions) locked
// during PR β design (2026-07-07); Tier 3 candidates (`cidr`, `*_subnets`,
// `default`) are deliberately excluded — see the PR description on #23
// for the ambiguity rationale on each.
var cidrTriggers = map[string]cidrShape{
	// Tier 1 — standard AWS provider (scalar)
	"cidr_block":                  cidrShapeScalar,
	"destination_cidr_block":      cidrShapeScalar,
	"destination_ipv6_cidr_block": cidrShapeScalar,
	"source_cidr_block":           cidrShapeScalar,
	"ipv6_cidr_block":             cidrShapeScalar,
	"source_ipv6_cidr_block":      cidrShapeScalar,
	// Tier 1 — standard AWS provider (list)
	"cidr_blocks":             cidrShapeList,
	"ipv6_cidr_blocks":        cidrShapeList,
	"source_ipv6_cidr_blocks": cidrShapeList,
	// Tier 2 — module conventions (scalar)
	"cluster_service_cidr": cidrShapeScalar,
	"primary_vpc_cidr":     cidrShapeScalar,
	"secondary_vpc_cidr":   cidrShapeScalar,
	"tgw_destination_cidr": cidrShapeScalar,
	"vpc_cidr":             cidrShapeScalar,
	// Tier 2 — module conventions (list)
	"admin_cidr_blocks":           cidrShapeList,
	"allowed_cidr_blocks":         cidrShapeList,
	"egress_cidr_blocks":          cidrShapeList,
	"ingress_cidr_blocks":         cidrShapeList,
	"secondary_cidr_blocks":       cidrShapeList,
	"transit_gateway_cidr_blocks": cidrShapeList,
}

// checkCIDR runs E101 over a single parsed file, returning one Violation per
// bad CIDR literal. See walkCIDRBlocks for the walk contract.
func checkCIDR(f ParsedFile) []Violation {
	var violations []Violation
	walkCIDRBlocks(f.Body, f.Name, &violations)
	return violations
}

// walkCIDRBlocks descends into a body's attributes and child blocks. It skips
// `variable` blocks in their entirety because `default` inside them is a
// Tier 3 exclusion (the variable's declared type is not knowable to an
// offline checker, so a bad `default` cannot be distinguished from a
// deliberately-loose default that callers always override).
func walkCIDRBlocks(body *hclsyntax.Body, file string, violations *[]Violation) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		switch cidrTriggers[attr.Name] {
		case cidrShapeScalar:
			checkCIDRScalar(file, attr, violations)
		case cidrShapeList:
			checkCIDRList(file, attr, violations)
		}
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" {
			continue
		}
		walkCIDRBlocks(block.Body, file, violations)
	}
}

// checkCIDRScalar validates a single-string CIDR attribute. Interpolation,
// empty literals, and non-string values are silently skipped — the check is
// deliberately conservative to keep the false-positive rate at zero on real
// modules.
func checkCIDRScalar(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	v, ok := cidrLiteralString(attr.Expr)
	if !ok || v == "" {
		return
	}
	if err := validateCIDR(v); err != nil {
		*violations = append(*violations, cidrViolation(file, attr.Expr.Range().Start.Line, attr.Name, v, err))
	}
}

// checkCIDRList validates each element of a list-typed CIDR attribute
// independently. A single bad element produces one violation without
// affecting the sibling elements — one bad CIDR in a security-group
// ingress list should not silence findings on the other entries.
func checkCIDRList(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	tuple, ok := attr.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		// Interpolated single-value or traversal (e.g. cidr_blocks = var.foo).
		// Not statically checkable; skip rather than emit a spurious error.
		return
	}
	for _, elem := range tuple.Exprs {
		v, ok := cidrLiteralString(elem)
		if !ok || v == "" {
			continue
		}
		if err := validateCIDR(v); err != nil {
			*violations = append(*violations, cidrViolation(file, elem.Range().Start.Line, attr.Name, v, err))
		}
	}
}

// cidrViolation packages a Violation for E101. Extracted so the scalar and
// list paths share the exact same message format — otherwise the two are
// easy to drift apart during future edits.
func cidrViolation(file string, line int, attrName, value string, err error) Violation {
	return Violation{
		Code:     "E101",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + ": invalid CIDR block \"" + value + "\" (" + err.Error() + ")",
	}
}

// validateCIDR is a thin wrapper over net/netip.ParsePrefix. Kept as a named
// helper both for the microbenchmark (BenchmarkE101_Corpus) and to make the
// intent obvious at the call site — the underlying parser handles IPv4 and
// IPv6 uniformly, so callers don't need to branch on address family.
func validateCIDR(v string) error {
	_, err := netip.ParsePrefix(v)
	return err
}

// cidrLiteralString extracts a string literal from an hclsyntax expression.
// Returns ("", false) for anything that isn't a fully-static literal — that
// includes interpolation (`${var.x}`), non-string values (`42`, `true`),
// and typed-null (`null`). The bool distinguishes "not a literal" from "an
// empty-string literal"; callers handle the empty-string case explicitly
// so an interpolation and an intentional empty placeholder produce the
// same "skip" behaviour.
//
// Structurally identical to the corpus extractor's helper of the same shape
// (bench/attr-corpus/cmd/extract/main.go) — see round-3 review discussion on
// PR #35 for the empty-string ambiguity that motivated the (string, bool)
// return.
func cidrLiteralString(e hclsyntax.Expression) (string, bool) {
	tpl, ok := e.(*hclsyntax.TemplateExpr)
	if !ok || len(tpl.Parts) != 1 {
		return "", false
	}
	lit, ok := tpl.Parts[0].(*hclsyntax.LiteralValueExpr)
	if !ok {
		return "", false
	}
	val, diags := lit.Value(nil)
	if diags.HasErrors() || val.IsNull() || !val.Type().Equals(cty.String) {
		return "", false
	}
	return val.AsString(), true
}
