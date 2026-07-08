// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
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
//
// Interpolation-aware validation (2026-07-08): pure-literal CIDRs go
// through net/netip.ParsePrefix as before. Templated values are handled
// via the checker/template.go subsystem — the composed form (with each
// ${...} replaced by a "0" placeholder) is validated as a CIDR when the
// literal parts carry enough shape information to be meaningful, and
// each interpolation is separately checked for a valid Terraform scope
// root via ValidateScopeRoot (E009). See #23 for the model discussion.

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

// cidrPlaceholderNumeric is the substitution used for validation:
// interpolations become "0", which is a valid IPv4 octet, a valid IPv6
// group, and a valid /-prefix. This lets net/netip.ParsePrefix run over
// the composed form and catch any literal shape errors (missing slash,
// wrong number of octets, bad literal octet like 256, etc.).
const cidrPlaceholderNumeric = "0"

// cidrPlaceholderDisplay is the substitution used in error messages —
// human-readable, unambiguously not-a-literal, and short. Prefer this
// over the numeric form when reporting so the user sees where the
// interpolations landed in the value they wrote.
const cidrPlaceholderDisplay = "<P>"

// checkCIDR runs E101 (and, for interpolation contexts, E009) over a single
// parsed file, returning one Violation per finding. E009 diagnostics are
// only emitted when the user has E009 enabled — E101 gates the outer
// dispatch, so a user with only --checks=E101 sees CIDR format errors
// (including composed-form errors for templated values) but no scope-root
// diagnostics.
func checkCIDR(f ParsedFile, checks CheckSet) []Violation {
	var violations []Violation
	walkCIDRBlocks(f.Body, f.Name, checks, &violations)
	return violations
}

// walkCIDRBlocks descends into a body's attributes and child blocks. It skips
// `variable` blocks in their entirety because `default` inside them is a
// Tier 3 exclusion (the variable's declared type is not knowable to an
// offline checker, so a bad `default` cannot be distinguished from a
// deliberately-loose default that callers always override).
func walkCIDRBlocks(body *hclsyntax.Body, file string, checks CheckSet, violations *[]Violation) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		switch s := cidrTriggers[attr.Name]; s {
		case cidrShapeScalar:
			checkCIDRScalar(file, attr, checks, violations)
		case cidrShapeList:
			checkCIDRList(file, attr, checks, violations)
		case cidrShapeNone:
			// Zero value: attribute is not on the trigger list. Explicit
			// case (rather than an implicit fall-through) documents the
			// intent and keeps the exhaustive linter honest — if a new
			// cidrShape value is added later, the linter will surface
			// this switch as needing an update.
		default:
			// Defence-in-depth for unexported enum-like types, matching
			// the pattern used for schemaKind switches in modules.go and
			// the guidance documented in .golangci.yml. Reachable only if
			// someone constructs an out-of-range cidrShape value directly
			// (bypassing the enumerated constants) — panic makes the
			// mistake loud at test time rather than silently swallowing
			// a new shape as "unrecognised, skip attribute".
			panic(fmt.Sprintf("unrecognised cidrShape: %d", s))
		}
	}
	for _, block := range body.Blocks {
		if block.Type == "variable" {
			continue
		}
		walkCIDRBlocks(block.Body, file, checks, violations)
	}
}

// checkCIDRScalar validates a single-string CIDR attribute using the
// template subsystem. Non-template expressions (e.g. `cidr_block = var.foo`
// as a bare traversal, or `cidr_block = 42` as a number) are silently
// skipped — statically-unresolvable references can't be validated as
// CIDRs, and a bare `var.foo` traversal is Terraform's own type-check
// concern, not ours.
//
// Fast path: pure-literal values (the overwhelming majority on real
// modules — every hardcoded CIDR literal falls here) go through
// TryLiteralString and validateCIDR directly, without allocating a
// []TemplatePart slice. The interpolation path only runs on templated
// values, which are the minority.
func checkCIDRScalar(file string, attr *hclsyntax.Attribute, checks CheckSet, violations *[]Violation) {
	if s, ok := TryLiteralString(attr.Expr); ok {
		if s == "" {
			return
		}
		if err := validateCIDR(s); err != nil {
			*violations = append(*violations, cidrViolation(file, attr.Expr.Range().Start.Line, attr.Name, s, err))
		}
		return
	}
	parts := SplitTemplate(attr.Expr)
	if parts == nil {
		return
	}
	validateCIDRTemplate(file, attr.Expr.Range().Start.Line, attr.Name, parts, checks, violations)
}

