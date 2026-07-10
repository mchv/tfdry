// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── E202: AWS account ID validation ─────────────────────────────────────────
//
// An AWS account ID is a 12-digit string identifier. AWS uses these
// identifiers as strings (with leading zeroes preserved) in ARNs, IAM
// policy documents, and cross-account references — never as integers.
//
// Attribute triggers are enumerated (Tier 1): the sole `account_id`
// attribute name, which appears as a scalar on many AWS resources
// (snapshot filters, account-scoping data sources, and cross-account
// resource references).
//
// Interpolation-aware validation (deferred): account IDs are compact
// digit sequences with no natural segment boundaries. A templated form
// like "1234${var.mid}9012" could compose to arbitrary digit
// substrings; placeholder-composed validation would produce false
// positives without adding signal. Interpolated / templated values are
// silently skipped — matches the E101/E201 policy.
//
// Zero-alloc hot path: length check (exactly 12) + branchless digit
// scan using unsigned byte wraparound. No map, no allocation.

// accountIDTriggers is the enumerated attribute-name → shape table for
// E202. Tier 1 only: the sole `account_id` attribute. Additions require
// a documented use case rather than a heuristic guess (e.g. `owner_id`,
// `caller_account_id`, `allowed_account_ids` list form).
var accountIDTriggers = map[string]cidrShape{
	"account_id": cidrShapeScalar,
}

// accountIDLength is the fixed length of an AWS account ID in decimal
// digits — 12. Exported-shape constant so the ARN check (which validates
// the account field of an ARN using the same grammar) can share the
// length rather than duplicating the magic number.
const accountIDLength = 12

// checkAccountID runs E202 over a single parsed file, returning one
// Violation per finding. Called from Run() when E202 is enabled.
func checkAccountID(f ParsedFile) []Violation {
	var violations []Violation
	walkAccountIDBlocks(f.Body, f.Name, &violations)
	return violations
}

// walkAccountIDBlocks descends into a body's attributes and child blocks.
// Skips `variable` blocks (Tier-3 exclusion, mirrors E101/E201).
func walkAccountIDBlocks(body *hclsyntax.Body, file string, violations *[]Violation) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		if accountIDTriggers[attr.Name] != cidrShapeScalar {
			continue
		}
		checkAccountIDScalar(file, attr, violations)
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" {
			continue
		}
		walkAccountIDBlocks(block.Body, file, violations)
	}
}

// checkAccountIDScalar validates a single-string account_id attribute.
// Non-template expressions (bare traversals, function calls) and
// templated / interpolated values are silently skipped — those can't be
// statically resolved to a 12-digit literal.
func checkAccountIDScalar(file string, attr *hclsyntax.Attribute, violations *[]Violation) {
	s, ok := TryLiteralString(attr.Expr)
	if !ok || s == "" {
		return
	}
	if validateAccountID(s) {
		return
	}
	*violations = append(*violations, accountIDViolation(file, attr.Expr.Range().Start.Line, attr.Name, s))
}

// validateAccountID reports whether s is a syntactically valid AWS
// account ID: exactly 12 characters, all ASCII digits '0'-'9'.
//
// Zero-alloc, branchless digit check: the length constraint short-circuits
// the common failure path in one CPU instruction; the digit loop uses the
// unsigned byte wraparound trick (`c - '0'` wraps to a large uint8 when
// c < '0', which fails the `> 9` check for c > '9' as well) to avoid a
// branch per byte. Modern compilers can keep this in registers with no
// memory access beyond the input string bytes.
func validateAccountID(s string) bool {
	if len(s) != accountIDLength {
		return false
	}
	// Branchless: (c - '0') is uint8; if c < '0' it wraps to > 9, if
	// c > '9' the subtraction is straightforward > 9. Either way, a
	// single `> 9` comparison rejects any non-digit byte.
	for i := 0; i < accountIDLength; i++ {
		if s[i]-'0' > 9 {
			return false
		}
	}
	return true
}

// accountIDViolation packages a Violation for E202.
func accountIDViolation(file string, line int, attrName, value string) Violation {
	return Violation{
		Code:     "E202",
		Severity: "error",
		File:     file,
		Line:     line,
		Message:  attrName + ": invalid AWS account ID \"" + value + "\" (must be exactly 12 digits)",
	}
}
