// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ── HCL template splitting ───────────────────────────────────────────────────
//
// Terraform quoted-string attribute values parse as HCL template
// expressions. A template is a sequence of parts, each of which is either
// a literal string chunk or an interpolation expression (`${...}`).
// Non-quoted-string right-hand sides — bare traversals (`cidr_block =
// var.foo`), function calls, numeric or boolean literals — are not
// templates and produce a different AST type; SplitTemplate below returns
// nil for those, so callers can uniformly treat "template result was nil"
// as "not analysable as a string literal, skip". This file provides
// SplitTemplate to expose the template structure to family checks (E101,
// future E20x) so they can validate both:
//
//   1. The composed shape after placeholder substitution — a "canonical"
//      form of the value with every interpolation replaced by a
//      family-defined placeholder token. E.g. for CIDR, `10.0.<P>.0/24`
//      substitutes to `10.0.0.0/24` and validates as a normal CIDR.
//
//   2. The Terraform grammar of each interpolation individually — see
//      ValidateScopeRoot in tfsyntax.go for the scope-root check that
//      catches typos like `${vars.foo}`.

// TemplatePart is one part of a parsed HCL template expression. Exactly one
// of Literal / Interp is meaningful per part:
//
//   - A literal part carries its unescaped string content in Literal;
//     Interp is nil.
//   - An interpolation part carries its expression AST in Interp;
//     Literal is the empty string.
//
// Callers should use IsInterp() to distinguish rather than reading the
// zero-value of one field — a legitimate literal part can be the empty
// string ("" between two consecutive interpolations), so a zero Literal
// does not by itself imply an interpolation.
type TemplatePart struct {
	Literal string               // literal chunk (meaningful only when Interp == nil)
	Interp  hclsyntax.Expression // interpolation expression (nil for literal parts)
}

// IsInterp reports whether this part is an interpolation. Preferred over
// checking Interp != nil at call sites because it documents intent.
func (p TemplatePart) IsInterp() bool {
	return p.Interp != nil
}

// TryLiteralString extracts the literal string from a single-part
// TemplateExpr, returning ("", false) if the expression is not a pure
// string literal. Zero-alloc fast path for callers that only care about
// the pure-literal case — no []TemplatePart slice is allocated. Callers
// that need to distinguish literal parts from interpolation parts, or
// that need to Compose a placeholder-substituted form, should fall
// through to SplitTemplate on the "not pure literal" case:
//
//	if s, ok := TryLiteralString(expr); ok {
//		// fast path — validate the literal string directly
//	} else if parts := SplitTemplate(expr); parts != nil {
//		// slow path — handle interpolations
//	}
//
// The empty-string case ("", true) is legitimate: a literal empty string
// is a valid template expression whose result is the empty string. The
// bool disambiguates from ("", false) meaning "not a pure literal".
func TryLiteralString(expr hclsyntax.Expression) (string, bool) {
	tpl, ok := expr.(*hclsyntax.TemplateExpr)
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

// SplitTemplate splits an HCL string-valued expression into its literal and
// interpolation parts. Returns nil for expressions that are not a template
// (e.g. a bare traversal `var.foo`, a number, or a boolean) — callers
// should treat nil as "not statically analysable as a string" and skip.
//
// Handles both TemplateExpr (the general case, multi-part) and
// TemplateWrapExpr (the compact case for `"${expr}"`).
//
// Templates containing a `%{for ...}...%{endfor}` directive return nil.
// HCL models these directives with a TemplateJoinExpr part whose semantics
// (a variable-iteration join of a tuple's values) don't fit the flat
// literal/interpolation part model — the composed shape depends on the
// iterated tuple's length, so placeholder substitution would fabricate a
// value whose validity can't be reasoned about statically.
//
// Templates containing a `%{if ...}...%{endif}` directive are, by contrast,
// modelled with a plain ConditionalExpr part — structurally identical to a
// regular `${x ? y : z}` interpolation. Since we can't distinguish the two
// at the AST level, SplitTemplate accepts the ConditionalExpr as an opaque
// interpolation part; placeholder substitution over it is no worse than
// over any other value-producing interpolation.
//
// A malformed literal part (non-string cty value, HCL diagnostic errors)
// causes SplitTemplate to return nil for the whole expression. This is
// conservative: rather than emit a half-parsed template with a phantom
// empty literal, the caller sees "not analysable" and skips. HCL parse
// errors on the containing file would surface via the file-level parser
// diagnostics, not here.
func SplitTemplate(expr hclsyntax.Expression) []TemplatePart {
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr:
		// Reject templates that contain a directive-join (`%{for}` block)
		// as an early exit before allocating the parts slice.
		for _, sub := range e.Parts {
			if _, isJoin := sub.(*hclsyntax.TemplateJoinExpr); isJoin {
				return nil
			}
		}
		parts := make([]TemplatePart, 0, len(e.Parts))
		for _, sub := range e.Parts {
			lit, isLit := sub.(*hclsyntax.LiteralValueExpr)
			if !isLit {
				parts = append(parts, TemplatePart{Interp: sub})
				continue
			}
			val, diags := lit.Value(nil)
			if diags.HasErrors() || val.IsNull() || !val.Type().Equals(cty.String) {
				return nil
			}
			parts = append(parts, TemplatePart{Literal: val.AsString()})
		}
		return parts
	case *hclsyntax.TemplateWrapExpr:
		// `"${expr}"` — the whole string is one interpolation, no literal
		// parts. HCL emits this compact form as an optimisation over the
		// equivalent TemplateExpr{Parts: [expr]}. Defensive guard for the
		// (syntactically unusual) case where the wrapped expression is a
		// TemplateJoinExpr — matches the TemplateExpr rejection above so
		// the two entry paths stay consistent.
		if _, isJoin := e.Wrapped.(*hclsyntax.TemplateJoinExpr); isJoin {
			return nil
		}
		return []TemplatePart{{Interp: e.Wrapped}}
	default:
		return nil
	}
}

