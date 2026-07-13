// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// ── Terraform scope-root grammar validation ──────────────────────────────────
//
// Terraform's reference expression grammar has a fixed set of top-level scope
// roots (`var`, `local`, `data`, ...) plus provider resource-type identifiers
// which follow the `<provider>_<name>` convention. Anything else in the root
// position of a scope traversal is a probable typo:
//
//   ${vars.foo}       — typo, should be ${var.foo}
//   ${locals.foo}     — typo, should be ${local.foo}
//   ${datas.aws.foo}  — typo, should be ${data.aws.foo}
//
// This file provides ValidateScopeRoot to catch such typos on any
// scope traversal — whether it appears bare on the RHS of an
// attribute (`bucket = vars.name`) or inside a template interpolation
// (`"prefix-${vars.env}"`) — complementing the family-grammar checks
// (E101, future E20x).

// tfScopeRoots is the enumerated list of fixed Terraform top-level scope root
// names in reference expressions. Sourced from the Terraform language
// documentation: https://developer.hashicorp.com/terraform/language/expressions/references
//
// Locked list — additions require a spec review, not just a hunch.
var tfScopeRoots = map[string]struct{}{
	"var":       {}, // var.<NAME> — input variables
	"local":     {}, // local.<NAME> — locals
	"module":    {}, // module.<NAME>.<OUTPUT> — child module outputs
	"data":      {}, // data.<TYPE>.<NAME>.<ATTR> — data sources
	"ephemeral": {}, // ephemeral.<TYPE>.<NAME>.<ATTR> — ephemeral resources (Terraform 1.10+)
	"path":      {}, // path.module, path.root, path.cwd
	"terraform": {}, // terraform.workspace
	"each":      {}, // each.key, each.value — inside for_each
	"count":     {}, // count.index — inside count
	"self":      {}, // self.<ATTR> — inside precondition/postcondition
}

// isResourceTypeIdentifier reports whether name looks like a Terraform
// provider resource type or data source type. Convention:
// `<provider>_<name>` — one or more underscores, only lowercase letters and
// digits, must start with a letter, must not start or end with an underscore,
// no consecutive underscores.
//
// Not exhaustive by Terraform's grammar (which permits any identifier), but
// covers the standard provider naming convention followed by every
// mainstream provider (aws_iam_role, google_project, azurerm_virtual_network,
// kubernetes_namespace, null_resource, random_pet, ...). Rejects common
// typos like `vars` (no underscore) and `_aws_iam` (leading underscore)
// which would otherwise sneak past the fixed-list check.
func isResourceTypeIdentifier(name string) bool {
	if name == "" {
		return false
	}
	// Must start with lowercase letter (not digit, not underscore).
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	// Must end with letter or digit (not underscore).
	last := name[len(name)-1]
	if last == '_' {
		return false
	}
	seenUnderscore := false
	prevUnderscore := false
	// Loop starts at index 1: name[0] has already been validated as a
	// lowercase letter above, so re-scanning it would be redundant.
	for i := 1; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_':
			if prevUnderscore {
				return false // consecutive underscores
			}
			seenUnderscore = true
			prevUnderscore = true
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevUnderscore = false
		default:
			return false // uppercase, hyphen, dot, etc. — not a resource type
		}
	}
	return seenUnderscore
}

// scopeRootTypo maps common typos to their canonical scope root. Extracted
// as a table so future additions are a single-line change and the
// suggestion machinery stays declarative rather than sprouting per-name
// conditionals.
//
// Kept minimal: only pluralisation-style typos (the most common) plus the
// exact `datas` case which appeared during initial development. Not a
// general edit-distance mapping — a fuzzier match would risk suggesting
// bogus corrections for legitimately-unusual identifiers.
var scopeRootTypo = map[string]string{
	"vars":       "var",
	"locals":     "local",
	"modules":    "module",
	"datas":      "data",
	"paths":      "path",
	"terraforms": "terraform",
}

// ScopeRootDiag describes a scope-root validation failure — the root
// identifier of a scope traversal is neither a fixed Terraform scope root,
// a dynamic-block iterator in current scope, nor a resource-type identifier
// per convention. Carries an optional suggested correction when the typo
// matches a known pattern.
type ScopeRootDiag struct {
	Range hcl.Range // source range of the offending root identifier
	Root  string    // the unrecognised identifier
	Hint  string    // suggested correction (empty if no obvious match)
	// IsTypo is true when the root matches a known pluralisation-style
	// typo in scopeRootTypo (`vars`, `locals`, `datas`, ...). High
	// confidence — used by scopeRootViolation to pick between E009
	// (error) and W009 (warning) severity. Unknown roots without a
	// mapped hint are genuinely uncertain — we can't tell if the user
	// mistyped something we don't know about or if Terraform grew a
	// new root — so those downgrade to W009.
	IsTypo bool
}

