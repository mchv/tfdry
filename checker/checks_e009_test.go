// Copyright 2026 Mariot Chauvin
// SPDX-License-Identifier: Apache-2.0

package checker_test

import (
	"context"
	"slices"
	"testing"

	"github.com/mchv/tfdry/checker"
)

// ── E009 broadening — fires from walkExpressions, not just CIDR context ─────

// TestE009_NonCIDRAttribute_FiresE009 verifies E009 catches scope-root
// typos in attributes that are not CIDR-triggering. Prior to round 5,
// E009 emitted only from checkCIDR, so a typo in e.g. a resource `name`
// attribute went undetected. The registry summary implied broader
// coverage than the implementation delivered.
func TestE009_NonCIDRAttribute_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "prefix-${vars.env}"
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on scope-root typo 'vars' in non-CIDR attribute, got: %v", codes(vs))
	}
}

// TestE009_VariableDefault_FiresE009 verifies E009 fires on typos inside
// variable default values — which walkCIDRBlocks previously skipped by
// design (variable defaults are Tier-3-excluded for CIDR). Broadening
// E009 to the general expression walker reaches these positions.
func TestE009_VariableDefault_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
variable "env_tag" {
  default = "prefix-${vars.env}"
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on scope-root typo 'vars' inside variable default, got: %v", codes(vs))
	}
}

// TestE009_BareTraversal_FiresE009 verifies E009 fires on a bare
// (non-template) traversal with an unknown root. Prior to round 5, E009
// went through SplitTemplate which returns nil for bare traversals, so
// this case was silently missed even in CIDR context.
func TestE009_BareTraversal_FiresE009(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = vars.name
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("expected E009 on bare traversal 'vars.name', got: %v", codes(vs))
	}
}

// ── Dynamic-block iterator scoping ──────────────────────────────────────────

// TestE009_DynamicBlockDefaultIterator_NoFalsePositive verifies that a
// reference to the default iterator name (the dynamic block's label)
// inside its content{} block does not fire E009. Reported by Gemini
// as a HIGH-priority false positive: `dynamic "ingress"` introduces
// `ingress` as a valid scope root inside the content block, but the
// naked ValidateScopeRoot allowlist would reject it.
func TestE009_DynamicBlockDefaultIterator_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    content {
      cidr_blocks = [ingress.value]
      description = "from ${ingress.key}"
    }
  }
}
`,
	})
	assertNoScopeRootDiag(t, vs, "dynamic block iterator 'ingress'")
}

// TestE009_DynamicBlockCustomIterator_NoFalsePositive verifies that a
// dynamic block with an explicit `iterator = X` override uses X (not
// the label) as the in-scope iterator name.
func TestE009_DynamicBlockCustomIterator_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    iterator = rule
    content {
      cidr_blocks = [rule.value]
      description = "from ${rule.key}"
    }
  }
}
`,
	})
	assertNoScopeRootDiag(t, vs, "custom iterator 'rule'")
}

// TestW009_DynamicBlockOutsideScope_StillFires verifies that the
// iterator name is only in scope inside the dynamic block's content{}.
// A reference to `ingress.value` in a sibling attribute or outside the
// block must still produce a diagnostic — otherwise the iterator scope
// is leaking. As of the E009/W009 hierarchy split, `ingress` here is a
// genuinely-uncertain root (no scopeRootTypo hint, no resource-type
// shape), so the diagnostic is W009 (warning), not E009 (error).
// Renamed from TestE009_DynamicBlockOutsideScope_StillFires to reflect
// the new severity.
func TestW009_DynamicBlockOutsideScope_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  # ingress.value is NOT in scope here — the dynamic block below
  # introduces the iterator only inside its content{}.
  description = "sg with ${ingress.value}"
  dynamic "ingress" {
    for_each = var.cidrs
    content {
      cidr_blocks = [ingress.value]
    }
  }
}
`,
	})
	if !hasCode(vs, "W009") {
		t.Fatalf("expected W009 on 'ingress' used outside dynamic block content, got: %v", codes(vs))
	}
	if hasCode(vs, "E009") {
		t.Fatalf("'ingress' is a genuinely-uncertain root, not a known typo — should fire W009 not E009, got: %v", codes(vs))
	}
}

// TestE009_DynamicBlockNested_BothIteratorsInScope verifies that nested
// dynamic blocks stack their iterators — the inner content{} sees both
// the inner and outer iterator names.
func TestE009_DynamicBlockNested_BothIteratorsInScope(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.groups
    content {
      dynamic "cidr_blocks" {
        for_each = ingress.value.cidrs
        content {
          # Both ingress.key (outer) and cidr_blocks.value (inner)
          # must be recognised as iterators here.
          description = "${ingress.key}: ${cidr_blocks.value}"
        }
      }
    }
  }
}
`,
	})
	assertNoScopeRootDiag(t, vs, "nested dynamic-block iterators")
}