// IsAllLiteral reports whether every part is a literal (no interpolations).
// A convenient predicate for the fast path in family checks: pure literals
// go straight to the existing grammar validator without the placeholder-
// composition step.
func IsAllLiteral(parts []TemplatePart) bool {
	for _, p := range parts {
		if p.IsInterp() {
			return false
		}
	}
	return true
}

// LiteralString returns the concatenation of every literal part in parts.
// Interpolation parts contribute nothing — LiteralString is only meaningful
// when IsAllLiteral(parts) is true, in which case it returns the exact
// unescaped string that the source HCL denotes. Kept as a two-step API
// (IsAllLiteral + LiteralString) rather than a combined bool+string return
// because callers frequently want to branch on the boolean and reach for
// the string only in the literal branch.
func LiteralString(parts []TemplatePart) string {
	var b strings.Builder
	for _, p := range parts {
		if !p.IsInterp() {
			b.WriteString(p.Literal)
		}
	}
	return b.String()
}

// Compose builds a placeholder-substituted string from parts. Every
// interpolation is replaced by placeholder; literal parts are copied
// verbatim. Family checks use this to construct a canonical shape for
// grammar validation:
//
//	parts = SplitTemplate(`"10.0.${var.subnet}.0/24"`)
//	Compose(parts, "0")  // "10.0.0.0/24"  — validates as normal CIDR
//	Compose(parts, "<P>") // "10.0.<P>.0/24" — human-readable form for errors
//
// The placeholder is caller-defined so each family can pick a token that
// satisfies its grammar in the relevant field positions (a digit for
// numeric fields, a sentinel for opaque resource paths, etc.).
func Compose(parts []TemplatePart, placeholder string) string {
	// Pre-size the builder to the exact composed length. strings.Builder
	// otherwise grows by doubling (8 → 16 → 32 → ... 4 allocs for a
	// 60-byte ARN); one Grow call up front cuts that to a single
	// allocation of the correct size.
	var n int
	for _, p := range parts {
		if p.IsInterp() {
			n += len(placeholder)
			continue
		}
		n += len(p.Literal)
	}
	var b strings.Builder
	b.Grow(n)
	for _, p := range parts {
		if p.IsInterp() {
			b.WriteString(placeholder)
			continue
		}
		b.WriteString(p.Literal)
	}
	return b.String()
}
