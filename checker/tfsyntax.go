// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker

import (
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
// This file provides ValidateScopeRoot to catch such typos in interpolation
// expressions, complementing the family-grammar checks (E101, future E20x).

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
	for i := 0; i < len(name); i++ {
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
// identifier of an interpolation is neither a fixed Terraform scope root
// nor a resource-type identifier per convention. Carries an optional
// suggested correction when the typo matches a known pattern.
type ScopeRootDiag struct {
	Range hcl.Range // source range of the offending root identifier
	Root  string    // the unrecognised identifier
	Hint  string    // suggested correction (empty if no obvious match)
}

// ValidateScopeRoot inspects the root of a ScopeTraversalExpr and returns
// a diagnostic if the root identifier is neither in tfScopeRoots nor a
// valid resource-type identifier. Returns nil for well-formed roots and
// for expression types that aren't scope traversals (function calls,
// binary ops, literals — those are checked elsewhere or not at all).
//
// The check is deliberately narrow: only the outermost traversal root is
// examined. Nested expressions inside function calls or binary ops are
// out of scope for this first pass; a follow-up can walk the expression
// tree if needed.
func ValidateScopeRoot(expr hclsyntax.Expression) *ScopeRootDiag {
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
	if isResourceTypeIdentifier(root.Name) {
		return nil
	}
	return &ScopeRootDiag{
		Range: root.SrcRange,
		Root:  root.Name,
		Hint:  scopeRootTypo[root.Name],
	}
}