// TestE009_DynamicBlockIteratorAttribute_NotFlagged verifies that the
// `iterator = X` attribute itself does not fire E009. X is a bare
// identifier that Terraform interprets as the iterator name, not a
// reference to an existing scope root. It's syntactically a
// ScopeTraversalExpr in HCL but semantically a declaration.
//
// This is a corner case: without a special case, walkExpressions would
// visit the `rule` traversal and E009 would flag it as unknown (it's
// not in tfScopeRoots and not a resource-type identifier).
func TestE009_DynamicBlockIteratorAttribute_NotFlagged(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_security_group" "x" {
  dynamic "ingress" {
    for_each = var.cidrs
    iterator = rule
    content {
      cidr_blocks = [rule.value]
    }
  }
}
`,
	})
	// The iterator attribute value ('rule') must not itself be flagged.
	// If E009 or W009 fires with root "rule" here, the iterator
	// declaration is being mis-interpreted as a reference.
	assertNoScopeRootDiag(t, vs, "iterator declaration 'rule'")
}

// ── Regression guards for the CIDR-context tests already in cidr_test.go ────
// Under the new architecture (E009 emitted from walkExpressions, not from
// checkCIDR), the E009-only mode must still fire on CIDR-attribute
// interpolations. Kept lightweight — the primary coverage is in cidr_test.go.

// TestE009_CIDRContext_StillFiresAfterMove verifies that moving E009
// emission out of checkCIDR into walkExpressions did not regress the
// original CIDR-context detection. Same fixture as
// TestE009_InterpolatedInvalidScopeRoot_ViolationE009 in cidr_test.go.
func TestE009_CIDRContext_StillFiresAfterMove(t *testing.T) {
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
		t.Fatalf("E009 must still fire in CIDR context after move, got: %v", codes(vs))
	}
}

// ── ForExpr scope tracking (fix for merged-code bug) ────────────────────────

// TestE009_ForExprValueVar_NoFalsePositive verifies that the value
// variable of a tuple for-expression is in scope inside the value
// expression, and does NOT fire E009 as an unknown scope root.
//
// The bug: walkExpressions previously used hclsyntax.VisitAll on
// attribute expressions, which visits every descendant with the
// enclosing scope. Inside `[for s in var.names : upper(s)]`, the
// traversal `s` was seen without `s` being in the iterators map, so
// E009 flagged it as an unknown root.
//
// The fix uses hclsyntax.Walk with a scoped Walker that responds to
// the ChildScope nodes HCL synthesises around ForExpr's KeyExpr /
// ValExpr / CondExpr — pushing KeyVar and ValVar into scope on entry
// and popping on exit.
func TestE009_ForExprValueVar_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  upper_names = [for s in var.names : upper(s)]
}
`,
	})
	assertNoScopeRootDiag(t, vs, "for-expression value var 's'")
}

// TestE009_ForExprKeyValueVars_NoFalsePositive verifies that BOTH the
// key and value variables of an object for-expression are in scope
// inside the produced key and value expressions. Covers the `{for K,
// V in COLL : K => V.name}` shape.
func TestE009_ForExprKeyValueVars_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  name_by_key = {for k, v in var.items : k => v.name}
}
`,
	})
	assertNoScopeRootDiag(t, vs, "for-expression key/value vars 'k'/'v'")
}

// TestE009_ForExprIfCondition_NoFalsePositive verifies that the
// iterator var is in scope inside the `if` clause of a for-expression.
// HCL synthesises a ChildScope for CondExpr too, so if the walker
// mishandles that node the guard fires wrongly.
func TestE009_ForExprIfCondition_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  non_empty = [for s in var.names : s if s != ""]
}
`,
	})
	assertNoScopeRootDiag(t, vs, "for-expression iterator 's' in if-condition")
}

// TestE009_ForExprCollectionTypo_StillFires guards the scope
// boundary: the collection expression is evaluated BEFORE the
// iterator vars are bound, so a scope-root typo inside CollExpr
// must still fire E009. Otherwise the fix would over-relax and
// stop catching legitimate typos.
func TestE009_ForExprCollectionTypo_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  bad = [for s in vars.names : upper(s)]
}
`,
	})
	if !hasCode(vs, "E009") {
		t.Fatalf("scope-root typo 'vars' in for-expression collection must still fire E009, got: %v", codes(vs))
	}
}

// TestW009_ForExprAfterEnd_StillFires verifies the iterator scope is
// popped after the for-expression: a reference to the iterator name
// in an adjacent attribute must produce a diagnostic (the name is
// not package-scoped, only expression-scoped). Under the E009/W009
// hierarchy split, `s` here is a genuinely-uncertain root (no
// scopeRootTypo hint, no resource-type shape), so the diagnostic is
// W009 (warning), not E009 (error).
func TestW009_ForExprAfterEnd_StillFires(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  inside  = [for s in var.names : upper(s)]
  outside = s
}
`,
	})
	if !hasCode(vs, "W009") {
		t.Fatalf("iterator 's' used outside its for-expression must fire W009, got: %v", codes(vs))
	}
	if hasCode(vs, "E009") {
		t.Fatalf("'s' is a genuinely-uncertain root — should fire W009 not E009, got: %v", codes(vs))
	}
}