// ValidateScopeRoot inspects the root of a ScopeTraversalExpr and returns
// a diagnostic if the root identifier is neither in tfScopeRoots, in the
// caller-supplied iterators set, nor a valid resource-type identifier.
// Returns nil for well-formed roots and for expression types that aren't
// scope traversals (function calls, binary ops, literals — those are
// checked elsewhere or not at all).
//
// The iterators parameter carries lexical scope from the caller's walk.
// Terraform's `dynamic "X" { content { ... } }` blocks introduce X (or
// the value of `iterator = ...`) as a valid scope root visible only
// inside content{}; callers should thread the active iterator set
// through their traversal and pass it here. Nil is accepted and means
// "no active iterators", equivalent to an empty map — safe for callers
// that don't do dynamic-block-aware walking.
//
// The check is deliberately narrow: only the outermost traversal root is
// examined. Nested expressions inside function calls or binary ops are
// out of scope for this first pass; a follow-up can walk the expression
// tree if needed.
func ValidateScopeRoot(expr hclsyntax.Expression, iterators map[string]struct{}) *ScopeRootDiag {
	trav, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(trav.Traversal) == 0 {
		return nil
	}
	root, ok := trav.Traversal[0].(hcl.TraverseRoot)
	if !ok {
		return nil
	}
	if _, known := tfScopeRoots[root.Name]; known {
		return nil
	}
	// Nil-safe: Go map lookup on a nil map returns the zero value
	// (bool false), so callers that don't do iterator tracking can
	// pass nil without a guard here.
	if _, isIter := iterators[root.Name]; isIter {
		return nil
	}
	if isResourceTypeIdentifier(root.Name) {
		return nil
	}
	hint := scopeRootTypo[root.Name]
	return &ScopeRootDiag{
		Range:  root.SrcRange,
		Root:   root.Name,
		Hint:   hint,
		IsTypo: hint != "",
	}
}

// scopeRootViolation packages a diagnostic for a scope-root failure
// found during expression traversal. The severity split reflects our
// confidence in the diagnosis:
//
//   - High-confidence typos (scopeRootTypo hit — e.g. `vars`, `locals`,
//     `datas`) fire E009 with severity "error". We know what the user
//     meant, so failing the build is appropriate.
//
//   - Genuinely uncertain roots (unknown to us, not a known typo, and
//     not resource-shaped) fire W009 with severity "warning". We can't
//     tell if the user mistyped something we don't recognise or if
//     Terraform introduced a new root we haven't listed — matching the
//     project's "default findings must be highly certain" contract.
//
// The message includes the offending root and, when known, a suggested
// correction. Kept alongside ScopeRootDiag since both E009 and W009 are
// emitted from the general expression walker (walkExpressions in
// checks.go) rather than from any single check family.
func scopeRootViolation(file string, diag *ScopeRootDiag) Violation {
	code := "W009"
	severity := "warning"
	msg := "unfamiliar Terraform scope root \"" + diag.Root + "\" (may be a typo or an unrecognised construct)"
	if diag.IsTypo {
		code = "E009"
		severity = "error"
		msg = "invalid Terraform scope root \"" + diag.Root + "\""
		if diag.Hint != "" {
			msg += " (did you mean \"" + diag.Hint + "\"?)"
		}
	}
	return Violation{
		Code:     code,
		Severity: severity,
		File:     file,
		Line:     diag.Range.Start.Line,
		Message:  msg,
	}
}

// isAWSBlock reports whether a top-level block (provider / resource /
// data) carries AWS applicability — i.e. attributes inside it can be
// assumed to reference AWS grammar (region codes, account IDs) by
// default. Used by the AWS-family checks (E201, E202) to gate their
// validation so they don't false-positive on cross-provider generic
// attribute names.
//
// Rules:
//
//   - provider "aws"           → true
//   - provider "OTHER"         → false (google, google-beta, cloudflare, azurerm, ...)
//   - resource "aws_*" "..."   → true
//   - resource "OTHER_*" "..." → false
//   - data "aws_*" "..."       → true
//   - data "OTHER_*" "..."     → false
//   - anything else            → false
//
// Nested blocks inside a resource/data body (e.g. `destination { ... }`)
// are handled by the caller, which inherits the parent block's AWS
// context when recursing.
func isAWSBlock(blockType string, labels []string) bool {
	if len(labels) == 0 {
		return false
	}
	switch blockType {
	case "provider":
		return labels[0] == "aws"
	case "resource", "data":
		return strings.HasPrefix(labels[0], "aws_")
	}
	return false
}