// checkCIDRList validates each element of a list-typed CIDR attribute
// independently. A single bad element produces one violation without
// affecting the sibling elements — one bad CIDR in a security-group
// ingress list should not silence findings on the other entries.
//
// Same fast-path optimisation as checkCIDRScalar: pure-literal elements
// avoid the []TemplatePart allocation.
func checkCIDRList(file string, attr *hclsyntax.Attribute, checks CheckSet, violations *[]Violation) {
	tuple, ok := attr.Expr.(*hclsyntax.TupleConsExpr)
	if !ok {
		// Interpolated single-value or traversal (e.g. cidr_blocks = var.foo).
		// Not statically checkable; skip rather than emit a spurious error.
		return
	}
	for _, elem := range tuple.Exprs {
		if s, ok := TryLiteralString(elem); ok {
			if s == "" {
				continue
			}
			if err := validateCIDR(s); err != nil {
				*violations = append(*violations, cidrViolation(file, elem.Range().Start.Line, attr.Name, s, err))
			}
			continue
		}
		parts := SplitTemplate(elem)
		if parts == nil {
			continue
		}
		validateCIDRTemplate(file, elem.Range().Start.Line, attr.Name, parts, checks, violations)
	}
}

// validateCIDRTemplate runs the two-part validation for one CIDR-shaped
// template value:
//
//  1. Format check — validates the composed (placeholder-substituted) form
//     as a CIDR via net/netip.ParsePrefix. Pure literals always run the
//     check; interpolated forms run it only when the literal parts carry
//     enough shape information to make the check meaningful (see
//     cidrHasEnoughShape). Emits E101 on format failure.
//  2. Scope-root check — for each interpolation part, validates that its
//     root identifier is a known Terraform scope root or a resource-type
//     identifier. Emits E009 on scope-root failure (only when the user
//     has E009 enabled).
//
// Both checks run independently: a value can produce zero, one, or both
// kinds of diagnostics. The two checks answer different questions and
// their findings are complementary rather than redundant.
func validateCIDRTemplate(file string, line int, attrName string, parts []TemplatePart, checks CheckSet, violations *[]Violation) {
	// Format check (E101).
	if IsAllLiteral(parts) {
		v := LiteralString(parts)
		if v != "" {
			if err := validateCIDR(v); err != nil {
				*violations = append(*violations, cidrViolation(file, line, attrName, v, err))
			}
		}
	} else if cidrHasEnoughShape(parts) {
		composed := Compose(parts, cidrPlaceholderNumeric)
		if err := validateCIDR(composed); err != nil {
			display := Compose(parts, cidrPlaceholderDisplay)
			*violations = append(*violations, cidrViolation(file, line, attrName, display, err))
		}
	}

	// Scope-root check (E009). Gated separately from E101 — a user who
	// runs with --checks=E101,-E009 gets format errors but no scope-root
	// diagnostics.
	if !checks.Enabled("E009") {
		return
	}
	for _, p := range parts {
		if !p.IsInterp() {
			continue
		}
		if diag := ValidateScopeRoot(p.Interp); diag != nil {
			*violations = append(*violations, scopeRootViolation(file, diag))
		}
	}
}

// cidrHasEnoughShape reports whether the literal parts of a templated CIDR
// value carry enough structure for the composed-form check to produce
// reliable results.
//
// Rationale: an interpolation can substitute a single octet ("10"), or
// multiple octets ("10.0"), or the whole IP address, or the whole CIDR
// including prefix. Only when the literal parts commit to the canonical
// IPv4 octet count is a placeholder substitution meaningful — otherwise
// the composed form is guessing at the number of segments.
//
// The check requires all of:
//
//   - A `/` (CIDR prefix separator) — signals the user has committed to
//     the CIDR shape with an explicit prefix boundary.
//   - Exactly 3 dots (`.`) in the literal parts before the `/` — pins the
//     IPv4 octet count at 4, leaving each interpolation as a single-octet
//     placeholder. `10.0.${var}.0/24` (3 literal dots) is checked;
//     `${var}.0.0/16` (2 literal dots — interp could be "10.0") is not.
//
// IPv6 support is deliberately deferred: the `::` compression rule makes
// literal-colon-counting ambiguous without a full parse. Templated IPv6
// CIDRs fall through to "not checked". A follow-up can add IPv6-aware
// shape detection when the ambiguity is worth the parser complexity.
func cidrHasEnoughShape(parts []TemplatePart) bool {
	litSum := LiteralString(parts)
	slashIdx := strings.Index(litSum, "/")
	if slashIdx < 0 {
		return false
	}
	ipPart := litSum[:slashIdx]
	return strings.Count(ipPart, ".") == 3
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

// scopeRootViolation packages an E009 diagnostic for a scope-root failure
// in a CIDR-attribute interpolation. The message includes the offending
// root and, when known, a suggested correction.
func scopeRootViolation(file string, diag *ScopeRootDiag) Violation {
	msg := "invalid Terraform scope root \"" + diag.Root + "\""
	if diag.Hint != "" {
		msg += " (did you mean \"" + diag.Hint + "\"?)"
	}
	return Violation{
		Code:     "E009",
		Severity: "error",
		File:     file,
		Line:     diag.Range.Start.Line,
		Message:  msg,
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