// TestE009_NestedForExpr_BothIteratorsInScope covers scope-stack
// discipline. Inside an inner for-expression, both the outer and
// inner iterator names must be visible. If the walker replaces
// (rather than augments) the scope on entry, the outer name would
// disappear and cause a false E009.
func TestE009_NestedForExpr_BothIteratorsInScope(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
locals {
  matrix = [for row in var.rows : [for cell in row : cell + row[0]]]
}
`,
	})
	assertNoScopeRootDiag(t, vs, "nested for-expression iterators")
}

// ── ephemeral root (Terraform 1.10+) ────────────────────────────────────────

// TestE009_EphemeralRoot_NoFalsePositive verifies that the
// `ephemeral.*` scope root (introduced in Terraform 1.10 for ephemeral
// resources like `ephemeral "random_password" "..."`) is recognised
// and does not fire E009 as an unknown root.
//
// Bug: tfScopeRoots was frozen at Terraform 1.x baseline (var, local,
// module, data, path, terraform, each, count, self) and had not been
// updated when Terraform 1.10 added `ephemeral`. Any Terraform config
// referencing `ephemeral.<TYPE>.<NAME>.<ATTR>` would fire E009.
func TestE009_EphemeralRoot_NoFalsePositive(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
ephemeral "random_password" "db_password" {
  length = 32
}

resource "aws_db_instance" "x" {
  password = ephemeral.random_password.db_password.result
}
`,
	})
	assertNoScopeRootDiag(t, vs, "ephemeral.* root")
}

// ── E009/W009 hierarchy split (round 2: conservatism) ───────────────────────

// TestE009_KnownTypo_FiresAsError verifies the E009 side of the
// hierarchy: high-confidence typos (roots that appear in the
// scopeRootTypo table with a mapped correction hint) fire E009 as an
// error — these are the highest-confidence diagnostics because we
// know what the user meant.
//
// Covers all six known typos: vars, locals, modules, datas, paths,
// terraforms. Each has a documented hint pointing at the correct root.
func TestE009_KnownTypo_FiresAsError(t *testing.T) {
	cases := []struct{ typo, hint string }{
		{"vars", "var"},
		{"locals", "local"},
		{"modules", "module"},
		{"datas", "data"},
		{"paths", "path"},
		{"terraforms", "terraform"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.typo, func(t *testing.T) {
			t.Parallel()
			vs := run(t, map[string]string{
				"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "prefix-${` + tc.typo + `.env}"
}
`,
			})
			if !hasCode(vs, "E009") {
				t.Fatalf("known typo %q must fire E009 (error), got: %v", tc.typo, codes(vs))
			}
			if hasCode(vs, "W009") {
				t.Fatalf("known typo %q should not also fire W009 (it's a high-confidence error, not an uncertain warning), got: %v", tc.typo, codes(vs))
			}
		})
	}
}

// TestW009_UnknownRootWithoutHint_FiresAsWarning verifies the W009
// side of the hierarchy: a scope root that's neither in the known-roots
// table, nor an iterator, nor a resource-type identifier, nor a known
// typo (no scopeRootTypo hint) is genuinely uncertain. We can't tell
// if the user typoed something we don't know about, or if Terraform
// grew a new top-level root we haven't listed. Downgraded from E009
// error to W009 warning: the signal is preserved but does not fail
// the build.
//
// This is the round-2 refinement per the reviewer's suggestion: "a
// default finding should be highly certain". W009 keeps the diagnostic
// visible without staking it as a hard failure.
func TestW009_UnknownRootWithoutHint_FiresAsWarning(t *testing.T) {
	// `mynewthing` isn't in tfScopeRoots, not an iterator, not a
	// resource-type identifier shape (no underscore), and not in
	// scopeRootTypo (no hint). Under the round-2 hierarchy this is a
	// W009 warning, not an E009 error.
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "prefix-${mynewthing.env}"
}
`,
	})
	if !hasCode(vs, "W009") {
		t.Fatalf("genuinely-uncertain root 'mynewthing' must fire W009 (warning), got: %v", codes(vs))
	}
	if hasCode(vs, "E009") {
		t.Fatalf("'mynewthing' has no hint — should fire W009 not E009 (avoid default-error false positives on unfamiliar roots), got: %v", codes(vs))
	}
}

// TestW009_HasWarningSeverity verifies W009 diagnostics carry the
// "warning" severity, matching W001's precedent. Combined with the
// existing exit-code invariant (only errors trigger exit 1), this
// means W009 doesn't fail CI on unfamiliar-but-plausible roots.
func TestW009_HasWarningSeverity(t *testing.T) {
	vs := run(t, map[string]string{
		"main.tf": `
resource "aws_s3_bucket" "b" {
  bucket = "prefix-${mynewthing.env}"
}
`,
	})
	for _, v := range vs {
		if v.Code == "W009" {
			if v.Severity != "warning" {
				t.Fatalf("W009 must have severity 'warning' (matches W001 precedent), got %q", v.Severity)
			}
			return
		}
	}
	t.Fatalf("expected at least one W009 violation, got codes: %v", codes(vs))
}
